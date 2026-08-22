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

// cleanupFake 记录 sub2api 收到的删除与停用请求。
type cleanupFake struct {
	mu            sync.Mutex
	deleted       map[int64]bool
	unschedulable map[int64]bool
	inactive      map[int64]bool
	failNext      bool
}

func newCleanupFake() *cleanupFake {
	return &cleanupFake{
		deleted:       map[int64]bool{},
		inactive:      map[int64]bool{},
		unschedulable: map[int64]bool{},
	}
}

func (f *cleanupFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/accounts/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/")
		var id int64
		if _, err := fmt.Sscanf(strings.Split(rest, "/")[0], "%d", &id); err != nil {
			http.NotFound(w, r)
			return
		}

		f.mu.Lock()
		shouldFail := f.failNext
		f.mu.Unlock()
		if shouldFail {
			http.Error(w, `{"code":500,"message":"boom"}`, http.StatusInternalServerError)
			return
		}

		switch r.Method {
		case http.MethodDelete:
			f.mu.Lock()
			f.deleted[id] = true
			f.mu.Unlock()
		case http.MethodPost:
			// POST /accounts/{id}/schedulable —— 删除前摘流量走的就是这条。
			if strings.Contains(rest, "schedulable") {
				var payload struct {
					Schedulable bool `json:"schedulable"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if !payload.Schedulable {
					f.mu.Lock()
					f.unschedulable[id] = true
					f.mu.Unlock()
				}
			}
		case http.MethodPut:
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["status"] == "inactive" {
				f.mu.Lock()
				f.inactive[id] = true
				f.mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"ok": true}})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func (f *cleanupFake) wasDeleted(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleted[id]
}

func (f *cleanupFake) wasInactive(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inactive[id]
}

// wasUnschedulable 报告是否调用过 schedulable=false（删除前摘流量）。
func (f *cleanupFake) wasUnschedulable(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unschedulable[id]
}

// cleanupEngine 组装一个可直接调 applyCleanup 的引擎。
func cleanupEngine(t *testing.T, fake *cleanupFake) (*Engine, *store.Store) {
	t.Helper()
	server := fake.server(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	client := upstream.New(server.URL, "test-key", 5*time.Second)
	return New(st, client), st
}

// authFailChannel 构造一个「已熔断 + 反复认证失效」的渠道。
func authFailChannel(id int64, p policy.Policy, fatalCount int, fusedFor time.Duration) *channel {
	policy.Normalize(&p)
	now := time.Now()

	samples := make([]domain.Sample, 0, fatalCount)
	for i := 0; i < fatalCount; i++ {
		samples = append(samples, domain.Sample{
			AccountID:  id,
			OccurredAt: now.Add(-time.Duration(i) * time.Minute),
			EventType:  domain.EventFatal,
			Score:      0,
			StatusCode: 401,
			Message:    "401 Unauthorized: invalid api key",
		})
	}

	ch := &channel{
		account: domain.Account{
			ID: id, Name: "渠道" + itoa(id), Platform: "anthropic",
			Type: "apikey", Status: "active", Priority: 10, Concurrency: 5,
			RateMultiplier: 1, Schedulable: false,
		},
		samples:      samples,
		pol:          p,
		primaryGroup: 1,
		groupIDs:     []int64{1},
	}
	ch.state.AccountID = id
	ch.state.Health = domain.HealthFused
	ch.state.HealthSince = now.Add(-fusedFor)
	ch.state.CleanupEligibleSince = now.Add(-fusedFor)
	ch.desired.health = domain.HealthFused
	return ch
}

func cleanupRound(p policy.Policy, channels ...*channel) *round {
	policy.Normalize(&p)
	group := domain.Group{ID: 1, Name: "测试分组", RateMultiplier: 1}
	r := &round{
		now:          time.Now(),
		global:       p,
		overrides:    map[int64]*policy.GroupOverride{},
		groups:       []domain.Group{group},
		groupByID:    map[int64]domain.Group{1: group},
		byAccountID:  map[int64]*channel{},
		groupMembers: map[int64][]*channel{},
		softFuses:    map[int64]int{},
	}
	for _, ch := range channels {
		ch.pol = p
		r.channels = append(r.channels, ch)
		r.byAccountID[ch.account.ID] = ch
		r.groupMembers[1] = append(r.groupMembers[1], ch)
	}
	return r
}

// enabledCleanup 返回一份打开清理、且守卫条件容易满足的策略。
func enabledCleanup(action policy.FatalAction) policy.Policy {
	p := policy.Default()
	p.Cleanup.Enabled = true
	p.Cleanup.Action = action
	p.Cleanup.Occurrences = 3
	p.Cleanup.Window = 5
	p.Cleanup.MinFusedMinutes = 30
	p.Cleanup.MaxPerRound = 1
	p.Cleanup.KeepLastInGroup = true
	p.Cleanup.OnlyAuthErrors = true
	policy.Normalize(&p)
	return p
}

func TestCleanupDisabledByDefault(t *testing.T) {
	if policy.Default().Cleanup.Enabled {
		t.Fatal("自动清理必须默认关闭：它是不可逆或会脱离调度的操作")
	}
	if policy.Default().Cleanup.Action == policy.FatalActionDelete {
		t.Fatal("默认动作不应是删除")
	}
}

func TestCleanupSkippedWhenDisabled(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)

	p := enabledCleanup(policy.FatalActionDelete)
	p.Cleanup.Enabled = false

	bad := authFailChannel(101, p, 5, time.Hour)
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, bad, spare)); got != 0 {
		t.Fatalf("关闭时不应处置任何渠道，实际 %d", got)
	}
	if fake.wasDeleted(101) {
		t.Fatal("关闭时不应删除渠道")
	}
}

func TestCleanupDeletesAfterRepeatedAuthFailures(t *testing.T) {
	fake := newCleanupFake()
	eng, st := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	bad := authFailChannel(101, p, 5, time.Hour)
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, bad, spare)); got != 1 {
		t.Fatalf("处置数 = %d, 期望 1", got)
	}
	if !fake.wasDeleted(101) {
		t.Fatal("应删除认证反复失效的渠道")
	}
	if fake.wasDeleted(102) {
		t.Fatal("健康渠道不应被删除")
	}

	// 删除前后都要留痕，且快照要能识别出删的是哪个渠道。
	events, _, err := st.Events(store.EventFilter{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	var pending, deleted *domain.Event
	for i := range events {
		switch events[i].Action {
		case "cleanup_delete_pending":
			pending = &events[i]
		case "cleanup_deleted":
			deleted = &events[i]
		}
	}
	if pending == nil {
		t.Fatal("删除前应先写入待删除快照")
	}
	if deleted == nil {
		t.Fatal("删除后应写入结果事件")
	}
	if !strings.Contains(pending.Detail, `"account_id":101`) {
		t.Fatalf("快照应包含账号信息，实际 %s", pending.Detail)
	}
	if !strings.Contains(deleted.Message, "无法重建") {
		t.Fatalf("删除事件应提示不可重建，实际 %s", deleted.Message)
	}
}

func TestCleanupKeepsLastChannelInGroup(t *testing.T) {
	fake := newCleanupFake()
	eng, st := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	// 组内唯一渠道，即使认证反复失效也不能删。
	only := authFailChannel(101, p, 5, time.Hour)

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, only)); got != 0 {
		t.Fatalf("不应处置分组内最后一个渠道，实际处置 %d 个", got)
	}
	if fake.wasDeleted(101) {
		t.Fatal("分组内最后一个渠道被删除了，这会导致分组彻底断供")
	}

	events, _, _ := st.Events(store.EventFilter{Action: "cleanup_skipped", Page: 1, PageSize: 10})
	if len(events) == 0 {
		t.Fatal("跳过时应留下说明事件")
	}
}

func TestCleanupRespectsMaxPerRound(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)
	p.Cleanup.MaxPerRound = 1

	bad1 := authFailChannel(101, p, 5, time.Hour)
	bad2 := authFailChannel(102, p, 5, time.Hour)
	bad3 := authFailChannel(103, p, 5, time.Hour)
	spare := authFailChannel(104, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	got := eng.applyCleanup(context.Background(), cleanupRound(p, bad1, bad2, bad3, spare))
	if got != 1 {
		t.Fatalf("每轮处置数 = %d, 期望 1", got)
	}

	deletedCount := 0
	for _, id := range []int64{101, 102, 103} {
		if fake.wasDeleted(id) {
			deletedCount++
		}
	}
	if deletedCount != 1 {
		t.Fatalf("实际删除 %d 个，期望受每轮上限约束只删 1 个", deletedCount)
	}
}

func TestCleanupRequiresMinFusedDuration(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)
	p.Cleanup.MinFusedMinutes = 30

	// 刚熔断 1 分钟：还在人工介入窗口内，不该动手。
	fresh := authFailChannel(101, p, 5, time.Minute)
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, fresh, spare)); got != 0 {
		t.Fatalf("未满最短熔断时长不应处置，实际 %d", got)
	}
	if fake.wasDeleted(101) {
		t.Fatal("刚熔断的渠道被立即删除了")
	}
}

func TestCleanupObservationStartsWhenConditionFirstMatches(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	bad := authFailChannel(101, p, 5, 24*time.Hour)
	bad.state.Health = domain.HealthDegraded
	bad.state.HealthSince = time.Now().Add(-24 * time.Hour)
	bad.state.CleanupEligibleSince = time.Time{}
	bad.desired.health = domain.HealthDegraded
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, bad, spare)); got != 0 {
		t.Fatalf("首次达到处置条件必须重新开始观察，实际处置 %d 个", got)
	}
	if bad.state.CleanupEligibleSince.IsZero() {
		t.Fatal("首次达到处置条件时应记录独立的观察起点")
	}
	if fake.wasDeleted(101) {
		t.Fatal("长期降级渠道刚出现认证失效时不应立即删除")
	}
}

func TestCleanupRequiresEnoughOccurrences(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)
	p.Cleanup.Occurrences = 3

	// 只有 1 次认证失败：可能是抖动，不该动手。
	once := authFailChannel(101, p, 1, time.Hour)
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, once, spare)); got != 0 {
		t.Fatalf("次数不足不应处置，实际 %d", got)
	}
}

// TestCleanupIgnoresQuotaErrors 是很重要的一条：
// 余额不足、额度耗尽充值就能恢复，绝不能当成凭据失效删掉。
func TestCleanupIgnoresQuotaErrors(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	broke := authFailChannel(101, p, 0, time.Hour)
	now := time.Now()
	for i := 0; i < 5; i++ {
		broke.samples = append(broke.samples, domain.Sample{
			AccountID:  101,
			OccurredAt: now.Add(-time.Duration(i) * time.Minute),
			EventType:  domain.EventFatal,
			Message:    "insufficient balance, please recharge",
		})
	}
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, broke, spare)); got != 0 {
		t.Fatalf("余额不足不应触发删除，实际处置 %d 个", got)
	}
	if fake.wasDeleted(101) {
		t.Fatal("欠费渠道被删除了，充值即可恢复的账号不该被清理")
	}
}

// statusChannel 构造一个带指定状态码的熔断渠道。
func statusChannel(id int64, p policy.Policy, status int, count int) *channel {
	ch := authFailChannel(id, p, 0, time.Hour)
	now := time.Now()
	for i := 0; i < count; i++ {
		ch.samples = append(ch.samples, domain.Sample{
			AccountID:  id,
			OccurredAt: now.Add(-time.Duration(i) * time.Minute),
			EventType:  domain.EventFatal,
			StatusCode: status,
			Message:    fmt.Sprintf("upstream returned %d", status),
		})
	}
	return ch
}

// TestCleanupTriggerStatusCodes 验证可配置错误码：只处置列表内的状态码。
func TestCleanupTriggerStatusCodes(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)
	p.Cleanup.TriggerStatusCodes = []int{403}
	policy.Normalize(&p)

	// 401 不在列表里，不该被处置。
	unlisted := statusChannel(101, p, 401, 5)
	// 403 在列表里，应被处置。
	listed := statusChannel(102, p, 403, 5)
	spare := authFailChannel(103, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, unlisted, listed, spare)); got != 1 {
		t.Fatalf("处置数 = %d, 期望 1", got)
	}
	if fake.wasDeleted(101) {
		t.Fatal("401 不在配置的状态码列表里，不应被删除")
	}
	if !fake.wasDeleted(102) {
		t.Fatal("403 在配置的状态码列表里，应被删除")
	}
}

// TestCleanupStatusCodesOverrideAuthOnly 验证状态码配置优先于「仅凭据失效」：
// 显式把 402（余额不足）加进列表时，它应该被处置。
func TestCleanupStatusCodesOverrideAuthOnly(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)
	p.Cleanup.OnlyAuthErrors = true // 即便开着，状态码列表也优先
	p.Cleanup.TriggerStatusCodes = []int{402}
	policy.Normalize(&p)

	quota := statusChannel(101, p, 402, 5)
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, quota, spare)); got != 1 {
		t.Fatalf("显式配置 402 后应被处置，实际 %d", got)
	}
}

// TestCleanupDefaultStatusCodes 验证默认状态码列表是 401/403。
func TestCleanupDefaultStatusCodes(t *testing.T) {
	got := policy.Default().Cleanup.TriggerStatusCodes
	if len(got) != 2 || got[0] != 401 || got[1] != 403 {
		t.Fatalf("默认触发状态码 = %v, 期望 [401 403]", got)
	}
}

// TestCleanupRejectsInvalidStatusCodes 验证非法状态码被清理掉。
func TestCleanupRejectsInvalidStatusCodes(t *testing.T) {
	p := policy.Default()
	p.Cleanup.TriggerStatusCodes = []int{401, 0, -5, 999, 403, 401}
	policy.Normalize(&p)

	got := p.Cleanup.TriggerStatusCodes
	if len(got) != 2 || got[0] != 401 || got[1] != 403 {
		t.Fatalf("规范化后 = %v, 期望去重并剔除非法值后的 [401 403]", got)
	}
}

// TestCleanupHandlesDegradedChannels 是「配了 401 却不删」的核心回归。
//
// 判定不再要求「已经熔断」。早期实现要求 desired.health == fused，而
// 「网关错误只降级不熔断」成为默认之后，401 渠道往往停在降级、永远进不了熔断，
// 于是用户配了 401 自动删除却什么都不会发生。
//
// 安全性改由观察期、保留组内最后一个、每轮上限、以及删除前先摘流量来保证。
func TestCleanupHandlesDegradedChannels(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	// 命中失效样本，但当前只是降级而非熔断。
	degraded := authFailChannel(101, p, 5, time.Hour)
	degraded.desired.health = domain.HealthDegraded
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, degraded, spare)); got != 1 {
		t.Fatalf("降级但命中错误码的渠道应被处置，实际处置 %d 个", got)
	}
	if !fake.wasDeleted(101) {
		t.Fatal("应删除命中错误码的渠道")
	}
	if fake.wasDeleted(102) {
		t.Fatal("健康渠道不该被删除")
	}
}

// TestCleanupSkipsSurvivor 确认保底强留的渠道不会被清理。
//
// 它是分组最后的防线，优先级高于自动处置 —— 删掉它整组就断供了。
func TestCleanupSkipsSurvivor(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	survivor := authFailChannel(101, p, 5, time.Hour)
	survivor.desired.health = domain.HealthSurvivor
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, survivor, spare)); got != 0 {
		t.Fatalf("保底强留的渠道不该被处置，实际处置 %d 个", got)
	}
	if fake.wasDeleted(101) {
		t.Fatal("保底渠道被删除会让分组断供")
	}
}

// TestCleanupDisablesBeforeDelete 确认删除前先把流量摘掉。
//
// 判定不再要求先熔断，所以待删渠道可能仍在接流量；
// 先置 schedulable=false 再删，避免飞行中的请求打到即将消失的账号上。
func TestCleanupDisablesBeforeDelete(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	bad := authFailChannel(101, p, 5, time.Hour)
	bad.desired.health = domain.HealthDegraded
	bad.account.Schedulable = true // 仍在接流量
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, bad, spare)); got != 1 {
		t.Fatalf("应处置 1 个，实际 %d", got)
	}
	if !fake.wasUnschedulable(101) {
		t.Fatal("删除前应先把 schedulable 置为 false，把流量摘掉")
	}
	if !fake.wasDeleted(101) {
		t.Fatal("摘完流量后应继续删除")
	}
}

func TestCleanupPauseAction(t *testing.T) {
	fake := newCleanupFake()
	eng, st := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionPause)
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	bad := authFailChannel(101, p, 5, time.Hour)
	bad.account.Schedulable = true
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, bad, spare)); got != 1 {
		t.Fatalf("处置数 = %d, 期望 1", got)
	}
	if fake.wasDeleted(101) {
		t.Fatal("暂停动作不应删除账号")
	}
	if !fake.wasUnschedulable(101) {
		t.Fatal("暂停动作必须在当前轮次立即写入 schedulable=false")
	}

	saved, _ := st.Policy()
	if !saved.AccountPaused(101) {
		t.Fatal("渠道应被加入暂停名单")
	}
}

func TestCleanupDisableAction(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDisable)

	bad := authFailChannel(101, p, 5, time.Hour)
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, bad, spare)); got != 1 {
		t.Fatalf("处置数 = %d, 期望 1", got)
	}
	if !fake.wasInactive(101) {
		t.Fatal("应把账号置为停用")
	}
	if fake.wasDeleted(101) {
		t.Fatal("停用动作不应删除账号")
	}
}

func TestCleanupDeleteFailureIsRecorded(t *testing.T) {
	fake := newCleanupFake()
	fake.failNext = true
	eng, st := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	bad := authFailChannel(101, p, 5, time.Hour)
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, bad, spare)); got != 0 {
		t.Fatalf("删除失败时不应计入处置数，实际 %d", got)
	}

	events, _, _ := st.Events(store.EventFilter{Action: "cleanup_failed", Page: 1, PageSize: 10})
	if len(events) == 0 {
		t.Fatal("删除失败应写入错误事件")
	}
}

func TestCleanupExcludedChannelUntouched(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)

	excluded := authFailChannel(101, p, 5, time.Hour)
	excluded.excluded = true
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, excluded, spare)); got != 0 {
		t.Fatalf("被排除的渠道不应参与清理，实际 %d", got)
	}
}
