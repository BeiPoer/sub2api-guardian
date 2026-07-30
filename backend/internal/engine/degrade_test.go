package engine

import (
	"context"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

// TestGatewayErrorDegradesInsteadOfFusing 是「不要把正常渠道熔断」的核心回归。
//
// 分组渠道越多越好：5xx 与限流多半是上游临时抖动，摘掉渠道等于直接减少可用容量。
// 默认改为只降级 —— 渠道仍参与调度，但权重与优先级被压低，流量自然挪走。
func TestGatewayErrorDegradesInsteadOfFusing(t *testing.T) {
	p := policy.Default()
	// 用默认配置（HTTPDegradeOnly=true）。
	bad := makeChannel(1, p,
		domain.EventGatewayError, domain.EventGatewayError, domain.EventGatewayError,
		domain.EventPerfect, domain.EventPerfect)
	good := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, bad, good)
	decideOffline(r)

	if bad.desired.health == domain.HealthFused {
		t.Fatal("默认配置下网关错误应只降级，不该熔断")
	}
	if bad.desired.health != domain.HealthDegraded {
		t.Fatalf("状态 = %v, 期望 degraded", bad.desired.health)
	}
	// 关键：降级的渠道仍要接流量，否则等于变相熔断。
	if !bad.desired.schedulable {
		t.Fatal("降级渠道必须保持可调度")
	}
	// 但权重要明显低于健康渠道，流量才会挪走。
	if bad.desired.weight >= good.desired.weight {
		t.Fatalf("降级渠道权重 %.1f 应低于健康渠道 %.1f",
			bad.desired.weight, good.desired.weight)
	}
}

// TestLatencyDegradesInsteadOfFusing 同上，针对高延迟。
func TestLatencyDegradesInsteadOfFusing(t *testing.T) {
	p := policy.Default()
	slow := makeChannel(1, p, repeatEvent(domain.EventSlowTTFB, 10)...)
	for i := range slow.samples {
		slow.samples[i].TTFBMs = int64(p.Breaker.LatencyTTFBMs) + 5000
	}
	good := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, slow, good)
	decideOffline(r)

	if slow.desired.health == domain.HealthFused {
		t.Fatal("默认配置下高延迟应只降级，不该熔断")
	}
	if !slow.desired.schedulable {
		t.Fatal("降级渠道必须保持可调度")
	}
}

// TestQuotaExhaustedDoesNotFuse 覆盖你实际遇到的 429 场景。
//
// sub2api 的 429 报文是 usage_limit_reached，以前会命中致命关键字 "usage limit"，
// 被判为致命错误 → 健康分强制归 0 → 而 0 分低于回池目标分 → 永久出局。
func TestQuotaExhaustedDoesNotFuse(t *testing.T) {
	p := policy.Default()
	ch := makeChannel(1, p, repeatEvent(domain.EventQuotaExhausted, 5)...)
	spare := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, ch, spare)
	decideOffline(r)

	if ch.desired.health == domain.HealthFused {
		t.Fatal("限流/额度耗尽不该熔断：等窗口重置就能恢复")
	}
	// 健康分不能是 0，否则永远达不到回池目标分。
	if ch.score.Final <= 0 {
		t.Fatalf("限流渠道健康分 = %.1f, 不该被压到 0（会永久出局）", ch.score.Final)
	}
	if ch.score.FatalOverride {
		t.Fatal("限流不该触发致命一票否决")
	}
}

// TestQuotaNeverCountsTowardErrorRate 确认 429 不参与错误率熔断判定。
//
// 早期实现把限流也算进 CountGatewayFailures，于是一批渠道同时限流会连锁触发
// 熔断判定 —— 而限流是等一会就好的事，摘掉渠道白白损失容量。
//
// 限流已经通过扣分体现在健康分里（分低了权重自然降），不需要再推一把熔断。
func TestQuotaNeverCountsTowardErrorRate(t *testing.T) {
	p := policy.Default()
	p.Breaker.MinPoolSize = 0
	// 即使显式打开熔断（关掉只降级），限流也不该触发错误率熔断。
	p.Breaker.HTTPDegradeOnly = false
	p.Breaker.LatencyDegradeOnly = false

	ch := makeChannel(1, p, repeatEvent(domain.EventQuotaExhausted, 5)...)
	spare := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, ch, spare)
	decideOffline(r)

	if ch.desired.health == domain.HealthFused {
		t.Fatal("限流不该触发错误率熔断")
	}
}

// TestRateLimitNeverLeavesPool 是「限流渠道绝不摘掉」的硬约束回归。
//
// 即使用户把 429 写进 instant_status_codes，限流渠道也必须留在池子里。
// 这不是保守，而是因为 sub2api 自己已经处理了限流：上游返回 429 时它写入
// `rate_limit_reset_at`，选路查询直接排除该账号，窗口一过自动纳入
// （account_repo.go:1123、account.go:156）。
//
// Guardian 若改 schedulable，就把「到点自动恢复」换成了「要等恢复探测跑成功
// 才回来」（默认 180 秒）。高并发时这段空窗意味着可用容量凭空少一截，
// 而那正是最扛不住的时候。
func TestRateLimitNeverLeavesPool(t *testing.T) {
	p := policy.Default()
	p.Breaker.MinPoolSize = 0
	// 刻意把 429 配进「见到即熔断」，并关掉只降级 —— 都不该改变结果。
	p.Breaker.InstantStatusCodes = []int{429}
	p.Breaker.HTTPDegradeOnly = false
	p.Breaker.LatencyDegradeOnly = false

	ch := makeChannel(1, p, repeatEvent(domain.EventQuotaExhausted, 5)...)
	for i := range ch.samples {
		ch.samples[i].StatusCode = 429
	}
	spare := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, ch, spare)
	decideOffline(r)

	if ch.desired.health == domain.HealthFused {
		t.Fatalf("限流渠道绝不能熔断，实际 %v", ch.desired.health)
	}
	if !ch.desired.schedulable {
		t.Fatal("限流渠道必须保持可调度：限流一结束就能立刻承接流量")
	}
	// 权重要被压低，流量自然挪走 —— 这是「降级而非摘除」的正确形态。
	if ch.desired.weight >= spare.desired.weight {
		t.Fatalf("限流渠道权重 %.1f 应低于健康渠道 %.1f",
			ch.desired.weight, spare.desired.weight)
	}
}

// TestRateLimitNeverCleanedUp 确认限流渠道不会被自动处置（暂停/停用/删除）。
//
// 处置动作都会让渠道脱离调度，与上面同理：sub2api 已经在管限流了。
func TestRateLimitNeverCleanedUp(t *testing.T) {
	fake := newCleanupFake()
	eng, _ := cleanupEngine(t, fake)
	p := enabledCleanup(policy.FatalActionDelete)
	// 即使把 429 配成触发码，也不该处置。
	p.Cleanup.TriggerStatusCodes = []int{429}
	p.Cleanup.Occurrences = 1
	p.Cleanup.MinFusedMinutes = 0
	p.Cleanup.KeepLastInGroup = false

	limited := authFailChannel(101, p, 5, time.Hour)
	for i := range limited.samples {
		limited.samples[i].EventType = domain.EventQuotaExhausted
		limited.samples[i].StatusCode = 429
		limited.samples[i].Message = "429 usage limit reached"
	}
	limited.desired.health = domain.HealthDegraded
	spare := authFailChannel(102, p, 0, 0)
	spare.desired.health = domain.HealthHealthy

	if got := eng.applyCleanup(context.Background(), cleanupRound(p, limited, spare)); got != 0 {
		t.Fatalf("限流渠道不该被处置，实际处置 %d 个", got)
	}
	if fake.wasDeleted(101) {
		t.Fatal("限流渠道被删除了 —— 限流是暂时的，删掉不可逆")
	}
	if fake.wasUnschedulable(101) {
		t.Fatal("限流渠道被摘掉了流量")
	}
}

// TestInstantStatusCodeStillFuses 确认「见到即熔断」不受降级开关影响。
//
// 那是用户显式列出的状态码，意图明确 —— 401 凭据废了，继续打没意义。
func TestInstantStatusCodeStillFuses(t *testing.T) {
	p := policy.Default()
	p.Breaker.MinPoolSize = 0
	p.Breaker.InstantStatusCodes = []int{401}
	// 两个降级开关都开着，也不该影响立即熔断。
	p.Breaker.HTTPDegradeOnly = true
	p.Breaker.LatencyDegradeOnly = true

	ch := makeChannel(1, p, repeatEvent(domain.EventFatal, 3)...)
	for i := range ch.samples {
		ch.samples[i].StatusCode = 401
	}
	spare := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, ch, spare)
	decideOffline(r)

	if ch.desired.health != domain.HealthFused {
		t.Fatalf("命中「见到即熔断」状态码应熔断，实际 %v", ch.desired.health)
	}
}

// TestDegradeOnlyStillRespectsHardFatal 确认凭据失效仍会被摘掉。
//
// 「渠道越多越好」不等于「什么都不摘」：凭据废了的渠道留着只会持续报错。
func TestDegradeOnlyStillRespectsHardFatal(t *testing.T) {
	p := policy.Default()
	p.Breaker.MinPoolSize = 0
	p.Breaker.HardFatal = true
	p.Breaker.HTTPDegradeOnly = true
	p.Breaker.LatencyDegradeOnly = true

	dead := makeChannel(1, p, repeatEvent(domain.EventFatal, 3)...)
	spare := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, dead, spare)
	decideOffline(r)

	if dead.desired.health != domain.HealthFused {
		t.Fatalf("致命错误（凭据失效）仍应熔断，实际 %v", dead.desired.health)
	}
}
