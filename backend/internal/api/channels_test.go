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
	groupCalls   atomic.Int64
	accountCalls atomic.Int64
	updateCalls  atomic.Int64

	// groupCount 控制假分组数量，用来放大 Sync 的调用次数。
	groupCount int
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
		writeEnvelope(w, map[string]any{
			"items": []map[string]any{{
				"id": 101, "name": "渠道A", "platform": "anthropic", "type": "apikey",
				"status": "active", "schedulable": true, "priority": 10, "concurrency": 5,
				"rate_multiplier": 1.0, "group_ids": []int64{1},
			}},
			"total": 1, "page": 1, "page_size": 200, "pages": 1,
		})
	})

	mux.HandleFunc("/api/v1/admin/accounts/", func(w http.ResponseWriter, r *http.Request) {
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
