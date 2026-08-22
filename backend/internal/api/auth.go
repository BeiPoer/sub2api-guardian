package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/auth"
	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

const (
	// sessionCookie 是会话 Cookie 名。
	sessionCookie = "guardian_session"

	// sessionTTL 是会话有效期。持续使用会自动续期（见 renewSession）。
	sessionTTL = 14 * 24 * time.Hour

	// sessionRenewInterval 是同一个会话两次续期之间的最小间隔。
	// 页面每 20 秒就会刷一次，不限流的话每次请求都要写一次库。
	sessionRenewInterval = time.Hour

	// maxRenewEntries 是续期限流表的容量上限，超过就整体丢弃重来。
	maxRenewEntries = 4096

	authRateWindow = time.Minute
	loginRateLimit = 10
	setupRateLimit = 5
)

// ctxUserKey 用于在请求上下文里传递已认证用户。
type ctxUserKey struct{}

// currentUser 取出当前请求的登录用户。
func currentUser(r *http.Request) (domain.User, bool) {
	user, ok := r.Context().Value(ctxUserKey{}).(domain.User)
	return user, ok
}

// currentToken 取出当前请求携带的会话令牌。
func currentToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// requireAuth 拦下未登录请求。
//
// 未登录一律返回 401 且带 code=UNAUTHENTICATED，前端据此切到登录页，
// 而不是把它当成普通错误弹一堆提示。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := currentToken(r)
		if token == "" {
			writeUnauthenticated(w)
			return
		}
		user, err := s.store.SessionUser(token)
		if err != nil {
			// 令牌无效或已过期，顺手把浏览器里的残留 Cookie 清掉。
			clearSessionCookie(w, r)
			writeUnauthenticated(w)
			return
		}

		s.renewSession(token)
		ctx := context.WithValue(r.Context(), ctxUserKey{}, user)
		next(w, r.WithContext(ctx))
	}
}

// renewSession 滑动续期，让持续使用的会话不会在中途被踢下线。
//
// 每次请求都写库太浪费，用内存里的时间戳限流：同一个令牌
// 至多每 sessionRenewInterval 续一次。进程重启后重新计时，
// 代价只是多写一次库。
func (s *Server) renewSession(token string) {
	now := time.Now()

	s.renewMu.Lock()
	last, seen := s.lastRenew[token]
	if seen && now.Sub(last) < sessionRenewInterval {
		s.renewMu.Unlock()
		return
	}
	// 限流表按令牌累积，长期运行会越攒越多，超过阈值就整体丢弃重来。
	if len(s.lastRenew) > maxRenewEntries {
		s.lastRenew = make(map[string]time.Time, 64)
	}
	s.lastRenew[token] = now
	s.renewMu.Unlock()

	_ = s.store.TouchSession(token, now.Add(sessionTTL))
}

// setupStatus 报告是否需要初始化。这是唯一无需登录即可访问的业务接口。
func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.UserCount()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup": count == 0,
		"has_users":   count > 0,
	})
}

// setup 处理首次部署的初始化：建管理员 + 存连接 + 实测连接 + 首次同步。
//
// 只在一个用户都没有时可用。判定与建号的原子性由 store.CreateFirstUser 保证。
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !s.consumeAuthAttempt(w, r, "setup", setupRateLimit) {
		return
	}
	var payload struct {
		Username       string `json:"username"`
		Password       string `json:"password"`
		BaseURL        string `json:"base_url"`
		AdminAPIKey    string `json:"admin_api_key"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}

	username := strings.TrimSpace(payload.Username)
	if username == "" {
		writeErrorMessage(w, http.StatusBadRequest, "请填写用户名")
		return
	}
	if err := auth.ValidatePassword(payload.Password); err != nil {
		writeErrorMessage(w, http.StatusBadRequest, err.Error())
		return
	}

	baseURL := strings.TrimRight(strings.TrimSpace(payload.BaseURL), "/")
	adminKey := strings.TrimSpace(payload.AdminAPIKey)
	if baseURL == "" || adminKey == "" {
		writeErrorMessage(w, http.StatusBadRequest, "请填写 sub2api 地址与 Admin API Key")
		return
	}
	timeout := payload.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}

	conn := domain.Connection{
		BaseURL:        baseURL,
		AdminAPIKey:    adminKey,
		TimeoutSeconds: timeout,
		Enabled:        true,
	}

	// 先验连接，再建账号。
	//
	// 顺序很重要：账号一旦建出来，初始化接口就永久关闭了（只能建第一个用户）。
	// 如果先建号再验连接，Key 填错的人会被卡在「账号已存在但连不上」的中间态，
	// 只能去数据库里删表才能重来。先验后建则可以在向导里反复改到对为止。
	probeCtx, cancelProbe := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancelProbe()
	probe := upstream.New(conn.BaseURL, conn.AdminAPIKey, time.Duration(conn.TimeoutSeconds)*time.Second)
	if _, err := probe.ListGroups(probeCtx); err != nil {
		writeErrorMessage(w, http.StatusBadGateway,
			"连接 sub2api 失败，请检查地址与 Admin API Key："+err.Error())
		return
	}

	hash, err := auth.HashPassword(payload.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	userID, err := s.store.CreateFirstUser(username, hash)
	if err != nil {
		if err == store.ErrSetupDone {
			writeErrorMessage(w, http.StatusConflict, "系统已完成初始化，请直接登录")
			return
		}
		writeError(w, err)
		return
	}

	if err := s.store.SaveConnection(conn); err != nil {
		writeError(w, err)
		return
	}
	s.engine.Reconfigure(conn)

	// 连接已经验过了，这里的同步基本不会失败；万一失败也不必挡人，
	// 后台每 2 分钟还会自己重试一次。
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	syncErr := s.engine.SyncNow(ctx)

	if _, err := s.issueSession(w, r, userID); err != nil {
		writeError(w, err)
		return
	}

	s.store.Log("info", "setup_completed", nil, nil,
		"已完成初始化：创建管理员账号并配置 sub2api 连接", map[string]any{
			"username": username,
			"base_url": baseURL,
		})
	s.hub.broadcast()

	out := map[string]any{"ok": true, "username": username}
	if syncErr != nil {
		out["sync_error"] = syncErr.Error()
	}
	writeJSON(w, http.StatusOK, out)
	s.resetAuthAttempts(r, "setup")
}

// login 校验口令并下发会话 Cookie。
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.consumeAuthAttempt(w, r, "login", loginRateLimit) {
		return
	}
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}

	user, err := s.store.UserByName(payload.Username)
	if err != nil {
		// 不区分「用户不存在」与「口令错误」：区分开等于送了一个用户名枚举接口。
		writeErrorMessage(w, http.StatusUnauthorized, "用户名或密码不正确")
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, payload.Password) {
		s.store.Log("warn", "login_failed", nil, nil,
			"登录失败：密码不正确（用户 "+user.Username+"）", nil)
		writeErrorMessage(w, http.StatusUnauthorized, "用户名或密码不正确")
		return
	}

	if _, err := s.issueSession(w, r, user.ID); err != nil {
		writeError(w, err)
		return
	}
	s.store.Log("info", "login", nil, nil, "用户 "+user.Username+" 已登录", nil)
	s.resetAuthAttempts(r, "login")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": user.Username})
}

// consumeAuthAttempt 对无需会话的敏感入口做进程内短窗口限流。
// 只有 TCP 对端是回环地址时才采信反向代理传入的 X-Forwarded-For。
func (s *Server) consumeAuthAttempt(w http.ResponseWriter, r *http.Request, scope string, limit int) bool {
	now := time.Now()
	key := scope + ":" + requestRemoteHost(r)

	s.authRateMu.Lock()
	if len(s.authRates) > 4096 {
		s.authRates = make(map[string]authRateEntry, 64)
	}
	entry := s.authRates[key]
	if entry.resetAt.IsZero() || !now.Before(entry.resetAt) {
		entry = authRateEntry{resetAt: now.Add(authRateWindow)}
	}
	if entry.count >= limit {
		retryAfter := max(1, int(time.Until(entry.resetAt).Seconds()))
		s.authRateMu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeErrorMessage(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
		return false
	}
	entry.count++
	s.authRates[key] = entry
	s.authRateMu.Unlock()
	return true
}

func (s *Server) resetAuthAttempts(r *http.Request, scope string) {
	key := scope + ":" + requestRemoteHost(r)
	s.authRateMu.Lock()
	delete(s.authRates, key)
	s.authRateMu.Unlock()
}

func requestRemoteHost(r *http.Request) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	peer := net.ParseIP(strings.TrimSpace(host))
	if peer != nil && peer.IsLoopback() {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			if ip := net.ParseIP(forwarded); ip != nil {
				return ip.String()
			}
		}
	}
	if peer != nil {
		return peer.String()
	}
	return host
}

// logout 注销当前会话。
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := currentToken(r); token != "" {
		_ = s.store.DeleteSession(token)
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// me 返回当前登录用户。
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeUnauthenticated(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   user.Username,
		"created_at": user.CreatedAt,
	})
}

// updateAccount 修改用户名或口令。
//
// 改口令要求先验旧口令：会话可能是别人在无人值守的机器上捡到的，
// 只凭会话就能改口令等于把账号送出去。
func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeUnauthenticated(w)
		return
	}
	var payload struct {
		Username        string `json:"username"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}

	if !auth.VerifyPassword(user.PasswordHash, payload.CurrentPassword) {
		writeErrorMessage(w, http.StatusForbidden, "当前密码不正确")
		return
	}

	changed := make([]string, 0, 2)

	if name := strings.TrimSpace(payload.Username); name != "" && !strings.EqualFold(name, user.Username) {
		if err := s.store.UpdateUsername(user.ID, name); err != nil {
			if err == store.ErrUserExists {
				writeErrorMessage(w, http.StatusConflict, "该用户名已被占用")
				return
			}
			writeError(w, err)
			return
		}
		changed = append(changed, "用户名")
	}

	passwordChanged := false
	if payload.NewPassword != "" {
		if err := auth.ValidatePassword(payload.NewPassword); err != nil {
			writeErrorMessage(w, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := auth.HashPassword(payload.NewPassword)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := s.store.UpdateUserPassword(user.ID, hash); err != nil {
			writeError(w, err)
			return
		}
		passwordChanged = true
		changed = append(changed, "密码")
	}

	if len(changed) == 0 {
		writeErrorMessage(w, http.StatusBadRequest, "没有任何改动")
		return
	}

	if passwordChanged {
		// 口令换了就把别处的登录态一并作废，只留当前这一个。
		_ = s.store.DeleteUserSessionsExcept(user.ID, currentToken(r))
	}

	updated, err := s.store.User(user.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	s.store.Log("info", "account_updated", nil, nil,
		"账户设置已更新："+strings.Join(changed, "、"), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"username": updated.Username,
		"changed":  changed,
	})
}

// issueSession 创建会话并写入 Cookie。
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, userID int64) (string, error) {
	token, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(sessionTTL)
	if err := s.store.CreateSession(token, userID, expires, r.UserAgent()); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: token,
		Path:  "/",
		// HttpOnly：JS 读不到，XSS 也偷不走会话。
		HttpOnly: true,
		// SameSite=Strict：跨站请求不带 Cookie，顺带挡掉 CSRF。
		SameSite: http.SameSiteStrictMode,
		// 仅在 HTTPS 下加 Secure，否则本地 http 访问会拿不到 Cookie。
		Secure:  isTLS(r),
		Expires: expires,
	})
	return token, nil
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isTLS(r),
		MaxAge:   -1,
	})
}

// isTLS 判断请求是否经由 HTTPS 抵达（含反向代理转发的情形）。
func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func writeUnauthenticated(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": "请先登录",
		"code":  "UNAUTHENTICATED",
	})
}

func writeErrorMessage(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
