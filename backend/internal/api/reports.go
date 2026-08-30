package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"sub2api-guardian/backend/internal/reports"
	"sub2api-guardian/backend/internal/upstream"
	"sub2api-guardian/backend/internal/wecom"
)

func (s *Server) getReportNotifications(w http.ResponseWriter, _ *http.Request) {
	config, err := s.scheduledReports.NotificationSettings()
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (s *Server) saveReportNotifications(w http.ResponseWriter, r *http.Request) {
	var payload reports.NotificationSaveInput
	if err := decodeBody(r, &payload); err != nil {
		writeReportError(w, err)
		return
	}
	config, err := s.scheduledReports.SaveNotificationSettings(payload)
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (s *Server) testReportNotificationsWeCom(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	messageID, err := s.scheduledReports.TestNotification(ctx)
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message_id": messageID})
}

func (s *Server) getChannelUsageReport(w http.ResponseWriter, _ *http.Request) {
	view, err := s.scheduledReports.View()
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) saveChannelUsageReport(w http.ResponseWriter, r *http.Request) {
	var payload reports.SaveInput
	if err := decodeBody(r, &payload); err != nil {
		writeReportError(w, err)
		return
	}
	view, err := s.scheduledReports.Save(payload)
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) channelUsageReportRuns(w http.ResponseWriter, r *http.Request) {
	items, total, page, pageSize, pages, err := s.scheduledReports.Runs(queryInt(r, "page", 1), queryInt(r, "page_size", 20))
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": pageSize, "pages": pages,
	})
}

func (s *Server) runChannelUsageReport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	run, err := s.scheduledReports.RunNow(ctx)
	if err != nil {
		writeReportError(w, err)
		return
	}
	if run.Status == "error" && run.Error == upstream.ErrNotConfigured.Error() {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"error": run.Error, "run": run})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *Server) getDailyReport(w http.ResponseWriter, _ *http.Request) {
	view, err := s.scheduledReports.DailyView()
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) saveDailyReport(w http.ResponseWriter, r *http.Request) {
	var payload reports.DailySaveInput
	if err := decodeBody(r, &payload); err != nil {
		writeReportError(w, err)
		return
	}
	view, err := s.scheduledReports.SaveDaily(payload)
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) dailyReportRuns(w http.ResponseWriter, r *http.Request) {
	items, total, page, pageSize, pages, err := s.scheduledReports.DailyRuns(queryInt(r, "page", 1), queryInt(r, "page_size", 20))
	if err != nil {
		writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": pageSize, "pages": pages,
	})
}

func (s *Server) runDailyReport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	run, err := s.scheduledReports.RunDailyNow(ctx)
	if err != nil {
		writeReportError(w, err)
		return
	}
	if run.Status == "error" && run.Error == upstream.ErrNotConfigured.Error() {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"error": run.Error, "run": run})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func writeReportError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var reportErr *reports.Error
	var wecomErr *wecom.APIError
	switch {
	case errors.As(err, &reportErr) && reportErr.Status > 0:
		status = reportErr.Status
	case errors.Is(err, reports.ErrAlreadyRunning):
		status = http.StatusConflict
	case errors.Is(err, upstream.ErrNotConfigured):
		status = http.StatusPreconditionFailed
	case errors.As(err, &wecomErr):
		status = http.StatusBadGateway
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	case upstream.StatusCodeOf(err) != 0:
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
