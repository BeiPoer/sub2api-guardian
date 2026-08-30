// Package wecom 封装企业微信应用消息协议，供不同类型的通知任务共享。
package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultAPIBaseURL = "https://qyapi.weixin.qq.com"

type MessageType string

const (
	Text     MessageType = "text"
	Markdown MessageType = "markdown"
)

type Settings struct {
	CorpID  string
	AgentID int64
	Secret  string
	Target  string
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

// APIError 表示企微返回的协议错误或 HTTP 错误。
type APIError struct {
	Action     string
	Code       int
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("企业微信 %s 失败：%s（错误码 %d）", e.Action, e.Message, e.Code)
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("企业微信 %s 失败：%s（HTTP %d）", e.Action, e.Message, e.StatusCode)
	}
	return fmt.Sprintf("企业微信 %s 失败：%s", e.Action, e.Message)
}

// ErrorCode 返回企微业务错误码，普通网络/HTTP 错误返回 0。
func ErrorCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return 0
}

func IsTokenError(err error) bool {
	code := ErrorCode(err)
	return code == 40014 || code == 42001
}

type Client struct {
	mu       sync.Mutex
	http     *http.Client
	baseURL  string
	tokens   map[string]cachedToken
	tokenTTL time.Duration
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Second}
	}
	return &Client{
		http:     httpClient,
		baseURL:  DefaultAPIBaseURL,
		tokens:   make(map[string]cachedToken),
		tokenTTL: time.Minute,
	}
}

// SetBaseURL 主要供测试替换官方地址；生产环境保持默认地址。
func (c *Client) SetBaseURL(value string) {
	c.mu.Lock()
	c.baseURL = strings.TrimRight(strings.TrimSpace(value), "/")
	c.mu.Unlock()
}

func Validate(settings Settings, requireComplete bool) error {
	for _, value := range []string{settings.CorpID, settings.Secret, settings.Target} {
		if strings.ContainsAny(value, "\r\n") {
			return errors.New("企微配置不能包含换行符")
		}
	}
	if settings.AgentID < 0 {
		return errors.New("企微应用 AgentId 无效")
	}
	if !requireComplete {
		return nil
	}
	switch {
	case strings.TrimSpace(settings.CorpID) == "":
		return errors.New("企微企业 ID 未配置")
	case strings.TrimSpace(settings.Secret) == "":
		return errors.New("企微应用 Secret 未配置")
	case settings.AgentID <= 0:
		return errors.New("企微应用 AgentId 未配置")
	case strings.TrimSpace(settings.Target) == "":
		return errors.New("企微接收人未配置")
	default:
		return nil
	}
}

// Send 获取/复用应用 token，并发送 text 或 markdown 应用消息。
func (c *Client) Send(ctx context.Context, settings Settings, kind MessageType, content string) (string, error) {
	if kind != Text && kind != Markdown {
		return "", errors.New("企微消息类型无效")
	}
	if err := Validate(settings, true); err != nil {
		return "", err
	}
	settings.CorpID = strings.TrimSpace(settings.CorpID)
	settings.Secret = strings.TrimSpace(settings.Secret)
	settings.Target = strings.TrimSpace(settings.Target)

	token, err := c.accessToken(ctx, settings, false)
	if err != nil {
		return "", err
	}
	messageID, err := c.sendMessage(ctx, settings, kind, content, token)
	if !IsTokenError(err) {
		return messageID, err
	}
	_ = c.invalidateToken(settings, token)
	token, err = c.accessToken(ctx, settings, true)
	if err != nil {
		return "", err
	}
	return c.sendMessage(ctx, settings, kind, content, token)
}

func (c *Client) accessToken(ctx context.Context, settings Settings, force bool) (string, error) {
	base := c.snapshotBaseURL()
	key := base + "\x00" + settings.CorpID + "\x00" + settings.Secret
	now := time.Now()
	c.mu.Lock()
	if !force {
		if cached, ok := c.tokens[key]; ok && cached.value != "" && now.Before(cached.expiresAt) {
			c.mu.Unlock()
			return cached.value, nil
		}
	}
	// 同一 Client 上的 token 请求串行化，避免并发报告/告警在缓存未命中时
	// 同时向企微申请多份 token。
	defer c.mu.Unlock()
	if !force {
		if cached, ok := c.tokens[key]; ok && cached.value != "" && now.Before(cached.expiresAt) {
			return cached.value, nil
		}
	}
	endpoint, err := endpointForBase(base, "/cgi-bin/gettoken", url.Values{
		"corpid":     []string{settings.CorpID},
		"corpsecret": []string{settings.Secret},
	})
	if err != nil {
		return "", err
	}
	payload, err := c.request(ctx, http.MethodGet, endpoint, nil, "gettoken")
	if err != nil {
		return "", err
	}
	record, err := parseResponse(payload, "gettoken")
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(stringValue(record["access_token"]))
	if token == "" {
		return "", &APIError{Action: "gettoken", Message: "响应未返回 access_token"}
	}
	expiresIn := 7200
	if value, ok := finiteNumber(record["expires_in"]); ok && value > 0 && value < float64(math.MaxInt) {
		expiresIn = int(value)
	}
	expiresAt := now.Add(time.Duration(expiresIn)*time.Second - c.tokenTTL)
	if !expiresAt.After(now) {
		expiresAt = now.Add(time.Second)
	}
	c.tokens[key] = cachedToken{value: token, expiresAt: expiresAt}
	return token, nil
}

func (c *Client) invalidateToken(settings Settings, expected string) error {
	key := c.snapshotBaseURL() + "\x00" + settings.CorpID + "\x00" + settings.Secret
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.tokens[key]; ok && (expected == "" || cached.value == expected) {
		delete(c.tokens, key)
	}
	return nil
}

func (c *Client) sendMessage(ctx context.Context, settings Settings, kind MessageType, content, token string) (string, error) {
	endpoint, err := c.endpoint("/cgi-bin/message/send", url.Values{"access_token": []string{token}})
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"touser":  settings.Target,
		"msgtype": string(kind),
		"agentid": settings.AgentID,
	}
	if kind == Markdown {
		payload["markdown"] = map[string]string{"content": content}
	} else {
		payload["text"] = map[string]string{"content": content}
	}
	response, err := c.request(ctx, http.MethodPost, endpoint, payload, "message/send")
	if err != nil {
		return "", err
	}
	record, err := parseResponse(response, "message/send")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stringValue(record["msgid"])), nil
}

func (c *Client) snapshotBaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	base := strings.TrimRight(strings.TrimSpace(c.baseURL), "/")
	if base == "" {
		return DefaultAPIBaseURL
	}
	return base
}

func (c *Client) endpoint(path string, query url.Values) (string, error) {
	return endpointForBase(c.snapshotBaseURL(), path, query)
}

func endpointForBase(base, path string, query url.Values) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(base, "/") + path)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", errors.New("企业微信 API 地址无效")
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (c *Client) request(ctx context.Context, method, endpoint string, body any, action string) (any, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var payload any
	if len(bytes.TrimSpace(raw)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			return nil, &APIError{Action: action, StatusCode: resp.StatusCode, Message: "响应格式异常"}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := "HTTP 请求失败"
		if record, ok := payload.(map[string]any); ok {
			message = strings.TrimSpace(stringValue(record["errmsg"]))
			if message == "" {
				message = strings.TrimSpace(stringValue(record["message"]))
			}
		}
		return nil, &APIError{Action: action, StatusCode: resp.StatusCode, Message: message}
	}
	return payload, nil
}

func parseResponse(payload any, action string) (map[string]any, error) {
	record, ok := payload.(map[string]any)
	if !ok {
		return nil, &APIError{Action: action, Message: "响应格式异常"}
	}
	codeValue, ok := finiteNumber(record["errcode"])
	if !ok {
		return nil, &APIError{Action: action, Message: "响应缺少 errcode"}
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
		return nil, &APIError{Action: action, Code: code, Message: message}
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

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(typed)
	}
}

func finiteNumber(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(stringValue(value)), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}
