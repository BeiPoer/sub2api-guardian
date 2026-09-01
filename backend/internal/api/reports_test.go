package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/reports"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

func TestChannelUsageReportAPISecretsAndRunHistory(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})

	rec := doJSON(t, handler, http.MethodGet, "/api/reports/channel-usage", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) || strings.Contains(rec.Body.String(), `"wecom"`) {
		t.Fatalf("默认报告响应异常或仍包含企微配置: %d %s", rec.Code, rec.Body.String())
	}

	notificationPayload := map[string]any{
		"wecom": map[string]any{
			"enabled": true, "corp_id": "ww-corp", "agent_id": 1,
			"secret": "report-secret", "target": "@all",
		},
	}
	rec = doJSON(t, handler, http.MethodPut, "/api/reports/notifications", notificationPayload)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"secret":"report-secret"`) || !strings.Contains(rec.Body.String(), `"has_secret":true`) {
		t.Fatalf("保存通知配置响应异常或 Secret 未返回: %d %s", rec.Code, rec.Body.String())
	}

	notificationPayload["wecom"].(map[string]any)["secret"] = ""
	rec = doJSON(t, handler, http.MethodPut, "/api/reports/notifications", notificationPayload)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"secret":"report-secret"`) || !strings.Contains(rec.Body.String(), `"has_secret":true`) {
		t.Fatalf("空 Secret 保存后未保留明文值: %d %s", rec.Code, rec.Body.String())
	}

	payload := map[string]any{
		"enabled": true, "interval_minutes": 60, "start_hour": 9, "end_hour": 22,
		"timezone": "Asia/Shanghai", "lookback_hours": 1,
		"first_token_threshold_ms": 30000, "trigger_count": 20,
	}
	rec = doJSON(t, handler, http.MethodPut, "/api/reports/channel-usage", payload)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"wecom"`) {
		t.Fatalf("保存报告响应异常或仍包含企微配置: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodGet, "/api/reports/channel-usage/runs?page=1&page_size=20", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("运行历史空列表异常: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPost, "/api/reports/channel-usage/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("立即执行接口失败: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Run struct {
			Status string `json:"status"`
		} `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Run.Status != "ok" {
		t.Fatalf("立即执行结果异常: %s err=%v", rec.Body.String(), err)
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/reports/channel-usage/runs", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("运行历史未写入: %d %s", rec.Code, rec.Body.String())
	}
}

func TestChannelUsageReportAPIRejectsInvalidConfig(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})
	rec := doJSON(t, handler, http.MethodPut, "/api/reports/channel-usage", map[string]any{
		"enabled": false, "interval_minutes": 0, "start_hour": 9, "end_hour": 22,
		"timezone": "Asia/Shanghai", "lookback_hours": 1,
		"first_token_threshold_ms": 30000, "trigger_count": 20,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法运行间隔应返回 400: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReportSourceAPIGetSavePreserveAndValidate(t *testing.T) {
	handler, st := setupAPI(t, &fakeUpstream{groupCount: 1})

	rec := doJSON(t, handler, http.MethodGet, "/api/reports/source", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"mode":"global"`) ||
		!strings.Contains(rec.Body.String(), `"effective_type":"sub2api"`) || !strings.Contains(rec.Body.String(), `"configured":true`) {
		t.Fatalf("默认报告源站响应异常: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPut, "/api/reports/source", map[string]any{
		"mode": "custom", "source_type": "sub2api", "base_url": "https://custom.example.com/", "credential": "custom-secret",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"base_url":"https://custom.example.com"`) ||
		!strings.Contains(rec.Body.String(), `"has_credential":true`) || strings.Contains(rec.Body.String(), "custom-secret") || strings.Contains(rec.Body.String(), `"credential"`) {
		t.Fatalf("保存源站响应异常或泄露凭据: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPut, "/api/reports/source", map[string]any{
		"mode": "custom", "source_type": "sub2api", "base_url": "https://custom-2.example.com", "credential": "",
	})
	settings, _, err := st.ScheduledReportSourceSettings()
	if rec.Code != http.StatusOK || err != nil || settings.Credential != "custom-secret" {
		t.Fatalf("同类型空凭据未保留: %d %s settings=%+v err=%v", rec.Code, rec.Body.String(), settings, err)
	}

	for _, payload := range []map[string]any{
		{"mode": "custom", "source_type": "newapi", "base_url": "not-a-url", "credential": "token", "newapi_user_id": 1},
		{"mode": "custom", "source_type": "newapi", "base_url": "https://new.example.com", "credential": "token", "newapi_user_id": 0},
		{"mode": "custom", "source_type": "newapi", "base_url": "https://new.example.com", "credential": "", "newapi_user_id": 1},
	} {
		rec = doJSON(t, handler, http.MethodPut, "/api/reports/source", payload)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("非法源站配置应返回 400: payload=%+v code=%d body=%s", payload, rec.Code, rec.Body.String())
		}
	}

	rec = doJSON(t, handler, http.MethodPut, "/api/reports/source", map[string]any{"mode": "global"})
	settings, _, err = st.ScheduledReportSourceSettings()
	if rec.Code != http.StatusOK || err != nil || settings.Mode != store.ScheduledReportSourceGlobal ||
		settings.BaseURL != "https://custom-2.example.com" || settings.Credential != "custom-secret" {
		t.Fatalf("全局模式未保留自定义配置: %d %s settings=%+v err=%v", rec.Code, rec.Body.String(), settings, err)
	}
}

func TestReportRunWithIncompleteCustomSourceReturnsPrecondition(t *testing.T) {
	handler, st := setupAPI(t, &fakeUpstream{groupCount: 1})
	if _, err := st.SaveScheduledReportSourceSettings(store.ScheduledReportSourceSettings{
		Mode: store.ScheduledReportSourceCustom, SourceType: store.ScheduledReportSourceNewAPI,
		BaseURL: "https://new.example.com", Credential: "", NewAPIUserID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, handler, http.MethodPost, "/api/reports/channel-usage/run", nil)
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "报告源站未配置完整") {
		t.Fatalf("未完整配置自定义源站应返回 412: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "custom-secret") || strings.Contains(rec.Body.String(), "api-key") {
		t.Fatalf("错误响应不应暴露凭据: %s", rec.Body.String())
	}
}

func TestChannelUsageReportRunWithoutMainConnectionReturnsPrecondition(t *testing.T) {
	fx := setupAuthAPI(t)
	fx.server.scheduledReports = reports.New(fx.store, upstream.New("", "", time.Second))
	cookie := &http.Cookie{Name: sessionCookie, Value: seedSession(t, fx.store, "report-user", "hunter2hunter2")}
	rec := raw(t, fx.handler, http.MethodPost, "/api/reports/channel-usage/run", nil, cookie)
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "sub2api 连接未配置") {
		t.Fatalf("未配置主连接应返回明确状态: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "admin") || strings.Contains(rec.Body.String(), "api-key") {
		t.Fatalf("错误响应不应暴露凭据: %s", rec.Body.String())
	}
}

func TestDailyReportAPIDefaultSaveRunAndHistory(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})

	rec := doJSON(t, handler, http.MethodGet, "/api/reports/daily", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"run_hour":23`) || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("每日报告默认配置异常: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPut, "/api/reports/daily", map[string]any{
		"enabled": true, "run_hour": 22, "timezone": "Asia/Shanghai",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"run_hour":22`) || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("每日报告配置保存异常: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPost, "/api/reports/daily/run", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) || !strings.Contains(rec.Body.String(), `"new_users":0`) {
		t.Fatalf("每日报告立即执行异常: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodGet, "/api/reports/daily/runs?page=1&page_size=20", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("每日报告运行历史异常: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDailyReportAPIRejectsInvalidRunHour(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})
	rec := doJSON(t, handler, http.MethodPut, "/api/reports/daily", map[string]any{
		"enabled": false, "run_hour": 24, "timezone": "Asia/Shanghai",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法每日执行小时应返回 400: %d %s", rec.Code, rec.Body.String())
	}
}
