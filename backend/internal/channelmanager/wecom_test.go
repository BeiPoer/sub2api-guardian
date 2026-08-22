package channelmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"sub2api-guardian/backend/internal/store"
)

func TestWeComDirectAPIRequestAndTokenCache(t *testing.T) {
	var tokenCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			if r.URL.Query().Get("corpid") != "ww-corp" || r.URL.Query().Get("corpsecret") != "app-secret" {
				t.Errorf("gettoken 参数异常: %s", r.URL.RawQuery)
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			tokenCalls.Add(1)
			writeTestJSON(w, map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "token-1", "expires_in": 7200})
		case "/cgi-bin/message/send":
			if r.URL.Query().Get("access_token") != "token-1" {
				t.Errorf("access_token 异常: %q", r.URL.Query().Get("access_token"))
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("解析 message/send 请求失败: %v", err)
				return
			}
			textPayload, _ := payload["text"].(map[string]any)
			if payload["touser"] != "zhangsan|lisi" || payload["msgtype"] != "text" ||
				payload["agentid"] != float64(1000002) || textPayload["content"] != "测试内容" {
				t.Errorf("message/send 请求体异常: %+v", payload)
			}
			writeTestJSON(w, map[string]any{"errcode": 0, "errmsg": "ok", "msgid": 1024})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager, _ := testManager(t)
	manager.wecomBaseURL = server.URL
	settings, err := manager.SaveWeComSettings(store.UpstreamWeComSettings{
		CorpID: "ww-corp", AgentID: 1000002, Secret: "app-secret", Target: "zhangsan|lisi",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(settings)
	if strings.Contains(string(encoded), "app-secret") || !settings.HasSecret {
		t.Fatalf("企微 Secret 泄露或标记缺失: %s / %+v", encoded, settings)
	}

	for i := 0; i < 2; i++ {
		id, err := manager.sendWeCom(context.Background(), "", "测试内容")
		if err != nil || id != "1024" {
			t.Fatalf("企微推送结果 = %q, err=%v", id, err)
		}
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("有效期内应复用 access_token，gettoken 调用次数=%d", tokenCalls.Load())
	}
}

func TestWeComRefreshesExpiredTokenOnce(t *testing.T) {
	var tokenCalls atomic.Int64
	var sendCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			call := tokenCalls.Add(1)
			writeTestJSON(w, map[string]any{"errcode": 0, "errmsg": "ok", "access_token": "token-" + strconv.FormatInt(call, 10), "expires_in": 7200})
			return
		}
		if r.URL.Path == "/cgi-bin/message/send" {
			if sendCalls.Add(1) == 1 {
				writeTestJSON(w, map[string]any{"errcode": 40014, "errmsg": "invalid access_token"})
				return
			}
			writeTestJSON(w, map[string]any{"errcode": 0, "errmsg": "ok", "msgid": 9})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	manager, _ := testManager(t)
	manager.wecomBaseURL = server.URL
	if _, err := manager.SaveWeComSettings(store.UpstreamWeComSettings{
		CorpID: "ww-corp", AgentID: 1000002, Secret: "app-secret", Target: "zhangsan",
	}); err != nil {
		t.Fatal(err)
	}
	if id, err := manager.sendWeCom(context.Background(), "", "测试"); err != nil || id != "9" {
		t.Fatalf("过期 token 重试失败: id=%q err=%v", id, err)
	}
	if tokenCalls.Load() != 2 || sendCalls.Load() != 2 {
		t.Fatalf("token 刷新次数异常: gettoken=%d send=%d", tokenCalls.Load(), sendCalls.Load())
	}
}

func TestWeComAPIErrorIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			writeTestJSON(w, map[string]any{"errcode": 40013, "errmsg": "invalid corpid"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	manager, _ := testManager(t)
	manager.wecomBaseURL = server.URL
	if _, err := manager.SaveWeComSettings(store.UpstreamWeComSettings{
		CorpID: "bad", AgentID: 1, Secret: "secret", Target: "zhangsan",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.sendWeCom(context.Background(), "", "测试"); err == nil || !strings.Contains(err.Error(), "invalid corpid") {
		t.Fatalf("企微错误响应未透传: %v", err)
	}
}

func TestWeComIPWhitelistErrorIsActionable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			writeTestJSON(w, map[string]any{"errcode": 60020, "errmsg": "not allow from ip: 203.0.113.10, more details"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	manager, _ := testManager(t)
	manager.wecomBaseURL = server.URL
	if _, err := manager.SaveWeComSettings(store.UpstreamWeComSettings{
		CorpID: "ww-corp", AgentID: 1, Secret: "app-secret", Target: "zhangsan",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.sendWeCom(context.Background(), "", "测试"); err == nil ||
		!strings.Contains(err.Error(), "企业可信 IP") || !strings.Contains(err.Error(), "203.0.113.10") {
		t.Fatalf("企微 IP 白名单错误提示不够明确: %v", err)
	}
}
