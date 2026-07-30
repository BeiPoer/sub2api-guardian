// Package domain 定义 Guardian 的核心数据类型，供 store / upstream / engine / api 共用。
package domain

import (
	"strings"
	"time"
)

// Connection 是连接 sub2api 管理端所需的配置。
type Connection struct {
	BaseURL        string `json:"base_url"`
	AdminAPIKey    string `json:"admin_api_key"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Enabled        bool   `json:"enabled"` // 自动守护总开关
}

// DefaultConnection 返回默认连接配置。
func DefaultConnection() Connection {
	return Connection{
		BaseURL:        "http://127.0.0.1:8080",
		TimeoutSeconds: 60,
		Enabled:        true,
	}
}

// Group 是 sub2api 的分组。
type Group struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Platform       string  `json:"platform"`
	Status         string  `json:"status"`
	RateMultiplier float64 `json:"rate_multiplier"`
	AccountCount   int64   `json:"account_count"`
	SortOrder      int     `json:"sort_order"`
}

// Account 是 sub2api 的渠道账号。只保留 Guardian 关心的字段。
type Account struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	Platform         string   `json:"platform"`
	Type             string   `json:"type"`
	Status           string   `json:"status"`
	Schedulable      bool     `json:"schedulable"`
	Priority         int      `json:"priority"`
	LoadFactor       *int     `json:"load_factor"`
	Concurrency      int      `json:"concurrency"`
	RateMultiplier   float64  `json:"rate_multiplier"`
	ErrorMessage     string   `json:"error_message"`
	GroupIDs         []int64  `json:"group_ids"`
	Groups           []Group  `json:"groups"`
	QuotaLimit       *float64 `json:"quota_limit,omitempty"`
	QuotaUsed        *float64 `json:"quota_used,omitempty"`
	QuotaDailyLimit  *float64 `json:"quota_daily_limit,omitempty"`
	QuotaDailyUsed   *float64 `json:"quota_daily_used,omitempty"`
	QuotaWeeklyLimit *float64 `json:"quota_weekly_limit,omitempty"`
	QuotaWeeklyUsed  *float64 `json:"quota_weekly_used,omitempty"`

	// ExpiresAt 对齐 sub2api 管理端 DTO，使用 Unix 秒而非 RFC3339 字符串。
	ExpiresAt          *int64 `json:"expires_at,omitempty"`
	AutoPauseOnExpired bool   `json:"auto_pause_on_expired,omitempty"`

	RateLimitedAt          *time.Time `json:"rate_limited_at,omitempty"`
	TempUnschedulableUntil *time.Time `json:"temp_unschedulable_until,omitempty"`
	LastUsedAt             *time.Time `json:"last_used_at,omitempty"`

	// RateLimitResetAt 是 sub2api 记录的限流窗口结束时间。
	//
	// 这个字段是「网站此刻会不会把请求发给它」的权威依据：sub2api 的选路查询带
	// `AND (rate_limit_reset_at IS NULL OR rate_limit_reset_at <= now)`，
	// 窗口没过的账号根本不会被选中，到点又自动纳入。
	//
	// 早先 Guardian 不缓存它，只能靠自己探测到 429 来推断限流。两者会长时间不一致：
	// 网站在真实流量里撞到限流并写下这个时间戳，Guardian 却要等下一次探测
	// （默认 300 秒）才知道，期间矩阵上仍显示健康。窗口长达数天时更明显 ——
	// 探测一次成功就把它算回健康，而网站一整天都不会路由给它。
	RateLimitResetAt *time.Time `json:"rate_limit_reset_at,omitempty"`

	// OverloadUntil 是上游过载退避的结束时间，同样参与 sub2api 的选路过滤。
	OverloadUntil *time.Time `json:"overload_until,omitempty"`

	// TempUnschedulableReason 是临时不可调度的原因，仅用于展示。
	TempUnschedulableReason string `json:"temp_unschedulable_reason,omitempty"`
}

// BlockKind 表示 sub2api 不把请求发给某个渠道的原因类别。
type BlockKind string

const (
	// BlockNone 表示上游此刻愿意把请求发给它。
	BlockNone BlockKind = ""
	// BlockDisabled 是账号在 sub2api 侧被停用。
	BlockDisabled BlockKind = "disabled"
	// BlockUnschedulable 是账号被关闭调用（schedulable=false）。
	BlockUnschedulable BlockKind = "unschedulable"
	// BlockRateLimited 是限流窗口未过，到点自动恢复。
	BlockRateLimited BlockKind = "rate_limited"
	// BlockTempUnschedulable 是临时不可调度，到点自动恢复。
	BlockTempUnschedulable BlockKind = "temp_unschedulable"
	// BlockOverloaded 是上游过载退避中，到点自动恢复。
	BlockOverloaded BlockKind = "overloaded"
	// BlockExpired 是账号已到期且启用了到期自动暂停。
	BlockExpired BlockKind = "expired"
	// BlockQuotaExceeded 是 API Key / Bedrock 账号的总、日或周配额已用尽。
	BlockQuotaExceeded BlockKind = "quota_exceeded"
)

// SelfHealing 报告这类阻塞是否会到点自动解除、无需人工干预。
//
// 界面据此决定要不要提示「需要处理」：限流、临时不可调度、过载退避都会自愈，
// 把它们混进待处理项会让每次上游限流都看起来像一堆故障，把真问题淹掉。
func (k BlockKind) SelfHealing() bool {
	switch k {
	case BlockRateLimited, BlockTempUnschedulable, BlockOverloaded:
		return true
	default:
		return false
	}
}

// UpstreamBlock 返回「sub2api 此刻不会把请求发给这个渠道」的原因，可用时返回 BlockNone。
//
// 判定口径完全对齐 sub2api 的选路过滤条件，顺序也照抄：先看账号级开关，
// 再看三个到点自动失效的时间窗。这样矩阵上的数字才和网站后台看到的一致 ——
// 这几个窗口都是上游在真实流量里写下的，Guardian 探测不到也必须承认。
//
// 注意这里**不含** Guardian 自己的判断（熔断、人工排除、健康分）。
// 它回答的是「网站怎么看」，Guardian 的意见由 ChannelState 表达，两者分开才能对账。
func (a Account) UpstreamBlock(now time.Time) (BlockKind, string) {
	if !a.IsActive() {
		return BlockDisabled, "账号状态为 " + strings.TrimSpace(a.Status) + "，未在 sub2api 启用"
	}
	if !a.Schedulable {
		return BlockUnschedulable, "已在 sub2api 关闭调用"
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && now.Unix() >= *a.ExpiresAt {
		return BlockExpired, "账号已到期"
	}
	if after(a.OverloadUntil, now) {
		return BlockOverloaded, "上游过载退避中，" + localStamp(a.OverloadUntil) + " 恢复"
	}
	if after(a.RateLimitResetAt, now) {
		return BlockRateLimited, "限流中，" + localStamp(a.RateLimitResetAt) + " 恢复"
	}
	if after(a.TempUnschedulableUntil, now) {
		reason := a.TempUnschedulableReason
		if reason == "" {
			reason = "临时不可调度"
		}
		return BlockTempUnschedulable, reason + "，" + localStamp(a.TempUnschedulableUntil) + " 恢复"
	}
	if dimension, exceeded := a.ExceededQuota(); exceeded {
		return BlockQuotaExceeded, dimension + "已用尽"
	}
	return BlockNone, ""
}

// IsActive 对齐 sub2api 的账号状态契约：只有 active 会进入调度候选。
func (a Account) IsActive() bool {
	return strings.EqualFold(strings.TrimSpace(a.Status), "active")
}

// ExceededQuota 返回会阻止 sub2api 调度的静态配额维度。
// sub2api 仅对 API Key 与 Bedrock 账号应用这组配额约束。
func (a Account) ExceededQuota() (string, bool) {
	accountType := strings.ToLower(strings.TrimSpace(a.Type))
	if accountType != "apikey" && accountType != "bedrock" {
		return "", false
	}
	checks := []struct {
		name        string
		limit, used *float64
	}{
		{name: "总配额", limit: a.QuotaLimit, used: a.QuotaUsed},
		{name: "日配额", limit: a.QuotaDailyLimit, used: a.QuotaDailyUsed},
		{name: "周配额", limit: a.QuotaWeeklyLimit, used: a.QuotaWeeklyUsed},
	}
	for _, check := range checks {
		if check.limit != nil && check.used != nil && *check.limit > 0 && *check.used >= *check.limit {
			return check.name, true
		}
	}
	return "", false
}

// SchedulableAt 报告 sub2api 此刻是否会把请求发给这个渠道。
func (a Account) SchedulableAt(now time.Time) bool {
	kind, _ := a.UpstreamBlock(now)
	return kind == BlockNone
}

// after 判断时间点是否还没到。三个窗口字段都可能为空指针，空视为「无限制」。
func after(t *time.Time, now time.Time) bool {
	return t != nil && t.After(now)
}

func localStamp(t *time.Time) string {
	return t.Local().Format("01-02 15:04")
}

// EffectiveLoadFactor 对齐 sub2api：load_factor 未设置时回退到 concurrency，再回退到 1。
func (a Account) EffectiveLoadFactor() int {
	if a.LoadFactor != nil && *a.LoadFactor > 0 {
		return *a.LoadFactor
	}
	if a.Concurrency > 0 {
		return a.Concurrency
	}
	return 1
}

// Balance 返回 apikey 账号的剩余额度，无法计算时返回 nil。
func (a Account) Balance() *float64 {
	if a.QuotaLimit == nil || a.QuotaUsed == nil {
		return nil
	}
	v := *a.QuotaLimit - *a.QuotaUsed
	return &v
}

// GroupIDSet 返回账号所属分组 ID 的合并集合。
func (a Account) GroupIDSet() []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(a.GroupIDs)+len(a.Groups))
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range a.GroupIDs {
		add(id)
	}
	for _, g := range a.Groups {
		add(g.ID)
	}
	return out
}

// EventType 是一次探测或真实请求的结果分类。
type EventType string

const (
	// EventPerfect 正常返回且首字延迟在优秀范围内。
	EventPerfect EventType = "perfect"
	// EventSlowTTFB 正常返回但首字延迟超阈值。
	EventSlowTTFB EventType = "slow_ttfb"
	// EventUpstreamUnknown 上游返回非标准错误。
	EventUpstreamUnknown EventType = "upstream_unknown"
	// EventGatewayError 网关/限流类错误（429/5xx）。
	EventGatewayError EventType = "gateway_error"
	// EventProbeFail 探测超时或连接失败。
	EventProbeFail EventType = "probe_fail"

	// EventQuotaExhausted 限流或额度耗尽：429、余额不足、用量上限。
	//
	// 与 EventFatal 分开是因为两者的可恢复性完全不同：
	// 额度会随窗口重置而恢复，充值也能解决；而凭据失效不会自己好。
	// 早期版本把它们混为一类，结果 429 的渠道被强制归零分，
	// 而 0 分低于回池目标分，这些渠道永远回不了池。
	EventQuotaExhausted EventType = "quota_exhausted"

	// EventFatal 致命错误：凭据失效，且不会自行恢复。
	EventFatal EventType = "fatal"
)

// SampleSource 标记样本来自主动探测还是真实流量。
type SampleSource string

const (
	// SourceProbe 主动探测样本。
	SourceProbe SampleSource = "probe"
	// SourceTraffic 真实流量样本。
	SourceTraffic SampleSource = "traffic"
)

// Sample 是一条健康度样本。
type Sample struct {
	ID         int64        `json:"id"`
	AccountID  int64        `json:"account_id"`
	OccurredAt time.Time    `json:"occurred_at"`
	Source     SampleSource `json:"source"`
	EventType  EventType    `json:"event_type"`
	Score      float64      `json:"score"`
	TTFBMs     int64        `json:"ttfb_ms"`
	DurationMs int64        `json:"duration_ms"`
	StatusCode int          `json:"status_code"`

	// Model 是实际使用的模型；RequestModel 是我们请求的模型。
	// 两者不同说明 sub2api 侧应用了账号级模型映射。
	Model        string `json:"model"`
	RequestModel string `json:"request_model,omitempty"`

	RequestID string `json:"request_id"`
	Message   string `json:"message"`
}

// ChannelHealth 是渠道的健康态。
type ChannelHealth string

const (
	// HealthUnknown 尚无样本。
	HealthUnknown ChannelHealth = "unknown"
	// HealthHealthy 健康。
	HealthHealthy ChannelHealth = "healthy"
	// HealthDegraded 降级中（仍可调度，但权重被压低）。
	HealthDegraded ChannelHealth = "degraded"
	// HealthFused 已熔断。
	HealthFused ChannelHealth = "fused"
	// HealthExcluded 人工排除。
	HealthExcluded ChannelHealth = "excluded"
	// HealthSurvivor 保底强留：本应熔断但为保证分组存活而留下。
	HealthSurvivor ChannelHealth = "survivor"
	// HealthPaused 人工暂停调度：不接流量，但仍受 Guardian 监控，
	// 且不会因健康分回升而被自动放回可用池（与熔断的关键区别）。
	HealthPaused ChannelHealth = "paused"
)

// ChannelState 是 Guardian 对单个渠道的计算结果与期望值。
type ChannelState struct {
	AccountID int64  `json:"account_id"`
	GroupID   *int64 `json:"group_id"`

	// Health 是**已经生效**的状态：写回 sub2api 成功后才会推进到这里。
	//
	// DesiredHealth 是引擎本轮算出的**期望**状态。两者不同说明写回还没落地
	// —— 例如判定要熔断但 sub2api 写回失败，此时渠道实际仍在接流量，
	// 界面必须如实反映，不能显示成已经摘掉了。
	Health        ChannelHealth `json:"health"`
	DesiredHealth ChannelHealth `json:"desired_health"`

	// ApplyPending 为真表示期望状态尚未在 sub2api 上生效。
	ApplyPending   bool   `json:"apply_pending"`
	LastApplyError string `json:"last_apply_error,omitempty"`

	HealthSince time.Time `json:"health_since"`
	ShortScore  float64   `json:"short_score"`
	LongScore   float64   `json:"long_score"`
	HealthScore float64   `json:"health_score"`

	ConsecutiveOK   int `json:"consecutive_ok"`
	ConsecutiveFail int `json:"consecutive_fail"`
	SampleCount     int `json:"sample_count"`

	TTFBP50Ms int64 `json:"ttfb_p50_ms"`
	TTFBP95Ms int64 `json:"ttfb_p95_ms"`

	// Multiplier 是该渠道生效的调度倍率（越低越优先）。
	// 仅供 Guardian 调度使用，不写回 sub2api。
	Multiplier       float64  `json:"multiplier"`
	MultiplierManual bool     `json:"multiplier_manual"` // 是否为人工设置
	Balance          *float64 `json:"balance,omitempty"`

	Weight             float64 `json:"weight"`
	DesiredPriority    int     `json:"desired_priority"`
	DesiredLoadFactor  *int    `json:"desired_load_factor,omitempty"`
	DesiredConcurrency *int    `json:"desired_concurrency,omitempty"`

	FusedReason  string    `json:"fused_reason"`
	FusedUntil   time.Time `json:"fused_until"`
	CooldownTill time.Time `json:"cooldown_till"`
	LastSampleAt time.Time `json:"last_sample_at"`
	LastProbeAt  time.Time `json:"last_probe_at"`

	// 最近一次探测请求的模型与 sub2api 实际使用的模型。
	// ModelRewritten 为真表示指定的模型被账号级映射改掉了。
	LastRequestModel string `json:"last_request_model,omitempty"`
	LastProbeModel   string `json:"last_probe_model,omitempty"`
	ModelRewritten   bool   `json:"model_rewritten"`

	LastTrafficAt time.Time `json:"last_traffic_at"`
	LastApplyAt   time.Time `json:"last_apply_at"`
	LastError     string    `json:"last_error"`
	Models        []string  `json:"models"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// GroupStatus 是分组的聚合状态。
type GroupStatus string

const (
	// GroupEmpty 分组内没有受管渠道。
	GroupEmpty GroupStatus = "empty"
	// GroupSkipped 分组未参与守护。
	GroupSkipped GroupStatus = "skipped"
	// GroupHealthy 分组全部渠道健康。
	GroupHealthy GroupStatus = "healthy"
	// GroupPartial 分组部分渠道异常。
	GroupPartial GroupStatus = "partial_degraded"
	// GroupRateLimited 分组内有渠道正在限流，但没有真故障。
	//
	// 与 GroupPartial 分开：限流会随窗口重置自愈、渠道仍在池子里，
	// 不需要人介入；混成同一个「部分异常」会让运维分不清该不该动手。
	GroupRateLimited GroupStatus = "rate_limited"
	// GroupSurvivorOnly 分组只剩保底渠道。
	GroupSurvivorOnly GroupStatus = "survivor_only"
	// GroupAllFused 分组全部渠道熔断。
	GroupAllFused GroupStatus = "all_fused"
)

// GroupState 是分组的聚合结果。
type GroupState struct {
	GroupID  int64       `json:"group_id"`
	Status   GroupStatus `json:"status"`
	Strategy string      `json:"strategy"`

	TotalAccounts    int `json:"total_accounts"`
	HealthyAccounts  int `json:"healthy_accounts"`
	DegradedAccounts int `json:"degraded_accounts"`
	FusedAccounts    int `json:"fused_accounts"`
	PausedAccounts   int `json:"paused_accounts"`

	// ExcludedAccounts 是「接不到流量」的渠道数，含三种：
	// 人工排除、sub2api 侧 status=inactive/error、以及 schedulable=false（被关掉调用）。
	//
	// 它们都不接流量，因此要计入「分组是否断供」的判定：
	// 只数熔断与暂停的话，把一组渠道全部排除后分组仍会显示为健康。
	//
	// 「探测正常但被关掉调用」也归在这里 —— 它探测得再好也接不到一个请求，
	// 算进 HealthyAccounts 会让矩阵上的健康数比实际能服务的多。
	ExcludedAccounts int `json:"excluded_accounts"`

	// RateLimitedAccounts 是当前处于限流 / 额度耗尽的渠道数。
	//
	// 它是 DegradedAccounts 的子集，单独统计是为了在界面上区分两种「不正常」：
	// 限流会随窗口重置自愈、渠道仍在池子里，而真故障需要人介入。
	// 只看「降级 51 个」分不出这两者，运维不知道该不该动手。
	RateLimitedAccounts int `json:"rate_limited_accounts"`

	// PendingAccounts 是尚未被探测过的渠道数。
	//
	// 它们在 sub2api 侧正常服务，只是 Guardian 还没采到样本，
	// 因此既不算健康也不算降级，单独统计避免污染两边。
	PendingAccounts int `json:"pending_accounts"`

	// AvailableAccounts 是计入可用池的渠道数，与保底判定同口径。
	AvailableAccounts int `json:"available_accounts"`

	SurvivorAccountID *int64  `json:"survivor_account_id,omitempty"`
	BestScore         float64 `json:"best_score"`
	AvgScore          float64 `json:"avg_score"`
	TotalWeight       float64 `json:"total_weight"`
	TotalConcurrency  int     `json:"total_concurrency"`

	LastAlertAt      time.Time `json:"last_alert_at"`
	LastAlertMessage string    `json:"last_alert_message"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Baseline 是 Guardian 接管前的账号原值快照，用于恢复。
type Baseline struct {
	AccountID      int64     `json:"account_id"`
	Status         string    `json:"status,omitempty"`
	Priority       int       `json:"priority"`
	LoadFactor     *int      `json:"load_factor,omitempty"`
	Concurrency    int       `json:"concurrency"`
	RateMultiplier float64   `json:"rate_multiplier"`
	Schedulable    bool      `json:"schedulable"`
	CapturedAt     time.Time `json:"captured_at"`

	// OwnershipVersion 与 Managed* 记录 Guardian 最近一次已持久化的写入意图。
	// 恢复时仅回滚仍等于该意图的字段，避免覆盖管理员之后的手工修改。
	// 旧版基线没有这些字段（版本 0），恢复时保持原有的全量还原语义。
	OwnershipVersion      int      `json:"ownership_version,omitempty"`
	ManagedPriority       *int     `json:"managed_priority,omitempty"`
	ManagedLoadFactor     *int     `json:"managed_load_factor,omitempty"`
	ManagedConcurrency    *int     `json:"managed_concurrency,omitempty"`
	ManagedRateMultiplier *float64 `json:"managed_rate_multiplier,omitempty"`
	ManagedSchedulable    *bool    `json:"managed_schedulable,omitempty"`
	ManagedStatus         *string  `json:"managed_status,omitempty"`
}

// Action 是一次对 sub2api 的写操作记录。
type Action struct {
	ID        int64     `json:"id"`
	AccountID int64     `json:"account_id"`
	Kind      string    `json:"kind"`
	Before    string    `json:"before"`
	After     string    `json:"after"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"created_at"`
}

// Event 是一条审计事件。
type Event struct {
	ID        int64     `json:"id"`
	Level     string    `json:"level"` // info | warn | error
	Action    string    `json:"action"`
	AccountID *int64    `json:"account_id,omitempty"`
	GroupID   *int64    `json:"group_id,omitempty"`
	Message   string    `json:"message"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// User 是面板的登录账号。
//
// PasswordHash 带 json:"-"：这个结构体会被塞进 API 响应，
// 摘要一旦随手序列化出去就等于把离线破解的原料送到了客户端。
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
