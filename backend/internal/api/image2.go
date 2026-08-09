package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sub2api-guardian/backend/internal/store"
)

const (
	image2MaxRequestBytes   = 100 << 20
	image2MaxResponseBytes  = 128 << 20
	image2CleanupInterval   = 5 * time.Minute
	image2URLKeyEnv         = "IMAGE2_URL_ENCRYPTION_KEY"
	image2DefaultURLKey     = "sub2api-guardian-default-image2-url-key"
	image2URLAssociatedData = "sub2api-guardian:image2-url:v1"
)

type image2UpstreamInput struct {
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key"`
	ModelMapping string `json:"model_mapping"`
}

func (s *Server) getImage2(w http.ResponseWriter, _ *http.Request) {
	settings, err := s.store.Image2Settings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	upstreams, err := s.store.Image2Upstreams()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":  image2SettingsView(settings),
		"upstreams": upstreams,
	})
}

func (s *Server) saveImage2Settings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Image2Settings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	var payload struct {
		ImageDomain    *string `json:"image_domain"`
		RetentionHours *int    `json:"retention_hours"`
		ProxyAPIKey    *string `json:"proxy_api_key"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求内容不是有效的 JSON"})
		return
	}
	if payload.ImageDomain != nil {
		value := strings.TrimSpace(*payload.ImageDomain)
		if value != "" {
			value, err = normalizeImage2Domain(value)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
		}
		settings.ImageDomain = value
	}
	if payload.RetentionHours != nil {
		if *payload.RetentionHours <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "保留时长必须是正整数小时"})
			return
		}
		settings.RetentionHours = *payload.RetentionHours
	}
	if payload.ProxyAPIKey != nil && strings.TrimSpace(*payload.ProxyAPIKey) != "" {
		settings.ProxyAPIKey = strings.TrimSpace(*payload.ProxyAPIKey)
	}
	if err := s.store.SaveImage2Settings(settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, image2SettingsView(settings))
}

func (s *Server) createImage2Upstream(w http.ResponseWriter, r *http.Request) {
	var payload image2UpstreamInput
	if err := decodeBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求内容不是有效的 JSON"})
		return
	}
	upstream, err := normalizeImage2Upstream(payload, 0, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	created, err := s.store.CreateImage2Upstream(upstream)
	if err != nil {
		writeImage2StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateImage2Upstream(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "上游 ID 无效"})
		return
	}
	current, err := s.store.Image2UpstreamByID(id)
	if err != nil {
		writeImage2StoreError(w, err)
		return
	}
	var payload image2UpstreamInput
	if err := decodeBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求内容不是有效的 JSON"})
		return
	}
	upstream, err := normalizeImage2Upstream(payload, id, current.APIKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	updated, err := s.store.UpdateImage2Upstream(upstream)
	if err != nil {
		writeImage2StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteImage2Upstream(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "上游 ID 无效"})
		return
	}
	if err := s.store.DeleteImage2Upstream(id); err != nil {
		writeImage2StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func image2SettingsView(settings store.Image2Settings) map[string]any {
	return map[string]any{
		"image_domain":      settings.ImageDomain,
		"retention_hours":   settings.RetentionHours,
		"has_proxy_api_key": settings.ProxyAPIKey != "",
	}
}

func normalizeImage2Upstream(input image2UpstreamInput, id int64, currentKey string) (store.Image2Upstream, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return store.Image2Upstream{}, errors.New("显示名称不能为空")
	}
	if utf8.RuneCountInString(name) > 100 {
		return store.Image2Upstream{}, errors.New("显示名称不能超过 100 个字符")
	}
	slug, err := normalizeImage2Slug(input.Slug)
	if err != nil {
		return store.Image2Upstream{}, err
	}
	baseURL, err := normalizeImage2UpstreamURL(input.BaseURL)
	if err != nil {
		return store.Image2Upstream{}, err
	}
	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == "" {
		apiKey = currentKey
	}
	if apiKey == "" {
		return store.Image2Upstream{}, errors.New("上游 API Key 不能为空")
	}
	mapping, err := normalizeImage2ModelMapping(input.ModelMapping)
	if err != nil {
		return store.Image2Upstream{}, err
	}
	return store.Image2Upstream{
		ID: id, Name: name, Slug: slug, BaseURL: baseURL, APIKey: apiKey,
		ModelMapping: mapping,
	}, nil
}

func normalizeImage2Slug(value string) (string, error) {
	value = strings.TrimSpace(value)
	if count := utf8.RuneCountInString(value); count < 1 || count > 64 {
		return "", errors.New("URL 标识长度必须为 1 到 64 个字符")
	}
	for _, r := range value {
		if r != '_' && r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", errors.New("URL 标识只能包含中文、字母、数字、下划线和短横线")
		}
	}
	return value, nil
}

func normalizeImage2UpstreamURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("必须填写不含认证信息、查询参数或片段的 HTTP(S) 绝对 URL")
	}
	if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/v1") {
		return "", errors.New("上游 Base URL 不需要填写 /v1")
	}
	return value, nil
}

func normalizeImage2Domain(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse("http://" + value)
	if err != nil || parsed.Host != value || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("图片域名只能填写域名或 IP，可带端口，不要包含协议或路径")
	}
	return value, nil
}

func normalizeImage2ModelMapping(value string) (string, error) {
	seen := make(map[string]bool)
	lines := make([]string, 0)
	for number, raw := range strings.Split(value, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Count(line, "=") != 1 {
			return "", fmt.Errorf("模型映射第 %d 行必须使用 外部模型=上游模型 格式", number+1)
		}
		source, target, _ := strings.Cut(line, "=")
		source, target = strings.TrimSpace(source), strings.TrimSpace(target)
		if source == "" || target == "" || strings.ContainsAny(source+target, " \t\r\n") {
			return "", fmt.Errorf("模型映射第 %d 行包含空白或空模型名", number+1)
		}
		if seen[source] {
			return "", fmt.Errorf("模型映射重复定义外部模型：%s", source)
		}
		seen[source] = true
		lines = append(lines, source+"="+target)
	}
	return strings.Join(lines, "\n"), nil
}

func parseImage2ModelMapping(value string) map[string]string {
	mapping := make(map[string]string)
	for _, line := range strings.Split(value, "\n") {
		if source, target, ok := strings.Cut(line, "="); ok {
			mapping[source] = target
		}
	}
	return mapping
}

func writeImage2StoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrImage2SlugExists):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "URL 标识已存在"})
	case errors.Is(err, store.ErrImage2UpstreamNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "上游不存在"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

type image2ProxyError struct {
	status    int
	message   string
	errorType string
	code      string
	header    http.Header
}

func (s *Server) generateImage2(w http.ResponseWriter, r *http.Request) {
	s.proxyImage2(w, r, "generations")
}

func (s *Server) editImage2(w http.ResponseWriter, r *http.Request) {
	s.proxyImage2(w, r, "edits")
}

func (s *Server) proxyImage2(w http.ResponseWriter, r *http.Request, operation string) {
	settings, proxyErr := s.requireImage2Key(r)
	if proxyErr != nil {
		writeImage2ProxyError(w, proxyErr)
		return
	}
	upstream, err := s.store.Image2UpstreamBySlug(r.PathValue("slug"))
	if errors.Is(err, store.ErrImage2UpstreamNotFound) {
		writeImage2ProxyError(w, &image2ProxyError{
			status: http.StatusNotFound, message: fmt.Sprintf("Upstream %q was not found.", r.PathValue("slug")),
			errorType: "invalid_request_error", code: "upstream_not_found",
		})
		return
	}
	if err != nil {
		writeImage2ProxyError(w, image2InternalError(err))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, image2MaxRequestBytes)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeImage2ProxyError(w, image2UnsupportedContentType(operation))
		return
	}
	mapping := parseImage2ModelMapping(upstream.ModelMapping)
	var (
		body           io.ReadCloser
		contentType    string
		responseFormat string
	)
	switch {
	case mediaType == "application/json":
		prepared, format, prepareErr := prepareImage2JSON(r, mapping)
		if prepareErr != nil {
			writeImage2ProxyError(w, prepareErr)
			return
		}
		body = io.NopCloser(bytes.NewReader(prepared))
		contentType = "application/json"
		responseFormat = format
	case operation == "edits" && mediaType == "multipart/form-data":
		prepared, preparedType, format, prepareErr := prepareImage2Multipart(r, mapping)
		if prepareErr != nil {
			writeImage2ProxyError(w, prepareErr)
			return
		}
		body, contentType, responseFormat = prepared, preparedType, format
	default:
		writeImage2ProxyError(w, image2UnsupportedContentType(operation))
		return
	}
	target := strings.TrimRight(upstream.BaseURL, "/") + "/v1/images/" + operation
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, body)
	if err != nil {
		_ = body.Close()
		writeImage2ProxyError(w, image2InternalError(err))
		return
	}
	copyImage2RequestHeaders(request.Header, r.Header)
	request.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	request.Header.Set("Content-Type", contentType)

	response, err := s.image2Client.Do(request)
	if err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			writeImage2ProxyError(w, &image2ProxyError{
				status: http.StatusGatewayTimeout, message: "Upstream request timed out.",
				errorType: "upstream_error", code: "timeout",
			})
			return
		}
		writeImage2ProxyError(w, &image2ProxyError{
			status: http.StatusBadGateway, message: "Could not connect to upstream.",
			errorType: "upstream_error", code: "connection_error",
		})
		return
	}
	defer response.Body.Close()
	copyImage2ResponseHeaders(w.Header(), response.Header)
	if response.StatusCode < 200 || response.StatusCode >= 300 ||
		(responseFormat != "url" && responseFormat != "b64_json") {
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		return
	}

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, image2MaxResponseBytes+1))
	if readErr != nil || len(raw) > image2MaxResponseBytes {
		writeImage2ProxyError(w, &image2ProxyError{
			status: http.StatusBadGateway, message: "Upstream returned an invalid Images API response.",
			errorType: "upstream_error", code: "upstream_response_too_large",
		})
		return
	}
	imageBaseURL := image2PublicBaseURL(r, settings.ImageDomain)
	converted, convertErr := s.convertImage2Response(r.Context(), raw, imageBaseURL, responseFormat)
	if convertErr != nil {
		writeImage2ProxyError(w, convertErr)
		return
	}
	if !bytes.Equal(converted, raw) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(converted)
}

func image2PublicBaseURL(r *http.Request, imageDomain string) string {
	host := r.Host
	if imageDomain != "" {
		host = imageDomain
	}
	scheme := "http"
	if isTLS(r) {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/images"}).String()
}

func (s *Server) requireImage2Key(r *http.Request) (store.Image2Settings, *image2ProxyError) {
	settings, err := s.store.Image2Settings()
	if err != nil {
		return store.Image2Settings{}, image2InternalError(err)
	}
	if settings.ProxyAPIKey == "" {
		return store.Image2Settings{}, &image2ProxyError{
			status: http.StatusServiceUnavailable, message: "proxy_api_key is not configured.",
			errorType: "configuration_error", code: "proxy_api_key_missing",
		}
	}
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") ||
		subtle.ConstantTimeCompare([]byte(token), []byte(settings.ProxyAPIKey)) != 1 {
		return store.Image2Settings{}, &image2ProxyError{
			status: http.StatusUnauthorized, message: "Incorrect API key provided.",
			errorType: "invalid_request_error", code: "invalid_api_key",
			header: http.Header{"WWW-Authenticate": []string{"Bearer"}},
		}
	}
	return settings, nil
}

func prepareImage2JSON(r *http.Request, mapping map[string]string) ([]byte, string, *image2ProxyError) {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, "", image2InvalidJSON(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, "", image2InvalidJSON(err)
	}
	if model, ok := payload["model"].(string); ok && mapping[model] != "" {
		payload["model"] = mapping[model]
	}
	responseFormat, _ := payload["response_format"].(string)
	if image2True(payload["stream"]) {
		payload["stream"] = false
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", image2InternalError(err)
	}
	return raw, responseFormat, nil
}

func prepareImage2Multipart(r *http.Request, mapping map[string]string) (io.ReadCloser, string, string, *image2ProxyError) {
	if err := r.ParseMultipartForm(32 << 20); err != nil || r.MultipartForm == nil {
		code := "invalid_form"
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status, code = http.StatusRequestEntityTooLarge, "request_too_large"
		}
		return nil, "", "", &image2ProxyError{
			status: status, message: "Request body must be valid multipart form data.",
			errorType: "invalid_request_error", code: code,
		}
	}
	form := r.MultipartForm
	responseFormat := ""
	if values := form.Value["response_format"]; len(values) > 0 {
		responseFormat = values[0]
	}

	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	contentType := multipartWriter.FormDataContentType()
	go func() {
		defer form.RemoveAll()
		for name, values := range form.Value {
			for _, value := range values {
				if name == "stream" && image2True(value) {
					value = "false"
				}
				if name == "model" && mapping[value] != "" {
					value = mapping[value]
				}
				if err := multipartWriter.WriteField(name, value); err != nil {
					_ = writer.CloseWithError(err)
					return
				}
			}
		}
		for name, files := range form.File {
			for _, fileHeader := range files {
				if err := copyImage2MultipartFile(multipartWriter, name, fileHeader); err != nil {
					_ = writer.CloseWithError(err)
					return
				}
			}
		}
		if err := multipartWriter.Close(); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_ = writer.Close()
	}()
	return reader, contentType, responseFormat, nil
}

func copyImage2MultipartFile(writer *multipart.Writer, name string, fileHeader *multipart.FileHeader) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name": name, "filename": fileHeader.Filename,
	}))
	contentType := fileHeader.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	file, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(part, file)
	return err
}

func image2True(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case json.Number:
		return typed.String() == "1"
	case string:
		return strings.EqualFold(typed, "true")
	default:
		return false
	}
}

func copyImage2RequestHeaders(target, source http.Header) {
	excluded := map[string]bool{
		"accept-encoding": true, "authorization": true, "connection": true,
		"content-length": true, "content-type": true, "cookie": true, "host": true,
		"keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true,
		"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
	}
	for name, values := range source {
		if excluded[strings.ToLower(name)] {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func copyImage2ResponseHeaders(target, source http.Header) {
	for _, name := range []string{"Content-Type", "X-Request-Id", "Retry-After"} {
		if value := source.Get(name); value != "" {
			target.Set(name, value)
		}
	}
}

func (s *Server) convertImage2Response(ctx context.Context, raw []byte, imageBaseURL, responseFormat string) ([]byte, *image2ProxyError) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, &image2ProxyError{
			status: http.StatusBadGateway, message: "Upstream returned invalid JSON.",
			errorType: "upstream_error", code: "invalid_upstream_json",
		}
	}
	items, ok := payload["data"].([]any)
	if !ok {
		return nil, &image2ProxyError{
			status: http.StatusBadGateway, message: "Upstream returned an invalid Images API response.",
			errorType: "upstream_error", code: "invalid_images_response",
		}
	}
	var saved []string
	rollback := func() {
		for _, path := range saved {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("删除未完成的 image2 图片失败 %s: %v", filepath.Base(path), err)
			}
		}
	}
	convertedAny := false
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			rollback()
			return nil, image2InvalidBase64()
		}
		if responseFormat == "b64_json" {
			if encoded, hasBase64 := item["b64_json"].(string); hasBase64 && encoded != "" {
				continue
			}
			imageURL, ok := item["url"].(string)
			if !ok || imageURL == "" {
				return nil, image2InvalidURL()
			}
			content, err := s.readImage2URL(ctx, imageURL)
			if err != nil {
				return nil, image2InvalidURL()
			}
			delete(item, "url")
			item["b64_json"] = base64.StdEncoding.EncodeToString(content)
			convertedAny = true
			continue
		}
		encoded, hasBase64 := item["b64_json"]
		if !hasBase64 || encoded == nil {
			imageURL, ok := item["url"].(string)
			if !ok || imageURL == "" {
				rollback()
				return nil, image2InvalidBase64()
			}
			proxyURL, err := s.image2ProxyURL(imageBaseURL, imageURL)
			if err != nil {
				rollback()
				return nil, image2InvalidURL()
			}
			item["url"] = proxyURL
			convertedAny = true
			continue
		}
		encodedString, ok := encoded.(string)
		if !ok || encodedString == "" {
			rollback()
			return nil, image2InvalidBase64()
		}
		if imageBaseURL == "" {
			rollback()
			return nil, &image2ProxyError{
				status: http.StatusServiceUnavailable, message: "image_domain is not configured.",
				errorType: "configuration_error", code: "image_domain_missing",
			}
		}
		content, err := base64.StdEncoding.Strict().DecodeString(encodedString)
		if err != nil || len(content) == 0 {
			rollback()
			return nil, image2InvalidBase64()
		}
		path, err := s.writeImage2File(content)
		if err != nil {
			rollback()
			return nil, image2InvalidBase64()
		}
		saved = append(saved, path)
		delete(item, "b64_json")
		item["url"] = strings.TrimRight(imageBaseURL, "/") + "/" + url.PathEscape(filepath.Base(path))
		convertedAny = true
	}
	if !convertedAny {
		return raw, nil
	}
	converted, err := json.Marshal(payload)
	if err != nil {
		rollback()
		return nil, image2InternalError(err)
	}
	return converted, nil
}

func newImage2URLCipher(secret string) cipher.AEAD {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = image2DefaultURLKey
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err)
	}
	result, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return result
}

func parseImage2RemoteURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("invalid image URL")
	}
	return parsed, nil
}

func image2ProxyExtension(urlPath string) string {
	switch strings.ToLower(path.Ext(urlPath)) {
	case ".jpg", ".jpeg":
		return ".jpg"
	case ".webp":
		return ".webp"
	default:
		return ".png"
	}
}

func (s *Server) image2ProxyURL(imageBaseURL, source string) (string, error) {
	parsed, err := parseImage2RemoteURL(source)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.image2URLCipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := s.image2URLCipher.Seal(nonce, nonce, []byte(source), []byte(image2URLAssociatedData))
	token := base64.RawURLEncoding.EncodeToString(sealed)
	return strings.TrimRight(imageBaseURL, "/") + "/from/" + token + image2ProxyExtension(parsed.Path), nil
}

func (s *Server) decryptImage2URL(token string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(sealed) < s.image2URLCipher.NonceSize()+s.image2URLCipher.Overhead() {
		return "", errors.New("invalid image token")
	}
	nonceSize := s.image2URLCipher.NonceSize()
	plain, err := s.image2URLCipher.Open(nil, sealed[:nonceSize], sealed[nonceSize:], []byte(image2URLAssociatedData))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Server) readImage2URL(ctx context.Context, value string) ([]byte, error) {
	if _, err := parseImage2RemoteURL(value); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.image2Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("image URL returned a non-success status")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, image2MaxResponseBytes+1))
	if err != nil || len(content) == 0 || len(content) > image2MaxResponseBytes ||
		!strings.HasPrefix(http.DetectContentType(content), "image/") {
		return nil, errors.New("image URL returned invalid image data")
	}
	return content, nil
}

func image2ProxyToken(name string) (string, bool) {
	extension := path.Ext(name)
	switch strings.ToLower(extension) {
	case ".png", ".jpg", ".webp":
	default:
		return "", false
	}
	token := strings.TrimSuffix(name, extension)
	return token, token != ""
}

func validImage2ProxyContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	switch strings.ToLower(mediaType) {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func (s *Server) serveImage2URL(w http.ResponseWriter, r *http.Request) {
	token, ok := image2ProxyToken(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	source, err := s.decryptImage2URL(token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := parseImage2RemoteURL(source); err != nil {
		http.NotFound(w, r)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, source, nil)
	if err != nil {
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}
	request.Header.Set("Accept", "image/png, image/jpeg, image/webp")
	response, err := s.image2ProxyClient.Do(request)
	if err != nil {
		status := http.StatusBadGateway
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, "image unavailable", status)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 ||
		!validImage2ProxyContentType(response.Header.Get("Content-Type")) {
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}
	for _, name := range []string{"Content-Type", "Content-Length", "Cache-Control", "ETag", "Last-Modified", "Expires"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, response.Body)
	}
}

func (s *Server) writeImage2File(content []byte) (string, error) {
	if s.image2InitErr != nil {
		return "", s.image2InitErr
	}
	extension := ""
	switch http.DetectContentType(content) {
	case "image/png":
		extension = "png"
	case "image/jpeg":
		extension = "jpg"
	case "image/webp":
		extension = "webp"
	default:
		return "", errors.New("unsupported image format")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	name := hex.EncodeToString(random) + "." + extension
	finalPath := filepath.Join(s.image2Dir, name)
	temporaryPath := filepath.Join(s.image2Dir, "."+name+".tmp")
	if err := os.WriteFile(temporaryPath, content, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	return finalPath, nil
}

func (s *Server) serveImage2File(w http.ResponseWriter, r *http.Request) {
	if s.image2InitErr != nil {
		http.Error(w, "image2 storage unavailable", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	if !validImage2Filename(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Join(s.image2Dir, name))
}

func validImage2Filename(name string) bool {
	stem, extension, found := strings.Cut(name, ".")
	if !found || len(stem) != 32 || (extension != "png" && extension != "jpg" && extension != "webp") {
		return false
	}
	_, err := hex.DecodeString(stem)
	return err == nil
}

func (s *Server) cleanupImage2Files() (int, error) {
	settings, err := s.store.Image2Settings()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-time.Duration(settings.RetentionHours) * time.Hour)
	entries, err := os.ReadDir(s.image2Dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			log.Printf("读取 image2 图片信息失败 %s: %v", entry.Name(), err)
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.image2Dir, entry.Name())); err != nil {
			log.Printf("清理过期 image2 图片失败 %s: %v", entry.Name(), err)
			continue
		}
		removed++
	}
	return removed, nil
}

func (s *Server) image2CleanupLoop() {
	defer close(s.image2Done)
	ticker := time.NewTicker(image2CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.image2Stop:
			return
		case <-ticker.C:
			removed, err := s.cleanupImage2Files()
			if err != nil {
				log.Printf("清理 image2 图片失败: %v", err)
			} else if removed > 0 {
				log.Printf("已清理 %d 个过期 image2 图片", removed)
			}
		}
	}
}

func writeImage2ProxyError(w http.ResponseWriter, err *image2ProxyError) {
	for name, values := range err.header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	writeJSON(w, err.status, map[string]any{"error": map[string]any{
		"message": err.message, "type": err.errorType, "param": nil, "code": err.code,
	}})
}

func image2InvalidJSON(err error) *image2ProxyError {
	status, code := http.StatusBadRequest, "invalid_json"
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		status, code = http.StatusRequestEntityTooLarge, "request_too_large"
	}
	return &image2ProxyError{
		status: status, message: "Request body must be a JSON object.",
		errorType: "invalid_request_error", code: code,
	}
}

func image2UnsupportedContentType(operation string) *image2ProxyError {
	expected := "application/json"
	if operation == "edits" {
		expected += " or multipart/form-data"
	}
	return &image2ProxyError{
		status: http.StatusUnsupportedMediaType, message: "Content-Type must be " + expected + ".",
		errorType: "invalid_request_error", code: "unsupported_content_type",
	}
}

func image2InvalidBase64() *image2ProxyError {
	return &image2ProxyError{
		status: http.StatusBadGateway, message: "Upstream returned invalid base64 image data.",
		errorType: "upstream_error", code: "invalid_image_data",
	}
}

func image2InvalidURL() *image2ProxyError {
	return &image2ProxyError{
		status: http.StatusBadGateway, message: "Upstream returned an invalid or unavailable image URL.",
		errorType: "upstream_error", code: "invalid_image_url",
	}
}

func image2InternalError(err error) *image2ProxyError {
	log.Printf("image2 内部错误: %v", err)
	return &image2ProxyError{
		status: http.StatusInternalServerError, message: "Internal server error.",
		errorType: "server_error", code: "internal_error",
	}
}
