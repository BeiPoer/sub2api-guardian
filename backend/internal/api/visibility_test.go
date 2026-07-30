package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

// multiGroupFake 提供跨分组的账号布局：
//
//	渠道 101 只属于 A 组
//	渠道 102 同属 A 组与 B 组
//	渠道 103 只属于 B 组
//
// 用来验证「分组被排除后渠道是否该出现在渠道池」的边界。
type multiGroupFake struct{}

func (f *multiGroupFake) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/admin/groups/all", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, []map[string]any{
			{"id": 1, "name": "A组", "platform": "gemini", "status": "active", "rate_multiplier": 1.0},
			{"id": 2, "name": "B组", "platform": "gemini", "status": "active", "rate_multiplier": 1.0},
		})
	})

	accounts := []map[string]any{
		{
			"id": 101, "name": "只在A组", "platform": "gemini", "type": "apikey",
			"status": "active", "schedulable": true, "priority": 10, "concurrency": 5,
			"rate_multiplier": 1.0, "group_ids": []int64{1},
		},
		{
			"id": 102, "name": "跨AB两组", "platform": "gemini", "type": "apikey",
			"status": "active", "schedulable": true, "priority": 10, "concurrency": 5,
			"rate_multiplier": 1.0, "group_ids": []int64{1, 2},
		},
		{
			"id": 103, "name": "只在B组", "platform": "gemini", "type": "apikey",
			"status": "active", "schedulable": true, "priority": 10, "concurrency": 5,
			"rate_multiplier": 1.0, "group_ids": []int64{2},
		},
	}

	mux.HandleFunc("/api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		group := r.URL.Query().Get("group")
		items := make([]map[string]any, 0, len(accounts))
		for _, account := range accounts {
			if group == "" {
				items = append(items, account)
				continue
			}
			for _, id := range account["group_ids"].([]int64) {
				if fmt.Sprint(id) == group {
					items = append(items, account)
					break
				}
			}
		}
		writeEnvelope(w, map[string]any{
			"items": items, "total": len(items), "page": 1, "page_size": 200, "pages": 1,
		})
	})

	mux.HandleFunc("/api/v1/admin/ops/requests", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "Ops monitoring is disabled"})
	})

	mux.HandleFunc("/api/v1/admin/accounts/", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, map[string]any{"ok": true})
	})

	return mux
}

func setupVisibilityAPI(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()

	server := httptest.NewServer((&multiGroupFake{}).handler())
	t.Cleanup(server.Close)

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

	testSessionToken = seedSession(t, st, "admin", "hunter2hunter2")

	client := upstream.New(server.URL, "k", 10*time.Second)
	eng := engine.New(st, client)
	apiServer := NewServer(st, client, eng, nil)
	t.Cleanup(apiServer.Close)

	handler := apiServer.Handler()
	syncCatalog(t, handler)
	return handler, st
}

// channelIDs 取出渠道池返回的渠道 ID。
func channelIDs(t *testing.T, handler http.Handler, query string) []int64 {
	t.Helper()
	rec := doJSON(t, handler, http.MethodGet, "/api/channels"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列出渠道失败: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	out := make([]int64, 0, len(payload.Items))
	for _, item := range payload.Items {
		out = append(out, item.ID)
	}
	return out
}

func contains(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestExcludedGroupHidesChannelsWithNoOtherGroup 是本次的核心预期：
//
// 分组被排除后，只属于该分组的渠道不该再出现在渠道池 —— 它已经完全脱离
// 调度系统的管辖范围，显示出来只会造成困惑。
// 但同时属于其他未排除分组的渠道必须保留。
func TestExcludedGroupHidesChannelsWithNoOtherGroup(t *testing.T) {
	handler, st := setupVisibilityAPI(t)

	// 排除前三个渠道都可见。
	ids := channelIDs(t, handler, "?managed=false")
	for _, want := range []int64{101, 102, 103} {
		if !contains(ids, want) {
			t.Fatalf("排除前渠道 %d 应可见，实际 %v", want, ids)
		}
	}

	// 排除 B 组。
	p, _ := st.Policy()
	p.ExcludedGroupIDs = []int64{2}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	ids = channelIDs(t, handler, "?managed=false")

	// 103 只属于 B 组：应彻底隐藏。
	if contains(ids, 103) {
		t.Fatalf("渠道 103 只属于被排除的 B 组，不该出现在渠道池，实际 %v", ids)
	}
	// 102 还属于未排除的 A 组：必须保留。
	if !contains(ids, 102) {
		t.Fatalf("渠道 102 仍属于未排除的 A 组，应保留在渠道池，实际 %v", ids)
	}
	// 101 与 B 组无关：不受影响。
	if !contains(ids, 101) {
		t.Fatalf("渠道 101 不属于 B 组，不该受影响，实际 %v", ids)
	}
}

// TestExcludedGroupRestoreBringsChannelsBack 验证恢复分组管控后渠道会回来。
func TestExcludedGroupRestoreBringsChannelsBack(t *testing.T) {
	handler, st := setupVisibilityAPI(t)

	p, _ := st.Policy()
	p.ExcludedGroupIDs = []int64{2}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if ids := channelIDs(t, handler, "?managed=false"); contains(ids, 103) {
		t.Fatal("排除后渠道 103 应隐藏")
	}

	p, _ = st.Policy()
	p.ExcludedGroupIDs = nil
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if ids := channelIDs(t, handler, "?managed=false"); !contains(ids, 103) {
		t.Fatalf("恢复管控后渠道 103 应重新可见，实际 %v", ids)
	}
}

// TestManuallyExcludedChannelStaysVisible 确认这次改动没有波及渠道级排除：
// 人工排除的单个渠道仍要能看到并恢复，否则用户无从取消排除。
func TestManuallyExcludedChannelStaysVisible(t *testing.T) {
	handler, st := setupVisibilityAPI(t)

	p, _ := st.Policy()
	p.ExcludedAccountIDs = []int64{103}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	ids := channelIDs(t, handler, "?managed=false")
	if !contains(ids, 103) {
		t.Fatalf("人工排除的渠道仍应可见（否则无法取消排除），实际 %v", ids)
	}
}

// channelByID 取出单个渠道的关键字段。
func channelByID(t *testing.T, handler http.Handler, id int64) (health string, excluded, paused bool) {
	t.Helper()
	rec := doJSON(t, handler, http.MethodGet, "/api/channels?managed=false", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列出渠道失败: %d", rec.Code)
	}
	var payload struct {
		Items []struct {
			ID       int64  `json:"id"`
			Health   string `json:"health"`
			Excluded bool   `json:"excluded"`
			Paused   bool   `json:"paused"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	for _, item := range payload.Items {
		if item.ID == id {
			return item.Health, item.Excluded, item.Paused
		}
	}
	t.Fatalf("渠道 %d 不在列表里", id)
	return "", false, false
}

// TestExcludeReflectsImmediatelyWithoutRound 是「开了显示已排除仍显示 0 个」的回归。
//
// 排除是即时生效的策略事实。早期版本的 health 只由引擎每轮写入，
// 于是点了排除但没跑调度时，health 仍是 healthy ——
// 渠道池按 health 统计「已排除」页签，就永远是 0。
func TestExcludeReflectsImmediatelyWithoutRound(t *testing.T) {
	handler, st := setupVisibilityAPI(t)

	// 刻意不跑 run-once，直接改策略。
	p, _ := st.Policy()
	p.ExcludedAccountIDs = []int64{101}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	health, excluded, _ := channelByID(t, handler, 101)
	if !excluded {
		t.Fatal("excluded 标记应立即生效")
	}
	if health != "excluded" {
		t.Fatalf("health = %q, 期望立即变为 excluded（不必等下一轮调度）", health)
	}
}

// TestPauseReflectsImmediatelyWithoutRound 暂停同理。
func TestPauseReflectsImmediatelyWithoutRound(t *testing.T) {
	handler, st := setupVisibilityAPI(t)

	p, _ := st.Policy()
	p.PausedAccountIDs = []int64{101}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	health, _, paused := channelByID(t, handler, 101)
	if !paused {
		t.Fatal("paused 标记应立即生效")
	}
	if health != "paused" {
		t.Fatalf("health = %q, 期望立即变为 paused", health)
	}
}

// TestUnexcludeReflectsImmediately 反向也要成立：
// 移出名单后不能因为状态没刷新而继续显示「已排除」。
func TestUnexcludeReflectsImmediately(t *testing.T) {
	handler, st := setupVisibilityAPI(t)

	p, _ := st.Policy()
	p.ExcludedAccountIDs = []int64{101}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if health, _, _ := channelByID(t, handler, 101); health != "excluded" {
		t.Fatalf("排除后 health = %q", health)
	}

	p, _ = st.Policy()
	p.ExcludedAccountIDs = nil
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	health, excluded, _ := channelByID(t, handler, 101)
	if excluded {
		t.Fatal("已移出名单，excluded 应为 false")
	}
	if health == "excluded" {
		t.Fatal("已移出名单，health 不该仍为 excluded")
	}
}

// TestExcludedGroupHiddenFromGroupsView 分组视图里也不该再挂着这些渠道。
func TestExcludedGroupHiddenFromGroupsView(t *testing.T) {
	handler, st := setupVisibilityAPI(t)

	p, _ := st.Policy()
	p.ExcludedGroupIDs = []int64{2}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	rec := doJSON(t, handler, http.MethodGet, "/api/groups", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列出分组失败: %d", rec.Code)
	}
	var payload struct {
		Items []struct {
			ID       int64 `json:"id"`
			Excluded bool  `json:"excluded"`
			Channels []struct {
				ID int64 `json:"id"`
			} `json:"channels"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	for _, group := range payload.Items {
		if group.ID != 2 {
			continue
		}
		if !group.Excluded {
			t.Fatal("B 组应标记为已排除")
		}
		for _, ch := range group.Channels {
			if ch.ID == 103 {
				t.Fatal("被排除分组下只属于它的渠道不该再挂在分组卡上")
			}
		}
	}
}
