package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

func TestSyncUsesAccountGroupIDsAndFallsBackForLegacyServers(t *testing.T) {
	tests := []struct {
		name             string
		includeGroupIDs  bool
		wantGroupQueries int
	}{
		{name: "current response", includeGroupIDs: true, wantGroupQueries: 0},
		{name: "legacy response", includeGroupIDs: false, wantGroupQueries: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			groupQueries := 0
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/admin/groups/all", func(w http.ResponseWriter, _ *http.Request) {
				writeEnvelope(w, []map[string]any{
					{"id": 1, "name": "一组", "status": "active"},
					{"id": 2, "name": "二组", "status": "active"},
				})
			})
			mux.HandleFunc("/api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
				group := r.URL.Query().Get("group")
				if group != "" {
					mu.Lock()
					groupQueries++
					mu.Unlock()
				}

				items := []map[string]any{}
				switch group {
				case "1":
					items = append(items, syncTestAccount(101, nil))
				case "2":
					items = append(items, syncTestAccount(102, nil))
				default:
					var firstGroups, secondGroups []int64
					if tc.includeGroupIDs {
						firstGroups = []int64{1}
						secondGroups = []int64{2}
					}
					items = append(items,
						syncTestAccount(101, firstGroups),
						syncTestAccount(102, secondGroups),
					)
				}
				writeEnvelope(w, map[string]any{
					"items": items, "total": len(items), "page": 1, "page_size": 200, "pages": 1,
				})
			})
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)

			st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
			if err != nil {
				t.Fatalf("打开数据库失败: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			client := upstream.New(server.URL, "key", 5*time.Second)
			eng := New(st, client)

			if err := eng.Sync(context.Background()); err != nil {
				t.Fatalf("同步失败: %v", err)
			}
			mu.Lock()
			gotQueries := groupQueries
			mu.Unlock()
			if gotQueries != tc.wantGroupQueries {
				t.Fatalf("按分组查询次数 = %d，期望 %d", gotQueries, tc.wantGroupQueries)
			}
			accounts, err := st.Accounts()
			if err != nil || len(accounts) != 2 {
				t.Fatalf("同步账号 = %d / %v，期望 2 个", len(accounts), err)
			}
			for _, account := range accounts {
				groups := account.GroupIDSet()
				if len(groups) != 1 || groups[0] != account.ID-100 {
					t.Fatalf("账号 %d 分组 = %v", account.ID, groups)
				}
			}
		})
	}
}

func syncTestAccount(id int64, groupIDs []int64) map[string]any {
	return map[string]any{
		"id": id, "name": "渠道", "platform": "anthropic", "type": "apikey",
		"status": "active", "schedulable": true, "priority": 10, "concurrency": 5,
		"rate_multiplier": 1.0, "group_ids": groupIDs,
	}
}
