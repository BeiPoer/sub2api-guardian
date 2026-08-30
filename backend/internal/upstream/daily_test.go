package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetDailyReportStatsUsesDateRangesAndAggregatesSourceData(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, time.August, 30, 0, 0, 0, 0, location)
	end := time.Date(2026, time.August, 30, 23, 0, 0, 0, location)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Errorf("鉴权头异常: %q", r.Header.Get("x-api-key"))
		}
		query := r.URL.Query()
		switch r.URL.Path {
		case "/api/v1/admin/usage/stats":
			for key, want := range map[string]string{
				"start_date": "2026-08-30", "end_date": "2026-08-30",
				"timezone": "Asia/Shanghai", "nocache": "true",
			} {
				if query.Get(key) != want {
					t.Errorf("usage 参数 %s = %q，期望 %q", key, query.Get(key), want)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"total_actual_cost": "12.34", "total_tokens": "5678"},
			})
		case "/api/v1/admin/users":
			if query.Get("page_size") != "1000" || query.Get("sort_by") != "created_at" || query.Get("sort_order") != "desc" {
				t.Errorf("用户分页参数异常: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"items": []any{
						map[string]any{"created_at": "2026-08-30T03:00:00Z"},
						map[string]any{"created_at": "2026-08-29T15:59:59Z"},
					},
					"page": 1, "page_size": 1000, "pages": 1,
				},
			})
		case "/api/v1/admin/payment/orders":
			if query.Get("order_type") != "balance" || query.Get("page_size") != "1000" {
				t.Errorf("充值订单参数异常: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"items": []any{
						map[string]any{"status": "COMPLETED", "order_type": "balance", "user_id": 10, "pay_amount": "10", "currency": "CNY", "paid_at": "2026-08-30T04:00:00Z", "created_at": "2026-08-30T04:00:00Z"},
						map[string]any{"status": "COMPLETED", "order_type": "balance", "user_id": 10, "pay_amount": 1, "currency": "CNY", "paid_at": "2026-08-30T04:30:00Z", "created_at": "2026-08-30T04:30:00Z"},
						map[string]any{"status": "PAID", "order_type": "balance", "user_id": 20, "pay_amount": 2.5, "currency": "USD", "paid_at": "2026-08-30T05:00:00Z", "created_at": "2026-08-30T05:00:00Z"},
						map[string]any{"status": "PENDING", "order_type": "balance", "pay_amount": 99, "currency": "CNY", "paid_at": nil, "created_at": "2026-08-30T06:00:00Z"},
					},
					"page": 1, "page_size": 1000, "pages": 1,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stats, err := New(server.URL, "admin-key", 5*time.Second).GetDailyReportStats(context.Background(), start, end, location.String())
	if err != nil {
		t.Fatalf("GetDailyReportStats() 失败: %v", err)
	}
	if stats.TotalActualCost != 12.34 || stats.TotalTokens != 5678 || stats.NewUsers != 1 {
		t.Fatalf("每日统计聚合异常: %+v", stats)
	}
	if len(stats.RechargeAmounts) != 2 || stats.RechargeAmounts["CNY"] != 11 || stats.RechargeAmounts["USD"] != 2.5 || stats.RechargeUsers != 2 {
		t.Fatalf("充值币种汇总异常: %+v", stats.RechargeAmounts)
	}
}

func TestGetDailyReportStatsStopsUserAndOrderPaginationAfterWindow(t *testing.T) {
	location := time.UTC
	start := time.Date(2026, time.August, 30, 0, 0, 0, 0, location)
	end := start.Add(12 * time.Hour)
	var userPages, orderPages int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch r.URL.Path {
		case "/api/v1/admin/usage/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"total_actual_cost": 0, "total_tokens": 0}})
		case "/api/v1/admin/users":
			userPages++
			if page == "1" {
				items := make([]any, dailyReportPageSize)
				for i := range items {
					items[i] = map[string]any{"created_at": "2026-08-30T01:00:00Z"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items, "page": 1, "page_size": dailyReportPageSize, "pages": 3}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"created_at": "2026-08-29T23:59:59Z"}}, "page": 2, "page_size": dailyReportPageSize, "pages": 3}})
		case "/api/v1/admin/payment/orders":
			orderPages++
			if page == "1" {
				items := make([]any, dailyReportPageSize)
				for i := range items {
					items[i] = map[string]any{"status": "COMPLETED", "user_id": i + 1, "pay_amount": 1, "currency": "CNY", "paid_at": "2026-08-30T02:00:00Z", "created_at": "2026-08-30T02:00:00Z"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items, "page": 1, "page_size": dailyReportPageSize, "pages": 3}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"created_at": "2026-08-29T23:59:59Z"}}, "page": 2, "page_size": dailyReportPageSize, "pages": 3}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stats, err := New(server.URL, "key", 5*time.Second).GetDailyReportStats(context.Background(), start, end, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if stats.NewUsers != dailyReportPageSize || stats.RechargeAmounts["CNY"] != dailyReportPageSize || stats.RechargeUsers != dailyReportPageSize {
		t.Fatalf("分页汇总异常: %+v", stats)
	}
	if userPages != 2 || orderPages != 2 {
		t.Fatalf("发现窗口外记录后未提前停止分页: users=%d orders=%d", userPages, orderPages)
	}
}

func TestGetDailyReportStatsRejectsInvalidTimeRange(t *testing.T) {
	client := New("http://example.invalid", "key", time.Second)
	_, err := client.GetDailyReportStats(context.Background(), time.Now(), time.Now(), "UTC")
	if err == nil || !strings.Contains(err.Error(), "时间范围无效") {
		t.Fatalf("非法时间范围错误异常: %v", err)
	}
}
