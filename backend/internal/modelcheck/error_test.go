package modelcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"sub2api-guardian/backend/internal/upstream"
)

func TestUpstreamErrorKeepsDiagnosticsAndRedactsCredentials(t *testing.T) {
	raw := []byte(`{"type":"error","request_id":"req_123","error":{"type":"rate_limit_error","code":429,"message":"Rate limit for channel-secret. Bearer another-token; api_key=other-secret; https://user:password@host/path","api_key":"hidden-key","details":{"message":"wait 10 seconds","password":"hidden-password"}},"request":{"headers":{"Authorization":"do-not-store"}},"output":"do-not-store"}`)
	message := upstreamResponseError("渠道返回流式接口错误", raw, "channel-secret").Error()
	for _, want := range []string{"rate_limit_error", "429", "req_123", "Rate limit", "wait 10 seconds", "[redacted]"} {
		if !strings.Contains(message, want) {
			t.Fatalf("missing %q: %s", want, message)
		}
	}
	for _, secret := range []string{"channel-secret", "another-token", "other-secret", "user:password", "hidden-key", "hidden-password", "do-not-store"} {
		if strings.Contains(message, secret) {
			t.Fatalf("leaked %q: %s", secret, message)
		}
	}
}

func TestUpstreamErrorEncodedSecretsAndLength(t *testing.T) {
	for _, raw := range []string{
		`{"error":{"message":"key: secret\"/token 超限"}}`,
		`token rejected: secret%22%2Ftoken`,
		`<html>upstream failed api_key="other-secret" Authorization: Bearer second-secret sk-abcdefghijklmn</html>`,
	} {
		message := upstreamResponseError("error", []byte(raw), `secret"/token`).Error()
		for _, secret := range []string{`secret`, "second-secret", "sk-abcdefghijklmn"} {
			if strings.Contains(message, secret) {
				t.Fatalf("secret leaked: %s", message)
			}
		}
	}
	message := upstreamResponseError("error", []byte(strings.Repeat("错", 5000)), "").Error()
	if utf8.RuneCountInString(message) > 4200 || !strings.Contains(message, "已截断") || !utf8.ValidString(message) {
		t.Fatal("error display not safely bounded")
	}
}

func TestHTTPAndJSONErrorsExposeSanitizedResponse(t *testing.T) {
	for _, test := range []struct {
		status     int
		body, want string
	}{
		{429, `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Too many requests channel-secret"},"request_id":"req_123"}`, "rate_limit_exceeded"},
		{502, `upstream unavailable channel-secret`, "upstream unavailable"},
		{200, `{"error":{"code":"insufficient_quota","message":"Quota exceeded channel-secret"}}`, "insufficient_quota"},
		{200, `<html>gateway unavailable channel-secret</html>`, "gateway unavailable"},
	} {
		t.Run(fmt.Sprint(test.status, test.want), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); fmt.Fprint(w, test.body) }))
			defer server.Close()
			_, status, err := requestOutput(context.Background(), server.Client(), upstream.AccountCredentials{BaseURL: server.URL, APIKey: "channel-secret"}, "model", "prompt", "chat/completions")
			if err == nil || status != test.status || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "channel-secret") {
				t.Fatalf("error response: %d %v", status, err)
			}
		})
	}
}

func TestStreamingFailureDetailsSurviveRetriesAndPersistence(t *testing.T) {
	for _, protocol := range []string{"chat/completions", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			event := `{"type":"error","error":{"type":"overloaded_error","message":"Capacity exceeded channel-secret"},"request_id":"req-stream"}`
			if protocol == "responses" {
				event = `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"Capacity exceeded channel-secret"},"output":"do-not-store"},"request_id":"req-stream"}`
			}
			text, err := readStreamOutput(strings.NewReader("data: "+event+"\n\n"), protocol, "channel-secret")
			if text != "" || err == nil || !strings.Contains(err.Error(), "Capacity exceeded") || strings.Contains(err.Error(), "channel-secret") || strings.Contains(err.Error(), "do-not-store") {
				t.Fatalf("stream error: %q %v", text, err)
			}
		})
	}
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"code\":\"overloaded\",\"message\":\"Capacity exceeded channel-secret\"}}\n\n")
	})
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	runs, err := m.store.ModelCheckRuns(1, "", 0, 1)
	if err != nil || len(runs) != 1 || calls.Load() != 4 || runs[0].Status != "error" {
		t.Fatalf("retry failure: %+v %v", runs, err)
	}
	raw, _ := json.Marshal(runs)
	if !strings.Contains(runs[0].Message, "overloaded") || !strings.Contains(runs[0].Message, "Capacity exceeded") || strings.Contains(string(raw), "channel-secret") {
		t.Fatalf("persisted error lost diagnostics or leaked key: %s", raw)
	}
}
