package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProbeResult 是一次主动探测的结果。
type ProbeResult struct {
	Success bool

	// Model 是请求的模型（Guardian 指定的那个）。
	Model string

	// ActualModel 是 sub2api 实际使用的模型，取自 SSE 的 test_start 事件。
	//
	// 两者可能不同：sub2api 对 apikey 账号会应用账号级的通配符模型映射
	// （service.Account.GetMappedModel），把请求的模型重写掉。
	// 分开记录才能让这种偏差可见，否则用户会以为「指定的模型没生效」。
	ActualModel string

	Message    string
	TTFBMs     int64 // 首个内容事件的到达时间
	DurationMs int64 // 整个测试流的耗时
	StatusCode int   // 非 2xx 时的 HTTP 状态码
	Timeout    bool
	Raw        string
}

// ModelRewritten 报告 sub2api 是否把请求的模型改成了别的。
func (r ProbeResult) ModelRewritten() bool {
	return r.Model != "" && r.ActualModel != "" && r.Model != r.ActualModel
}

// Probe 通过 sub2api 的账号测试接口做一次探测。
//
// 该接口是 SSE 流：test_start → status/content → test_complete 或 error。
// 首个 content/image 事件的时间近似为首字时间（TTFB）。
func (c *Client) Probe(ctx context.Context, accountID int64, model, prompt string) (ProbeResult, error) {
	if err := c.Ready(); err != nil {
		return ProbeResult{}, err
	}
	baseURL, apiKey, httpClient := c.snapshot()

	body, err := json.Marshal(map[string]string{
		"model_id": strings.TrimSpace(model),
		"prompt":   prompt,
	})
	if err != nil {
		return ProbeResult{}, err
	}

	path := fmt.Sprintf("/api/v1/admin/accounts/%d/test", accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return ProbeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-api-key", apiKey)

	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		return ProbeResult{
			Success:    false,
			Message:    err.Error(),
			DurationMs: time.Since(start).Milliseconds(),
			Timeout:    isTimeout(err),
		}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		msg := envelopeMessage(raw)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		result := ProbeResult{
			Success:    false,
			Message:    msg,
			StatusCode: resp.StatusCode,
			DurationMs: time.Since(start).Milliseconds(),
			Raw:        string(raw),
		}
		return result, &APIError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Message: msg}
	}

	result := ProbeResult{Model: strings.TrimSpace(model)}
	var lines []string

	// 判定用的三个独立事实，不靠字段的零值兼职表达：
	//   sawContent —— 收到过内容，TTFB 有效（0ms 也是有效值，不能用 TTFBMs==0 代替）
	//   sawFailure —— 出现过明确失败，粘性，后续事件不能翻转
	//   sawSuccess —— 收到过明确的 success=true
	var sawContent, sawFailure, sawSuccess bool

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		lines = append(lines, payload)
		if payload == "[DONE]" {
			break
		}

		var event struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Model   string `json:"model"`
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		// 记录 sub2api 实际使用的模型。不要写回 result.Model：
		// 那是我们请求的模型，两者的差异本身就是要暴露的信息。
		if result.ActualModel == "" && event.Model != "" {
			result.ActualModel = event.Model
		}
		switch event.Type {
		case "content", "image":
			if !sawContent {
				sawContent = true
				result.TTFBMs = time.Since(start).Milliseconds()
			}
		case "test_complete":
			// 只认明确的 success == true。
			//
			// sub2api 的 TestEvent.Success 带 omitempty，success=false 时该字段
			// 在 JSON 里根本不出现；早期实现用 `event.Success || event.Error == ""`
			// 兜底，等于把「没说自己成功」当成了成功，会把不可用的渠道留在池里。
			// 上游失败一律走 error 事件，因此这里不需要任何兜底。
			if event.Error != "" {
				sawFailure = true
				if result.Message == "" || result.Message == "测试完成" {
					result.Message = event.Error
				}
				break
			}
			if event.Success {
				sawSuccess = true
				if result.Message == "" {
					result.Message = "测试完成"
				}
				break
			}
			sawFailure = true
			if result.Message == "" {
				result.Message = "测试完成事件未声明成功"
			}
		case "error":
			sawFailure = true
			if event.Error != "" {
				result.Message = event.Error
			}
		case "status":
			if event.Text != "" && result.Message == "" {
				result.Message = event.Text
			}
		}
	}

	result.DurationMs = time.Since(start).Milliseconds()
	result.Raw = strings.Join(lines, "\n")

	// 失败是粘性的：出现过 error 就不再看后面的 test_complete。
	// 重试逻辑、多段流、代理拼接都可能产生「先 error 后 complete」的序列，
	// 让后者覆盖前者会把已确认的故障渠道判成健康。
	result.Success = sawSuccess && !sawFailure

	if err := scanner.Err(); err != nil {
		result.Success = false
		if result.Message == "" {
			result.Message = err.Error()
		}
		result.Timeout = isTimeout(err)
		return result, err
	}

	// 没有 content 事件时退化为整体耗时，保证 TTFB 始终有值。
	// 判据是 sawContent 而不是 TTFBMs == 0：首字时间真的是 0ms 时也得认。
	if !sawContent {
		result.TTFBMs = result.DurationMs
	}
	if result.Message == "" {
		result.Message = "测试流结束但未返回结果"
	}
	if !result.Success {
		return result, fmt.Errorf("%s", result.Message)
	}
	return result, nil
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	var timeouter interface{ Timeout() bool }
	if errors.As(err, &timeouter) && timeouter.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context canceled")
}
