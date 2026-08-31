package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

// fakeSub2API 模拟 sub2api 管理端，用于端到端验证整条调度链路。
type fakeSub2API struct {
	mu sync.Mutex

	// probeFatal 中的账号在测试接口上返回致命错误。
	probeFatal map[int64]bool

	updates     map[int64]map[string]any
	nameUpdates map[int64]int
	schedulable map[int64]bool
	clearedErr  map[int64]bool
	hidden      map[int64]bool

	// failWrites 中的账号，其写操作（PUT / schedulable / DELETE）返回 500，
	// 用来验证写回失败后的状态一致性。
	failWrites map[int64]bool
	deletes    map[int64]int

	// status 覆盖账号列表接口回显的状态，模拟在 sub2api 后台停用账号。
	status map[int64]string

	// probeResults 指定某账号的探测结果类型（限流等）。
	probeResults map[int64]probeResult

	// rateLimitReset 模拟 sub2api 自己写下的限流窗口结束时间。
	//
	// 这是网站「此刻会不会把请求发给这个账号」的权威依据，Guardian 探测不出来，
	// 只能从账号列表接口读。有它才能测出「探测成功但上游仍在限流」这种情形。
	rateLimitReset map[int64]time.Time

	// upstreamMultipliers 模拟新版 Sub2API 原生上游计费探测的当前有效倍率。
	upstreamMultipliers map[int64]float64
	credentials         map[int64]map[string]any
	exportCount         int
}

// probeResult 是假 sub2api 可以返回的探测结果类型。
type probeResult int

const (
	probeOK    probeResult = iota // 正常返回
	probeQuota                    // 429 限流 / 额度耗尽
)

func newFakeSub2API() *fakeSub2API {
	return &fakeSub2API{
		probeFatal:          map[int64]bool{},
		updates:             map[int64]map[string]any{},
		nameUpdates:         map[int64]int{},
		schedulable:         map[int64]bool{},
		clearedErr:          map[int64]bool{},
		hidden:              map[int64]bool{},
		failWrites:          map[int64]bool{},
		deletes:             map[int64]int{},
		status:              map[int64]string{},
		probeResults:        map[int64]probeResult{},
		rateLimitReset:      map[int64]time.Time{},
		upstreamMultipliers: map[int64]float64{},
		credentials:         map[int64]map[string]any{},
	}
}

func (f *fakeSub2API) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/admin/groups/all", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		writeEnvelope(w, []map[string]any{
			{"id": 1, "name": "主分组", "platform": "anthropic", "status": "active", "rate_multiplier": 2.0},
		})
	})

	mux.HandleFunc("/api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		all := []map[string]any{
			{
				"id": 101, "name": "健康渠道", "platform": "anthropic", "type": "apikey",
				"status": f.statusNow(101), "schedulable": f.schedulableNow(101), "priority": 10, "concurrency": 5,
				"rate_multiplier": 1.0, "group_ids": []int64{1},
				"rate_limit_reset_at": f.rateLimitResetOf(101),
			},
			{
				"id": 102, "name": "问题渠道", "platform": "anthropic", "type": "apikey",
				"status": f.statusNow(102), "schedulable": f.schedulableNow(102), "priority": 10, "concurrency": 5,
				"rate_multiplier": 3.0, "group_ids": []int64{1},
				"rate_limit_reset_at": f.rateLimitResetOf(102),
			},
		}
		items := make([]map[string]any, 0, len(all))
		for _, item := range all {
			if !f.isHidden(int64(item["id"].(int))) {
				items = append(items, item)
			}
		}
		writeEnvelope(w, map[string]any{
			"items": items, "total": len(items), "page": 1, "page_size": 200, "pages": 1,
		})
	})

	// 运维监控关闭：引擎应降级为纯探针模式而不是报错。
	mux.HandleFunc("/api/v1/admin/ops/requests", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 404, "message": "Ops monitoring is disabled",
		})
	})

	mux.HandleFunc("/api/v1/admin/accounts/upstream-billing-probe/batch", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			AccountIDs []int64 `json:"account_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		results := make([]map[string]any, 0, len(request.AccountIDs))
		for _, accountID := range request.AccountIDs {
			value := f.upstreamMultiplier(accountID)
			results = append(results, map[string]any{
				"account_id": accountID,
				"snapshot": map[string]any{
					"status": "ok",
					"data": map[string]any{
						"billing_scope":             "token",
						"resolved_rate_multiplier":  value,
						"effective_rate_multiplier": value,
					},
				},
			})
		}
		writeEnvelope(w, map[string]any{"results": results})
	})

	mux.HandleFunc("/api/v1/admin/accounts/data", func(w http.ResponseWriter, r *http.Request) {
		var accountID int64
		_, _ = fmt.Sscanf(r.URL.Query().Get("ids"), "%d", &accountID)
		f.mu.Lock()
		f.exportCount++
		credentials := f.credentials[accountID]
		f.mu.Unlock()
		if credentials == nil {
			writeEnvelope(w, map[string]any{"accounts": []any{}})
			return
		}
		writeEnvelope(w, map[string]any{"accounts": []map[string]any{{
			"name": "测试渠道", "type": "api_key", "credentials": credentials,
		}}})
	})

	mux.HandleFunc("/api/v1/admin/accounts/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/")
		parts := strings.Split(rest, "/")
		var accountID int64
		if _, err := fmt.Sscanf(parts[0], "%d", &accountID); err != nil {
			http.NotFound(w, r)
			return
		}
		action := ""
		if len(parts) > 1 {
			action = strings.Join(parts[1:], "/")
		}

		// 配置类写操作按 failWrites 名单决定成败；探测、读取与删除不受影响
		// —— 删除要保持可用，才能验证「写回没落地时不该删渠道」。
		isWrite := r.Method == http.MethodPut || action == "schedulable"
		if isWrite && f.writeFails(accountID) {
			http.Error(w, `{"code":500,"message":"upstream write failed"}`,
				http.StatusInternalServerError)
			return
		}

		switch {
		case action == "test" && r.Method == http.MethodPost:
			f.serveProbe(w, accountID)
		case action == "upstream-billing-probe" && r.Method == http.MethodPost:
			value := f.upstreamMultiplier(accountID)
			writeEnvelope(w, map[string]any{
				"account_id": accountID,
				"snapshot": map[string]any{
					"status": "ok",
					"data": map[string]any{
						"billing_scope":             "token",
						"resolved_rate_multiplier":  value,
						"effective_rate_multiplier": value,
					},
				},
			})
		case action == "" && r.Method == http.MethodDelete:
			f.mu.Lock()
			f.deletes[accountID]++
			f.hidden[accountID] = true
			f.mu.Unlock()
			writeEnvelope(w, map[string]any{"ok": true})
		case action == "schedulable" && r.Method == http.MethodPost:
			var payload struct {
				Schedulable bool `json:"schedulable"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			f.mu.Lock()
			f.schedulable[accountID] = payload.Schedulable
			f.mu.Unlock()
			writeEnvelope(w, map[string]any{"ok": true})
		case action == "clear-error" || action == "recover-state":
			f.mu.Lock()
			f.clearedErr[accountID] = true
			f.mu.Unlock()
			writeEnvelope(w, map[string]any{"ok": true})
		case action == "" && r.Method == http.MethodPut:
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			f.mu.Lock()
			if f.updates[accountID] == nil {
				f.updates[accountID] = map[string]any{}
			}
			for key, value := range payload {
				f.updates[accountID][key] = value
			}
			if _, ok := payload["name"]; ok {
				f.nameUpdates[accountID]++
			}
			f.mu.Unlock()
			writeEnvelope(w, map[string]any{"ok": true})
		default:
			writeEnvelope(w, map[string]any{"ok": true})
		}
	})

	return mux
}

// serveProbe 以 SSE 形式返回测试结果，形态与 sub2api 的账号测试接口一致。
func (f *fakeSub2API) serveProbe(w http.ResponseWriter, accountID int64) {
	f.mu.Lock()
	fatal := f.probeFatal[accountID]
	result := f.probeResults[accountID]
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	send := func(payload map[string]any) {
		raw, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		if flusher != nil {
			flusher.Flush()
		}
	}

	send(map[string]any{"type": "test_start", "model": "claude-sonnet-5"})
	if result == probeQuota {
		// sub2api 的 429 原文形态。
		send(map[string]any{
			"type":  "error",
			"error": `API returned 429: {"error":{"type":"usage_limit_reached"}}`,
		})
		return
	}
	if fatal {
		send(map[string]any{"type": "error", "error": "401 Unauthorized: invalid api key"})
		return
	}
	send(map[string]any{"type": "content", "text": "hi"})
	send(map[string]any{"type": "test_complete", "success": true})
}

func writeEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "", "data": data})
}

// hideAccount 模拟 sub2api 侧删除渠道：账号列表里不再返回它。
func (f *fakeSub2API) hideAccount(accountID int64, hidden bool) {
	f.mu.Lock()
	f.hidden[accountID] = hidden
	f.mu.Unlock()
}

func (f *fakeSub2API) isHidden(accountID int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hidden[accountID]
}

// setSchedulable 直接设定账号列表接口回显的可调度状态。
// setRateLimitReset 让账号列表接口回显一个限流窗口结束时间。
func (f *fakeSub2API) setRateLimitReset(accountID int64, until time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rateLimitReset[accountID] = until
}

// rateLimitResetOf 返回账号列表接口该回显的限流窗口，未设置时返回 nil。
func (f *fakeSub2API) rateLimitResetOf(accountID int64) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if until, ok := f.rateLimitReset[accountID]; ok {
		return until.UTC().Format(time.RFC3339Nano)
	}
	return nil
}

func (f *fakeSub2API) setSchedulable(accountID int64, schedulable bool) {
	f.mu.Lock()
	f.schedulable[accountID] = schedulable
	f.mu.Unlock()
}

func (f *fakeSub2API) setFatal(accountID int64, fatal bool) {
	f.mu.Lock()
	f.probeFatal[accountID] = fatal
	f.mu.Unlock()
}

func (f *fakeSub2API) setUpstreamMultiplier(accountID int64, value float64) {
	f.mu.Lock()
	f.upstreamMultipliers[accountID] = value
	f.mu.Unlock()
}

func (f *fakeSub2API) setCredentials(accountID int64, credentials map[string]any) {
	f.mu.Lock()
	f.credentials[accountID] = credentials
	f.mu.Unlock()
}

func (f *fakeSub2API) credentialExportCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exportCount
}

func (f *fakeSub2API) upstreamMultiplier(accountID int64) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if value := f.upstreamMultipliers[accountID]; value > 0 {
		return value
	}
	return 1
}

// setFailWrites 让指定账号的所有写操作失败，模拟 sub2api 短暂不可写。
func (f *fakeSub2API) setFailWrites(accountID int64, fail bool) {
	f.mu.Lock()
	f.failWrites[accountID] = fail
	f.mu.Unlock()
}

func (f *fakeSub2API) writeFails(accountID int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failWrites[accountID]
}

func (f *fakeSub2API) deleteCount(accountID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes[accountID]
}

// setProbeResult 指定某账号的探测结果类型。
func (f *fakeSub2API) setProbeResult(accountID int64, result probeResult) {
	f.mu.Lock()
	f.probeResults[accountID] = result
	f.mu.Unlock()
}

// setStatus 模拟在 sub2api 后台改动账号状态（如停用）。
func (f *fakeSub2API) setStatus(accountID int64, status string) {
	f.mu.Lock()
	f.status[accountID] = status
	f.mu.Unlock()
}

func (f *fakeSub2API) statusNow(accountID int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if value, ok := f.status[accountID]; ok {
		return value
	}
	return "active"
}

func (f *fakeSub2API) schedulableOf(accountID int64) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.schedulable[accountID]
	return value, ok
}

// schedulableNow 返回账号列表接口应回显的可调度状态：
// 没有被写过时默认为 true，写过之后要如实反映，否则引擎会以为无需再写。
func (f *fakeSub2API) schedulableNow(accountID int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if value, ok := f.schedulable[accountID]; ok {
		return value
	}
	return true
}

func (f *fakeSub2API) updateOf(accountID int64, key string) (any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.updates[accountID][key]
	return value, ok
}

func (f *fakeSub2API) nameUpdateCount(accountID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nameUpdates[accountID]
}

// setupEngine 起一个假 sub2api 并连上真实的 store 与引擎。
func setupEngine(t *testing.T) (*Engine, *store.Store, *fakeSub2API) {
	t.Helper()

	fake := newFakeSub2API()
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SaveConnection(domain.Connection{
		BaseURL:        server.URL,
		AdminAPIKey:    "test-key",
		TimeoutSeconds: 10,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("保存连接配置失败: %v", err)
	}

	// 让每一轮都执行探测，便于测试推进状态。
	p, _ := st.Policy()
	p.Probe.IntervalSeconds = 30
	p.Probe.SkipWhenTrafficFresh = false
	p.Recovery.ProbeIntervalSeconds = 30
	p.Recovery.HoldSeconds = 0
	p.Recovery.SuccessCount = 1
	p.Breaker.FusedCooldownSecs = 0
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	client := upstream.New(server.URL, "test-key", 10*time.Second)
	return New(st, client), st, fake
}

func TestSyncLinkedMultipliersMatchesCredentialsAndReplacesNameSuffix(t *testing.T) {
	eng, st, fake := setupEngine(t)
	if err := eng.RunOnce(context.Background()); err != nil {
		t.Fatalf("初始化目录失败: %v", err)
	}
	conn, err := st.Connection()
	if err != nil {
		t.Fatal(err)
	}
	fake.setCredentials(101, map[string]any{
		"api_key":  "linked-key",
		"base_url": conn.BaseURL + "/",
	})
	channel := store.UpstreamChannel{ID: 1, Type: store.UpstreamChannelSub2API, BaseURL: conn.BaseURL}

	if err := eng.SyncLinkedMultipliers(context.Background(), channel, map[string]float64{"linked-key": 0.12}); err != nil {
		t.Fatalf("倍率联动失败: %v", err)
	}
	p, err := st.Policy()
	if err != nil || p.AccountLinkedMultipliers["101"] != 0.12 {
		t.Fatalf("联动倍率未保存: %+v err=%v", p.AccountLinkedMultipliers, err)
	}
	if name, ok := fake.updateOf(101, "name"); !ok || name != "健康渠道【x0.12】" {
		t.Fatalf("渠道名称写回异常: %v/%v", name, ok)
	}
	account, err := st.Account(101)
	if err != nil || account.Name != "健康渠道【x0.12】" {
		t.Fatalf("本地渠道缓存异常: %+v err=%v", account, err)
	}
	if fake.nameUpdateCount(101) != 1 {
		t.Fatalf("首次同步应只写一次名称: %d", fake.nameUpdateCount(101))
	}

	if err := eng.SyncLinkedMultipliers(context.Background(), channel, map[string]float64{"linked-key": 0.12}); err != nil {
		t.Fatalf("重复倍率联动失败: %v", err)
	}
	if fake.nameUpdateCount(101) != 1 {
		t.Fatalf("重复同步不应再次写名称: %d", fake.nameUpdateCount(101))
	}
}

func TestSyncLinkedMultipliersKeepsLocalValueWhenNameWriteFails(t *testing.T) {
	eng, st, fake := setupEngine(t)
	if err := eng.RunOnce(context.Background()); err != nil {
		t.Fatalf("初始化目录失败: %v", err)
	}
	conn, _ := st.Connection()
	fake.setCredentials(101, map[string]any{"api_key": "linked-key", "base_url": conn.BaseURL})
	fake.setFailWrites(101, true)
	channel := store.UpstreamChannel{ID: 1, Type: store.UpstreamChannelSub2API, BaseURL: conn.BaseURL}
	if err := eng.SyncLinkedMultipliers(context.Background(), channel, map[string]float64{"linked-key": 0.2}); err != nil {
		t.Fatalf("名称写回失败不应中断联动: %v", err)
	}
	p, err := st.Policy()
	if err != nil || p.AccountLinkedMultipliers["101"] != 0.2 {
		t.Fatalf("名称失败时本地倍率仍应保存: %+v err=%v", p.AccountLinkedMultipliers, err)
	}
	if fake.nameUpdateCount(101) != 0 {
		t.Fatalf("写回失败不应计入成功名称更新: %d", fake.nameUpdateCount(101))
	}
}

func TestSyncLinkedMultipliersSkipsURLAndKeyMismatches(t *testing.T) {
	eng, st, fake := setupEngine(t)
	if err := eng.RunOnce(context.Background()); err != nil {
		t.Fatalf("初始化目录失败: %v", err)
	}
	conn, _ := st.Connection()
	p, _ := st.Policy()
	p.AccountLinkedMultipliers["101"] = 0.5
	p.AccountLinkedMultipliers["102"] = 0.6
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("准备旧联动倍率失败: %v", err)
	}
	fake.setCredentials(101, map[string]any{"api_key": "different-key", "base_url": conn.BaseURL})
	fake.setCredentials(102, map[string]any{"api_key": "linked-key", "base_url": conn.BaseURL + "/other"})
	channel := store.UpstreamChannel{ID: 1, Type: store.UpstreamChannelSub2API, BaseURL: conn.BaseURL}
	if err := eng.SyncLinkedMultipliers(context.Background(), channel, map[string]float64{"linked-key": 0.12}); err != nil {
		t.Fatalf("失配联动不应失败: %v", err)
	}
	p, _ = st.Policy()
	if p.AccountLinkedMultipliers["101"] != 0.5 || p.AccountLinkedMultipliers["102"] != 0.6 {
		t.Fatalf("失配账号旧倍率不应被清理: %#v", p.AccountLinkedMultipliers)
	}
	if fake.nameUpdateCount(101) != 0 || fake.nameUpdateCount(102) != 0 {
		t.Fatalf("URL/Key 失配账号不应写回名称: 101=%d 102=%d", fake.nameUpdateCount(101), fake.nameUpdateCount(102))
	}
}

// TestEndToEndFuseAndRecover 覆盖完整链路：
// 同步 → 探测 → 评分 → 致命错误熔断并写回 → 上游恢复后自动回池。
func TestEndToEndFuseAndRecover(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 第一轮：两个渠道都健康。
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("第一轮调度失败: %v", err)
	}

	states, err := st.ChannelStateMap()
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("受管渠道数 = %d, 期望 2", len(states))
	}
	for id, state := range states {
		if state.Health != domain.HealthHealthy {
			t.Fatalf("渠道 %d 状态 = %v, 期望 healthy", id, state.Health)
		}
		if state.HealthScore != 100 {
			t.Fatalf("渠道 %d 健康分 = %.1f, 期望 100", id, state.HealthScore)
		}
	}

	// 两个渠道都是 apikey 类型，未人工设置倍率时都取默认值 1。
	for id, state := range states {
		if state.Multiplier != policy.DefaultAPIKeyMultiplier {
			t.Fatalf("渠道 %d 倍率 = %v, 期望 APIKey 默认值 %v",
				id, state.Multiplier, policy.DefaultAPIKeyMultiplier)
		}
		if state.MultiplierManual {
			t.Fatalf("渠道 %d 未人工设置过倍率，不应标记为 manual", id)
		}
	}

	// 人工把 102 的倍率调高，价格优先下它的权重应低于 101。
	p, _ := st.Policy()
	p.AccountMultipliers = map[string]float64{"102": 5}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存倍率失败: %v", err)
	}
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调整倍率后调度失败: %v", err)
	}
	states, _ = st.ChannelStateMap()
	if states[101].Weight <= states[102].Weight {
		t.Fatalf("低倍率渠道权重 %.1f 应高于高倍率渠道 %.1f",
			states[101].Weight, states[102].Weight)
	}
	if !states[102].MultiplierManual {
		t.Fatal("人工设置过倍率的渠道应标记为 manual")
	}

	// 第二轮：102 开始返回致命错误。
	fake.setFatal(102, true)
	forceProbeNow(t, st, 102)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("第二轮调度失败: %v", err)
	}

	state, err := st.ChannelState(102)
	if err != nil {
		t.Fatalf("读取渠道 102 状态失败: %v", err)
	}
	if state.Health != domain.HealthFused {
		t.Fatalf("致命错误后状态 = %v, 期望 fused（分数 %.1f）", state.Health, state.HealthScore)
	}
	if schedulable, ok := fake.schedulableOf(102); !ok || schedulable {
		t.Fatalf("应向 sub2api 写入 schedulable=false，实际 %v / 是否写入 %v", schedulable, ok)
	}

	// 熔断前必须先抓取基线，否则无法还原用户的原始配置。
	base, err := st.Baseline(102)
	if err != nil {
		t.Fatalf("熔断前应记录基线: %v", err)
	}
	if base.Priority != 10 || base.Concurrency != 5 || !base.Schedulable {
		t.Fatalf("基线内容不正确: %+v", base)
	}

	// 健康渠道不应被误伤。
	if healthyState, err := st.ChannelState(101); err != nil || healthyState.Health != domain.HealthHealthy {
		t.Fatalf("健康渠道被误判为 %v", healthyState.Health)
	}

	// 第三轮起：上游恢复。回池要求健康分重新爬回目标线（默认 75 分），
	// 因此需要连续几次成功探测，而不是一次成功就放回流量。
	fake.setFatal(102, false)

	var recovered bool
	for round := 0; round < 6 && !recovered; round++ {
		forceProbeNow(t, st, 102)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("恢复轮次 %d 调度失败: %v", round, err)
		}
		state, err = st.ChannelState(102)
		if err != nil {
			t.Fatalf("读取渠道 102 状态失败: %v", err)
		}
		recovered = state.Health != domain.HealthFused
	}

	if !recovered {
		t.Fatalf("连续成功探测后仍未回池（分数 %.1f，连续成功 %d）",
			state.HealthScore, state.ConsecutiveOK)
	}
	if state.HealthScore < 75 {
		t.Fatalf("回池时健康分 = %.1f, 不应低于回池目标 75", state.HealthScore)
	}
	if schedulable, ok := fake.schedulableOf(102); !ok || !schedulable {
		t.Fatalf("回池后应写入 schedulable=true，实际 %v", schedulable)
	}
}

func TestAutomaticUpstreamMultiplierDrivesPricePriority(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("准备健康状态失败: %v", err)
	}

	fake.setUpstreamMultiplier(101, 0.25)
	fake.setUpstreamMultiplier(102, 2.5)
	p, _ := st.Policy()
	p.Strategy = policy.StrategyPrice
	p.AccountUpstreamMultiplierEnabled = map[string]bool{"101": true, "102": true}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存实时倍率配置失败: %v", err)
	}

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("实时倍率调度失败: %v", err)
	}
	states, err := st.ChannelStateMap()
	if err != nil {
		t.Fatalf("读取渠道状态失败: %v", err)
	}
	if states[101].Multiplier != 0.25 || states[102].Multiplier != 2.5 {
		t.Fatalf("自动倍率未进入本轮状态: 101=%g 102=%g", states[101].Multiplier, states[102].Multiplier)
	}
	if states[101].DesiredPriority >= states[102].DesiredPriority {
		t.Fatalf("价格优先未按自动倍率重排: 101=%d 102=%d",
			states[101].DesiredPriority, states[102].DesiredPriority)
	}
}

func TestAutomaticUpstreamMultiplierFusesAboveThresholdInSameRun(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("准备健康状态失败: %v", err)
	}

	fake.setUpstreamMultiplier(102, 2.5)
	p, _ := st.Policy()
	p.AccountUpstreamMultiplierEnabled = map[string]bool{"102": true}
	p.AccountUpstreamMultiplierBreakers = map[string]policy.UpstreamMultiplierBreaker{
		"102": {Enabled: true, Threshold: 1.5},
	}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存倍率阈值配置失败: %v", err)
	}

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("倍率阈值调度失败: %v", err)
	}
	state, err := st.ChannelState(102)
	if err != nil {
		t.Fatalf("读取渠道状态失败: %v", err)
	}
	if state.Multiplier != 2.5 || state.Health != domain.HealthFused {
		t.Fatalf("超阈值倍率未在同轮熔断: multiplier=%g health=%s", state.Multiplier, state.Health)
	}
	if !strings.Contains(state.FusedReason, "2.5") || !strings.Contains(state.FusedReason, "1.5") {
		t.Fatalf("倍率熔断原因不完整: %q", state.FusedReason)
	}
	if schedulable, ok := fake.schedulableOf(102); !ok || schedulable {
		t.Fatalf("超阈值渠道未写回 schedulable=false: %v/%v", schedulable, ok)
	}
}

// TestEndToEndMinPoolKeepsGroupAlive 是保底的端到端回归：
// 组内两个渠道同时 401，分组也绝不能被打空 —— 必须留下一个保底渠道并告警。
func TestEndToEndMinPoolKeepsGroupAlive(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	fake.setFatal(101, true)
	fake.setFatal(102, true)

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	states, err := st.ChannelStateMap()
	if err != nil {
		t.Fatalf("读取渠道状态失败: %v", err)
	}
	survivors, fused := 0, 0
	for _, state := range states {
		switch state.Health {
		case domain.HealthSurvivor:
			survivors++
		case domain.HealthFused:
			fused++
		}
	}
	if survivors != 1 {
		t.Fatalf("保底渠道数 = %d, 期望 1（全部 401 也不能让分组断供）", survivors)
	}
	if fused != 1 {
		t.Fatalf("熔断渠道数 = %d, 期望 1", fused)
	}

	// 保底渠道必须仍然可调度，否则分组实际上还是断的。
	for id, state := range states {
		if state.Health != domain.HealthSurvivor {
			continue
		}
		if schedulable, ok := fake.schedulableOf(id); ok && !schedulable {
			t.Fatalf("保底渠道 %d 被写成不可调度，分组实际已断供", id)
		}
	}

	groupState, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if groupState.Status != domain.GroupSurvivorOnly {
		t.Fatalf("分组状态 = %v, 期望 survivor_only", groupState.Status)
	}

	events, _, err := st.Events(store.EventFilter{Action: "survivor_kept", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("保底强留时应写入 survivor_kept 告警")
	}
}

// TestEndToEndAutoApplyDisabled 验证关闭自动执行后只算不写。
func TestEndToEndAutoApplyDisabled(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.AutoApply.Schedulable = false
	p.AutoApply.Priority = false
	p.AutoApply.LoadFactor = false
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	fake.setFatal(102, true)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	state, err := st.ChannelState(102)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Health == domain.HealthFused {
		t.Fatalf("关闭自动写回时实际状态不应伪装成 fused，实际 %v", state.Health)
	}
	if state.DesiredHealth != domain.HealthFused {
		t.Fatalf("期望状态应计算为 fused，实际 %v", state.DesiredHealth)
	}
	if !state.ApplyPending {
		t.Fatal("关闭自动写回且期望状态未生效时应标记 apply_pending")
	}
	if !strings.Contains(state.LastApplyError, "预览模式") {
		t.Fatalf("应说明预览模式未写回，实际 %q", state.LastApplyError)
	}
	if _, ok := fake.schedulableOf(102); ok {
		t.Fatal("关闭自动执行后不应写回 schedulable")
	}
	if _, ok := fake.updateOf(102, "priority"); ok {
		t.Fatal("关闭自动执行后不应写回 priority")
	}
}

// TestEndToEndPausePersistsAcrossRounds 验证人工暂停的持久性：
// 暂停后即使渠道一直健康，后续每一轮调度都不能把它放回流量。
// 这是暂停与熔断最关键的区别。
func TestEndToEndPausePersistsAcrossRounds(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}

	// 人工暂停 102：写进策略名单，并立即写回 sub2api。
	p, _ := st.Policy()
	p.PausedAccountIDs = []int64{102}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if err := eng.SetPaused(ctx, 102, true); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}

	if schedulable, ok := fake.schedulableOf(102); !ok || schedulable {
		t.Fatalf("暂停后应立即写入 schedulable=false，实际 %v / 是否写入 %v", schedulable, ok)
	}

	// 连跑三轮：渠道始终健康，但不能被自动回池。
	for round := 0; round < 3; round++ {
		forceProbeNow(t, st, 102)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", round, err)
		}

		state, err := st.ChannelState(102)
		if err != nil {
			t.Fatalf("读取状态失败: %v", err)
		}
		if state.Health != domain.HealthPaused {
			t.Fatalf("第 %d 轮后状态 = %v, 期望始终为 paused（健康分 %.1f）",
				round, state.Health, state.HealthScore)
		}
		if state.Weight != 0 {
			t.Fatalf("第 %d 轮后暂停渠道权重 = %.1f, 期望 0", round, state.Weight)
		}
		if schedulable, _ := fake.schedulableOf(102); schedulable {
			t.Fatalf("第 %d 轮后暂停渠道被重新放回调度", round)
		}
	}

	// 暂停期间仍在采样：这是与「排除」的区别，便于观察何时可以恢复。
	samples, err := st.RecentSamples(102, 10)
	if err != nil {
		t.Fatalf("读取样本失败: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("暂停的渠道仍应被探测计分")
	}

	// 恢复：从名单移除后应重新参与调度。
	p, _ = st.Policy()
	p.PausedAccountIDs = nil
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if err := eng.SetPaused(ctx, 102, false); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("恢复后调度失败: %v", err)
	}

	state, err := st.ChannelState(102)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Health == domain.HealthPaused {
		t.Fatal("移出暂停名单后不应仍为 paused")
	}
	if schedulable, ok := fake.schedulableOf(102); !ok || !schedulable {
		t.Fatalf("恢复后应写入 schedulable=true，实际 %v", schedulable)
	}
}

// TestEndToEndPauseAllMarksGroupDown 验证组内渠道全部暂停时分组被标记为断供，
// 而不是因为「没有熔断」就显示健康。
func TestEndToEndPauseAllMarksGroupDown(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.PausedAccountIDs = []int64{101, 102}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	groupState, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if groupState.PausedAccounts != 2 {
		t.Fatalf("暂停渠道数 = %d, 期望 2", groupState.PausedAccounts)
	}
	if groupState.Status != domain.GroupAllFused {
		t.Fatalf("全部暂停时分组状态 = %v, 期望 all_fused（无可调度渠道）", groupState.Status)
	}
}

// TestEndToEndCleanupNeedsExplicitOptIn 验证默认配置下，
// 即使渠道持续 401，也绝不会被自动删除。
func TestEndToEndCleanupNeedsExplicitOptIn(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	fake.setFatal(102, true)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	for round := 0; round < 3; round++ {
		forceProbeNow(t, st, 102)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", round, err)
		}
	}

	// 账号仍在缓存里说明没被删。
	if _, err := st.Account(102); err != nil {
		t.Fatalf("默认配置下不应删除任何渠道: %v", err)
	}
	events, _, _ := st.Events(store.EventFilter{Action: "cleanup_deleted", Page: 1, PageSize: 10})
	if len(events) != 0 {
		t.Fatal("默认配置下不应产生删除事件")
	}
}

// TestSyncNowRefreshesGroupStates 是「分组健康矩阵与网站对不上」的回归。
//
// 手动同步必须同时刷新目录与分组聚合，否则页面上仍是旧的分组构成。
func TestSyncNowRefreshesGroupStates(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	// 同步前没有任何分组状态。
	if states, _ := st.GroupStates(); len(states) != 0 {
		t.Fatalf("初始应无分组状态，实际 %d 条", len(states))
	}

	if err := eng.SyncNow(ctx); err != nil {
		t.Fatalf("手动同步失败: %v", err)
	}

	// 同步后目录与分组聚合都要就位。
	accounts, err := st.Accounts()
	if err != nil || len(accounts) != 2 {
		t.Fatalf("同步后账号数 = %d, 期望 2 (err=%v)", len(accounts), err)
	}
	groupState, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("同步后应产生分组状态: %v", err)
	}
	if groupState.TotalAccounts != 2 {
		t.Fatalf("分组渠道数 = %d, 期望 2（与 sub2api 一致）", groupState.TotalAccounts)
	}
	if groupState.Strategy == "" {
		t.Fatal("分组状态应带上生效策略")
	}
}

// TestSyncNowReflectsUpstreamChanges 验证 sub2api 侧增删渠道后，
// 手动同步能让分组矩阵跟上。
func TestSyncNowReflectsUpstreamChanges(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	if err := eng.SyncNow(ctx); err != nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	if state, _ := st.GroupState(1); state.TotalAccounts != 2 {
		t.Fatalf("首次同步后渠道数 = %d, 期望 2", state.TotalAccounts)
	}

	// 模拟 sub2api 侧删掉一个渠道。
	fake.hideAccount(102, true)
	if err := eng.SyncNow(ctx); err != nil {
		t.Fatalf("二次同步失败: %v", err)
	}

	accounts, _ := st.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("上游删除后账号数 = %d, 期望 1", len(accounts))
	}
	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.TotalAccounts != 1 {
		t.Fatalf("分组矩阵渠道数 = %d, 期望 1（应跟上上游变化）", state.TotalAccounts)
	}
}

// TestSyncNowRefreshesSchedulingWithoutChangingSamples 固定渠道池“立即同步”的契约：
// 只读取 sub2api 的真实目录状态，不调用测试接口，也不改已有健康样本。
func TestSyncNowRefreshesSchedulingWithoutChangingSamples(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 完整跑一轮生成真实测试样本，随后只做目录同步。
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	before, err := st.RecentSamples(101, 200)
	if err != nil || len(before) == 0 {
		t.Fatalf("读取同步前样本失败或样本为空: len=%d err=%v", len(before), err)
	}

	fake.setSchedulable(101, false)
	if err := eng.SyncNow(ctx); err != nil {
		t.Fatalf("立即同步失败: %v", err)
	}

	account, err := st.Account(101)
	if err != nil {
		t.Fatalf("读取同步后渠道失败: %v", err)
	}
	if account.Schedulable {
		t.Fatal("同步后仍显示可调度，未反映 sub2api 的真实关闭状态")
	}

	after, err := st.RecentSamples(101, 200)
	if err != nil {
		t.Fatalf("读取同步后样本失败: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("同步改变了样本数量: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID || !after[i].OccurredAt.Equal(before[i].OccurredAt) {
			t.Fatalf("同步改动了第 %d 条样本: before=%+v after=%+v", i, before[i], after[i])
		}
	}
}

// TestSyncedButUnprobedGroupLooksHealthy 是「分组健康矩阵与网站对不上」的回归。
//
// 用户报告：分组下明明有存活渠道，矩阵却显示可用 0。根因是刚同步完
// 还没探测过的渠道被一律判为「不可用」——没有证据说明它坏，就不该假定它坏。
func TestSyncedButUnprobedGroupLooksHealthy(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	// 只同步、不探测，模拟刚接入或自动守护关闭的状态。
	if err := eng.SyncNow(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}

	if state.TotalAccounts != 2 {
		t.Fatalf("分组渠道数 = %d, 期望 2", state.TotalAccounts)
	}
	if state.PendingAccounts != 2 {
		t.Fatalf("待探测渠道数 = %d, 期望 2", state.PendingAccounts)
	}
	if state.DegradedAccounts != 0 {
		t.Fatalf("待探测渠道被算成降级 %d 个，应独立统计", state.DegradedAccounts)
	}
	// 核心断言：sub2api 侧可调度的渠道必须计入可用池。
	if state.AvailableAccounts != 2 {
		t.Fatalf("可用渠道数 = %d, 期望 2（网站上正常服务的渠道不该显示为 0）",
			state.AvailableAccounts)
	}
	if state.Status != domain.GroupHealthy {
		t.Fatalf("分组状态 = %v, 期望 healthy（没有证据说明渠道有问题）", state.Status)
	}
}

// TestUnschedulableUnprobedNotCountedAlive 验证口径不是无脑放宽：
// sub2api 侧已经不可调度的渠道，即使没探测过也不算可用。
func TestUnschedulableUnprobedNotCountedAlive(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 让 102 在 sub2api 侧就是不可调度的。
	fake.setSchedulable(102, false)

	if err := eng.SyncNow(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.AvailableAccounts != 1 {
		t.Fatalf("可用渠道数 = %d, 期望 1（sub2api 侧不可调度的不算）",
			state.AvailableAccounts)
	}
}

// forceProbeNow 把上次探测时间清零，让下一轮立即重新探测。
func forceProbeNow(t *testing.T, st *store.Store, accountID int64) {
	t.Helper()
	state, err := st.ChannelState(accountID)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	state.LastProbeAt = time.Time{}
	state.LastSampleAt = time.Time{}
	if err := st.SaveChannelState(state); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
}
