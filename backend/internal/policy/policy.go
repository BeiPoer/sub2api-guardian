// Package policy 定义 Guardian 的全部调度策略：开关、阈值和分组级覆盖。
//
// 设计原则：所有会影响自动化行为的参数都必须出现在这里，且带有合理默认值，
// 这样前端只要渲染这个结构体就能把配置权完整交给运营。
package policy

import (
	"strings"
)

// minProbeIntervalSeconds 是探测间隔的下限，防止把上游打爆。
const minProbeIntervalSeconds = 30

// minUpstreamMultiplierIntervalSeconds 限制自动倍率读取的最短周期，避免上游探测接口被打爆。
const minUpstreamMultiplierIntervalSeconds = 30

// Strategy 是组内渠道的排序与调权策略。
type Strategy string

const (
	// StrategyPrice 价格优先：成本越低权重越高。
	StrategyPrice Strategy = "price"
	// StrategySpeed 速度优先：首字延迟越低权重越高。
	StrategySpeed Strategy = "speed"
	// StrategyBalanced 均衡：价格与速度按比例混合。
	StrategyBalanced Strategy = "balanced"
)

// Valid 报告策略值是否合法。
func (s Strategy) Valid() bool {
	switch s {
	case StrategyPrice, StrategySpeed, StrategyBalanced:
		return true
	}
	return false
}

// EventScores 是各类探测/请求结果对应的健康分。
type EventScores struct {
	Perfect         float64 `json:"perfect"`
	SlowTTFB        float64 `json:"slow_ttfb"`
	UpstreamUnknown float64 `json:"upstream_unknown"`
	GatewayError    float64 `json:"gateway_error"`
	ProbeFail       float64 `json:"probe_fail"`

	// QuotaExhausted 是限流 / 额度耗尽的分值。
	//
	// 刻意不给 0：额度会随窗口重置恢复，给 0 分等于把渠道永久钉死
	// （0 分低于回池目标分，再也回不了池）。给一个偏低但非零的分，
	// 限流期间权重被压低、不接主要流量，限流过去后能随成功样本回升。
	QuotaExhausted float64 `json:"quota_exhausted"`

	Fatal float64 `json:"fatal"`
}

// Scoring 控制健康分的窗口与长短期权重。
type Scoring struct {
	EventScores  EventScores `json:"event_scores"`
	ShortWindow  int         `json:"short_window"`  // 短期分取最近多少条样本
	LongWindow   int         `json:"long_window"`   // 长期分取最近多少条样本
	LatestWeight float64     `json:"latest_weight"` // 短期分中最新一条的权重
	ShortRatio   float64     `json:"short_ratio"`   // 最终分中短期分占比
	SlowTTFBMs   int         `json:"slow_ttfb_ms"`  // 超过该首字时间记为“首字慢”
}

// Breaker 控制熔断触发条件。
type Breaker struct {
	Enabled            bool    `json:"enabled"`
	HardFatal          bool    `json:"hard_fatal"`             // 致命错误立即熔断
	HTTPWindow         int     `json:"http_window"`            // 错误率判定窗口
	HTTPFailures       int     `json:"http_failures"`          // 窗口内失败次数阈值
	HTTPScoreBelow     float64 `json:"http_score_below"`       // 且健康分低于该值
	LatencyWindow      int     `json:"latency_window"`         // 延迟判定窗口
	LatencyOccurrences int     `json:"latency_occurrences"`    // 窗口内慢响应次数阈值
	LatencyTTFBMs      int     `json:"latency_ttfb_ms"`        // 慢响应的首字时间界限
	MaxSwitchPerRound  int     `json:"max_switch_per_round"`   // 每轮最多熔断几个
	MinPoolSize        int     `json:"min_pool_size"`          // 每个分组保底可用渠道数
	MinPoolScore       float64 `json:"min_pool_score"`         // 计入保底池的最低健康分
	FusedCooldownSecs  int     `json:"fused_cooldown_seconds"` // 熔断后最短保持时间

	// InstantStatusCodes 是「见到即熔断」的状态码，不需要累计次数。
	//
	// 默认空：走常规的错误率与延迟判定。填了之后，最近一次样本命中其中任一
	// 状态码就立即熔断 —— 适合把 402（欠费）、429（持续限流）这类
	// 「继续打也没意义」的错误快速摘掉。仍受保底池约束。
	InstantStatusCodes []int `json:"instant_status_codes"`

	// HTTPDegradeOnly 为真时，错误率超标只降级不熔断。
	//
	// 降级 = 仍参与调度但权重与优先级被压低。适合「分组渠道越多越好」的场景：
	// 5xx、限流这类错误往往是上游临时抖动，摘掉渠道反而减少了可用容量。
	HTTPDegradeOnly bool `json:"http_degrade_only"`

	// LatencyDegradeOnly 为真时，延迟超标只降级不熔断。
	LatencyDegradeOnly bool `json:"latency_degrade_only"`
}

// Degrade 控制降级（不熔断，只压低权重和优先级）。
type Degrade struct {
	Enabled         bool    `json:"enabled"`
	ScoreThreshold  float64 `json:"score_threshold"`   // 低于该分进入降级
	PriorityStep    int     `json:"priority_step"`     // 降级时优先级数值增量
	LoadFactorRatio float64 `json:"load_factor_ratio"` // 降级时负载因子乘数
	MinLoadFactor   int     `json:"min_load_factor"`
}

// Recovery 控制熔断渠道的自动回池。
type Recovery struct {
	Enabled              bool    `json:"enabled"`
	ProbeIntervalSeconds int     `json:"probe_interval_seconds"`
	TargetScore          float64 `json:"target_score"`
	SuccessCount         int     `json:"success_count"`
	HoldSeconds          int     `json:"hold_seconds"`
}

// Weights 控制负载因子与优先级调权。
type Weights struct {
	Enabled            bool    `json:"enabled"`
	Budget             int     `json:"budget"`               // 每个分组的权重预算点数
	MinPriority        int     `json:"min_priority"`         // 自动调整时优先级的最小值
	GateFloor          float64 `json:"gate_floor"`           // 健康分低于该值权重归零
	PriceExp           float64 `json:"price_exp"`            // 价格优先的指数强度
	SpeedExp           float64 `json:"speed_exp"`            // 速度优先的指数强度
	BalancedPriceRatio float64 `json:"balanced_price_ratio"` // 均衡策略中价格占比
	ChangeThreshold    float64 `json:"change_threshold"`     // 变化小于该比例不写回
	CooldownSeconds    int     `json:"cooldown_seconds"`     // 写回后的冷却期
	MinLoadFactor      int     `json:"min_load_factor"`
	MaxLoadFactor      int     `json:"max_load_factor"`
}

// FatalAction 是渠道反复出现认证类致命错误时的处置动作。
type FatalAction string

const (
	// FatalActionNone 只熔断，不做额外处置（默认）。
	FatalActionNone FatalAction = "none"
	// FatalActionPause 转为人工暂停：不再自动回池，等人来处理。
	FatalActionPause FatalAction = "pause"
	// FatalActionDisable 在 sub2api 里把账号置为停用状态，保留凭据。
	FatalActionDisable FatalAction = "disable"
	// FatalActionDelete 直接删除账号。
	//
	// 危险：sub2api 的账号接口对凭据做了脱敏，Guardian 读不到 api_key，
	// 因此删除后无法由 Guardian 重建，只能靠你自己的凭据来源恢复。
	FatalActionDelete FatalAction = "delete"
)

// Valid 报告处置动作是否合法。
func (a FatalAction) Valid() bool {
	switch a {
	case FatalActionNone, FatalActionPause, FatalActionDisable, FatalActionDelete:
		return true
	}
	return false
}

// FatalCleanup 控制认证失效渠道的自动清理。
//
// 默认整体关闭：这里的动作要么不可逆（删除），要么会脱离自动调度（暂停/停用），
// 必须由运维显式打开。
type FatalCleanup struct {
	Enabled bool        `json:"enabled"`
	Action  FatalAction `json:"action"`

	// Occurrences 是触发前需要累计的致命错误次数，
	// 在 Window 条最近样本里统计，避免一次抖动就误伤。
	Occurrences int `json:"occurrences"`
	Window      int `json:"window"`

	// MinFusedMinutes 要求渠道已经熔断满一段时间，给人工介入留出窗口。
	MinFusedMinutes int `json:"min_fused_minutes"`

	// MaxPerRound 限制每轮处置数量，防止一次配置错误清空整个池子。
	MaxPerRound int `json:"max_per_round"`

	// KeepLastInGroup 为真时绝不处置分组里最后一个渠道。
	KeepLastInGroup bool `json:"keep_last_in_group"`

	// OnlyAuthErrors 为真时只处置认证类错误（401/403/invalid key），
	// 余额不足、额度耗尽这类「充值就能恢复」的错误不参与。
	OnlyAuthErrors bool `json:"only_auth_errors"`

	// TriggerStatusCodes 是触发处置的 HTTP 状态码。
	//
	// 留空时按 OnlyAuthErrors 的语义走（认证类关键字 + 401/403）；
	// 填写后只认这些状态码，便于把 402、429 之类按自己的运维口径纳入或排除。
	TriggerStatusCodes []int `json:"trigger_status_codes"`
}

// Scaling 控制账号并发扩缩容。
type Scaling struct {
	Enabled              bool    `json:"enabled"`
	GlobalMaxConcurrency int     `json:"global_max_concurrency"`
	MinPerAccount        int     `json:"min_per_account"`
	MaxPerAccount        int     `json:"max_per_account"`
	ScaleUpRatio         float64 `json:"scale_up_ratio"`
	StepUp               int     `json:"step_up"`
	StepDown             int     `json:"step_down"`
	CooldownSeconds      int     `json:"cooldown_seconds"`
}

// AutoApply 控制哪些字段允许自动写回 sub2api，关闭后只产生建议。
type AutoApply struct {
	Schedulable bool `json:"schedulable"`
	Priority    bool `json:"priority"`
	LoadFactor  bool `json:"load_factor"`
	Concurrency bool `json:"concurrency"`
}

// Probe 控制主动探测。
type Probe struct {
	Enabled              bool   `json:"enabled"`
	IntervalSeconds      int    `json:"interval_seconds"`
	TimeoutSeconds       int    `json:"timeout_seconds"`
	Concurrency          int    `json:"concurrency"`
	Model                string `json:"model"`
	Prompt               string `json:"prompt"`
	SkipWhenTrafficFresh bool   `json:"skip_when_traffic_fresh"`
	TrafficFreshSeconds  int    `json:"traffic_fresh_seconds"`
}

// Traffic 控制真实流量样本采集。
type Traffic struct {
	Enabled              bool `json:"enabled"`
	RefreshSeconds       int  `json:"refresh_seconds"` // 每个渠道拉取真实流量的最小间隔
	LookbackMinutes      int  `json:"lookback_minutes"`
	MaxSamplesPerAccount int  `json:"max_samples_per_account"`
}

// UpstreamMultiplier 控制所有已开启实时倍率渠道的自动读取周期。
type UpstreamMultiplier struct {
	IntervalSeconds int `json:"interval_seconds"`
}

// UpstreamMultiplierBreaker 是单渠道的倍率上限熔断配置。
//
// Threshold 只和 Guardian 调度使用的上游倍率比较，不会修改 Sub2API 的计费配置。
type UpstreamMultiplierBreaker struct {
	Enabled   bool    `json:"enabled"`
	Threshold float64 `json:"threshold"`
}

// Classify 控制错误分类关键字。
type Classify struct {
	FatalPatterns      []string `json:"fatal_patterns"`
	GatewayStatusCodes []int    `json:"gateway_status_codes"`
}

// Policy 是全局策略。分组可以覆盖其中一部分。
type Policy struct {
	Strategy           Strategy           `json:"strategy"`
	Scoring            Scoring            `json:"scoring"`
	Breaker            Breaker            `json:"breaker"`
	Degrade            Degrade            `json:"degrade"`
	Recovery           Recovery           `json:"recovery"`
	Weights            Weights            `json:"weights"`
	Cleanup            FatalCleanup       `json:"cleanup"`
	Scaling            Scaling            `json:"scaling"`
	AutoApply          AutoApply          `json:"auto_apply"`
	Probe              Probe              `json:"probe"`
	Traffic            Traffic            `json:"traffic"`
	UpstreamMultiplier UpstreamMultiplier `json:"upstream_multiplier"`
	Classify           Classify           `json:"classify"`

	ManagedGroupMode    string   `json:"managed_group_mode"` // all | selected
	ManagedGroupIDs     []int64  `json:"managed_group_ids"`
	ManagedAccountTypes []string `json:"managed_account_types"` // 空表示不限类型
	ManagedPlatforms    []string `json:"managed_platforms"`     // 空表示不限平台
	ExcludedGroupIDs    []int64  `json:"excluded_group_ids"`    // 整组移出调度系统管控
	ExcludedAccountIDs  []int64  `json:"excluded_account_ids"`
	PausedAccountIDs    []int64  `json:"paused_account_ids"` // 人工暂停调度，仍受监控

	AccountTestModels map[string]string `json:"account_test_models"`

	// AccountMultipliers 是人工设置的调度倍率，键为账号 ID。
	//
	// 这是 Guardian 内部的调度口径，**不会写回 sub2api**，与网站计费无关。
	// 未设置的账号按类型取默认值（见 DefaultMultiplierFor）。
	AccountMultipliers map[string]float64 `json:"account_multipliers"`

	// AccountUpstreamMultiplierEnabled 控制 API Key 渠道是否使用 sub2api
	// 账号目录返回的最新倍率。只保存开启项，键为账号 ID。
	AccountUpstreamMultiplierEnabled map[string]bool `json:"account_upstream_multiplier_enabled"`

	// AccountUpstreamMultiplierBreakers 保存各渠道的倍率上限熔断配置。
	// 只有对应渠道开启实时倍率时才保留并生效。
	AccountUpstreamMultiplierBreakers map[string]UpstreamMultiplierBreaker `json:"account_upstream_multiplier_breakers"`
}

// GroupOverride 是分组级覆盖，nil 字段表示沿用全局。
type GroupOverride struct {
	Enabled            *bool     `json:"enabled,omitempty"`
	Strategy           *Strategy `json:"strategy,omitempty"`
	MinPoolSize        *int      `json:"min_pool_size,omitempty"`
	WeightBudget       *int      `json:"weight_budget,omitempty"`
	BalancedPriceRatio *float64  `json:"balanced_price_ratio,omitempty"`
	BreakerEnabled     *bool     `json:"breaker_enabled,omitempty"`
	RecoveryEnabled    *bool     `json:"recovery_enabled,omitempty"`
	WeightsEnabled     *bool     `json:"weights_enabled,omitempty"`
	ScalingEnabled     *bool     `json:"scaling_enabled,omitempty"`

	// 定时测试：分组可以有自己的测活节奏。核心分组测得勤一些，
	// 冷门或昂贵的分组测得稀疏一些，避免为低价值渠道浪费上游额度。
	ProbeEnabled         *bool   `json:"probe_enabled,omitempty"`
	ProbeIntervalSeconds *int    `json:"probe_interval_seconds,omitempty"`
	ProbeModel           *string `json:"probe_model,omitempty"`
}

// Default 返回一份符合 PRD 默认值的策略。
func Default() Policy {
	return Policy{
		Strategy: StrategyPrice,
		Scoring: Scoring{
			EventScores: EventScores{
				Perfect:         100,
				SlowTTFB:        65,
				UpstreamUnknown: 40,
				GatewayError:    25,
				QuotaExhausted:  15,
				ProbeFail:       10,
				Fatal:           0,
			},
			ShortWindow:  10,
			LongWindow:   60,
			LatestWeight: 0.5,
			ShortRatio:   0.7,
			SlowTTFBMs:   5000,
		},
		Breaker: Breaker{
			Enabled:        true,
			HardFatal:      true,
			HTTPWindow:     5,
			HTTPFailures:   3,
			HTTPScoreBelow: 60,
			// 默认只降级不熔断：5xx 与限流多是上游临时抖动，
			// 摘掉渠道等于减少可用容量，而降级已经能把流量挪开。
			HTTPDegradeOnly:    true,
			LatencyWindow:      10,
			LatencyOccurrences: 5,
			LatencyTTFBMs:      15000,
			LatencyDegradeOnly: true,
			MaxSwitchPerRound:  1,
			MinPoolSize:        1,
			MinPoolScore:       3,
			FusedCooldownSecs:  180,
			InstantStatusCodes: []int{},
		},
		Degrade: Degrade{
			Enabled:         true,
			ScoreThreshold:  75,
			PriorityStep:    10,
			LoadFactorRatio: 0.5,
			MinLoadFactor:   1,
		},
		Recovery: Recovery{
			Enabled:              true,
			ProbeIntervalSeconds: 180,
			TargetScore:          75,
			SuccessCount:         2,
			HoldSeconds:          60,
		},
		Weights: Weights{
			Enabled:            true,
			Budget:             400,
			MinPriority:        1,
			GateFloor:          40,
			PriceExp:           1,
			SpeedExp:           1,
			BalancedPriceRatio: 0.5,
			ChangeThreshold:    0.1,
			CooldownSeconds:    60,
			MinLoadFactor:      1,
			MaxLoadFactor:      100,
		},
		Cleanup: FatalCleanup{
			Enabled:            false,
			Action:             FatalActionPause,
			Occurrences:        3,
			Window:             5,
			MinFusedMinutes:    30,
			MaxPerRound:        1,
			KeepLastInGroup:    true,
			OnlyAuthErrors:     true,
			TriggerStatusCodes: []int{401, 403},
		},
		Scaling: Scaling{
			Enabled:              false,
			GlobalMaxConcurrency: 900,
			MinPerAccount:        3,
			MaxPerAccount:        250,
			ScaleUpRatio:         0.8,
			StepUp:               5,
			StepDown:             5,
			CooldownSeconds:      60,
		},
		AutoApply: AutoApply{
			Schedulable: true,
			Priority:    true,
			LoadFactor:  true,
			Concurrency: false,
		},
		Probe: Probe{
			Enabled:              true,
			IntervalSeconds:      300,
			TimeoutSeconds:       60,
			Concurrency:          4,
			Prompt:               "hi",
			SkipWhenTrafficFresh: true,
			TrafficFreshSeconds:  180,
		},
		Traffic: Traffic{
			Enabled:              true,
			RefreshSeconds:       60,
			LookbackMinutes:      120,
			MaxSamplesPerAccount: 60,
		},
		UpstreamMultiplier: UpstreamMultiplier{
			IntervalSeconds: 120,
		},
		Classify: Classify{
			FatalPatterns: []string{
				"invalid api key", "unauthorized", "forbidden", "authentication",
				"account not found", "no api key", "no access token",
				"insufficient", "balance", "quota exceeded", "usage limit",
				"credit", "expired",
			},
			GatewayStatusCodes: []int{429, 500, 502, 503, 504},
		},
		ManagedGroupMode:                  "all",
		ManagedGroupIDs:                   []int64{},
		ManagedAccountTypes:               []string{},
		ManagedPlatforms:                  []string{},
		ExcludedGroupIDs:                  []int64{},
		ExcludedAccountIDs:                []int64{},
		PausedAccountIDs:                  []int64{},
		AccountTestModels:                 map[string]string{},
		AccountMultipliers:                map[string]float64{},
		AccountUpstreamMultiplierEnabled:  map[string]bool{},
		AccountUpstreamMultiplierBreakers: map[string]UpstreamMultiplierBreaker{},
	}
}

// Normalize 把越界或缺省的字段修正为可用值，避免坏配置导致引擎行为异常。
func Normalize(p *Policy) {
	d := Default()

	if !p.Strategy.Valid() {
		p.Strategy = d.Strategy
	}

	s := &p.Scoring
	if s.EventScores == (EventScores{}) {
		s.EventScores = d.Scoring.EventScores
	}
	clampScore(&s.EventScores.Perfect, d.Scoring.EventScores.Perfect)
	clampScore(&s.EventScores.SlowTTFB, d.Scoring.EventScores.SlowTTFB)
	clampScore(&s.EventScores.UpstreamUnknown, d.Scoring.EventScores.UpstreamUnknown)
	clampScore(&s.EventScores.GatewayError, d.Scoring.EventScores.GatewayError)
	// 限流分值不接受 0：0 分低于回池目标分，会让限流中的渠道永远回不了池。
	// 用户想让限流更严厉可以调低到 1，但不能是 0 —— 那是「凭据失效」的语义。
	if s.EventScores.QuotaExhausted <= 0 || s.EventScores.QuotaExhausted > 100 {
		s.EventScores.QuotaExhausted = d.Scoring.EventScores.QuotaExhausted
	}
	clampScore(&s.EventScores.ProbeFail, d.Scoring.EventScores.ProbeFail)
	clampScore(&s.EventScores.Fatal, d.Scoring.EventScores.Fatal)
	positiveInt(&s.ShortWindow, d.Scoring.ShortWindow)
	positiveInt(&s.LongWindow, d.Scoring.LongWindow)
	if s.LongWindow < s.ShortWindow {
		s.LongWindow = s.ShortWindow
	}
	ratio(&s.LatestWeight, d.Scoring.LatestWeight)
	ratio(&s.ShortRatio, d.Scoring.ShortRatio)
	positiveInt(&s.SlowTTFBMs, d.Scoring.SlowTTFBMs)

	b := &p.Breaker
	positiveInt(&b.HTTPWindow, d.Breaker.HTTPWindow)
	positiveInt(&b.HTTPFailures, d.Breaker.HTTPFailures)
	if b.HTTPFailures > b.HTTPWindow {
		b.HTTPFailures = b.HTTPWindow
	}
	clampScore(&b.HTTPScoreBelow, d.Breaker.HTTPScoreBelow)
	positiveInt(&b.LatencyWindow, d.Breaker.LatencyWindow)
	positiveInt(&b.LatencyOccurrences, d.Breaker.LatencyOccurrences)
	if b.LatencyOccurrences > b.LatencyWindow {
		b.LatencyOccurrences = b.LatencyWindow
	}
	positiveInt(&b.LatencyTTFBMs, d.Breaker.LatencyTTFBMs)
	positiveInt(&b.MaxSwitchPerRound, d.Breaker.MaxSwitchPerRound)
	if b.MinPoolSize < 0 {
		b.MinPoolSize = d.Breaker.MinPoolSize
	}
	clampScore(&b.MinPoolScore, d.Breaker.MinPoolScore)
	nonNegativeInt(&b.FusedCooldownSecs, d.Breaker.FusedCooldownSecs)
	b.InstantStatusCodes = cleanStatusCodes(b.InstantStatusCodes)

	g := &p.Degrade
	clampScore(&g.ScoreThreshold, d.Degrade.ScoreThreshold)
	positiveInt(&g.PriorityStep, d.Degrade.PriorityStep)
	ratio(&g.LoadFactorRatio, d.Degrade.LoadFactorRatio)
	positiveInt(&g.MinLoadFactor, d.Degrade.MinLoadFactor)

	r := &p.Recovery
	positiveInt(&r.ProbeIntervalSeconds, d.Recovery.ProbeIntervalSeconds)
	clampScore(&r.TargetScore, d.Recovery.TargetScore)
	positiveInt(&r.SuccessCount, d.Recovery.SuccessCount)
	nonNegativeInt(&r.HoldSeconds, d.Recovery.HoldSeconds)

	w := &p.Weights
	positiveInt(&w.Budget, d.Weights.Budget)
	positiveInt(&w.MinPriority, d.Weights.MinPriority)
	clampScore(&w.GateFloor, d.Weights.GateFloor)
	positiveFloat(&w.PriceExp, d.Weights.PriceExp)
	positiveFloat(&w.SpeedExp, d.Weights.SpeedExp)
	ratio(&w.BalancedPriceRatio, d.Weights.BalancedPriceRatio)
	ratio(&w.ChangeThreshold, d.Weights.ChangeThreshold)
	nonNegativeInt(&w.CooldownSeconds, d.Weights.CooldownSeconds)
	positiveInt(&w.MinLoadFactor, d.Weights.MinLoadFactor)
	positiveInt(&w.MaxLoadFactor, d.Weights.MaxLoadFactor)
	if w.MaxLoadFactor < w.MinLoadFactor {
		w.MaxLoadFactor = w.MinLoadFactor
	}

	cu := &p.Cleanup
	if !cu.Action.Valid() {
		cu.Action = d.Cleanup.Action
	}
	positiveInt(&cu.Window, d.Cleanup.Window)
	positiveInt(&cu.Occurrences, d.Cleanup.Occurrences)
	if cu.Occurrences > cu.Window {
		cu.Occurrences = cu.Window
	}
	nonNegativeInt(&cu.MinFusedMinutes, d.Cleanup.MinFusedMinutes)
	positiveInt(&cu.MaxPerRound, d.Cleanup.MaxPerRound)
	cu.TriggerStatusCodes = cleanStatusCodes(cu.TriggerStatusCodes)

	sc := &p.Scaling
	positiveInt(&sc.GlobalMaxConcurrency, d.Scaling.GlobalMaxConcurrency)
	positiveInt(&sc.MinPerAccount, d.Scaling.MinPerAccount)
	positiveInt(&sc.MaxPerAccount, d.Scaling.MaxPerAccount)
	if sc.MaxPerAccount < sc.MinPerAccount {
		sc.MaxPerAccount = sc.MinPerAccount
	}
	ratio(&sc.ScaleUpRatio, d.Scaling.ScaleUpRatio)
	positiveInt(&sc.StepUp, d.Scaling.StepUp)
	positiveInt(&sc.StepDown, d.Scaling.StepDown)
	nonNegativeInt(&sc.CooldownSeconds, d.Scaling.CooldownSeconds)

	pb := &p.Probe
	positiveInt(&pb.IntervalSeconds, d.Probe.IntervalSeconds)
	if pb.IntervalSeconds < minProbeIntervalSeconds {
		pb.IntervalSeconds = minProbeIntervalSeconds
	}
	positiveInt(&pb.TimeoutSeconds, d.Probe.TimeoutSeconds)
	positiveInt(&pb.Concurrency, d.Probe.Concurrency)
	if pb.Concurrency > 32 {
		pb.Concurrency = 32
	}
	if strings.TrimSpace(pb.Prompt) == "" {
		pb.Prompt = d.Probe.Prompt
	}
	positiveInt(&pb.TrafficFreshSeconds, d.Probe.TrafficFreshSeconds)

	tf := &p.Traffic
	positiveInt(&tf.RefreshSeconds, d.Traffic.RefreshSeconds)
	positiveInt(&tf.LookbackMinutes, d.Traffic.LookbackMinutes)
	positiveInt(&tf.MaxSamplesPerAccount, d.Traffic.MaxSamplesPerAccount)
	if tf.MaxSamplesPerAccount > 200 {
		tf.MaxSamplesPerAccount = 200
	}

	um := &p.UpstreamMultiplier
	positiveInt(&um.IntervalSeconds, d.UpstreamMultiplier.IntervalSeconds)
	if um.IntervalSeconds < minUpstreamMultiplierIntervalSeconds {
		um.IntervalSeconds = minUpstreamMultiplierIntervalSeconds
	}

	cl := &p.Classify
	cl.FatalPatterns = cleanPatterns(cl.FatalPatterns)
	if len(cl.GatewayStatusCodes) == 0 {
		cl.GatewayStatusCodes = append([]int(nil), d.Classify.GatewayStatusCodes...)
	}

	if p.ManagedGroupMode != "selected" {
		p.ManagedGroupMode = "all"
	}
	if p.ManagedGroupIDs == nil {
		p.ManagedGroupIDs = []int64{}
	}
	p.ManagedAccountTypes = cleanPatterns(p.ManagedAccountTypes)
	p.ManagedPlatforms = cleanPatterns(p.ManagedPlatforms)
	if p.ExcludedGroupIDs == nil {
		p.ExcludedGroupIDs = []int64{}
	}
	if p.ExcludedAccountIDs == nil {
		p.ExcludedAccountIDs = []int64{}
	}
	if p.PausedAccountIDs == nil {
		p.PausedAccountIDs = []int64{}
	}
	if p.AccountTestModels == nil {
		p.AccountTestModels = map[string]string{}
	}
	if p.AccountMultipliers == nil {
		p.AccountMultipliers = map[string]float64{}
	}
	// 倍率必须是有限正数：0、负数、NaN 或无穷值都会破坏权重计算。
	for key, value := range p.AccountMultipliers {
		if !validMultiplier(value) {
			delete(p.AccountMultipliers, key)
		}
	}
	if p.AccountUpstreamMultiplierEnabled == nil {
		p.AccountUpstreamMultiplierEnabled = map[string]bool{}
	}
	// false 与缺省语义一致，移除它可以避免策略 JSON 无限制积累废项。
	for key, enabled := range p.AccountUpstreamMultiplierEnabled {
		if !enabled {
			delete(p.AccountUpstreamMultiplierEnabled, key)
		}
	}
	if p.AccountUpstreamMultiplierBreakers == nil {
		p.AccountUpstreamMultiplierBreakers = map[string]UpstreamMultiplierBreaker{}
	}
	for key, breaker := range p.AccountUpstreamMultiplierBreakers {
		if !p.AccountUpstreamMultiplierEnabled[key] || !validMultiplier(breaker.Threshold) {
			delete(p.AccountUpstreamMultiplierBreakers, key)
		}
	}
}

// ForGroup 把分组覆盖合并到全局策略上，返回该分组的生效策略。
func (p Policy) ForGroup(o *GroupOverride) Policy {
	out := p
	if o == nil {
		return out
	}
	if o.Strategy != nil && o.Strategy.Valid() {
		out.Strategy = *o.Strategy
	}
	if o.MinPoolSize != nil && *o.MinPoolSize >= 0 {
		out.Breaker.MinPoolSize = *o.MinPoolSize
	}
	if o.WeightBudget != nil && *o.WeightBudget > 0 {
		out.Weights.Budget = *o.WeightBudget
	}
	if o.BalancedPriceRatio != nil {
		v := *o.BalancedPriceRatio
		if v >= 0 && v <= 1 {
			out.Weights.BalancedPriceRatio = v
		}
	}
	if o.BreakerEnabled != nil {
		out.Breaker.Enabled = *o.BreakerEnabled
	}
	if o.RecoveryEnabled != nil {
		out.Recovery.Enabled = *o.RecoveryEnabled
	}
	if o.WeightsEnabled != nil {
		out.Weights.Enabled = *o.WeightsEnabled
	}
	if o.ScalingEnabled != nil {
		out.Scaling.Enabled = *o.ScalingEnabled
	}
	if o.ProbeEnabled != nil {
		out.Probe.Enabled = *o.ProbeEnabled
	}
	if o.ProbeIntervalSeconds != nil && *o.ProbeIntervalSeconds >= minProbeIntervalSeconds {
		out.Probe.IntervalSeconds = *o.ProbeIntervalSeconds
	}
	if o.ProbeModel != nil {
		if model := strings.TrimSpace(*o.ProbeModel); model != "" {
			out.Probe.Model = model
		}
	}
	return out
}

// GroupEnabled 报告某个分组是否参与守护。
func (p Policy) GroupEnabled(groupID int64, o *GroupOverride) bool {
	// 排除名单优先级最高：整组移出调度系统管控。
	if p.GroupExcluded(groupID) {
		return false
	}
	if o != nil && o.Enabled != nil && !*o.Enabled {
		return false
	}
	if p.ManagedGroupMode != "selected" {
		return true
	}
	for _, id := range p.ManagedGroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}

// GroupExcluded 报告某个分组是否被整组排除。
//
// 与「未勾选参与」的区别：排除是显式动作，不受 ManagedGroupMode 影响，
// 即使切回「全部分组参与」，被排除的分组仍然不受管控。
func (p Policy) GroupExcluded(groupID int64) bool {
	for _, id := range p.ExcludedGroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}

// AllGroupsExcluded 报告给定分组集合是否全部被排除。
//
// 用于判断渠道是否已完全脱离管辖范围：只要还剩一个未排除的分组，
// 渠道就仍在管辖内。空集合返回 false —— 未分组的渠道不算脱离范围。
func (p Policy) AllGroupsExcluded(groupIDs []int64) bool {
	if len(groupIDs) == 0 {
		return false
	}
	for _, groupID := range groupIDs {
		if !p.GroupExcluded(groupID) {
			return false
		}
	}
	return true
}

// AccountExcluded 报告账号是否在排除名单里。
func (p Policy) AccountExcluded(accountID int64) bool {
	for _, id := range p.ExcludedAccountIDs {
		if id == accountID {
			return true
		}
	}
	return false
}

// AccountPaused 报告账号是否被人工暂停调度。
//
// 与排除的区别：暂停的渠道仍会被探测和计分（便于观察何时可以放回），
// 只是不接流量，且不会因健康分回升被自动回池。
func (p Policy) AccountPaused(accountID int64) bool {
	for _, id := range p.PausedAccountIDs {
		if id == accountID {
			return true
		}
	}
	return false
}

// TypeManaged 报告账号类型是否在受管范围内（空名单表示不限）。
func (p Policy) TypeManaged(accountType string) bool {
	return matchesFilter(p.ManagedAccountTypes, accountType)
}

// PlatformManaged 报告账号平台是否在受管范围内（空名单表示不限）。
func (p Policy) PlatformManaged(platform string) bool {
	return matchesFilter(p.ManagedPlatforms, platform)
}

func matchesFilter(allowed []string, value string) bool {
	if len(allowed) == 0 {
		return true
	}
	value = strings.ToLower(strings.TrimSpace(value))
	for _, item := range allowed {
		if item == value {
			return true
		}
	}
	return false
}

func clampScore(v *float64, fallback float64) {
	if *v < 0 || *v > 100 {
		*v = fallback
	}
}

func ratio(v *float64, fallback float64) {
	if *v <= 0 || *v > 1 {
		*v = fallback
	}
}

func positiveInt(v *int, fallback int) {
	if *v <= 0 {
		*v = fallback
	}
}

func nonNegativeInt(v *int, fallback int) {
	if *v < 0 {
		*v = fallback
	}
}

func positiveFloat(v *float64, fallback float64) {
	if *v <= 0 {
		*v = fallback
	}
}

// cleanStatusCodes 去重并剔除非法状态码。
func cleanStatusCodes(items []int) []int {
	out := make([]int, 0, len(items))
	seen := map[int]struct{}{}
	for _, code := range items {
		if code < 100 || code > 599 {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

func cleanPatterns(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
