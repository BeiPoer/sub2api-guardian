package channelmanager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"sub2api-guardian/backend/internal/store"
)

type captchaTransport func(*http.Request) (*http.Response, error)

func (f captchaTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSub2APICaptchaLogin(t *testing.T) {
	for _, scenario := range []string{"unchecked", "disabled", "no-captcha", "success", "provider-error", "missing-token", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			m, st := testManager(t)
			if err := st.SaveUpstreamCaptchaSettings(store.UpstreamCaptchaSettings{Enabled: scenario != "disabled", ClientKey: "secret-key"}); err != nil {
				t.Fatal(err)
			}
			channel, err := st.CreateUpstreamChannel(store.UpstreamChannelInput{Type: store.UpstreamChannelSub2API, BaseURL: "https://upstream.example", Username: "user@example.com", Password: "password", RequiresTurnstile: scenario != "unchecked"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			creates, logins, polls := 0, 0, 0
			defer func() {
				stored, err := st.UpstreamChannel(channel.ID)
				if err != nil || stored.YesCaptchaCalls != int64(creates) {
					t.Errorf("调用次数应等于创建任务次数，不包含轮询或会话复用: got=%d want=%d err=%v", stored.YesCaptchaCalls, creates, err)
				}
			}()
			m.client.Transport = captchaTransport(func(r *http.Request) (*http.Response, error) {
				var body map[string]any
				if r.Body != nil {
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
				}
				response := ""
				switch r.URL.Path {
				case "/api/v1/settings/public":
					if scenario == "disabled" || scenario == "unchecked" {
						t.Fatal("disabled should not read settings")
					}
					response = `{"code":0,"data":{"turnstile_enabled":true,"turnstile_site_key":"site-key"}}`
					if scenario == "no-captcha" {
						response = `{"code":0,"data":{"turnstile_enabled":false}}`
					}
				case "/createTask":
					creates++
					if r.URL.Host != "api.yescaptcha.com" || body["clientKey"] != "secret-key" {
						t.Fatal("wrong provider request")
					}
					task := body["task"].(map[string]any)
					if len(body) != 2 || len(task) != 3 || task["websiteURL"] != "https://upstream.example/login" || task["websiteKey"] != "site-key" || task["type"] != "TurnstileTaskProxyless" {
						t.Fatalf("unexpected task: %v", task)
					}
					response = `{"errorId":0,"taskId":123}`
					if scenario == "provider-error" {
						response = `{"errorId":1,"errorDescription":"secret-key"}`
					}
					if scenario == "cancelled" {
						cancel()
					}
				case "/getTaskResult":
					polls++
					if body["taskId"] != float64(123) {
						t.Fatal("task ID changed")
					}
					response = `{"errorId":0,"status":"ready","solution":{"token":"solved-token"}}`
					if scenario == "success" && polls == 1 {
						response = `{"errorId":0,"status":"processing"}`
					}
					if scenario == "missing-token" {
						response = `{"errorId":0,"status":"ready","solution":{}}`
					}
				case "/api/v1/auth/login":
					logins++
					if body["email"] != channel.Username || body["password"] != channel.Password {
						t.Fatal("missing credentials")
					}
					if scenario == "success" && body["turnstile_token"] != "solved-token" {
						t.Fatal("missing token")
					}
					if scenario != "success" && body["turnstile_token"] != nil {
						t.Fatal("unexpected token")
					}
					response = `{"code":0,"data":{"access_token":"access","refresh_token":"refresh","expires_in":3600}}`
				default:
					t.Fatalf("unexpected URL: %s", r.URL)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			got, err := m.sub2APILogin(ctx, channel)
			if scenario == "provider-error" || scenario == "missing-token" || scenario == "cancelled" || scenario == "disabled" || scenario == "no-captcha" {
				if err == nil || logins != 0 || strings.Contains(err.Error(), "secret-key") {
					t.Fatalf("unsafe failure: %v, logins=%d", err, logins)
				}
				return
			}
			if err != nil || got.Sub2APIAccessToken != "access" || logins != 1 {
				t.Fatalf("login failed: %v", err)
			}
			if scenario == "success" && creates != 1 {
				t.Fatal("expected one paid task")
			}
			if scenario != "success" && creates != 0 {
				t.Fatal("unexpected paid task")
			}
			if _, err := m.ensureSub2APIAccess(ctx, got, false); err != nil || logins != 1 {
				t.Fatal("session was not reused")
			}
		})
	}
}
