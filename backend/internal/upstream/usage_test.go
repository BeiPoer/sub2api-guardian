package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestListUsageUsesAdminKeyQueryAndStopsAfterOldRecord(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 30, 0, 0, location)
	start := now.Add(-time.Hour)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Errorf("鉴权头异常: %q", r.Header.Get("x-api-key"))
		}
		query := r.URL.Query()
		for key, want := range map[string]string{
			"page_size": "100", "sort_by": "created_at", "sort_order": "desc",
			"timezone": "Asia/Shanghai", "exact_total": "false",
			"start_date": "2026-08-30", "end_date": "2026-08-31",
		} {
			if query.Get(key) != want {
				t.Errorf("查询参数 %s = %q，期望 %q", key, query.Get(key), want)
			}
		}
		var items []UsageRecord
		if query.Get("page") == "1" {
			items = make([]UsageRecord, 100)
			for i := range items {
				items[i] = UsageRecord{CreatedAt: now.Add(-10 * time.Minute).Format(time.RFC3339), FirstTokenMS: 30001}
			}
		} else if query.Get("page") == "2" {
			items = make([]UsageRecord, 100)
			items[0] = UsageRecord{CreatedAt: start.Add(-time.Second).Format(time.RFC3339)}
		} else {
			t.Errorf("提前停止后不应请求第 %s 页", query.Get("page"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"items": items, "page": 1, "page_size": 100, "pages": 10},
		})
	}))
	defer server.Close()

	client := New(server.URL, "admin-key", 5*time.Second)
	items, err := client.ListUsage(context.Background(), start, now, location.String())
	if err != nil {
		t.Fatalf("ListUsage() 失败: %v", err)
	}
	if len(items) != 200 || calls.Load() != 2 {
		t.Fatalf("分页结果/请求次数异常: items=%d calls=%d", len(items), calls.Load())
	}
}

func TestListUsageRetriesRetryableHTTPError(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"items": []any{}, "page": 1, "page_size": 100, "pages": 1},
		})
	}))
	defer server.Close()

	now := time.Now()
	client := New(server.URL, "key", 5*time.Second)
	if _, err := client.ListUsage(context.Background(), now.Add(-time.Hour), now, "UTC"); err != nil {
		t.Fatalf("可重试 503 最终失败: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("503 应重试一次，实际请求 %d 次", calls.Load())
	}
}

func TestParseUsageTimeAcceptsZTimestamp(t *testing.T) {
	parsed, ok := ParseUsageTime("2026-08-30T04:30:00.123Z", time.UTC)
	if !ok || parsed.Year() != 2026 || parsed.Nanosecond() != 123000000 {
		t.Fatalf("Z 时间解析异常: %v %v", parsed, ok)
	}
}
