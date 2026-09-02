package upstream

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchAccountUpstreamMultiplierUsesNativeSub2APIProbe(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/accounts/101/upstream-billing-probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("x-api-key") != "admin-key" {
			t.Fatal("原生探测接口请求不正确")
		}
		writeTestEnvelope(t, w, map[string]any{
			"account_id": 101,
			"snapshot": map[string]any{
				"status": "ok",
				"data": map[string]any{
					"billing_scope":             "token",
					"group_rate_multiplier":     0.2,
					"user_rate_multiplier":      0.5,
					"resolved_rate_multiplier":  0.1,
					"effective_rate_multiplier": 0.15,
				},
			},
		})
	})
	mux.HandleFunc("/api/v1/admin/accounts/data", func(http.ResponseWriter, *http.Request) {
		t.Fatal("原生探测成功后不应读取账号凭据")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	value, err := client.FetchAccountUpstreamMultiplier(context.Background(), 101, "openai")
	if err != nil || value != 0.15 {
		t.Fatalf("FetchAccountUpstreamMultiplier() = %v, %v", value, err)
	}
}

func TestExportAccountCredentialsReturnsOnlyConnectionFields(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts/data" || r.URL.Query().Get("ids") != "101" ||
			r.URL.Query().Get("include_proxies") != "false" || r.Header.Get("x-api-key") != "admin-key" {
			t.Fatalf("导出凭据请求异常: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		writeTestEnvelope(t, w, map[string]any{"accounts": []map[string]any{{
			"name": "channel", "type": "api_key",
			"credentials": map[string]any{"api_key": "secret-key", "base_url": server.URL + "/v1", "password": "do-not-return"},
		}}})
	}))
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	credentials, err := client.ExportAccountCredentials(context.Background(), 101)
	if err != nil || credentials.BaseURL != server.URL+"/v1" || credentials.APIKey != "secret-key" {
		t.Fatalf("账号凭据 = %+v, err=%v", credentials, err)
	}
	raw, _ := json.Marshal(credentials)
	if strings.Contains(string(raw), "secret-key") {
		t.Fatalf("导出凭据不应进入序列化结果: %s", raw)
	}
}

func TestExportAccountCredentialsSanitizesErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"code":502,"message":"secret-key"}`))
	}))
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	_, err := client.ExportAccountCredentials(context.Background(), 101)
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("导出错误不应携带凭据: %v", err)
	}
}

func TestListAccountCredentialsUsesBatchEndpoint(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/accounts" ||
			query.Get("type") != "apikey" || query.Get("page_size") != "1000" ||
			query.Get("include_scheduler_score") != "0" || query.Get("timezone") != "Asia/Shanghai" ||
			r.Header.Get("x-api-key") != "admin-key" {
			t.Fatalf("批量账号凭据请求异常: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		writeTestEnvelope(t, w, map[string]any{
			"items": []map[string]any{
				{"id": 101, "type": "apikey", "credentials": map[string]any{
					"api_key": "secret-key", "base_url": server.URL + "/v1",
				}},
				{"id": 102, "type": "apikey", "credentials": map[string]any{
					"api_key": "masked********", "base_url": server.URL + "/v1",
				}},
			},
			"total": 2, "page": 1, "page_size": 1000, "pages": 1,
		})
	}))
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	records, err := client.ListAccountCredentials(context.Background())
	if err != nil || len(records) != 1 || records[0].AccountID != 101 ||
		records[0].Credentials.APIKey != "secret-key" {
		t.Fatalf("批量账号凭据 = %+v, err=%v", records, err)
	}
}

func TestAccountCredentialsFromExportOnlyRequiresFullAPIKey(t *testing.T) {
	credentials, err := accountCredentialsFromExport(exportedAccount{
		Type:        "apikey",
		Credentials: map[string]any{"api_key": "secret-key"},
	})
	if err != nil || credentials.APIKey != "secret-key" {
		t.Fatalf("没有 URL 也应接受完整 API Key: %+v, err=%v", credentials, err)
	}
}

func TestFetchAccountUpstreamMultiplierUsesNativeProbeForNonOpenAIAPIKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/accounts/202/upstream-billing-probe", func(w http.ResponseWriter, _ *http.Request) {
		writeTestEnvelope(t, w, map[string]any{
			"account_id": 202,
			"snapshot": map[string]any{
				"status": "ok",
				"data": map[string]any{
					"billing_scope":             "token",
					"effective_rate_multiplier": 0.35,
				},
			},
		})
	})
	mux.HandleFunc("/api/v1/admin/accounts/data", func(http.ResponseWriter, *http.Request) {
		t.Fatal("新版原生探测支持非 OpenAI API Key，不应导出账号凭据")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	value, err := client.FetchAccountUpstreamMultiplier(context.Background(), 202, "anthropic")
	if err != nil || value != 0.35 {
		t.Fatalf("FetchAccountUpstreamMultiplier() = %v, %v", value, err)
	}
}

func TestFetchAccountUpstreamMultiplierFallsBackToResolvedField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestEnvelope(t, w, map[string]any{
			"snapshot": map[string]any{
				"status": "ok",
				"data": map[string]any{
					"billing_scope":            "token",
					"resolved_rate_multiplier": 0.42,
				},
			},
		})
	}))
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	value, err := client.FetchAccountUpstreamMultiplier(context.Background(), 101, "openai")
	if err != nil || value != 0.42 {
		t.Fatalf("旧版字段兼容结果 = %v, %v", value, err)
	}
}

func TestFetchAccountUpstreamMultiplierNativeFailureDoesNotUseLegacyValue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/accounts/101/upstream-billing-probe", func(w http.ResponseWriter, _ *http.Request) {
		writeTestEnvelope(t, w, map[string]any{
			"account_id": 101,
			"snapshot":   map[string]any{"status": "failed", "last_error": "upstream rejected probe"},
		})
	})
	mux.HandleFunc("/api/v1/admin/accounts/data", func(http.ResponseWriter, *http.Request) {
		t.Fatal("原生探测已受支持但失败时不应降级到不可靠的历史接口")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	_, err := client.FetchAccountUpstreamMultiplier(context.Background(), 101, "openai")
	if err == nil || !strings.Contains(err.Error(), "upstream rejected probe") {
		t.Fatalf("错误 = %v", err)
	}
}

func TestFetchAccountUpstreamMultiplierBatchKeepsPerAccountFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/accounts/upstream-billing-probe/batch" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var request struct {
			AccountIDs []int64 `json:"account_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.AccountIDs) != 2 {
			t.Fatalf("批量请求错误: ids=%v err=%v", request.AccountIDs, err)
		}
		writeTestEnvelope(t, w, map[string]any{
			"results": []map[string]any{
				{
					"account_id": 101,
					"snapshot": map[string]any{"status": "ok", "data": map[string]any{
						"billing_scope": "token", "effective_rate_multiplier": 0.25,
					}},
				},
				{
					"account_id": 102,
					"snapshot":   map[string]any{"status": "failed", "last_error": "http_error"},
				},
			},
		})
	}))
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	results, available, err := client.FetchAccountUpstreamMultiplierBatch(context.Background(), []int64{101, 102})
	if err != nil || !available {
		t.Fatalf("批量探测 = available %v, err %v", available, err)
	}
	if result := results[101]; result.Err != nil || result.Multiplier != 0.25 {
		t.Fatalf("成功项错误: %+v", result)
	}
	if result := results[102]; result.Err == nil || !strings.Contains(result.Err.Error(), "http_error") {
		t.Fatalf("失败项错误: %+v", result)
	}
}

func TestFetchAccountUpstreamMultiplierFallsBackForOlderSub2API(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/admin/accounts/101/upstream-billing-probe", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/v1/admin/accounts/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "admin-key" {
			t.Fatal("管理接口未携带 Admin API Key")
		}
		writeTestEnvelope(t, w, map[string]any{
			"accounts": []map[string]any{{
				"name": "channel", "platform": "openai", "type": "api_key",
				"credentials": map[string]any{"api_key": "secret-key", "base_url": server.URL + "/v1"},
			}},
		})
	})
	mux.HandleFunc("/v1/usage", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-key" || r.Header.Get("x-api-key") != "secret-key" {
			t.Fatal("上游倍率接口未使用账号 API Key")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"rate_multiplier": json.Number("1.375")},
		})
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := New(server.URL, "admin-key", 5*time.Second)
	value, err := client.FetchAccountUpstreamMultiplier(context.Background(), 101, "openai")
	if err != nil || value != 1.375 {
		t.Fatalf("FetchAccountUpstreamMultiplier() = %v, %v", value, err)
	}
}

func TestRequestMultiplierRejectsInvalidOrAmbiguousValues(t *testing.T) {
	tests := []struct {
		name string
		body any
		want string
	}{
		{name: "zero", body: map[string]any{"rate_multiplier": 0}, want: "未返回可识别"},
		{name: "not a number", body: map[string]any{"rate_multiplier": "bad"}, want: "未返回可识别"},
		{name: "ambiguous", body: map[string]any{"rate_multiplier": 1.2, "data": map[string]any{"multiplier": 1.3}}, want: "多个不一致"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tt.body)
			}))
			defer server.Close()
			endpoint, _ := validateMultiplierURL(server.URL)
			_, err := requestMultiplier(context.Background(), endpoint, "secret")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("错误 = %v, 期望包含 %q", err, tt.want)
			}
		})
	}
}

func TestRequestMultiplierHTTPErrorAndTimeout(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()
		endpoint, _ := validateMultiplierURL(server.URL)
		_, err := requestMultiplier(context.Background(), endpoint, "secret")
		if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
			t.Fatalf("错误 = %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		endpoint, _ := validateMultiplierURL(server.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := requestMultiplier(ctx, endpoint, "secret")
		if err == nil {
			t.Fatal("超时请求应失败")
		}
	})
}

func TestMultiplierEndpointRejectsCrossHostExplicitURL(t *testing.T) {
	_, err := multiplierEndpoint(map[string]any{
		"base_url":            "https://api.example.com/v1",
		"api_key":             "secret",
		"rate_multiplier_url": "https://other.example.com/rate",
	})
	if err == nil || !strings.Contains(err.Error(), "同一主机") {
		t.Fatalf("跨主机倍率地址应被拒绝: %v", err)
	}
}

func TestValidateResolvedMultiplierIP(t *testing.T) {
	for _, raw := range []string{"0.0.0.0", "224.0.0.1", "169.254.169.254", "100.100.100.200", "fe80::1"} {
		if err := validateResolvedMultiplierIP(net.ParseIP(raw)); err == nil {
			t.Fatalf("危险地址 %s 应被拒绝", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.0.0.8", "192.168.1.8", "::1"} {
		if err := validateResolvedMultiplierIP(net.ParseIP(raw)); err != nil {
			t.Fatalf("本地或私网地址 %s 应保留支持: %v", raw, err)
		}
	}
}

func writeTestEnvelope(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "", "data": data}); err != nil {
		t.Fatalf("写响应失败: %v", err)
	}
}
