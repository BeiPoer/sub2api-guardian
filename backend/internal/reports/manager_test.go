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

func TestManagerDailyDefaultsAndRun(t *testing.T) {
	st := openReportStore(t)
	manager := New(st, upstream.New("", "", time.Second))
	view, err := manager.DailyView()
	if err != nil {
		t.Fatal(err)
	}
	if view.Config.Enabled || view.Config.RunHour != 23 || view.Config.Timezone != "Asia/Shanghai" {
		t.Fatalf("每日报告默认配置异常: %+v", view.Config)
	}
	if _, exists, err := st.ScheduledReport(store.ScheduledReportDaily); err != nil || exists {
		t.Fatalf("读取默认每日报告不应写库: exists=%v err=%v", exists, err)
	}
}

func TestFormatTokenCountUsesCompactUnits(t *testing.T) {
	for _, test := range []struct {
		value int64
		want  string
	}{
		{999, "999"},
		{1234, "1.23K"},
		{1_234_567, "1.23M"},
		{1_234_567_890, "1.23B"},
	} {
		if got := formatTokenCount(test.value); got != test.want {
			t.Fatalf("formatTokenCount(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestManagerMigratesLegacyWeComSettingsToSharedConfig(t *testing.T) {
	st := openReportStore(t)
	_, err := st.SaveScheduledReportConfig(store.ScheduledReport{
		Type: store.ScheduledReportChannelUsage, Enabled: false, IntervalMinutes: 60,
		StartHour: 9, EndHour: 22, Timezone: "Asia/Shanghai",
		ConfigJSON: `{"lookback_hours":1,"first_token_threshold_ms":30000,"trigger_count":20,"wecom":{"enabled":true,"corp_id":"ww-corp","agent_id":1,"secret":"legacy-secret","target":"@all"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := New(st, upstream.New("", "", time.Second))
	config, err := manager.NotificationSettings()
	if err != nil || config.WeCom.Secret != "legacy-secret" || !config.WeCom.Enabled {
		t.Fatalf("旧报告企微配置迁移失败: %+v %v", config, err)
	}
	if _, exists, err := st.ScheduledReportNotificationSettings(); err != nil || !exists {
		t.Fatalf("共享企微配置未写入: exists=%v err=%v", exists, err)
	}
	report, exists, err := st.ScheduledReport(store.ScheduledReportChannelUsage)
	if err != nil || !exists || strings.Contains(report.ConfigJSON, "wecom") {
		t.Fatalf("旧报告配置未清理企微字段: %+v exists=%v err=%v", report, exists, err)
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
	notification := validNotificationInput()
	if _, err := manager.SaveNotificationSettings(notification); err != nil {
		t.Fatal(err)
	}
	notification.WeCom.Secret = ""
	if config, err := manager.SaveNotificationSettings(notification); err != nil || !config.WeCom.HasSecret || config.WeCom.Secret != "wecom-secret" {
		t.Fatalf("空 Secret 保存失败或明文值异常: %+v %v", config, err)
	}
	if _, err := manager.Save(validSaveInput(20)); err != nil {
		t.Fatal(err)
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
	if err != nil || !exists || strings.Contains(report.ConfigJSON, "wecom-secret") {
		t.Fatalf("报告配置不应继续存储企微 Secret: %+v exists=%v err=%v", report, exists, err)
	}
	shared, exists, err := st.ScheduledReportNotificationSettings()
	if err != nil || !exists || shared.WeCom.Secret != "wecom-secret" {
		t.Fatalf("共享企微 Secret 未保留: %+v exists=%v err=%v", shared, exists, err)
	}
}

func TestManagerAlertSendsTextWithoutCredentials(t *testing.T) {
	var sendCalls atomic.Int64
	wecomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "report-token", "expires_in": 7200})
		case "/cgi-bin/message/send":
			sendCalls.Add(1)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("企微普通文本请求体解析失败: %v", err)
			} else {
				if payload["msgtype"] != "text" || payload["touser"] != "@all" {
					t.Errorf("企微消息类型/接收人异常: %+v", payload)
				}
				textPayload, _ := payload["text"].(map[string]any)
				content, _ := textPayload["content"].(string)
				if !strings.Contains(content, "首 T 高延迟告警") || !strings.Contains(content, "高延迟明细") || strings.Contains(content, "admin-key") || strings.Contains(content, "wecom-secret") || strings.Contains(content, "##") || strings.Contains(content, "|") {
					t.Errorf("普通文本内容异常或泄露凭据: %s", content)
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
	if _, err := manager.SaveNotificationSettings(validNotificationInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save(validSaveInput(1)); err != nil {
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
	if _, err := manager.SaveNotificationSettings(validNotificationInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Save(validSaveInput(20)); err != nil {
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

func TestManagerDailyRunSendsPlainTextSummary(t *testing.T) {
	var sendCalls atomic.Int64
	var messageContent string
	wecomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "daily-token", "expires_in": 7200})
		case "/cgi-bin/message/send":
			sendCalls.Add(1)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("每日报告企微请求体解析失败: %v", err)
			} else {
				if payload["msgtype"] != "text" || payload["touser"] != "@all" {
					t.Errorf("每日报告企微消息类型/接收人异常: %+v", payload)
				}
				textPayload, _ := payload["text"].(map[string]any)
				messageContent, _ = textPayload["content"].(string)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "msgid": 10})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wecomServer.Close()

	now := time.Now().UTC()
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/usage/stats":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"total_actual_cost": 12.5, "total_tokens": 3456}})
		case "/api/v1/admin/users":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"items": []any{map[string]any{"created_at": now.Format(time.RFC3339Nano)}}, "page": 1, "page_size": 1000, "pages": 1,
			}})
		case "/api/v1/admin/payment/orders":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"items": []any{map[string]any{"status": "COMPLETED", "order_type": "balance", "user_id": 42, "pay_amount": 8.5, "currency": "CNY", "paid_at": now.Format(time.RFC3339Nano), "created_at": now.Format(time.RFC3339Nano)}}, "page": 1, "page_size": 1000, "pages": 1,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamServer.Close()

	st := openReportStore(t)
	manager := New(st, upstream.New(upstreamServer.URL, "admin-key", 5*time.Second))
	manager.wecom.SetBaseURL(wecomServer.URL)
	if _, err := manager.SaveNotificationSettings(validNotificationInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveDaily(DailySaveInput{Enabled: false, RunHour: 23, Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	run, err := manager.RunDailyNow(context.Background())
	if err != nil || run.Status != "ok" || run.NotificationStatus != "sent" || sendCalls.Load() != 1 {
		t.Fatalf("每日报告运行结果异常: run=%+v err=%v sends=%d", run, err, sendCalls.Load())
	}
	if !strings.Contains(messageContent, "每日报告") || !strings.Contains(messageContent, "今日消耗额度：12.50") ||
		!strings.Contains(messageContent, "今日总 Token：3.46K") || !strings.Contains(messageContent, "今日注册人数：1 人") ||
		!strings.Contains(messageContent, "CNY：8.50") || !strings.Contains(messageContent, "今日充值人数：1 人") || strings.Contains(messageContent, "admin-key") || strings.Contains(messageContent, "wecom-secret") || strings.Contains(messageContent, "|") {
		t.Fatalf("每日报告普通文本内容异常或泄露凭据: %s", messageContent)
	}
	summary, ok := run.Summary.(DailyReportSummary)
	if !ok || summary.TotalActualCost != 12.5 || summary.TotalTokens != 3456 || summary.NewUsers != 1 || summary.RechargeAmounts["CNY"] != 8.5 || summary.RechargeUsers != 1 {
		t.Fatalf("每日报告汇总结果异常: %#v", run.Summary)
	}
}

func TestManagerSourceSettingsPreserveCredentialAndCustomConfig(t *testing.T) {
	st := openReportStore(t)
	manager := New(st, upstream.New("https://global.example.com", "global-key", time.Second))

	config, err := manager.SourceSettings()
	if err != nil || config.Mode != store.ScheduledReportSourceGlobal || !config.Configured ||
		config.EffectiveType != store.ScheduledReportSourceSub2API || config.EffectiveBaseURL != "https://global.example.com" {
		t.Fatalf("默认全局源站异常: config=%+v err=%v", config, err)
	}
	config, err = manager.SaveSourceSettings(SourceSaveInput{
		Mode: store.ScheduledReportSourceCustom, SourceType: store.ScheduledReportSourceSub2API,
		BaseURL: "https://custom.example.com/", Credential: "custom-secret",
	})
	if err != nil || !config.HasCredential || config.BaseURL != "https://custom.example.com" || !config.Configured {
		t.Fatalf("保存自定义源站失败: config=%+v err=%v", config, err)
	}
	raw, err := json.Marshal(config)
	if err != nil || strings.Contains(string(raw), "custom-secret") || strings.Contains(string(raw), `"credential"`) {
		t.Fatalf("源站查询模型泄露凭据: %s err=%v", raw, err)
	}
	if _, err := manager.SaveSourceSettings(SourceSaveInput{
		Mode: store.ScheduledReportSourceCustom, SourceType: store.ScheduledReportSourceSub2API,
		BaseURL: "https://custom-2.example.com", Credential: "",
	}); err != nil {
		t.Fatalf("同类型空凭据应保留原值: %v", err)
	}
	stored, _, err := st.ScheduledReportSourceSettings()
	if err != nil || stored.Credential != "custom-secret" || stored.BaseURL != "https://custom-2.example.com" {
		t.Fatalf("空凭据未保留或地址未更新: %+v err=%v", stored, err)
	}
	if _, err := manager.SaveSourceSettings(SourceSaveInput{
		Mode: store.ScheduledReportSourceCustom, SourceType: store.ScheduledReportSourceNewAPI,
		BaseURL: "https://new.example.com", NewAPIUserID: 7,
	}); err == nil {
		t.Fatal("切换源站类型但未填写新凭据应失败")
	}
	config, err = manager.SaveSourceSettings(SourceSaveInput{Mode: store.ScheduledReportSourceGlobal})
	if err != nil || config.Mode != store.ScheduledReportSourceGlobal || config.EffectiveBaseURL != "https://global.example.com" {
		t.Fatalf("切换回全局源站失败: config=%+v err=%v", config, err)
	}
	stored, _, err = st.ScheduledReportSourceSettings()
	if err != nil || stored.SourceType != store.ScheduledReportSourceSub2API || stored.BaseURL != "https://custom-2.example.com" || stored.Credential != "custom-secret" {
		t.Fatalf("全局模式未保留自定义配置: %+v err=%v", stored, err)
	}
}

func TestManagerGlobalSourceFollowsClientReconfigure(t *testing.T) {
	var firstCalls atomic.Int64
	var secondCalls atomic.Int64
	usageServer := func(counter *atomic.Int64, account string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			counter.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"items": []any{map[string]any{
					"created_at":     time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano),
					"first_token_ms": 1, "group": "默认", "account": account,
				}}, "page": 1, "page_size": 100, "pages": 1,
			}})
		}))
	}
	first := usageServer(&firstCalls, "first")
	defer first.Close()
	second := usageServer(&secondCalls, "second")
	defer second.Close()

	st := openReportStore(t)
	client := upstream.New(first.URL, "first-key", time.Second)
	manager := New(st, client)
	if _, err := manager.Save(validSaveInput(20)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RunNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.Reconfigure(second.URL, "second-key", time.Second)
	if _, err := manager.RunNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("全局源站未跟随共享客户端热更新: first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
	config, err := manager.SourceSettings()
	if err != nil || config.EffectiveBaseURL != second.URL {
		t.Fatalf("热更新后的有效地址异常: config=%+v err=%v", config, err)
	}
}

func TestManagerCustomSub2APIDoesNotUseGlobalConnection(t *testing.T) {
	var globalCalls atomic.Int64
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		globalCalls.Add(1)
		http.Error(w, "global should not be used", http.StatusUnauthorized)
	}))
	defer global.Close()
	var customCalls atomic.Int64
	custom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customCalls.Add(1)
		if r.Header.Get("x-api-key") != "custom-key" {
			t.Errorf("自定义 Sub2API 凭据异常: %q", r.Header.Get("x-api-key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"items": []any{}, "page": 1, "page_size": 100, "pages": 1,
		}})
	}))
	defer custom.Close()

	st := openReportStore(t)
	manager := New(st, upstream.New(global.URL, "global-key", time.Second))
	if _, err := manager.SaveSourceSettings(SourceSaveInput{
		Mode: store.ScheduledReportSourceCustom, SourceType: store.ScheduledReportSourceSub2API,
		BaseURL: custom.URL, Credential: "custom-key",
	}); err != nil {
		t.Fatal(err)
	}
	run, err := manager.RunNow(context.Background())
	if err != nil || run.Status != "ok" || globalCalls.Load() != 0 || customCalls.Load() != 1 {
		t.Fatalf("自定义 Sub2API 执行异常: run=%+v err=%v global=%d custom=%d", run, err, globalCalls.Load(), customCalls.Load())
	}
}

func TestManagerCustomNewAPIDrivesBothReports(t *testing.T) {
	now := time.Now().UTC().Add(-time.Second)
	var logCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer newapi-token" || r.Header.Get("New-Api-User") != "9" {
			t.Errorf("New API 管理器认证头异常")
		}
		var data any
		switch r.URL.Path {
		case "/api/log/":
			logCalls.Add(1)
			data = map[string]any{"page": 1, "page_size": 100, "total": 1, "items": []any{map[string]any{
				"created_at": now.Unix(), "quota": 500, "prompt_tokens": 10, "completion_tokens": 5,
				"group": "生产", "channel": 3, "channel_name": "New API 渠道", "other": `{"frt":30001}`,
			}}}
		case "/api/status":
			data = map[string]any{"quota_per_unit": 500, "quota_display_type": "USD", "usd_exchange_rate": 7}
		case "/api/user/":
			data = map[string]any{"page": 1, "page_size": 100, "total": 1, "items": []any{map[string]any{"created_at": now.Unix()}}}
		case "/api/user/topup":
			data = map[string]any{"page": 1, "page_size": 100, "total": 1, "items": []any{map[string]any{
				"status": "success", "complete_time": now.Unix(), "amount": 2, "user_id": 11,
			}}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "", "data": data})
	}))
	defer server.Close()

	st := openReportStore(t)
	manager := New(st, upstream.New("https://global.invalid", "global-key", time.Second))
	if _, err := manager.SaveSourceSettings(SourceSaveInput{
		Mode: store.ScheduledReportSourceCustom, SourceType: store.ScheduledReportSourceNewAPI,
		BaseURL: server.URL, Credential: "newapi-token", NewAPIUserID: 9,
	}); err != nil {
		t.Fatal(err)
	}
	channelRun, err := manager.RunNow(context.Background())
	if err != nil || channelRun.Status != "ok" || channelRun.TotalRecords != 1 || channelRun.HighLatencyCount != 1 {
		t.Fatalf("New API 渠道报告异常: run=%+v err=%v", channelRun, err)
	}
	if _, err := manager.SaveDaily(DailySaveInput{Enabled: false, RunHour: 23, Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	dailyRun, err := manager.RunDailyNow(context.Background())
	summary, ok := dailyRun.Summary.(DailyReportSummary)
	if err != nil || dailyRun.Status != "ok" || !ok || summary.TotalActualCost != 1 || summary.QuotaUnit != "USD" ||
		summary.TotalTokens != 15 || summary.NewUsers != 1 || summary.RechargeAmounts["USD"] != 2 || summary.RechargeUsers != 1 || logCalls.Load() != 2 {
		t.Fatalf("New API 每日报告异常: run=%+v summary=%+v ok=%v err=%v logCalls=%d", dailyRun, summary, ok, err, logCalls.Load())
	}
	view, err := manager.DailyView()
	if err != nil || view.Source.Type != store.ScheduledReportSourceNewAPI || view.Connection.BaseURL != server.URL {
		t.Fatalf("New API 报告视图源站摘要异常: view=%+v err=%v", view, err)
	}
}

func validSaveInput(trigger int) SaveInput {
	return SaveInput{
		Enabled: false, IntervalMinutes: 60, StartHour: 0, EndHour: 23,
		Timezone: "UTC", LookbackHours: 1, FirstTokenThresholdMS: 30000,
		TriggerCount: trigger,
	}
}

func validNotificationInput() NotificationSaveInput {
	return NotificationSaveInput{
		WeCom: WeComInput{Enabled: true, CorpID: "ww-corp", AgentID: 1, Secret: "wecom-secret", Target: "@all"},
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
