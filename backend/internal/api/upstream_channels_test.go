package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestOtherUpstreamChannelCRUDKeepsCredentials(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})
	rec := doJSON(t, handler, http.MethodPost, "/api/upstream-channels", map[string]any{
		"name": "记录渠道", "type": "other", "base_url": "example.com",
		"username": "operator", "password": "visible-secret",
		"recharge_ratio": 1.2, "recharge_methods": []string{"wechat", "card"}, "recharge_fee": "每笔 2 元",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID              int64    `json:"id"`
		Password        string   `json:"password"`
		BaseURL         string   `json:"base_url"`
		RechargeRatio   float64  `json:"recharge_ratio"`
		RechargeMethods []string `json:"recharge_methods"`
		RechargeFee     string   `json:"recharge_fee"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Password != "visible-secret" || created.BaseURL != "https://example.com" || created.RechargeRatio != 1.2 ||
		len(created.RechargeMethods) != 2 || created.RechargeMethods[0] != "wechat" || created.RechargeMethods[1] != "card" ||
		created.RechargeFee != "每笔 2 元" {
		t.Fatalf("创建响应异常: %+v", created)
	}

	rec = doJSON(t, handler, http.MethodPut, "/api/upstream-channels/"+itoa(created.ID), map[string]any{
		"name": "改名后", "password": "",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"password":"visible-secret"`) ||
		!strings.Contains(rec.Body.String(), `"recharge_fee":"每笔 2 元"`) ||
		!strings.Contains(rec.Body.String(), `"recharge_ratio":1.2`) {
		t.Fatalf("空密码应保留原值: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPut, "/api/upstream-channels/"+itoa(created.ID), map[string]any{
		"recharge_ratio": 0,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无效充值比例应被拒绝: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, handler, http.MethodPut, "/api/upstream-channels/"+itoa(created.ID), map[string]any{
		"recharge_methods": []string{"bank"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("无效充值方式应被拒绝: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPut, "/api/upstream-channels/"+itoa(created.ID), map[string]any{
		"recharge_methods": []string{},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"recharge_methods":[]`) {
		t.Fatalf("充值方式应支持清空: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodGet, "/api/upstream-channels", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("列表响应异常: %d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rec.Body.String(), "visible-secret") {
		t.Fatalf("用户选择的直接凭据展示未生效: %s", rec.Body.String())
	}
	for _, path := range []string{"groups", "tokens", "balance-history"} {
		rec = doJSON(t, handler, http.MethodGet, "/api/upstream-channels/"+itoa(created.ID)+"/"+path, nil)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("独立读取接口 %s 响应异常: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/upstream-channels/"+itoa(created.ID)+"/subscriptions", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("other 渠道订阅接口应返回 null: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/upstream-channels/"+itoa(created.ID)+"/balance-query-logs", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("余额查询日志兼容接口响应异常: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPost, "/api/upstream-channels/"+itoa(created.ID)+"/tasks", map[string]any{
		"type": "low_balance", "threshold": 10,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("other 渠道不应允许创建自动任务: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodDelete, "/api/upstream-channels/"+itoa(created.ID), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("删除应返回 JSON: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUpstreamSMTPPasswordIsMasked(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})
	rec := doJSON(t, handler, http.MethodPut, "/api/upstream-email-settings", map[string]any{
		"smtp_host": "smtp.example.com", "smtp_port": 587, "smtp_user": "mailer",
		"smtp_password": "smtp-secret", "smtp_from": "guardian@example.com",
		"default_recipients": []string{"ops@example.com"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("保存 SMTP 失败: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "smtp-secret") || !strings.Contains(rec.Body.String(), `"has_smtp_password":true`) {
		t.Fatalf("SMTP 密码泄露或配置标志缺失: %s", rec.Body.String())
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/upstream-email-settings", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "smtp-secret") || !strings.Contains(rec.Body.String(), `"has_smtp_password":true`) {
		t.Fatalf("读取 SMTP 设置不应回传密码: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUpstreamWeComSettingsReturnsSecret(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})
	rec := doJSON(t, handler, http.MethodPut, "/api/upstream-wecom-settings", map[string]any{
		"corp_id": "ww-corp", "agent_id": 1000002, "secret": "app-secret", "target": "zhangsan",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"secret":"app-secret"`) || !strings.Contains(rec.Body.String(), `"has_secret":true`) {
		t.Fatalf("保存企微设置响应异常或 Secret 未返回: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/upstream-wecom-settings", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"secret":"app-secret"`) || !strings.Contains(rec.Body.String(), `"has_secret":true`) {
		t.Fatalf("读取企微设置响应异常或 Secret 未返回: %d %s", rec.Code, rec.Body.String())
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
