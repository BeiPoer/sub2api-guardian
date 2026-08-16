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
	if !result.Triggered || !strings.Contains(result.Message, "低于或等于") {
		t.Fatalf("低余额判断异常: %+v", result)
	}

	now := time.Now()
	burn := store.UpstreamAutomationTask{Type: store.UpstreamTaskBurnRate, Threshold: 4, LookbackMinutes: 60}
	result = EvaluateBalanceTask(burn, []store.UpstreamBalanceSnapshot{
		{ID: 1, Balance: 20, Unit: "USD", CapturedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)},
		{ID: 2, Balance: 15, Unit: "USD", CapturedAt: now.Format(time.RFC3339Nano)},
	}, "渠道A")
	if !result.Triggered {
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
