package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/scoring"
)

// buildTestRound 构造一个只含单分组的调度上下文，便于验证决策逻辑。
func buildTestRound(t *testing.T, p policy.Policy, channels ...*channel) *round {
	t.Helper()
	policy.Normalize(&p)

	group := domain.Group{ID: 1, Name: "测试分组", RateMultiplier: 1}
	r := &round{
		now:          time.Now(),
		global:       p,
		overrides:    map[int64]*policy.GroupOverride{},
		groups:       []domain.Group{group},
		groupByID:    map[int64]domain.Group{1: group},
		byAccountID:  map[int64]*channel{},
		groupMembers: map[int64][]*channel{},
		softFuses:    map[int64]int{},
	}
	for _, ch := range channels {
		ch.pol = p
		ch.primaryGroup = 1
		ch.groupIDs = []int64{1}
		ch.state.AccountID = ch.account.ID
		if ch.state.Health == "" {
			ch.state.Health = domain.HealthHealthy
		}
		r.channels = append(r.channels, ch)
		r.byAccountID[ch.account.ID] = ch
		r.groupMembers[1] = append(r.groupMembers[1], ch)
	}
	return r
}

// makeChannel 构造一个渠道，samples 按最新在前给出。
func makeChannel(id int64, p policy.Policy, events ...domain.EventType) *channel {
	policy.Normalize(&p)
	base := time.Now()
	samples := make([]domain.Sample, 0, len(events))
	for i, event := range events {
		samples = append(samples, domain.Sample{
			AccountID:  id,
			OccurredAt: base.Add(-time.Duration(i) * time.Minute),
			EventType:  event,
			Score:      scoring.ScoreFor(event, p),
			TTFBMs:     1000,
		})
	}
	ch := &channel{
		account: domain.Account{
			ID:             id,
			Name:           "渠道" + itoa(id),
			Status:         "active",
			Priority:       10,
			Concurrency:    10,
			RateMultiplier: 1,
			Schedulable:    true,
		},
		samples: samples,
		score:   scoring.Compute(samples, p),
	}
	return ch
}

func withLoadFactor(ch *channel, value int) *channel {
	ch.account.LoadFactor = &value
	return ch
}

// decideOffline 跑一遍不涉及网络与存储的决策链。
func decideOffline(r *round) {
	for _, ch := range r.channels {
		initDesired(ch)
		applyExclusion(ch)
		applyPause(ch)
	}
	softBreakerPass(r)
	for _, ch := range r.channels {
		gradeHealth(ch)
	}
	applyWeights(r)
	applyScaling(r)
}

// softBreakerPass 直接调用真实的熔断实现。
//
// 早期版本在这里复制了一份判定逻辑，结果生产代码改了保底口径后测试没跟上，
// 断言的是过时行为。直接复用真实函数，测试才有意义 ——
// 告警事件只会写进 round.alerts，不落库，因此不需要 store。
func softBreakerPass(r *round) {
	e := &Engine{}
	e.applyHardBreaker(r)
	e.applySoftBreaker(r)
}

func TestHardBreakerOnFatal(t *testing.T) {
	p := policy.Default()
	good := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	bad := makeChannel(2, p, append([]domain.EventType{domain.EventFatal},
		repeatEvent(domain.EventPerfect, 9)...)...)

	r := buildTestRound(t, p, good, bad)
	decideOffline(r)

	if bad.desired.health != domain.HealthFused {
		t.Fatalf("致命错误渠道状态 = %v, 期望 fused", bad.desired.health)
	}
	if bad.desired.schedulable {
		t.Fatal("致命错误渠道应被停止调度")
	}
	if good.desired.health != domain.HealthHealthy {
		t.Fatalf("健康渠道状态 = %v, 期望 healthy", good.desired.health)
	}
}

func TestSoftBreakerOnErrorRate(t *testing.T) {
	p := policy.Default()
	// 显式关掉「只降级不熔断」：本用例测的是熔断机制本身。
	// 默认值是 true —— 网关错误多为上游临时抖动，摘掉渠道等于减少可用容量。
	p.Breaker.HTTPDegradeOnly = false
	// 最近 5 次里 3 次网关错误，且健康分低于 60。
	bad := makeChannel(1, p,
		domain.EventGatewayError, domain.EventGatewayError, domain.EventGatewayError,
		domain.EventPerfect, domain.EventPerfect)
	good := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)
	spare := makeChannel(3, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, bad, good, spare)
	decideOffline(r)

	if bad.desired.health != domain.HealthFused {
		t.Fatalf("高错误率渠道状态 = %v (分数 %.1f), 期望 fused",
			bad.desired.health, bad.score.Final)
	}
}

func TestSoftBreakerOnLatency(t *testing.T) {
	p := policy.Default()
	// 同上：默认延迟超标只降级，这里显式打开熔断来验证机制。
	p.Breaker.LatencyDegradeOnly = false
	slow := makeChannel(1, p, repeatEvent(domain.EventSlowTTFB, 10)...)
	for i := range slow.samples {
		slow.samples[i].TTFBMs = 20000
	}
	slow.score = scoring.Compute(slow.samples, normalized(p))

	good := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)
	spare := makeChannel(3, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, slow, good, spare)
	decideOffline(r)

	if slow.desired.health != domain.HealthFused {
		t.Fatalf("高延迟渠道状态 = %v, 期望 fused", slow.desired.health)
	}
}

func TestMinPoolKeepsSurvivor(t *testing.T) {
	p := policy.Default()
	p.Breaker.HTTPDegradeOnly = false // 本用例验证保底路径，需要真的走熔断判定
	// 组内只有一个渠道，且它满足软熔断条件：必须保底强留而不是熔断。
	only := makeChannel(1, p,
		domain.EventGatewayError, domain.EventGatewayError, domain.EventGatewayError,
		domain.EventGatewayError, domain.EventGatewayError)

	r := buildTestRound(t, p, only)
	decideOffline(r)

	if only.desired.health != domain.HealthSurvivor {
		t.Fatalf("唯一渠道状态 = %v, 期望 survivor", only.desired.health)
	}
	if !only.desired.schedulable {
		t.Fatal("保底渠道必须保持可调度，否则分组断供")
	}
}

// TestHardBreakerKeepsSurvivor 是保底的核心回归：
// 一批渠道同时 401 时，分组不能被打空 —— 必须留下最后一个。
func TestHardBreakerKeepsSurvivor(t *testing.T) {
	p := policy.Default()

	// 组内两个渠道全部致命错误。
	fatal := func(id int64) *channel {
		return makeChannel(id, p, append([]domain.EventType{domain.EventFatal},
			repeatEvent(domain.EventFatal, 4)...)...)
	}
	a, b := fatal(1), fatal(2)

	r := buildTestRound(t, p, a, b)
	e := &Engine{}
	for _, ch := range r.channels {
		initDesired(ch)
	}
	e.applyHardBreaker(r)

	fused, survivors := 0, 0
	for _, ch := range r.channels {
		switch ch.desired.health {
		case domain.HealthFused:
			fused++
		case domain.HealthSurvivor:
			survivors++
		}
	}
	if survivors != 1 {
		t.Fatalf("保底渠道数 = %d, 期望 1（分组不能被 401 打空）", survivors)
	}
	if fused != 1 {
		t.Fatalf("熔断渠道数 = %d, 期望 1", fused)
	}
}

// TestHardBreakerAllFatalSingleChannel 验证组内唯一渠道 401 时也会被保底强留。
func TestHardBreakerAllFatalSingleChannel(t *testing.T) {
	p := policy.Default()
	only := makeChannel(1, p, repeatEvent(domain.EventFatal, 5)...)

	r := buildTestRound(t, p, only)
	e := &Engine{}
	initDesired(only)
	e.applyHardBreaker(r)

	if only.desired.health != domain.HealthSurvivor {
		t.Fatalf("唯一渠道状态 = %v, 期望 survivor（即使 401 也要留）", only.desired.health)
	}
	if !only.desired.schedulable {
		t.Fatal("保底渠道必须保持可调度")
	}
}

// TestHardBreakerFusesWhenPoolHasSpare 验证保底不会妨碍正常熔断：
// 组内还有健康渠道时，401 的渠道该熔断就熔断。
func TestHardBreakerFusesWhenPoolHasSpare(t *testing.T) {
	p := policy.Default()
	bad := makeChannel(1, p, repeatEvent(domain.EventFatal, 5)...)
	good := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, bad, good)
	e := &Engine{}
	for _, ch := range r.channels {
		initDesired(ch)
	}
	e.applyHardBreaker(r)

	if bad.desired.health != domain.HealthFused {
		t.Fatalf("有健康备用时应正常熔断，实际 %v", bad.desired.health)
	}
	if good.desired.health == domain.HealthFused {
		t.Fatal("健康渠道被误熔断")
	}
}

// TestMultiGroupHardBreakerProtectsEveryGroup verifies that an account-level
// schedulable change cannot leave any managed group without a survivor.
func TestMultiGroupHardBreakerProtectsEveryGroup(t *testing.T) {
	p := policy.Default()
	bad := makeChannel(1, p, repeatEvent(domain.EventFatal, 5)...)
	spare := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, bad, spare)
	second := domain.Group{ID: 2, Name: "第二分组", RateMultiplier: 1}
	r.groups = append(r.groups, second)
	r.groupByID[second.ID] = second
	r.groupMembers[second.ID] = []*channel{bad}
	bad.groupIDs = []int64{1, 2}

	for _, ch := range r.channels {
		initDesired(ch)
	}
	(&Engine{}).applyHardBreaker(r)

	if bad.desired.health != domain.HealthSurvivor {
		t.Fatalf("多分组账号状态 = %v，期望 survivor", bad.desired.health)
	}
	if !bad.desired.schedulable {
		t.Fatal("账号是第二分组唯一成员，必须保持可调度")
	}
}

// TestAliveCountExcludingIgnoresZeroScore 验证保底判定的口径：
// 致命错误渠道本就不在可用池，不该因为「减 1」而误判成即将断供。
func TestAliveCountExcludingIgnoresZeroScore(t *testing.T) {
	p := policy.Default()
	policy.Normalize(&p)

	dead := makeChannel(1, p, repeatEvent(domain.EventFatal, 5)...)
	alive := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)
	for _, ch := range []*channel{dead, alive} {
		ch.pol = p
		initDesired(ch)
	}
	members := []*channel{dead, alive}

	if got := aliveCount(members, p.Breaker.MinPoolScore, time.Now()); got != 1 {
		t.Fatalf("可用渠道数 = %d, 期望 1（致命错误渠道不计入）", got)
	}
	// 拿掉那个本来就不可用的渠道，剩余数量不该变化。
	if got := aliveCountExcluding(members, p.Breaker.MinPoolScore, dead.account.ID, time.Now()); got != 1 {
		t.Fatalf("排除致命渠道后 = %d, 期望仍为 1", got)
	}
	// 拿掉健康的那个才会归零。
	if got := aliveCountExcluding(members, p.Breaker.MinPoolScore, alive.account.ID, time.Now()); got != 0 {
		t.Fatalf("排除健康渠道后 = %d, 期望 0", got)
	}
}

// statusSample 构造一条带指定状态码的失败样本。
func statusSample(id int64, status int) domain.Sample {
	return domain.Sample{
		AccountID:  id,
		OccurredAt: time.Now(),
		EventType:  domain.EventGatewayError,
		Score:      25,
		StatusCode: status,
		Message:    fmt.Sprintf("upstream returned %d", status),
	}
}

// TestInstantFuseStatusCodes 验证「见到即熔断」的状态码：一次就够，不累计。
func TestInstantFuseStatusCodes(t *testing.T) {
	p := policy.Default()
	p.Breaker.InstantStatusCodes = []int{402}
	policy.Normalize(&p)

	// 只有一次 402：常规错误率判定不会触发（默认要 5 中 3 次）。
	instant := makeChannel(1, p, domain.EventPerfect)
	instant.samples = append([]domain.Sample{statusSample(1, 402)}, instant.samples...)
	instant.score = scoring.Compute(instant.samples, p)

	spare := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)
	other := makeChannel(3, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, instant, spare, other)
	decideOffline(r)

	if instant.desired.health != domain.HealthFused {
		t.Fatalf("命中即时熔断状态码的渠道 = %v, 期望 fused", instant.desired.health)
	}
	if !strings.Contains(instant.desired.reason, "402") {
		t.Fatalf("熔断原因应说明状态码，实际: %s", instant.desired.reason)
	}
}

// TestInstantFuseIgnoresUnlistedCodes 验证不在列表里的状态码不会被即时熔断。
func TestInstantFuseIgnoresUnlistedCodes(t *testing.T) {
	p := policy.Default()
	p.Breaker.InstantStatusCodes = []int{402}
	policy.Normalize(&p)

	// 一次 429，不在列表里，且次数不足常规阈值。
	ch := makeChannel(1, p, domain.EventPerfect)
	ch.samples = append([]domain.Sample{statusSample(1, 429)}, ch.samples...)
	ch.score = scoring.Compute(ch.samples, p)

	spare := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, ch, spare)
	decideOffline(r)

	if ch.desired.health == domain.HealthFused {
		t.Fatal("429 不在即时熔断列表里，单次不该触发熔断")
	}
}

// TestInstantFuseRespectsMinPool 验证即时熔断同样受保底约束。
func TestInstantFuseRespectsMinPool(t *testing.T) {
	p := policy.Default()
	p.Breaker.InstantStatusCodes = []int{402}
	policy.Normalize(&p)

	// 组内唯一渠道命中即时熔断码：必须保底强留。
	only := makeChannel(1, p, domain.EventPerfect)
	only.samples = append([]domain.Sample{statusSample(1, 402)}, only.samples...)
	only.score = scoring.Compute(only.samples, p)

	r := buildTestRound(t, p, only)
	decideOffline(r)

	if only.desired.health != domain.HealthSurvivor {
		t.Fatalf("唯一渠道状态 = %v, 期望 survivor（即时熔断也不能打空分组）",
			only.desired.health)
	}
}

// TestInstantFuseDefaultEmpty 验证默认不配置，行为不变。
func TestInstantFuseDefaultEmpty(t *testing.T) {
	if got := policy.Default().Breaker.InstantStatusCodes; len(got) != 0 {
		t.Fatalf("即时熔断状态码默认应为空，实际 %v", got)
	}
}

func TestMaxSwitchPerRound(t *testing.T) {
	p := policy.Default()
	p.Breaker.MinPoolSize = 0
	p.Breaker.MaxSwitchPerRound = 1
	p.Breaker.HTTPDegradeOnly = false // 本用例验证每轮切换上限，需要真的熔断

	failing := []*channel{
		makeChannel(1, p, repeatEvent(domain.EventGatewayError, 5)...),
		makeChannel(2, p, repeatEvent(domain.EventGatewayError, 5)...),
		makeChannel(3, p, repeatEvent(domain.EventGatewayError, 5)...),
	}
	r := buildTestRound(t, p, failing...)
	decideOffline(r)

	fused := 0
	for _, ch := range failing {
		if ch.desired.health == domain.HealthFused {
			fused++
		}
	}
	if fused != 1 {
		t.Fatalf("本轮熔断数 = %d, 期望 1（受每轮切换上限约束）", fused)
	}
}

func TestPriceStrategyPrefersCheapChannel(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategyPrice
	// 价格优先只看 Guardian 内部倍率：越低越优先。
	p.AccountMultipliers = map[string]float64{"1": 1, "2": 4}

	cheap := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	expensive := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, cheap, expensive)
	resolveMultipliers(r)
	decideOffline(r)

	if cheap.desired.weight <= expensive.desired.weight {
		t.Fatalf("便宜渠道权重 %.1f 应高于贵渠道 %.1f",
			cheap.desired.weight, expensive.desired.weight)
	}
	if cheap.desired.priority >= expensive.desired.priority {
		t.Fatalf("便宜渠道优先级 %d 应优于贵渠道 %d（数值更小）",
			cheap.desired.priority, expensive.desired.priority)
	}
}

func TestSpeedStrategyPrefersFastChannel(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategySpeed
	np := normalized(p)

	fast := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	for i := range fast.samples {
		fast.samples[i].TTFBMs = 300
	}
	fast.score = scoring.Compute(fast.samples, np)

	slow := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)
	for i := range slow.samples {
		slow.samples[i].TTFBMs = 4000
	}
	slow.score = scoring.Compute(slow.samples, np)

	// 快渠道倍率更贵，速度优先时不该因此吃亏。
	p.AccountMultipliers = map[string]float64{"1": 5, "2": 1}

	r := buildTestRound(t, p, fast, slow)
	resolveMultipliers(r)
	decideOffline(r)

	if fast.desired.weight <= slow.desired.weight {
		t.Fatalf("快渠道权重 %.1f 应高于慢渠道 %.1f",
			fast.desired.weight, slow.desired.weight)
	}
}

// TestDefaultMultiplierByType 验证账号类型渠道天然优先于 API Key。
func TestDefaultMultiplierByType(t *testing.T) {
	cases := map[string]float64{
		"oauth":       policy.DefaultOAuthMultiplier,
		"setup_token": policy.DefaultOAuthMultiplier,
		"OAuth":       policy.DefaultOAuthMultiplier,
		"apikey":      policy.DefaultAPIKeyMultiplier,
		"api_key":     policy.DefaultAPIKeyMultiplier,
		"APIKEY":      policy.DefaultAPIKeyMultiplier,
		"future_plan": policy.DefaultOAuthMultiplier, // 未知类型按订阅账号处理
	}
	for accountType, want := range cases {
		if got := policy.DefaultMultiplierFor(accountType); got != want {
			t.Fatalf("DefaultMultiplierFor(%q) = %v, 期望 %v", accountType, got, want)
		}
	}
}

// TestOAuthPreferredOverAPIKey 验证同等健康度下，账号类型渠道优先拿到流量。
func TestOAuthPreferredOverAPIKey(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategyPrice

	oauth := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	oauth.account.Type = "oauth"
	apikey := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)
	apikey.account.Type = "apikey"

	r := buildTestRound(t, p, oauth, apikey)
	resolveMultipliers(r)
	decideOffline(r)

	if oauth.state.Multiplier != policy.DefaultOAuthMultiplier {
		t.Fatalf("OAuth 渠道倍率 = %v, 期望 %v", oauth.state.Multiplier, policy.DefaultOAuthMultiplier)
	}
	if apikey.state.Multiplier != policy.DefaultAPIKeyMultiplier {
		t.Fatalf("APIKey 渠道倍率 = %v, 期望 %v", apikey.state.Multiplier, policy.DefaultAPIKeyMultiplier)
	}
	if oauth.desired.weight <= apikey.desired.weight {
		t.Fatalf("账号类型渠道权重 %.1f 应高于 APIKey %.1f",
			oauth.desired.weight, apikey.desired.weight)
	}
	if oauth.desired.priority >= apikey.desired.priority {
		t.Fatalf("账号类型渠道优先级 %d 应优于 APIKey %d（数值更小）",
			oauth.desired.priority, apikey.desired.priority)
	}
}

// TestManualMultiplierOverridesDefault 验证人工倍率优先于类型默认值。
func TestManualMultiplierOverridesDefault(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategyPrice
	// 手动把一个 APIKey 渠道设成比 OAuth 更便宜。
	p.AccountMultipliers = map[string]float64{"2": 0.001}

	oauth := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	oauth.account.Type = "oauth"
	cheapKey := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)
	cheapKey.account.Type = "apikey"

	r := buildTestRound(t, p, oauth, cheapKey)
	resolveMultipliers(r)
	decideOffline(r)

	if !cheapKey.state.MultiplierManual {
		t.Fatal("人工设置过倍率的渠道应标记为 manual")
	}
	if cheapKey.state.Multiplier != 0.001 {
		t.Fatalf("人工倍率 = %v, 期望 0.001", cheapKey.state.Multiplier)
	}
	if cheapKey.desired.weight <= oauth.desired.weight {
		t.Fatalf("人工设为更低倍率的渠道权重 %.1f 应高于 OAuth %.1f",
			cheapKey.desired.weight, oauth.desired.weight)
	}
}

// TestMultiplierNeverLeaksToUpstream 验证倍率不会混进写回 sub2api 的字段里。
func TestMultiplierNeverLeaksToUpstream(t *testing.T) {
	p := policy.Default()
	p.AccountMultipliers = map[string]float64{"1": 0.5}
	policy.Normalize(&p)

	ch := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	ch.account.RateMultiplier = 3 // sub2api 侧的计费倍率，应保持不动

	r := buildTestRound(t, p, ch)
	resolveMultipliers(r)
	decideOffline(r)

	// 调度倍率取人工值，但账号的 rate_multiplier 不受影响。
	if ch.state.Multiplier != 0.5 {
		t.Fatalf("调度倍率 = %v, 期望 0.5", ch.state.Multiplier)
	}
	if ch.account.RateMultiplier != 3 {
		t.Fatalf("sub2api 计费倍率被改动为 %v，调度倍率不应影响它", ch.account.RateMultiplier)
	}
}

func TestLoadFactorAntiFlap(t *testing.T) {
	threshold := normalized(policy.Default()).Weights.ChangeThreshold

	cases := []struct {
		current, target int
		want            bool
	}{
		{50, 52, true},  // 4% 变化：在阈值内，不写回
		{50, 54, true},  // 8% 变化：仍在阈值内
		{50, 56, false}, // 12% 变化：超过阈值，需要写回
		{50, 80, false}, // 60% 变化：需要写回
		{10, 10, true},  // 无变化
		{0, 5, false},   // 当前值缺失时总是写回
	}
	for _, tc := range cases {
		if got := withinThreshold(tc.current, tc.target, threshold); got != tc.want {
			t.Fatalf("withinThreshold(%d, %d) = %v, 期望 %v", tc.current, tc.target, got, tc.want)
		}
	}
}

func TestCooldownBlocksLoadFactorWrite(t *testing.T) {
	p := policy.Default()
	np := normalized(p)

	ch := withLoadFactor(makeChannel(1, np, repeatEvent(domain.EventPerfect, 10)...), 1)
	r := buildTestRound(t, np, ch)
	ch.state.CooldownTill = r.now.Add(time.Minute)

	decideOffline(r)
	if ch.desired.loadFactor != nil && *ch.desired.loadFactor != 1 {
		t.Fatalf("冷却期内不应改动 load_factor，实际目标 = %d", *ch.desired.loadFactor)
	}
}

// TestGroupExclusion 验证整组排除：不受管、且不受「全部分组参与」影响。
func TestGroupExclusion(t *testing.T) {
	p := policy.Default()
	p.ExcludedGroupIDs = []int64{7}
	policy.Normalize(&p)

	if p.GroupEnabled(7, nil) {
		t.Fatal("被排除的分组不应参与守护")
	}
	if !p.GroupEnabled(8, nil) {
		t.Fatal("未排除的分组应正常参与")
	}
	if !p.GroupExcluded(7) {
		t.Fatal("GroupExcluded 应返回 true")
	}

	// 即使显式勾选参与，排除仍然优先。
	p.ManagedGroupMode = "selected"
	p.ManagedGroupIDs = []int64{7, 8}
	if p.GroupEnabled(7, nil) {
		t.Fatal("排除的优先级应高于勾选参与")
	}
	if !p.GroupEnabled(8, nil) {
		t.Fatal("同时勾选的其他分组不该受影响")
	}

	// 分组覆盖里开启也不能翻案。
	enabled := true
	if p.GroupEnabled(7, &policy.GroupOverride{Enabled: &enabled}) {
		t.Fatal("排除的优先级应高于分组覆盖的启用开关")
	}
}

// TestExcludedGroupChannelsNotManaged 验证排除分组后，组内渠道不再进入调度。
func TestExcludedGroupChannelsNotManaged(t *testing.T) {
	global := policy.Default()
	global.ExcludedGroupIDs = []int64{1}
	policy.Normalize(&global)

	group := domain.Group{ID: 1, Name: "被排除的分组"}
	account := domain.Account{
		ID: 1, Name: "渠道", Type: "apikey", GroupIDs: []int64{1}, Schedulable: true,
	}

	r := buildRound(time.Now(), global, map[int64]*policy.GroupOverride{},
		[]domain.Group{group}, []domain.Account{account},
		map[int64]domain.ChannelState{}, map[int64]domain.Baseline{})

	if len(r.channels) != 0 {
		t.Fatalf("被排除分组下的渠道不应进入调度，实际纳入 %d 个", len(r.channels))
	}
}

func TestExcludedChannelIsNotScheduled(t *testing.T) {
	p := policy.Default()
	p.ExcludedAccountIDs = []int64{1}

	ch := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	ch.excluded = true
	r := buildTestRound(t, p, ch)
	decideOffline(r)

	if ch.desired.health != domain.HealthExcluded {
		t.Fatalf("排除渠道状态 = %v, 期望 excluded", ch.desired.health)
	}
	if ch.desired.weight != 0 {
		t.Fatalf("排除渠道权重 = %.1f, 期望 0", ch.desired.weight)
	}
}

// TestGroupProbeOverride 验证分组可以有自己的测活节奏。
func TestGroupProbeOverride(t *testing.T) {
	global := policy.Default()
	policy.Normalize(&global)
	global.Probe.IntervalSeconds = 600

	interval := 60
	disabled := false
	model := "claude-haiku-4-5-20251001"

	t.Run("覆盖间隔", func(t *testing.T) {
		effective := global.ForGroup(&policy.GroupOverride{ProbeIntervalSeconds: &interval})
		if effective.Probe.IntervalSeconds != 60 {
			t.Fatalf("分组探测间隔 = %d, 期望 60", effective.Probe.IntervalSeconds)
		}
		if global.Probe.IntervalSeconds != 600 {
			t.Fatalf("全局间隔被污染为 %d", global.Probe.IntervalSeconds)
		}
	})

	t.Run("关闭该分组探测", func(t *testing.T) {
		effective := global.ForGroup(&policy.GroupOverride{ProbeEnabled: &disabled})
		if effective.Probe.Enabled {
			t.Fatal("分组应能单独关闭定时测试")
		}
	})

	t.Run("覆盖测试模型", func(t *testing.T) {
		effective := global.ForGroup(&policy.GroupOverride{ProbeModel: &model})
		if effective.Probe.Model != model {
			t.Fatalf("分组测试模型 = %q, 期望 %q", effective.Probe.Model, model)
		}
	})

	t.Run("拒绝过小的间隔", func(t *testing.T) {
		tooSmall := 1
		effective := global.ForGroup(&policy.GroupOverride{ProbeIntervalSeconds: &tooSmall})
		if effective.Probe.IntervalSeconds != 600 {
			t.Fatalf("过小的间隔应被忽略，实际 %d", effective.Probe.IntervalSeconds)
		}
	})
}

// TestShouldProbeHonorsGroupInterval 验证探测调度真的按分组间隔走。
func TestShouldProbeHonorsGroupInterval(t *testing.T) {
	p := policy.Default()
	policy.Normalize(&p)
	p.Probe.SkipWhenTrafficFresh = false

	now := time.Now()
	ch := makeChannel(1, p, domain.EventPerfect)
	ch.state.LastSampleAt = now.Add(-2 * time.Minute)
	ch.state.LastProbeAt = now.Add(-2 * time.Minute)

	// 分组间隔 60 秒：距上次探测 2 分钟，应该探测。
	fast := p
	fast.Probe.IntervalSeconds = 60
	ch.pol = fast
	if !shouldProbe(ch, now) {
		t.Fatal("分组间隔 60 秒时距上次 2 分钟应触发探测")
	}

	// 分组间隔 600 秒：距上次才 2 分钟，不该探测。
	slow := p
	slow.Probe.IntervalSeconds = 600
	ch.pol = slow
	if shouldProbe(ch, now) {
		t.Fatal("分组间隔 600 秒时距上次 2 分钟不应探测")
	}

	// 分组关闭探测：无论多久都不探测。
	off := p
	off.Probe.Enabled = false
	ch.pol = off
	ch.state.LastProbeAt = time.Time{}
	if shouldProbe(ch, now) {
		t.Fatal("分组关闭定时测试后不应探测")
	}
}

func TestPausedChannelGetsNoTraffic(t *testing.T) {
	p := policy.Default()
	p.PausedAccountIDs = []int64{1}

	paused := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	paused.paused = true
	active := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, paused, active)
	decideOffline(r)

	if paused.desired.health != domain.HealthPaused {
		t.Fatalf("暂停渠道状态 = %v, 期望 paused", paused.desired.health)
	}
	if paused.desired.schedulable {
		t.Fatal("暂停渠道不应允许调度")
	}
	if paused.desired.weight != 0 {
		t.Fatalf("暂停渠道权重 = %.1f, 期望 0", paused.desired.weight)
	}
	// 满分渠道被暂停后不应被降级或熔断，只是不接流量。
	if active.desired.health != domain.HealthHealthy {
		t.Fatalf("同组健康渠道被误伤为 %v", active.desired.health)
	}
}

// TestPausedChannelNotCountedAlive 保证「组内只剩暂停渠道」不会被当成分组还活着。
func TestPausedChannelNotCountedAlive(t *testing.T) {
	p := policy.Default()
	policy.Normalize(&p)

	paused := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	paused.paused = true
	healthy := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, paused, healthy)
	decideOffline(r)

	if got := aliveCount(r.groupMembers[1], p.Breaker.MinPoolScore, time.Now()); got != 1 {
		t.Fatalf("可用渠道数 = %d, 期望 1（暂停渠道不计入）", got)
	}
}

// TestPausedBeatsAutoRecovery 是暂停与熔断的关键区别：
// 已熔断且健康分已回升的渠道，如果被人工暂停，不能被自动放回可用池。
func TestPausedBeatsAutoRecovery(t *testing.T) {
	p := policy.Default()
	policy.Normalize(&p)

	// 构造一个「原本熔断、当前健康分满分」的渠道——正常会触发自动回池。
	ch := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	ch.state.Health = domain.HealthFused
	ch.paused = true

	r := buildTestRound(t, p, ch)
	// 走完整决策链的前两步，验证 applyRecovery 不会推翻暂停。
	initDesired(ch)
	applyExclusion(ch)
	applyPause(ch)
	e := &Engine{}
	e.applyRecovery(r)

	if ch.desired.health != domain.HealthPaused {
		t.Fatalf("暂停应优先于自动回池，实际状态 = %v", ch.desired.health)
	}
	if ch.desired.schedulable {
		t.Fatal("暂停渠道不应被自动回池放回流量")
	}
}

func TestExcludedBeatsPaused(t *testing.T) {
	p := policy.Default()
	ch := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	ch.excluded = true
	ch.paused = true

	r := buildTestRound(t, p, ch)
	decideOffline(r)

	// 排除语义更强（要还原基线并完全退出），因此优先于暂停。
	if ch.desired.health != domain.HealthExcluded {
		t.Fatalf("同时排除与暂停时状态 = %v, 期望 excluded", ch.desired.health)
	}
}

func TestDegradeLowersPriority(t *testing.T) {
	p := policy.Default()
	// 分数介于降级线和熔断线之间：降级但不熔断。
	degraded := makeChannel(1, p, domain.EventSlowTTFB, domain.EventSlowTTFB,
		domain.EventSlowTTFB, domain.EventPerfect, domain.EventPerfect)
	good := makeChannel(2, p, repeatEvent(domain.EventPerfect, 10)...)

	r := buildTestRound(t, p, degraded, good)
	decideOffline(r)

	if degraded.desired.health != domain.HealthDegraded {
		t.Fatalf("降级渠道状态 = %v (分数 %.1f), 期望 degraded",
			degraded.desired.health, degraded.score.Final)
	}
	if degraded.desired.priority <= good.desired.priority {
		t.Fatalf("降级渠道优先级 %d 应劣于健康渠道 %d（数值更大）",
			degraded.desired.priority, good.desired.priority)
	}
}

func TestScalingRespectsBounds(t *testing.T) {
	p := policy.Default()
	p.Scaling.Enabled = true
	p.Scaling.MinPerAccount = 5
	p.Scaling.MaxPerAccount = 20
	p.Scaling.GlobalMaxConcurrency = 40
	p.Scaling.ScaleUpRatio = 0.1 // 让扩容条件必定成立
	p.Scaling.StepUp = 100       // 故意超过上限，验证 clamp

	ch := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	ch.account.Concurrency = 18
	r := buildTestRound(t, p, ch)
	decideOffline(r)

	if ch.desired.concurrency == nil {
		t.Fatal("应产生并发扩容目标")
	}
	if *ch.desired.concurrency > 20 {
		t.Fatalf("并发目标 = %d, 不应超过单账号上限 20", *ch.desired.concurrency)
	}
}

func TestScalingDisabledByDefault(t *testing.T) {
	p := policy.Default()
	ch := makeChannel(1, p, repeatEvent(domain.EventPerfect, 10)...)
	r := buildTestRound(t, p, ch)
	decideOffline(r)

	if ch.desired.concurrency != nil {
		t.Fatal("默认不开启智能扩容时不应产生并发目标")
	}
}

func normalized(p policy.Policy) policy.Policy {
	policy.Normalize(&p)
	return p
}

func repeatEvent(event domain.EventType, n int) []domain.EventType {
	out := make([]domain.EventType, n)
	for i := range out {
		out[i] = event
	}
	return out
}
