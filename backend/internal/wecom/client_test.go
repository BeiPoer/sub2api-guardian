package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientCachesTokensPerCredentialsAndSendsMarkdown(t *testing.T) {
	var tokenCalls atomic.Int64
	var messageCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			tokenCalls.Add(1)
			corpID := r.URL.Query().Get("corpid")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errcode": 0, "errmsg": "ok", "access_token": "token-" + corpID, "expires_in": 7200,
			})
		case "/cgi-bin/message/send":
			messageCalls.Add(1)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("解析 Markdown 请求失败: %v", err)
			} else if payload["msgtype"] != "markdown" {
				t.Errorf("消息类型 = %v，期望 markdown", payload["msgtype"])
			} else if markdown, ok := payload["markdown"].(map[string]any); !ok || !strings.Contains(markdown["content"].(string), "测试") {
				t.Errorf("Markdown 请求体异常: %+v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok", "msgid": messageCalls.Load()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(&http.Client{Timeout: 5 * time.Second})
	client.SetBaseURL(server.URL)
	first := Settings{CorpID: "corp-a", AgentID: 1, Secret: "secret-a", Target: "@all"}
	second := Settings{CorpID: "corp-b", AgentID: 1, Secret: "secret-b", Target: "zhangsan"}
	if _, err := client.Send(context.Background(), first, Markdown, "测试 A"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Send(context.Background(), first, Markdown, "测试 A2"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Send(context.Background(), second, Markdown, "测试 B"); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 2 || messageCalls.Load() != 3 {
		t.Fatalf("token 缓存未按凭据隔离: token=%d messages=%d", tokenCalls.Load(), messageCalls.Load())
	}
}
