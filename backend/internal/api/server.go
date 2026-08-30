// Package api 暴露 Guardian 的 HTTP 接口与 SSE 实时推送。
package api

import (
	"crypto/cipher"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sub2api-guardian/backend/internal/channelmanager"
	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/reports"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

const maxRequestBodyBytes int64 = 1 << 20

// Server 聚合 HTTP 处理所需的依赖。
type Server struct {
	store            *store.Store
	client           *upstream.Client
	engine           *engine.Engine
	hub              *hub
	assets           http.Handler
	upstreamChannels *channelmanager.Manager
	scheduledReports *reports.Manager

	// lastRenew 给会话滑动续期做限流，避免每个请求都写一次库。
	renewMu   sync.Mutex
	lastRenew map[string]time.Time

	authRateMu sync.Mutex
	authRates  map[string]authRateEntry

	image2Dir         string
	image2InitErr     error
	image2Client      *http.Client
	image2ProxyClient *http.Client
	image2URLCipher   cipher.AEAD
	image2Stop        chan struct{}
	image2Done        chan struct{}
	closeOnce         sync.Once
}

type authRateEntry struct {
	count   int
	resetAt time.Time
}

// NewServer 创建 API 服务。assets 用于托管内嵌前端，可为 nil。
func NewServer(st *store.Store, client *upstream.Client, eng *engine.Engine, assets http.Handler) *Server {
	image2Transport := http.DefaultTransport.(*http.Transport).Clone()
	image2ProxyTransport := image2Transport.Clone()
	image2Transport.DialContext = (&net.Dialer{}).DialContext
	image2Transport.TLSHandshakeTimeout = 0
	image2ProxyTransport.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	image2Dir := filepath.Join(st.DataDir(), "image2", "images")
	s := &Server{
		store:            st,
		client:           client,
		engine:           eng,
		hub:              newHub(),
		assets:           assets,
		lastRenew:        make(map[string]time.Time, 64),
		authRates:        make(map[string]authRateEntry, 64),
		upstreamChannels: channelmanager.New(st),
		scheduledReports: reports.New(st, client),
		image2Dir:        image2Dir,
		image2Client: &http.Client{
			Transport:     image2Transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		image2ProxyClient: &http.Client{
			Timeout:   3 * time.Minute,
			Transport: image2ProxyTransport,
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if len(via) >= 5 || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
					return errors.New("invalid image redirect")
				}
				return nil
			},
		},
		image2URLCipher: newImage2URLCipher(os.Getenv(image2URLKeyEnv)),
		image2Stop:      make(chan struct{}),
		image2Done:      make(chan struct{}),
	}
	s.image2InitErr = os.MkdirAll(image2Dir, 0o700)
	if s.image2InitErr != nil {
		log.Printf("创建 image2 图片目录失败: %v", s.image2InitErr)
		close(s.image2Done)
	} else {
		if removed, err := s.cleanupImage2Files(); err != nil {
			log.Printf("清理 image2 图片失败: %v", err)
		} else if removed > 0 {
			log.Printf("已清理 %d 个过期 image2 图片", removed)
		}
		go s.image2CleanupLoop()
	}
	eng.SetNotifier(s.hub.broadcast)
	return s
}

// StartBackgroundJobs 只由主程序显式调用，避免 API 测试构造时启动定时任务。
func (s *Server) StartBackgroundJobs() {
	s.upstreamChannels.Start()
	s.scheduledReports.Start()
}

// Close 释放 SSE 资源。
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.scheduledReports.Stop()
		s.upstreamChannels.Stop()
		close(s.image2Stop)
		<-s.image2Done
		s.hub.close()
	})
}

// protectedRoutes 是需要登录才能访问的全部接口。
//
// 用表驱动而不是逐条 HandleFunc，是为了让「有没有漏挂鉴权」变成可测的事实：
// 测试会遍历这张表逐条验证未登录返回 401（见 auth_test.go）。
// 新增接口时往表里加一行，漏加会被测试抓住。
func (s *Server) protectedRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /api/overview":                                       s.overview,
		"GET /api/groups":                                         s.listGroups,
		"PUT /api/groups/{id}/policy":                             s.saveGroupPolicy,
		"DELETE /api/groups/{id}/policy":                          s.deleteGroupPolicy,
		"POST /api/groups/{id}/exclude":                           s.excludeGroup,
		"GET /api/channels":                                       s.listChannels,
		"GET /api/channels/{id}":                                  s.getChannel,
		"PUT /api/channels/{id}":                                  s.updateChannel,
		"POST /api/channels/{id}/sync-upstream-multiplier":        s.syncChannelUpstreamMultiplier,
		"POST /api/channels/{id}/probe":                           s.probeChannel,
		"POST /api/channels/{id}/fuse":                            s.fuseChannel,
		"POST /api/channels/{id}/recover":                         s.recoverChannel,
		"POST /api/channels/{id}/exclude":                         s.excludeChannel,
		"POST /api/channels/{id}/pause":                           s.pauseChannel,
		"GET /api/channels/{id}/models":                           s.channelModels,
		"PUT /api/channels/{id}/test-model":                       s.setChannelTestModel,
		"GET /api/policy":                                         s.getPolicy,
		"PUT /api/policy":                                         s.savePolicy,
		"GET /api/connection":                                     s.getConnection,
		"PUT /api/connection":                                     s.saveConnection,
		"POST /api/sync":                                          s.sync,
		"POST /api/run-once":                                      s.runOnce,
		"POST /api/cancel":                                        s.cancelRun,
		"POST /api/resume":                                        s.resumeRun,
		"POST /api/restore-all":                                   s.restoreAll,
		"GET /api/events":                                         s.listEvents,
		"GET /api/actions":                                        s.listActions,
		"GET /api/image2":                                         s.getImage2,
		"PUT /api/image2/settings":                                s.saveImage2Settings,
		"POST /api/image2/upstreams":                              s.createImage2Upstream,
		"PUT /api/image2/upstreams/{id}":                          s.updateImage2Upstream,
		"DELETE /api/image2/upstreams/{id}":                       s.deleteImage2Upstream,
		"GET /api/memos":                                          s.listMemos,
		"POST /api/memos":                                         s.createMemo,
		"GET /api/memos/{id}":                                     s.getMemo,
		"PUT /api/memos/{id}":                                     s.updateMemo,
		"DELETE /api/memos/{id}":                                  s.deleteMemo,
		"GET /api/memos/{id}/archives":                            s.listMemoArchives,
		"POST /api/memos/{id}/archives/{archiveId}/restore":       s.restoreMemoArchive,
		"GET /api/upstream-channels":                              s.upstreamNoStore(s.listUpstreamChannels),
		"POST /api/upstream-channels":                             s.upstreamNoStore(s.createUpstreamChannel),
		"POST /api/upstream-channels/sync":                        s.upstreamNoStore(s.syncAllUpstreamChannels),
		"GET /api/upstream-channels/{id}":                         s.upstreamNoStore(s.getUpstreamChannel),
		"PUT /api/upstream-channels/{id}":                         s.upstreamNoStore(s.updateUpstreamChannel),
		"DELETE /api/upstream-channels/{id}":                      s.upstreamNoStore(s.deleteUpstreamChannel),
		"POST /api/upstream-channels/{id}/sync":                   s.upstreamNoStore(s.syncUpstreamChannel),
		"GET /api/upstream-channels/{id}/login":                   s.upstreamNoStore(s.loginUpstreamChannel),
		"GET /api/upstream-channels/{id}/upstream-login":          s.upstreamNoStore(s.loginUpstreamChannel),
		"GET /api/upstream-channels/{id}/overview":                s.upstreamNoStore(s.upstreamChannelOverview),
		"GET /api/upstream-channels/{id}/groups":                  s.upstreamNoStore(s.upstreamChannelGroups),
		"GET /api/upstream-channels/{id}/tokens":                  s.upstreamNoStore(s.upstreamChannelTokens),
		"GET /api/upstream-channels/{id}/subscriptions":           s.upstreamNoStore(s.upstreamChannelSubscriptions),
		"GET /api/upstream-channels/{id}/balance-history":         s.upstreamNoStore(s.upstreamBalanceHistory),
		"GET /api/upstream-channels/{id}/balance-query-logs":      s.upstreamNoStore(s.upstreamBalanceLogs),
		"GET /api/upstream-channels/{id}/tokens/{tokenId}/models": s.upstreamNoStore(s.upstreamTokenModels),
		"PUT /api/upstream-channels/{id}/tokens/{tokenId}/group":  s.upstreamNoStore(s.updateUpstreamTokenGroup),
		"GET /api/upstream-channels/{id}/tasks":                   s.upstreamNoStore(s.listUpstreamTasks),
		"POST /api/upstream-channels/{id}/tasks":                  s.upstreamNoStore(s.createUpstreamTask),
		"PUT /api/upstream-channels/{id}/tasks/{taskId}":          s.upstreamNoStore(s.updateUpstreamTask),
		"DELETE /api/upstream-channels/{id}/tasks/{taskId}":       s.upstreamNoStore(s.deleteUpstreamTask),
		"GET /api/upstream-channels/{id}/balance-logs":            s.upstreamNoStore(s.upstreamBalanceLogs),
		"GET /api/upstream-channels/{id}/alerts":                  s.upstreamNoStore(s.upstreamAlerts),
		"GET /api/upstream-email-settings":                        s.upstreamNoStore(s.getUpstreamEmailSettings),
		"PUT /api/upstream-email-settings":                        s.upstreamNoStore(s.saveUpstreamEmailSettings),
		"POST /api/upstream-email-settings/test":                  s.upstreamNoStore(s.testUpstreamEmailSettings),
		"GET /api/upstream-wecom-settings":                        s.upstreamNoStore(s.getUpstreamWeComSettings),
		"PUT /api/upstream-wecom-settings":                        s.upstreamNoStore(s.saveUpstreamWeComSettings),
		"POST /api/upstream-wecom-settings/test":                  s.upstreamNoStore(s.testUpstreamWeComSettings),
		"GET /api/reports/notifications":                          s.upstreamNoStore(s.getReportNotifications),
		"PUT /api/reports/notifications":                          s.upstreamNoStore(s.saveReportNotifications),
		"POST /api/reports/notifications/wecom/test":              s.upstreamNoStore(s.testReportNotificationsWeCom),
		"GET /api/reports/channel-usage":                          s.upstreamNoStore(s.getChannelUsageReport),
		"PUT /api/reports/channel-usage":                          s.upstreamNoStore(s.saveChannelUsageReport),
		"GET /api/reports/channel-usage/runs":                     s.upstreamNoStore(s.channelUsageReportRuns),
		"POST /api/reports/channel-usage/run":                     s.upstreamNoStore(s.runChannelUsageReport),
		"POST /api/reports/channel-usage/wecom/test":              s.upstreamNoStore(s.testReportNotificationsWeCom),
		"GET /api/stream":                                         s.stream,
		"GET /api/me":                                             s.me,
		"PUT /api/account":                                        s.updateAccount,
		"POST /api/logout":                                        s.logout,
	}
}

// Handler 返回带路由与中间件的处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 公开接口：探活，以及登录前必须能访问的初始化与登录本身。
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/setup", s.setupStatus)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /images/from/{name}", s.serveImage2URL)
	mux.HandleFunc("GET /images/{name}", s.serveImage2File)
	mux.HandleFunc("GET /files/{name}", s.serveImage2File)
	mux.HandleFunc("POST /{slug}/v1/images/generations", s.generateImage2)
	mux.HandleFunc("POST /{slug}/v1/images/edits", s.editImage2)

	for pattern, handler := range s.protectedRoutes() {
		mux.HandleFunc(pattern, s.requireAuth(handler))
	}

	if s.assets != nil {
		mux.Handle("/", s.assets)
	}
	return hardenHTTP(cors(mux))
}

// health 只回一个存活标记。
//
// 早期版本还带上 engine.Status()，那会把运行状态、上次调度结果和错误信息
// 泄露给任何未鉴权的调用方 —— 探活接口不需要这些。
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// devOrigins 是开发期允许跨源访问的前端地址。
//
// 生产环境下前端由二进制内嵌托管，本来就同源、不需要 CORS；
// 这里只为「前端跑 vite dev、直连后端」这种场景留条路。
var devOrigins = map[string]bool{
	"http://127.0.0.1:5177": true,
	"http://localhost:5177": true,
}

// hardenHTTP 给 API 与静态页面统一加上边界保护。Origin 只用于阻止浏览器
// 发起的跨站写请求；没有 Origin 的 CLI、systemd 探活和同源反代不受影响。
func hardenHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")

		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && (isStateChanging(r.Method) || r.Method == http.MethodOptions) && !allowedOrigin(origin, r) {
			writeErrorMessage(w, http.StatusForbidden, "拒绝跨站请求")
			return
		}

		image2ProxyRequest := isImage2ProxyRequest(r)
		if r.Body != nil && !image2ProxyRequest {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}
		if !image2ProxyRequest && isStateChanging(r.Method) && requestHasBody(r) {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || !strings.EqualFold(mediaType, "application/json") {
				writeErrorMessage(w, http.StatusUnsupportedMediaType, "请求正文必须使用 application/json")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func isImage2ProxyRequest(r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return r.Method == http.MethodPost && len(parts) == 4 && parts[0] != "" &&
		parts[1] == "v1" && parts[2] == "images" &&
		(parts[3] == "generations" || parts[3] == "edits")
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func requestHasBody(r *http.Request) bool {
	return r.ContentLength != 0 || len(r.TransferEncoding) > 0
}

func allowedOrigin(origin string, r *http.Request) bool {
	if devOrigins[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// cors 按白名单回显 Origin。
//
// 不能再用 Access-Control-Allow-Origin: *：会话走 Cookie，
// 而浏览器规范禁止通配 Origin 携带凭据，通配符会让带 Cookie 的跨源请求直接失败。
// 更要紧的是，通配 + 凭据本身就是把接口暴露给任意站点的组合。
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && devOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			// 响应随 Origin 变化，必须告诉缓存别把某一个源的响应复用给别人。
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, engine.ErrAlreadyRunning):
		status = http.StatusConflict
	case errors.Is(err, engine.ErrAccountNotManaged):
		status = http.StatusNotFound
	case errors.Is(err, upstream.ErrNotConfigured):
		status = http.StatusPreconditionFailed
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
}

func decodeBody(r *http.Request, out any) error {
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("请求体只能包含一个 JSON 值")
		}
		return err
	}
	return nil
}

func queryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil {
		return fallback
	}
	return value
}

func queryInt64Ptr(r *http.Request, key string) *int64 {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}
