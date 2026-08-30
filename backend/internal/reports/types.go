package reports

import (
	"errors"
	"net/http"

	"sub2api-guardian/backend/internal/store"
)

const (
	defaultIntervalMinutes   = 60
	defaultStartHour         = 9
	defaultEndHour           = 22
	defaultTimezone          = "Asia/Shanghai"
	defaultLookbackHours     = 1
	defaultFirstTokenMS      = 30_000
	defaultTriggerCount      = 20
	maxLookbackHours         = 168
	maxIntervalMinutes       = 1440
	usageRequestTimeout      = 45
	runHistoryRetentionHours = 7 * 24
)

var ErrAlreadyRunning = errors.New("渠道使用报告正在执行")

// Error 是报告 API 可以安全返回给前端的业务错误。
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func invalid(message string) error { return &Error{Status: http.StatusBadRequest, Message: message} }

type NotificationWeComConfig struct {
	Enabled   bool   `json:"enabled"`
	CorpID    string `json:"corp_id"`
	AgentID   int64  `json:"agent_id"`
	Secret    string `json:"secret"`
	Target    string `json:"target"`
	HasSecret bool   `json:"has_secret"`
}

type NotificationConfig struct {
	WeCom NotificationWeComConfig `json:"wecom"`
}

// ChannelUsageConfig 是对外返回的配置与运行状态。
type ChannelUsageConfig struct {
	Enabled               bool   `json:"enabled"`
	IntervalMinutes       int    `json:"interval_minutes"`
	StartHour             int    `json:"start_hour"`
	EndHour               int    `json:"end_hour"`
	Timezone              string `json:"timezone"`
	LookbackHours         int    `json:"lookback_hours"`
	FirstTokenThresholdMS int64  `json:"first_token_threshold_ms"`
	TriggerCount          int    `json:"trigger_count"`
	LastRunAt             string `json:"last_run_at"`
	LastStatus            string `json:"last_status"`
	LastError             string `json:"last_error"`
	NextRunAt             string `json:"next_run_at"`
}

type WeComInput struct {
	Enabled bool   `json:"enabled"`
	CorpID  string `json:"corp_id"`
	AgentID int64  `json:"agent_id"`
	Secret  string `json:"secret"`
	Target  string `json:"target"`
}

type SaveInput struct {
	Enabled               bool   `json:"enabled"`
	IntervalMinutes       int    `json:"interval_minutes"`
	StartHour             int    `json:"start_hour"`
	EndHour               int    `json:"end_hour"`
	Timezone              string `json:"timezone"`
	LookbackHours         int    `json:"lookback_hours"`
	FirstTokenThresholdMS int64  `json:"first_token_threshold_ms"`
	TriggerCount          int    `json:"trigger_count"`
}

type NotificationSaveInput struct {
	WeCom WeComInput `json:"wecom"`
}

type ConnectionSummary struct {
	Configured bool   `json:"configured"`
	BaseURL    string `json:"base_url"`
}

type View struct {
	Config     ChannelUsageConfig        `json:"config"`
	Connection ConnectionSummary         `json:"connection"`
	LatestRun  *store.ScheduledReportRun `json:"latest_run"`
}

type SummaryRow struct {
	GroupName        string `json:"group_name"`
	AccountName      string `json:"account_name"`
	HighLatencyCount int    `json:"high_latency_count"`
	TotalRecords     int    `json:"total_records"`
}

type Evaluation struct {
	TotalRecords     int          `json:"total_records"`
	HighLatencyCount int          `json:"high_latency_count"`
	Alert            bool         `json:"alert"`
	Rows             []SummaryRow `json:"rows"`
}

type storedWeComConfig struct {
	Enabled bool   `json:"enabled"`
	CorpID  string `json:"corp_id"`
	AgentID int64  `json:"agent_id"`
	Secret  string `json:"secret"`
	Target  string `json:"target"`
}

type storedConfig struct {
	LookbackHours         int   `json:"lookback_hours"`
	FirstTokenThresholdMS int64 `json:"first_token_threshold_ms"`
	TriggerCount          int   `json:"trigger_count"`
}

type legacyStoredConfig struct {
	WeCom *storedWeComConfig `json:"wecom"`
}
