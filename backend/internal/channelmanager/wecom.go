package channelmanager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/store"
)

const (
	wecomAPIBaseURL  = "https://qyapi.weixin.qq.com"
	wecomTokenBuffer = time.Minute
)

type wecomAPIError struct {
	action  string
	code    int
	message string
}

func (e *wecomAPIError) Error() string {
	return fmt.Sprintf("企业微信 %s 失败：%s（错误码 %d）", e.action, e.message, e.code)
}

func (m *Manager) WeComSettings() (store.UpstreamWeComSettings, error) {
	return m.store.UpstreamWeComSettings()
}

func (m *Manager) SaveWeComSettings(settings store.UpstreamWeComSettings) (store.UpstreamWeComSettings, error) {
	current, err := m.store.UpstreamWeComSettings()
	if err != nil {
		return store.UpstreamWeComSettings{}, err
	}
	if settings.Secret == "" {
		settings.Secret = current.Secret
	}
	if err := validateWeComSettings(settings, false); err != nil {
		return store.UpstreamWeComSettings{}, err
	}
	if current.CorpID != settings.CorpID || current.Secret != settings.Secret {
		m.invalidateWeComToken("")
	}
	return m.store.SaveUpstreamWeComSettings(settings)
}

func (m *Manager) TestWeCom(ctx context.Context, target string) (string, error) {
	return m.sendWeCom(ctx, target, "Sub2API Guardian 企微通知测试")
}

func (m *Manager) sendWeCom(ctx context.Context, target, content string) (string, error) {
	settings, err := m.store.UpstreamWeComSettings()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(target) != "" {
		settings.Target = strings.TrimSpace(target)
	}
	if err := validateWeComSettings(settings, true); err != nil {
		return "", err
	}

	accessToken, err := m.getWeComAccessToken(ctx, settings, false)
	if err != nil {
		return "", exposeWeComError(err)
	}
	messageID, err := m.sendWeComMessage(ctx, settings, accessToken, content)
	if isWeComTokenError(err) {
		m.invalidateWeComToken(accessToken)
		accessToken, err = m.getWeComAccessToken(ctx, settings, true)
		if err == nil {
			messageID, err = m.sendWeComMessage(ctx, settings, accessToken, content)
		}
	}
	if err != nil {
		return "", exposeWeComError(err)
	}
	return messageID, nil
}

func (m *Manager) getWeComAccessToken(ctx context.Context, settings store.UpstreamWeComSettings, force bool) (string, error) {
	m.wecomMu.Lock()
	defer m.wecomMu.Unlock()

	now := time.Now()
	if !force && m.wecomAccessToken != "" && now.Before(m.wecomTokenExpiresAt) {
		return m.wecomAccessToken, nil
	}
	endpoint, err := m.wecomEndpoint("/cgi-bin/gettoken", url.Values{
		"corpid":     []string{settings.CorpID},
		"corpsecret": []string{settings.Secret},
	})
	if err != nil {
		return "", err
	}
	payload, _, err := m.requestJSON(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return "", err
	}
	record, err := parseWeComResponse(payload, "gettoken")
	if err != nil {
		return "", err
	}
	accessToken := strings.TrimSpace(stringValue(record["access_token"]))
	if accessToken == "" {
		return "", &wecomAPIError{action: "gettoken", message: "响应未返回 access_token"}
	}
	expiresIn := 7200
	if value, ok := positiveInt(record["expires_in"]); ok {
		expiresIn = value
	}
	expiresAt := now.Add(time.Duration(expiresIn) * time.Second).Add(-wecomTokenBuffer)
	if !expiresAt.After(now) {
		expiresAt = now.Add(time.Second)
	}
	m.wecomAccessToken = accessToken
	m.wecomTokenExpiresAt = expiresAt
	return accessToken, nil
}

func (m *Manager) sendWeComMessage(ctx context.Context, settings store.UpstreamWeComSettings, accessToken, content string) (string, error) {
	endpoint, err := m.wecomEndpoint("/cgi-bin/message/send", url.Values{
		"access_token": []string{accessToken},
	})
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"touser":  settings.Target,
		"msgtype": "text",
		"agentid": settings.AgentID,
		"text": map[string]string{
			"content": content,
		},
	}
	response, _, err := m.requestJSON(ctx, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return "", err
	}
	record, err := parseWeComResponse(response, "message/send")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stringValue(record["msgid"])), nil
}

func (m *Manager) wecomEndpoint(path string, query url.Values) (string, error) {
	base := strings.TrimRight(m.wecomBaseURL, "/")
	if base == "" {
		base = wecomAPIBaseURL
	}
	parsed, err := url.Parse(base + path)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", invalid("企业微信 API 地址无效")
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}

func parseWeComResponse(payload any, action string) (map[string]any, error) {
	record, ok := asObject(payload)
	if !ok {
		return nil, &wecomAPIError{action: action, message: "响应格式异常"}
	}
	codeValue, ok := finiteNumber(record["errcode"])
	if !ok {
		return nil, &wecomAPIError{action: action, message: "响应缺少 errcode"}
	}
	code := int(codeValue)
	if code != 0 {
		message := strings.TrimSpace(stringValue(record["errmsg"]))
		if message == "" {
			message = "未知错误"
		}
		if code == 60020 {
			message = wecomIPWhitelistMessage(message)
		}
		return nil, &wecomAPIError{action: action, code: code, message: message}
	}
	return record, nil
}

func wecomIPWhitelistMessage(errmsg string) string {
	const marker = "from ip: "
	message := "企业微信 IP 白名单校验失败，请在应用设置的“企业可信 IP”列表中添加本服务器 IP"
	index := strings.Index(errmsg, marker)
	if index < 0 {
		return message
	}
	ip := strings.TrimSpace(errmsg[index+len(marker):])
	if comma := strings.IndexByte(ip, ','); comma >= 0 {
		ip = strings.TrimSpace(ip[:comma])
	}
	if ip == "" {
		return message
	}
	return fmt.Sprintf("%s [%s]", message, ip)
}

func isWeComTokenError(err error) bool {
	var apiErr *wecomAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.code == 40014 || apiErr.code == 42001
}

func (m *Manager) invalidateWeComToken(expected string) {
	m.wecomMu.Lock()
	defer m.wecomMu.Unlock()
	if expected == "" || m.wecomAccessToken == expected {
		m.wecomAccessToken = ""
		m.wecomTokenExpiresAt = time.Time{}
	}
}

func exposeWeComError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *wecomAPIError
	if errors.As(err, &apiErr) {
		return &Error{Status: http.StatusBadGateway, UpstreamCode: apiErr.code, Message: apiErr.Error()}
	}
	return err
}

func validateWeComSettings(settings store.UpstreamWeComSettings, requireComplete bool) error {
	for _, value := range []string{settings.CorpID, settings.Secret, settings.Target} {
		if strings.ContainsAny(value, "\r\n") {
			return invalid("企微配置不能包含换行符")
		}
	}
	if settings.AgentID < 0 {
		return invalid("企微应用 AgentId 无效")
	}
	if requireComplete {
		switch {
		case strings.TrimSpace(settings.CorpID) == "":
			return invalid("企微企业 ID 未配置")
		case strings.TrimSpace(settings.Secret) == "":
			return invalid("企微应用 Secret 未配置")
		case settings.AgentID <= 0:
			return invalid("企微应用 AgentId 未配置")
		case strings.TrimSpace(settings.Target) == "":
			return invalid("企微接收人未配置")
		}
	}
	return nil
}
