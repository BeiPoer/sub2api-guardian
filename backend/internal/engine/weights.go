package engine

import (
	"math"
	"sort"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

// applyWeights 按分组策略分配权重，并把权重落成 load_factor 与 priority。
//
// 权重公式（价格优先）：w ∝ healthGate × (1/倍率)^PriceExp
// 权重公式（速度优先）：w ∝ healthGate × (1/首字P95)^SpeedExp
// 均衡策略按 BalancedPriceRatio 混合两者。
//
// 一个账号可能同时属于多个策略不同的分组（价格优先 + 速度优先）。
// sub2api 的 priority / load_factor 都是**账号级**字段，同一个账号在不同分组里
// 无法有不同取值（表里虽有 account_groups.priority，但插入时固定为 50，
// 管理端也没有修改接口），所以只能给出一个折中：
// 在每个所属分组各按该组策略算一份权重，再取平均。
// 这样两个分组的诉求都被计入，而不是让主分组单方面决定。
func applyWeights(r *round) {
	// 第一步：每个分组各自算一份归一化权重。
	perGroup := make(map[int64][]float64, len(r.channels))
	for groupID, members := range r.groupMembers {
		p := r.groupPolicy(groupID)
		for accountID, weight := range groupWeights(members, p) {
			perGroup[accountID] = append(perGroup[accountID], weight)
		}
	}

	// 第二步：把同一个账号在各分组里的权重取平均，作为它的最终权重。
	for _, ch := range r.channels {
		weights := perGroup[ch.account.ID]
		if len(weights) == 0 {
			ch.desired.weight = 0
			continue
		}
		var sum float64
		for _, w := range weights {
			sum += w
		}
		ch.desired.weight = sum / float64(len(weights))
	}

	// 第三步：按主分组落地 priority 与 load_factor。
	//
	// 仍然只由主分组负责写回：这两个字段是账号级的，多个分组同时改会互相打架。
	for groupID, members := range r.groupMembers {
		assignGroupPlacement(r, groupID, members, r.groupPolicy(groupID))
	}
}

// groupWeights 按某个分组的策略，算出组内每个渠道的归一化权重。
//
// 返回的是「账号 ID → 权重点数」，权重总和等于该组的权重预算。
// 注意这里遍历分组的**全部**成员，不再只看主分组 —— 否则非主分组的策略
// 对渠道完全没有影响，用户在那个分组里改策略会看不到任何变化。
func groupWeights(members []*channel, p policy.Policy) map[int64]float64 {
	out := make(map[int64]float64, len(members))
	if len(members) == 0 {
		return out
	}

	raws := make(map[int64]float64, len(members))
	var rawTotal float64
	for _, ch := range members {
		raw := rawWeight(ch, p)
		raws[ch.account.ID] = raw
		rawTotal += raw
	}

	for _, ch := range members {
		switch ch.desired.health {
		case domain.HealthFused, domain.HealthExcluded, domain.HealthPaused:
			out[ch.account.ID] = 0
			continue
		}
		if rawTotal <= 0 {
			// 全组权重都为 0（例如全部低于健康闸门）：平均分配，保证仍有流量。
			out[ch.account.ID] = float64(p.Weights.Budget) / float64(len(members))
			continue
		}
		out[ch.account.ID] = float64(p.Weights.Budget) * raws[ch.account.ID] / rawTotal
	}
	return out
}

// assignGroupPlacement 把权重排名落成 priority 与 load_factor。
//
// 只处理主分组是本组的渠道：这两个字段是账号级的，交给多个分组同时改会互相覆盖。
func assignGroupPlacement(r *round, groupID int64, members []*channel, p policy.Policy) {
	owned := make([]*channel, 0, len(members))
	for _, ch := range members {
		if ch.primaryGroup == groupID {
			owned = append(owned, ch)
		}
	}
	if len(owned) == 0 {
		return
	}

	sortByWeightDesc(owned)

	base := groupPriorityBase(owned)
	for rank, ch := range owned {
		assignPriority(ch, base, rank, p)
		assignLoadFactor(r, ch, p, len(owned))
	}
}

// groupPriorityBase 取组内优先级基准：以原值的中位数为中心、向前退半个组的长度。
//
// 用中位数而不是最小值。最小值看着更直观，但会把整组压到区间下沿：
// 实测某个 179 个渠道的分组原值散布在 51~678，取最小值会把它们全部重排进
// 51~229，于是整组「提前」，插到了原本更靠前的其他分组（8~131、50~69）前面 ——
// 相当于悄悄改变了分组之间的优先关系。
//
// 以中位数为中心时，重排后的区间与原区间大致重合（上例得到 330~508），
// 组内顺序按权重重新排定，组间的相对位置保持不变。
func groupPriorityBase(members []*channel) int {
	if len(members) == 0 {
		return 1
	}
	values := make([]int, 0, len(members))
	for _, ch := range members {
		values = append(values, basePriority(ch))
	}
	sort.Ints(values)

	median := values[len(values)/2]
	base := median - len(values)/2
	if base < 1 {
		base = 1
	}
	return base
}

// rawWeight 计算未归一化的权重。
func rawWeight(ch *channel, p policy.Policy) float64 {
	switch ch.desired.health {
	case domain.HealthFused, domain.HealthExcluded, domain.HealthPaused:
		return 0
	}

	gate := healthGate(ch.score.Final, p.Weights.GateFloor)
	if ch.desired.health == domain.HealthSurvivor {
		// 保底渠道保留极小权重：既不断供，也不承接主要流量。
		gate = math.Max(gate, 0.01)
	}
	if gate <= 0 {
		return 0
	}

	priceTerm := math.Pow(1/multiplierBasis(ch), p.Weights.PriceExp)
	speedTerm := math.Pow(1/speedBasis(ch), p.Weights.SpeedExp)

	var base float64
	switch p.Strategy {
	case policy.StrategyPrice:
		base = priceTerm
	case policy.StrategySpeed:
		base = speedTerm
	default:
		ratio := p.Weights.BalancedPriceRatio
		base = priceTerm*ratio + speedTerm*(1-ratio)
	}

	weight := gate * base
	if weight < 0 {
		return 0
	}
	return weight
}

// healthGate 把健康分映射到 [0,1] 的权重闸门，低于地板值直接归零。
func healthGate(score, floor float64) float64 {
	if score <= floor {
		return 0
	}
	if floor >= 100 {
		return 1
	}
	return (score - floor) / (100 - floor)
}

// multiplierBasis 返回参与权重计算的倍率，越小越优先。
//
// 倍率是 Guardian 内部口径（人工设置 > 按账号类型取默认值），
// 与 sub2api 的计费倍率无关，也不会写回网站。
func multiplierBasis(ch *channel) float64 {
	if ch.state.Multiplier > 0 {
		return ch.state.Multiplier
	}
	return policy.DefaultMultiplierFor(ch.account.Type)
}

// speedBasis 返回参与权重计算的延迟基准（毫秒，归一化到秒量级）。
func speedBasis(ch *channel) float64 {
	ttfb := ch.score.TTFBP95Ms
	if ttfb <= 0 {
		ttfb = ch.score.TTFBP50Ms
	}
	if ttfb <= 0 {
		return 1
	}
	return math.Max(float64(ttfb)/1000, 0.05)
}

// assignPriority 按组内排名生成期望优先级（数值越小越优先）。
//
// 用「分组统一基准 + 排名」而不是「各渠道原值 + 排名」。
//
// 后者是失效的：sub2api 按 priority 硬排序选路，而各渠道的原值本身就相差几十到
// 上百（实测同组内 369~520），排名贡献的 0..N 完全被原值差淹没 —— 算出来的
// 期望值往往恰好等于现值，一次写回都不会发生，用户改了策略看不到任何变化。
func assignPriority(ch *channel, base, rank int, p policy.Policy) {
	priority := base + rank

	switch ch.desired.health {
	case domain.HealthDegraded:
		priority += p.Degrade.PriorityStep * maxInt(1, ch.score.ConsecutiveFail)
	case domain.HealthSurvivor:
		priority += p.Degrade.PriorityStep
	}
	minPriority := maxInt(1, p.Weights.MinPriority)
	if priority < minPriority {
		priority = minPriority
	}
	ch.desired.priority = priority
}

// basePriority 取账号被接管前的优先级，作为计算分组基准的输入。
func basePriority(ch *channel) int {
	if ch.baseline != nil && ch.baseline.Priority > 0 {
		return ch.baseline.Priority
	}
	if ch.account.Priority > 0 {
		return ch.account.Priority
	}
	return 1
}

// assignLoadFactor 把权重换算成 load_factor。
//
// 防抖：与当前值相差不足 ChangeThreshold，或仍在冷却期内，就维持现状。
func assignLoadFactor(r *round, ch *channel, p policy.Policy, members int) {
	if !p.Weights.Enabled {
		return
	}
	switch ch.desired.health {
	case domain.HealthFused, domain.HealthExcluded, domain.HealthPaused:
		return
	}

	current := ch.account.EffectiveLoadFactor()
	target := loadFactorFromWeight(ch.desired.weight, p, members)

	if ch.desired.health == domain.HealthDegraded {
		target = int(math.Round(float64(target) * p.Degrade.LoadFactorRatio))
	}
	if ch.desired.health == domain.HealthSurvivor {
		target = p.Weights.MinLoadFactor
	}
	target = clampInt(target, p.Weights.MinLoadFactor, p.Weights.MaxLoadFactor)

	if !ch.state.CooldownTill.IsZero() && r.now.Before(ch.state.CooldownTill) {
		return
	}
	if withinThreshold(current, target, p.Weights.ChangeThreshold) {
		return
	}
	ch.desired.loadFactor = &target
}

// loadFactorFromWeight 把权重点数换算成 load_factor。
//
// 以「平均分配」为 1 倍基准：拿到平均权重的渠道得到 MinLoadFactor 的中位水平，
// 高于平均的按比例放大，从而在 sub2api 里体现出流量倾斜。
func loadFactorFromWeight(weight float64, p policy.Policy, members int) int {
	if members <= 0 {
		return p.Weights.MinLoadFactor
	}
	average := float64(p.Weights.Budget) / float64(members)
	if average <= 0 {
		return p.Weights.MinLoadFactor
	}
	scale := weight / average
	mid := float64(p.Weights.MinLoadFactor+p.Weights.MaxLoadFactor) / 2
	if mid <= 0 {
		mid = 1
	}
	return int(math.Round(scale * mid))
}

func withinThreshold(current, target int, threshold float64) bool {
	if current == target {
		return true
	}
	if current <= 0 {
		return false
	}
	delta := math.Abs(float64(target-current)) / float64(current)
	return delta < threshold
}

// sortByWeightDesc 按权重从高到低排序，权重相同时按账号 ID 稳定排序，避免每轮抖动。
func sortByWeightDesc(items []*channel) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].desired.weight != items[j].desired.weight {
			return items[i].desired.weight > items[j].desired.weight
		}
		return items[i].account.ID < items[j].account.ID
	})
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
