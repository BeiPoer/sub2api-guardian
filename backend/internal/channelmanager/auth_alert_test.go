package channelmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/store"
)

func TestManualTokenAuthAlerts(t *testing.T) {
	for _, channelType := range []store.UpstreamChannelType{store.UpstreamChannelSub2API, store.UpstreamChannelNewAPI} {
		for _, taskType := range []store.UpstreamTaskType{
			store.UpstreamTaskLowBalance, store.UpstreamTaskBurnRate, store.UpstreamTaskGroupAdded,
			store.UpstreamTaskGroupRemoved, store.UpstreamTaskGroupRatioChange,
		} {
			t.Run(string(channelType)+"/"+string(taskType), func(t *testing.T) {
				manager, st := testManager(t)
				var sends atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/cgi-bin/gettoken":
						writeTestJSON(w, map[string]any{"errcode": 0, "access_token": "wecom-token", "expires_in": 7200})
						return
					case "/cgi-bin/message/send":
						var payload struct {
							Text struct {
								Content string `json:"content"`
							} `json:"text"`
						}
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
						}
						if !strings.Contains(payload.Text.Content, "Token 鉴权失败告警") || !strings.Contains(payload.Text.Content, "检查并替换 Token") || strings.Contains(payload.Text.Content, "secret-token") {
							t.Errorf("鉴权失败通知不正确或泄露凭据: %s", payload.Text.Content)
						}
						sends.Add(1)
						writeTestJSON(w, map[string]any{"errcode": 0, "msgid": 1})
						return
					}
					if r.Header.Get("Authorization") != "Bearer replacement-token" {
						w.WriteHeader(http.StatusUnauthorized)
						writeTestJSON(w, map[string]any{"message": "invalid access token secret-token"})
						return
					}
					var data any = []any{}
					switch r.URL.Path {
					case "/api/v1/auth/me":
						data = map[string]any{"balance": 100}
					case "/api/user/self":
						data = map[string]any{"id": 9, "quota": 50000000}
					case "/api/status":
						data = map[string]any{"quota_per_unit": 500000}
					}
					if channelType == store.UpstreamChannelSub2API {
						writeTestJSON(w, map[string]any{"code": 0, "data": data})
					} else {
						writeTestJSON(w, map[string]any{"success": true, "data": data})
					}
				}))
				defer server.Close()
				manager.wecomBaseURL = server.URL
				if _, err := manager.SaveWeComSettings(store.UpstreamWeComSettings{CorpID: "corp", AgentID: 1, Secret: "wecom-secret", Target: "admin"}); err != nil {
					t.Fatal(err)
				}
				input := store.UpstreamChannelInput{
					Name: "手动 Token 渠道", Type: channelType, BaseURL: server.URL,
					Sub2APIManualAccessToken: "secret-token", NewAPIAccessToken: "secret-token", NewAPIUserID: "9",
				}
				channel, err := st.CreateUpstreamChannel(input)
				if err != nil {
					t.Fatal(err)
				}
				task, err := st.CreateUpstreamAutomationTask(store.UpstreamAutomationTask{
					ChannelID: channel.ID, Type: taskType, Enabled: true, IntervalMinutes: 5,
					LookbackMinutes: 60, CooldownMinutes: 30, Threshold: 10,
				})
				if err != nil {
					t.Fatal(err)
				}
				baseline := []any{map[string]any{"name": "pro", "ratio": 1}}
				if err := st.SaveUpstreamTaskState(task.ID, upstreamGroupStateKey, baseline); err != nil {
					t.Fatal(err)
				}
				now := time.Now()
				runDue := func(wantError bool) {
					t.Helper()
					if err := st.MarkUpstreamTaskRun(task.ID, now.Add(-10*time.Minute)); err != nil {
						t.Fatal(err)
					}
					if err := manager.RunDueTasks(context.Background()); (err != nil) != wantError {
						t.Fatalf("RunDueTasks error=%v wantError=%v", err, wantError)
					}
				}
				runDue(true)
				alerts, err := st.UpstreamAlertEvents(channel.ID, 10)
				if err != nil || len(alerts) != 1 {
					t.Fatalf("alerts=%+v err=%v", alerts, err)
				}
				alert := alerts[0]
				raw, _ := json.Marshal(alert)
				if alert.Type != upstreamAuthFailedAlert || alert.TaskID == nil || *alert.TaskID != task.ID || !alert.WeComSent || alert.EmailSent || alert.EmailError == "" || strings.Contains(string(raw), "secret-token") {
					t.Fatalf("鉴权告警、投递结果或脱敏异常: %s", raw)
				}
				updated, err := st.UpstreamAutomationTask(channel.ID, task.ID)
				if err != nil || updated.LastRunAt == "" || updated.LastAlertAt == "" {
					t.Fatalf("任务时间未记录: %+v err=%v", updated, err)
				}
				state, _, err := st.UpstreamTaskState(task.ID, upstreamGroupStateKey)
				stateJSON, _ := json.Marshal(state)
				baselineJSON, _ := json.Marshal(baseline)
				if err != nil || string(stateJSON) != string(baselineJSON) {
					t.Fatalf("失败时不应覆盖分组基线: %s err=%v", stateJSON, err)
				}
				if snapshot, err := st.LatestUpstreamBalanceSnapshot(channel.ID); err != nil || snapshot != nil {
					t.Fatalf("鉴权失败不应产生余额快照: %+v err=%v", snapshot, err)
				}
				runDue(true)
				if sends.Load() != 1 {
					t.Fatalf("冷却期内重复推送: %d", sends.Load())
				}
				if err := st.MarkUpstreamTaskAlert(task.ID, now.Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
				runDue(true)
				if sends.Load() != 2 {
					t.Fatalf("冷却期后未再次推送: %d", sends.Load())
				}
				input.Sub2APIManualAccessToken, input.NewAPIAccessToken = "replacement-token", "replacement-token"
				if _, err := st.UpdateUpstreamChannel(channel.ID, input); err != nil {
					t.Fatal(err)
				}
				runDue(false)
				if sends.Load() != 2 {
					t.Fatalf("替换 Token 后不应继续发送鉴权告警: %d", sends.Load())
				}
				if snapshot, err := st.LatestUpstreamBalanceSnapshot(channel.ID); err != nil || snapshot == nil || snapshot.Balance != 100 {
					t.Fatalf("替换 Token 后监控未恢复: %+v err=%v", snapshot, err)
				}
			})
		}
	}
}

func TestManualTokenAuthAlertErrorFormats(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channelType store.UpstreamChannelType
		status      int
		body        string
		path        string
		wantAlert   bool
	}{
		{"newapi business error", store.UpstreamChannelNewAPI, 200, `{"success":false,"message":"无权进行此操作，access token 无效"}`, "/api/user/self", true},
		{"sub2api business code", store.UpstreamChannelSub2API, 200, `{"code":401,"message":"unauthorized"}`, "/api/v1/auth/me", true},
		{"forbidden", store.UpstreamChannelSub2API, 403, `{"message":"access denied"}`, "/api/v1/auth/me", true},
		{"rates unauthorized", store.UpstreamChannelSub2API, 401, `{"message":"token expired"}`, "/api/v1/groups/rates", true},
		{"server error", store.UpstreamChannelNewAPI, 500, `{"message":"invalid access token"}`, "/api/user/self", false},
		{"rate limited", store.UpstreamChannelNewAPI, 429, `{"message":"too many requests"}`, "/api/user/self", false},
		{"unrelated business error", store.UpstreamChannelNewAPI, 200, `{"success":false,"message":"database unavailable"}`, "/api/user/self", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.path {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
					return
				}
				data := any([]any{})
				if r.URL.Path == "/api/v1/auth/me" {
					data = map[string]any{"balance": 100}
				}
				writeTestJSON(w, map[string]any{"code": 0, "data": data})
			}))
			defer server.Close()
			manager, st := testManager(t)
			channel, err := st.CreateUpstreamChannel(store.UpstreamChannelInput{
				Name: "渠道", Type: tc.channelType, BaseURL: server.URL, Sub2APIManualAccessToken: "manual", NewAPIAccessToken: "manual", NewAPIUserID: "9",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.CreateUpstreamAutomationTask(store.UpstreamAutomationTask{ChannelID: channel.ID, Type: store.UpstreamTaskGroupRatioChange, Enabled: true, IntervalMinutes: 5, LookbackMinutes: 60, CooldownMinutes: 30}); err != nil {
				t.Fatal(err)
			}
			if err := manager.RunDueTasks(context.Background()); err == nil {
				t.Fatal("上游失败应保留错误返回")
			}
			alerts, err := st.UpstreamAlertEvents(channel.ID, 10)
			if err != nil || (len(alerts) == 1) != tc.wantAlert {
				t.Fatalf("alerts=%+v wantAlert=%v err=%v", alerts, tc.wantAlert, err)
			}
		})
	}
	if isManualTokenAuthError(store.UpstreamChannel{Type: store.UpstreamChannelSub2API, Username: "u", Password: "p"}, upstreamError(401, "invalid token", nil)) {
		t.Fatal("账号密码渠道不应被标记为手动 Token 失效")
	}
}
