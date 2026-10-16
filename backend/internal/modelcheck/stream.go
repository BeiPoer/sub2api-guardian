package modelcheck

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// readStreamOutput collects only answer text; reasoning and final snapshots must not enter the fingerprint.
func readStreamOutput(reader io.Reader, protocol string) (string, error) {
	buffer := bufio.NewReader(reader)
	var text strings.Builder
	var data []string
	var eventType string
	completed := false
	incomplete := errors.New("检测流未正常结束，请重试")
	consume := func() (bool, error) {
		if len(data) == 0 {
			eventType = ""
			return false, nil
		}
		payload := strings.TrimSpace(strings.Join(data, "\n"))
		data = nil
		name := eventType
		eventType = ""
		if payload == "[DONE]" {
			if protocol != "chat/completions" || !completed {
				return false, incomplete
			}
			return true, nil
		}
		var event struct {
			Type         string          `json:"type"`
			Error        json.RawMessage `json:"error"`
			Delta        json.RawMessage `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
			Response struct {
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"response"`
			Choices []struct {
				Index        int    `json:"index"`
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content string `json:"content"`
					Refusal string `json:"refusal"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &event) != nil {
			return false, errors.New("检测流事件格式无效")
		}
		if event.Type != "" {
			name = event.Type
		}
		if name == "error" || name == "response.failed" || event.Response.Status == "failed" ||
			(len(event.Error) > 0 && string(event.Error) != "null") || (len(event.Response.Error) > 0 && string(event.Response.Error) != "null") {
			return false, errors.New("渠道返回流式接口错误")
		}
		switch protocol {
		case "chat/completions":
			for _, choice := range event.Choices {
				if choice.Index != 0 {
					continue
				}
				if choice.Delta.Refusal != "" {
					return false, errInvalidOutput
				}
				text.WriteString(choice.Delta.Content)
				if choice.FinishReason != "" {
					if choice.FinishReason != "stop" {
						return false, errInvalidOutput
					}
					completed = true
				}
			}
		case "responses":
			switch name {
			case "response.output_text.delta":
				var delta string
				if json.Unmarshal(event.Delta, &delta) != nil {
					return false, errors.New("检测流文本格式无效")
				}
				text.WriteString(delta)
			case "response.output_text.done":
				text.WriteByte('\n')
			case "response.refusal.delta", "response.refusal.done", "response.incomplete":
				return false, errInvalidOutput
			case "response.completed":
				if event.Response.Status != "completed" {
					return false, incomplete
				}
				return true, nil
			}
		case "messages":
			switch name {
			case "content_block_start":
				if event.ContentBlock.Type == "text" {
					if text.Len() > 0 {
						text.WriteByte('\n')
					}
					text.WriteString(event.ContentBlock.Text)
				}
			case "content_block_delta", "message_delta":
				var delta struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					StopReason string `json:"stop_reason"`
				}
				if json.Unmarshal(event.Delta, &delta) != nil {
					return false, errors.New("检测流文本格式无效")
				}
				if name == "content_block_delta" && delta.Type == "text_delta" {
					text.WriteString(delta.Text)
				}
				if name == "message_delta" && delta.StopReason != "" {
					if delta.StopReason != "end_turn" && delta.StopReason != "stop_sequence" {
						return false, errInvalidOutput
					}
					completed = true
				}
			case "message_stop":
				if !completed {
					return false, incomplete
				}
				return true, nil
			}
		}
		return false, nil
	}
	for {
		line, readErr := buffer.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", responseReadError(readErr)
		}
		if len(line) == 0 && readErr != nil {
			break
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			done, err := consume()
			if err != nil {
				return "", err
			}
			if done {
				return text.String(), nil
			}
		} else {
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "data":
				data = append(data, value)
			case "event":
				eventType = value
			}
		}
		if readErr != nil {
			break
		}
	}
	if len(data) > 0 {
		done, err := consume()
		if err != nil {
			return "", err
		}
		if done {
			return text.String(), nil
		}
	}
	if protocol == "chat/completions" && completed {
		return text.String(), nil
	}
	return "", incomplete
}
