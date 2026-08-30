package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ScheduledReportType identifies a report definition. The type is deliberately
// validated in Go so adding a future report does not require an SQLite CHECK migration.
type ScheduledReportType string

const (
	ScheduledReportChannelUsage ScheduledReportType = "channel_usage"
	ScheduledReportDaily        ScheduledReportType = "daily"
)

func (t ScheduledReportType) Valid() bool {
	return t == ScheduledReportChannelUsage || t == ScheduledReportDaily
}

type ScheduledReport struct {
	ID              int64               `json:"id"`
	Type            ScheduledReportType `json:"type"`
	Enabled         bool                `json:"enabled"`
	IntervalMinutes int                 `json:"interval_minutes"`
	StartHour       int                 `json:"start_hour"`
	EndHour         int                 `json:"end_hour"`
	Timezone        string              `json:"timezone"`
	ConfigJSON      string              `json:"-"`
	LastRunAt       string              `json:"last_run_at"`
	LastStatus      string              `json:"last_status"`
	LastError       string              `json:"last_error"`
	NextRunAt       string              `json:"next_run_at"`
	CreatedAt       string              `json:"created_at"`
	UpdatedAt       string              `json:"updated_at"`
}

type ScheduledReportRun struct {
	ID                 int64  `json:"id"`
	ReportID           int64  `json:"report_id"`
	Status             string `json:"status"`
	StartedAt          string `json:"started_at"`
	FinishedAt         string `json:"finished_at"`
	WindowStart        string `json:"window_start"`
	WindowEnd          string `json:"window_end"`
	TotalRecords       int    `json:"total_records"`
	HighLatencyCount   int    `json:"high_latency_count"`
	NotificationStatus string `json:"notification_status"`
	NotificationError  string `json:"notification_error"`
	Error              string `json:"error"`
	Summary            any    `json:"summary,omitempty"`
	Message            string `json:"message"`
	SummaryJSON        string `json:"-"`
}

var ErrScheduledReportNotFound = errors.New("定时报告不存在")

func scanScheduledReport(row interface{ Scan(...any) error }) (ScheduledReport, error) {
	var report ScheduledReport
	var enabled int
	var lastRunAt, nextRunAt sql.NullString
	if err := row.Scan(
		&report.ID, &report.Type, &enabled, &report.IntervalMinutes,
		&report.StartHour, &report.EndHour, &report.Timezone, &report.ConfigJSON,
		&lastRunAt, &report.LastStatus, &report.LastError, &nextRunAt,
		&report.CreatedAt, &report.UpdatedAt,
	); err != nil {
		return ScheduledReport{}, err
	}
	report.Enabled = enabled != 0
	if lastRunAt.Valid {
		report.LastRunAt = lastRunAt.String
	}
	if nextRunAt.Valid {
		report.NextRunAt = nextRunAt.String
	}
	return report, nil
}

const scheduledReportColumns = `id, type, enabled, interval_minutes, start_hour, end_hour,
	timezone, config_json, last_run_at, last_status, last_error, next_run_at, created_at, updated_at`

func (s *Store) ScheduledReport(reportType ScheduledReportType) (ScheduledReport, bool, error) {
	report, err := scanScheduledReport(s.db.QueryRow(
		`SELECT `+scheduledReportColumns+` FROM scheduled_reports WHERE type = ?`, string(reportType),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ScheduledReport{}, false, nil
	}
	return report, err == nil, err
}

// SaveScheduledReportConfig updates only configuration fields and preserves runtime state.
func (s *Store) SaveScheduledReportConfig(report ScheduledReport) (ScheduledReport, error) {
	if !report.Type.Valid() {
		return ScheduledReport{}, fmt.Errorf("定时报告类型无效: %s", report.Type)
	}
	if report.IntervalMinutes <= 0 || report.StartHour < 0 || report.StartHour > 23 ||
		report.EndHour < 0 || report.EndHour > 23 || report.StartHour > report.EndHour {
		return ScheduledReport{}, errors.New("定时报告调度参数无效")
	}
	if report.Timezone == "" {
		return ScheduledReport{}, errors.New("定时报告时区不能为空")
	}
	if _, err := time.LoadLocation(report.Timezone); err != nil {
		return ScheduledReport{}, errors.New("定时报告时区无效")
	}
	if !json.Valid([]byte(report.ConfigJSON)) {
		return ScheduledReport{}, errors.New("定时报告配置不是有效 JSON")
	}

	now := nowString()
	s.mu.Lock()
	_, err := s.db.Exec(`INSERT INTO scheduled_reports(
		type, enabled, interval_minutes, start_hour, end_hour, timezone, config_json,
		last_status, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, 'never', ?, ?)
	ON CONFLICT(type) DO UPDATE SET
		enabled = excluded.enabled,
		interval_minutes = excluded.interval_minutes,
		start_hour = excluded.start_hour,
		end_hour = excluded.end_hour,
		timezone = excluded.timezone,
		config_json = excluded.config_json,
		updated_at = excluded.updated_at`,
		string(report.Type), boolInt(report.Enabled), report.IntervalMinutes, report.StartHour,
		report.EndHour, report.Timezone, report.ConfigJSON, now, now)
	s.mu.Unlock()
	if err != nil {
		return ScheduledReport{}, err
	}
	saved, exists, err := s.ScheduledReport(report.Type)
	if err != nil {
		return ScheduledReport{}, err
	}
	if !exists {
		return ScheduledReport{}, ErrScheduledReportNotFound
	}
	return saved, nil
}

func (s *Store) UpdateScheduledReportRunState(reportID int64, lastRunAt, status, lastError, nextRunAt string) error {
	s.mu.Lock()
	result, err := s.db.Exec(`UPDATE scheduled_reports SET last_run_at = ?, last_status = ?,
		last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		nullableString(lastRunAt), status, truncate(lastError, 4000), nullableString(nextRunAt), nowString(), reportID)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrScheduledReportNotFound
	}
	return nil
}

func (s *Store) AddScheduledReportRun(run ScheduledReportRun) (ScheduledReportRun, error) {
	if run.ReportID <= 0 {
		return ScheduledReportRun{}, errors.New("定时报告 ID 无效")
	}
	if run.Status != "ok" && run.Status != "alert" && run.Status != "error" {
		return ScheduledReportRun{}, errors.New("定时报告运行状态无效")
	}
	if run.NotificationStatus == "" {
		run.NotificationStatus = "not_needed"
	}
	switch run.NotificationStatus {
	case "not_needed", "disabled", "sent", "failed":
	default:
		return ScheduledReportRun{}, errors.New("定时报告通知状态无效")
	}
	if run.StartedAt == "" {
		run.StartedAt = nowString()
	}
	if run.FinishedAt == "" {
		run.FinishedAt = run.StartedAt
	}
	raw, err := marshalJSON(run.Summary)
	if err != nil {
		return ScheduledReportRun{}, err
	}
	run.SummaryJSON = raw

	s.mu.Lock()
	result, err := s.db.Exec(`INSERT INTO scheduled_report_runs(
		report_id, status, started_at, finished_at, window_start, window_end,
		total_records, high_latency_count, notification_status, notification_error,
		error, summary_json, message
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ReportID, run.Status, run.StartedAt, run.FinishedAt, run.WindowStart, run.WindowEnd,
		run.TotalRecords, run.HighLatencyCount, run.NotificationStatus,
		truncate(run.NotificationError, 4000), truncate(run.Error, 4000), raw, truncate(run.Message, 8000))
	s.mu.Unlock()
	if err != nil {
		return ScheduledReportRun{}, err
	}
	run.ID, _ = result.LastInsertId()
	return run, nil
}

func (s *Store) ScheduledReportRuns(reportID int64, page, pageSize int) ([]ScheduledReportRun, int64, int, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM scheduled_report_runs WHERE report_id = ? AND started_at >= ?`, reportID, cutoff).Scan(&total); err != nil {
		return nil, 0, 0, 0, err
	}
	offset := (page - 1) * pageSize
	rows, err := s.db.Query(`SELECT id, report_id, status, started_at, finished_at, window_start, window_end,
		total_records, high_latency_count, notification_status, notification_error, error, summary_json, message
		FROM scheduled_report_runs WHERE report_id = ? AND started_at >= ? ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`,
		reportID, cutoff, pageSize, offset)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer rows.Close()
	items := make([]ScheduledReportRun, 0)
	for rows.Next() {
		var item ScheduledReportRun
		if err := rows.Scan(&item.ID, &item.ReportID, &item.Status, &item.StartedAt, &item.FinishedAt,
			&item.WindowStart, &item.WindowEnd, &item.TotalRecords, &item.HighLatencyCount,
			&item.NotificationStatus, &item.NotificationError, &item.Error, &item.SummaryJSON, &item.Message); err != nil {
			return nil, 0, 0, 0, err
		}
		if item.SummaryJSON != "" {
			if err := json.Unmarshal([]byte(item.SummaryJSON), &item.Summary); err != nil {
				item.Summary = nil
			}
		}
		items = append(items, item)
	}
	return items, total, page, pageSize, rows.Err()
}

func (s *Store) CleanupScheduledReportRuns(before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM scheduled_report_runs WHERE started_at < ?`, before.Format(time.RFC3339Nano))
	return err
}
