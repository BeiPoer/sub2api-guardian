package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/auth"
	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/store"
)

type multiplierSourceConfigDTO struct {
	Mode             store.MultiplierSourceMode    `json:"mode"`
	BaseURL          string                        `json:"base_url"`
	Username         string                        `json:"username"`
	TimeoutSeconds   int                           `json:"timeout_seconds"`
	HasAuthorization bool                          `json:"has_authorization"`
	SourceID         string                        `json:"source_id"`
	LastRevision     string                        `json:"last_revision"`
	LastState        string                        `json:"last_state"`
	LastComplete     bool                          `json:"last_complete"`
	LastSuccessAt    string                        `json:"last_success_at,omitempty"`
	LastError        string                        `json:"last_error,omitempty"`
	LastMatched      int                           `json:"last_matched"`
	LastTotal        int                           `json:"last_total"`
	LocalStatus      engine.MultiplierSourceStatus `json:"local_status"`
}

type multiplierSourceConfigPayload struct {
	Mode           *store.MultiplierSourceMode `json:"mode"`
	BaseURL        *string                     `json:"base_url"`
	Username       *string                     `json:"username"`
	TimeoutSeconds *int                        `json:"timeout_seconds"`
}

type multiplierSourceAuthorizePayload struct {
	BaseURL        string `json:"base_url"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type multiplierSourceAuthorizeResponse struct {
	Config    multiplierSourceConfigDTO          `json:"config"`
	Sync      *engine.RemoteMultiplierSyncResult `json:"sync,omitempty"`
	SyncError string                             `json:"sync_error,omitempty"`
}

func multiplierSourceConfigView(settings store.MultiplierSourceSettings, local engine.MultiplierSourceStatus) multiplierSourceConfigDTO {
	return multiplierSourceConfigDTO{
		Mode: settings.Mode, BaseURL: settings.BaseURL, Username: settings.Username,
		TimeoutSeconds: settings.TimeoutSeconds, HasAuthorization: settings.AccessToken != "",
		SourceID: settings.SourceID, LastRevision: settings.LastRevision,
		LastState: settings.LastState, LastComplete: settings.LastComplete,
		LastSuccessAt: settings.LastSuccessAt, LastError: settings.LastError,
		LastMatched: settings.LastMatched, LastTotal: settings.LastTotal,
		LocalStatus: local,
	}
}

func (s *Server) multiplierSourceConfig(w http.ResponseWriter, _ *http.Request) {
	settings, _, err := s.store.MultiplierSourceSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	local, localErr := s.engine.MultiplierSourceStatus()
	if localErr != nil {
		writeError(w, localErr)
		return
	}
	writeJSON(w, http.StatusOK, multiplierSourceConfigView(settings, local))
}

func (s *Server) saveMultiplierSourceConfig(w http.ResponseWriter, r *http.Request) {
	var payload multiplierSourceConfigPayload
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if _, err := s.engine.UpdateMultiplierSourceSettings(ctx, func(current *store.MultiplierSourceSettings) (bool, error) {
		originalMode, originalURL, originalUsername := current.Mode, current.BaseURL, current.Username
		hadRemoteOwnership := len(current.RemoteAccounts) > 0
		if payload.Mode != nil {
			if *payload.Mode != store.MultiplierSourceLocal && *payload.Mode != store.MultiplierSourceRemote {
				return false, errors.New("倍率源模式无效")
			}
			current.Mode = *payload.Mode
		}
		// 本机模式不使用远程地址；切回本机时不要因为表单里残留的
		// G1 字段格式异常而阻止模式切换。
		if payload.BaseURL != nil && current.Mode == store.MultiplierSourceRemote {
			if strings.TrimSpace(*payload.BaseURL) != "" {
				normalized, normalizeErr := engine.NormalizeRemoteMultiplierURL(*payload.BaseURL)
				if normalizeErr != nil {
					return false, normalizeErr
				}
				current.BaseURL = normalized
			} else {
				current.BaseURL = ""
			}
		}
		if payload.Username != nil {
			current.Username = strings.TrimSpace(*payload.Username)
		}
		if payload.TimeoutSeconds != nil {
			current.TimeoutSeconds = *payload.TimeoutSeconds
		}
		identityChanged := current.Mode == store.MultiplierSourceRemote &&
			(originalMode != current.Mode || originalURL != current.BaseURL || originalUsername != current.Username)
		reset := func() {
			current.AccessToken = ""
			current.SourceID = ""
			current.LastRevision = ""
			current.LastState = ""
			current.LastComplete = false
			current.LastSuccessAt = ""
			current.LastError = ""
			current.LastMatched = 0
			current.LastTotal = 0
			current.RemoteAccounts = map[string]store.RemoteLinkedAccount{}
		}
		if identityChanged || current.Mode == store.MultiplierSourceLocal {
			reset()
		}
		return (originalMode == store.MultiplierSourceRemote || hadRemoteOwnership) &&
			(current.Mode == store.MultiplierSourceLocal || identityChanged), nil
	}); err != nil {
		writeError(w, err)
		return
	}
	s.multiplierSourceConfig(w, r)
}

// authorizeMultiplierSource 由 G2 调用本地接口，使用 G1 现有网站密码换取窄权限令牌。
func (s *Server) authorizeMultiplierSource(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !s.consumeAuthAttempt(w, r, "multiplier-source-authorize", 5) {
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
	if err != nil || !auth.VerifyPassword(user.PasswordHash, payload.Password) {
		writeErrorMessage(w, http.StatusUnauthorized, "用户名或密码不正确")
		return
	}
	status, statusErr := s.engine.MultiplierSourceStatus()
	if statusErr != nil {
		writeError(w, statusErr)
		return
	}
	token, err := auth.NewSessionToken()
	if err != nil {
		writeError(w, err)
		return
	}
	expires := time.Now().Add(engine.MultiplierSourceSessionTTL)
	if err := s.store.CreateScopedSession(token, user.ID, expires, r.UserAgent(), engine.MultiplierSourceScope); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token, "source_id": status.SourceID,
		"expires_at": expires.UTC().Format(time.RFC3339Nano),
		"status":     status,
	})
	s.resetAuthAttempts(r, "multiplier-source-authorize")
}

func multiplierBearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	prefix, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(prefix, "bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func (s *Server) multiplierSourceUser(r *http.Request) bool {
	token := multiplierBearerToken(r)
	if token == "" {
		return false
	}
	_, err := s.store.SessionUserWithScope(token, engine.MultiplierSourceScope)
	return err == nil
}

func (s *Server) multiplierSourceStatus(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !s.multiplierSourceUser(r) {
		writeErrorMessage(w, http.StatusUnauthorized, "倍率源授权无效")
		return
	}
	status, err := s.engine.MultiplierSourceStatus()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) multiplierSourceResolve(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	token := multiplierBearerToken(r)
	if token == "" {
		writeErrorMessage(w, http.StatusUnauthorized, "倍率源授权无效")
		return
	}
	if _, err := s.store.SessionUserWithScope(token, engine.MultiplierSourceScope); err != nil {
		writeErrorMessage(w, http.StatusUnauthorized, "倍率源授权无效")
		return
	}
	var payload engine.MultiplierSourceResolveRequest
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}
	if payload.Protocol != engine.MultiplierSourceProtocol {
		writeErrorMessage(w, http.StatusBadRequest, "倍率源协议版本不兼容")
		return
	}
	result, err := s.engine.ResolveMultiplierSource(token, payload.Fingerprints)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) authorizeRemoteMultiplierSource(w http.ResponseWriter, r *http.Request) {
	var payload multiplierSourceAuthorizePayload
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(payload.Username) == "" || payload.Password == "" {
		writeErrorMessage(w, http.StatusBadRequest, "G1 用户名和密码不能为空")
		return
	}
	baseURL, err := engine.NormalizeRemoteMultiplierURL(payload.BaseURL)
	if err != nil {
		writeError(w, err)
		return
	}
	timeoutSeconds := payload.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 10
	}
	if timeoutSeconds > 120 {
		timeoutSeconds = 120
	}
	authResult, err := engine.AuthorizeRemoteMultiplierSource(
		r.Context(), baseURL, payload.Username, payload.Password,
		time.Duration(timeoutSeconds)*time.Second,
	)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	saveCtx, saveCancel := context.WithTimeout(r.Context(), 3*time.Minute)
	_, err = s.engine.UpdateMultiplierSourceSettings(saveCtx, func(settings *store.MultiplierSourceSettings) (bool, error) {
		detach := settings.Mode == store.MultiplierSourceRemote || len(settings.RemoteAccounts) > 0
		settings.Mode = store.MultiplierSourceRemote
		settings.BaseURL = baseURL
		settings.Username = strings.TrimSpace(payload.Username)
		settings.AccessToken = authResult.AccessToken
		settings.SourceID = authResult.SourceID
		settings.LastRevision = ""
		settings.LastState = "authorized"
		settings.LastComplete = false
		settings.LastSuccessAt = ""
		settings.LastError = ""
		settings.LastMatched = 0
		settings.LastTotal = 0
		settings.RemoteAccounts = map[string]store.RemoteLinkedAccount{}
		settings.TimeoutSeconds = timeoutSeconds
		return detach, nil
	})
	saveCancel()
	if err != nil {
		writeError(w, err)
		return
	}

	response := multiplierSourceAuthorizeResponse{}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if result, syncErr := s.engine.SyncConfiguredMultiplierSource(ctx, true); syncErr != nil {
		response.SyncError = syncErr.Error()
	} else {
		response.Sync = &result
	}
	latest, _, _ := s.store.MultiplierSourceSettings()
	local, _ := s.engine.MultiplierSourceStatus()
	response.Config = multiplierSourceConfigView(latest, local)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) testRemoteMultiplierSource(w http.ResponseWriter, r *http.Request) {
	requested, _, err := s.store.MultiplierSourceSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	if requested.Mode != store.MultiplierSourceRemote || requested.AccessToken == "" {
		writeErrorMessage(w, http.StatusPreconditionFailed, "请先配置并授权 G1 倍率源")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(requested.TimeoutSeconds)*time.Second)
	defer cancel()
	status, err := engine.FetchRemoteMultiplierSourceStatus(ctx, requested)
	if err != nil {
		_, _ = s.engine.UpdateMultiplierSourceSettings(ctx, func(current *store.MultiplierSourceSettings) (bool, error) {
			if !sameRemoteMultiplierSource(current, requested) {
				return false, nil
			}
			current.LastState = "error"
			current.LastError = err.Error()
			return false, nil
		})
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	if _, err := s.engine.UpdateMultiplierSourceSettings(ctx, func(current *store.MultiplierSourceSettings) (bool, error) {
		if !sameRemoteMultiplierSource(current, requested) {
			return false, errors.New("倍率源配置已变化，请刷新后重试")
		}
		current.SourceID = status.SourceID
		current.LastRevision = status.Revision
		current.LastState = status.State
		current.LastComplete = status.Complete
		current.LastError = ""
		return false, nil
	}); err != nil {
		writeError(w, err)
		return
	}
	local, _ := s.engine.MultiplierSourceStatus()
	latest, _, _ := s.store.MultiplierSourceSettings()
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "config": multiplierSourceConfigView(latest, local)})
}

func sameRemoteMultiplierSource(a *store.MultiplierSourceSettings, b store.MultiplierSourceSettings) bool {
	return a.Mode == store.MultiplierSourceRemote && b.Mode == store.MultiplierSourceRemote &&
		a.BaseURL == b.BaseURL && a.Username == b.Username && a.AccessToken == b.AccessToken
}

func (s *Server) syncRemoteMultiplierSource(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	result, err := s.engine.SyncConfiguredMultiplierSource(ctx, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "result": result})
		return
	}
	settings, _, _ := s.store.MultiplierSourceSettings()
	local, _ := s.engine.MultiplierSourceStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"result": result, "config": multiplierSourceConfigView(settings, local),
	})
}

func (s *Server) clearRemoteMultiplierSource(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if _, err := s.engine.UpdateMultiplierSourceSettings(ctx, func(settings *store.MultiplierSourceSettings) (bool, error) {
		wasRemote := settings.Mode == store.MultiplierSourceRemote || len(settings.RemoteAccounts) > 0
		settings.Mode = store.MultiplierSourceLocal
		settings.AccessToken = ""
		settings.SourceID = ""
		settings.LastRevision = ""
		settings.LastState = ""
		settings.LastComplete = false
		settings.LastSuccessAt = ""
		settings.LastError = ""
		settings.LastMatched = 0
		settings.LastTotal = 0
		settings.RemoteAccounts = map[string]store.RemoteLinkedAccount{}
		return wasRemote, nil
	}); err != nil {
		writeError(w, err)
		return
	}
	s.multiplierSourceConfig(w, r)
}
