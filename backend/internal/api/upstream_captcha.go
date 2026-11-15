package api

import (
	"net/http"
	"strings"
)

func (s *Server) getUpstreamCaptchaSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.UpstreamCaptchaSettings()
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": settings.Enabled, "has_client_key": settings.ClientKey != "", "soft_id": settings.SoftID})
}

func (s *Server) saveUpstreamCaptchaSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.UpstreamCaptchaSettings()
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	var payload struct {
		Enabled   *bool  `json:"enabled"`
		ClientKey string `json:"client_key"`
		SoftID    *int64 `json:"soft_id"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "设置格式无效，Soft ID 必须为整数"})
		return
	}
	if payload.Enabled != nil {
		settings.Enabled = *payload.Enabled
	}
	if payload.SoftID != nil {
		if *payload.SoftID < 0 || *payload.SoftID > 9007199254740991 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Soft ID 必须为 0 到 9007199254740991 之间的整数（0 表示不传）"})
			return
		}
		settings.SoftID = *payload.SoftID
	}
	if key := strings.TrimSpace(payload.ClientKey); key != "" {
		settings.ClientKey = key
	}
	if settings.Enabled && settings.ClientKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "启用 YesCaptcha 前请填写 Client Key"})
		return
	}
	if err := s.store.SaveUpstreamCaptchaSettings(settings); err != nil {
		writeUpstreamError(w, err)
		return
	}
	s.getUpstreamCaptchaSettings(w, r)
}
