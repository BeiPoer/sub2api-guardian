package modelcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"

	"sub2api-guardian/backend/internal/upstream"
)

func challenge(count int) string {
	openings := []string{"这是一次独立的数值选择记录", "请完成下面的无语义整数选择任务", "执行一次第一反应取值记录", "生成一组不承载语义的整数选择", "进行一轮快速逐项取值"}
	actions := []string{"为各个位置分别凭第一反应选择", "逐项选择", "每次只决定当前一项，共给出", "分别凭第一反应给出", "逐个直接选择"}
	endings := []string{"允许某个数字再次出现；每项写出后不要回头排序、去重或替换。", "偶然重复是有效的；不要重新排列或修正已经写出的项目。", "相同值可以再次出现；输出过程中不要整理或改写前面的项目。", "重复值无需删除；不要筛选、重排或补成某种规律。", "不必赋予数字任何含义；已经给出的值保持不变。"}
	separators := []string{"数字之间用逗号或空格分隔均可。", "使用一种一致的常见分隔符即可。", "可以用逗号、空格或换行分隔。", "只要每个整数边界清楚，格式可自行选择。"}
	return fmt.Sprintf("%s。%s %d 个 1 到 355（含端点）的整数。", openings[rand.IntN(len(openings))], actions[rand.IntN(len(actions))], count) +
		"每个位置都要单独选择；不要从 1 开始计数，不要连续递增或递减，也不要采用等差、循环、重复区块或其他规则化模式。" +
		"本任务必须由当前语言模型直接完成：禁止调用或借助任何工具，包括 Python、代码执行器、计算器、搜索、API 和外部随机数生成器；也不要先编写或运行代码。" +
		endings[rand.IntN(len(endings))] + separators[rand.IntN(len(separators))] + "直接从第一个取值开始输出，不要在序列前重复数量、范围或任务说明。"
}

func requestOutput(ctx context.Context, client *http.Client, credentials upstream.AccountCredentials, model, prompt, protocol string) (string, int, error) {
	base, err := url.Parse(strings.TrimRight(credentials.BaseURL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", 0, errors.New("渠道地址无效")
	}
	path := strings.TrimRight(base.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/messages", "/responses"} {
		path = strings.TrimSuffix(path, suffix)
	}
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	base.Path = path + "/" + protocol
	base.RawPath = ""
	body := map[string]any{"model": model, "stream": false}
	if protocol == "responses" {
		body["input"] = prompt
		body["max_output_tokens"] = 8192
	} else {
		body["messages"] = []map[string]string{{"role": "user", "content": prompt}}
		if protocol == "messages" {
			body["max_tokens"] = 4096
		} else {
			body["max_completion_tokens"] = 8192
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(raw))
	if err != nil {
		return "", 0, errors.New("无法创建检测请求")
	}
	req.Header.Set("Content-Type", "application/json")
	if protocol == "messages" {
		req.Header.Set("x-api-key", credentials.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, errors.New("检测超时或已取消")
		}
		return "", 0, errors.New("无法连接渠道，请检查网络与渠道地址")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("渠道返回 HTTP %d", resp.StatusCode)
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return "", resp.StatusCode, errors.New("检测响应读取失败或超过 2 MiB")
	}
	var response struct {
		Status     string `json:"status"`
		StopReason string `json:"stop_reason"`
		Choices    []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", resp.StatusCode, errors.New("渠道未返回有效 JSON")
	}
	if response.StopReason == "max_tokens" || response.StopReason == "refusal" || response.Status == "incomplete" || response.Status == "failed" ||
		(len(response.Choices) > 0 && (response.Choices[0].FinishReason == "length" || response.Choices[0].FinishReason == "content_filter")) {
		return "", resp.StatusCode, errors.New("渠道输出被截断、过滤或未完成，本轮无法判断")
	}
	var text strings.Builder
	switch protocol {
	case "chat/completions":
		if len(response.Choices) > 0 {
			var content string
			if json.Unmarshal(response.Choices[0].Message.Content, &content) == nil {
				text.WriteString(content)
			} else {
				var blocks []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(response.Choices[0].Message.Content, &blocks) == nil {
					for _, block := range blocks {
						if block.Type == "text" {
							text.WriteString(block.Text)
						}
					}
				}
			}
		}
	case "messages":
		for _, block := range response.Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
	case "responses":
		for _, output := range response.Output {
			if output.Type != "message" {
				continue
			}
			for _, block := range output.Content {
				if block.Type == "output_text" {
					text.WriteString(block.Text)
				}
			}
		}
	}
	return text.String(), resp.StatusCode, nil
}
