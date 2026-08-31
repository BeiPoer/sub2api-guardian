package channelmanager

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"sub2api-guardian/backend/internal/store"
)

func testManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st), st
}

func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestSyncSub2APIAndBuildLoginURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600,
		}})
	})
	mux.HandleFunc("/api/v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"access_token": "access-2", "refresh_token": "refresh-2", "expires_in": 3600,
		}})
	})
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer access-") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"id": 7, "balance": 42.5}})
		}
	})
	mux.HandleFunc("/api/v1/groups/available", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			writeTestJSON(w, map[string]any{"code": 0, "data": []any{map[string]any{"id": 3, "name": "pro"}}})
		}
	})
	mux.HandleFunc("/api/v1/groups/rates", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"3": 1.5}})
	})
	mux.HandleFunc("/api/v1/keys", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{
			"items": []any{map[string]any{"id": 11, "name": "main", "key": "key-1", "group_id": 3}}, "total": 1,
		}})
	})
	mux.HandleFunc("/api/v1/subscriptions/active", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"plan": "pro"}})
	})
	mux.HandleFunc("/api/v1/subscriptions/summary", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"used": 3}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	manager, st := testManager(t)
	var linkedBaseURL string
	var linked map[string]float64
	manager.SetMultiplierLinker(func(_ context.Context, channel store.UpstreamChannel, ratios map[string]float64) error {
		linkedBaseURL = channel.BaseURL
		linked = ratios
		return nil
	})
	channel, err := st.CreateUpstreamChannel(store.UpstreamChannelInput{
		Name: "sub", Type: store.UpstreamChannelSub2API, BaseURL: server.URL,
		Username: "user@example.com", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(context.Background(), channel.ID); err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Overview(channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if overview.LatestSnapshot == nil || overview.LatestSnapshot.Balance != 42.5 || len(normalizeCollection(overview.Tokens)) != 1 {
		t.Fatalf("同步结果异常: %+v", overview)
	}
	if linkedBaseURL != channel.BaseURL || linked["key-1"] != 1.5 {
		t.Fatalf("同步后未提取令牌倍率: url=%q ratios=%#v", linkedBaseURL, linked)
	}
	stored, _ := st.UpstreamChannel(channel.ID)
	if stored.Status != "active" || stored.Sub2APIAccessToken != "access-1" {
		t.Fatalf("渠道状态/会话异常: %+v", stored)
	}
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), "access-1") || strings.Contains(string(raw), "refresh-1") {
		t.Fatalf("服务端会话令牌不应序列化: %s", raw)
	}
	loginURL, err := manager.LoginURL(context.Background(), channel.ID)
	if err != nil || !strings.Contains(loginURL, "access_token=access-2") || !strings.Contains(loginURL, "/auth/oauth/callback#") {
		t.Fatalf("登录 URL=%q err=%v", loginURL, err)
	}
}

func TestSyncNewAPIQuotaAndTokenModels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer system-token" || r.Header.Get("New-Api-User") != "9" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var data any
		switch r.URL.Path {
		case "/api/user/self":
			data = map[string]any{"id": 9, "quota": 1000000, "used_quota": 100000}
		case "/api/status":
			data = map[string]any{"quota_display_type": "CNY", "quota_per_unit": 500000, "usd_exchange_rate": 7}
		case "/api/user/self/groups":
			data = map[string]any{"default": map[string]any{"ratio": 1}}
		case "/api/token/":
			data = map[string]any{"items": []any{map[string]any{"id": 1, "name": "limited"}}, "total": 1}
		case "/api/token/1":
			data = map[string]any{"id": 1, "name": "limited", "model_limits_enabled": true, "model_limits": "gpt-4o,claude-3-5"}
		default:
			http.NotFound(w, r)
			return
		}
		writeTestJSON(w, map[string]any{"success": true, "data": data})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	manager, st := testManager(t)
	channel, _ := st.CreateUpstreamChannel(store.UpstreamChannelInput{
		Name: "new", Type: store.UpstreamChannelNewAPI, BaseURL: server.URL,
		NewAPIAccessToken: "system-token", NewAPIUserID: "9",
	})
	if err := manager.Sync(context.Background(), channel.ID); err != nil {
		t.Fatal(err)
	}
	overview, _ := manager.Overview(channel.ID)
	if overview.LatestSnapshot == nil || overview.LatestSnapshot.Balance != 14 || overview.LatestSnapshot.UsedBalance == nil || math.Abs(*overview.LatestSnapshot.UsedBalance-1.4) > 1e-9 {
		t.Fatalf("额度换算异常: %+v", overview.LatestSnapshot)
	}
	models, err := manager.TokenModels(context.Background(), channel.ID, 1)
	if err != nil || models.Source != "token_limits" || strings.Join(models.Models, ",") != "gpt-4o,claude-3-5" {
		t.Fatalf("令牌模型异常: %+v err=%v", models, err)
	}
}

func TestUpstream401MapsToBadGateway(t *testing.T) {
	var loginCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		loginCalls.Add(1)
		writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"access_token": "bad-token", "expires_in": 3600}})
	})
	mux.HandleFunc("/api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "token expired"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	manager, st := testManager(t)
	channel, _ := st.CreateUpstreamChannel(store.UpstreamChannelInput{
		Name: "sub", Type: store.UpstreamChannelSub2API, BaseURL: server.URL, Username: "u", Password: "p",
	})
	err := manager.Sync(context.Background(), channel.ID)
	var managed *Error
	if !errors.As(err, &managed) || managed.Status != http.StatusBadGateway || managed.UpstreamCode != http.StatusUnauthorized {
		t.Fatalf("401 映射异常: %#v", err)
	}
	if loginCalls.Load() < 2 {
		t.Fatalf("401 后应刷新并重试一次，登录次数=%d", loginCalls.Load())
	}
}

type countingTransport struct{ calls atomic.Int64 }

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("unexpected request")
}

func TestOtherChannelNeverCallsNetwork(t *testing.T) {
	manager, st := testManager(t)
	transport := &countingTransport{}
	manager.client.Transport = transport
	channel, _ := st.CreateUpstreamChannel(store.UpstreamChannelInput{
		Name: "other", Type: store.UpstreamChannelOther, BaseURL: "https://example.test", Username: "u", Password: "p",
	})
	if err := manager.Sync(context.Background(), channel.ID); err == nil {
		t.Fatal("other 渠道同步应被拒绝")
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("other 渠道发起了 %d 次网络请求", transport.calls.Load())
	}
}
