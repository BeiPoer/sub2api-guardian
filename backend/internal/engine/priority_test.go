package engine

import (
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/scoring"
)

// weightChannel 造一个带指定延迟与倍率的渠道。
func weightChannel(id int64, priority int, ttfbMs int64, multiplier float64, groupIDs ...int64) *channel {
	if len(groupIDs) == 0 {
		groupIDs = []int64{1}
	}
	ch := &channel{
		account: domain.Account{
			ID: id, Name: "渠道", Status: "active", Schedulable: true,
			Priority: priority, Concurrency: 5, Type: "apikey",
		},
		score: scoring.Result{
			Final: 90, SampleCount: 10, TTFBP95Ms: ttfbMs,
		},
		state:        domain.ChannelState{AccountID: id, Multiplier: multiplier},
		primaryGroup: groupIDs[0],
		groupIDs:     groupIDs,
	}
	ch.desired.health = domain.HealthHealthy
	return ch
}

// TestSpeedStrategyReordersPriority 是「调速度优先看不到优先级变化」的回归。
//
// 旧算法是 `各渠道原值 + 排名`。同组内原值本身相差几十到上百（实测 369~520），
// 排名只贡献 0..N 的微调，完全被原值差淹没 —— 算出的期望值往往恰好等于现值，
// 一次写回都不会发生。而 sub2api 是按 priority 硬排序选路的，
// 结果就是策略改了但流量分布毫无变化。
func TestSpeedStrategyReordersPriority(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategySpeed
	policy.Normalize(&p)

	// 原值刻意乱序：最快的渠道原值最大，最慢的原值最小。
	fast := weightChannel(1, 520, 800, 1.0)  // 最快
	mid := weightChannel(2, 400, 2500, 1.0)  //
	slow := weightChannel(3, 369, 6000, 1.0) // 最慢，但原值最小
	r := weightRound(p, fast, mid, slow)

	applyWeights(r)

	if !(fast.desired.priority < mid.desired.priority && mid.desired.priority < slow.desired.priority) {
		t.Fatalf("速度优先下应按延迟重排优先级，实际 快=%d 中=%d 慢=%d",
			fast.desired.priority, mid.desired.priority, slow.desired.priority)
	}
	// 基准以组内原值的中位数为中心（原值 369/400/520，中位 400，退 1 个位 → 399）。
	// 用中位数而不是最小值，是为了让重排后的区间与原区间重合，
	// 避免整组被压到下沿、插到其他分组前面去。
	if fast.desired.priority != 399 {
		t.Fatalf("最快渠道的优先级 = %d, 期望等于组内基准 399（中位 400 退 1）",
			fast.desired.priority)
	}
	// 权重也要体现出差距。
	if !(fast.desired.weight > mid.desired.weight && mid.desired.weight > slow.desired.weight) {
		t.Fatalf("速度优先下权重应随延迟递减，实际 %.1f / %.1f / %.1f",
			fast.desired.weight, mid.desired.weight, slow.desired.weight)
	}
}

// TestPriceStrategyReordersPriority 同上，但按倍率排。
func TestPriceStrategyReordersPriority(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategyPrice
	policy.Normalize(&p)

	// 延迟都一样，只有倍率不同：便宜的应排前面。
	cheap := weightChannel(1, 500, 2000, 0.01)
	normal := weightChannel(2, 450, 2000, 1.0)
	pricey := weightChannel(3, 400, 2000, 5.0)
	r := weightRound(p, cheap, normal, pricey)

	applyWeights(r)

	if !(cheap.desired.priority < normal.desired.priority && normal.desired.priority < pricey.desired.priority) {
		t.Fatalf("价格优先下应按倍率重排，实际 便宜=%d 普通=%d 贵=%d",
			cheap.desired.priority, normal.desired.priority, pricey.desired.priority)
	}
}

func TestPriorityRespectsConfiguredMinimum(t *testing.T) {
	p := policy.Default()
	p.Weights.MinPriority = 5
	policy.Normalize(&p)

	channel := weightChannel(1, 1, 2000, 1.0)
	r := weightRound(p, channel)
	applyWeights(r)

	if channel.desired.priority != 5 {
		t.Fatalf("配置优先级下限为 5 时，期望值 = %d，期望 5", channel.desired.priority)
	}
}

func TestPriceStrategyUsesLatestUpstreamMultiplierSnapshot(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategyPrice
	p.AccountUpstreamMultiplierEnabled["1"] = true
	p.AccountUpstreamMultiplierEnabled["2"] = true
	policy.Normalize(&p)

	a := weightChannel(1, 100, 2000, 1)
	b := weightChannel(2, 100, 2000, 1)
	a.account.RateMultiplier = 1
	b.account.RateMultiplier = 1
	r := weightRound(p, a, b)
	r.upstreamMultipliers = map[int64]domain.UpstreamMultiplierSnapshot{
		1: {Value: 2, UpdatedAt: time.Now()},
		2: {Value: 0.5, UpdatedAt: time.Now()},
	}

	resolveMultipliers(r)
	applyWeights(r)

	if a.state.Multiplier != 2 || b.state.Multiplier != 0.5 {
		t.Fatalf("未使用最新上游倍率快照: A=%g B=%g", a.state.Multiplier, b.state.Multiplier)
	}
	if b.desired.priority >= a.desired.priority {
		t.Fatalf("价格优先未按新倍率调整优先级: 便宜=%d 昂贵=%d", b.desired.priority, a.desired.priority)
	}
}

// TestPriorityStaysWithinOriginalRange 确认重排不会把渠道插到别的分组前面。
//
// 基准取组内原值的最小值，重排后的数值仍落在用户原有区间里。
func TestPriorityStaysWithinOriginalRange(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategySpeed
	policy.Normalize(&p)

	a := weightChannel(1, 300, 900, 1.0)
	b := weightChannel(2, 320, 3000, 1.0)
	c := weightChannel(3, 340, 5000, 1.0)
	r := weightRound(p, a, b, c)

	applyWeights(r)

	for _, ch := range []*channel{a, b, c} {
		if ch.desired.priority < 300 {
			t.Fatalf("渠道 %d 的优先级 %d 低于组内基准 300，会插到其他分组前面",
				ch.account.ID, ch.desired.priority)
		}
		if ch.desired.priority > 340+len(r.channels) {
			t.Fatalf("渠道 %d 的优先级 %d 超出原有区间过多",
				ch.account.ID, ch.desired.priority)
		}
	}
}

// TestReorderKeepsGroupsApart 确认重排不会改变分组之间的优先关系。
//
// 这是「用中位数而不是最小值作基准」的理由。实测你库里某个分组原值散布在
// 51~678，若取最小值作基准，179 个渠道会被全部重排进 51~229，于是整组「提前」，
// 插到原本更靠前的其他分组（8~131、50~69）前面 —— 等于悄悄改了分组间的优先级。
func TestReorderKeepsGroupsApart(t *testing.T) {
	p := policy.Default()
	p.Strategy = policy.StrategySpeed
	policy.Normalize(&p)

	// 靠前的分组：原值 10~30。
	frontA := weightChannel(1, 10, 5000, 1.0, 1)
	frontB := weightChannel(2, 20, 900, 1.0, 1)
	frontC := weightChannel(3, 30, 3000, 1.0, 1)
	// 靠后的分组：原值 500~520。
	backA := weightChannel(4, 500, 5000, 1.0, 2)
	backB := weightChannel(5, 510, 900, 1.0, 2)
	backC := weightChannel(6, 520, 3000, 1.0, 2)

	r := weightRound(p, frontA, frontB, frontC, backA, backB, backC)
	applyWeights(r)

	frontMax := 0
	for _, ch := range []*channel{frontA, frontB, frontC} {
		if ch.desired.priority > frontMax {
			frontMax = ch.desired.priority
		}
	}
	backMin := 1 << 30
	for _, ch := range []*channel{backA, backB, backC} {
		if ch.desired.priority < backMin {
			backMin = ch.desired.priority
		}
	}
	if frontMax >= backMin {
		t.Fatalf("重排后靠前分组的最大优先级 %d 已追上靠后分组的最小值 %d，"+
			"分组间的优先关系被改变了", frontMax, backMin)
	}
}

// TestMultiGroupWeightFusion 是多分组策略冲突的核心用例。
//
// 同一个渠道同时属于「价格优先」和「速度优先」两个分组。sub2api 的 priority 与
// load_factor 都是账号级字段（account_groups.priority 虽存在但插入固定为 50、
// 管理端无接口可改），无法为同一账号在不同分组里取不同值，因此只能折中：
// 在每个分组各按该组策略算一份权重再取平均，让两边的诉求都被计入。
func TestMultiGroupWeightFusion(t *testing.T) {
	p := policy.Default()
	policy.Normalize(&p)

	// 分组 1 走价格优先，分组 2 走速度优先。
	priceOverride := &policy.GroupOverride{Strategy: strategyPtr(policy.StrategyPrice)}
	speedOverride := &policy.GroupOverride{Strategy: strategyPtr(policy.StrategySpeed)}

	// cheapSlow：便宜但慢 —— 价格组给高权重，速度组给低权重。
	cheapSlow := weightChannel(1, 100, 6000, 0.01, 1, 2)
	// pricyFast：贵但快 —— 反之。
	pricyFast := weightChannel(2, 100, 500, 5.0, 1, 2)

	r := weightRound(p, cheapSlow, pricyFast)
	r.overrides[1] = priceOverride
	r.overrides[2] = speedOverride
	r.groupMembers[2] = []*channel{cheapSlow, pricyFast}

	applyWeights(r)

	// 两个渠道各自在一边占优、另一边劣势，融合后差距应明显小于单看一边。
	if cheapSlow.desired.weight <= 0 || pricyFast.desired.weight <= 0 {
		t.Fatalf("两个渠道都该拿到权重，实际 %.2f / %.2f",
			cheapSlow.desired.weight, pricyFast.desired.weight)
	}

	// 关键断言：把速度组的策略也改成价格优先，融合结果必须随之变化。
	// 旧实现只看主分组，改非主分组的策略毫无影响。
	r2 := weightRound(p, cheapSlow, pricyFast)
	r2.overrides[1] = priceOverride
	r2.overrides[2] = priceOverride // 两边都价格优先
	r2.groupMembers[2] = []*channel{cheapSlow, pricyFast}
	applyWeights(r2)
	bothPriceCheap := cheapSlow.desired.weight

	r3 := weightRound(p, cheapSlow, pricyFast)
	r3.overrides[1] = priceOverride
	r3.overrides[2] = speedOverride // 一边价格一边速度
	r3.groupMembers[2] = []*channel{cheapSlow, pricyFast}
	applyWeights(r3)
	mixedCheap := cheapSlow.desired.weight

	if bothPriceCheap == mixedCheap {
		t.Fatal("非主分组的策略变化必须影响最终权重，否则那个分组的策略等于无效设置")
	}
	if bothPriceCheap <= mixedCheap {
		t.Fatalf("便宜渠道在「两边都价格优先」时权重应更高，实际 %.2f vs %.2f",
			bothPriceCheap, mixedCheap)
	}
}

// TestFusedChannelGetsZeroWeight 确认熔断渠道不参与权重分配。
func TestFusedChannelGetsZeroWeight(t *testing.T) {
	p := policy.Default()
	policy.Normalize(&p)

	ok := weightChannel(1, 100, 1000, 1.0)
	fused := weightChannel(2, 100, 1000, 1.0)
	fused.desired.health = domain.HealthFused

	r := weightRound(p, ok, fused)
	applyWeights(r)

	if fused.desired.weight != 0 {
		t.Fatalf("熔断渠道权重 = %.2f, 期望 0", fused.desired.weight)
	}
	if ok.desired.weight <= 0 {
		t.Fatalf("健康渠道应拿到权重，实际 %.2f", ok.desired.weight)
	}
}

func strategyPtr(s policy.Strategy) *policy.Strategy { return &s }

// weightRound 造一个只含权重计算所需字段的轮次。
func weightRound(p policy.Policy, channels ...*channel) *round {
	group1 := domain.Group{ID: 1, Name: "分组1", RateMultiplier: 1}
	group2 := domain.Group{ID: 2, Name: "分组2", RateMultiplier: 1}
	r := &round{
		global:       p,
		overrides:    map[int64]*policy.GroupOverride{},
		groups:       []domain.Group{group1, group2},
		groupByID:    map[int64]domain.Group{1: group1, 2: group2},
		byAccountID:  map[int64]*channel{},
		groupMembers: map[int64][]*channel{},
		softFuses:    map[int64]int{},
		cleanedUp:    map[int64]bool{},
	}
	for _, ch := range channels {
		ch.pol = p
		r.channels = append(r.channels, ch)
		r.byAccountID[ch.account.ID] = ch
		r.groupMembers[ch.primaryGroup] = append(r.groupMembers[ch.primaryGroup], ch)
	}
	return r
}
