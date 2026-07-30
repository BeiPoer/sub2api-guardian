package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/domain"
)

// applyAll 把本轮期望值写回 sub2api，返回实际发生写操作的渠道数。
func (e *Engine) applyAll(ctx context.Context, r *round) int {
	applied := 0
	for _, ch := range r.channels {
		if e.applyChannel(ctx, r, ch) {
			applied++
		}
	}
	return applied
}

// applyChannel 写回单个渠道，并把落地情况记进 ch.apply。
//
// 四条约束：
//  1. 首次改动前先抓取基线，保证任何时候都能还原用户的原始配置；
//  2. 只提交与当前值不同的字段，避免无意义写入；
//  3. 每类字段各受 AutoApply 开关控制，关掉后只记录期望值不落库；
//  4. 写回失败必须如实上报（ch.apply.err / schedulableStuck），
//     persist 才能避免把未生效的状态当成已生效。
func (e *Engine) applyChannel(ctx context.Context, r *round, ch *channel) bool {
	ch.apply = applyResult{}

	// 排除名单：把之前的改动还原回基线，然后不再干预。
	if ch.desired.health == domain.HealthExcluded {
		wrote := e.restoreBaseline(ctx, ch, "渠道已被人工排除，恢复原始配置")
		ch.apply.wrote = wrote
		return wrote
	}

	auto := ch.pol.AutoApply
	payload := map[string]any{}
	before := map[string]any{}

	if auto.Priority && ch.desired.priority > 0 && ch.desired.priority != ch.account.Priority {
		payload["priority"] = ch.desired.priority
		before["priority"] = ch.account.Priority
	}
	if auto.LoadFactor && ch.desired.loadFactor != nil {
		if ch.account.LoadFactor == nil || *ch.account.LoadFactor != *ch.desired.loadFactor {
			payload["load_factor"] = *ch.desired.loadFactor
			before["load_factor"] = ch.account.LoadFactor
		}
	}
	if auto.Concurrency && ch.desired.concurrency != nil && *ch.desired.concurrency != ch.account.Concurrency {
		payload["concurrency"] = *ch.desired.concurrency
		before["concurrency"] = ch.account.Concurrency
	}

	schedulableDiff := ch.desired.schedulable != ch.account.Schedulable
	schedulableChanged := auto.Schedulable && schedulableDiff
	if schedulableDiff && !auto.Schedulable {
		ch.apply.schedulableStuck = true
		ch.apply.err = "预览模式：自动写回 schedulable 已关闭"
	}

	if len(payload) == 0 && !schedulableChanged {
		return false
	}
	if err := e.ensureBaseline(ch); err != nil {
		ch.apply.err = fmt.Sprintf("保存接管基线失败，已取消写回: %s", err)
		if schedulableChanged {
			ch.apply.schedulableStuck = true
		}
		e.store.Log("error", "baseline_capture_failed", accountRef(ch.account.ID), nil, ch.apply.err, nil)
		return false
	}
	var managedSchedulable *bool
	if schedulableChanged {
		value := ch.desired.schedulable
		managedSchedulable = &value
	}
	if err := e.persistManagedIntent(ch, payload, managedSchedulable); err != nil {
		ch.apply.err = fmt.Sprintf("保存 Guardian 写入所有权失败，已取消写回: %s", err)
		if schedulableChanged {
			ch.apply.schedulableStuck = true
		}
		e.store.Log("error", "ownership_capture_failed", accountRef(ch.account.ID), nil, ch.apply.err, nil)
		return false
	}

	changed := false

	if len(payload) > 0 {
		err := e.client.UpdateAccount(ctx, ch.account.ID, payload)
		e.recordAction(ch.account.ID, "update_account", before, payload, err)
		if err != nil {
			ch.apply.err = fmt.Sprintf("写回调度参数失败: %s", err)
			e.store.Log("error", "apply_failed", accountRef(ch.account.ID), accountRef(ch.primaryGroup),
				fmt.Sprintf("写回账号配置失败: %s", err), payload)
		} else {
			changed = true
			e.store.Log("info", "apply", accountRef(ch.account.ID), accountRef(ch.primaryGroup),
				fmt.Sprintf("渠道 %s 调度参数已更新", ch.account.Name), payload)
		}
	}

	if schedulableChanged {
		err := e.client.SetSchedulable(ctx, ch.account.ID, ch.desired.schedulable)
		e.recordAction(ch.account.ID, "set_schedulable",
			map[string]any{"schedulable": ch.account.Schedulable},
			map[string]any{"schedulable": ch.desired.schedulable}, err)
		if err != nil {
			// 这一维度决定渠道到底接不接流量，失败就意味着熔断/恢复没生效。
			ch.apply.schedulableStuck = true
			ch.apply.err = fmt.Sprintf("切换可调度状态失败: %s", err)
			e.store.Log("error", "apply_failed", accountRef(ch.account.ID), accountRef(ch.primaryGroup),
				fmt.Sprintf("切换可调度状态失败: %s", err), nil)
		} else {
			changed = true
			if ch.desired.schedulable {
				// 恢复上线时顺手清掉 sub2api 侧的错误与限流标记。
				_ = e.client.ClearError(ctx, ch.account.ID)
				_ = e.client.RecoverState(ctx, ch.account.ID)
				e.store.Log("info", "recovered", accountRef(ch.account.ID), accountRef(ch.primaryGroup),
					fmt.Sprintf("渠道 %s 已恢复调度：%s", ch.account.Name, ch.desired.reason), nil)
			} else {
				e.store.Log("warn", "fused", accountRef(ch.account.ID), accountRef(ch.primaryGroup),
					fmt.Sprintf("渠道 %s 已停止调度：%s", ch.account.Name, ch.desired.reason), nil)
			}
		}
	}

	ch.apply.wrote = changed
	if changed {
		ch.state.LastApplyAt = r.now
		if ch.pol.Weights.CooldownSeconds > 0 {
			ch.state.CooldownTill = r.now.Add(time.Duration(ch.pol.Weights.CooldownSeconds) * time.Second)
		}
	}
	return changed
}

// ensureBaseline 在首次改动前记录账号原值。
func (e *Engine) ensureBaseline(ch *channel) error {
	if ch.baseline != nil {
		return nil
	}
	base := domain.Baseline{
		AccountID:        ch.account.ID,
		Status:           ch.account.Status,
		Priority:         ch.account.Priority,
		LoadFactor:       ch.account.LoadFactor,
		Concurrency:      ch.account.Concurrency,
		RateMultiplier:   ch.account.RateMultiplier,
		Schedulable:      ch.account.Schedulable,
		CapturedAt:       time.Now(),
		OwnershipVersion: 1,
	}
	if err := e.store.SaveBaseline(base); err != nil {
		return err
	}
	ch.baseline = &base
	return nil
}

// restoreBaseline 把账号还原到基线状态，成功后删除基线记录。
func (e *Engine) restoreBaseline(ctx context.Context, ch *channel, reason string) bool {
	if ch.baseline == nil {
		return false
	}
	base := *ch.baseline

	legacy := base.OwnershipVersion == 0
	payload := map[string]any{}
	conflicts := make([]string, 0, 4)
	if base.Priority != ch.account.Priority &&
		(legacy || intOwnedByGuardian(base.ManagedPriority, ch.account.Priority)) {
		payload["priority"] = base.Priority
	} else if base.Priority != ch.account.Priority && base.ManagedPriority != nil {
		conflicts = append(conflicts, "priority")
	}
	if !sameLoadFactor(base.LoadFactor, ch.account.LoadFactor) &&
		(legacy || sameLoadFactor(base.ManagedLoadFactor, ch.account.LoadFactor)) {
		if base.LoadFactor == nil {
			// sub2api 约定 load_factor <= 0 表示清空。
			payload["load_factor"] = 0
		} else {
			payload["load_factor"] = *base.LoadFactor
		}
	} else if !sameLoadFactor(base.LoadFactor, ch.account.LoadFactor) && base.ManagedLoadFactor != nil {
		conflicts = append(conflicts, "load_factor")
	}
	if base.Concurrency != ch.account.Concurrency &&
		(legacy || intOwnedByGuardian(base.ManagedConcurrency, ch.account.Concurrency)) {
		payload["concurrency"] = base.Concurrency
	} else if base.Concurrency != ch.account.Concurrency && base.ManagedConcurrency != nil {
		conflicts = append(conflicts, "concurrency")
	}
	if base.RateMultiplier != ch.account.RateMultiplier &&
		(legacy || floatOwnedByGuardian(base.ManagedRateMultiplier, ch.account.RateMultiplier)) {
		payload["rate_multiplier"] = base.RateMultiplier
	} else if base.RateMultiplier != ch.account.RateMultiplier && base.ManagedRateMultiplier != nil {
		conflicts = append(conflicts, "rate_multiplier")
	}
	if base.Status != "" && base.Status != ch.account.Status &&
		(legacy || stringOwnedByGuardian(base.ManagedStatus, ch.account.Status)) {
		payload["status"] = base.Status
	} else if base.Status != "" && base.Status != ch.account.Status && base.ManagedStatus != nil {
		conflicts = append(conflicts, "status")
	}

	changed := false
	if len(payload) > 0 {
		err := e.client.UpdateAccount(ctx, ch.account.ID, payload)
		e.recordAction(ch.account.ID, "restore_baseline", nil, payload, err)
		if err != nil {
			e.store.Log("error", "restore_failed", accountRef(ch.account.ID), nil, err.Error(), payload)
			return false
		}
		changed = true
	}
	restoredSchedulable := false
	if base.Schedulable != ch.account.Schedulable &&
		(legacy || boolOwnedByGuardian(base.ManagedSchedulable, ch.account.Schedulable)) {
		err := e.client.SetSchedulable(ctx, ch.account.ID, base.Schedulable)
		e.recordAction(ch.account.ID, "restore_schedulable", nil,
			map[string]any{"schedulable": base.Schedulable}, err)
		if err != nil {
			e.store.Log("error", "restore_failed", accountRef(ch.account.ID), nil, err.Error(), nil)
			return false
		}
		changed = true
		restoredSchedulable = true
	} else if base.Schedulable != ch.account.Schedulable && base.ManagedSchedulable != nil {
		conflicts = append(conflicts, "schedulable")
	}
	if restoredSchedulable && base.Schedulable {
		// SetSchedulable(true) 主要依赖 outbox；这两个恢复端点会同步刷新运行态快照。
		_ = e.client.ClearError(ctx, ch.account.ID)
		_ = e.client.RecoverState(ctx, ch.account.ID)
	}

	if changed {
		e.store.Log("info", "restored", accountRef(ch.account.ID), nil,
			fmt.Sprintf("渠道 %s 已恢复原始配置：%s", ch.account.Name, reason), payload)
	}
	if len(conflicts) > 0 {
		e.store.Log("warn", "restore_conflict", accountRef(ch.account.ID), nil,
			fmt.Sprintf("渠道 %s 的 %v 已被外部修改，Guardian 保留当前值", ch.account.Name, conflicts), nil)
	}
	if err := e.store.DeleteBaseline(ch.account.ID); err == nil {
		ch.baseline = nil
	} else {
		e.store.Log("error", "restore_failed", accountRef(ch.account.ID), nil,
			fmt.Sprintf("原值已恢复但删除基线失败: %s", err), nil)
	}
	return changed
}

// persistManagedIntent 在调用 sub2api 前持久化写入意图。
// 即使 HTTP 请求超时但上游实际已提交，下一轮仍能识别并安全恢复该字段。
func (e *Engine) persistManagedIntent(ch *channel, payload map[string]any, schedulable *bool) error {
	if ch.baseline == nil {
		return fmt.Errorf("baseline missing")
	}
	base := *ch.baseline
	base.OwnershipVersion = 1
	if value, ok := payload["priority"]; ok {
		if parsed, valid := numberAsInt(value); valid {
			base.ManagedPriority = &parsed
		}
	}
	if value, ok := payload["load_factor"]; ok {
		if parsed, valid := numberAsInt(value); valid {
			base.ManagedLoadFactor = &parsed
		}
	}
	if value, ok := payload["concurrency"]; ok {
		if parsed, valid := numberAsInt(value); valid {
			base.ManagedConcurrency = &parsed
		}
	}
	if value, ok := payload["rate_multiplier"]; ok {
		if parsed, valid := numberAsFloat(value); valid {
			base.ManagedRateMultiplier = &parsed
		}
	}
	if value, ok := payload["status"].(string); ok {
		value = normalizeAccountStatus(value)
		base.ManagedStatus = &value
	}
	if schedulable != nil {
		value := *schedulable
		base.ManagedSchedulable = &value
	}
	if err := e.store.SaveBaseline(base); err != nil {
		return err
	}
	ch.baseline = &base
	return nil
}

func intOwnedByGuardian(managed *int, current int) bool {
	return managed != nil && *managed == current
}

func floatOwnedByGuardian(managed *float64, current float64) bool {
	return managed != nil && *managed == current
}

func boolOwnedByGuardian(managed *bool, current bool) bool {
	return managed != nil && *managed == current
}

func stringOwnedByGuardian(managed *string, current string) bool {
	return managed != nil && strings.EqualFold(*managed, current)
}

func numberAsInt(value any) (int, bool) {
	switch item := value.(type) {
	case int:
		return item, true
	case int64:
		return int(item), true
	case float64:
		return int(item), true
	case json.Number:
		parsed, err := item.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func numberAsFloat(value any) (float64, bool) {
	switch item := value.(type) {
	case float64:
		return item, true
	case float32:
		return float64(item), true
	case int:
		return float64(item), true
	case int64:
		return float64(item), true
	case json.Number:
		parsed, err := item.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func normalizeAccountStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "disabled" {
		return "inactive"
	}
	return value
}

func validAccountStatus(value string) bool {
	switch normalizeAccountStatus(value) {
	case "active", "inactive", "error":
		return true
	default:
		return false
	}
}

// RestoreAll 把所有被接管过的渠道还原到基线，用于「一键交还控制权」。
func (e *Engine) RestoreAll(ctx context.Context) (int, error) {
	conn, err := e.store.Connection()
	if err != nil {
		return 0, err
	}
	e.Reconfigure(conn)
	if err := e.client.Ready(); err != nil {
		return 0, err
	}

	baselines, err := e.store.Baselines()
	if err != nil {
		return 0, err
	}
	restored := 0
	for accountID := range baselines {
		account, err := e.store.Account(accountID)
		if err != nil {
			continue
		}
		base := baselines[accountID]
		ch := &channel{account: account, baseline: &base}
		if e.restoreBaseline(ctx, ch, "手动恢复全部渠道") {
			restored++
		}
	}
	e.fireNotify()
	return restored, nil
}

func (e *Engine) recordAction(accountID int64, kind string, before, after map[string]any, err error) {
	action := domain.Action{
		AccountID: accountID,
		Kind:      kind,
		Before:    encodeJSON(before),
		After:     encodeJSON(after),
		OK:        err == nil,
	}
	if err != nil {
		action.Error = err.Error()
	}
	e.store.AddAction(action)
}

// persist 把本轮计算结果落库，并写入告警事件。
//
// 关键区分：Health 是**已生效**状态，DesiredHealth 是**期望**状态。
// 写回 sub2api 失败时只推进后者，前者停在上一轮的值 —— 界面因此不会声称
// 渠道已被摘掉而实际仍在接流量，同时期望值不丢，下一轮继续重试。
func (e *Engine) persist(ctx context.Context, r *round) error {
	for _, ch := range r.channels {
		state := ch.state
		effective := ch.effectiveHealth()

		state.Health = effective
		state.DesiredHealth = ch.desired.health
		state.ApplyPending = effective != ch.desired.health
		state.LastApplyError = ""
		if state.ApplyPending {
			state.LastApplyError = ch.apply.err
		}

		state.Weight = ch.desired.weight
		state.DesiredPriority = ch.desired.priority
		state.DesiredLoadFactor = ch.desired.loadFactor
		state.DesiredConcurrency = ch.desired.concurrency
		state.FusedReason = ""
		state.FusedUntil = ch.desired.fusedUntil
		state.UpdatedAt = r.now

		// 停机原因按期望值写：写回失败时用户仍需要知道引擎为什么想摘掉它。
		switch ch.desired.health {
		case domain.HealthFused, domain.HealthSurvivor, domain.HealthPaused:
			state.FusedReason = ch.desired.reason
		}
		if ch.state.Health != effective {
			state.HealthSince = r.now
		}
		if err := e.store.SaveChannelState(state); err != nil {
			return err
		}
	}
	if err := e.persistUnmanaged(ctx, r); err != nil {
		return err
	}
	for _, alert := range r.alerts {
		e.store.AddEvent(alert)
	}
	return nil
}

// persistUnmanaged 更新本轮未纳入调度的渠道状态。
//
// 不做这一步的话，这些渠道的状态会冻结在上一次的值：
// 一个渠道曾被排除过，即使后来移出名单，页面也会一直显示「已排除」。
func (e *Engine) persistUnmanaged(ctx context.Context, r *round) error {
	if len(r.unmanaged) == 0 {
		return nil
	}
	global := r.global

	for _, account := range r.unmanaged {
		state, err := e.store.ChannelState(account.ID)
		if err != nil {
			state = newChannelState(account, r.now)
		}

		// 退出受管范围等同于交还控制权。先恢复 Guardian 接管前的配置，
		// 成功后才把本地状态改成 unmanaged；失败则保留基线供下一轮重试。
		if base, ok := r.baselines[account.ID]; ok {
			copied := base
			ch := &channel{account: account, state: state, baseline: &copied}
			e.restoreBaseline(ctx, ch, "渠道已退出 Guardian 受管范围")
			if ch.baseline != nil {
				state.DesiredHealth = domain.HealthUnknown
				state.ApplyPending = true
				state.LastApplyError = "退出受管范围时恢复原始配置失败，下一轮将重试"
				state.UpdatedAt = r.now
				if err := e.store.SaveChannelState(state); err != nil {
					return err
				}
				continue
			}
		}

		health := domain.HealthUnknown
		reason := "不在守护范围内"
		if global.AccountExcluded(account.ID) {
			health = domain.HealthExcluded
			reason = "人工排除"
		} else if global.AllGroupsExcluded(account.GroupIDSet()) {
			health = domain.HealthExcluded
			reason = "所属分组已移出调度系统管控"
		}

		// 状态没变就不写，避免每轮都刷一遍 updated_at。
		if state.Health == health && state.FusedReason == reason && !state.ApplyPending {
			continue
		}
		if state.Health != health {
			state.HealthSince = r.now
		}
		state.Health = health
		// 不在守护范围内的渠道没有待生效的写回，期望值即生效值。
		state.DesiredHealth = health
		state.ApplyPending = false
		state.LastApplyError = ""
		state.FusedReason = reason
		state.Weight = 0
		state.UpdatedAt = r.now
		if err := e.store.SaveChannelState(state); err != nil {
			return err
		}
	}
	return nil
}

func sameLoadFactor(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func encodeJSON(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}
