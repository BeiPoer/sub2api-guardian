package engine

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

const (
	upstreamMultiplierConcurrency = 4
	upstreamMultiplierTimeout     = 25 * time.Second
)

// UpstreamMultiplierSyncResult 是单渠道倍率同步的非敏感结果。
type UpstreamMultiplierSyncResult struct {
	Multiplier         float64   `json:"multiplier"`
	PreviousMultiplier float64   `json:"previous_multiplier"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// SyncUpstreamMultiplier 立即读取单个 API Key 渠道的上游倍率。
// 失败路径绝不写快照，因此调用方可以安全地继续使用原倍率。
func (e *Engine) SyncUpstreamMultiplier(ctx context.Context, accountID int64) (UpstreamMultiplierSyncResult, error) {
	conn, err := e.store.Connection()
	if err != nil {
		return UpstreamMultiplierSyncResult{}, err
	}
	e.Reconfigure(conn)
	result, err := e.fetchAndSaveUpstreamMultiplier(ctx, accountID)
	e.markMultiplierAttempt(accountID)
	if err != nil {
		e.store.Log("warn", "upstream_multiplier_sync_failed", accountRef(accountID), nil,
			fmt.Sprintf("同步上游倍率失败，继续使用原倍率：%s", err), nil)
		return UpstreamMultiplierSyncResult{}, err
	}
	if err := e.refreshGroupStates(); err != nil {
		// 倍率快照已经可靠落库；聚合刷新失败留给下一轮修复，不能把一次
		// 成功同步误报成失败，避免用户重复点击并怀疑原值被覆盖。
		e.store.Log("warn", "upstream_multiplier_refresh_failed", accountRef(accountID), nil,
			fmt.Sprintf("上游倍率已保存，但分组聚合刷新失败：%s", err), nil)
	}
	e.store.Log("info", "upstream_multiplier_synced", accountRef(accountID), nil,
		fmt.Sprintf("上游倍率已更新：%g → %g", result.PreviousMultiplier, result.Multiplier), map[string]any{
			"previous_multiplier": result.PreviousMultiplier,
			"multiplier":          result.Multiplier,
			"updated_at":          result.UpdatedAt,
		})
	e.fireNotify()
	return result, nil
}

func (e *Engine) fetchAndSaveUpstreamMultiplier(ctx context.Context, accountID int64) (UpstreamMultiplierSyncResult, error) {
	account, err := e.store.Account(accountID)
	if err != nil {
		return UpstreamMultiplierSyncResult{}, fmt.Errorf("读取渠道失败: %w", err)
	}
	if !policy.IsAPIKeyType(account.Type) {
		return UpstreamMultiplierSyncResult{}, fmt.Errorf("只有 API Key 类型渠道可以同步上游倍率")
	}

	snapshots, err := e.store.UpstreamMultipliers()
	if err != nil {
		return UpstreamMultiplierSyncResult{}, err
	}
	previous := previousUpstreamMultiplier(account, snapshots)

	requestCtx, cancel := context.WithTimeout(ctx, upstreamMultiplierTimeout)
	defer cancel()
	value, err := e.client.FetchAccountUpstreamMultiplier(requestCtx, accountID, account.Platform)
	if err != nil {
		return UpstreamMultiplierSyncResult{}, err
	}
	return e.saveFetchedUpstreamMultiplier(accountID, previous, value)
}

func (e *Engine) refreshEnabledUpstreamMultipliers(ctx context.Context, accounts []domain.Account) (int, error) {
	global, err := e.store.Policy()
	if err != nil {
		return 0, err
	}
	interval := time.Duration(global.UpstreamMultiplier.IntervalSeconds) * time.Second
	due := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if !global.UpstreamMultiplierEnabled(account.ID, account.Type) ||
			!e.reserveMultiplierAttempt(account.ID, interval) {
			continue
		}
		due = append(due, account)
	}
	if len(due) == 0 {
		return 0, nil
	}

	accountIDs := make([]int64, 0, len(due))
	accountByID := make(map[int64]domain.Account, len(due))
	for _, account := range due {
		accountIDs = append(accountIDs, account.ID)
		accountByID[account.ID] = account
	}
	batch, available, batchErr := e.client.FetchAccountUpstreamMultiplierBatch(ctx, accountIDs)
	if available {
		if batchErr != nil {
			for _, account := range due {
				e.logAutomaticMultiplierFailure(account.ID, batchErr)
			}
			return 0, nil
		}
		snapshots, err := e.store.UpstreamMultipliers()
		if err != nil {
			return 0, err
		}
		succeeded := 0
		for _, accountID := range accountIDs {
			result := batch[accountID]
			if result.Err != nil {
				e.logAutomaticMultiplierFailure(accountID, result.Err)
				continue
			}
			account := accountByID[accountID]
			previous := previousUpstreamMultiplier(account, snapshots)
			if _, err := e.saveFetchedUpstreamMultiplier(accountID, previous, result.Multiplier); err != nil {
				e.logAutomaticMultiplierFailure(accountID, err)
				continue
			}
			succeeded++
		}
		return succeeded, nil
	}

	// 旧版 Sub2API 没有批量原生接口时，保持原有的有限并发兼容路径。
	sem := make(chan struct{}, upstreamMultiplierConcurrency)
	succeeded := make(chan struct{}, len(due))
	var wait sync.WaitGroup
	for _, account := range due {
		accountID := account.ID
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			if _, err := e.fetchAndSaveUpstreamMultiplier(ctx, accountID); err != nil {
				e.logAutomaticMultiplierFailure(accountID, err)
				return
			}
			succeeded <- struct{}{}
		}()
	}
	wait.Wait()
	return len(succeeded), nil
}

func (e *Engine) saveFetchedUpstreamMultiplier(
	accountID int64,
	previous float64,
	value float64,
) (UpstreamMultiplierSyncResult, error) {
	if !validUpstreamMultiplier(value) {
		return UpstreamMultiplierSyncResult{}, fmt.Errorf("上游返回非法倍率，继续使用原倍率")
	}
	updatedAt := time.Now()
	if err := e.store.SaveUpstreamMultiplier(accountID, value, updatedAt); err != nil {
		return UpstreamMultiplierSyncResult{}, err
	}
	return UpstreamMultiplierSyncResult{
		Multiplier: value, PreviousMultiplier: previous, UpdatedAt: updatedAt,
	}, nil
}

func (e *Engine) logAutomaticMultiplierFailure(accountID int64, err error) {
	e.store.Log("warn", "upstream_multiplier_auto_sync_failed", accountRef(accountID), nil,
		fmt.Sprintf("自动同步上游倍率失败，继续使用原倍率：%s", err), nil)
}

func previousUpstreamMultiplier(
	account domain.Account,
	snapshots map[int64]domain.UpstreamMultiplierSnapshot,
) float64 {
	if snapshot, ok := snapshots[account.ID]; ok && validUpstreamMultiplier(snapshot.Value) {
		return snapshot.Value
	}
	return validOrDefaultAccountMultiplier(account)
}

// refreshCachedUpstreamMultipliers 让自动守护关闭时，倍率任务仍按自己的周期运行。
// 它只读取倍率并刷新页面快照，不探测渠道，也不执行任何调度写回。
func (e *Engine) refreshCachedUpstreamMultipliers(conn domain.Connection) error {
	accounts, err := e.store.Accounts()
	if err != nil {
		return err
	}

	e.Reconfigure(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, remoteErr := e.SyncConfiguredMultiplierSource(ctx, false); remoteErr != nil {
		e.store.Log("warn", "remote_multiplier_source_sync_failed", nil, nil,
			fmt.Sprintf("自动守护已关闭，远程倍率源同步失败: %s", remoteErr), nil)
	}
	if len(accounts) == 0 {
		return nil
	}
	updated, err := e.refreshEnabledUpstreamMultipliers(ctx, accounts)
	if err != nil || updated == 0 {
		return err
	}
	if err := e.refreshGroupStates(); err != nil {
		e.store.Log("warn", "upstream_multiplier_refresh_failed", nil, nil,
			fmt.Sprintf("自动倍率已保存，但分组聚合刷新失败：%s", err), nil)
	}
	e.fireNotify()
	return nil
}

func (e *Engine) reserveMultiplierAttempt(accountID int64, interval time.Duration) bool {
	e.multiplierMu.Lock()
	defer e.multiplierMu.Unlock()
	if last := e.multiplierAttempts[accountID]; !last.IsZero() && time.Since(last) < interval {
		return false
	}
	e.multiplierAttempts[accountID] = time.Now()
	return true
}

func (e *Engine) markMultiplierAttempt(accountID int64) {
	e.multiplierMu.Lock()
	e.multiplierAttempts[accountID] = time.Now()
	e.multiplierMu.Unlock()
}

func validUpstreamMultiplier(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validOrDefaultAccountMultiplier(account domain.Account) float64 {
	if validUpstreamMultiplier(account.RateMultiplier) {
		return account.RateMultiplier
	}
	return policy.DefaultMultiplierFor(account.Type)
}
