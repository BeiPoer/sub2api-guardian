package reports

import (
	"testing"
	"time"

	"sub2api-guardian/backend/internal/upstream"
)

func TestEvaluateUsesClosedWindowThresholdAndGlobalTrigger(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	start := time.Date(2026, 8, 30, 10, 0, 0, 0, location)
	end := start.Add(time.Hour)
	records := []upstream.UsageRecord{
		{CreatedAt: start.Format(time.RFC3339), FirstTokenMS: 30000, Group: map[string]any{"name": "A"}, Account: map[string]any{"name": "a"}},
		{CreatedAt: end.Format(time.RFC3339), FirstTokenMS: "30001", Group: map[string]any{"name": "A"}, Account: map[string]any{"name": "a"}},
		{CreatedAt: end.Add(-time.Minute).Format(time.RFC3339), FirstTokenMS: 30002, GroupID: "2", AccountID: 8},
		{CreatedAt: end.Add(-2 * time.Minute).Format(time.RFC3339), FirstTokenMS: "invalid", GroupID: 3, Account: map[string]any{}},
		{CreatedAt: end.Add(-3 * time.Minute).Format(time.RFC3339), Group: map[string]any{"name": "B"}, Account: map[string]any{"name": "b"}},
		{CreatedAt: start.Add(-time.Second).Format(time.RFC3339), FirstTokenMS: 90000},
		{CreatedAt: "not-a-time", FirstTokenMS: 90000},
	}

	result := Evaluate(records, start, end, location, 30000, 1)
	if result.TotalRecords != 5 || result.HighLatencyCount != 2 || !result.Alert {
		t.Fatalf("窗口/阈值/全局触发结果异常: %+v", result)
	}
	if len(result.Rows) != 4 {
		t.Fatalf("聚合行数 = %d，期望 4", len(result.Rows))
	}
	if result.Rows[0].GroupName != "2" || result.Rows[0].AccountName != "8" || result.Rows[0].HighLatencyCount != 1 || result.Rows[0].TotalRecords != 1 {
		t.Fatalf("首行聚合异常: %+v", result.Rows[0])
	}
	if result.Rows[1].GroupName != "A" || result.Rows[1].AccountName != "a" || result.Rows[1].TotalRecords != 2 {
		t.Fatalf("ID 回退或排序异常: %+v", result.Rows[1])
	}
}

func TestEvaluateSkipsMissingOrInvalidFirstToken(t *testing.T) {
	location := time.UTC
	start := time.Date(2026, 8, 30, 0, 0, 0, 0, location)
	end := start.Add(time.Hour)
	result := Evaluate([]upstream.UsageRecord{
		{CreatedAt: start.Format(time.RFC3339), FirstTokenMS: nil, Group: "g", Account: "a"},
		{CreatedAt: start.Add(time.Minute).Format(time.RFC3339), FirstTokenMS: "NaN", Group: "g", Account: "a"},
		{CreatedAt: start.Add(2 * time.Minute).Format(time.RFC3339), FirstTokenMS: 30001, Group: "g", Account: "a"},
	}, start, end, location, 30000, 20)
	if result.TotalRecords != 3 || result.HighLatencyCount != 1 || result.Alert {
		t.Fatalf("首 T 边界结果异常: %+v", result)
	}
}
