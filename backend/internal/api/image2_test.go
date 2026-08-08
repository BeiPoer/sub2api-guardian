package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

func TestImage2ProxyConvertsBase64ToURL(t *testing.T) {
	image := []byte("\x89PNG\r\n\x1a\nimage2-test")
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" || r.URL.RawQuery != "trace=1" {
			t.Errorf("上游请求地址 = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Errorf("上游 Authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("解析上游请求失败: %v", err)
		}
		if payload["model"] != "provider-image-model" || payload["stream"] != false {
			t.Errorf("请求改写结果 = %#v", payload)
		}
		if payload["response_format"] != "url" {
			t.Errorf("上游未收到 response_format=url: %#v", payload)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"b64_json": base64.StdEncoding.EncodeToString(image)},
		}})
	}))
	defer upstreamServer.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()
	if err := st.SaveImage2Settings(store.Image2Settings{
		ImageDomain: "images.example.com", RetentionHours: 24, ProxyAPIKey: "proxy-secret",
	}); err != nil {
		t.Fatalf("保存 image2 设置失败: %v", err)
	}
	if _, err := st.CreateImage2Upstream(store.Image2Upstream{
		Name: "主上游", Slug: "primary", BaseURL: upstreamServer.URL,
		APIKey: "upstream-secret", ModelMapping: "gpt-image-2=provider-image-model",
	}); err != nil {
		t.Fatalf("创建 image2 上游失败: %v", err)
	}

	client := upstream.New(upstreamServer.URL, "unused", 10*time.Second)
	server := NewServer(st, client, engine.New(st, client), nil)
	defer server.Close()
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodPost, "/primary/v1/images/generations?trace=1",
		bytes.NewBufferString(`{"model":"gpt-image-2","stream":true,"response_format":"url"}`))
	request.Host = "203.0.113.8:8787"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Authorization", "Bearer proxy-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("代理返回 %d: %s", response.Code, response.Body.String())
	}

	var converted struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &converted); err != nil || len(converted.Data) != 1 {
		t.Fatalf("转换响应无效: %v %s", err, response.Body.String())
	}
	imageURL, _ := converted.Data[0]["url"].(string)
	parsed, err := url.Parse(imageURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "images.example.com" ||
		!strings.HasPrefix(parsed.Path, "/images/") {
		t.Fatalf("图片 URL = %q, err=%v", imageURL, err)
	}
	if _, exists := converted.Data[0]["b64_json"]; exists {
		t.Fatal("转换后仍包含 b64_json")
	}

	fileRequest := httptest.NewRequest(http.MethodGet, parsed.Path, nil)
	fileResponse := httptest.NewRecorder()
	handler.ServeHTTP(fileResponse, fileRequest)
	if fileResponse.Code != http.StatusOK || !bytes.Equal(fileResponse.Body.Bytes(), image) {
		t.Fatalf("公开图片返回 %d，内容一致=%v", fileResponse.Code, bytes.Equal(fileResponse.Body.Bytes(), image))
	}
}

func TestImage2ResponseOnlyNeedsImageDomainForBase64(t *testing.T) {
	native := []byte(`{"data":[{"url":"https://native.example/image.png"}]}`)
	converted, proxyErr := (&Server{}).convertImage2Response(native, "")
	if proxyErr != nil || !bytes.Equal(converted, native) {
		t.Fatalf("原生 URL 响应被额外处理: err=%v body=%s", proxyErr, converted)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte("not-an-image"))
	_, proxyErr = (&Server{}).convertImage2Response(
		[]byte(`{"data":[{"b64_json":"`+encoded+`"}]}`), "")
	if proxyErr == nil || proxyErr.status != http.StatusServiceUnavailable || proxyErr.code != "image_domain_missing" {
		t.Fatalf("Base64 响应未要求图片域名: %#v", proxyErr)
	}
}

func TestImage2PublicBaseURLFallsBackToRequestHost(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "guardian.example.com:8787"
	if got := image2PublicBaseURL(request, ""); got != "http://guardian.example.com:8787/images" {
		t.Fatalf("默认图片地址 = %q", got)
	}
	if got := image2PublicBaseURL(request, "images.example.com"); got != "http://images.example.com/images" {
		t.Fatalf("自定义图片地址 = %q", got)
	}
}

func TestNormalizeImage2URLsUsesRootUpstreamAndDomainOnly(t *testing.T) {
	if got, err := normalizeImage2UpstreamURL("https://api.example.com///"); err != nil || got != "https://api.example.com" {
		t.Fatalf("上游根地址规范化 = %q, err=%v", got, err)
	}
	if _, err := normalizeImage2UpstreamURL("https://api.example.com/v1"); err == nil {
		t.Fatal("上游 Base URL 不应接受 /v1")
	}
	if got, err := normalizeImage2Domain("images.example.com/"); err != nil || got != "images.example.com" {
		t.Fatalf("图片域名规范化 = %q, err=%v", got, err)
	}
	if _, err := normalizeImage2Domain("https://images.example.com"); err == nil {
		t.Fatal("图片域名不应包含协议")
	}
}

func TestImage2ProxyPassesThroughRequestedFormats(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("解析上游请求失败: %v", err)
		}
		responseFormat, _ := payload["response_format"].(string)
		if responseFormat != "url" && responseFormat != "b64_json" {
			t.Errorf("response_format 未原样转发: %#v", payload)
		}
		if responseFormat == "url" {
			_, _ = w.Write([]byte(`{"data":[{"url":"https://native.example/image.png"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"upstream-base64"}]}`))
	}))
	defer upstreamServer.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()
	if err := st.SaveImage2Settings(store.Image2Settings{ProxyAPIKey: "proxy-secret"}); err != nil {
		t.Fatalf("保存 image2 设置失败: %v", err)
	}
	if _, err := st.CreateImage2Upstream(store.Image2Upstream{
		Name: "主上游", Slug: "primary", BaseURL: upstreamServer.URL, APIKey: "upstream-secret",
	}); err != nil {
		t.Fatalf("创建 image2 上游失败: %v", err)
	}
	client := upstream.New(upstreamServer.URL, "unused", 10*time.Second)
	server := NewServer(st, client, engine.New(st, client), nil)
	defer server.Close()

	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/primary/v1/images/generations",
			bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer proxy-secret")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}

	response := call(`{"response_format":"url"}`)
	if response.Code != http.StatusOK || response.Body.String() != `{"data":[{"url":"https://native.example/image.png"}]}` {
		t.Fatalf("原生 URL 未透传: %d %s", response.Code, response.Body.String())
	}

	response = call(`{"response_format":"b64_json"}`)
	if response.Code != http.StatusOK || response.Body.String() != `{"data":[{"b64_json":"upstream-base64"}]}` {
		t.Fatalf("b64_json 未透传: %d %s", response.Code, response.Body.String())
	}
}
