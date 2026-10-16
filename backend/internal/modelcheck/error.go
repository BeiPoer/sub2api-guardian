package modelcheck

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	errorAuthorization  = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]+`)
	errorSecretField    = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|x-api-key|authorization|access[_-]?token|refresh[_-]?token|token|password|secret|cookie|set-cookie)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;<>]+)`)
	errorAPIKey         = regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]{8,}`)
	errorURLCredentials = regexp.MustCompile(`(https?://)[^/\s@]+@`)
)

// Keep diagnostic fields rather than an echoed request, headers, or generated output.
func upstreamResponseError(prefix string, raw []byte, apiKey string) error {
	redact := func(text string) string {
		if apiKey != "" {
			for _, secret := range []string{apiKey, url.QueryEscape(apiKey), url.PathEscape(apiKey)} {
				text = strings.ReplaceAll(text, secret, "[redacted]")
			}
		}
		text = errorAuthorization.ReplaceAllString(text, "[redacted authorization]")
		text = errorSecretField.ReplaceAllString(text, "${1}[redacted]")
		text = errorAPIKey.ReplaceAllString(text, "[redacted]")
		return errorURLCredentials.ReplaceAllString(text, "${1}[redacted]@")
	}
	var decoded any
	detail := ""
	if json.Unmarshal(raw, &decoded) == nil {
		var selectFields func(any) any
		selectFields = func(value any) any {
			switch v := value.(type) {
			case string:
				return redact(v)
			case map[string]any:
				selected := map[string]any{}
				for _, key := range []string{"type", "code", "message", "param", "request_id", "requestId", "status", "error", "detail", "details"} {
					if child, ok := v[key]; ok && child != nil {
						selected[key] = selectFields(child)
					}
				}
				if response, ok := v["response"].(map[string]any); ok {
					selected["response"] = selectFields(response)
				}
				return selected
			case []any:
				result := make([]any, len(v))
				for i, child := range v {
					result[i] = selectFields(child)
				}
				return result
			default:
				return value
			}
		}
		selected := selectFields(decoded)
		if text, ok := selected.(string); ok {
			detail = text
		} else {
			if encoded, err := json.MarshalIndent(selected, "", "  "); err == nil && string(encoded) != "{}" && string(encoded) != "null" {
				detail = string(encoded)
			}
		}
	} else {
		detail = redact(string(raw))
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return errors.New(prefix)
	}
	runes := []rune(detail)
	if len(runes) > 4096 {
		detail = string(runes[:4096]) + "\n…（错误详情已截断）"
	}
	return errors.New(prefix + "\n上游返回：" + detail)
}
