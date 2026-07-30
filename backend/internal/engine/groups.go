package engine

import (
	"fmt"

	"sub2api-guardian/backend/internal/domain"
)

// aggregateGroups 汇总每个分组的健康状况，并在分组失去可用渠道时告警。
func (e *Engine) aggregateGroups(r *round) {
	for _, group := range r.groups {
		state := domain.GroupState{
			GroupID:   group.ID,
			Status:    domain.GroupEmpty,
			UpdatedAt: r.now,
		}

		if !r.groupEnabled(group.ID) {
			state.Status = domain.GroupSkipped
			state.Strategy = string(r.groupPolicy(group.ID).Strategy)
			e.carryAlertHistory(&state)
			_ = e.store.SaveGroupState(state)
			continue
		}

		p := r.groupPolicy(group.ID)
		state.Strategy = string(p.Strategy)

		members := r.groupMembers[group.ID]
		state.TotalAccounts = len(members)

		var scoreSum float64
		var scored int
		for _, ch := range members {
			// 用生效值而非期望值：健康矩阵必须与 sub2api 的实际状况对得上。
			// 熔断写回失败的渠道仍在接流量，报成「已熔断」会误导运维。
			health := ch.effectiveHealth()

			// Guardian 自己判定的状态先归自己的桶。
			//
			// 这几种必须排在上游判定之前：熔断与暂停都是 Guardian 主动把
			// schedulable 写成 false 实现的，若先看上游，它们会被一律算成
			// 「已在 sub2api 关闭调用」，「为什么不接流量」这个信息就丢了 ——
			// 熔断是自动判定，关闭调用是人工决定，两者要能分开看。
			switch health {
			case domain.HealthFused:
				state.FusedAccounts++
				continue
			case domain.HealthPaused:
				state.PausedAccounts++
				continue
			case domain.HealthExcluded:
				state.ExcludedAccounts++
				continue
			case domain.HealthSurvivor:
				state.DegradedAccounts++
				id := ch.account.ID
				state.SurvivorAccountID = &id
				e.accumulate(&state, ch, group.ID, &scoreSum, &scored)
				continue
			}

			// 剩下的是 healthy / unknown / degraded —— Guardian 认为它没问题。
			// 这时候以「网站此刻会不会把请求发给它」为准，判定权在 sub2api。
			//
			// 上游的限流窗口、临时不可调度、过载退避都是它在真实流量里写下的，
			// 而 Guardian 只有探测这一个信息源。两者必然不同步：网站撞到 429 立刻
			// 记下 rate_limit_reset_at 并停止路由，Guardian 却要等下一次探测
			// （默认 300 秒）才可能发现；窗口长达数天时，探测一次成功就把它算回健康，
			// 而网站一整天都不会用它。矩阵数字与网站后台对不上，根子就在这里。
			if kind, _ := ch.account.UpstreamBlock(r.now); kind != domain.BlockNone {
				if kind == domain.BlockRateLimited {
					// 限流按降级计：渠道没被摘出池子，窗口一过就自动回来。
					// 归到「不可用」会让人以为需要动手，而这里恰恰不该动手。
					state.DegradedAccounts++
					state.RateLimitedAccounts++
				} else {
					// 停用 / 关闭调用 / 临时不可调度 / 过载退避：接不到一个请求。
					// 探测得再好也不能算「健康」，否则矩阵上的健康数比实际能服务的多。
					state.ExcludedAccounts++
				}
				e.accumulate(&state, ch, group.ID, &scoreSum, &scored)
				continue
			}

			switch health {
			case domain.HealthHealthy:
				state.HealthyAccounts++
			case domain.HealthUnknown:
				// 待探测：在 sub2api 侧正常服务，只是还没采到样本。
				// 早期版本把它算成降级，导致刚同步完的分组一律显示「部分异常」。
				state.PendingAccounts++
			case domain.HealthDegraded:
				state.DegradedAccounts++
				// 限流单独再记一笔（仍算在降级里）：界面要能区分
				// 「等窗口重置就好」与「真的坏了，需要人看一眼」。
				//
				// 这里是「Guardian 探测到 429、但上游还没记下窗口」的情形，
				// 与上面按 UpstreamBlock 判定的那批互补，不会重复计数。
				if isRateLimited(ch, r.now) {
					state.RateLimitedAccounts++
				}
			}
			e.accumulate(&state, ch, group.ID, &scoreSum, &scored)
		}
		if scored > 0 {
			state.AvgScore = scoreSum / float64(scored)
		}

		state.AvailableAccounts = aliveCount(members, p.Breaker.MinPoolScore, r.now)

		// 断供判定的口径是「有没有渠道在接流量」，渠道为什么不接不重要：
		// 熔断、人工暂停、人工排除、sub2api 侧停用一律计入。
		//
		// 早期实现只数熔断与暂停，于是把一组渠道全部排除后分组仍显示为健康。
		unavailable := state.FusedAccounts + state.PausedAccounts + state.ExcludedAccounts
		switch {
		case state.TotalAccounts == 0:
			state.Status = domain.GroupEmpty
		case unavailable >= state.TotalAccounts:
			state.Status = domain.GroupAllFused
		case state.SurvivorAccountID != nil && state.AvailableAccounts <= p.Breaker.MinPoolSize:
			state.Status = domain.GroupSurvivorOnly
		case unavailable > 0 || state.DegradedAccounts > state.RateLimitedAccounts:
			// 有渠道不接流量，或有「非限流」的降级 —— 这些要人看一眼。
			state.Status = domain.GroupPartial
		case state.RateLimitedAccounts > 0:
			// 只有限流：渠道都还在池子里、会随窗口重置自愈。
			// 单独一个状态，运维一眼就知道不用动手。
			//
			// 这一条排在健康之前：限流的渠道确实不是正常状态，
			// 不该和「全员健康」显示成同一个样子。
			state.Status = domain.GroupRateLimited
		default:
			// 含「全是待探测渠道」的情形：没有任何证据说明它们有问题，
			// 就按健康显示。等首轮探测出结果后自然会转为健康或降级。
			state.Status = domain.GroupHealthy
		}

		previous, err := e.store.GroupState(group.ID)
		hadPrevious := err == nil

		switch {
		case state.Status == domain.GroupAllFused && (!hadPrevious || previous.Status != domain.GroupAllFused):
			state.LastAlertAt = r.now
			state.LastAlertMessage = fmt.Sprintf("分组 %s 已无可调度渠道", group.Name)
			r.alerts = append(r.alerts, domain.Event{
				Level:   "error",
				Action:  "group_all_fused",
				GroupID: accountRef(group.ID),
				Message: state.LastAlertMessage,
				Detail:  encodeJSON(state),
			})
		case state.Status == domain.GroupSurvivorOnly && (!hadPrevious || previous.Status != domain.GroupSurvivorOnly):
			state.LastAlertAt = r.now
			state.LastAlertMessage = fmt.Sprintf("分组 %s 仅剩保底渠道，请尽快补充账号", group.Name)
			r.alerts = append(r.alerts, domain.Event{
				Level:   "warn",
				Action:  "group_survivor_only",
				GroupID: accountRef(group.ID),
				Message: state.LastAlertMessage,
				Detail:  encodeJSON(state),
			})
		case hadPrevious:
			state.LastAlertAt = previous.LastAlertAt
			state.LastAlertMessage = previous.LastAlertMessage
		}

		_ = e.store.SaveGroupState(state)
	}
}

// accumulate 把单个渠道的权重、并发与健康分并入分组聚合。
//
// 提出来是因为归类分支现在有多个出口（熔断、上游拦截、健康态各一条），
// 每条都得记这笔账。留在原地就得在每个 continue 之前复制一遍，
// 漏掉一处的表现是「均分和权重悄悄少算了一部分渠道」——不报错，只是数不对。
func (e *Engine) accumulate(
	state *domain.GroupState,
	ch *channel,
	groupID int64,
	scoreSum *float64,
	scored *int,
) {
	if ch.primaryGroup == groupID {
		state.TotalWeight += ch.desired.weight
		state.TotalConcurrency += ch.account.Concurrency
	}
	if ch.score.SampleCount > 0 {
		*scoreSum += ch.score.Final
		*scored++
		if ch.score.Final > state.BestScore {
			state.BestScore = ch.score.Final
		}
	}
}

func (e *Engine) carryAlertHistory(state *domain.GroupState) {
	previous, err := e.store.GroupState(state.GroupID)
	if err != nil {
		return
	}
	state.LastAlertAt = previous.LastAlertAt
	state.LastAlertMessage = previous.LastAlertMessage
}
