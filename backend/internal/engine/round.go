// Package engine 是 Guardian 的调度引擎：采样、评分、熔断、降级、调权、扩容、回池。
package engine

import (
	"sort"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/scoring"
)

// channel 是一轮调度中单个渠道的全部上下文。
type channel struct {
	account domain.Account
	state   domain.ChannelState
	samples []domain.Sample
	score   scoring.Result

	// baseline 是 Guardian 接管前的账号原值，恢复时按它还原；未接管过时为 nil。
	baseline *domain.Baseline

	// pol 是该渠道所属主分组的生效策略（全局策略叠加分组覆盖）。
	pol policy.Policy

	// primaryGroup 是权重与优先级的归属分组。
	// 一个账号可能同时属于多个分组，但 load_factor / priority 在 sub2api 里是账号级字段，
	// 因此调权只能有一个归属；保底存活判定仍会在它所属的每个分组里各算一次。
	primaryGroup int64
	groupIDs     []int64

	excluded bool
	paused   bool
	desired  desired

	// apply 记录本轮写回 sub2api 的落地情况，persist 据此决定生效状态。
	apply applyResult
}

// applyResult 描述期望值有没有真正写进 sub2api。
//
// 需要区分「算出了什么」和「改成了什么」：写回失败时渠道在 sub2api 侧的行为
// 毫无变化，状态不能按期望值落库，否则页面会给出虚假的安全感。
type applyResult struct {
	// wrote 表示本轮确实发生了写操作（用于冷却计时）。
	wrote bool

	// schedulableStuck 为真表示「接不接流量」这一维度没能改成期望值。
	// 健康态只在这一维度上做「已生效」判定 —— 熔断与回池都靠它落地。
	schedulableStuck bool

	// err 是最近一次写回失败的原因，供界面如实展示。
	err string
}

// effectiveHealth 返回本轮实际生效的健康态。
//
// 熔断与回池都要靠写回 schedulable 才真正生效。写回失败时保持上一轮的值，
// 而不是推进到期望值 —— 否则界面会显示「已熔断」而 sub2api 仍在给它派流量。
// 期望值另存于 ChannelState.DesiredHealth，下一轮继续重试，不会丢。
func (ch *channel) effectiveHealth() domain.ChannelHealth {
	if !ch.apply.schedulableStuck {
		return ch.desired.health
	}
	if ch.state.Health == "" {
		return domain.HealthUnknown
	}
	return ch.state.Health
}

// desired 是本轮希望写回 sub2api 的目标值。
type desired struct {
	health      domain.ChannelHealth
	schedulable bool
	priority    int
	loadFactor  *int
	concurrency *int
	weight      float64
	reason      string
	fusedUntil  time.Time
}

// round 是一轮调度的可变上下文。
type round struct {
	now                 time.Time
	global              policy.Policy
	overrides           map[int64]*policy.GroupOverride
	groups              []domain.Group
	groupByID           map[int64]domain.Group
	upstreamMultipliers map[int64]domain.UpstreamMultiplierSnapshot

	channels     []*channel
	byAccountID  map[int64]*channel
	groupMembers map[int64][]*channel

	// unmanaged 是本轮未纳入调度的账号（分组被排除、类型/平台不匹配等）。
	//
	// 它们的状态同样要落库：否则会停留在上一次的值 ——
	// 一个渠道曾被排除过，即使后来移出名单，页面也会一直显示「已排除」。
	unmanaged []domain.Account
	baselines map[int64]domain.Baseline

	// 每个分组本轮已执行的软熔断次数，用于限制切换速度。
	softFuses map[int64]int

	// cleanedUp 记录本轮已经被清理处置掉的渠道。
	//
	// groupMembers 是轮次开始时的快照，处置过的渠道仍在里面，
	// 「保留分组内最后一个」的判定必须把它们排除，否则同组渠道会被逐个删光。
	cleanedUp map[int64]bool

	monitoringOK bool
	alerts       []domain.Event
}

// markCleanedUp 记录某渠道已在本轮被处置掉。
//
// 惰性建表：round 有多处构造点（含测试辅助函数），要求每一处都记得初始化
// 这个 map 太脆弱，漏一处就是 nil map 赋值 panic。
func (r *round) markCleanedUp(accountID int64) {
	if r.cleanedUp == nil {
		r.cleanedUp = map[int64]bool{}
	}
	r.cleanedUp[accountID] = true
}

// groupPolicy 返回某分组的生效策略。
func (r *round) groupPolicy(groupID int64) policy.Policy {
	return r.global.ForGroup(r.overrides[groupID])
}

// groupEnabled 报告某分组是否参与守护。
func (r *round) groupEnabled(groupID int64) bool {
	return r.global.GroupEnabled(groupID, r.overrides[groupID])
}

// buildRound 把账号、状态、基线、样本组装成一轮调度上下文。
func buildRound(
	now time.Time,
	global policy.Policy,
	overrides map[int64]*policy.GroupOverride,
	groups []domain.Group,
	accounts []domain.Account,
	states map[int64]domain.ChannelState,
	baselines map[int64]domain.Baseline,
) *round {
	r := &round{
		now:                 now,
		global:              global,
		overrides:           overrides,
		groups:              groups,
		groupByID:           make(map[int64]domain.Group, len(groups)),
		upstreamMultipliers: map[int64]domain.UpstreamMultiplierSnapshot{},
		byAccountID:         map[int64]*channel{},
		groupMembers:        map[int64][]*channel{},
		softFuses:           map[int64]int{},
		cleanedUp:           map[int64]bool{},
		baselines:           baselines,
	}
	for _, group := range groups {
		r.groupByID[group.ID] = group
	}

	for _, account := range accounts {
		managedGroups := make([]int64, 0, 2)
		for _, groupID := range account.GroupIDSet() {
			if _, ok := r.groupByID[groupID]; !ok {
				continue
			}
			if !r.groupEnabled(groupID) {
				continue
			}
			managedGroups = append(managedGroups, groupID)
		}
		if len(managedGroups) == 0 ||
			!global.TypeManaged(account.Type) || !global.PlatformManaged(account.Platform) {
			r.unmanaged = append(r.unmanaged, account)
			continue
		}
		sort.Slice(managedGroups, func(i, j int) bool { return managedGroups[i] < managedGroups[j] })

		state, ok := states[account.ID]
		if !ok {
			state = newChannelState(account, now)
		}
		primary := managedGroups[0]
		ch := &channel{
			account:      account,
			state:        state,
			pol:          r.groupPolicy(primary),
			primaryGroup: primary,
			groupIDs:     managedGroups,
			excluded:     global.AccountExcluded(account.ID),
			paused:       global.AccountPaused(account.ID),
		}
		if base, ok := baselines[account.ID]; ok {
			copied := base
			ch.baseline = &copied
		}
		ch.state.GroupID = &primary

		r.channels = append(r.channels, ch)
		r.byAccountID[account.ID] = ch
		for _, groupID := range managedGroups {
			r.groupMembers[groupID] = append(r.groupMembers[groupID], ch)
		}
	}
	return r
}

func newChannelState(account domain.Account, now time.Time) domain.ChannelState {
	return domain.ChannelState{
		AccountID:       account.ID,
		Health:          domain.HealthUnknown,
		HealthSince:     now,
		DesiredPriority: account.Priority,
		UpdatedAt:       now,
	}
}

// aliveCount 统计分组内计入可用池的渠道数。
//
// 依据 PRD：健康分达到 MinPoolScore 且未熔断/未排除的渠道才算“可用”。
func aliveCount(members []*channel, minScore float64, now time.Time) int {
	return aliveCountExcluding(members, minScore, 0, now)
}

// aliveCountExcluding 统计把 excludeID 拿掉之后，分组内还剩几个可用渠道。
//
// 熔断判定必须用这个而不是 aliveCount()-1：致命错误的渠道分数是 0，
// 本来就不计入可用池，再减 1 会把「还有健康渠道」误判成「即将断供」。
//
// 保底强留的渠道计入在内：它虽然不健康，但仍在接流量，是分组的最后一道防线。
// 不计入的话，一批渠道同时失效时会被逐个留成 survivor，保底数量失控。
func aliveCountExcluding(members []*channel, minScore float64, excludeID int64, now time.Time) int {
	count := 0
	for _, ch := range members {
		if excludeID != 0 && ch.account.ID == excludeID {
			continue
		}
		// 排除与人工暂停的渠道都不接流量，因此都不能算作保底池成员，
		// 否则组内只剩暂停渠道时引擎会误判分组仍然存活。
		if ch.excluded || ch.paused {
			continue
		}
		// sub2api 侧停用的渠道不接流量，与它过去表现多好无关。
		//
		// 这一条要在所有分支之前判断。早期实现只在「尚无样本」的分支里检查，
		// 于是一个刚在 sub2api 后台被停用、但历史健康分还很高的渠道会被算作可用；
		// 保底判定据此以为分组还有活口，放心熔断真正健康的渠道，整组实际断供。
		if !ch.account.IsActive() {
			continue
		}
		// 处在限流 / 临时不可调度 / 过载退避窗口里的渠道同样拿不到流量，
		// 也就不能充当分组的活口 —— 哪怕它历史健康分很高。
		//
		// 这一条修的是与上面同一类的错：把「上游此刻不发流量」的渠道算作可用，
		// 会让保底判定以为分组还有余量，从而放心熔断真正在服务的渠道。
		// 限流面积一大（几百个渠道同时在窗口里）时这个误判最危险。
		//
		// 方向上只会让熔断更保守：可用数变少，MinPoolSize 保护更早介入。
		if kind, _ := ch.account.UpstreamBlock(now); kind != domain.BlockNone && kind != domain.BlockUnschedulable {
			continue
		}
		// 熔断判定阶段 apply 还是零值，effectiveHealth 等同 desired；
		// 写回之后再调用（分组聚合）才会回退到实际生效值 —— 熔断没写成功的
		// 渠道仍在接流量，此时把它算作可用才是与 sub2api 一致的事实。
		switch ch.effectiveHealth() {
		case domain.HealthFused, domain.HealthExcluded, domain.HealthPaused:
			continue
		case domain.HealthSurvivor:
			// 已经保底强留的渠道占住名额，避免同一分组留下多个 survivor。
			//
			// 刻意排在 Schedulable 检查之前：survivor 是 Guardian 正在把
			// schedulable 写回 true 的渠道，本轮读到的 account.Schedulable
			// 还是熔断时的 false。漏算它会让同一分组反复留下新的 survivor。
			count++
			continue
		}
		// 不可调度的渠道拿不到流量，同样不能充当分组的活口。
		if !ch.account.Schedulable {
			continue
		}
		if ch.score.SampleCount == 0 {
			// 尚无样本时以 sub2api 的实际状态为准 —— 上面已经确认它能接流量。
			//
			// 早期版本一律不计入，导致刚同步完（还没探测过）的分组在健康矩阵里
			// 显示「可用 0」，与网站上明明在正常服务的渠道对不上，还会误触发保底告警。
			// 没有证据说明它坏，就不该假定它坏。
			count++
			continue
		}
		if ch.score.Final < minScore {
			continue
		}
		count++
	}
	return count
}

// sortedMembers 返回按健康分从高到低排序的分组成员，分数相同时按账号 ID 稳定排序。
func sortedMembers(members []*channel) []*channel {
	out := append([]*channel(nil), members...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score.Final != out[j].score.Final {
			return out[i].score.Final > out[j].score.Final
		}
		return out[i].account.ID < out[j].account.ID
	})
	return out
}

func accountRef(id int64) *int64 { return &id }
