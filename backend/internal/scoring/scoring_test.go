package scoring

import (
	"math"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

func testPolicy() policy.Policy {
	p := policy.Default()
	policy.Normalize(&p)
	return p
}

func TestClassify(t *testing.T) {
	p := testPolicy()

	cases := []struct {
		name string
		in   ClassifyInput
		want domain.EventType
	}{
		{"成功且快", ClassifyInput{Success: true, TTFBMs: 800}, domain.EventPerfect},
		{"成功但首字慢", ClassifyInput{Success: true, TTFBMs: 9000}, domain.EventSlowTTFB},
		{"401 认证失败", ClassifyInput{StatusCode: 401, Message: "Unauthorized"}, domain.EventFatal},
		{"403 禁止", ClassifyInput{StatusCode: 403}, domain.EventFatal},

		// 限流与额度独立成一类：它们等窗口重置或充值就能恢复，
		// 与「凭据废了」性质完全不同。混为致命会把健康分强制归零，
		// 而 0 分低于回池目标分，渠道再也回不了池。
		{"余额不足", ClassifyInput{Message: "insufficient balance"}, domain.EventQuotaExhausted},
		{"额度耗尽", ClassifyInput{Message: "You have hit your usage limit"}, domain.EventQuotaExhausted},
		{"429 限流", ClassifyInput{StatusCode: 429, Message: "Too Many Requests"}, domain.EventQuotaExhausted},
		{
			"sub2api 的 429 报文",
			ClassifyInput{Message: `API returned 429: {"error":{"type":"usage_limit_reached"}}`},
			domain.EventQuotaExhausted,
		},
		{"额度类但带凭据失效措辞按致命处理",
			ClassifyInput{Message: "unauthorized: credit exhausted"}, domain.EventFatal},
		{"502 网关", ClassifyInput{StatusCode: 502}, domain.EventGatewayError},
		{"文本里的 503", ClassifyInput{Message: "upstream returned 503: service unavailable"}, domain.EventGatewayError},
		{"探测超时", ClassifyInput{Timeout: true, Message: "context deadline exceeded"}, domain.EventProbeFail},
		{"连接被拒", ClassifyInput{Message: "dial tcp 127.0.0.1:8080: connection refused"}, domain.EventProbeFail},
		{"上游未知格式", ClassifyInput{Message: "unexpected upstream payload"}, domain.EventUpstreamUnknown},
		{"400 参数错误算未知", ClassifyInput{StatusCode: 400, Message: "bad request"}, domain.EventUpstreamUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.in, p); got != tc.want {
				t.Fatalf("Classify() = %v, 期望 %v", got, tc.want)
			}
		})
	}
}

// TestClassifyQuotaBeatsGateway 确认额度类错误归入可恢复的一类。
//
// 「429 + insufficient credit」以前被判为致命（健康分归 0、永久出局），
// 但它等窗口重置或充值就能恢复，应当只扣分。
func TestClassifyQuotaBeatsGateway(t *testing.T) {
	p := testPolicy()
	got := Classify(ClassifyInput{StatusCode: 429, Message: "insufficient credit"}, p)
	if got != domain.EventQuotaExhausted {
		t.Fatalf("Classify() = %v, 期望 quota_exhausted", got)
	}
}

// TestClassifyAuthBeatsQuota 确认凭据失效的优先级高于额度。
//
// 有些上游会在同一条报文里既说 unauthorized 又提 credit，
// 这种情况按更严重的凭据失效处理 —— 它不会自己好。
func TestClassifyAuthBeatsQuota(t *testing.T) {
	p := testPolicy()
	cases := []string{
		"unauthorized: your credit has run out",
		"invalid api key (quota also exceeded)",
	}
	for _, msg := range cases {
		if got := Classify(ClassifyInput{Message: msg}, p); got != domain.EventFatal {
			t.Fatalf("Classify(%q) = %v, 期望 fatal", msg, got)
		}
	}
}

// TestQuotaScoreIsNotZero 是这次修复的核心约束。
//
// 限流分值必须非零：0 分低于回池目标分（默认 75），
// 给 0 等于把限流中的渠道永久钉死在熔断状态。
func TestQuotaScoreIsNotZero(t *testing.T) {
	p := testPolicy()
	score := ScoreFor(domain.EventQuotaExhausted, p)
	if score <= 0 {
		t.Fatalf("限流分值 = %.1f, 必须大于 0，否则渠道永远回不了池", score)
	}
	if score >= ScoreFor(domain.EventPerfect, p) {
		t.Fatalf("限流分值 = %.1f, 不该高到与正常返回相当", score)
	}
}

// TestQuotaDoesNotZeroHealthScore 确认限流样本不会一票否决健康分。
func TestQuotaDoesNotZeroHealthScore(t *testing.T) {
	p := testPolicy()
	now := time.Now()

	// 最近一条是限流，之前都是成功。
	samples := []domain.Sample{
		{EventType: domain.EventQuotaExhausted, Score: ScoreFor(domain.EventQuotaExhausted, p), OccurredAt: now},
	}
	for i := 1; i <= 9; i++ {
		samples = append(samples, domain.Sample{
			EventType:  domain.EventPerfect,
			Score:      100,
			OccurredAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	result := Compute(samples, p)
	if result.FatalOverride {
		t.Fatal("限流不该触发致命一票否决")
	}
	if result.Final <= 0 {
		t.Fatalf("健康分 = %.1f, 限流不该把它压到 0", result.Final)
	}

	// 对照：同样位置换成致命错误，必须归零。
	samples[0].EventType = domain.EventFatal
	samples[0].Score = 0
	fatal := Compute(samples, p)
	if !fatal.FatalOverride || fatal.Final != 0 {
		t.Fatalf("致命错误仍应一票否决，实际 override=%v final=%.1f",
			fatal.FatalOverride, fatal.Final)
	}
}

func TestScoreFor(t *testing.T) {
	p := testPolicy()
	cases := map[domain.EventType]float64{
		domain.EventPerfect:         100,
		domain.EventSlowTTFB:        65,
		domain.EventUpstreamUnknown: 40,
		domain.EventGatewayError:    25,
		domain.EventProbeFail:       10,
		domain.EventFatal:           0,
	}
	for event, want := range cases {
		if got := ScoreFor(event, p); got != want {
			t.Fatalf("ScoreFor(%v) = %v, 期望 %v", event, got, want)
		}
	}
}

// newest 构造按时间倒序（最新在前）的样本序列。
func newest(events ...domain.EventType) []domain.Sample {
	p := testPolicy()
	base := time.Now()
	out := make([]domain.Sample, 0, len(events))
	for i, event := range events {
		out = append(out, domain.Sample{
			AccountID:  1,
			OccurredAt: base.Add(-time.Duration(i) * time.Minute),
			EventType:  event,
			Score:      ScoreFor(event, p),
			TTFBMs:     1000,
		})
	}
	return out
}

func TestComputeEmpty(t *testing.T) {
	got := Compute(nil, testPolicy())
	if got.SampleCount != 0 || got.Final != 0 {
		t.Fatalf("空样本应返回零值，实际 %+v", got)
	}
}

func TestComputeAllPerfect(t *testing.T) {
	got := Compute(newest(repeat(domain.EventPerfect, 12)...), testPolicy())
	if math.Abs(got.Final-100) > 1e-9 {
		t.Fatalf("Final = %v, 期望 100", got.Final)
	}
	if got.ConsecutiveOK != 12 || got.ConsecutiveFail != 0 {
		t.Fatalf("连续计数 = (%d, %d), 期望 (12, 0)", got.ConsecutiveOK, got.ConsecutiveFail)
	}
}

func TestComputeLatestWeightsHalf(t *testing.T) {
	p := testPolicy()
	// 最新一条 100 分，其余 9 条 0 分：短期分应恰好等于最新权重 50。
	samples := newest(append([]domain.EventType{domain.EventPerfect},
		repeat(domain.EventFatal, 9)...)...)
	got := Compute(samples, p)
	if math.Abs(got.Short-50) > 1e-9 {
		t.Fatalf("Short = %v, 期望 50", got.Short)
	}
	// 长期分是 10 条的均值：100/10 = 10。
	if math.Abs(got.Long-10) > 1e-9 {
		t.Fatalf("Long = %v, 期望 10", got.Long)
	}
	// 最终分 = 50*0.7 + 10*0.3 = 38。
	if math.Abs(got.Final-38) > 1e-9 {
		t.Fatalf("Final = %v, 期望 38", got.Final)
	}
}

func TestComputeFatalOverride(t *testing.T) {
	p := testPolicy()
	// 前面全是满分，最新一条致命错误：最终分必须被压到 0。
	samples := newest(append([]domain.EventType{domain.EventFatal},
		repeat(domain.EventPerfect, 20)...)...)
	got := Compute(samples, p)
	if !got.FatalOverride {
		t.Fatal("期望 FatalOverride 为 true")
	}
	if got.Final != 0 {
		t.Fatalf("Final = %v, 期望 0", got.Final)
	}
}

func TestComputeSingleSample(t *testing.T) {
	got := Compute(newest(domain.EventSlowTTFB), testPolicy())
	if math.Abs(got.Short-65) > 1e-9 {
		t.Fatalf("Short = %v, 期望 65", got.Short)
	}
	if math.Abs(got.Final-65) > 1e-9 {
		t.Fatalf("Final = %v, 期望 65（单样本时短期分与长期分相同）", got.Final)
	}
}

func TestComputeLongWindowCap(t *testing.T) {
	p := testPolicy()
	p.Scoring.LongWindow = 5
	// 最近 5 条满分，更早的 20 条 0 分：长期分只看窗口内。
	samples := newest(append(repeat(domain.EventPerfect, 5), repeat(domain.EventFatal, 20)...)...)
	got := Compute(samples, p)
	if math.Abs(got.Long-100) > 1e-9 {
		t.Fatalf("Long = %v, 期望 100", got.Long)
	}
}

func TestStreaksFailure(t *testing.T) {
	got := Compute(newest(domain.EventGatewayError, domain.EventProbeFail, domain.EventPerfect), testPolicy())
	if got.ConsecutiveFail != 2 || got.ConsecutiveOK != 0 {
		t.Fatalf("连续计数 = (%d, %d), 期望 (0, 2)", got.ConsecutiveOK, got.ConsecutiveFail)
	}
}

func TestPercentileTTFB(t *testing.T) {
	p := testPolicy()
	samples := []domain.Sample{
		{EventType: domain.EventPerfect, Score: 100, TTFBMs: 100},
		{EventType: domain.EventPerfect, Score: 100, TTFBMs: 200},
		{EventType: domain.EventPerfect, Score: 100, TTFBMs: 300},
		{EventType: domain.EventPerfect, Score: 100, TTFBMs: 400},
		{EventType: domain.EventGatewayError, Score: 25, TTFBMs: 99999}, // 失败样本不计入分位
	}
	got := Compute(samples, p)
	if got.TTFBP50Ms != 200 {
		t.Fatalf("P50 = %d, 期望 200", got.TTFBP50Ms)
	}
	if got.TTFBP95Ms != 400 {
		t.Fatalf("P95 = %d, 期望 400", got.TTFBP95Ms)
	}
}

func TestCountHelpers(t *testing.T) {
	samples := newest(
		domain.EventGatewayError,
		domain.EventPerfect,
		domain.EventProbeFail,
		domain.EventGatewayError,
		domain.EventPerfect,
		domain.EventGatewayError, // 第 6 条，窗口为 5 时不应计入
	)
	if got := CountGatewayFailures(samples, 5); got != 3 {
		t.Fatalf("CountGatewayFailures() = %d, 期望 3", got)
	}
	if got := CountGatewayFailures(samples, 6); got != 4 {
		t.Fatalf("CountGatewayFailures(6) = %d, 期望 4", got)
	}

	slow := []domain.Sample{
		{EventType: domain.EventSlowTTFB, TTFBMs: 20000},
		{EventType: domain.EventPerfect, TTFBMs: 900},
		{EventType: domain.EventSlowTTFB, TTFBMs: 16000},
	}
	if got := CountSlowResponses(slow, 10, 15000); got != 2 {
		t.Fatalf("CountSlowResponses() = %d, 期望 2", got)
	}

	if !HasFatal(newest(domain.EventPerfect, domain.EventFatal), 5) {
		t.Fatal("HasFatal() = false, 期望 true")
	}
	if HasFatal(newest(domain.EventPerfect, domain.EventFatal), 1) {
		t.Fatal("HasFatal(window=1) = true, 期望 false")
	}
}

func repeat(event domain.EventType, n int) []domain.EventType {
	out := make([]domain.EventType, n)
	for i := range out {
		out[i] = event
	}
	return out
}
