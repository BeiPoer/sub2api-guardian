package engine

import (
	"fmt"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/scoring"
)

// decide 计算本轮每个渠道的期望状态。
//
// 顺序固定：初始化 → 排除名单 → 人工暂停 → 回池 → 倍率阈值熔断 → 硬熔断 → 软熔断（受保底与
// 切换上限约束）→ 健康/降级分级 → 调权 → 扩缩容。
//
// 人工暂停刻意排在回池之前：它是运维的显式意图，不能被健康分回升覆盖。
//
// 分组聚合不在这里做：它描述的是分组**当前实际**的健康状况，必须等写回
// sub2api 之后才知道哪些期望值真的生效了，否则健康矩阵会与网站对不上。
func (e *Engine) decide(r *round) {
	for _, ch := range r.channels {
		initDesired(ch)
	}
	for _, ch := range r.channels {
		applyExclusion(ch)
		applyPause(ch)
	}
	e.applyRecovery(r)
	e.applyUpstreamMultiplierBreaker(r)
	e.applyHardBreaker(r)
	e.applySoftBreaker(r)
	for _, ch := range r.channels {
		gradeHealth(ch)
	}
	applyWeights(r)
	applyScaling(r)
}

// applyUpstreamMultiplierBreaker 对最近一次成功读取的真实上游倍率执行渠道级熔断。
//
// 拉取失败不会覆盖快照，因此价格调权仍使用最近成功值；没有成功快照时不触发。
// 阈值熔断是显式渠道配置，不受普通健康熔断总开关影响，但仍遵守分组保底规则。
func (e *Engine) applyUpstreamMultiplierBreaker(r *round) {
	for _, ch := range r.channels {
		breaker, configured := r.global.UpstreamMultiplierBreakerFor(ch.account.ID, ch.account.Type)
		if !configured || !breaker.Enabled || ch.excluded || ch.paused ||
			ch.desired.health == domain.HealthFused {
			continue
		}
		snapshot, hasSnapshot := r.upstreamMultipliers[ch.account.ID]
		if !hasSnapshot || !validUpstreamMultiplier(snapshot.Value) || snapshot.Value <= breaker.Threshold {
			continue
		}

		reason := fmt.Sprintf("上游倍率 %g 超过配置阈值 %g", snapshot.Value, breaker.Threshold)
		if blockingGroup, blocked := fuseBlockedByAnyGroup(r, ch); blocked {
			keepAsSurvivor(ch, r, fmt.Sprintf("%s（会使分组 %d 低于保底容量）", reason, blockingGroup))
			continue
		}
		fuse(ch, r, reason, false)
	}
}

// initDesired 以「维持现状」为起点初始化期望值。
func initDesired(ch *channel) {
	ch.desired = desired{
		health:      ch.state.Health,
		schedulable: ch.account.Schedulable,
		priority:    ch.account.Priority,
		fusedUntil:  ch.state.FusedUntil,
	}
	if ch.desired.health == "" {
		ch.desired.health = domain.HealthUnknown
	}

	// 排除与暂停完全由当轮的名单决定，不能从上一轮继承。
	//
	// 继承的话会锁死：一个渠道曾被排除过，即使移出名单，
	// gradeHealth 也会因为 desired.health 仍是 excluded 而直接跳过它，
	// 页面上就一直显示「已排除」。
	switch ch.desired.health {
	case domain.HealthExcluded, domain.HealthPaused:
		ch.desired.health = domain.HealthUnknown
	}

	if ch.account.LoadFactor != nil {
		v := *ch.account.LoadFactor
		ch.desired.loadFactor = &v
	}
}

// applyExclusion 处理人工排除的渠道：不参与调度，且把已做过的改动还原。
func applyExclusion(ch *channel) {
	if !ch.excluded {
		return
	}
	ch.desired.health = domain.HealthExcluded
	ch.desired.reason = "人工排除"
	ch.desired.weight = 0
}

// applyPause 处理人工暂停：停止接流量，但保留监控与计分。
//
// 与排除不同，暂停不还原基线——运维随时会恢复它，没必要来回改写 sub2api。
func applyPause(ch *channel) {
	if !ch.paused || ch.excluded {
		return
	}
	ch.desired.health = domain.HealthPaused
	ch.desired.schedulable = false
	ch.desired.reason = "人工暂停调度"
	ch.desired.weight = 0
	ch.desired.fusedUntil = time.Time{}
}

// applyRecovery 让已熔断的渠道在健康分回升后自动回池。
func (e *Engine) applyRecovery(r *round) {
	for _, ch := range r.channels {
		// 人工暂停优先于自动回池：运维的显式意图不能被健康分回升推翻。
		if ch.excluded || ch.paused || ch.state.Health != domain.HealthFused {
			continue
		}
		p := ch.pol
		if !p.Recovery.Enabled {
			ch.desired.schedulable = false
			continue
		}

		// 熔断冷却期内不考虑回池，避免刚熔断就被一次成功探测拉回来。
		if !ch.state.FusedUntil.IsZero() && r.now.Before(ch.state.FusedUntil) {
			ch.desired.schedulable = false
			continue
		}
		// 最近仍有致命错误时不回池。
		if scoring.HasFatal(ch.samples, p.Recovery.SuccessCount) {
			ch.desired.schedulable = false
			continue
		}
		if ch.score.Final < p.Recovery.TargetScore || ch.score.ConsecutiveOK < p.Recovery.SuccessCount {
			ch.desired.schedulable = false
			continue
		}
		// 需要连续健康持续一段时间，防止抖动期间反复回池。
		if p.Recovery.HoldSeconds > 0 && len(ch.samples) > 0 {
			oldest := ch.samples[min(ch.score.ConsecutiveOK, len(ch.samples))-1].OccurredAt
			if r.now.Sub(oldest) < time.Duration(p.Recovery.HoldSeconds)*time.Second {
				ch.desired.schedulable = false
				continue
			}
		}

		ch.desired.health = domain.HealthHealthy
		ch.desired.schedulable = true
		ch.desired.reason = fmt.Sprintf("健康分回升至 %.1f，自动回池", ch.score.Final)
		ch.desired.fusedUntil = time.Time{}
	}
}

// applyHardBreaker 处理致命错误：立即移出可用池。
//
// 保底约束同样适用。早期版本让硬熔断绕过保底，结果是一批渠道同时 401 时
// 整个分组会被打空、彻底断供 —— 「每个分组至少留一个渠道」是系统的第一原则，
// 即使留下的那个大概率也不可用：断供由人来决策，不由自动化代劳。
//
// 与软熔断的区别仍然保留：硬熔断不受每轮切换上限约束，因为致命错误必须尽快止血。
func (e *Engine) applyHardBreaker(r *round) {
	for groupID, members := range r.groupMembers {
		p := r.groupPolicy(groupID)
		if !p.Breaker.Enabled || !p.Breaker.HardFatal {
			continue
		}

		// 分数最低的先处理，保证保底留下的是组内相对最好的那个。
		ordered := sortedMembers(members)
		for i := len(ordered) - 1; i >= 0; i-- {
			ch := ordered[i]
			// 暂停的渠道已经不接流量，没必要再叠加熔断状态。
			if ch.excluded || ch.paused || ch.desired.health == domain.HealthFused {
				continue
			}
			if ch.primaryGroup != groupID {
				continue
			}
			if ch.score.SampleCount == 0 || !ch.score.FatalOverride {
				continue
			}

			// 这里只可能是凭据失效：限流与额度耗尽被归为 quota_exhausted，
			// 不会置 FatalOverride，因此不会走到这条路径上。
			reason := "致命错误：凭据失效"
			if msg := latestMessage(ch); msg != "" {
				reason = "致命错误：" + shorten(msg, 160)
			}

			if blockingGroup, blocked := fuseBlockedByAnyGroup(r, ch); blocked {
				keepAsSurvivor(ch, r, fmt.Sprintf("%s（会使分组 %d 低于保底容量）", reason, blockingGroup))
				continue
			}
			fuse(ch, r, reason, false)
		}
	}
}

// applySoftBreaker 处理错误率与延迟触发的软熔断。
//
// 两项约束务必同时生效：
//   - 保底池：熔断后分组可用渠道数不得低于 MinPoolSize，否则改为「保底强留」；
//   - 切换上限：每个分组每轮最多软熔断 MaxSwitchPerRound 个，防雪崩。
func (e *Engine) applySoftBreaker(r *round) {
	for groupID, members := range r.groupMembers {
		p := r.groupPolicy(groupID)
		if !p.Breaker.Enabled {
			continue
		}

		// 分数最低的先评估，保证被熔断的总是最差的渠道。
		ordered := sortedMembers(members)
		for i := len(ordered) - 1; i >= 0; i-- {
			ch := ordered[i]
			if ch.excluded || ch.paused || ch.desired.health == domain.HealthFused {
				continue
			}
			// 调权与熔断策略归属于主分组；摘流量前仍会检查所有所属分组。
			if ch.primaryGroup != groupID {
				continue
			}
			reason, kind, hit := softBreakerReason(ch, p, r.now)
			if !hit {
				continue
			}

			// 只降级不熔断：渠道仍参与调度，但权重与优先级被压低。
			//
			// 「分组渠道越多越好」—— 5xx、限流、高延迟多半是上游临时抖动，
			// 摘掉渠道等于直接减少可用容量，而降级已经能把流量挪走。
			if degradeOnly(kind, p) {
				markDegraded(ch, reason)
				continue
			}

			if blockingGroup, blocked := softFuseBudgetBlocked(r, ch); blocked {
				ch.desired.reason = fmt.Sprintf("%s（分组 %d 本轮切换额度已用完，下轮继续评估）", reason, blockingGroup)
				continue
			}
			if blockingGroup, blocked := fuseBlockedByAnyGroup(r, ch); blocked {
				keepAsSurvivor(ch, r, fmt.Sprintf("%s（会使分组 %d 低于保底容量）", reason, blockingGroup))
				continue
			}

			fuse(ch, r, reason, true)
			for _, affectedGroupID := range managedGroupIDs(ch) {
				r.softFuses[affectedGroupID]++
			}
		}
	}
}

// fuseBlockedByAnyGroup 保证账号级 schedulable 变更不会打穿任一所属分组。
// sub2api 的 schedulable 是账号字段，一个多分组账号被摘掉后会同时从所有组消失。
func fuseBlockedByAnyGroup(r *round, ch *channel) (int64, bool) {
	for _, groupID := range managedGroupIDs(ch) {
		p := r.groupPolicy(groupID)
		if aliveCountExcluding(r.groupMembers[groupID], p.Breaker.MinPoolScore, ch.account.ID, r.now) < p.Breaker.MinPoolSize {
			return groupID, true
		}
	}
	return 0, false
}

func softFuseBudgetBlocked(r *round, ch *channel) (int64, bool) {
	for _, groupID := range managedGroupIDs(ch) {
		p := r.groupPolicy(groupID)
		if r.softFuses[groupID] >= p.Breaker.MaxSwitchPerRound {
			return groupID, true
		}
	}
	return 0, false
}

func managedGroupIDs(ch *channel) []int64 {
	if len(ch.groupIDs) > 0 {
		return ch.groupIDs
	}
	if ch.primaryGroup > 0 {
		return []int64{ch.primaryGroup}
	}
	return nil
}

// breakerKind 标记软熔断是被哪一类条件触发的。
//
// 需要区分，是因为「只降级不熔断」是按类别配置的：
// 用户可以要求 5xx 与高延迟只降级，同时保留「见到 401 就摘掉」。
type breakerKind int

const (
	breakerInstant breakerKind = iota // 命中「见到即熔断」状态码
	breakerHTTP                       // 错误率超标
	breakerLatency                    // 延迟超标
)

// degradeOnly 报告某类触发条件是否只降级不熔断。
//
// 「见到即熔断」是用户显式列出的状态码，意图明确，不受降级开关影响。
func degradeOnly(kind breakerKind, p policy.Policy) bool {
	switch kind {
	case breakerHTTP:
		return p.Breaker.HTTPDegradeOnly
	case breakerLatency:
		return p.Breaker.LatencyDegradeOnly
	default:
		return false
	}
}

// markDegraded 把渠道标记为降级：仍接流量，但权重与优先级被压低。
func markDegraded(ch *channel, reason string) {
	// 已经是更严重的状态（熔断/保底/排除/暂停）就不降级了。
	switch ch.desired.health {
	case domain.HealthFused, domain.HealthSurvivor, domain.HealthExcluded, domain.HealthPaused:
		return
	}
	ch.desired.health = domain.HealthDegraded
	ch.desired.reason = reason + "（按配置只降级不熔断）"
}

// softBreakerReason 判断软熔断是否触发，并给出原因与触发类别。
func softBreakerReason(ch *channel, p policy.Policy, now time.Time) (string, breakerKind, bool) {
	b := p.Breaker

	// 限流的渠道一律不熔断，这条是硬性约束，不受任何配置影响。
	//
	// 理由不是「保守」，而是**sub2api 自己已经处理了限流**：
	// 上游返回 429 时它会写入 `rate_limit_reset_at`，选路查询里
	// `AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= now)`
	// 直接把该账号排除在候选之外，窗口一过又自动纳入
	// （见 sub2api 的 account_repo.go:1123 与 account.go:156）。
	//
	// Guardian 若再去改 schedulable，等于用一个**需要探测才能恢复**的开关，
	// 覆盖掉一个**到点自动恢复**的机制：限流结束后渠道不会立刻回来，
	// 要等下一次恢复探测（默认 180 秒）跑成功才放回去。高并发时这段空窗
	// 意味着可用容量凭空少一截，而这正是最扛不住的时候。
	//
	// 限流已经通过扣分体现在健康分里 —— 权重降低、优先级后移，流量自然挪走，
	// 但渠道始终留在池子里，限流一结束就能立刻承接流量。
	if isRateLimited(ch, now) {
		return "", breakerHTTP, false
	}

	// 「见到即熔断」的状态码：只看最近一次样本，不累计次数。
	// 适合把 401/403 这类「继续打也没意义」的错误快速摘掉。
	if len(b.InstantStatusCodes) > 0 && len(ch.samples) > 0 {
		if code, hit := scoring.MatchStatus(ch.samples[0], b.InstantStatusCodes); hit {
			return fmt.Sprintf("最近一次请求返回 %d（已配置为立即熔断）", code),
				breakerInstant, true
		}
	}

	failures := scoring.CountGatewayFailures(ch.samples, b.HTTPWindow)
	if failures >= b.HTTPFailures && ch.score.Final < b.HTTPScoreBelow {
		return fmt.Sprintf("最近 %d 次请求中 %d 次网关/限流错误，健康分 %.1f 低于 %.0f",
			b.HTTPWindow, failures, ch.score.Final, b.HTTPScoreBelow), breakerHTTP, true
	}

	slow := scoring.CountSlowResponses(ch.samples, b.LatencyWindow, b.LatencyTTFBMs)
	if slow >= b.LatencyOccurrences {
		return fmt.Sprintf("最近 %d 次请求中 %d 次首字时间超过 %dms",
			b.LatencyWindow, slow, b.LatencyTTFBMs), breakerLatency, true
	}
	return "", breakerHTTP, false
}

// isRateLimited 报告渠道此刻是否处于限流 / 额度耗尽。
//
// 两个信息源，任一成立即为限流：
//
//  1. 上游的 rate_limit_reset_at 窗口还没过。这是权威依据 —— 网站在真实流量里
//     撞到 429 时写下它，并据此把账号排除在选路之外。
//  2. Guardian 最近一次探测结果是限流。补上游还没记下窗口的空档。
//
// 只看最新一条样本：限流是「当下的状态」，不是累积的故障，历史上限流过不代表现在还限。
//
// 加上第 1 条是必要的，不只是为了数字准。这个函数同时守着「限流一律不熔断」那条
// 硬性约束（见 softBreakerReason）：只看样本时，一个被网站限流数天、但恰好探测
// 成功过一次的渠道会重新变成熔断候选，于是 Guardian 去关它的 schedulable ——
// 把一个到点自动恢复的机制，换成一个必须探测才能恢复的开关。
func isRateLimited(ch *channel, now time.Time) bool {
	if kind, _ := ch.account.UpstreamBlock(now); kind == domain.BlockRateLimited {
		return true
	}
	if len(ch.samples) == 0 {
		return false
	}
	return scoring.IsQuotaExhausted(ch.samples[0].EventType)
}

// fuse 把渠道标记为熔断。
func fuse(ch *channel, r *round, reason string, soft bool) {
	// 去重看的是上一轮的**期望**值而不是生效值：写回失败时熔断会逐轮重试，
	// 用生效值判断会让同一次熔断每轮都重复告警。写回失败本身另有 apply_failed
	// 日志与 ApplyPending 标记，不会被这条去重掩盖。
	already := ch.state.DesiredHealth == domain.HealthFused
	ch.desired.health = domain.HealthFused
	ch.desired.schedulable = false
	ch.desired.reason = reason
	ch.desired.weight = 0
	ch.desired.fusedUntil = r.now.Add(time.Duration(ch.pol.Breaker.FusedCooldownSecs) * time.Second)

	if already {
		return
	}
	level := "error"
	action := "breaker_open"
	if soft {
		level = "warn"
		action = "breaker_open_soft"
	}
	r.alerts = append(r.alerts, domain.Event{
		Level:     level,
		Action:    action,
		AccountID: accountRef(ch.account.ID),
		GroupID:   accountRef(ch.primaryGroup),
		Message:   fmt.Sprintf("渠道 %s 已熔断：%s", ch.account.Name, reason),
		Detail:    encodeSummary(ch),
	})
}

// keepAsSurvivor 保底强留：本应熔断，但分组会因此断供，改为压到最低权重并告警。
func keepAsSurvivor(ch *channel, r *round, reason string) {
	ch.desired.health = domain.HealthSurvivor
	ch.desired.schedulable = true
	ch.desired.reason = "保底强留：" + reason

	// 同上，按期望值去重，避免写回反复失败时刷屏。
	if ch.state.DesiredHealth == domain.HealthSurvivor {
		return
	}
	r.alerts = append(r.alerts, domain.Event{
		Level:     "warn",
		Action:    "survivor_kept",
		AccountID: accountRef(ch.account.ID),
		GroupID:   accountRef(ch.primaryGroup),
		Message: fmt.Sprintf("渠道 %s 触发熔断条件，但为保证分组存活被强留（%s）",
			ch.account.Name, reason),
		Detail: encodeSummary(ch),
	})
}

// gradeHealth 给未熔断的渠道做健康/降级分级。
func gradeHealth(ch *channel) {
	switch ch.desired.health {
	case domain.HealthFused, domain.HealthExcluded, domain.HealthSurvivor, domain.HealthPaused:
		return
	}
	if ch.score.SampleCount == 0 {
		ch.desired.health = domain.HealthUnknown
		ch.desired.reason = "尚无样本，等待首次探测"
		return
	}
	if ch.pol.Degrade.Enabled && ch.score.Final < ch.pol.Degrade.ScoreThreshold {
		ch.desired.health = domain.HealthDegraded
		ch.desired.reason = fmt.Sprintf("健康分 %.1f 低于降级线 %.0f",
			ch.score.Final, ch.pol.Degrade.ScoreThreshold)
		return
	}
	ch.desired.health = domain.HealthHealthy
	ch.desired.reason = ""
}

func latestMessage(ch *channel) string {
	if len(ch.samples) == 0 {
		return ""
	}
	return ch.samples[0].Message
}

func encodeSummary(ch *channel) string {
	return fmt.Sprintf(
		`{"account_id":%d,"health_score":%.1f,"short":%.1f,"long":%.1f,"ttfb_p95":%d,"fail_streak":%d}`,
		ch.account.ID, ch.score.Final, ch.score.Short, ch.score.Long,
		ch.score.TTFBP95Ms, ch.score.ConsecutiveFail)
}
