package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/auth"
	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

func TestRemoteMultiplierSourceAuthorizesAndAppliesSharedLink(t *testing.T) {
	g1Store, err := store.Open(filepath.Join(t.TempDir(), "g1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer g1Store.Close()
	passwordHash, err := auth.HashPassword("hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g1Store.CreateUser("admin", passwordHash); err != nil {
		t.Fatal(err)
	}
	channel, err := g1Store.CreateUpstreamChannel(store.UpstreamChannelInput{
		Name: "supplier", Type: store.UpstreamChannelSub2API, BaseURL: "https://upstream.example.com",
		Username: "user", Password: "password", RechargeRatio: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := g1Store.MarkUpstreamChannelSynced(channel.ID); err != nil {
		t.Fatal(err)
	}
	groups := []any{map[string]any{"id": 1, "name": "pro", "user_rate_multiplier": 0.15}}
	tokens := []any{map[string]any{"key": "linked-key", "group_id": 1}}
	if err := g1Store.SaveUpstreamCache(channel.ID, "groups", groups, groups); err != nil {
		t.Fatal(err)
	}
	if err := g1Store.SaveUpstreamCache(channel.ID, "tokens", tokens, tokens); err != nil {
		t.Fatal(err)
	}
	g1Engine := engine.New(g1Store, upstream.New("", "", time.Second))
	g1API := NewServer(g1Store, upstream.New("", "", time.Second), g1Engine, nil)
	g1HTTP := httptest.NewServer(g1API.Handler())
	defer g1HTTP.Close()
	defer g1API.Close()

	authorizeBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "hunter2hunter2"})
	authorizeReq, _ := http.NewRequest(http.MethodPost, g1HTTP.URL+"/internal/v1/multiplier-source/authorize", bytes.NewReader(authorizeBody))
	authorizeReq.Header.Set("Content-Type", "application/json")
	authorizeResp, err := http.DefaultClient.Do(authorizeReq)
	if err != nil {
		t.Fatal(err)
	}
	defer authorizeResp.Body.Close()
	if authorizeResp.StatusCode != http.StatusOK {
		t.Fatalf("G1 授权返回 %d", authorizeResp.StatusCode)
	}
	var authorization struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(authorizeResp.Body).Decode(&authorization); err != nil || authorization.AccessToken == "" {
		t.Fatalf("G1 授权响应异常: %+v err=%v", authorization, err)
	}
	statusReq, _ := http.NewRequest(http.MethodGet, g1HTTP.URL+"/internal/v1/multiplier-source/status", nil)
	statusReq.Header.Set("Authorization", "Bearer "+authorization.AccessToken)
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	if statusResp.StatusCode != http.StatusOK || statusResp.Header.Get("Cache-Control") != "no-store" {
		statusResp.Body.Close()
		t.Fatalf("G1 只读状态接口异常: status=%d cache=%q", statusResp.StatusCode, statusResp.Header.Get("Cache-Control"))
	}
	statusResp.Body.Close()
	adminReq, _ := http.NewRequest(http.MethodGet, g1HTTP.URL+"/api/me", nil)
	adminReq.Header.Set("Authorization", "Bearer "+authorization.AccessToken)
	adminResp, err := http.DefaultClient.Do(adminReq)
	if err != nil {
		t.Fatal(err)
	}
	adminResp.Body.Close()
	if adminResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("只读令牌不应访问管理 API，实际状态 %d", adminResp.StatusCode)
	}

	var updateMu sync.Mutex
	var updatedName string
	g2Upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "g2-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/accounts":
			writeJSONForTest(w, map[string]any{"code": 0, "data": map[string]any{
				"items": []any{map[string]any{
					"id": 101, "name": "G2 channel", "type": "apikey",
					"credentials": map[string]any{"api_key": "linked-key", "base_url": "https://upstream.example.com/"},
				}}, "pages": 1,
			}})
		case "/api/v1/admin/accounts/101":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			updateMu.Lock()
			updatedName, _ = payload["name"].(string)
			updateMu.Unlock()
			writeJSONForTest(w, map[string]any{"code": 0, "data": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer g2Upstream.Close()

	g2Store, err := store.Open(filepath.Join(t.TempDir(), "g2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer g2Store.Close()
	if err := g2Store.SaveConnection(domain.Connection{BaseURL: g2Upstream.URL, AdminAPIKey: "g2-key", TimeoutSeconds: 10, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := g2Store.ReplaceAccounts([]domain.Account{{ID: 101, Name: "G2 channel", Type: "apikey", Status: "active", Schedulable: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g2Store.SaveMultiplierSourceSettings(store.MultiplierSourceSettings{
		Mode: store.MultiplierSourceRemote, BaseURL: g1HTTP.URL, Username: "admin",
		AccessToken: authorization.AccessToken, TimeoutSeconds: 10,
	}); err != nil {
		t.Fatal(err)
	}
	g2Client := upstream.New(g2Upstream.URL, "g2-key", 10*time.Second)
	g2Engine := engine.New(g2Store, g2Client)
	result, err := g2Engine.SyncConfiguredMultiplierSource(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 1 || result.Total != 1 || !result.Complete {
		t.Fatalf("G2 同步结果异常: %+v", result)
	}
	policy, err := g2Store.Policy()
	if err != nil || policy.AccountLinkedMultipliers["101"] != 0.015 {
		t.Fatalf("G2 联动倍率异常: %+v err=%v", policy.AccountLinkedMultipliers, err)
	}
	updateMu.Lock()
	firstUpdatedName := updatedName
	updateMu.Unlock()
	if firstUpdatedName != "G2 channel【x0.015】" {
		t.Fatalf("G2 名称后缀异常: %q", firstUpdatedName)
	}
	if err := g1Store.SaveUpstreamCache(channel.ID, "tokens", []any{}, []any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := g2Engine.SyncConfiguredMultiplierSource(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	policy, err = g2Store.Policy()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := policy.AccountLinkedMultipliers["101"]; exists {
		t.Fatalf("远程完整快照删除令牌后仍保留联动倍率: %#v", policy.AccountLinkedMultipliers)
	}
	account, err := g2Store.Account(101)
	if err != nil || account.Name != "G2 channel" {
		t.Fatalf("远程完整快照未清理名称后缀: %+v err=%v", account, err)
	}
}

func writeJSONForTest(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
