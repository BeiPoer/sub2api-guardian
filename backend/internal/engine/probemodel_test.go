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

// modelSpyFake 记录每次探测请求实际用了哪个模型。
type modelSpyFake struct {
	mu sync.Mutex
	// models 按账号记录收到的 model_id，用来断言「指定模型真的被用上了」。
	models map[int64][]string
	// available 是 /models 接口返回的模型列表。
	available []string

	// rewriteTo 非空时，SSE 回传该模型，模拟 sub2api 的账号级模型映射。
	rewriteTo string
}

func newModelSpyFake(available ...string) *modelSpyFake {
	return &modelSpyFake{models: map[int64][]string{}, available: available}
}

func (f *modelSpyFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/admin/groups/all", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, []map[string]any{
			{"id": 1, "name": "分组", "platform": "gemini", "status": "active", "rate_multiplier": 1.0},
		})
	})

	mux.HandleFunc("/api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, map[string]any{
			"items": []map[string]any{{
				"id": 101, "name": "渠道", "platform": "gemini", "type": "apikey",
				"status": "active", "schedulable": true, "priority": 10,
				"concurrency": 5, "rate_multiplier": 1.0, "group_ids": []int64{1},
			}},
			"total": 1, "page": 1, "page_size": 200, "pages": 1,
		})
	})

	mux.HandleFunc("/api/v1/admin/ops/requests", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "Ops monitoring is disabled"})
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

		switch {
		case action == "test" && r.Method == http.MethodPost:
			var payload struct {
				ModelID string `json:"model_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			f.mu.Lock()
			f.models[accountID] = append(f.models[accountID], payload.ModelID)
			f.mu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			send := func(v map[string]any) {
				raw, _ := json.Marshal(v)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
				if flusher != nil {
					flusher.Flush()
				}
			}
			actual := payload.ModelID
			if f.rewriteTo != "" {
				actual = f.rewriteTo
			}
			send(map[string]any{"type": "test_start", "model": actual})
			send(map[string]any{"type": "content", "text": "hi"})
			send(map[string]any{"type": "test_complete", "success": true})

		case action == "models" && r.Method == http.MethodGet:
			writeEnvelope(w, f.available)
		default:
			writeEnvelope(w, map[string]any{"ok": true})
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func (f *modelSpyFake) usedModels(accountID int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models[accountID]...)
}

func (f *modelSpyFake) lastModel(accountID int64) string {
	used := f.usedModels(accountID)
	if len(used) == 0 {
		return ""
	}
	return used[len(used)-1]
}

func setupModelSpy(t *testing.T, fake *modelSpyFake) (*Engine, *store.Store) {
	t.Helper()
	server := fake.server(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SaveConnection(domain.Connection{
		BaseURL: server.URL, AdminAPIKey: "k", TimeoutSeconds: 10, Enabled: false,
	}); err != nil {
		t.Fatalf("保存连接失败: %v", err)
	}

	p, _ := st.Policy()
	p.Probe.IntervalSeconds = 30
	p.Probe.SkipWhenTrafficFresh = false
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	client := upstream.New(server.URL, "k", 10*time.Second)
	return New(st, client), st
}

// TestAccountTestModelWins 是核心回归：渠道单独指定的模型优先级最高。
//
// 用户报告指定 gemini-3-flash 后，探测仍用了别的模型导致 500。
func TestAccountTestModelWins(t *testing.T) {
	fake := newModelSpyFake("gemini-1.5-pro", "gemini-2.0-flash")
	eng, st := setupModelSpy(t, fake)
	ctx := context.Background()

	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	// 全局默认模型是另一个，分组也设了第三个，账号级设 gemini-3-flash。
	p, _ := st.Policy()
	p.Probe.Model = "global-model"
	p.AccountTestModels = map[string]string{"101": "gemini-3-flash"}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	groupModel := "group-model"
	if err := st.SaveGroupOverride(1, policy.GroupOverride{ProbeModel: &groupModel}); err != nil {
		t.Fatalf("保存分组覆盖失败: %v", err)
	}

	if _, err := eng.ProbeAccount(ctx, 101); err != nil {
		t.Fatalf("探测失败: %v", err)
	}

	if got := fake.lastModel(101); got != "gemini-3-flash" {
		t.Fatalf("探测使用的模型 = %q, 期望账号指定的 gemini-3-flash（账号级优先级最高）", got)
	}
}

// TestAccountTestModelWinsInScheduledRound 验证自动调度轮次里同样生效。
func TestAccountTestModelWinsInScheduledRound(t *testing.T) {
	fake := newModelSpyFake("gemini-1.5-pro")
	eng, st := setupModelSpy(t, fake)
	ctx := context.Background()

	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	p, _ := st.Policy()
	p.Probe.Model = "global-model"
	p.AccountTestModels = map[string]string{"101": "gemini-3-flash"}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	used := fake.usedModels(101)
	if len(used) == 0 {
		t.Fatal("本轮应对渠道做过探测")
	}
	for i, model := range used {
		if model != "gemini-3-flash" {
			t.Fatalf("第 %d 次探测用了 %q, 期望始终是账号指定的 gemini-3-flash", i+1, model)
		}
	}
}

// TestGroupModelBeatsGlobal 验证没有账号级设置时，分组模型优先于全局。
func TestGroupModelBeatsGlobal(t *testing.T) {
	fake := newModelSpyFake("gemini-1.5-pro")
	eng, st := setupModelSpy(t, fake)
	ctx := context.Background()

	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	p, _ := st.Policy()
	p.Probe.Model = "global-model"
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	groupModel := "group-model"
	if err := st.SaveGroupOverride(1, policy.GroupOverride{ProbeModel: &groupModel}); err != nil {
		t.Fatalf("保存分组覆盖失败: %v", err)
	}

	if _, err := eng.ProbeAccount(ctx, 101); err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if got := fake.lastModel(101); got != "group-model" {
		t.Fatalf("探测使用的模型 = %q, 期望分组指定的 group-model", got)
	}
}

// TestModelRewriteIsDetected 是「指定模型没生效」的核心回归。
//
// sub2api 对 apikey 账号会应用账号级通配符模型映射（Account.GetMappedModel），
// 把 Guardian 请求的模型重写掉。Guardian 无法阻止，但必须把偏差暴露出来，
// 否则用户只会看到莫名的 500 而不知道模型被换了。
func TestModelRewriteIsDetected(t *testing.T) {
	fake := newModelSpyFake("gemini-1.5-pro")
	// 模拟映射：无论请求什么，都回传 mapped-by-sub2api。
	fake.rewriteTo = "mapped-by-sub2api"

	eng, st := setupModelSpy(t, fake)
	ctx := context.Background()

	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	p, _ := st.Policy()
	p.AccountTestModels = map[string]string{"101": "gemini-3-flash"}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	if _, err := eng.ProbeAccount(ctx, 101); err != nil {
		t.Fatalf("探测失败: %v", err)
	}

	// Guardian 请求的仍是我们指定的模型。
	if got := fake.lastModel(101); got != "gemini-3-flash" {
		t.Fatalf("请求的模型 = %q, 期望 gemini-3-flash", got)
	}

	state, err := st.ChannelState(101)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if !state.ModelRewritten {
		t.Fatal("sub2api 换了模型，应标记 ModelRewritten")
	}
	if state.LastRequestModel != "gemini-3-flash" {
		t.Fatalf("记录的请求模型 = %q, 期望 gemini-3-flash", state.LastRequestModel)
	}
	if state.LastProbeModel != "mapped-by-sub2api" {
		t.Fatalf("记录的实际模型 = %q, 期望 mapped-by-sub2api", state.LastProbeModel)
	}

	// 必须留下可检索的告警，这是用户排查的唯一线索。
	events, _, err := st.Events(store.EventFilter{Action: "probe_model_rewritten", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("模型被改写时应写入 probe_model_rewritten 告警")
	}
	if !strings.Contains(events[0].Message, "gemini-3-flash") ||
		!strings.Contains(events[0].Message, "mapped-by-sub2api") {
		t.Fatalf("告警应同时含请求与实际模型，实际: %s", events[0].Message)
	}

	// 样本里两个模型都要留痕。
	samples, err := st.RecentSamples(101, 5)
	if err != nil || len(samples) == 0 {
		t.Fatalf("应有样本: %v", err)
	}
	if samples[0].RequestModel != "gemini-3-flash" || samples[0].Model != "mapped-by-sub2api" {
		t.Fatalf("样本模型记录不正确: request=%q actual=%q",
			samples[0].RequestModel, samples[0].Model)
	}
}

// TestNoRewriteWhenModelsMatch 验证模型一致时不误报。
func TestNoRewriteWhenModelsMatch(t *testing.T) {
	fake := newModelSpyFake("gemini-1.5-pro")
	eng, st := setupModelSpy(t, fake)
	ctx := context.Background()

	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	p, _ := st.Policy()
	p.AccountTestModels = map[string]string{"101": "gemini-3-flash"}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	if _, err := eng.ProbeAccount(ctx, 101); err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	state, _ := st.ChannelState(101)
	if state.ModelRewritten {
		t.Fatal("模型一致时不应标记 ModelRewritten")
	}
	events, _, _ := st.Events(store.EventFilter{Action: "probe_model_rewritten", Page: 1, PageSize: 10})
	if len(events) != 0 {
		t.Fatal("模型一致时不应产生改写告警")
	}
}

// TestGlobalModelUsedWhenNoOverride 验证都没设时用全局默认。
func TestGlobalModelUsedWhenNoOverride(t *testing.T) {
	fake := newModelSpyFake("gemini-1.5-pro")
	eng, st := setupModelSpy(t, fake)
	ctx := context.Background()

	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	p, _ := st.Policy()
	p.Probe.Model = "global-model"
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	if _, err := eng.ProbeAccount(ctx, 101); err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if got := fake.lastModel(101); got != "global-model" {
		t.Fatalf("探测使用的模型 = %q, 期望全局默认 global-model", got)
	}
}
