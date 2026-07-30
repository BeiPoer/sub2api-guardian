// Package scoring 实现错误分类与健康分计算。
//
// 这里全部是纯函数：输入样本与策略，输出分类结果和分数，便于单测覆盖。
package scoring

import (
	"regexp"
	"strconv"
	"strings"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

// ClassifyInput 是一次结果的原始信号。
type ClassifyInput struct {
	Success    bool
	StatusCode int
	Message    string
	TTFBMs     int64
	Timeout    bool
}

// statusInMessage 从错误文本里提取 3 位 HTTP 状态码，
// sub2api 的上游错误常以 "upstream returned 429: ..." 形式回传。
var statusInMessage = regexp.MustCompile(`\b(4\d{2}|5\d{2})\b`)

// Classify 把一次结果归类为事件类型。
//
// 判定顺序刻意固定：限流/额度 → 致命错误一票否决 → 网关 → 探测失败 →
// 上游未知 → 首字慢 → 完美。
//
// 限流排在致命之前：额度类报文（429 usage_limit_reached、insufficient balance）
// 往往同时命中致命关键字，而它们的可恢复性完全不同 —— 额度会随窗口重置恢复，
// 凭据失效不会。混为一类会让限流中的渠道被强制归零分，再也回不了池。
func Classify(in ClassifyInput, p policy.Policy) domain.EventType {
	status := in.StatusCode
	if status == 0 {
		status = statusFromMessage(in.Message)
	}

	// 成功的请求不做错误分类，先让它走下面的 Success 分支。
	if !in.Success && isQuotaExhausted(status, in.Message) {
		return domain.EventQuotaExhausted
	}
	if isFatal(status, in.Message, p.Classify.FatalPatterns) {
		return domain.EventFatal
	}
	if in.Success {
		if in.TTFBMs > int64(p.Scoring.SlowTTFBMs) {
			return domain.EventSlowTTFB
		}
		return domain.EventPerfect
	}
	if containsInt(p.Classify.GatewayStatusCodes, status) {
		return domain.EventGatewayError
	}
	if in.Timeout {
		return domain.EventProbeFail
	}
	if status == 0 {
		// 没有可识别的状态码：多半是连接失败或流中断。
		if looksLikeNetworkFailure(in.Message) {
			return domain.EventProbeFail
		}
		return domain.EventUpstreamUnknown
	}
	return domain.EventUpstreamUnknown
}

// ScoreFor 返回事件类型对应的分值。
func ScoreFor(event domain.EventType, p policy.Policy) float64 {
	s := p.Scoring.EventScores
	switch event {
	case domain.EventPerfect:
		return s.Perfect
	case domain.EventSlowTTFB:
		return s.SlowTTFB
	case domain.EventUpstreamUnknown:
		return s.UpstreamUnknown
	case domain.EventGatewayError:
		return s.GatewayError
	case domain.EventQuotaExhausted:
		return s.QuotaExhausted
	case domain.EventProbeFail:
		return s.ProbeFail
	case domain.EventFatal:
		return s.Fatal
	default:
		return s.UpstreamUnknown
	}
}

// IsFailure 报告事件类型是否算作失败（用于连续失败计数与错误率熔断）。
func IsFailure(event domain.EventType) bool {
	switch event {
	case domain.EventPerfect, domain.EventSlowTTFB:
		return false
	default:
		return true
	}
}

// IsFatal 报告事件类型是否为致命错误。
func IsFatal(event domain.EventType) bool { return event == domain.EventFatal }

// IsQuotaExhausted 报告事件类型是否为限流/额度耗尽。
func IsQuotaExhausted(event domain.EventType) bool {
	return event == domain.EventQuotaExhausted
}

// quotaPatterns 是「限流 / 额度耗尽」的特征。
//
// 这类错误等窗口重置或充值就能恢复，因此不该按致命错误一票否决健康分。
// 关键字取得比较全，因为各家上游的措辞差别很大
// （例如 Grok 回的是 "Grok Build usage balance exhausted"）。
var quotaPatterns = []string{
	"usage limit", "usage_limit", "quota", "rate limit", "rate_limit",
	"insufficient", "balance", "credit", "billing",
	"too many requests", "exceeded your current",
	"resource_exhausted", "resource exhausted", "overloaded",
}

// quotaStatusCodes 是明确表示限流或欠费的状态码。
//
// 402 是「需要付费」，403 不在其中 —— 那通常是权限问题。
var quotaStatusCodes = []int{402, 429}

// QuotaPatterns 返回限流关键字列表的副本。
//
// 暴露出来是为了让数据迁移复用同一份定义：早期迁移在 SQL 里手抄了一份，
// 结果漏了 "balance"，Grok 的欠费报文没被改判，渠道继续卡在 0 分。
func QuotaPatterns() []string {
	return append([]string(nil), quotaPatterns...)
}

// AuthFailurePatterns 返回凭据失效关键字列表的副本。
func AuthFailurePatterns() []string {
	return append([]string(nil), authFailurePatterns...)
}

// QuotaStatusCodes 返回限流状态码列表的副本。
func QuotaStatusCodes() []int {
	return append([]int(nil), quotaStatusCodes...)
}

// isQuotaExhausted 判定一次结果是否为限流或额度耗尽。
//
// 429（限流）与 402（需付费）直接认；其余靠关键字。
func isQuotaExhausted(status int, message string) bool {
	if containsInt(quotaStatusCodes, status) {
		return true
	}
	msg := strings.ToLower(message)
	if msg == "" {
		return false
	}
	// 凭据失效的措辞优先：有些报文会同时出现 "unauthorized" 与 "credit"，
	// 这种情况按更严重的凭据失效处理。
	for _, pattern := range authFailurePatterns {
		if strings.Contains(msg, pattern) {
			return false
		}
	}
	for _, pattern := range quotaPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

// authFailurePatterns 是「凭据本身失效」的特征。
//
// 与余额不足、额度耗尽区分开：后者充值或等窗口重置就能恢复，
// 不应触发自动清理。
var authFailurePatterns = []string{
	"invalid api key", "invalid_api_key", "invalid key", "invalid token",
	"unauthorized", "authentication", "authentication_error",
	"forbidden", "permission denied", "access denied",
	"account not found", "no api key", "no access token",
	"revoked", "disabled key", "key not found",
}

// IsAuthFailure 报告一条样本是否为凭据失效（而非欠费或限额）。
//
// 判定同时看状态码与文本：401/403 直接算，其余靠关键字。
func IsAuthFailure(sample domain.Sample) bool {
	if sample.EventType != domain.EventFatal {
		return false
	}
	if sample.StatusCode == 401 || sample.StatusCode == 403 {
		return true
	}
	msg := strings.ToLower(sample.Message)
	if msg == "" {
		return false
	}
	// 明显是余额/额度问题的先排除，避免「充值即可恢复」的渠道被清理。
	for _, quota := range []string{
		"insufficient", "balance", "quota", "credit", "billing",
		"usage limit", "rate limit", "exceeded your current",
	} {
		if strings.Contains(msg, quota) {
			return false
		}
	}
	for _, pattern := range authFailurePatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	if status := statusFromMessage(msg); status == 401 || status == 403 {
		return true
	}
	return false
}

// CountAuthFailures 统计最近 window 条样本里的凭据失效次数。
func CountAuthFailures(samples []domain.Sample, window int) int {
	count := 0
	for _, sample := range take(samples, window) {
		if IsAuthFailure(sample) {
			count++
		}
	}
	return count
}

// MatchStatus 报告单条样本是否命中给定状态码，并返回命中的那个码。
//
// 状态码优先取样本自带的 status_code，缺失时从错误文本里提取。
func MatchStatus(sample domain.Sample, codes []int) (int, bool) {
	if len(codes) == 0 {
		return 0, false
	}
	status := sample.StatusCode
	if status == 0 {
		status = statusFromMessage(strings.ToLower(sample.Message))
	}
	if status == 0 {
		return 0, false
	}
	for _, code := range codes {
		if code == status {
			return status, true
		}
	}
	return 0, false
}

// CountStatusMatches 统计最近 window 条样本里命中给定状态码的次数。
//
// 状态码优先取样本自带的 status_code，缺失时从错误文本里提取，
// 因为上游错误常以「upstream returned 401: ...」的形式回传。
func CountStatusMatches(samples []domain.Sample, window int, codes []int) int {
	if len(codes) == 0 {
		return 0
	}
	count := 0
	for _, sample := range take(samples, window) {
		if _, hit := MatchStatus(sample, codes); hit {
			count++
		}
	}
	return count
}

// CountFatal 统计最近 window 条样本里的致命错误次数。
func CountFatal(samples []domain.Sample, window int) int {
	count := 0
	for _, sample := range take(samples, window) {
		if IsFatal(sample.EventType) {
			count++
		}
	}
	return count
}

func isFatal(status int, message string, patterns []string) bool {
	if status == 401 || status == 402 || status == 403 {
		return true
	}
	msg := strings.ToLower(message)
	if msg == "" {
		return false
	}
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

func statusFromMessage(message string) int {
	match := statusInMessage.FindString(message)
	if match == "" {
		return 0
	}
	code, err := strconv.Atoi(match)
	if err != nil {
		return 0
	}
	return code
}

func looksLikeNetworkFailure(message string) bool {
	msg := strings.ToLower(message)
	for _, needle := range []string{
		"timeout", "deadline exceeded", "connection refused", "connection reset",
		"no such host", "eof", "broken pipe", "network is unreachable",
		"tls", "dial tcp", "canceled",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func containsInt(items []int, value int) bool {
	if value == 0 {
		return false
	}
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
