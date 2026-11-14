package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestUpstreamCaptchaSettings(t *testing.T) {
	handler, _ := setupAPI(t, &fakeUpstream{groupCount: 1})
	for _, step := range []struct {
		body   map[string]any
		status int
		want   string
	}{
		{map[string]any{"enabled": true}, 400, "Client Key"},
		{map[string]any{"enabled": true, "client_key": " secret-value "}, 200, `"has_client_key":true`},
		{map[string]any{"enabled": false, "client_key": ""}, 200, `"has_client_key":true`},
		{map[string]any{"enabled": true}, 200, `"enabled":true`},
	} {
		rec := doJSON(t, handler, http.MethodPut, "/api/upstream-captcha-settings", step.body)
		if rec.Code != step.status || !strings.Contains(rec.Body.String(), step.want) || strings.Contains(rec.Body.String(), "secret-value") {
			t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := doJSON(t, handler, http.MethodGet, "/api/upstream-captcha-settings", nil)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret-value") || !strings.Contains(rec.Body.String(), `"has_client_key":true`) {
		t.Fatalf("unexpected settings: %s", rec.Body.String())
	}
}
