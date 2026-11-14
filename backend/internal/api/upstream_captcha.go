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
	writeJSON(w, http.StatusOK, map[string]any{"enabled": settings.Enabled, "has_client_key": settings.ClientKey != ""})
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
	}
	if err := decodeBody(r, &payload); err != nil {
		writeUpstreamError(w, err)
		return
	}
	if payload.Enabled != nil {
		settings.Enabled = *payload.Enabled
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
