package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
)

func TestUpstreamChannelTablesStayIsolatedAndCascade(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	version, err := st.getMeta(metaSchemaVersion)
	if err != nil || version != currentSchemaVersion {
		t.Fatalf("全局 schema 版本 = %q, err=%v", version, err)
	}
	channel, err := st.CreateUpstreamChannel(UpstreamChannelInput{
		Name: "记录渠道", Type: UpstreamChannelOther, BaseURL: "https://example.test",
		Username: "user", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUpstreamCache(channel.ID, "tokens", []any{}, []any{map[string]any{"id": 1}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.AddUpstreamBalanceSnapshot(UpstreamBalanceSnapshot{ChannelID: channel.ID, Balance: 12, Unit: "USD"})
	if err != nil || snapshot.ID == 0 {
		t.Fatalf("写余额快照失败: %+v %v", snapshot, err)
	}
	task, err := st.CreateUpstreamAutomationTask(UpstreamAutomationTask{
		ChannelID: channel.ID, Type: UpstreamTaskLowBalance, Enabled: true,
		IntervalMinutes: 5, Threshold: 10, LookbackMinutes: 60, CooldownMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUpstreamTaskState(task.ID, "groups", []any{}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddUpstreamAlertEvent(UpstreamAlertEvent{ChannelID: channel.ID, TaskID: &task.ID, Type: string(task.Type), Message: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUpstreamChannel(channel.ID); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{
		"upstream_channel_cache", "upstream_balance_snapshots", "upstream_automation_tasks",
		"upstream_automation_task_state", "upstream_alert_events",
	} {
		var count int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s 未级联清理: count=%d err=%v", table, count, err)
		}
	}
	var guardianChannels bool
	if err := st.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='channels')`).Scan(&guardianChannels); err != nil {
		t.Fatal(err)
	}
	if guardianChannels {
		t.Fatal("上游渠道迁移不应创建或复用名为 channels 的表")
	}
}

func TestUpstreamChannelRechargeSettingsDefaultAndPersistence(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	channel, err := st.CreateUpstreamChannel(UpstreamChannelInput{
		Name: "充值渠道", Type: UpstreamChannelOther, BaseURL: "https://example.test",
		Username: "user", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if channel.RechargeRatio != 1 || len(channel.RechargeMethods) != 0 || channel.RechargeFee != "" {
		t.Fatalf("充值配置默认值异常: %+v", channel)
	}

	updated, err := st.UpdateUpstreamChannel(channel.ID, UpstreamChannelInput{
		Name: channel.Name, Type: channel.Type, BaseURL: channel.BaseURL,
		Username: channel.Username, Password: channel.Password,
		RechargeRatio: 1.2,
		RechargeMethods: []UpstreamRechargeMethod{
			UpstreamRechargeWechat, UpstreamRechargeAlipay, UpstreamRechargeWechat,
		},
		RechargeFee: "每笔 2 元",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RechargeRatio != 1.2 || len(updated.RechargeMethods) != 2 ||
		updated.RechargeMethods[0] != UpstreamRechargeWechat || updated.RechargeMethods[1] != UpstreamRechargeAlipay ||
		updated.RechargeFee != "每笔 2 元" {
		t.Fatalf("充值配置持久化异常: %+v", updated)
	}
}

func TestUpstreamBalanceHistoryReturnsLatestSnapshotsInTimeOrder(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	channel, err := st.CreateUpstreamChannel(UpstreamChannelInput{
		Name: "趋势渠道", Type: UpstreamChannelOther, BaseURL: "https://example.test",
		Username: "user", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 201; i++ {
		if _, err := st.AddUpstreamBalanceSnapshot(UpstreamBalanceSnapshot{
			ChannelID: channel.ID, Balance: float64(i), Unit: "USD",
			CapturedAt: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatal(err)
		}
	}

	history, err := st.UpstreamBalanceHistory(channel.ID, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 200 || history[0].Balance != 1 || history[len(history)-1].Balance != 200 {
		t.Fatalf("余额历史窗口错误: len=%d first=%v last=%v", len(history), history[0].Balance, history[len(history)-1].Balance)
	}
	for i := 1; i < len(history); i++ {
		if history[i-1].CapturedAt > history[i].CapturedAt {
			t.Fatalf("余额历史未按时间正序: %s > %s", history[i-1].CapturedAt, history[i].CapturedAt)
		}
	}
}

func TestCleanupUpstreamHistoryDoesNotTouchGuardianEvents(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	channel, _ := st.CreateUpstreamChannel(UpstreamChannelInput{
		Name: "记录渠道", Type: UpstreamChannelOther, BaseURL: "https://example.test", Username: "u", Password: "p",
	})
	old := time.Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, _ = st.AddUpstreamBalanceSnapshot(UpstreamBalanceSnapshot{ChannelID: channel.ID, Balance: 1, Unit: "USD", CapturedAt: old})
	st.AddEvent(domain.Event{Level: "info", Action: "test", Message: "guardian-event"})
	if err := st.CleanupUpstreamHistory(time.Now().Add(-7 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	var snapshots, events int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM upstream_balance_snapshots`).Scan(&snapshots)
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events)
	if snapshots != 0 || events != 1 {
		t.Fatalf("清理结果 snapshots=%d events=%d", snapshots, events)
	}
}

func TestUpstreamSMTPPasswordPersistsButIsNotJSONVisible(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.SaveUpstreamEmailSettings(UpstreamEmailSettings{
		SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPPassword: "secret", SMTPFrom: "guardian@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := st.UpstreamEmailSettings()
	if err != nil || loaded.SMTPPassword != "secret" || !loaded.HasSMTPPassword {
		t.Fatalf("SMTP 密码未持久化: %+v err=%v", loaded, err)
	}
	raw, _ := json.Marshal(loaded)
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("SMTP 密码不应出现在 JSON 中: %s", raw)
	}
}

func TestUpstreamWeComSecretPersistsAndIsJSONVisible(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.SaveUpstreamWeComSettings(UpstreamWeComSettings{
		CorpID: "ww-corp", AgentID: 1000002, Secret: "app-secret", Target: "zhangsan",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := st.UpstreamWeComSettings()
	if err != nil || loaded.Secret != "app-secret" || !loaded.HasSecret || loaded.AgentID != 1000002 || loaded.Target != "zhangsan" {
		t.Fatalf("企微配置未持久化: %+v err=%v", loaded, err)
	}
	raw, _ := json.Marshal(loaded)
	if !strings.Contains(string(raw), "app-secret") {
		t.Fatalf("企微 Secret 应出现在 JSON 中: %s", raw)
	}
}

func TestUpstreamAlertStoresWeComDeliveryResult(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	channel, err := st.CreateUpstreamChannel(UpstreamChannelInput{
		Name: "告警渠道", Type: UpstreamChannelOther, BaseURL: "https://example.test", Username: "u", Password: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddUpstreamAlertEvent(UpstreamAlertEvent{
		ChannelID: channel.ID, Type: string(UpstreamTaskLowBalance), Message: "余额低",
		WeComSent: true, WeComError: "",
	}); err != nil {
		t.Fatal(err)
	}
	alerts, err := st.UpstreamAlertEvents(channel.ID, 10)
	if err != nil || len(alerts) != 1 || !alerts[0].WeComSent || alerts[0].WeComError != "" {
		t.Fatalf("企微告警结果读取异常: %+v err=%v", alerts, err)
	}
}
