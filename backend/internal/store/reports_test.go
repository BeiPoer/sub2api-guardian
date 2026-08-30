package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduledReportDefaultsAreNotPersisted(t *testing.T) {
	st := openTemp(t)
	report, exists, err := st.ScheduledReport(ScheduledReportChannelUsage)
	if err != nil || exists || report.ID != 0 {
		t.Fatalf("新库不应自动写入报告: %+v exists=%v err=%v", report, exists, err)
	}
}

func TestScheduledReportConfigAndSecretRetention(t *testing.T) {
	st := openTemp(t)
	first := ScheduledReport{
		Type: ScheduledReportChannelUsage, Enabled: true, IntervalMinutes: 60,
		StartHour: 9, EndHour: 22, Timezone: "Asia/Shanghai",
		ConfigJSON: `{"lookback_hours":1,"first_token_threshold_ms":30000,"trigger_count":20,"wecom":{"enabled":true,"corp_id":"ww","agent_id":1,"secret":"secret-value","target":"@all"}}`,
	}
	saved, err := st.SaveScheduledReportConfig(first)
	if err != nil || saved.ID == 0 {
		t.Fatalf("保存报告失败: %+v %v", saved, err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(saved.ConfigJSON), &config); err != nil || config["wecom"] == nil {
		t.Fatalf("配置 JSON 异常: %s %v", saved.ConfigJSON, err)
	}
	updated := first
	updated.Enabled = false
	updated.ConfigJSON = `{"lookback_hours":2,"first_token_threshold_ms":40000,"trigger_count":3,"wecom":{"enabled":false,"corp_id":"ww-new","agent_id":2,"secret":"","target":"zhangsan"}}`
	if _, err := st.SaveScheduledReportConfig(updated); err != nil {
		t.Fatalf("更新报告失败: %v", err)
	}
	reloaded, exists, err := st.ScheduledReport(ScheduledReportChannelUsage)
	if err != nil || !exists || reloaded.Enabled || reloaded.IntervalMinutes != 60 {
		t.Fatalf("更新结果异常: %+v exists=%v err=%v", reloaded, exists, err)
	}
	if !json.Valid([]byte(reloaded.ConfigJSON)) {
		t.Fatalf("更新后的配置 JSON 无效: %s", reloaded.ConfigJSON)
	}
}

func TestScheduledReportRunsAndCleanup(t *testing.T) {
	st := openTemp(t)
	report, err := st.SaveScheduledReportConfig(ScheduledReport{
		Type: ScheduledReportChannelUsage, IntervalMinutes: 60, StartHour: 9, EndHour: 22,
		Timezone: "UTC", ConfigJSON: `{"lookback_hours":1,"first_token_threshold_ms":30000,"trigger_count":20}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
	oldRun, err := st.AddScheduledReportRun(ScheduledReportRun{
		ReportID: report.ID, Status: "ok", StartedAt: old, FinishedAt: old,
		WindowStart: old, WindowEnd: old, Summary: []any{}, Message: "old",
	})
	if err != nil || oldRun.ID == 0 {
		t.Fatalf("保存旧运行失败: %+v %v", oldRun, err)
	}
	newRun, err := st.AddScheduledReportRun(ScheduledReportRun{
		ReportID: report.ID, Status: "alert", StartedAt: time.Now().Format(time.RFC3339Nano),
		FinishedAt: time.Now().Format(time.RFC3339Nano), WindowStart: old, WindowEnd: old,
		TotalRecords: 10, HighLatencyCount: 3, NotificationStatus: "failed",
		NotificationError: "send failed", Summary: []map[string]any{{"group_name": "g"}},
	})
	if err != nil || newRun.ID == 0 {
		t.Fatalf("保存新运行失败: %+v %v", newRun, err)
	}
	items, total, _, _, err := st.ScheduledReportRuns(report.ID, 1, 20)
	if err != nil || total != 1 || len(items) != 1 || items[0].Status != "alert" {
		t.Fatalf("7 天历史查询异常: items=%+v total=%d err=%v", items, total, err)
	}
	if err := st.CleanupScheduledReportRuns(time.Now().Add(-7 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := st.ScheduledReportRuns(report.ID, 1, 20); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledReportMigrationCreatesTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guardian.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, table := range []string{"scheduled_reports", "scheduled_report_runs"} {
		var name string
		if err := st.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil || name != table {
			t.Fatalf("缺少迁移表 %s: %q %v", table, name, err)
		}
	}
}
