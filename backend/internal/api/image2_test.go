package api

import (
	"bufio"
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

func TestImage2ConfigReturnsUpstreamAPIKey(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateImage2Upstream(store.Image2Upstream{
		Name: "test", Slug: "test", BaseURL: "https://example.com", APIKey: "upstream-secret",
	}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	(&Server{store: st}).getImage2(response, httptest.NewRequest(http.MethodGet, "/api/image2", nil))
	var config struct {
		Upstreams []store.Image2Upstream `json:"upstreams"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(config.Upstreams) != 1 || config.Upstreams[0].APIKey != "upstream-secret" {
		t.Fatal("image2 配置未返回完整上游 API Key")
	}
}

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
	request.Pattern = "/{slug}/v1/{path...}"
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	response := httptest.NewRecorder()
	hardenHTTP(next).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("图片编辑请求返回 %d: %s", response.Code, response.Body.String())
	}
}

func TestHardenHTTPAllowsCrossOriginImage2Proxy(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodPost, "/primary/v1/images/generations", nil)
	request.Pattern = "/{slug}/v1/{path...}"
	request.Header.Set("Origin", "https://static.example.com")
	response := httptest.NewRecorder()
	hardenHTTP(cors(next)).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "https://static.example.com" {
		t.Fatalf("跨域 image2 请求被拒绝: status=%d allow-origin=%q", response.Code, response.Header().Get("Access-Control-Allow-Origin"))
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/primary/v1/images/generations", nil)
	preflight.Pattern = "/{slug}/v1/{path...}"
	preflight.Header.Set("Origin", "https://static.example.com")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflightResponse := httptest.NewRecorder()
	hardenHTTP(cors(next)).ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusNoContent || preflightResponse.Header().Get("Access-Control-Allow-Origin") != "https://static.example.com" {
		t.Fatalf("跨域 image2 预检失败: status=%d allow-origin=%q", preflightResponse.Code, preflightResponse.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestImage2PassThrough(t *testing.T) {
	fixture := setupAuthAPI(t)
	if err := fixture.store.SaveImage2Settings(store.Image2Settings{ProxyAPIKey: "proxy-secret", RetentionHours: 24}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, contentType, body string
		status                          int
	}{
		{http.MethodGet, "models?tag=a%2Bb&tag=c", "", "", http.StatusOK},
		{http.MethodPost, "chat/completions", "application/json", `{"model":"alias","stream":true,"quality":"high"}`, http.StatusOK},
		{http.MethodPost, "responses", "application/json", `{"model":"alias","input":"hello"}`, http.StatusTooManyRequests},
		{http.MethodPost, "files", "multipart/form-data; boundary=test", "--test\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\ndata\r\n--test--\r\n", http.StatusCreated},
		{http.MethodPatch, "files/item", "application/octet-stream", strings.Repeat("x", int(maxRequestBodyBytes)+1), http.StatusOK},
		{http.MethodDelete, "files/a%2Fb?raw=a+b&raw=%2F", "", "", http.StatusAccepted},
		{http.MethodGet, "images/generations", "", "", http.StatusTemporaryRedirect},
		{http.MethodHead, "models", "", "", http.StatusOK},
		{http.MethodOptions, "models", "", "", http.StatusOK},
	} {
		t.Run(tc.method+"/"+tc.path, func(t *testing.T) {
			const responseBody = `{"data":[{"b64_json":"unchanged"}],"model":"alias"}`
			var calls atomic.Int32
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != tc.method || r.RequestURI != "/gateway%20prefix/v1/"+tc.path {
					t.Errorf("请求方法/地址被改写: %s %s", r.Method, r.RequestURI)
				}
				if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("Cookie") != "" ||
					r.Header.Get("X-Custom") != "keep" || r.Header.Get("Content-Type") != tc.contentType {
					t.Error("转发请求头不符合预期")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.body {
					t.Errorf("请求体被修改或截断: bytes=%d err=%v", len(body), err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("X-Upstream-Custom", "preserved")
				w.Header().Set("Retry-After", "10")
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(tc.status)
				if r.Method != http.MethodHead {
					_, _ = io.WriteString(w, responseBody)
				}
			}))
			defer upstreamServer.Close()
			up, err := fixture.store.CreateImage2Upstream(store.Image2Upstream{
				Name: "透传", Slug: "透传", BaseURL: upstreamServer.URL + "/gateway%20prefix", APIKey: "upstream-secret",
				ModelMapping: "alias=changed", BlockedParams: "quality", ProxyImageURLs: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.store.DeleteImage2Upstream(up.ID)
			request := httptest.NewRequest(tc.method, "/%E9%80%8F%E4%BC%A0/v1/"+tc.path, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer proxy-secret")
			request.Header.Set("Cookie", "guardian_session=private")
			request.Header.Set("Content-Type", tc.contentType)
			request.Header.Set("X-Custom", "keep")
			request.Header.Set("Origin", "https://client.example.com")
			response := httptest.NewRecorder()
			fixture.handler.ServeHTTP(response, request)
			wantBody := responseBody
			if tc.method == http.MethodHead {
				wantBody = ""
			}
			if response.Code != tc.status || response.Body.String() != wantBody || calls.Load() != 1 {
				t.Fatalf("透传失败: status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
			}
			if response.Header().Get("X-Upstream-Custom") != "preserved" || response.Header().Get("Retry-After") != "10" ||
				response.Header().Get("Location") != "/redirect-target" || response.Header().Get("Access-Control-Allow-Origin") != "https://client.example.com" ||
				len(response.Header().Values("Access-Control-Allow-Origin")) != 1 || response.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatalf("响应头未保留: %v", response.Header())
			}
		})
	}

	for _, tc := range []struct {
		path, key string
		status    int
	}{
		{"/missing/v1/models", "wrong", http.StatusUnauthorized},
		{"/missing/v1/models", "proxy-secret", http.StatusNotFound},
		{"/api/image2", "proxy-secret", http.StatusUnauthorized},
	} {
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		request.Header.Set("Authorization", "Bearer "+tc.key)
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Errorf("%s: status=%d want=%d", tc.path, response.Code, tc.status)
		}
	}
	preflight := httptest.NewRequest(http.MethodOptions, "/primary/v1/files", nil)
	preflight.Header.Set("Origin", "https://client.example.com")
	preflight.Header.Set("Access-Control-Request-Method", "PATCH")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-custom")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, preflight)
	if response.Code != http.StatusNoContent || !strings.Contains(response.Header().Get("Access-Control-Allow-Methods"), "PATCH") ||
		response.Header().Get("Access-Control-Allow-Headers") != "authorization,content-type,x-custom" {
		t.Fatalf("透传接口预检失败: status=%d headers=%v", response.Code, response.Header())
	}
	internal := httptest.NewRequest(http.MethodPost, "/internal/v1/multiplier-source/resolve", strings.NewReader("{}"))
	internal.Header.Set("Origin", "https://client.example.com")
	internal.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, internal)
	if response.Code != http.StatusForbidden {
		t.Fatalf("Guardian 内部接口错误绕过跨域保护: %d", response.Code)
	}
}

func TestImage2PassThroughStreamsBeforeUpstreamFinishes(t *testing.T) {
	fixture := setupAuthAPI(t)
	release := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstreamServer.Close()
	defer close(release)
	if err := fixture.store.SaveImage2Settings(store.Image2Settings{ProxyAPIKey: "proxy-secret", RetentionHours: 24}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateImage2Upstream(store.Image2Upstream{
		Name: "stream", Slug: "stream", BaseURL: upstreamServer.URL, APIKey: "upstream-secret",
	}); err != nil {
		t.Fatal(err)
	}
	proxyServer := httptest.NewServer(fixture.handler)
	defer proxyServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyServer.URL+"/stream/v1/responses", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer proxy-secret")
	request.Header.Set("Content-Type", "application/json")
	response, err := proxyServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("上游结束前未收到流式数据: line=%q err=%v", line, err)
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

func TestImage2ProxyConvertsDataURLToFileURL(t *testing.T) {
	image := []byte("\x89PNG\r\n\x1a\nimage2-data-url")
	encoded := base64.StdEncoding.EncodeToString(image)
	dir := t.TempDir()
	server := &Server{image2Dir: dir}
	converted, proxyErr := server.convertImage2Response(context.Background(),
		[]byte(`{"data":[{"url":"data:image/png;base64,`+encoded+`"}]}`),
		"https://images.example.com/images", "url", true)
	if proxyErr != nil {
		t.Fatalf("Data URL 转换失败: %#v", proxyErr)
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(converted, &payload); err != nil || len(payload.Data) != 1 {
		t.Fatalf("转换响应无效: %v %s", err, converted)
	}
	imageURL, _ := payload.Data[0]["url"].(string)
	if !strings.HasPrefix(imageURL, "https://images.example.com/images/") || !strings.HasSuffix(imageURL, ".png") {
		t.Fatalf("转换后的图片 URL = %q", imageURL)
	}
	name := filepath.Base(imageURL)
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || !bytes.Equal(content, image) {
		t.Fatalf("落盘图片错误: %v 内容一致=%v", err, bytes.Equal(content, image))
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
