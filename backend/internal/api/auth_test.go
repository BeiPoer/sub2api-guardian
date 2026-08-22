package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/auth"
	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

// authFixture 是鉴权用例的上下文。
type authFixture struct {
	server      *Server
	handler     http.Handler
	store       *store.Store
	upstreamURL string
}

// setupAuthAPI 起一个未初始化（没有任何用户）的服务。
func setupAuthAPI(t *testing.T) *authFixture {
	t.Helper()

	upstreamServer := httptest.NewServer((&fakeUpstream{groupCount: 1}).handler())
	t.Cleanup(upstreamServer.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SaveConnection(domain.Connection{
		BaseURL: upstreamServer.URL, AdminAPIKey: "k", TimeoutSeconds: 10, Enabled: false,
	}); err != nil {
		t.Fatalf("保存连接失败: %v", err)
	}

	client := upstream.New(upstreamServer.URL, "k", 10*time.Second)
	server := NewServer(st, client, engine.New(st, client), nil)
	t.Cleanup(server.Close)

	// 这些用例自己控制凭据，清掉共享的会话令牌避免串味。
	testSessionToken = ""
	t.Cleanup(func() { testSessionToken = "" })

	return &authFixture{
		server:      server,
		handler:     server.Handler(),
		store:       st,
		upstreamURL: upstreamServer.URL,
	}
}

// raw 发一个不带任何凭据的请求。
func raw(t *testing.T, handler http.Handler, method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// sessionCookieFrom 从响应里取出会话 Cookie。
func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookie && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatalf("响应里没有会话 Cookie: %s", rec.Body.String())
	return nil
}

// TestEveryProtectedRouteRequiresAuth 遍历路由表逐条验证未登录返回 401。
//
// 用表遍历而不是手写清单：以后新增接口只要进了 protectedRoutes，
// 就自动被这条测试覆盖；漏挂鉴权会在这里失败，而不是等到线上被人发现。
func TestEveryProtectedRouteRequiresAuth(t *testing.T) {
	fx := setupAuthAPI(t)
	server, handler := fx.server, fx.handler

	routes := server.protectedRoutes()
	if len(routes) < 20 {
		t.Fatalf("路由表只有 %d 条，疑似没取到完整列表", len(routes))
	}

	for pattern := range routes {
		t.Run(pattern, func(t *testing.T) {
			method, path, ok := strings.Cut(pattern, " ")
			if !ok {
				t.Fatalf("路由格式异常: %q", pattern)
			}
			// 把 {id} 占位符换成一个具体 ID。
			path = strings.ReplaceAll(path, "{id}", "101")

			rec := raw(t, handler, method, path, map[string]any{})
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("未登录访问 %s 返回 %d，期望 401（该接口没挂鉴权？）",
					pattern, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "UNAUTHENTICATED") {
				t.Fatalf("401 响应应带 code=UNAUTHENTICATED，实际 %s", rec.Body.String())
			}
		})
	}
}

// TestHealthzStaysPublicAndQuiet 确认探活公开，但不泄露引擎状态。
func TestHealthzStaysPublicAndQuiet(t *testing.T) {
	fx := setupAuthAPI(t)
	handler := fx.handler

	rec := raw(t, handler, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz 状态码 = %d, 期望 200", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if _, leaked := payload["status"]; leaked {
		t.Fatal("探活接口不该把引擎状态暴露给未鉴权调用方")
	}
}

// TestSetupFlow 覆盖初始化的完整链路。
func TestSetupFlow(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store

	rec := raw(t, handler, http.MethodGet, "/api/setup", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"needs_setup":true`) {
		t.Fatalf("空库应报告需要初始化，实际 %d %s", rec.Code, rec.Body.String())
	}

	rec = raw(t, handler, http.MethodPost, "/api/setup", map[string]any{
		"username":      "admin",
		"password":      "hunter2hunter2",
		"base_url":      fx.upstreamURL,
		"admin_api_key": "some-key",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("初始化失败: %d %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFrom(t, rec)

	// 初始化后应当已经登录。
	if got := raw(t, handler, http.MethodGet, "/api/me", nil, cookie); got.Code != http.StatusOK {
		t.Fatalf("初始化后应处于登录态，/api/me 返回 %d", got.Code)
	}
	// 连接配置要落库。
	conn, err := st.Connection()
	if err != nil {
		t.Fatalf("读取连接失败: %v", err)
	}
	if conn.AdminAPIKey != "some-key" {
		t.Fatalf("Admin Key = %q, 期望已保存", conn.AdminAPIKey)
	}

	rec = raw(t, handler, http.MethodGet, "/api/setup", nil)
	if !strings.Contains(rec.Body.String(), `"needs_setup":false`) {
		t.Fatalf("初始化后不该再报告需要初始化: %s", rec.Body.String())
	}
}

// TestSetupCannotRunTwice 是初始化接口最重要的约束。
//
// 允许二次调用等于给面板留了个不需要任何凭据的建号后门。
func TestSetupCannotRunTwice(t *testing.T) {
	fx := setupAuthAPI(t)
	handler := fx.handler

	body := map[string]any{
		"username": "admin", "password": "hunter2hunter2",
		"base_url": fx.upstreamURL, "admin_api_key": "k",
	}
	if rec := raw(t, handler, http.MethodPost, "/api/setup", body); rec.Code != http.StatusOK {
		t.Fatalf("首次初始化失败: %d %s", rec.Code, rec.Body.String())
	}

	body["username"] = "intruder"
	rec := raw(t, handler, http.MethodPost, "/api/setup", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("二次初始化返回 %d, 期望 409", rec.Code)
	}
}

// TestSetupRejectsWeakPassword 确认口令强度在服务端校验，不只靠前端。
func TestSetupRejectsWeakPassword(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store

	rec := raw(t, handler, http.MethodPost, "/api/setup", map[string]any{
		"username": "admin", "password": "123",
		"base_url": fx.upstreamURL, "admin_api_key": "k",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("弱口令返回 %d, 期望 400", rec.Code)
	}
	if count, _ := st.UserCount(); count != 0 {
		t.Fatalf("弱口令不该建出用户，实际用户数 %d", count)
	}
}

// TestSetupRequiresConnection 确认没填连接信息不放行。
func TestSetupRequiresConnection(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store

	rec := raw(t, handler, http.MethodPost, "/api/setup", map[string]any{
		"username": "admin", "password": "hunter2hunter2",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少连接信息返回 %d, 期望 400", rec.Code)
	}
	if count, _ := st.UserCount(); count != 0 {
		t.Fatalf("校验失败时不该建出用户，实际用户数 %d", count)
	}
}

// TestSetupRejectsBadConnection 确认连不上 sub2api 时不放行、也不建账号。
//
// 顺序是关键：账号一旦建出来，初始化接口就永久关闭了（只能建第一个用户）。
// 若先建号再验连接，Key 填错的人会卡在「账号已存在但连不上」的中间态，
// 只能去数据库删表才能重来。
func TestSetupRejectsBadConnection(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store

	rec := raw(t, handler, http.MethodPost, "/api/setup", map[string]any{
		"username": "admin", "password": "hunter2hunter2",
		// 指向一个没人监听的端口，必然连不上。
		"base_url": "http://127.0.0.1:1", "admin_api_key": "k",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("连不上 sub2api 时返回 %d, 期望 502", rec.Code)
	}
	if count, _ := st.UserCount(); count != 0 {
		t.Fatalf("连接校验失败时不该建出账号，实际用户数 %d", count)
	}

	// 关键点：还能用正确的连接重来一次，不会被前一次失败锁死。
	rec = raw(t, handler, http.MethodPost, "/api/setup", map[string]any{
		"username": "admin", "password": "hunter2hunter2",
		"base_url": fx.upstreamURL, "admin_api_key": "k",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("修正连接后应能重试初始化，实际 %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginAndLogout(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store
	hash, _ := auth.HashPassword("hunter2hunter2")
	if _, err := st.CreateUser("admin", hash); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	rec := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
		"username": "admin", "password": "hunter2hunter2",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFrom(t, rec)

	if got := raw(t, handler, http.MethodGet, "/api/me", nil, cookie); got.Code != http.StatusOK {
		t.Fatalf("登录后 /api/me 返回 %d", got.Code)
	}

	if got := raw(t, handler, http.MethodPost, "/api/logout", nil, cookie); got.Code != http.StatusOK {
		t.Fatalf("注销返回 %d", got.Code)
	}
	if got := raw(t, handler, http.MethodGet, "/api/me", nil, cookie); got.Code != http.StatusUnauthorized {
		t.Fatalf("注销后会话应失效，实际 %d", got.Code)
	}
}

// TestLoginCookieIsHardened 确认会话 Cookie 的安全属性。
func TestLoginCookieIsHardened(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store
	hash, _ := auth.HashPassword("hunter2hunter2")
	_, _ = st.CreateUser("admin", hash)

	rec := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
		"username": "admin", "password": "hunter2hunter2",
	})
	cookie := sessionCookieFrom(t, rec)

	if !cookie.HttpOnly {
		t.Fatal("会话 Cookie 必须是 HttpOnly，否则 XSS 能直接偷走登录态")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("会话 Cookie 应为 SameSite=Strict，用于挡 CSRF")
	}
}

// TestLoginDoesNotLeakWhetherUserExists 确认登录失败不泄露用户名是否存在。
func TestLoginDoesNotLeakWhetherUserExists(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store
	hash, _ := auth.HashPassword("hunter2hunter2")
	_, _ = st.CreateUser("admin", hash)

	wrongPassword := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
		"username": "admin", "password": "not-the-password",
	})
	noSuchUser := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
		"username": "nobody", "password": "not-the-password",
	})

	if wrongPassword.Code != noSuchUser.Code {
		t.Fatalf("两种失败的状态码不同（%d vs %d），可用于枚举用户名",
			wrongPassword.Code, noSuchUser.Code)
	}
	if wrongPassword.Body.String() != noSuchUser.Body.String() {
		t.Fatalf("两种失败的响应体不同，可用于枚举用户名:\n%s\n%s",
			wrongPassword.Body.String(), noSuchUser.Body.String())
	}
}

func TestLoginRateLimitAndSuccessfulReset(t *testing.T) {
	fx := setupAuthAPI(t)
	hash, _ := auth.HashPassword("correct-password")
	_, _ = fx.store.CreateUser("admin", hash)

	wrong := map[string]any{"username": "admin", "password": "wrong-password"}
	for attempt := 1; attempt <= loginRateLimit; attempt++ {
		rec := raw(t, fx.handler, http.MethodPost, "/api/login", wrong)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败登录返回 %d，期望 401", attempt, rec.Code)
		}
	}
	blocked := raw(t, fx.handler, http.MethodPost, "/api/login", wrong)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("第 %d 次登录返回 %d，期望 429", loginRateLimit+1, blocked.Code)
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("限流响应应包含 Retry-After")
	}

	// A successful login clears the source counter. Use a fresh source because
	// a currently blocked client cannot authenticate until its window expires.
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader([]byte(
		`{"username":"admin","password":"correct-password"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.20:4321"
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确凭据登录失败: %d %s", rec.Code, rec.Body.String())
	}
	key := "login:198.51.100.20"
	fx.server.authRateMu.Lock()
	_, stillTracked := fx.server.authRates[key]
	fx.server.authRateMu.Unlock()
	if stillTracked {
		t.Fatal("成功登录后应清除该来源的限流计数")
	}
}

func TestSetupRateLimit(t *testing.T) {
	fx := setupAuthAPI(t)
	invalid := map[string]any{"username": "admin", "password": "short"}
	for attempt := 1; attempt <= setupRateLimit; attempt++ {
		rec := raw(t, fx.handler, http.MethodPost, "/api/setup", invalid)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次无效初始化返回 %d，期望 400", attempt, rec.Code)
		}
	}
	blocked := raw(t, fx.handler, http.MethodPost, "/api/setup", invalid)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("第 %d 次初始化返回 %d，期望 429", setupRateLimit+1, blocked.Code)
	}
}

func TestAuthRateLimitIgnoresForwardedFor(t *testing.T) {
	fx := setupAuthAPI(t)
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader([]byte(`{}`)))
	req.RemoteAddr = "203.0.113.10:9000"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	rec := httptest.NewRecorder()

	if !fx.server.consumeAuthAttempt(rec, req, "login", 1) {
		t.Fatal("首次请求不应被限流")
	}
	fx.server.authRateMu.Lock()
	_, remoteTracked := fx.server.authRates["login:203.0.113.10"]
	_, forwardedTracked := fx.server.authRates["login:1.2.3.4"]
	fx.server.authRateMu.Unlock()
	if !remoteTracked || forwardedTracked {
		t.Fatalf("限流键应来自 RemoteAddr，remote=%v forwarded=%v", remoteTracked, forwardedTracked)
	}
}

func TestAuthRateLimitTrustsLoopbackProxy(t *testing.T) {
	fx := setupAuthAPI(t)
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader([]byte(`{}`)))
	req.RemoteAddr = "127.0.0.1:9000"
	req.Header.Set("X-Forwarded-For", "198.51.100.40, 127.0.0.1")
	rec := httptest.NewRecorder()

	if !fx.server.consumeAuthAttempt(rec, req, "login", 1) {
		t.Fatal("首次请求不应被限流")
	}
	fx.server.authRateMu.Lock()
	_, forwardedTracked := fx.server.authRates["login:198.51.100.40"]
	_, proxyTracked := fx.server.authRates["login:127.0.0.1"]
	fx.server.authRateMu.Unlock()
	if !forwardedTracked || proxyTracked {
		t.Fatalf("回环代理应按真实客户端限流，forwarded=%v proxy=%v", forwardedTracked, proxyTracked)
	}
}

func TestSetupRejectsCrossSiteAndNonJSONRequests(t *testing.T) {
	t.Run("cross-site", func(t *testing.T) {
		fx := setupAuthAPI(t)
		req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader([]byte(`{
			"username":"admin","password":"hunter2hunter2","base_url":"http://example.com","admin_api_key":"k"
		}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("跨站初始化返回 %d，期望 403", rec.Code)
		}
		if count, _ := fx.store.UserCount(); count != 0 {
			t.Fatalf("跨站请求不该创建用户，实际用户数 %d", count)
		}
	})

	t.Run("text-plain", func(t *testing.T) {
		fx := setupAuthAPI(t)
		req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{
			"username":"admin","password":"hunter2hunter2","base_url":"http://example.com","admin_api_key":"k"
		}`))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("text/plain 初始化返回 %d，期望 415", rec.Code)
		}
		if count, _ := fx.store.UserCount(); count != 0 {
			t.Fatalf("非 JSON 请求不该创建用户，实际用户数 %d", count)
		}
	})
}

func TestHTTPBoundaryProtection(t *testing.T) {
	fx := setupAuthAPI(t)

	response := raw(t, fx.handler, http.MethodGet, "/healthz", nil)
	for _, header := range []string{
		"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options",
		"Referrer-Policy", "Permissions-Policy",
	} {
		if response.Header().Get(header) == "" {
			t.Fatalf("响应缺少安全头 %s", header)
		}
	}

	tooLarge := append([]byte{'"'}, bytes.Repeat([]byte("x"), int(maxRequestBodyBytes)+1)...)
	tooLarge = append(tooLarge, '"')
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(tooLarge))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("超限正文返回 %d %s，期望 400 且说明过大", rec.Code, rec.Body.String())
	}
}

// TestInvalidSessionRejected 确认伪造的令牌不被接受。
func TestInvalidSessionRejected(t *testing.T) {
	fx := setupAuthAPI(t)
	handler := fx.handler

	rec := raw(t, handler, http.MethodGet, "/api/me", nil,
		&http.Cookie{Name: sessionCookie, Value: "forged-token"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("伪造令牌返回 %d, 期望 401", rec.Code)
	}
}

// TestChangePasswordRevokesOtherSessions 是改口令的核心语义。
//
// 口令可能是因为泄露才要改的，其他地方的登录态必须一并作废，
// 否则改了口令等于什么都没做。
func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store
	hash, _ := auth.HashPassword("old-password-1")
	_, _ = st.CreateUser("admin", hash)

	login := func() *http.Cookie {
		rec := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
			"username": "admin", "password": "old-password-1",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("登录失败: %d %s", rec.Code, rec.Body.String())
		}
		return sessionCookieFrom(t, rec)
	}
	current := login()
	elsewhere := login()

	rec := raw(t, handler, http.MethodPut, "/api/account", map[string]any{
		"current_password": "old-password-1",
		"new_password":     "brand-new-password",
	}, current)
	if rec.Code != http.StatusOK {
		t.Fatalf("改口令失败: %d %s", rec.Code, rec.Body.String())
	}

	if got := raw(t, handler, http.MethodGet, "/api/me", nil, current); got.Code != http.StatusOK {
		t.Fatalf("当前会话应保留，实际 %d", got.Code)
	}
	if got := raw(t, handler, http.MethodGet, "/api/me", nil, elsewhere); got.Code != http.StatusUnauthorized {
		t.Fatalf("其他会话应被吊销，实际 %d", got.Code)
	}

	// 新口令生效、旧口令失效。
	if rec := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
		"username": "admin", "password": "old-password-1",
	}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("旧口令仍能登录，状态码 %d", rec.Code)
	}
	if rec := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
		"username": "admin", "password": "brand-new-password",
	}); rec.Code != http.StatusOK {
		t.Fatalf("新口令应能登录，状态码 %d", rec.Code)
	}
}

// TestChangeAccountRequiresCurrentPassword 确认改动前必须验旧口令。
//
// 只凭会话就能改用户名口令的话，一台没锁屏的机器就等于账号被接管。
func TestChangeAccountRequiresCurrentPassword(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store
	hash, _ := auth.HashPassword("old-password-1")
	_, _ = st.CreateUser("admin", hash)

	rec := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
		"username": "admin", "password": "old-password-1",
	})
	cookie := sessionCookieFrom(t, rec)

	got := raw(t, handler, http.MethodPut, "/api/account", map[string]any{
		"current_password": "wrong-password",
		"new_password":     "brand-new-password",
	}, cookie)
	if got.Code != http.StatusForbidden {
		t.Fatalf("旧口令不对时返回 %d, 期望 403", got.Code)
	}

	user, _ := st.UserByName("admin")
	if auth.VerifyPassword(user.PasswordHash, "brand-new-password") {
		t.Fatal("旧口令校验失败时不该改掉口令")
	}
}

// TestChangeUsername 覆盖改用户名。
func TestChangeUsername(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store
	hash, _ := auth.HashPassword("old-password-1")
	_, _ = st.CreateUser("admin", hash)

	rec := raw(t, handler, http.MethodPost, "/api/login", map[string]any{
		"username": "admin", "password": "old-password-1",
	})
	cookie := sessionCookieFrom(t, rec)

	got := raw(t, handler, http.MethodPut, "/api/account", map[string]any{
		"current_password": "old-password-1",
		"username":         "operator",
	}, cookie)
	if got.Code != http.StatusOK {
		t.Fatalf("改用户名失败: %d %s", got.Code, got.Body.String())
	}
	if _, err := st.UserByName("operator"); err != nil {
		t.Fatalf("改名后应能按新名字查到: %v", err)
	}
	// 只改名不改口令，当前会话应保留。
	if me := raw(t, handler, http.MethodGet, "/api/me", nil, cookie); me.Code != http.StatusOK {
		t.Fatalf("只改用户名不该踢掉当前会话，实际 %d", me.Code)
	}
}

// TestCorsDoesNotUseWildcardWithCredentials 确认 CORS 不再用通配符。
//
// 通配 Origin 与 Cookie 凭据在浏览器里不能共存，而且通配 + 凭据
// 本身就是把接口开放给任意站点的组合。
func TestCorsDoesNotUseWildcardWithCredentials(t *testing.T) {
	fx := setupAuthAPI(t)
	handler := fx.handler

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin == "*" {
		t.Fatal("不该再返回 Access-Control-Allow-Origin: *")
	}
	if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin == "http://evil.example" {
		t.Fatal("不该回显白名单之外的 Origin")
	}

	// 白名单内的开发端口仍要放行，否则 vite 直连模式没法用。
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5177")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin != "http://127.0.0.1:5177" {
		t.Fatalf("开发端口应被放行，实际 Allow-Origin = %q", origin)
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("放行的源应允许携带凭据，否则 Cookie 会话在 dev 直连下失效")
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
		t.Fatal("按 Origin 变化的响应必须带 Vary: Origin，否则缓存会串源")
	}
}

// TestSessionExpiryRejected 确认过期会话在 API 层也被拒绝。
func TestSessionExpiryRejected(t *testing.T) {
	fx := setupAuthAPI(t)
	handler, st := fx.handler, fx.store
	hash, _ := auth.HashPassword("hunter2hunter2")
	userID, _ := st.CreateUser("admin", hash)

	token, _ := auth.NewSessionToken()
	if err := st.CreateSession(token, userID, time.Now().Add(-time.Second), ""); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	rec := raw(t, handler, http.MethodGet, "/api/me", nil,
		&http.Cookie{Name: sessionCookie, Value: token})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("过期会话返回 %d, 期望 401", rec.Code)
	}
}
