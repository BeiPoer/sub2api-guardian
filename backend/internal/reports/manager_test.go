package reports

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

func TestManagerDefaultsDoNotWriteUntilSave(t *testing.T) {
	st := openReportStore(t)
	manager := New(st, upstream.New("", "", time.Second))
	view, err := manager.View()
	if err != nil {
		t.Fatal(err)
	}
	if view.Config.Enabled || view.Config.IntervalMinutes != 60 || view.Config.LookbackHours != 1 || view.Config.FirstTokenThresholdMS != 30000 || view.Config.TriggerCount != 20 {
		t.Fatalf("默认报告配置异常: %+v", view.Config)
	}
	if _, exists, err := st.ScheduledReport(store.ScheduledReportChannelUsage); err != nil || exists {
		t.Fatalf("读取默认配置不应写库: exists=%v err=%v", exists, err)
	}
}

func TestManagerPreservesBlankSecretAndRunsSummary(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Errorf("Admin API Key 未使用: %q", r.Header.Get("x-api-key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"items": []any{
				map[string]any{"created_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "first_token_ms": 30001,
					"group": map[string]any{"name": "生产"}, "account": map[string]any{"name": "主账号"}},
			}, "page": 1, "page_size": 100, "pages": 1},
		})
	}))
	defer upstreamServer.Close()
	st := openReportStore(t)
	manager := New(st, upstream.New(upstreamServer.URL, "admin-key", 5*time.Second))
	input := validSaveInput(20)
	input.WeCom = WeComInput{Enabled: true, CorpID: "ww-corp", AgentID: 1, Secret: "wecom-secret", Target: "@all"}
	if _, err := manager.Save(input); err != nil {
		t.Fatal(err)
	}
	input.WeCom.Secret = ""
	if view, err := manager.Save(input); err != nil || !view.Config.WeCom.HasSecret || view.Config.WeCom.Secret != "wecom-secret" {
		t.Fatalf("空 Secret 保存失败或明文值异常: %+v %v", view, err)
	}

	run, err := manager.RunNow(context.Background())
	if err != nil {
		t.Fatalf("立即执行失败: %v", err)
	}
	if run.Status != "ok" || run.NotificationStatus != "not_needed" || run.TotalRecords != 1 || run.HighLatencyCount != 1 {
		t.Fatalf("运行结果异常: %+v", run)
	}
	rows, ok := run.Summary.([]SummaryRow)
	if !ok || len(rows) != 1 || rows[0].GroupName != "生产" || rows[0].AccountName != "主账号" {
		t.Fatalf("聚合结果异常: %#v", run.Summary)
	}
	report, exists, err := st.ScheduledReport(store.ScheduledReportChannelUsage)
	if err != nil || !exists || !strings.Contains(report.ConfigJSON, "wecom-secret") {
		t.Fatalf("Secret 未保留在服务端配置: %+v exists=%v err=%v", report, exists, err)
	}
}

func TestManagerAlertSendsMarkdownWithoutCredentials(t *testing.T) {
	var sendCalls atomic.Int64
	wecomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "report-token", "expires_in": 7200})
		case "/cgi-bin/message/send":
			sendCalls.Add(1)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("企微 Markdown 请求体解析失败: %v", err)
			} else {
				if payload["msgtype"] != "markdown" || payload["touser"] != "@all" {
					t.Errorf("企微消息类型/接收人异常: %+v", payload)
				}
				markdown, _ := payload["markdown"].(map[string]any)
				content, _ := markdown["content"].(string)
				if !strings.Contains(content, "首 T 高延迟告警") || strings.Contains(content, "admin-key") || strings.Contains(content, "wecom-secret") {
					t.Errorf("Markdown 内容异常或泄露凭据: %s", content)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "msgid": 9})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wecomServer.Close()

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"items": []any{
				map[string]any{"created_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "first_token_ms": 30001, "group_id": 1, "account_id": 2},
				map[string]any{"created_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "first_token_ms": "30002", "group_id": 1, "account_id": 2},
			}, "page": 1, "page_size": 100, "pages": 1},
		})
	}))
	defer upstreamServer.Close()

	st := openReportStore(t)
	manager := New(st, upstream.New(upstreamServer.URL, "admin-key", 5*time.Second))
	manager.wecom.SetBaseURL(wecomServer.URL)
	input := validSaveInput(1)
	input.WeCom = WeComInput{Enabled: true, CorpID: "ww-corp", AgentID: 1, Secret: "wecom-secret", Target: "@all"}
	if _, err := manager.Save(input); err != nil {
		t.Fatal(err)
	}
	run, err := manager.RunNow(context.Background())
	if err != nil || run.Status != "alert" || run.NotificationStatus != "sent" || sendCalls.Load() != 1 {
		t.Fatalf("告警企微结果异常: run=%+v err=%v sends=%d", run, err, sendCalls.Load())
	}
}

func TestManagerRecordsQueryFailureAndSendsOneFailureNotice(t *testing.T) {
	var sendCalls atomic.Int64
	wecomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "failure-token", "expires_in": 7200})
			return
		}
		if r.URL.Path == "/cgi-bin/message/send" {
			sendCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "msgid": 11})
			return
		}
		http.NotFound(w, r)
	}))
	defer wecomServer.Close()
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary failure", http.StatusBadGateway)
	}))
	defer upstreamServer.Close()

	st := openReportStore(t)
	manager := New(st, upstream.New(upstreamServer.URL, "admin-key", 5*time.Second))
	manager.wecom.SetBaseURL(wecomServer.URL)
	input := validSaveInput(20)
	input.WeCom = WeComInput{Enabled: true, CorpID: "ww-corp", AgentID: 1, Secret: "wecom-secret", Target: "@all"}
	if _, err := manager.Save(input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	run, err := manager.RunNow(ctx)
	if err != nil || run.Status != "error" || run.NotificationStatus != "sent" || sendCalls.Load() != 1 {
		t.Fatalf("失败通知结果异常: run=%+v err=%v sends=%d", run, err, sendCalls.Load())
	}
	if run.Error == "" || strings.Contains(run.Error, "wecom-secret") {
		t.Fatalf("失败记录错误信息异常或泄露 Secret: %q", run.Error)
	}
}

func validSaveInput(trigger int) SaveInput {
	return SaveInput{
		Enabled: false, IntervalMinutes: 60, StartHour: 0, EndHour: 23,
		Timezone: "UTC", LookbackHours: 1, FirstTokenThresholdMS: 30000,
		TriggerCount: trigger,
	}
}

func openReportStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
