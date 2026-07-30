package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/scoring"
)

// cleanupCandidate 是一个通过全部守卫检查、待处置的渠道。
type cleanupCandidate struct {
	ch      *channel
	groupID int64
	reason  string
}

// applyCleanup 处置反复出现认证失效的渠道。
//
// 这是全流程里唯一可能删数据的环节，因此守卫条件写得很保守：
//  1. 总开关必须显式打开（默认关闭）；
//  2. 渠道必须已经处于熔断态，且熔断满 MinFusedMinutes；
//  3. 最近 Window 条样本里认证失效达到 Occurrences 次；
//  4. 开启 OnlyAuthErrors 时，余额/额度类错误一律不处置；
//  5. KeepLastInGroup 为真时绝不动分组里最后一个渠道；
//  6. 每轮最多处置 MaxPerRound 个。
//
// 处置前会把账号快照写进事件日志。注意：sub2api 对凭据做了脱敏，
// 快照里不含 api_key，删除后 Guardian 无法重建该渠道。
func (e *Engine) applyCleanup(ctx context.Context, r *round) int {
	if !r.global.Cleanup.Enabled {
		return 0
	}

	candidates := e.collectCleanupCandidates(r)
	if len(candidates) == 0 {
		return 0
	}

	done := 0
	for _, candidate := range candidates {
		p := candidate.ch.pol
		if done >= p.Cleanup.MaxPerRound {
			e.store.Log("info", "cleanup_deferred", accountRef(candidate.ch.account.ID),
				accountRef(candidate.groupID),
				fmt.Sprintf("渠道 %s 满足清理条件，但本轮处置额度已用完，下轮继续评估",
					candidate.ch.account.Name), nil)
			continue
		}
		if e.executeCleanup(ctx, r, candidate) {
			done++
			// 记下来，后续候选的「保留最后一个」判定要把它算作已经没了。
			r.markCleanedUp(candidate.ch.account.ID)
		}
	}
	return done
}

// collectCleanupCandidates 挑出满足条件的渠道，逐条记录未通过的原因。
func (e *Engine) collectCleanupCandidates(r *round) []cleanupCandidate {
	var out []cleanupCandidate

	for _, ch := range r.channels {
		p := ch.pol
		if !p.Cleanup.Enabled || ch.excluded {
			continue
		}

		// 限流中的渠道一律不处置，这条不受任何配置影响。
		//
		// 处置动作（暂停 / 停用 / 删除）都会让渠道脱离调度，而 sub2api 自己
		// 已经用 `rate_limit_reset_at` 把限流账号排除在选路之外、到点自动恢复。
		// 在这里插手，等于把「到点自动恢复」换成「需要人工或探测才能恢复」，
		// 高并发时白白损失容量 —— 而那正是最需要容量的时候。
		//
		// 想清理真正废掉的渠道，请用 401/403（凭据失效）作为触发条件。
		if isRateLimited(ch, r.now) {
			continue
		}

		// 从这里往下的每一道「不处置」都要留下可诊断的日志。
		//
		// 守卫共有五道，任何一道都能让渠道免于处置。早期版本一律静默 continue，
		// 用户配好了 401 自动删除却什么都没发生，也无从知道是被哪一条拦下的。
		// 只对「命中了失效样本」的渠道记日志，避免把健康渠道也刷进日志里。
		hits, matchDesc := cleanupHits(ch, p)
		hasHits := hits > 0

		// 判定只看「最近 N 条里命中了几次配置的错误码」，不再要求先熔断。
		//
		// 早期实现要求 desired.health == fused 才处置。这条前提在「网关错误只降级
		// 不熔断」成为默认之后就站不住了：401 渠道往往停在降级、永远进不了熔断，
		// 于是用户配了 401 自动删除却什么都不会发生（实测日志里 188 条都是这个原因）。
		//
		// 安全性不靠「先熔断」保证，而靠下面几道：观察期、保留组内最后一个、
		// 每轮上限，以及删除前先把 schedulable 摘掉（见 cleanupByDelete）。
		if hits < p.Cleanup.Occurrences {
			if hasHits {
				e.store.Log("info", "cleanup_skipped", accountRef(ch.account.ID),
					accountRef(ch.primaryGroup),
					fmt.Sprintf("渠道 %s 最近 %d 条样本里%s %d 次，未达到阈值 %d 次",
						ch.account.Name, p.Cleanup.Window, matchDesc, hits, p.Cleanup.Occurrences), nil)
			}
			continue
		}

		// 保底强留的渠道不处置：它是分组最后的防线，优先级高于自动清理。
		if ch.desired.health == domain.HealthSurvivor {
			e.store.Log("info", "cleanup_skipped", accountRef(ch.account.ID),
				accountRef(ch.primaryGroup),
				fmt.Sprintf("渠道 %s 命中 %d 次失效样本，但它是分组的保底强留渠道，暂不处置",
					ch.account.Name, hits), nil)
			continue
		}

		// 最短观察期：从状态变化时刻起算，给人工介入留出窗口。
		if p.Cleanup.MinFusedMinutes > 0 {
			since := ch.state.HealthSince
			if since.IsZero() || r.now.Sub(since) < time.Duration(p.Cleanup.MinFusedMinutes)*time.Minute {
				waited := time.Duration(0)
				if !since.IsZero() {
					waited = r.now.Sub(since)
				}
				e.store.Log("info", "cleanup_skipped", accountRef(ch.account.ID),
					accountRef(ch.primaryGroup),
					fmt.Sprintf("渠道 %s 已满足处置条件，但当前状态仅持续 %.0f 分钟，需满 %d 分钟观察期",
						ch.account.Name, waited.Minutes(), p.Cleanup.MinFusedMinutes), nil)
				continue
			}
		}

		out = append(out, cleanupCandidate{
			ch:      ch,
			groupID: ch.primaryGroup,
			reason: fmt.Sprintf("最近 %d 次样本中 %d 次%s，且该状态已持续超过 %d 分钟",
				p.Cleanup.Window, hits, matchDesc, p.Cleanup.MinFusedMinutes),
		})
	}
	return out
}

// cleanupHits 统计触发处置的样本数，并给出人类可读的匹配口径。
//
// 三种口径按优先级排列：
//  1. 配置了状态码列表 → 只认这些状态码；
//  2. 开启「仅凭据失效」→ 认证类关键字 + 401/403，排除欠费与限额；
//  3. 都没有 → 任何致命错误都算。
func cleanupHits(ch *channel, p policy.Policy) (int, string) {
	if codes := p.Cleanup.TriggerStatusCodes; len(codes) > 0 {
		return scoring.CountStatusMatches(ch.samples, p.Cleanup.Window, codes),
			fmt.Sprintf("命中状态码 %v", codes)
	}
	if p.Cleanup.OnlyAuthErrors {
		return scoring.CountAuthFailures(ch.samples, p.Cleanup.Window), "认证失效"
	}
	return scoring.CountFatal(ch.samples, p.Cleanup.Window), "致命错误"
}

// executeCleanup 对单个候选执行处置，返回是否真的动了手。
func (e *Engine) executeCleanup(ctx context.Context, r *round, candidate cleanupCandidate) bool {
	ch := candidate.ch
	p := ch.pol
	accountID := ch.account.ID

	// 保底：绝不让一个分组因为自动清理而彻底没有渠道。
	//
	// 判定必须把本轮已经处置掉的渠道排除在外。r.groupMembers 是轮次开始时的
	// 快照，处置过的渠道仍在里面；只看快照的话，删掉 A 之后 B 仍以为「组里还有
	// A 撑着」，于是两个都被删，保护形同虚设。
	if p.Cleanup.KeepLastInGroup {
		for _, groupID := range ch.groupIDs {
			if remaining := survivingMembers(r.groupMembers[groupID], accountID, r.cleanedUp); remaining == 0 {
				e.store.Log("warn", "cleanup_skipped", accountRef(accountID), accountRef(groupID),
					fmt.Sprintf("渠道 %s 满足清理条件，但它是分组内最后一个渠道，已跳过",
						ch.account.Name), nil)
				return false
			}
		}
	}

	snapshot := e.snapshotAccount(ch)

	switch p.Cleanup.Action {
	case policy.FatalActionPause:
		return e.cleanupByPause(accountID, candidate, snapshot)
	case policy.FatalActionDisable:
		return e.cleanupByDisable(ctx, accountID, candidate, snapshot)
	case policy.FatalActionDelete:
		return e.cleanupByDelete(ctx, accountID, candidate, snapshot)
	default:
		return false
	}
}

// cleanupByPause 把渠道转为人工暂停：不再自动回池，等人来处理。
func (e *Engine) cleanupByPause(accountID int64, candidate cleanupCandidate, snapshot string) bool {
	global, err := e.store.Policy()
	if err != nil {
		return false
	}
	if global.AccountPaused(accountID) {
		return false
	}
	global.PausedAccountIDs = append(global.PausedAccountIDs, accountID)
	if _, err := e.store.SavePolicy(global); err != nil {
		e.store.Log("error", "cleanup_failed", accountRef(accountID), accountRef(candidate.groupID),
			err.Error(), nil)
		return false
	}

	e.recordAction(accountID, "cleanup_pause", nil, map[string]any{"paused": true}, nil)
	e.store.AddEvent(domain.Event{
		Level:     "warn",
		Action:    "cleanup_paused",
		AccountID: accountRef(accountID),
		GroupID:   accountRef(candidate.groupID),
		Message: fmt.Sprintf("渠道 %s 已自动转为暂停：%s",
			candidate.ch.account.Name, candidate.reason),
		Detail: snapshot,
	})
	return true
}

// cleanupByDisable 在 sub2api 里把账号置为停用，保留凭据便于人工恢复。
func (e *Engine) cleanupByDisable(ctx context.Context, accountID int64, candidate cleanupCandidate, snapshot string) bool {
	if !candidate.ch.account.IsActive() {
		return false
	}
	if err := e.ensureBaseline(candidate.ch); err != nil {
		e.store.Log("error", "cleanup_failed", accountRef(accountID), accountRef(candidate.groupID),
			fmt.Sprintf("保存停用前基线失败: %s", err), nil)
		return false
	}
	payload := map[string]any{"status": "inactive"}
	if err := e.persistManagedIntent(candidate.ch, payload, nil); err != nil {
		e.store.Log("error", "cleanup_failed", accountRef(accountID), accountRef(candidate.groupID),
			fmt.Sprintf("保存停用写入所有权失败: %s", err), nil)
		return false
	}
	if err := e.client.UpdateAccount(ctx, accountID, payload); err != nil {
		e.recordAction(accountID, "cleanup_disable", nil, payload, err)
		e.store.Log("error", "cleanup_failed", accountRef(accountID), accountRef(candidate.groupID),
			fmt.Sprintf("停用渠道失败: %s", err), nil)
		return false
	}
	e.recordAction(accountID, "cleanup_disable", map[string]any{"status": candidate.ch.account.Status},
		payload, nil)

	e.store.AddEvent(domain.Event{
		Level:     "warn",
		Action:    "cleanup_disabled",
		AccountID: accountRef(accountID),
		GroupID:   accountRef(candidate.groupID),
		Message: fmt.Sprintf("渠道 %s 已自动停用：%s",
			candidate.ch.account.Name, candidate.reason),
		Detail: snapshot,
	})
	return true
}

// cleanupByDelete 从 sub2api 删除账号。不可逆。
func (e *Engine) cleanupByDelete(ctx context.Context, accountID int64, candidate cleanupCandidate, snapshot string) bool {
	// 快照先落库再删除：万一删除后进程挂了，日志里仍有记录可查。
	e.store.AddEvent(domain.Event{
		Level:     "warn",
		Action:    "cleanup_delete_pending",
		AccountID: accountRef(accountID),
		GroupID:   accountRef(candidate.groupID),
		Message: fmt.Sprintf("准备删除渠道 %s：%s",
			candidate.ch.account.Name, candidate.reason),
		Detail: snapshot,
	})

	// 删除前先把流量摘掉。
	//
	// 处置判定不再要求「已经熔断」（否则开着「只降级不熔断」时永远删不掉），
	// 所以这个渠道有可能仍在接流量。先置 schedulable=false 再删，
	// 避免正在飞行的请求打到一个即将消失的账号上。
	//
	// 失败不阻断删除：删除本身就会让它彻底不可用，摘流量只是让过渡更平滑。
	if candidate.ch.account.Schedulable {
		if err := e.client.SetSchedulable(ctx, accountID, false); err != nil {
			e.store.Log("warn", "cleanup_predisable_failed", accountRef(accountID),
				accountRef(candidate.groupID),
				fmt.Sprintf("删除前摘除流量失败（仍将继续删除）: %s", err), nil)
		} else {
			e.recordAction(accountID, "cleanup_predisable",
				map[string]any{"schedulable": true},
				map[string]any{"schedulable": false}, nil)
		}
	}

	if err := e.client.DeleteAccount(ctx, accountID); err != nil {
		e.recordAction(accountID, "cleanup_delete", nil, nil, err)
		e.store.Log("error", "cleanup_failed", accountRef(accountID), accountRef(candidate.groupID),
			fmt.Sprintf("删除渠道失败: %s", err), nil)
		return false
	}
	e.recordAction(accountID, "cleanup_delete", nil, map[string]any{"deleted": true}, nil)

	// 清掉本地痕迹，避免下一轮又把它当成受管渠道。
	_ = e.store.DeleteBaseline(accountID)

	e.store.AddEvent(domain.Event{
		Level:     "error",
		Action:    "cleanup_deleted",
		AccountID: accountRef(accountID),
		GroupID:   accountRef(candidate.groupID),
		Message: fmt.Sprintf("渠道 %s 已被自动删除：%s（凭据在 sub2api 侧已脱敏，Guardian 无法重建，请从原始凭据来源恢复）",
			candidate.ch.account.Name, candidate.reason),
		Detail: snapshot,
	})
	return true
}

// snapshotAccount 生成账号快照，供事后追溯。
//
// 注意：sub2api 的账号接口对 api_key 等敏感字段做了脱敏，
// 快照里不含凭据，只能用于识别「删掉的是哪一个渠道」。
func (e *Engine) snapshotAccount(ch *channel) string {
	groups := make([]map[string]any, 0, len(ch.groupIDs))
	for _, groupID := range ch.groupIDs {
		groups = append(groups, map[string]any{"id": groupID})
	}

	payload := map[string]any{
		"account_id":      ch.account.ID,
		"name":            ch.account.Name,
		"platform":        ch.account.Platform,
		"type":            ch.account.Type,
		"status":          ch.account.Status,
		"priority":        ch.account.Priority,
		"concurrency":     ch.account.Concurrency,
		"rate_multiplier": ch.account.RateMultiplier,
		"groups":          groups,
		"health_score":    ch.score.Final,
		"last_error":      ch.state.LastError,
		"snapshot_note":   "凭据已被 sub2api 脱敏，此快照不含 api_key",
	}
	if ch.account.LoadFactor != nil {
		payload["load_factor"] = *ch.account.LoadFactor
	}
	if ch.baseline != nil {
		payload["baseline"] = map[string]any{
			"priority":        ch.baseline.Priority,
			"concurrency":     ch.baseline.Concurrency,
			"rate_multiplier": ch.baseline.RateMultiplier,
			"schedulable":     ch.baseline.Schedulable,
		}
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

// survivingMembers 统计把 excludeID 排除后，分组内还剩几个可能提供服务的渠道。
//
// 已熔断的渠道仍然算数：它们还有机会自动回池，而被清理掉的没有。
// healthLabel 把健康态转成日志里能读懂的中文。
func healthLabel(health domain.ChannelHealth) string {
	switch health {
	case domain.HealthHealthy:
		return "健康"
	case domain.HealthDegraded:
		return "降级"
	case domain.HealthFused:
		return "已熔断"
	case domain.HealthSurvivor:
		return "保底强留"
	case domain.HealthPaused:
		return "已暂停"
	case domain.HealthExcluded:
		return "已排除"
	default:
		return "待探测"
	}
}

// survivingMembers 统计处置掉 excludeID 之后，分组里还剩几个渠道。
//
// done 是本轮已经处置过的渠道集合，必须一并排除：它们在成员快照里还在，
// 但实际已经不复存在了。
func survivingMembers(members []*channel, excludeID int64, done map[int64]bool) int {
	count := 0
	for _, ch := range members {
		if ch.account.ID == excludeID || ch.excluded || done[ch.account.ID] {
			continue
		}
		count++
	}
	return count
}
