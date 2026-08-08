package api

import (
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/policy"
)

// GroupRef 是渠道所属分组的精简引用。
type GroupRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// SampleDTO 是前端展示「最近 N 次结果」用的样本。
type SampleDTO struct {
	OccurredAt time.Time `json:"occurred_at"`
	Source     string    `json:"source"`
	EventType  string    `json:"event_type"`
	Score      float64   `json:"score"`
	TTFBMs     int64     `json:"ttfb_ms"`
	StatusCode int       `json:"status_code"`
	Message    string    `json:"message"`
}

// ChannelDTO 是渠道池表格的一行。
type ChannelDTO struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Platform    string     `json:"platform"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	Groups      []GroupRef `json:"groups"`
	PrimaryGrp  *int64     `json:"primary_group_id,omitempty"`
	Schedulable bool       `json:"schedulable"`
	Excluded    bool       `json:"excluded"`
	Paused      bool       `json:"paused"`

	// UpstreamBlock 是 sub2api 此刻不把请求发给它的原因类别，可用时为空串。
	// 取值见 domain.BlockKind：unschedulable / rate_limited / temp_unschedulable /
	// overloaded / disabled / expired / quota_exceeded。
	//
	// 这是与网站对账的关键字段：Guardian 探测出的健康分再高，只要上游在限流窗口里
	// 就一个请求都接不到。没有它，页面上会出现「健康分 100 却不接流量」的困惑。
	UpstreamBlock     string `json:"upstream_block,omitempty"`
	UpstreamBlockText string `json:"upstream_block_text,omitempty"`

	// Health 是已在 sub2api 生效的状态；DesiredHealth 是引擎的期望状态。
	// 两者不同（ApplyPending=true）说明写回失败或处于预览模式，渠道实际行为还没改变。
	Health        string  `json:"health"`
	DesiredHealth string  `json:"desired_health,omitempty"`
	ApplyPending  bool    `json:"apply_pending"`
	ApplyError    string  `json:"apply_error,omitempty"`
	HealthScore   float64 `json:"health_score"`
	ShortScore    float64 `json:"short_score"`
	LongScore     float64 `json:"long_score"`
	SampleCount   int     `json:"sample_count"`
	FailStreak    int     `json:"fail_streak"`
	OKStreak      int     `json:"ok_streak"`

	TTFBP50Ms int64 `json:"ttfb_p50_ms"`
	TTFBP95Ms int64 `json:"ttfb_p95_ms"`

	// Multiplier 是 Guardian 内部的调度倍率（越低越优先），与网站计费无关。
	Multiplier                       float64    `json:"multiplier"`
	MultiplierManual                 bool       `json:"multiplier_manual"`
	ManualMultiplier                 *float64   `json:"manual_multiplier,omitempty"`
	UpstreamMultiplierEnabled        bool       `json:"upstream_multiplier_enabled"`
	UpstreamMultiplierBreakerEnabled bool       `json:"upstream_multiplier_breaker_enabled"`
	UpstreamMultiplierThreshold      *float64   `json:"upstream_multiplier_threshold,omitempty"`
	MultiplierSource                 string     `json:"multiplier_source"`
	UpstreamMultiplier               *float64   `json:"upstream_multiplier,omitempty"`
	UpstreamMultiplierUpdatedAt      *time.Time `json:"upstream_multiplier_updated_at,omitempty"`
	Balance                          *float64   `json:"balance,omitempty"`

	RateMultiplier float64 `json:"rate_multiplier"`
	Priority       int     `json:"priority"`
	LoadFactor     *int    `json:"load_factor,omitempty"`
	Concurrency    int     `json:"concurrency"`

	Weight             float64 `json:"weight"`
	DesiredPriority    int     `json:"desired_priority"`
	DesiredLoadFactor  *int    `json:"desired_load_factor,omitempty"`
	DesiredConcurrency *int    `json:"desired_concurrency,omitempty"`

	FusedReason  string    `json:"fused_reason"`
	FusedUntil   time.Time `json:"fused_until"`
	CooldownTill time.Time `json:"cooldown_till"`
	LastSampleAt time.Time `json:"last_sample_at"`
	LastProbeAt  time.Time `json:"last_probe_at"`
	LastError    string    `json:"last_error"`
	TestModel    string    `json:"test_model"`
	Models       []string  `json:"models"`
	Managed      bool      `json:"managed"`

	// 最近一次探测请求的模型与 sub2api 实际使用的模型。
	// ModelRewritten 为真说明指定模型被账号级映射改掉了，需要在 sub2api 侧调整。
	LastRequestModel string `json:"last_request_model,omitempty"`
	LastProbeModel   string `json:"last_probe_model,omitempty"`
	ModelRewritten   bool   `json:"model_rewritten"`

	Recent []SampleDTO `json:"recent"`
}

// GroupDTO 是分组调度页的一张卡。
type GroupDTO struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Platform       string  `json:"platform"`
	Status         string  `json:"status"`
	RateMultiplier float64 `json:"rate_multiplier"`

	Managed  bool   `json:"managed"`
	Excluded bool   `json:"excluded"` // 整组移出调度系统管控
	Strategy string `json:"strategy"`

	State    domain.GroupState     `json:"state"`
	Override *policy.GroupOverride `json:"override,omitempty"`
	Channels []ChannelDTO          `json:"channels"`
}

// StatTile 是总览页顶部的统计卡。
type StatTile struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Meta  string  `json:"meta"`
	Tone  string  `json:"tone"`
}

// OverviewDTO 是总览页的完整数据。
type OverviewDTO struct {
	Status engine.Status  `json:"status"`
	Tiles  []StatTile     `json:"tiles"`
	Groups []GroupDTO     `json:"groups"`
	Events []domain.Event `json:"events"`

	TotalChannels    int `json:"total_channels"`
	HealthyChannels  int `json:"healthy_channels"`
	PendingChannels  int `json:"pending_channels"`
	DegradedChannels int `json:"degraded_channels"`
	FusedChannels    int `json:"fused_channels"`
	SurvivorChannels int `json:"survivor_channels"`

	// UnschedulableChannels 是探测正常、但在 sub2api 侧接不到流量的渠道数。
	//
	// 含被关掉调用（schedulable=false）、临时不可调度、过载退避三种。
	// 它们接不到任何请求，因此不计入健康数；单独统计是为了让面板能说清
	// 「有几个渠道其实是被关掉的」，而不是让它们凭空消失。
	//
	// 限流不在这里 —— 它会到点自愈，单独记进 RateLimitedChannels。
	UnschedulableChannels int `json:"unschedulable_channels"`

	// RateLimitedChannels 是处在上游限流窗口里的渠道数（DegradedChannels 的子集）。
	RateLimitedChannels int `json:"rate_limited_channels"`

	AllocatedConc     int     `json:"allocated_concurrency"`
	ConcurrencyLimit  int     `json:"concurrency_limit"`
	AvgHealthScore    float64 `json:"avg_health_score"`
	GroupsAtRisk      int     `json:"groups_at_risk"`
	MonitoringEnabled bool    `json:"monitoring_enabled"`
}

// ActionDTO 是一条写回记录，附带渠道与分组信息。
//
// 只存 account_id 的话，页面上只能显示裸 ID，排障时还得自己去对照渠道池。
type ActionDTO struct {
	domain.Action

	AccountName string     `json:"account_name"`
	Platform    string     `json:"platform"`
	Groups      []GroupRef `json:"groups"`
	Deleted     bool       `json:"deleted"` // 账号已不在缓存中（多为已被删除）
}

// EventPage 是事件日志的分页结果。
type EventPage struct {
	Items    []domain.Event `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}
