package policy

import (
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

// DefaultMultiplierFor 返回某账号类型的默认倍率。
//
// 判定规则刻意反向：只有明确是 API Key 的才按量付费，
// 其余（oauth、setup_token、以及未来新增的订阅类型）都当订阅账号优先用。
func DefaultMultiplierFor(accountType string) float64 {
	if _, ok := apiKeyTypes[strings.ToLower(strings.TrimSpace(accountType))]; ok {
		return DefaultAPIKeyMultiplier
	}
	return DefaultOAuthMultiplier
}

// MultiplierFor 返回某账号生效的调度倍率：人工设置优先，否则按类型取默认值。
func (p Policy) MultiplierFor(accountID int64, accountType string) float64 {
	if value, ok := p.AccountMultipliers[itoa(accountID)]; ok && value > 0 {
		return value
	}
	return DefaultMultiplierFor(accountType)
}

// HasManualMultiplier 报告某账号是否被人工设置过倍率。
func (p Policy) HasManualMultiplier(accountID int64) bool {
	value, ok := p.AccountMultipliers[itoa(accountID)]
	return ok && value > 0
}
