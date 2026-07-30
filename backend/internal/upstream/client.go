// Package upstream 封装对 sub2api 管理端 API 的访问。
//
// 所有请求都用 `x-api-key` 认证，可访问整个 /api/v1/admin 组。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client 是 sub2api 管理端客户端，可在运行期热更新连接配置。
type Client struct {
	mu      sync.RWMutex
	baseURL string
	apiKey  string
	http    *http.Client
}

// ErrNotConfigured 表示尚未填写 sub2api 地址或管理端 Key。
var ErrNotConfigured = errors.New("sub2api 连接未配置：请填写地址与 Admin API Key")

// ErrMonitoringDisabled 表示 sub2api 的运维监控（ops）未开启，无法读取真实流量。
var ErrMonitoringDisabled = errors.New("sub2api 运维监控未开启，无法读取真实流量样本")

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type page[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Pages    int   `json:"pages"`
}

// New 创建客户端。
func New(baseURL, apiKey string, timeout time.Duration) *Client {
	c := &Client{}
	c.Reconfigure(baseURL, apiKey, timeout)
	return c
}

// Reconfigure 热更新地址、Key 和超时。
func (c *Client) Reconfigure(baseURL, apiKey string, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	c.apiKey = strings.TrimSpace(apiKey)
	// 整体替换 client，避免并发请求期间修改共享 Timeout 产生数据竞争。
	c.http = &http.Client{Timeout: timeout}
}

// Ready 报告连接配置是否完整。
func (c *Client) Ready() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.baseURL == "" || c.apiKey == "" {
		return ErrNotConfigured
	}
	return nil
}

// BaseURL 返回当前配置的 sub2api 地址。
func (c *Client) BaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

func (c *Client) snapshot() (string, string, *http.Client) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL, c.apiKey, c.http
}

// APIError 是 sub2api 返回的非 2xx 响应。
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sub2api %s %s 返回 %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// StatusCodeOf 提取错误里的 HTTP 状态码，非 APIError 返回 0。
func StatusCodeOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	if err := c.Ready(); err != nil {
		return err
	}
	baseURL, apiKey, httpClient := c.snapshot()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := envelopeMessage(raw)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Message: msg}
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Data != nil {
		if env.Code != 0 && env.Message != "" {
			return errors.New(env.Message)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(env.Data, out)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// fetchAllPages 循环拉取分页接口，pathFn 接受页码返回完整路径。
func fetchAllPages[T any](ctx context.Context, c *Client, pathFn func(pageNum int) string) ([]T, error) {
	var out []T
	for pageNum := 1; pageNum <= 100; pageNum++ {
		var data page[T]
		if err := c.request(ctx, http.MethodGet, pathFn(pageNum), nil, &data); err != nil {
			return nil, err
		}
		out = append(out, data.Items...)
		if len(data.Items) == 0 || pageNum >= data.Pages {
			break
		}
	}
	return out, nil
}

func envelopeMessage(raw []byte) string {
	var env envelope
	if err := json.Unmarshal(raw, &env); err == nil {
		return env.Message
	}
	return ""
}

func anyFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
