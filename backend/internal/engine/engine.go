package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

// tickInterval 是引擎的固定心跳。
//
// 心跳很短是为了让熔断、回池和调权及时生效；真正的网络调用（探测、流量拉取、
// 用量统计）各自受策略里的间隔约束，不会因为心跳快而放大上游压力。
const tickInterval = 15 * time.Second

// monitoringRecheckInterval 是重新探测 sub2api 运维监控可用性的间隔。
const monitoringRecheckInterval = 5 * time.Minute

// catalogSyncInterval 是自动守护关闭时，仍然刷新分组与账号快照的间隔。
//
// 没有它的话，关掉自动守护后页面会一直显示旧的分组构成，
// 与 sub2api 实际状态对不上。
const catalogSyncInterval = 2 * time.Minute

// Engine 驱动整个守护流程。
type Engine struct {
	store  *store.Store
	client *upstream.Client

	mu       sync.Mutex
	running  bool
	lastRun  time.Time
	lastErr  string
	lastMs   int64
	lastPlan Summary

	// cancelRun 中断正在执行的调度轮次（含所有进行中的探测）。
	// 仅在 running 为真时非 nil。
	cancelRun context.CancelFunc
	// canceled 标记本轮是被人工取消的，用于区分「取消」与「失败」。
	canceled bool
	// runDone 在当前轮次退出时关闭，Stop 用它等待所有数据库写入完成。
	runDone  chan struct{}
	stopping bool

	monitoringOK       bool
	monitoringChecked  time.Time
	lastCatalogSync    time.Time
	multiplierMu       sync.Mutex
	multiplierAttempts map[int64]time.Time

	notifyMu sync.RWMutex
	notify   func()

	stopCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	loopWG    sync.WaitGroup
}

// Summary 是一轮调度的结果概要。
type Summary struct {
	Channels  int `json:"channels"`
	Probed    int `json:"probed"`
	Samples   int `json:"samples"`
	Fused     int `json:"fused"`
	Recovered int `json:"recovered"`
	Applied   int `json:"applied"`
	CleanedUp int `json:"cleaned_up"`
	Alerts    int `json:"alerts"`
}

// Status 是引擎对外暴露的运行状态。
type Status struct {
	Running           bool      `json:"running"`
	AutoEnabled       bool      `json:"auto_enabled"`
	Configured        bool      `json:"configured"`
	MonitoringEnabled bool      `json:"monitoring_enabled"`
	LastRunAt         time.Time `json:"last_run_at"`
	LastRunMs         int64     `json:"last_run_ms"`
	LastRunError      string    `json:"last_run_error"`
	LastSummary       Summary   `json:"last_summary"`
}

// New 创建引擎。
func New(st *store.Store, client *upstream.Client) *Engine {
	return &Engine{
		store: st, client: client, stopCh: make(chan struct{}),
		multiplierAttempts: map[int64]time.Time{},
	}
}

// SetNotifier 注册状态变更回调，用于向前端推送 SSE。
func (e *Engine) SetNotifier(fn func()) {
	e.notifyMu.Lock()
	e.notify = fn
	e.notifyMu.Unlock()
}

func (e *Engine) fireNotify() {
	e.notifyMu.RLock()
	fn := e.notify
	e.notifyMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// Start 启动后台心跳。
func (e *Engine) Start() {
	e.startOnce.Do(func() {
		e.mu.Lock()
		if e.stopping {
			e.mu.Unlock()
			return
		}
		e.loopWG.Add(1)
		e.mu.Unlock()
		go func() {
			defer e.loopWG.Done()
			ticker := time.NewTicker(tickInterval)
			defer ticker.Stop()

			// 启动后稍等一下，让 HTTP 服务先就绪。
			select {
			case <-e.stopCh:
				return
			case <-time.After(3 * time.Second):
			}

			for {
				e.tick()
				select {
				case <-e.stopCh:
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

// Stop 停止后台心跳，取消并等待当前轮次退出。它只影响进程生命周期，
// 不会像用户点击「停止自动调度」那样修改持久化开关。
func (e *Engine) Stop() {
	var done chan struct{}
	var cancel context.CancelFunc
	e.stopOnce.Do(func() {
		e.mu.Lock()
		e.stopping = true
		done = e.runDone
		cancel = e.cancelRun
		e.mu.Unlock()
		close(e.stopCh)
		if cancel != nil {
			cancel()
		}
	})
	e.mu.Lock()
	done = e.runDone
	e.mu.Unlock()
	e.loopWG.Wait()
	if done != nil {
		<-done
	}
}

// tick 是心跳的一次执行。
//
// 即使自动守护被关闭，也要按 catalogSyncInterval 定时同步目录：
// 否则分组与账号一直是旧快照，页面上的分组健康矩阵会和 sub2api 对不上。
func (e *Engine) tick() {
	conn, err := e.store.Connection()
	if err != nil || conn.BaseURL == "" || conn.AdminAPIKey == "" {
		return
	}

	if !conn.Enabled {
		e.syncCatalogIfStale(conn)
		if err := e.refreshCachedUpstreamMultipliers(conn); err != nil {
			e.store.Log("warn", "upstream_multiplier_refresh_failed", nil, nil,
				fmt.Sprintf("自动守护已关闭，定时同步上游倍率失败: %s", err), nil)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := e.RunOnce(ctx); err != nil &&
		!errors.Is(err, ErrAlreadyRunning) && !errors.Is(err, context.Canceled) {
		e.store.Log("error", "run_failed", nil, nil, err.Error(), nil)
	}
}

// syncCatalogIfStale 在自动守护关闭时，仍定期刷新分组与账号快照。
func (e *Engine) syncCatalogIfStale(conn domain.Connection) {
	e.mu.Lock()
	stale := time.Since(e.lastCatalogSync) >= catalogSyncInterval
	if stale {
		e.lastCatalogSync = time.Now()
	}
	e.mu.Unlock()
	if !stale {
		return
	}

	e.Reconfigure(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := e.Sync(ctx); err != nil {
		e.store.Log("warn", "catalog_sync_failed", nil, nil,
			fmt.Sprintf("自动守护已关闭，目录定时同步失败: %s", err), nil)
		return
	}
	// 目录变了就重算一次分组聚合，让健康矩阵与实际渠道对上。
	if err := e.refreshGroupStates(); err != nil {
		e.store.Log("warn", "group_state_refresh_failed", nil, nil, err.Error(), nil)
	}
	e.fireNotify()
}

// refreshGroupStates 只重算分组聚合，不做探测与写回。
//
// 供「目录同步后」和「手动同步」复用：用户改了 sub2api 侧的分组归属后，
// 应该立刻在页面上看到正确的分组构成，而不必等下一轮完整调度。
func (e *Engine) refreshGroupStates() error {
	global, err := e.store.Policy()
	if err != nil {
		return err
	}
	overrides, err := e.store.GroupOverrides()
	if err != nil {
		return err
	}
	groups, err := e.store.Groups()
	if err != nil {
		return err
	}
	accounts, err := e.store.Accounts()
	if err != nil {
		return err
	}
	states, err := e.store.ChannelStateMap()
	if err != nil {
		return err
	}
	baselines, err := e.store.Baselines()
	if err != nil {
		return err
	}

	r := buildRound(time.Now(), global, overrides, groups, accounts, states, baselines)
	r.upstreamMultipliers, err = e.store.UpstreamMultipliers()
	if err != nil {
		return err
	}
	resolveMultipliers(r)
	e.loadSamplesAndScore(r)
	for _, ch := range r.channels {
		initDesired(ch)
		applyExclusion(ch)
		applyPause(ch)
		gradeHealth(ch)
	}
	e.aggregateGroups(r)
	for _, alert := range r.alerts {
		e.store.AddEvent(alert)
	}
	return nil
}

// SyncNow 立即同步目录并刷新分组聚合，供页面上的手动同步按钮调用。
func (e *Engine) SyncNow(ctx context.Context) error {
	conn, err := e.store.Connection()
	if err != nil {
		return err
	}
	e.Reconfigure(conn)
	if err := e.Sync(ctx); err != nil {
		return err
	}

	e.mu.Lock()
	e.lastCatalogSync = time.Now()
	e.mu.Unlock()

	if err := e.refreshGroupStates(); err != nil {
		return err
	}
	e.fireNotify()
	return nil
}

// ErrAlreadyRunning 表示上一轮调度尚未结束。
var ErrAlreadyRunning = errors.New("上一轮调度仍在执行")

// Status 返回引擎运行状态。
func (e *Engine) Status() Status {
	e.mu.Lock()
	status := Status{
		Running:           e.running,
		MonitoringEnabled: e.monitoringOK,
		LastRunAt:         e.lastRun,
		LastRunMs:         e.lastMs,
		LastRunError:      e.lastErr,
		LastSummary:       e.lastPlan,
	}
	e.mu.Unlock()

	if conn, err := e.store.Connection(); err == nil {
		status.AutoEnabled = conn.Enabled
		status.Configured = conn.BaseURL != "" && conn.AdminAPIKey != ""
	}
	return status
}

// Reconfigure 用最新连接配置刷新客户端。
func (e *Engine) Reconfigure(conn domain.Connection) {
	e.client.Reconfigure(conn.BaseURL, conn.AdminAPIKey, time.Duration(conn.TimeoutSeconds)*time.Second)
}

// RunOnce 执行一轮完整调度。
func (e *Engine) RunOnce(ctx context.Context) error {
	// 派生一个可取消的 context 并存起来，让 Cancel() 能中断本轮的所有网络请求。
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	e.mu.Lock()
	if e.stopping {
		e.mu.Unlock()
		return context.Canceled
	}
	if e.running {
		e.mu.Unlock()
		return ErrAlreadyRunning
	}
	e.running = true
	e.canceled = false
	e.cancelRun = cancel
	e.runDone = make(chan struct{})
	e.mu.Unlock()

	started := time.Now()
	summary, err := e.runOnce(runCtx)

	e.mu.Lock()
	e.running = false
	e.cancelRun = nil
	wasCanceled := e.canceled
	stopping := e.stopping
	done := e.runDone
	e.runDone = nil
	e.lastRun = time.Now()
	e.lastMs = time.Since(started).Milliseconds()
	switch {
	case wasCanceled || (stopping && errors.Is(err, context.Canceled)):
		// 人工取消不是故障，不该在界面上显示成错误。
		e.lastErr = ""
		e.lastPlan = summary
	case err != nil:
		e.lastErr = err.Error()
	default:
		e.lastErr = ""
		e.lastPlan = summary
	}
	if done != nil {
		close(done)
	}
	e.mu.Unlock()

	e.fireNotify()
	if wasCanceled || (stopping && errors.Is(err, context.Canceled)) {
		return nil
	}
	return err
}

// Cancel 停止自动调度：中断当前轮次，并把「自动守护」持久化为关闭。
//
// 两件事必须一起做。早期版本只中断当前轮次，而 tick() 15 秒后照常再起一轮 ——
// 用户点了「取消调度」，探测和写回却还在继续，按钮也一直停在「取消调度」上。
//
// 关掉之后目录仍按 catalogSyncInterval 定时同步，页面数据不会变旧；
// 只是不再探测、不再熔断、不再写回。已采集的样本保留 —— 它们是真实观测。
//
// 返回是否中断了正在执行的轮次。无论有没有轮次在跑，自动守护都会被关掉。
func (e *Engine) Cancel() bool {
	e.mu.Lock()
	cancel := e.cancelRun
	if cancel != nil {
		e.canceled = true
	}
	e.mu.Unlock()

	stopped := false
	if cancel != nil {
		cancel()
		stopped = true
	}

	// 持久化关闭，重启也保持停止状态。
	if err := e.setAutoEnabled(false); err != nil {
		e.store.Log("error", "cancel_failed", nil, nil,
			fmt.Sprintf("停止自动调度失败: %s", err), nil)
	}

	message := "已停止自动调度（当前轮次已中断）"
	if !stopped {
		message = "已停止自动调度"
	}
	e.store.Log("warn", "run_canceled", nil, nil, message, nil)
	e.fireNotify()
	return stopped
}

// Resume 重新开启自动调度。
func (e *Engine) Resume() error {
	if err := e.setAutoEnabled(true); err != nil {
		return err
	}
	e.store.Log("info", "run_resumed", nil, nil, "已恢复自动调度", nil)
	e.fireNotify()
	return nil
}

// setAutoEnabled 持久化自动守护开关。
func (e *Engine) setAutoEnabled(enabled bool) error {
	conn, err := e.store.Connection()
	if err != nil {
		return err
	}
	if conn.Enabled == enabled {
		return nil
	}
	conn.Enabled = enabled
	return e.store.SaveConnection(conn)
}

func (e *Engine) runOnce(ctx context.Context) (Summary, error) {
	var summary Summary

	conn, err := e.store.Connection()
	if err != nil {
		return summary, err
	}
	e.Reconfigure(conn)
	if err := e.client.Ready(); err != nil {
		return summary, err
	}

	global, err := e.store.Policy()
	if err != nil {
		return summary, err
	}
	if err := e.Sync(ctx); err != nil {
		return summary, err
	}
	e.mu.Lock()
	e.lastCatalogSync = time.Now()
	e.mu.Unlock()

	overrides, err := e.store.GroupOverrides()
	if err != nil {
		return summary, err
	}
	groups, err := e.store.Groups()
	if err != nil {
		return summary, err
	}
	accounts, err := e.store.Accounts()
	if err != nil {
		return summary, err
	}
	states, err := e.store.ChannelStateMap()
	if err != nil {
		return summary, err
	}
	baselines, err := e.store.Baselines()
	if err != nil {
		return summary, err
	}

	r := buildRound(time.Now(), global, overrides, groups, accounts, states, baselines)
	r.upstreamMultipliers, err = e.store.UpstreamMultipliers()
	if err != nil {
		return summary, err
	}
	r.monitoringOK = e.checkMonitoring(ctx, global)

	summary.Channels = len(r.channels)
	summary.Probed, summary.Samples = e.collect(ctx, r)

	// 取消检查点：采样已经停了，不要再基于半截数据做熔断与写回决策。
	// 已采到的样本保留 —— 它们是真实观测，下一轮照常参与评分。
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	resolveMultipliers(r)
	e.loadSamplesAndScore(r)

	e.decide(r)
	summary.Applied = e.applyAll(ctx, r)

	// 统计与分组聚合都放在写回之后：本轮报出的「熔断 N 个」应当是真的摘掉了
	// N 个，健康矩阵也该反映 sub2api 的实际状况，而不是引擎的意图。
	// 写回失败的那些留待下轮重试时再计。
	summary.Fused, summary.Recovered = countTransitions(r)
	e.aggregateGroups(r)

	// 清理排在写回之后：先让熔断真正生效，再考虑要不要处置。
	summary.CleanedUp = e.applyCleanup(ctx, r)

	if err := e.persist(ctx, r); err != nil {
		return summary, err
	}
	summary.Alerts = len(r.alerts)
	e.maintenance(r)
	return summary, nil
}

// checkMonitoring 判断能否读取 sub2api 的真实流量，结果会缓存一段时间。
func (e *Engine) checkMonitoring(ctx context.Context, p policy.Policy) bool {
	if !p.Traffic.Enabled {
		return false
	}
	e.mu.Lock()
	cached := e.monitoringOK
	checked := e.monitoringChecked
	e.mu.Unlock()

	if time.Since(checked) < monitoringRecheckInterval {
		return cached
	}

	ok, err := e.client.MonitoringEnabled(ctx)
	if err != nil {
		// 探测失败不改变既有结论，避免网络抖动来回切换模式。
		return cached
	}

	e.mu.Lock()
	changed := e.monitoringOK != ok
	e.monitoringOK = ok
	e.monitoringChecked = time.Now()
	e.mu.Unlock()

	if changed {
		if ok {
			e.store.Log("info", "traffic_enabled", nil, nil, "已接入 sub2api 真实流量样本", nil)
		} else {
			e.store.Log("warn", "traffic_disabled", nil, nil,
				"sub2api 运维监控未开启，已降级为纯探针模式", nil)
		}
	}
	return ok
}

// Sync 从 sub2api 拉取分组与账号并刷新缓存。
func (e *Engine) Sync(ctx context.Context) error {
	groups, err := e.client.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("拉取分组失败: %w", err)
	}
	if err := e.store.ReplaceGroups(groups); err != nil {
		return err
	}

	accounts, err := e.client.ListAccounts(ctx, "")
	if err != nil {
		return fmt.Errorf("拉取账号失败: %w", err)
	}

	// 当前 sub2api 账号列表稳定回填 group_ids，直接使用可避免每轮按分组 N+1 查询。
	// 仅当整批账号完全没有归属信息时，才兼容旧版接口并逐组补齐。
	byID := make(map[int64]domain.Account, len(accounts))
	hasGroupIDs := false
	for _, account := range accounts {
		byID[account.ID] = account
		if len(account.GroupIDSet()) > 0 {
			hasGroupIDs = true
		}
	}
	if !hasGroupIDs && len(accounts) > 0 {
		for _, group := range groups {
			members, err := e.client.ListAccountsByGroup(ctx, group.ID, "")
			if err != nil {
				e.store.Log("warn", "sync_group_accounts", nil, accountRef(group.ID),
					fmt.Sprintf("分组 %s 账号同步失败: %s", group.Name, err), nil)
				continue
			}
			for _, member := range members {
				existing, ok := byID[member.ID]
				if !ok {
					existing = member
				}
				existing.GroupIDs = mergeIDs(existing.GroupIDs, group.ID)
				byID[member.ID] = existing
			}
		}
	}

	merged := make([]domain.Account, 0, len(byID))
	for _, account := range byID {
		merged = append(merged, account)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })

	if err := e.store.ReplaceAccounts(merged); err != nil {
		return err
	}

	keep := make(map[int64]struct{}, len(merged))
	for _, account := range merged {
		keep[account.ID] = struct{}{}
	}
	if err := e.store.DeleteChannelStates(keep); err != nil {
		return err
	}
	if err := e.store.PruneUpstreamMultipliers(keep); err != nil {
		return err
	}
	if _, err := e.refreshEnabledUpstreamMultipliers(ctx, merged); err != nil {
		return err
	}
	return nil
}

func (e *Engine) maintenance(r *round) {
	ids := make([]int64, 0, len(r.channels))
	for _, ch := range r.channels {
		ids = append(ids, ch.account.ID)
	}
	_ = e.store.PruneSamples(ids)
	_ = e.store.PruneEvents(5000)
	_ = e.store.PruneActions(2000)
	// 过期会话搭这趟车清理，不为它单开一个后台 goroutine。
	_, _ = e.store.PurgeExpiredSessions()
}

func mergeIDs(items []int64, id int64) []int64 {
	if id == 0 {
		return items
	}
	for _, item := range items {
		if item == id {
			return items
		}
	}
	return append(items, id)
}

// countTransitions 统计本轮**实际发生**的熔断与回池数量。
//
// 必须在 applyAll 之后调用：用 effectiveHealth 而非 desired，
// 写回失败的渠道不计入，避免摘要报出并未发生的处置。
func countTransitions(r *round) (fused, recovered int) {
	for _, ch := range r.channels {
		wasFused := ch.state.Health == domain.HealthFused
		isFused := ch.effectiveHealth() == domain.HealthFused
		switch {
		case !wasFused && isFused:
			fused++
		case wasFused && !isFused:
			recovered++
		}
	}
	return fused, recovered
}
