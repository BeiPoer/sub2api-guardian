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

var (
	ErrAlreadyRunning      = errors.New("定时报告正在执行")
	ErrSourceNotConfigured = errors.New("报告源站未配置完整，请前往源站配置完成设置")
)

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

type SourceConfig struct {
	ID               string                          `json:"id"`
	Name             string                          `json:"name"`
	Mode             store.ScheduledReportSourceMode `json:"mode"`
	SourceType       store.ScheduledReportSourceType `json:"source_type"`
	BaseURL          string                          `json:"base_url"`
	NewAPIUserID     int64                           `json:"newapi_user_id"`
	HasCredential    bool                            `json:"has_credential"`
	Configured       bool                            `json:"configured"`
	EffectiveType    store.ScheduledReportSourceType `json:"effective_type"`
	EffectiveBaseURL string                          `json:"effective_base_url"`
}

type SourceCatalogConfig struct {
	SourceConfig
	Items []SourceConfig `json:"items"`
}

type SourceSaveInput struct {
	ID           string                          `json:"id"`
	Name         string                          `json:"name"`
	Mode         store.ScheduledReportSourceMode `json:"mode"`
	SourceType   store.ScheduledReportSourceType `json:"source_type"`
	BaseURL      string                          `json:"base_url"`
	Credential   string                          `json:"credential"`
	NewAPIUserID int64                           `json:"newapi_user_id"`
}

type SourceSummary struct {
	ID         string                          `json:"id"`
	Name       string                          `json:"name"`
	Mode       store.ScheduledReportSourceMode `json:"mode"`
	Type       store.ScheduledReportSourceType `json:"type"`
	Configured bool                            `json:"configured"`
	BaseURL    string                          `json:"base_url"`
}

// ChannelUsageConfig 是对外返回的配置与运行状态。
type ChannelUsageConfig struct {
	SourceID              string `json:"source_id"`
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

// DailyReportConfig 是对外返回的每日报告配置与运行状态。
type DailyReportConfig struct {
	SourceID   string `json:"source_id"`
	Enabled    bool   `json:"enabled"`
	RunHour    int    `json:"run_hour"`
	Timezone   string `json:"timezone"`
	LastRunAt  string `json:"last_run_at"`
	LastStatus string `json:"last_status"`
	LastError  string `json:"last_error"`
	NextRunAt  string `json:"next_run_at"`
}

type WeComInput struct {
	Enabled bool   `json:"enabled"`
	CorpID  string `json:"corp_id"`
	AgentID int64  `json:"agent_id"`
	Secret  string `json:"secret"`
	Target  string `json:"target"`
}

type SaveInput struct {
	SourceID              string `json:"source_id"`
	Enabled               bool   `json:"enabled"`
	IntervalMinutes       int    `json:"interval_minutes"`
	StartHour             int    `json:"start_hour"`
	EndHour               int    `json:"end_hour"`
	Timezone              string `json:"timezone"`
	LookbackHours         int    `json:"lookback_hours"`
	FirstTokenThresholdMS int64  `json:"first_token_threshold_ms"`
	TriggerCount          int    `json:"trigger_count"`
}

type DailySaveInput struct {
	SourceID string `json:"source_id"`
	Enabled  bool   `json:"enabled"`
	RunHour  int    `json:"run_hour"`
	Timezone string `json:"timezone"`
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
	Source     SourceSummary             `json:"source"`
	Sources    []SourceSummary           `json:"sources"`
	Connection ConnectionSummary         `json:"connection"`
	LatestRun  *store.ScheduledReportRun `json:"latest_run"`
}

type DailyView struct {
	Config     DailyReportConfig         `json:"config"`
	Source     SourceSummary             `json:"source"`
	Sources    []SourceSummary           `json:"sources"`
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

type DailyReportSummary struct {
	Date            string             `json:"date"`
	Timezone        string             `json:"timezone"`
	TotalActualCost float64            `json:"total_actual_cost"`
	QuotaUnit       string             `json:"quota_unit"`
	TotalTokens     int64              `json:"total_tokens"`
	NewUsers        int                `json:"new_users"`
	RechargeAmounts map[string]float64 `json:"recharge_amounts"`
	RechargeUsers   int                `json:"recharge_users"`
}

type storedWeComConfig struct {
	Enabled bool   `json:"enabled"`
	CorpID  string `json:"corp_id"`
	AgentID int64  `json:"agent_id"`
	Secret  string `json:"secret"`
	Target  string `json:"target"`
}

type storedConfig struct {
	SourceID              string `json:"source_id,omitempty"`
	LookbackHours         int    `json:"lookback_hours"`
	FirstTokenThresholdMS int64  `json:"first_token_threshold_ms"`
	TriggerCount          int    `json:"trigger_count"`
}

type storedDailyConfig struct {
	SourceID string `json:"source_id,omitempty"`
}

type legacyStoredConfig struct {
	WeCom *storedWeComConfig `json:"wecom"`
}
