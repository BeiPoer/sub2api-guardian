package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewAPIListUsageAuthenticationPaginationAndNormalization(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	end := start.Add(time.Hour)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer system-token" || r.Header.Get("New-Api-User") != "42" {
			t.Errorf("New API 认证头异常: Authorization=%q New-Api-User=%q", r.Header.Get("Authorization"), r.Header.Get("New-Api-User"))
		}
		if r.URL.Path != "/api/log/" || r.URL.Query().Get("type") != "2" || r.URL.Query().Get("page_size") != "100" {
			t.Errorf("消费日志请求异常: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("p"))
		calls.Add(1)
		items := []any{map[string]any{
			"created_at": start.Unix(), "group": "生产", "channel": 3, "channel_name": "主渠道",
			"other": `{"frt":30001}`,
		}}
		if page == 2 {
			items = []any{
				map[string]any{"created_at": end.Add(-time.Second).Unix(), "group": "", "channel": 9, "channel_name": "", "other": `{"frt":0}`},
				map[string]any{"created_at": end.Unix(), "group": "边界外", "channel": 10, "other": `{"frt":40000}`},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "message": "", "data": map[string]any{
				"page": page, "page_size": 100, "total": 3, "items": items,
			},
		})
	}))
	defer server.Close()

	records, err := NewNewAPI(server.URL, "system-token", 42, time.Second).ListUsage(context.Background(), start, end, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(records) != 2 {
		t.Fatalf("分页或半开窗口过滤异常: calls=%d records=%+v", calls.Load(), records)
	}
	if records[0].Group != "生产" || records[0].Account != "主渠道" || records[0].FirstTokenMS != float64(30001) {
		t.Fatalf("消费日志归一化异常: %+v", records[0])
	}
	if records[1].Group != "未知分组" || records[1].Account != "channel-9" || records[1].FirstTokenMS != nil {
		t.Fatalf("消费日志回退或无效 frt 处理异常: %+v", records[1])
	}
}

func TestNewAPIRequestErrorsDoNotExposeCredential(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   map[string]any
	}{
		{name: "business", status: http.StatusOK, body: map[string]any{"success": false, "message": "token secret-token invalid"}},
		{name: "http", status: http.StatusUnauthorized, body: map[string]any{"success": false, "message": "secret-token unauthorized"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(test.body)
			}))
			defer server.Close()
			_, err := NewNewAPI(server.URL, "secret-token", 1, time.Second).ListUsage(
				context.Background(), time.Now().Add(-time.Hour), time.Now(), "UTC",
			)
			if err == nil || strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "[redacted]") {
				t.Fatalf("错误未正确脱敏: %v", err)
			}
		})
	}
}

func TestNewAPIDailyStatsConversionAndBoundaries(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	end := start.Add(24 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer daily-token" || r.Header.Get("New-Api-User") != "8" {
			t.Errorf("New API 每日报告认证头异常")
		}
		data := any(nil)
		switch r.URL.Path {
		case "/api/status":
			data = map[string]any{"quota_per_unit": 100, "quota_display_type": "CNY", "usd_exchange_rate": 7}
		case "/api/log/":
			data = map[string]any{"page": 1, "page_size": 100, "total": 3, "items": []any{
				map[string]any{"created_at": start.Unix(), "quota": 100, "prompt_tokens": 10, "completion_tokens": 5},
				map[string]any{"created_at": end.Add(-time.Second).Unix(), "quota": 200, "prompt_tokens": 20, "completion_tokens": 7},
				map[string]any{"created_at": end.Unix(), "quota": 900, "prompt_tokens": 90, "completion_tokens": 9},
			}}
		case "/api/user/":
			if !strings.Contains(r.URL.RawQuery, "sort_by=created_at") {
				t.Errorf("用户分页缺少创建时间排序: %s", r.URL.RawQuery)
			}
			data = map[string]any{"page": 1, "page_size": 100, "total": 3, "items": []any{
				map[string]any{"created_at": start.Unix()}, map[string]any{"created_at": end.Unix()}, map[string]any{"created_at": start.Add(-time.Second).Unix()},
			}}
		case "/api/user/topup":
			data = map[string]any{"page": 1, "page_size": 100, "total": 6, "items": []any{
				map[string]any{"status": "success", "complete_time": start.Unix(), "amount": 2, "user_id": 1},
				map[string]any{"status": "SUCCESS", "complete_time": end.Add(-time.Second).Unix(), "amount": 3, "user_id": 1},
				map[string]any{"status": "success", "complete_time": end.Add(-time.Minute).Unix(), "amount": 1, "user_id": 2},
				map[string]any{"status": "success", "complete_time": end.Unix(), "amount": 4, "user_id": 3},
				map[string]any{"status": "pending", "complete_time": start.Add(time.Hour).Unix(), "amount": 10, "user_id": 4},
				map[string]any{"status": "success", "complete_time": start.Add(-time.Second).Unix(), "amount": 10, "user_id": 5},
			}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "", "data": data})
	}))
	defer server.Close()

	stats, err := NewNewAPI(server.URL, "daily-token", 8, time.Second).GetDailyReportStats(context.Background(), start, end, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalActualCost != 21 || stats.QuotaUnit != "CNY" || stats.TotalTokens != 42 || stats.NewUsers != 1 ||
		stats.RechargeAmounts["CNY"] != 42 || stats.RechargeUsers != 2 {
		t.Fatalf("New API 每日统计异常: %+v", stats)
	}
}

func TestNewAPIQuotaDisplayModes(t *testing.T) {
	tests := []struct {
		status newAPIStatus
		quota  float64
		usd    float64
		unit   string
	}{
		{status: newAPIStatus{QuotaPerUnit: 100, QuotaDisplayType: "USD"}, quota: 2, usd: 2, unit: "USD"},
		{status: newAPIStatus{QuotaPerUnit: 100, QuotaDisplayType: "TOKENS"}, quota: 200, usd: 200, unit: "TOKENS"},
		{status: newAPIStatus{QuotaPerUnit: 100, QuotaDisplayType: "CUSTOM", CustomCurrencySymbol: "EUR", CustomCurrencyExchangeRate: 0.8}, quota: 1.6, usd: 1.6, unit: "EUR"},
	}
	for _, test := range tests {
		display, err := newQuotaDisplay(test.status)
		if err != nil || display.fromQuota(200) != test.quota || display.fromUSD(2) != test.usd || display.unit != test.unit {
			t.Fatalf("额度展示换算异常: display=%+v err=%v", display, err)
		}
	}
}

func TestNewAPIConsumptionQueryUsesUnixTimestamps(t *testing.T) {
	start := time.Unix(1234, 0)
	end := time.Unix(5678, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := url.ParseQuery(r.URL.RawQuery)
		if query.Get("start_timestamp") != "1234" || query.Get("end_timestamp") != "5678" {
			t.Errorf("Unix 时间参数异常: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"page": 1, "page_size": 100, "total": 0, "items": []any{}}})
	}))
	defer server.Close()
	_, err := NewNewAPI(server.URL, "token", 1, time.Second).ListUsage(context.Background(), start, end, "UTC")
	if err != nil {
		t.Fatal(err)
	}
}
