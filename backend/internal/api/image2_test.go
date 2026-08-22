package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
		if _, exists := payload["quality"]; exists {
			t.Errorf("上游收到已屏蔽参数 quality: %#v", payload)
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
		BlockedParams: "quality",
	}); err != nil {
		t.Fatalf("创建 image2 上游失败: %v", err)
	}

	client := upstream.New(upstreamServer.URL, "unused", 10*time.Second)
	server := NewServer(st, client, engine.New(st, client), nil)
	defer server.Close()
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodPost, "/primary/v1/images/generations?trace=1",
		bytes.NewBufferString(`{"model":"gpt-image-2","quality":"high","stream":true,"response_format":"url"}`))
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

func TestImage2MultipartBlocksConfiguredParams(t *testing.T) {
	var input bytes.Buffer
	inputWriter := multipart.NewWriter(&input)
	for name, value := range map[string]string{
		"prompt": "keep", "quality": "drop", "response_format": "url",
	} {
		if err := inputWriter.WriteField(name, value); err != nil {
			t.Fatalf("写入字段 %s 失败: %v", name, err)
		}
	}
	for name, content := range map[string]string{"image": "keep-image", "mask": "drop-mask"} {
		part, err := inputWriter.CreateFormFile(name, name+".png")
		if err != nil {
			t.Fatalf("创建文件字段 %s 失败: %v", name, err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("写入文件字段 %s 失败: %v", name, err)
		}
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatalf("关闭 multipart 请求失败: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &input)
	request.Header.Set("Content-Type", inputWriter.FormDataContentType())
	body, contentType, responseFormat, proxyErr := prepareImage2Multipart(request, nil,
		map[string]bool{"quality": true, "mask": true, "response_format": true})
	if proxyErr != nil {
		t.Fatalf("准备 multipart 请求失败: %#v", proxyErr)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("读取 multipart 请求失败: %v", err)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("解析 multipart Content-Type 失败: %v", err)
	}
	form, err := multipart.NewReader(bytes.NewReader(raw), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("解析转发 multipart 请求失败: %v", err)
	}
	defer form.RemoveAll()
	if responseFormat != "url" || form.Value["prompt"][0] != "keep" || len(form.File["image"]) != 1 {
		t.Fatalf("保留字段错误: response_format=%q values=%v files=%v", responseFormat, form.Value, form.File)
	}
	if len(form.Value["quality"]) != 0 || len(form.Value["response_format"]) != 0 || len(form.File["mask"]) != 0 {
		t.Fatalf("屏蔽字段仍被转发: values=%v files=%v", form.Value, form.File)
	}
}

func TestHardenHTTPPreservesImage2RequestRules(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(maxRequestBodyBytes)+1)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/primary/v1/images/edits", bytes.NewReader(body))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	response := httptest.NewRecorder()
	hardenHTTP(next).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("图片编辑请求返回 %d: %s", response.Code, response.Body.String())
	}
}

func TestImage2Base64ResponseRequiresImageDomain(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("not-an-image"))
	_, proxyErr := (&Server{}).convertImage2Response(context.Background(),
		[]byte(`{"data":[{"b64_json":"`+encoded+`"}]}`), "", "url", true)
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
	if got := normalizeImage2BlockedParams(" quality,mask, quality, ,response_format "); got != "quality,mask,response_format" {
		t.Fatalf("参数屏蔽规范化 = %q", got)
	}
}

func TestImage2ProxyExtensions(t *testing.T) {
	cases := map[string]string{
		"/image.png":  ".png",
		"/IMAGE.JPG":  ".jpg",
		"/image.jpeg": ".jpg",
		"/image.WebP": ".webp",
		"/image.gif":  ".png",
		"/image":      ".png",
	}
	for input, want := range cases {
		if got := image2ProxyExtension(input); got != want {
			t.Errorf("image2ProxyExtension(%q) = %q, 期望 %q", input, got, want)
		}
	}
}

func TestImage2ProxyHandlesRequestedFormats(t *testing.T) {
	t.Setenv(image2URLKeyEnv, "image2-test-encryption-key")
	base64Image := []byte("\x89PNG\r\n\x1a\nurl-to-base64")
	proxyImage := []byte("proxied-webp")
	var proxyHits atomic.Int32
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(base64Image)
		case "/source.webp":
			proxyHits.Add(1)
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Errorf("图片转发泄露调用方凭据: Authorization=%q Cookie=%q",
					r.Header.Get("Authorization"), r.Header.Get("Cookie"))
			}
			w.Header().Set("Content-Type", "image/webp")
			w.Header().Set("Cache-Control", "public, max-age=60")
			_, _ = w.Write(proxyImage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer imageServer.Close()

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
			writeJSON(w, http.StatusOK, map[string]any{"data": []any{
				map[string]any{"url": imageServer.URL + "/source.webp?signature=secret"},
			}})
			return
		}
		if payload["model"] == "already-base64" {
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"dXBzdHJlYW0tYmFzZTY0"}]}`))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"url": imageServer.URL + "/image.png"},
		}})
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
	created, err := st.CreateImage2Upstream(store.Image2Upstream{
		Name: "主上游", Slug: "primary", BaseURL: upstreamServer.URL, APIKey: "upstream-secret",
		ProxyImageURLs: true,
	})
	if err != nil {
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
		request.Host = "guardian.example.com"
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}

	response := call(`{"response_format":"url"}`)
	var proxied struct {
		Data []map[string]any `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &proxied) != nil || len(proxied.Data) != 1 {
		t.Fatalf("URL 代理响应无效: %d %s", response.Code, response.Body.String())
	}
	proxyURL, _ := proxied.Data[0]["url"].(string)
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil || parsedProxyURL.Host != "guardian.example.com" ||
		!strings.HasPrefix(parsedProxyURL.Path, "/images/from/") || !strings.HasSuffix(parsedProxyURL.Path, ".webp") {
		t.Fatalf("图片代理 URL = %q, err=%v", proxyURL, err)
	}
	if proxyHits.Load() != 0 {
		t.Fatal("生成代理 URL 时不应请求图片源")
	}

	proxyRequest := httptest.NewRequest(http.MethodGet, parsedProxyURL.Path, nil)
	proxyRequest.Header.Set("Authorization", "Bearer must-not-forward")
	proxyRequest.AddCookie(&http.Cookie{Name: "session", Value: "must-not-forward"})
	proxyResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK || !bytes.Equal(proxyResponse.Body.Bytes(), proxyImage) ||
		proxyResponse.Header().Get("Content-Type") != "image/webp" ||
		proxyResponse.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("图片流式转发失败: status=%d type=%q cache=%q body=%q", proxyResponse.Code,
			proxyResponse.Header().Get("Content-Type"), proxyResponse.Header().Get("Cache-Control"), proxyResponse.Body.Bytes())
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("图片源请求次数 = %d", proxyHits.Load())
	}

	prefix := "/images/from/"
	name := strings.TrimPrefix(parsedProxyURL.Path, prefix)
	token := strings.TrimSuffix(name, ".webp")
	defaultCipherServer := &Server{image2URLCipher: newImage2URLCipher("")}
	if _, err := defaultCipherServer.decryptImage2URL(token); err == nil {
		t.Fatal("环境变量密钥未覆盖代码默认值")
	}
	replacement := "A"
	if token[0] == 'A' {
		replacement = "B"
	}
	tamperedRequest := httptest.NewRequest(http.MethodGet,
		prefix+replacement+token[1:]+".webp", nil)
	tamperedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(tamperedResponse, tamperedRequest)
	if tamperedResponse.Code != http.StatusNotFound || proxyHits.Load() != 1 {
		t.Fatalf("篡改 token 返回 %d，图片源请求次数=%d", tamperedResponse.Code, proxyHits.Load())
	}

	created.ProxyImageURLs = false
	if _, err := st.UpdateImage2Upstream(created); err != nil {
		t.Fatalf("关闭 URL 转换失败: %v", err)
	}
	response = call(`{"response_format":"url"}`)
	var passthrough struct {
		Data []map[string]any `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &passthrough) != nil ||
		len(passthrough.Data) != 1 {
		t.Fatalf("URL 透传响应无效: %d %s", response.Code, response.Body.String())
	}
	if got, _ := passthrough.Data[0]["url"].(string); got != imageServer.URL+"/source.webp?signature=secret" {
		t.Fatalf("关闭转换后的图片 URL = %q", got)
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("URL 透传不应访问图片源，请求次数=%d", proxyHits.Load())
	}

	response = call(`{"model":"already-base64","response_format":"b64_json"}`)
	if response.Code != http.StatusOK || response.Body.String() != `{"data":[{"b64_json":"dXBzdHJlYW0tYmFzZTY0"}]}` {
		t.Fatalf("b64_json 未透传: %d %s", response.Code, response.Body.String())
	}

	response = call(`{"response_format":"b64_json"}`)
	var converted struct {
		Data []map[string]any `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &converted) != nil || len(converted.Data) != 1 {
		t.Fatalf("URL 转 Base64 响应无效: %d %s", response.Code, response.Body.String())
	}
	encoded, _ := converted.Data[0]["b64_json"].(string)
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || !bytes.Equal(decoded, base64Image) {
		t.Fatalf("Base64 图片无效: err=%v 内容一致=%v", err, bytes.Equal(decoded, base64Image))
	}
	if _, exists := converted.Data[0]["url"]; exists {
		t.Fatal("转换后仍包含 url")
	}
	entries, err := os.ReadDir(server.image2Dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("URL 转 Base64 不应落盘: err=%v 文件数=%d", err, len(entries))
	}
}
