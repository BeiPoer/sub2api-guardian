package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/scoring"
)

// ErrAccountNotManaged 表示账号不在当前受管范围内。
var ErrAccountNotManaged = errors.New("该账号不在受管范围内")

// prepare 加载连接配置并刷新客户端，供手动操作复用。
func (e *Engine) prepare() error {
	conn, err := e.store.Connection()
	if err != nil {
		return err
	}
	e.Reconfigure(conn)
	return e.client.Ready()
}

// ProbeAccount 手动探测单个渠道并立即刷新其健康分。
func (e *Engine) ProbeAccount(ctx context.Context, accountID int64) (domain.ChannelState, error) {
	if err := e.prepare(); err != nil {
		return domain.ChannelState{}, err
	}
	ch, err := e.loadChannel(accountID)
	if err != nil {
		return domain.ChannelState{}, err
	}

	e.probeOne(ctx, ch)

	samples, err := e.store.RecentSamples(accountID, ch.pol.Scoring.LongWindow)
	if err != nil {
		return domain.ChannelState{}, err
	}
	result := scoring.Compute(samples, ch.pol)

	state := ch.state
	state.ShortScore = result.Short
	state.LongScore = result.Long
	state.HealthScore = result.Final
	state.SampleCount = result.SampleCount
	state.ConsecutiveOK = result.ConsecutiveOK
	state.ConsecutiveFail = result.ConsecutiveFail
	state.TTFBP50Ms = result.TTFBP50Ms
	state.TTFBP95Ms = result.TTFBP95Ms
	if len(samples) > 0 {
		state.LastSampleAt = samples[0].OccurredAt
	}
	state.UpdatedAt = time.Now()
	if err := e.store.SaveChannelState(state); err != nil {
		return domain.ChannelState{}, err
	}
	e.fireNotify()
	return state, nil
}

// FuseAccount 手动熔断渠道。
func (e *Engine) FuseAccount(ctx context.Context, accountID int64, reason string) error {
	if err := e.prepare(); err != nil {
		return err
	}
	ch, err := e.loadChannel(accountID)
	if err != nil {
		return err
	}
	if err := e.ensureBaseline(ch); err != nil {
		return err
	}
	managedSchedulable := false
	if err := e.persistManagedIntent(ch, nil, &managedSchedulable); err != nil {
		return err
	}
	if err := e.client.SetSchedulable(ctx, accountID, false); err != nil {
		e.recordAction(accountID, "set_schedulable", nil, map[string]any{"schedulable": false}, err)
		return err
	}
	e.recordAction(accountID, "set_schedulable",
		map[string]any{"schedulable": ch.account.Schedulable},
		map[string]any{"schedulable": false}, nil)

	if strings.TrimSpace(reason) == "" {
		reason = "人工熔断"
	}
	// SetSchedulable 已经成功，期望值即生效值。
	state := ch.state
	state.Health = domain.HealthFused
	state.DesiredHealth = domain.HealthFused
	state.ApplyPending = false
	state.LastApplyError = ""
	state.HealthSince = time.Now()
	state.FusedReason = reason
	state.FusedUntil = time.Now().Add(time.Duration(ch.pol.Breaker.FusedCooldownSecs) * time.Second)
	state.UpdatedAt = time.Now()
	if err := e.store.SaveChannelState(state); err != nil {
		return err
	}

	e.store.Log("warn", "fused", accountRef(accountID), accountRef(ch.primaryGroup),
		fmt.Sprintf("渠道 %s 已人工熔断：%s", ch.account.Name, reason), nil)
	e.refreshAccount(ctx, accountID)
	e.fireNotify()
	return nil
}

// RecoverAccount 手动恢复渠道：还原基线并重新开放调度。
func (e *Engine) RecoverAccount(ctx context.Context, accountID int64) error {
	if err := e.prepare(); err != nil {
		return err
	}
	ch, err := e.loadChannel(accountID)
	if err != nil {
		return err
	}

	if ch.baseline != nil {
		e.restoreBaseline(ctx, ch, "人工恢复")
	}
	if err := e.client.SetSchedulable(ctx, accountID, true); err != nil {
		e.recordAction(accountID, "set_schedulable", nil, map[string]any{"schedulable": true}, err)
		return err
	}
	e.recordAction(accountID, "set_schedulable",
		map[string]any{"schedulable": ch.account.Schedulable},
		map[string]any{"schedulable": true}, nil)
	_ = e.client.ClearError(ctx, accountID)
	_ = e.client.RecoverState(ctx, accountID)

	state := ch.state
	state.Health = domain.HealthHealthy
	state.DesiredHealth = domain.HealthHealthy
	state.ApplyPending = false
	state.LastApplyError = ""
	state.HealthSince = time.Now()
	state.FusedReason = ""
	state.FusedUntil = time.Time{}
	state.CooldownTill = time.Time{}
	state.LastError = ""
	state.UpdatedAt = time.Now()
	if err := e.store.SaveChannelState(state); err != nil {
		return err
	}

	e.store.Log("info", "recovered", accountRef(accountID), accountRef(ch.primaryGroup),
		fmt.Sprintf("渠道 %s 已人工恢复", ch.account.Name), nil)
	e.refreshAccount(ctx, accountID)
	e.fireNotify()
	return nil
}

// SetPaused 人工暂停或恢复渠道调度。
//
// 与熔断的区别：暂停是运维的显式意图，引擎不会因健康分回升把它放回可用池；
// 但探测与计分继续，方便观察什么时候适合恢复。
func (e *Engine) SetPaused(ctx context.Context, accountID int64, paused bool) error {
	if err := e.prepare(); err != nil {
		return err
	}
	ch, err := e.loadChannel(accountID)
	if err != nil {
		return err
	}
	if err := e.ensureBaseline(ch); err != nil {
		return err
	}
	managedSchedulable := !paused
	if err := e.persistManagedIntent(ch, nil, &managedSchedulable); err != nil {
		return err
	}

	if err := e.client.SetSchedulable(ctx, accountID, !paused); err != nil {
		e.recordAction(accountID, "set_schedulable", nil, map[string]any{"schedulable": !paused}, err)
		return err
	}
	e.recordAction(accountID, "set_schedulable",
		map[string]any{"schedulable": ch.account.Schedulable},
		map[string]any{"schedulable": !paused}, nil)

	state := ch.state
	state.UpdatedAt = time.Now()
	state.HealthSince = time.Now()
	state.ApplyPending = false
	state.LastApplyError = ""
	if paused {
		state.Health = domain.HealthPaused
		state.FusedReason = "人工暂停调度"
		state.Weight = 0
	} else {
		// 恢复后交回引擎判定，先落成待评估状态并清掉限流/错误标记。
		state.Health = domain.HealthUnknown
		state.FusedReason = ""
		state.FusedUntil = time.Time{}
		state.CooldownTill = time.Time{}
		_ = e.client.ClearError(ctx, accountID)
		_ = e.client.RecoverState(ctx, accountID)
	}
	state.DesiredHealth = state.Health
	if err := e.store.SaveChannelState(state); err != nil {
		return err
	}

	action, message := "paused", "已暂停调度"
	if !paused {
		action, message = "resumed", "已恢复调度"
	}
	e.store.Log("info", action, accountRef(accountID), accountRef(ch.primaryGroup),
		fmt.Sprintf("渠道 %s %s", ch.account.Name, message), nil)
	e.refreshAccount(ctx, accountID)
	e.fireNotify()
	return nil
}

// UpdateAccountSettings 手动修改渠道的调度字段并同步回 sub2api。
func (e *Engine) UpdateAccountSettings(ctx context.Context, accountID int64, payload map[string]any) error {
	if err := e.prepare(); err != nil {
		return err
	}
	ch, err := e.loadChannel(accountID)
	if err != nil {
		return err
	}
	if err := e.ensureBaseline(ch); err != nil {
		return err
	}

	update := map[string]any{}
	for _, key := range []string{"priority", "load_factor", "concurrency", "rate_multiplier", "status"} {
		if value, ok := payload[key]; ok {
			if key == "status" {
				status, ok := value.(string)
				if !ok || !validAccountStatus(status) {
					return fmt.Errorf("status must be active, inactive, or error")
				}
				value = normalizeAccountStatus(status)
			}
			update[key] = value
		}
	}
	var managedSchedulable *bool
	if value, ok := payload["schedulable"].(bool); ok && value != ch.account.Schedulable {
		managedSchedulable = &value
	}
	if err := e.persistManagedIntent(ch, update, managedSchedulable); err != nil {
		return err
	}
	if len(update) > 0 {
		if err := e.client.UpdateAccount(ctx, accountID, update); err != nil {
			e.recordAction(accountID, "manual_update", nil, update, err)
			return err
		}
		e.recordAction(accountID, "manual_update", nil, update, nil)
	}
	if managedSchedulable != nil {
		value := *managedSchedulable
		if err := e.client.SetSchedulable(ctx, accountID, value); err != nil {
			e.recordAction(accountID, "manual_schedulable", nil, map[string]any{"schedulable": value}, err)
			return err
		}
		e.recordAction(accountID, "manual_schedulable", nil, map[string]any{"schedulable": value}, nil)
	}

	e.store.Log("info", "manual_update", accountRef(accountID), accountRef(ch.primaryGroup),
		fmt.Sprintf("渠道 %s 配置已手动修改", ch.account.Name), payload)

	// 只刷新这一个账号。这里曾经调用全量 Sync，导致改一条渠道要按分组数
	// 发起 N+1 次上游请求，分组多时页面会长时间转圈、看起来像卡死。
	e.refreshAccount(ctx, accountID)
	e.fireNotify()
	return nil
}

// refreshAccount 重新读取单个账号并更新缓存，让前端立刻看到 sub2api 的真实值。
//
// 刷新失败不影响写入结果本身（改动已经提交成功），因此只记录警告。
func (e *Engine) refreshAccount(ctx context.Context, accountID int64) {
	account, err := e.client.Account(ctx, accountID)
	if err != nil {
		e.store.Log("warn", "account_refresh_failed", accountRef(accountID), nil,
			fmt.Sprintf("改动已提交，但回读账号失败：%s", err), nil)
		return
	}
	if account.ID == 0 {
		return
	}

	// 单账号接口不一定回填分组归属，保留缓存里已有的关系，避免渠道从分组里消失。
	if cached, err := e.store.Account(accountID); err == nil {
		if len(account.GroupIDs) == 0 {
			account.GroupIDs = cached.GroupIDs
		}
		if len(account.Groups) == 0 {
			account.Groups = cached.Groups
		}
	}
	if err := e.store.UpsertAccount(account); err != nil {
		e.store.Log("warn", "account_refresh_failed", accountRef(accountID), nil, err.Error(), nil)
	}
}

// AccountModels 拉取渠道可用模型（优先从上游同步，失败时回退到本地列表）。
func (e *Engine) AccountModels(ctx context.Context, accountID int64) ([]string, error) {
	if err := e.prepare(); err != nil {
		return nil, err
	}
	models, err := e.client.SyncUpstreamModels(ctx, accountID)
	if err != nil {
		e.store.Log("warn", "models_sync_failed", accountRef(accountID), nil, err.Error(), nil)
		models, err = e.client.Models(ctx, accountID)
		if err != nil {
			return nil, err
		}
	}

	if state, err := e.store.ChannelState(accountID); err == nil {
		state.Models = models
		state.UpdatedAt = time.Now()
		_ = e.store.SaveChannelState(state)
	}
	return models, nil
}

// loadChannel 组装单个渠道的上下文，用于手动操作。
func (e *Engine) loadChannel(accountID int64) (*channel, error) {
	account, err := e.store.Account(accountID)
	if err != nil {
		return nil, ErrAccountNotManaged
	}
	global, err := e.store.Policy()
	if err != nil {
		return nil, err
	}
	overrides, err := e.store.GroupOverrides()
	if err != nil {
		return nil, err
	}

	primary := int64(0)
	for _, groupID := range account.GroupIDSet() {
		if global.GroupEnabled(groupID, overrides[groupID]) {
			primary = groupID
			break
		}
	}

	state, err := e.store.ChannelState(accountID)
	if err != nil {
		state = newChannelState(account, time.Now())
	}
	if primary > 0 {
		state.GroupID = &primary
	}

	ch := &channel{
		account:      account,
		state:        state,
		pol:          global.ForGroup(overrides[primary]),
		primaryGroup: primary,
		excluded:     global.AccountExcluded(accountID),
	}
	if base, err := e.store.Baseline(accountID); err == nil {
		ch.baseline = &base
	}
	return ch, nil
}
