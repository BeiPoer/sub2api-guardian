package policy

import (
	"math"
	"strconv"
	"strings"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// 渠道倍率的默认值。
//
// 倍率是 Guardian 内部的调度权重口径，**不写回 sub2api、与网站计费无关**。
// 价格优先策略只看它：倍率越低越优先。
const (
	// DefaultOAuthMultiplier 是账号类型渠道（OAuth / SetupToken 等）的默认倍率。
	// 这类渠道通常是包月订阅，边际成本极低，因此默认优先用。
	DefaultOAuthMultiplier = 0.01

	// DefaultAPIKeyMultiplier 是 API Key 类型渠道的默认倍率，按量付费。
	DefaultAPIKeyMultiplier = 1.0
)

// apiKeyTypes 是按量付费的账号类型，其余类型都按订阅账号处理。
var apiKeyTypes = map[string]struct{}{
	"apikey":  {},
	"api_key": {},
	"key":     {},
}

const (
	MultiplierSourceDefault          = "default"
	MultiplierSourceManual           = "manual"
	MultiplierSourceLinked           = "linked"
	MultiplierSourceUpstream         = "upstream"
	MultiplierSourceUpstreamFallback = "upstream_fallback"
)

// IsAPIKeyType 报告账号类型是否属于按量付费的 API Key 渠道。
func IsAPIKeyType(accountType string) bool {
	_, ok := apiKeyTypes[strings.ToLower(strings.TrimSpace(accountType))]
	return ok
}

func validMultiplier(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// DefaultMultiplierFor 返回某账号类型的默认倍率。
//
// 判定规则刻意反向：只有明确是 API Key 的才按量付费，
// 其余（oauth、setup_token、以及未来新增的订阅类型）都当订阅账号优先用。
func DefaultMultiplierFor(accountType string) float64 {
	if IsAPIKeyType(accountType) {
		return DefaultAPIKeyMultiplier
	}
	return DefaultOAuthMultiplier
}

// ResolveMultiplier 保留旧调用口径，供不持有倍率快照的兼容代码使用。
// 新的运行链路使用 ResolveMultiplierSnapshot。
func (p Policy) ResolveMultiplier(accountID int64, accountType string, upstream float64) (float64, string) {
	if value, ok := p.LinkedMultiplier(accountID); ok {
		return value, MultiplierSourceLinked
	}
	if p.UpstreamMultiplierEnabled(accountID, accountType) {
		if validMultiplier(upstream) {
			return upstream, MultiplierSourceUpstream
		}
		return DefaultAPIKeyMultiplier, MultiplierSourceUpstreamFallback
	}
	if value, ok := p.ManualMultiplier(accountID); ok {
		return value, MultiplierSourceManual
	}
	return DefaultMultiplierFor(accountType), MultiplierSourceDefault
}

// ResolveMultiplierSnapshot 使用真正向 API Key 上游读取并持久化的倍率快照。
// 从未成功读取过时沿用 Sub2API 账号当前倍率；读取失败不会删除旧快照。
func (p Policy) ResolveMultiplierSnapshot(
	accountID int64,
	accountType string,
	accountMultiplier float64,
	upstreamSnapshot float64,
	hasSnapshot bool,
) (float64, string) {
	if value, ok := p.LinkedMultiplier(accountID); ok {
		return value, MultiplierSourceLinked
	}
	if p.UpstreamMultiplierEnabled(accountID, accountType) {
		if hasSnapshot && validMultiplier(upstreamSnapshot) {
			return upstreamSnapshot, MultiplierSourceUpstream
		}
		if validMultiplier(accountMultiplier) {
			return accountMultiplier, MultiplierSourceUpstreamFallback
		}
		return DefaultAPIKeyMultiplier, MultiplierSourceUpstreamFallback
	}
	if value, ok := p.ManualMultiplier(accountID); ok {
		return value, MultiplierSourceManual
	}
	return DefaultMultiplierFor(accountType), MultiplierSourceDefault
}

// MultiplierFor 保留原有调用口径：不提供上游倍率时解析本地配置。
func (p Policy) MultiplierFor(accountID int64, accountType string) float64 {
	if value, ok := p.LinkedMultiplier(accountID); ok {
		return value
	}
	if value, ok := p.ManualMultiplier(accountID); ok {
		return value
	}
	return DefaultMultiplierFor(accountType)
}

// HasManualMultiplier 报告某账号是否被人工设置过倍率。
func (p Policy) HasManualMultiplier(accountID int64) bool {
	_, ok := p.ManualMultiplier(accountID)
	return ok
}

// ManualMultiplier 返回某账号保存的人工倍率。
func (p Policy) ManualMultiplier(accountID int64) (float64, bool) {
	value, ok := p.AccountMultipliers[itoa(accountID)]
	return value, ok && validMultiplier(value)
}

// LinkedMultiplier 返回渠道管理按凭据匹配得到的调度倍率。
func (p Policy) LinkedMultiplier(accountID int64) (float64, bool) {
	value, ok := p.AccountLinkedMultipliers[itoa(accountID)]
	return value, ok && validMultiplier(value)
}

// UpstreamMultiplierEnabled 报告某 API Key 账号是否开启实时倍率。
// 类型校验放在读取路径上，避免被手工篡改的策略影响 OAuth 渠道。
func (p Policy) UpstreamMultiplierEnabled(accountID int64, accountType string) bool {
	return IsAPIKeyType(accountType) && p.AccountUpstreamMultiplierEnabled[itoa(accountID)]
}

// UpstreamMultiplierBreakerFor 返回渠道保存的倍率上限配置。
// 自动倍率关闭或渠道不是 API Key 类型时，配置一律不生效。
func (p Policy) UpstreamMultiplierBreakerFor(
	accountID int64,
	accountType string,
) (UpstreamMultiplierBreaker, bool) {
	if !p.UpstreamMultiplierEnabled(accountID, accountType) {
		return UpstreamMultiplierBreaker{}, false
	}
	breaker, ok := p.AccountUpstreamMultiplierBreakers[itoa(accountID)]
	return breaker, ok && validMultiplier(breaker.Threshold)
}
