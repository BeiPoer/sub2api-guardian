package channelmanager

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/store"
)

func (m *Manager) sub2APITurnstileToken(ctx context.Context, channel store.UpstreamChannel) (string, error) {
	if !channel.RequiresTurnstile {
		return "", nil
	}
	settings, err := m.store.UpstreamCaptchaSettings()
	if err != nil {
		return "", err
	}
	if !settings.Enabled || settings.ClientKey == "" {
		return "", invalid("该渠道需要 Turnstile 验证，请先启用 YesCaptcha 并配置 Client Key")
	}
	payload, _, err := m.requestJSON(ctx, http.MethodGet, sub2APIURL(channel, "/settings/public"), nil, nil)
	if err != nil {
		return "", fmt.Errorf("读取 Turnstile 配置失败：%w", err)
	}
	data, err := unwrapSub2API(payload)
	if err != nil {
		return "", err
	}
	record, ok := asObject(data)
	if !ok || record["text"] != nil {
		return "", invalid("无法读取 sub2api 公开设置；YesCaptcha 登录验证不支持 Cloudflare 整站拦截")
	}
	if !truthy(record["turnstile_enabled"]) {
		return "", invalid("该渠道已勾选 Turnstile 验证，但上游未启用；请检查渠道配置")
	}
	siteKey := strings.TrimSpace(stringValue(record["turnstile_site_key"]))
	if siteKey == "" {
		return "", invalid("上游已启用 Turnstile，但未提供 site key")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// 按创建任务的尝试计数，包含失败请求，不包含结果轮询。
	if err := m.store.IncrementUpstreamYesCaptchaCalls(channel.ID); err != nil {
		return "", fmt.Errorf("记录 YesCaptcha 调用次数失败：%w", err)
	}
	result, err := m.yesCaptchaRequest(ctx, "createTask", map[string]any{
		"clientKey": settings.ClientKey,
		"task":      map[string]any{"type": "TurnstileTaskProxyless", "websiteURL": strings.TrimRight(channel.BaseURL, "/") + "/login", "websiteKey": siteKey},
	})
	if err != nil {
		return "", err
	}
	taskID := result["taskId"]
	if stringValue(taskID) == "" {
		return "", invalid("YesCaptcha 未返回任务 ID")
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", invalid("YesCaptcha 验证超时或已取消，请稍后重试")
		case <-ticker.C:
			result, err = m.yesCaptchaRequest(ctx, "getTaskResult", map[string]any{"clientKey": settings.ClientKey, "taskId": taskID})
			if err != nil {
				return "", err
			}
			switch stringValue(result["status"]) {
			case "ready":
				solution, _ := asObject(result["solution"])
				token := strings.TrimSpace(stringValue(solution["token"]))
				if token == "" {
					return "", invalid("YesCaptcha 未返回 Turnstile token")
				}
				return token, nil
			case "processing":
			default:
				return "", invalid("YesCaptcha 返回未知任务状态")
			}
		}
	}
}

func (m *Manager) yesCaptchaRequest(ctx context.Context, method string, body any) (map[string]any, error) {
	payload, _, err := m.requestJSON(ctx, http.MethodPost, "https://api.yescaptcha.com/"+method, body, nil)
	// 不保存服务商原始响应，避免密钥或验证码进入渠道错误日志。
	if err != nil {
		return nil, invalid("YesCaptcha 请求失败，请检查网络或稍后重试")
	}
	record, ok := asObject(payload)
	if !ok || stringValue(record["errorId"]) != "0" {
		return nil, invalid("YesCaptcha 验证失败，请检查密钥、余额及服务商任务记录")
	}
	return record, nil
}
