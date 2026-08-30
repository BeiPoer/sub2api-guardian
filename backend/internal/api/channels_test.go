package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/auth"
	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

// fakeUpstream 是一个最小可用的假 sub2api，用于验证 API 层行为。
type fakeUpstream struct {
	groupCalls      atomic.Int64
	accountCalls    atomic.Int64
	updateCalls     atomic.Int64
	multiplierCalls atomic.Int64

	// groupCount 控制假分组数量，用来放大 Sync 的调用次数。
	groupCount               int
	accountType              string
	rateMultiplier           float64
	baseURL                  string
	upstreamMultiplier       float64
	upstreamMultiplierStatus int
}

func (f *fakeUpstream) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/admin/groups/all", func(w http.ResponseWriter, r *http.Request) {
		f.groupCalls.Add(1)
		groups := make([]map[string]any, 0, f.groupCount)
		for i := 1; i <= f.groupCount; i++ {
			groups = append(groups, map[string]any{
				"id": i, "name": fmt.Sprintf("分组%d", i),
				"platform": "anthropic", "status": "active", "rate_multiplier": 2.0,
			})
		}
		writeEnvelope(w, groups)
	})

	mux.HandleFunc("/api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		f.accountCalls.Add(1)
		accountType := f.accountType
		if accountType == "" {
			accountType = "apikey"
		}
		rateMultiplier := f.rateMultiplier
		if rateMultiplier == 0 {
			rateMultiplier = 1
		}
		writeEnvelope(w, map[string]any{
			"items": []map[string]any{{
				"id": 101, "name": "渠道A", "platform": "anthropic", "type": accountType,
				"status": "active", "schedulable": true, "priority": 10, "concurrency": 5,
				"rate_multiplier": rateMultiplier, "group_ids": []int64{1},
			}},
			"total": 1, "page": 1, "page_size": 200, "pages": 1,
		})
	})

	mux.HandleFunc("/api/v1/admin/usage", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, map[string]any{
			"items": []any{}, "total": 0, "page": 1, "page_size": 100, "pages": 1,
		})
	})

	mux.HandleFunc("/api/v1/admin/usage/stats", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, map[string]any{"total_actual_cost": 0, "total_tokens": 0})
	})

	mux.HandleFunc("/api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, map[string]any{
			"items": []any{}, "total": 0, "page": 1, "page_size": 1000, "pages": 1,
		})
	})

	mux.HandleFunc("/api/v1/admin/payment/orders", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, map[string]any{
			"items": []any{}, "total": 0, "page": 1, "page_size": 1000, "pages": 1,
		})
	})

	mux.HandleFunc("/api/v1/admin/accounts/data", func(w http.ResponseWriter, r *http.Request) {
		accountType := f.accountType
		if accountType == "" {
			accountType = "apikey"
		}
		writeEnvelope(w, map[string]any{
			"accounts": []map[string]any{{
				"name": "渠道A", "platform": "anthropic", "type": accountType,
				"credentials": map[string]any{"api_key": "sk-upstream", "base_url": f.baseURL},
			}},
			"proxies": []any{},
		})
	})

	mux.HandleFunc("/api/v1/admin/accounts/upstream-billing-probe/batch", func(w http.ResponseWriter, r *http.Request) {
		f.multiplierCalls.Add(1)
		if f.upstreamMultiplierStatus != 0 {
			w.WriteHeader(f.upstreamMultiplierStatus)
			return
		}
		var request struct {
			AccountIDs []int64 `json:"account_ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		value := f.upstreamMultiplier
		if value == 0 {
			value = 1.25
		}
		results := make([]map[string]any, 0, len(request.AccountIDs))
		for _, accountID := range request.AccountIDs {
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

	mux.HandleFunc("/v1/usage", func(w http.ResponseWriter, r *http.Request) {
		f.multiplierCalls.Add(1)
		if f.upstreamMultiplierStatus != 0 {
			w.WriteHeader(f.upstreamMultiplierStatus)
			return
		}
		value := f.upstreamMultiplier
		if value == 0 {
			value = 1.25
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rate_multiplier": value})
	})

	mux.HandleFunc("/api/v1/admin/accounts/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/upstream-billing-probe") && r.Method == http.MethodPost {
			f.multiplierCalls.Add(1)
			if f.upstreamMultiplierStatus != 0 {
				w.WriteHeader(f.upstreamMultiplierStatus)
				return
			}
			value := f.upstreamMultiplier
			if value == 0 {
				value = 1.25
			}
			writeEnvelope(w, map[string]any{
				"account_id": 101,
				"snapshot": map[string]any{
					"status": "ok",
					"data": map[string]any{
						"billing_scope":             "token",
						"resolved_rate_multiplier":  value,
						"effective_rate_multiplier": value,
					},
				},
			})
			return
		}
		if r.Method == http.MethodPut {
			f.updateCalls.Add(1)
		}
		writeEnvelope(w, map[string]any{"ok": true})
	})

	return mux
}

func writeEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "", "data": data})
}

// testSessionToken 是 setupAPI 建好的登录会话，doJSON 会自动带上。
//
// 所有 /api/* 现在都要登录，测试沿用真实的会话链路（建用户 → 建会话 → 带
// Cookie），而不是给测试开后门 —— 后者会让鉴权本身变成没被覆盖的代码。
var testSessionToken string

func setupAPI(t *testing.T, fake *fakeUpstream) (http.Handler, *store.Store) {
	t.Helper()

	upstreamServer := httptest.NewServer(fake.handler())
	t.Cleanup(upstreamServer.Close)
	fake.baseURL = upstreamServer.URL

	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SaveConnection(domain.Connection{
		BaseURL:        upstreamServer.URL,
		AdminAPIKey:    "test-key",
		TimeoutSeconds: 10,
		Enabled:        false, // 关掉后台心跳，避免干扰断言
	}); err != nil {
		t.Fatalf("保存连接配置失败: %v", err)
	}

	testSessionToken = seedSession(t, st, "admin", "hunter2hunter2")

	client := upstream.New(upstreamServer.URL, "test-key", 10*time.Second)
	eng := engine.New(st, client)
	server := NewServer(st, client, eng, nil)
	t.Cleanup(server.Close)

	return server.Handler(), st
}

// seedSession 建一个用户并给它开一个有效会话，返回会话令牌。
func seedSession(t *testing.T, st *store.Store, username, password string) string {
	t.Helper()

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("生成口令摘要失败: %v", err)
	}
	userID, err := st.CreateUser(username, hash)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	token, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("生成会话令牌失败: %v", err)
	}
	if err := st.CreateSession(token, userID, time.Now().Add(time.Hour), "go-test"); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	return token
}

// doJSON 发一个 JSON 请求，超时即视为「卡住」。
func doJSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if testSessionToken != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: testSessionToken})
	}
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()

	select {
	case <-done:
		return rec
	case <-time.After(15 * time.Second):
		// 触发 goroutine 栈打印，便于定位卡在哪一步。
		panic(fmt.Sprintf("%s %s 超过 15 秒未返回（疑似死锁或长时间阻塞）", method, path))
	}
}

// TestUpdateChannelMultiplierOnly 是本次问题的核心回归：
// 只改上游调度倍率时，它是 Guardian 内部字段，不应触发任何 sub2api 写入或全量同步。
func TestUpdateChannelMultiplierOnly(t *testing.T) {
	fake := &fakeUpstream{groupCount: 30}
	handler, st := setupAPI(t, fake)

	baselineGroupCalls := fake.groupCalls.Load()
	baselineAccountCalls := fake.accountCalls.Load()

	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"multiplier": 2.5,
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 响应 = %s", rec.Code, rec.Body.String())
	}

	if got := fake.updateCalls.Load(); got != 0 {
		t.Fatalf("只改调度倍率不应写回 sub2api，实际写入 %d 次", got)
	}
	if got := fake.groupCalls.Load() - baselineGroupCalls; got != 0 {
		t.Fatalf("只改调度倍率不应触发全量同步，实际拉取分组 %d 次", got)
	}
	if got := fake.accountCalls.Load() - baselineAccountCalls; got != 0 {
		t.Fatalf("只改调度倍率不应触发账号同步，实际拉取账号 %d 次", got)
	}

	p, err := st.Policy()
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if got := p.AccountMultipliers["101"]; got != 2.5 {
		t.Fatalf("调度倍率 = %v, 期望 2.5", got)
	}
}

// TestUpdateChannelMultiplierClears 验证填 0 表示清除。
func TestUpdateChannelMultiplierClears(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1}
	handler, st := setupAPI(t, fake)

	doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"multiplier": 3.0,
	})
	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"multiplier": 0,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 响应 = %s", rec.Code, rec.Body.String())
	}

	p, _ := st.Policy()
	if _, ok := p.AccountMultipliers["101"]; ok {
		t.Fatalf("填 0 应清除调度倍率，实际仍为 %v", p.AccountMultipliers["101"])
	}
}

func TestUpdateChannelUpstreamMultiplierOnly(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, rateMultiplier: 1.75}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	baselineGroupCalls := fake.groupCalls.Load()
	baselineAccountCalls := fake.accountCalls.Load()
	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"upstream_multiplier_enabled": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 响应 = %s", rec.Code, rec.Body.String())
	}
	if fake.updateCalls.Load() != 0 {
		t.Fatal("实时倍率开关不应写回 sub2api")
	}
	if fake.groupCalls.Load() != baselineGroupCalls || fake.accountCalls.Load() != baselineAccountCalls {
		t.Fatal("实时倍率开关不应触发全量同步")
	}
	p, _ := st.Policy()
	if !p.AccountUpstreamMultiplierEnabled["101"] {
		t.Fatal("实时倍率开关未保存")
	}
}

func TestUpdateChannelRejectsUpstreamMultiplierForOAuth(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, accountType: "oauth"}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"upstream_multiplier_enabled": true,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400，响应 = %s", rec.Code, rec.Body.String())
	}
	p, _ := st.Policy()
	if p.AccountUpstreamMultiplierEnabled["101"] {
		t.Fatal("OAuth 渠道不应保存实时倍率开关")
	}
}

func TestChannelsExposeUpstreamMultiplierState(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, rateMultiplier: 1.75}
	handler, _ := setupAPI(t, fake)
	syncCatalog(t, handler)

	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"multiplier":                  0.5,
		"upstream_multiplier_enabled": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("保存失败: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodGet, "/api/channels", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取渠道失败: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Items []ChannelDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析渠道响应失败: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("渠道数 = %d, 期望 1", len(result.Items))
	}
	ch := result.Items[0]
	if !ch.UpstreamMultiplierEnabled || ch.MultiplierSource != "upstream_fallback" || ch.Multiplier != 1.75 {
		t.Fatalf("实时倍率状态错误: %+v", ch)
	}
	if ch.ManualMultiplier == nil || *ch.ManualMultiplier != 0.5 {
		t.Fatalf("人工倍率配置未保留: %v", ch.ManualMultiplier)
	}
}

func TestUpdateChannelUpstreamMultiplierBreaker(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, rateMultiplier: 1.75}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)
	baselineUpdates := fake.updateCalls.Load()

	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"upstream_multiplier_enabled":         true,
		"upstream_multiplier_breaker_enabled": true,
		"upstream_multiplier_threshold":       1.5,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("保存倍率阈值失败: %d %s", rec.Code, rec.Body.String())
	}
	if fake.updateCalls.Load() != baselineUpdates {
		t.Fatal("Guardian 倍率阈值字段不应写回 Sub2API")
	}

	p, err := st.Policy()
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	breaker, ok := p.AccountUpstreamMultiplierBreakers["101"]
	if !p.AccountUpstreamMultiplierEnabled["101"] || !ok || !breaker.Enabled || breaker.Threshold != 1.5 {
		t.Fatalf("倍率阈值配置未完整保存: enabled=%v breaker=%+v", p.AccountUpstreamMultiplierEnabled["101"], breaker)
	}

	rec = doJSON(t, handler, http.MethodGet, "/api/channels", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取渠道失败: %d %s", rec.Code, rec.Body.String())
	}
	var channels struct {
		Items []ChannelDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &channels); err != nil || len(channels.Items) != 1 {
		t.Fatalf("解析渠道失败: %v %s", err, rec.Body.String())
	}
	channel := channels.Items[0]
	if !channel.UpstreamMultiplierBreakerEnabled || channel.UpstreamMultiplierThreshold == nil ||
		*channel.UpstreamMultiplierThreshold != 1.5 {
		t.Fatalf("渠道 DTO 未返回倍率阈值配置: %+v", channel)
	}
}

func TestUpdateChannelRejectsBreakerWithoutAutomaticMultiplier(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"upstream_multiplier_breaker_enabled": true,
		"upstream_multiplier_threshold":       1.5,
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "先开启实时使用上游倍率") {
		t.Fatalf("未开启实时倍率时应拒绝阈值配置: %d %s", rec.Code, rec.Body.String())
	}
	p, _ := st.Policy()
	if len(p.AccountUpstreamMultiplierBreakers) != 0 {
		t.Fatalf("非法请求不应保存阈值配置: %#v", p.AccountUpstreamMultiplierBreakers)
	}
}

func TestDisablingAutomaticMultiplierClearsBreaker(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	if rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"upstream_multiplier_enabled":         true,
		"upstream_multiplier_breaker_enabled": true,
		"upstream_multiplier_threshold":       1.5,
	}); rec.Code != http.StatusOK {
		t.Fatalf("准备阈值配置失败: %d %s", rec.Code, rec.Body.String())
	}
	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"upstream_multiplier_enabled": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("关闭实时倍率失败: %d %s", rec.Code, rec.Body.String())
	}
	p, _ := st.Policy()
	if p.AccountUpstreamMultiplierEnabled["101"] {
		t.Fatal("实时倍率开关未关闭")
	}
	if _, ok := p.AccountUpstreamMultiplierBreakers["101"]; ok {
		t.Fatal("关闭实时倍率后应清除阈值配置")
	}
}

func TestSyncChannelUpstreamMultiplierUpdatesSnapshot(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, rateMultiplier: 1.75, upstreamMultiplier: 2.5}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	rec := doJSON(t, handler, http.MethodPost, "/api/channels/101/sync-upstream-multiplier", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("同步失败: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Multiplier         float64 `json:"multiplier"`
		PreviousMultiplier float64 `json:"previous_multiplier"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Multiplier != 2.5 || result.PreviousMultiplier != 1.75 {
		t.Fatalf("同步结果错误: %+v", result)
	}
	snapshots, err := st.UpstreamMultipliers()
	if err != nil || snapshots[101].Value != 2.5 || snapshots[101].UpdatedAt.IsZero() {
		t.Fatalf("倍率快照未保存: %#v, err=%v", snapshots, err)
	}

	rec = doJSON(t, handler, http.MethodGet, "/api/channels", nil)
	var channels struct {
		Items []ChannelDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &channels); err != nil || len(channels.Items) != 1 {
		t.Fatalf("读取渠道失败: %v %s", err, rec.Body.String())
	}
	channel := channels.Items[0]
	if channel.UpstreamMultiplier == nil || *channel.UpstreamMultiplier != 2.5 || channel.UpstreamMultiplierUpdatedAt == nil {
		t.Fatalf("渠道未返回倍率快照: %+v", channel)
	}
}

func TestSyncChannelUpstreamMultiplierFailureKeepsSnapshot(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, upstreamMultiplier: 2.5}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	first := doJSON(t, handler, http.MethodPost, "/api/channels/101/sync-upstream-multiplier", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("首次同步失败: %d %s", first.Code, first.Body.String())
	}
	before, _ := st.UpstreamMultipliers()
	fake.upstreamMultiplierStatus = http.StatusBadGateway

	failed := doJSON(t, handler, http.MethodPost, "/api/channels/101/sync-upstream-multiplier", nil)
	if failed.Code != http.StatusBadGateway || !strings.Contains(failed.Body.String(), "继续使用原倍率") {
		t.Fatalf("失败响应错误: %d %s", failed.Code, failed.Body.String())
	}
	after, _ := st.UpstreamMultipliers()
	if after[101] != before[101] {
		t.Fatalf("失败覆盖了旧快照: before=%+v after=%+v", before[101], after[101])
	}
}

func TestSyncChannelUpstreamMultiplierRejectsOAuth(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, accountType: "oauth"}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	rec := doJSON(t, handler, http.MethodPost, "/api/channels/101/sync-upstream-multiplier", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400: %s", rec.Code, rec.Body.String())
	}
	if fake.multiplierCalls.Load() != 0 {
		t.Fatal("OAuth 渠道不应请求上游倍率")
	}
	if snapshots, _ := st.UpstreamMultipliers(); len(snapshots) != 0 {
		t.Fatalf("OAuth 渠道不应保存倍率快照: %#v", snapshots)
	}
}

func TestRealtimeMultiplierRefreshesDuringCatalogSync(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, rateMultiplier: 1.75, upstreamMultiplier: 2.25}
	handler, _ := setupAPI(t, fake)
	syncCatalog(t, handler)
	if rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"upstream_multiplier_enabled": true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("开启实时倍率失败: %d %s", rec.Code, rec.Body.String())
	}

	syncCatalog(t, handler)
	if fake.multiplierCalls.Load() != 1 {
		t.Fatalf("自动倍率同步调用 %d 次，期望 1 次", fake.multiplierCalls.Load())
	}
	rec := doJSON(t, handler, http.MethodGet, "/api/channels", nil)
	var channels struct {
		Items []ChannelDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &channels); err != nil || len(channels.Items) != 1 {
		t.Fatalf("读取渠道失败: %v %s", err, rec.Body.String())
	}
	if channel := channels.Items[0]; channel.Multiplier != 2.25 || channel.MultiplierSource != "upstream" {
		t.Fatalf("自动同步倍率未生效: %+v", channel)
	}
}

func TestAutomaticMultiplierFailureKeepsLastSuccessfulSnapshot(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1, upstreamMultiplierStatus: http.StatusBadGateway}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	oldTime := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	if err := st.SaveUpstreamMultiplier(101, 2.5, oldTime); err != nil {
		t.Fatalf("准备旧倍率快照失败: %v", err)
	}
	if rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"upstream_multiplier_enabled": true,
	}); rec.Code != http.StatusOK {
		t.Fatalf("开启实时倍率失败: %d %s", rec.Code, rec.Body.String())
	}

	// 目录同步会触发到期渠道的自动倍率读取；假上游失败后旧快照必须原样保留。
	syncCatalog(t, handler)
	snapshots, err := st.UpstreamMultipliers()
	if err != nil {
		t.Fatalf("读取倍率快照失败: %v", err)
	}
	if snapshot := snapshots[101]; snapshot.Value != 2.5 || !snapshot.UpdatedAt.Equal(oldTime) {
		t.Fatalf("自动拉取失败覆盖了旧快照: %+v", snapshot)
	}
}

// syncCatalog 先同步一次，让账号进入缓存（受管判定依赖它）。
func syncCatalog(t *testing.T, handler http.Handler) {
	t.Helper()
	rec := doJSON(t, handler, http.MethodPost, "/api/sync", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("同步失败: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSyncSummaryIncludesAvailableAccounts(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1}
	handler, _ := setupAPI(t, fake)

	rec := doJSON(t, handler, http.MethodPost, "/api/sync", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("同步失败: %d %s", rec.Code, rec.Body.String())
	}

	var result struct {
		AvailableAccounts *int `json:"available_accounts"`
		HealthyAccounts   *int `json:"healthy_accounts"`
		Channels          *int `json:"channels"`
		TotalAccounts     int  `json:"total_accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析同步响应失败: %v", err)
	}
	if result.AvailableAccounts == nil {
		t.Fatal("同步响应缺少 available_accounts")
	}
	if result.Channels == nil || *result.Channels != 1 {
		t.Fatalf("同步渠道数 = %v, 期望 1", result.Channels)
	}
	if *result.AvailableAccounts != 1 || result.TotalAccounts != 1 {
		t.Fatalf("存活汇总 = %d/%d, 期望 1/1", *result.AvailableAccounts, result.TotalAccounts)
	}
	if result.HealthyAccounts == nil {
		t.Fatal("同步响应应保留 healthy_accounts 兼容字段")
	}
}

// TestUpdateChannelSchedulingFields 验证调度字段仍会写回 sub2api。
func TestUpdateChannelSchedulingFields(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1}
	handler, _ := setupAPI(t, fake)
	syncCatalog(t, handler)

	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"priority":    5,
		"load_factor": 20,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 响应 = %s", rec.Code, rec.Body.String())
	}
	if got := fake.updateCalls.Load(); got == 0 {
		t.Fatal("调度字段应写回 sub2api")
	}
}

// TestUpdateChannelMixedPayload 验证调度倍率与调度字段混在一起时各走各的路径。
func TestUpdateChannelMixedPayload(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1}
	handler, st := setupAPI(t, fake)
	syncCatalog(t, handler)

	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"priority":   7,
		"multiplier": 1.8,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 响应 = %s", rec.Code, rec.Body.String())
	}

	p, _ := st.Policy()
	if got := p.AccountMultipliers["101"]; got != 1.8 {
		t.Fatalf("调度倍率 = %v, 期望 1.8", got)
	}
	if got := fake.updateCalls.Load(); got == 0 {
		t.Fatal("混合负载中的调度字段仍应写回 sub2api")
	}
}

// TestUpdateChannelDoesNotRefetchEveryGroup 是「保存卡住」的核心回归。
//
// 保存单个渠道时若触发全量同步，分组数 N 就意味着 N+1 次上游请求；
// 分组多、上游慢时页面会长时间转圈，用户看到的就是「卡住保存不了」。
func TestUpdateChannelDoesNotRefetchEveryGroup(t *testing.T) {
	fake := &fakeUpstream{groupCount: 40}
	handler, _ := setupAPI(t, fake)
	syncCatalog(t, handler)

	baselineAccountCalls := fake.accountCalls.Load()

	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"priority": 5,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 响应 = %s", rec.Code, rec.Body.String())
	}

	// 保存单个渠道最多只需重新读这一个账号，不该把整个目录再拉一遍。
	if got := fake.accountCalls.Load() - baselineAccountCalls; got > 2 {
		t.Fatalf("保存单个渠道触发了 %d 次账号拉取（分组数 40），"+
			"说明仍在做全量同步，上游慢时会表现为页面卡住", got)
	}
}

// TestMultiplierWorksWithUnreachableUpstream 验证上游调度倍率是纯本地字段：
// 即使 sub2api 完全连不上，也应该能保存成功，而不是跟着一起卡住或失败。
func TestMultiplierWorksWithUnreachableUpstream(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1}
	handler, st := setupAPI(t, fake)

	conn, _ := st.Connection()
	conn.BaseURL = "http://127.0.0.1:1"
	conn.TimeoutSeconds = 5
	if err := st.SaveConnection(conn); err != nil {
		t.Fatalf("保存连接配置失败: %v", err)
	}

	start := time.Now()
	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"multiplier": 4.2,
	})
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("调度倍率是本地字段，上游不可达也应保存成功，实际 %d %s",
			rec.Code, rec.Body.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("保存本地字段耗时 %v，不应等待上游", elapsed)
	}

	p, _ := st.Policy()
	if got := p.AccountMultipliers["101"]; got != 4.2 {
		t.Fatalf("调度倍率 = %v, 期望 4.2", got)
	}
}

// TestUpdateChannelUnreachableUpstreamFailsFast 验证上游不可达时快速失败，
// 而不是让页面一直转圈。
func TestUpdateChannelUnreachableUpstreamFailsFast(t *testing.T) {
	fake := &fakeUpstream{groupCount: 1}
	handler, st := setupAPI(t, fake)

	// 把地址换成一个必定拒绝连接的端口。
	conn, _ := st.Connection()
	conn.BaseURL = "http://127.0.0.1:1"
	conn.TimeoutSeconds = 5
	if err := st.SaveConnection(conn); err != nil {
		t.Fatalf("保存连接配置失败: %v", err)
	}

	start := time.Now()
	rec := doJSON(t, handler, http.MethodPut, "/api/channels/101", map[string]any{
		"priority": 3,
	})
	elapsed := time.Since(start)

	if rec.Code == http.StatusOK {
		t.Fatal("上游不可达时不应返回成功")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("上游不可达时耗时 %v，应快速失败", elapsed)
	}
	if !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("应返回错误信息，实际 %s", rec.Body.String())
	}
}
