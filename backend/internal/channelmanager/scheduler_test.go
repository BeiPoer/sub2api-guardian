package channelmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/store"
)

func TestEvaluateTasks(t *testing.T) {
	low := store.UpstreamAutomationTask{Type: store.UpstreamTaskLowBalance, Threshold: 10}
	result := EvaluateBalanceTask(low, []store.UpstreamBalanceSnapshot{{ID: 1, Balance: 9, Unit: "USD"}}, "渠道A")
	if !result.Triggered || !strings.Contains(result.Message, "低于或等于") || strings.Contains(result.Message, "USD") {
		t.Fatalf("低余额判断异常: %+v", result)
	}

	now := time.Now()
	burn := store.UpstreamAutomationTask{Type: store.UpstreamTaskBurnRate, Threshold: 4, LookbackMinutes: 60}
	result = EvaluateBalanceTask(burn, []store.UpstreamBalanceSnapshot{
		{ID: 1, Balance: 20, Unit: "USD", CapturedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)},
		{ID: 2, Balance: 15, Unit: "USD", CapturedAt: now.Format(time.RFC3339Nano)},
	}, "渠道A")
	if !result.Triggered || strings.Contains(result.Message, "USD") {
		t.Fatalf("消耗速度判断异常: %+v", result)
	}

	groupTask := store.UpstreamAutomationTask{Type: store.UpstreamTaskGroupAdded}
	baseline := EvaluateGroupTask(groupTask, nil, []any{map[string]any{"name": "new"}}, "渠道A", false)
	if baseline.Triggered {
		t.Fatalf("首次建立分组基线不应告警: %+v", baseline)
	}
	added := EvaluateGroupTask(groupTask, []any{map[string]any{"name": "old"}}, []any{
		map[string]any{"name": "old"}, map[string]any{"name": "new"},
	}, "渠道A", true)
	if !added.Triggered || !strings.Contains(added.Message, "new") {
		t.Fatalf("新增分组判断异常: %+v", added)
	}
}

func TestTokenMultipliersForLinkUsesCompleteKeysAndUserRates(t *testing.T) {
	groups := []any{
		map[string]any{"id": 1, "name": "pro", "rate_multiplier": 9.0, "user_rate_multiplier": 0.12},
		map[string]any{"id": 2, "name": "basic", "rate_multiplier": 0.8},
	}
	tokens := []any{
		map[string]any{"key": "key-pro", "group_id": 1},
		map[string]any{"api_key": "key-basic", "group_id": 2},
		map[string]any{"key": "key-masked*", "group_id": 1},
		map[string]any{"key": "key-no-group"},
		map[string]any{"key": "key-conflict", "group_id": 1},
		map[string]any{"key": "key-conflict", "group_id": 2},
	}

	got := tokenMultipliersForLink(groups, tokens, store.UpstreamChannelSub2API)
	if got["key-pro"] != 0.12 || got["key-basic"] != 0.8 {
		t.Fatalf("令牌倍率 = %#v", got)
	}
	extraction := tokenMultiplierLinkCandidates(groups, tokens, store.UpstreamChannelSub2API)
	if extraction.conflicts != 1 {
		t.Fatalf("冲突令牌数量 = %d, 期望 1", extraction.conflicts)
	}
	for _, key := range []string{"key-masked*", "key-no-group", "key-conflict"} {
		if _, ok := got[key]; ok {
			t.Fatalf("失配令牌 %q 不应参与联动: %#v", key, got)
		}
	}
	if linked := tokenMultipliersForLink(groups, tokens, store.UpstreamChannelNewAPI); linked["key-pro"] != 0.12 || linked["key-basic"] != 0.8 {
		t.Fatalf("new-api 令牌倍率应参与联动: %#v", linked)
	}
	if linked := tokenMultipliersForLink(groups, tokens, store.UpstreamChannelOther); len(linked) != 0 {
		t.Fatalf("其它类型渠道不应参与联动: %#v", linked)
	}
}

func TestTokenMultiplierLinkFallsBackWhenUserRateIsInvalid(t *testing.T) {
	groups := []any{map[string]any{
		"id": 1, "name": "pro", "user_rate_multiplier": 0, "rate_multiplier": 0.8,
	}}
	tokens := []any{map[string]any{"key": "key-pro", "group_id": 1}}
	got := tokenMultipliersForLink(groups, tokens, store.UpstreamChannelSub2API)
	if got["key-pro"] != 0.8 {
		t.Fatalf("无效用户倍率应回退普通倍率: %#v", got)
	}
}

func TestDueTaskRecordsAlertWhenEmailIsUnconfigured(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"access_token": "access", "expires_in": 3600}})
	})
	mux.HandleFunc("/api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"balance": 5}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	manager, st := testManager(t)
	channel, _ := st.CreateUpstreamChannel(store.UpstreamChannelInput{
		Name: "sub", Type: store.UpstreamChannelSub2API, BaseURL: server.URL, Username: "u", Password: "p",
	})
	task, err := st.CreateUpstreamAutomationTask(store.UpstreamAutomationTask{
		ChannelID: channel.ID, Type: store.UpstreamTaskLowBalance, Enabled: true,
		IntervalMinutes: 5, Threshold: 10, LookbackMinutes: 60, CooldownMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RunDueTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	alerts, err := st.UpstreamAlertEvents(channel.ID, 10)
	if err != nil || len(alerts) != 1 {
		t.Fatalf("告警事件=%+v err=%v", alerts, err)
	}
	if alerts[0].EmailSent || alerts[0].EmailError == "" {
		t.Fatalf("SMTP 未配置时应记录邮件失败: %+v", alerts[0])
	}
	updated, _ := st.UpstreamAutomationTask(channel.ID, task.ID)
	if updated.LastRunAt == "" || updated.LastAlertAt == "" {
		t.Fatalf("任务时间未更新: %+v", updated)
	}
}

func TestOverviewReturnsLatestRecentRatioChangesOnlyWhileNotificationEnabled(t *testing.T) {
	manager, st := testManager(t)
	channel, err := st.CreateUpstreamChannel(store.UpstreamChannelInput{
		Name: "sub", Type: store.UpstreamChannelSub2API, BaseURL: "https://example.test", Username: "u", Password: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateUpstreamAutomationTask(store.UpstreamAutomationTask{
		ChannelID: channel.ID, Type: store.UpstreamTaskGroupRatioChange, Enabled: true,
		IntervalMinutes: 5, LookbackMinutes: 60, CooldownMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	addChange := func(at time.Time, changed ...map[string]any) {
		t.Helper()
		items := make([]any, len(changed))
		for index := range changed {
			items[index] = changed[index]
		}
		if err := st.AddUpstreamAlertEvent(store.UpstreamAlertEvent{
			ChannelID: channel.ID, TaskID: &task.ID, Type: string(task.Type),
			Snapshot: map[string]any{"changed": items}, CreatedAt: at.Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatal(err)
		}
	}
	addChange(now.Add(-25*time.Hour), map[string]any{"key": "old", "before": 1, "after": 2})
	addChange(now.Add(-2*time.Hour), map[string]any{"key": "pro", "label": "Pro", "before": 1, "after": 2})
	latestAt := now.Add(-time.Hour)
	addChange(latestAt,
		map[string]any{"key": "pro", "label": "Pro", "before": 2, "after": 1.5},
		map[string]any{"key": "basic", "before": 0.5, "after": 0.75},
	)

	overview, err := manager.Overview(channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.RecentGroupRatioChanges) != 2 {
		t.Fatalf("近 24 小时倍率变化=%+v", overview.RecentGroupRatioChanges)
	}
	byKey := make(map[string]UpstreamGroupRatioChange)
	for _, change := range overview.RecentGroupRatioChanges {
		byKey[change.Key] = change
	}
	if change := byKey["pro"]; change.Before != 2 || change.After != 1.5 || change.ChangedAt != latestAt.Format(time.RFC3339Nano) {
		t.Fatalf("pro 应返回最近一次变化: %+v", change)
	}
	if change := byKey["basic"]; change.Before != 0.5 || change.After != 0.75 {
		t.Fatalf("basic 倍率变化异常: %+v", change)
	}
	if _, exists := byKey["old"]; exists {
		t.Fatal("超过 24 小时的变化不应返回")
	}

	task.Enabled = false
	if _, err := st.UpdateUpstreamAutomationTask(task); err != nil {
		t.Fatal(err)
	}
	overview, err = manager.Overview(channel.ID)
	if err != nil || len(overview.RecentGroupRatioChanges) != 0 {
		t.Fatalf("通知关闭后不应返回倍率变化: %+v err=%v", overview.RecentGroupRatioChanges, err)
	}
}
