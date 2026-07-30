package scoring

import (
	"math"
	"sort"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

// Result 是一次健康分计算的完整输出。
type Result struct {
	Short           float64
	Long            float64
	Final           float64
	SampleCount     int
	ConsecutiveOK   int
	ConsecutiveFail int
	TTFBP50Ms       int64
	TTFBP95Ms       int64
	LatestEvent     domain.EventType
	LatestAt        time.Time
	FatalOverride   bool
}

// shortDecay 是短期分中非最新样本的几何衰减比。
const shortDecay = 0.5

// Compute 根据样本计算健康分。samples 必须按时间倒序（最新在前）。
//
// 短期分：取最近 ShortWindow 条，最新一条占 LatestWeight，其余按几何衰减分摊剩余权重。
// 长期分：取最近 LongWindow 条的算术平均。
// 最终分：短期分 × ShortRatio + 长期分 × (1 − ShortRatio)。
// 最新一条为致命错误时直接返回致命分（一票否决）。
func Compute(samples []domain.Sample, p policy.Policy) Result {
	out := Result{SampleCount: len(samples)}
	if len(samples) == 0 {
		return out
	}

	out.LatestEvent = samples[0].EventType
	out.LatestAt = samples[0].OccurredAt

	out.Short = shortScore(samples, p)
	out.Long = longScore(samples, p)
	out.Final = out.Short*p.Scoring.ShortRatio + out.Long*(1-p.Scoring.ShortRatio)

	if IsFatal(samples[0].EventType) {
		out.FatalOverride = true
		out.Final = p.Scoring.EventScores.Fatal
	}
	out.Final = clamp(out.Final, 0, 100)

	out.ConsecutiveOK, out.ConsecutiveFail = streaks(samples)
	out.TTFBP50Ms = percentileTTFB(samples, p.Scoring.LongWindow, 0.5)
	out.TTFBP95Ms = percentileTTFB(samples, p.Scoring.LongWindow, 0.95)
	return out
}

func shortScore(samples []domain.Sample, p policy.Policy) float64 {
	window := take(samples, p.Scoring.ShortWindow)
	if len(window) == 1 {
		return clamp(window[0].Score, 0, 100)
	}

	latestWeight := p.Scoring.LatestWeight
	rest := 1 - latestWeight

	// 先按几何衰减算出非最新样本的原始权重，再归一化到 rest。
	raw := make([]float64, len(window)-1)
	var rawSum float64
	for i := range raw {
		raw[i] = math.Pow(shortDecay, float64(i))
		rawSum += raw[i]
	}

	total := window[0].Score * latestWeight
	for i, sample := range window[1:] {
		total += sample.Score * rest * raw[i] / rawSum
	}
	return clamp(total, 0, 100)
}

func longScore(samples []domain.Sample, p policy.Policy) float64 {
	window := take(samples, p.Scoring.LongWindow)
	if len(window) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range window {
		sum += sample.Score
	}
	return clamp(sum/float64(len(window)), 0, 100)
}

// streaks 统计从最新样本往回的连续成功数与连续失败数（互斥，其中一个必为 0）。
func streaks(samples []domain.Sample) (okStreak, failStreak int) {
	if len(samples) == 0 {
		return 0, 0
	}
	failing := IsFailure(samples[0].EventType)
	for _, sample := range samples {
		if IsFailure(sample.EventType) != failing {
			break
		}
		if failing {
			failStreak++
		} else {
			okStreak++
		}
	}
	return okStreak, failStreak
}

// percentileTTFB 计算成功样本的首字时间分位值，无有效样本时返回 0。
func percentileTTFB(samples []domain.Sample, window int, q float64) int64 {
	values := make([]int64, 0, window)
	for _, sample := range take(samples, window) {
		if sample.TTFBMs > 0 && !IsFailure(sample.EventType) {
			values = append(values, sample.TTFBMs)
		}
	}
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	idx := int(math.Ceil(q*float64(len(values)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

// CountGatewayFailures 统计最近 window 条样本里的网关/限流/探测失败次数。
func CountGatewayFailures(samples []domain.Sample, window int) int {
	count := 0
	for _, sample := range take(samples, window) {
		switch sample.EventType {
		// 限流/额度**不**计入。
		//
		// 它已经通过扣分体现在健康分里（分低了权重自然降），不需要再推一把
		// 熔断判定。计入的话，一批渠道同时限流会连锁触发熔断 ——
		// 而限流是等一会就好的事，摘掉渠道反而白白损失容量。
		//
		// 想让 429 熔断的人可以显式配 breaker.instant_status_codes = [429]，
		// 那是明确的意图表达，与这里的默认口径不冲突。
		case domain.EventGatewayError, domain.EventProbeFail:
			count++
		}
	}
	return count
}

// CountSlowResponses 统计最近 window 条样本里首字时间超过阈值的次数。
func CountSlowResponses(samples []domain.Sample, window int, ttfbMs int) int {
	count := 0
	for _, sample := range take(samples, window) {
		if sample.TTFBMs > int64(ttfbMs) {
			count++
		}
	}
	return count
}

// HasFatal 报告最近 window 条样本里是否出现过致命错误。
func HasFatal(samples []domain.Sample, window int) bool {
	for _, sample := range take(samples, window) {
		if IsFatal(sample.EventType) {
			return true
		}
	}
	return false
}

func take(samples []domain.Sample, n int) []domain.Sample {
	if n <= 0 || n >= len(samples) {
		return samples
	}
	return samples[:n]
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
