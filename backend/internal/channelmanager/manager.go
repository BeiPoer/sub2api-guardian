package channelmanager

import (
	"context"
	"net/http"
	"sync"
	"time"

	"sub2api-guardian/backend/internal/store"
)

const (
	defaultHTTPTimeout       = 30 * time.Second
	defaultSyncTimeout       = 3 * time.Minute
	defaultCleanupInterval   = 24 * time.Hour
	upstreamHistoryRetention = 7 * 24 * time.Hour
)

// Manager 是上游渠道功能的唯一运行期入口。
type Manager struct {
	store  *store.Store
	client *http.Client

	wecomMu             sync.Mutex
	wecomBaseURL        string
	wecomAccessToken    string
	wecomTokenExpiresAt time.Time

	lockMu sync.Mutex
	locks  map[int64]*sync.Mutex

	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	started   bool
}

func New(st *store.Store) *Manager {
	return &Manager{
		store:        st,
		client:       &http.Client{Timeout: defaultHTTPTimeout},
		wecomBaseURL: wecomAPIBaseURL,
		locks:        make(map[int64]*sync.Mutex),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Start 启动独立的每分钟自动任务循环。测试和 API 构造不会隐式启动它。
func (m *Manager) Start() {
	m.startOnce.Do(func() {
		m.lockMu.Lock()
		m.started = true
		m.lockMu.Unlock()
		go m.loop()
	})
}

func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.lockMu.Lock()
		started := m.started
		m.lockMu.Unlock()
		if !started {
			return
		}
		close(m.stop)
		<-m.done
	})
}

func (m *Manager) loop() {
	defer close(m.done)
	// 启动时先清理一次，随后每 24 小时清理一次；任务检查仍按分钟执行。
	_ = m.store.CleanupUpstreamHistory(time.Now().Add(-upstreamHistoryRetention))
	m.runDue(context.Background())
	ticker := time.NewTicker(time.Minute)
	cleanup := time.NewTicker(defaultCleanupInterval)
	defer ticker.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.runDue(context.Background())
		case <-cleanup.C:
			_ = m.store.CleanupUpstreamHistory(time.Now().Add(-upstreamHistoryRetention))
		}
	}
}

func (m *Manager) lockFor(channelID int64) *sync.Mutex {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()
	if lock := m.locks[channelID]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	m.locks[channelID] = lock
	return lock
}

func (m *Manager) withChannelLock(ctx context.Context, channelID int64, fn func() error) error {
	lock := m.lockFor(channelID)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !lock.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer lock.Unlock()
	return fn()
}

// Sync 将一个可操作渠道完整同步到独立缓存。
func (m *Manager) Sync(ctx context.Context, channelID int64) error {
	return m.withChannelLock(ctx, channelID, func() error {
		return m.syncLocked(ctx, channelID)
	})
}

// SyncAll 顺序刷新全部未忽略的可操作渠道，返回成功和失败数量。
func (m *Manager) SyncAll(ctx context.Context) (int, int, error) {
	channels, err := m.store.UpstreamChannels()
	if err != nil {
		return 0, 0, err
	}
	ok, failed := 0, 0
	for _, channel := range channels {
		if channel.Ignored || channel.Type == store.UpstreamChannelOther {
			continue
		}
		if err := m.Sync(ctx, channel.ID); err != nil {
			failed++
			continue
		}
		ok++
	}
	return ok, failed, nil
}

func (m *Manager) Balance(ctx context.Context, channelID int64) (*store.UpstreamBalanceSnapshot, error) {
	var result *store.UpstreamBalanceSnapshot
	err := m.withChannelLock(ctx, channelID, func() error {
		var err error
		result, err = m.balanceLocked(ctx, channelID)
		return err
	})
	return result, err
}

func (m *Manager) Overview(channelID int64) (Overview, error) {
	channel, err := m.store.UpstreamChannel(channelID)
	if err != nil {
		return Overview{}, channelError(err)
	}
	profile := any(nil)
	groups := any([]any{})
	tokens := any([]any{})
	subscriptions := any(nil)
	for key, target := range map[string]*any{
		"profile":       &profile,
		"groups":        &groups,
		"tokens":        &tokens,
		"subscriptions": &subscriptions,
	} {
		entry, cacheErr := m.store.UpstreamCache(channelID, key)
		if cacheErr != nil {
			return Overview{}, cacheErr
		}
		if entry.Exists {
			*target = entry.Value
		}
	}
	latest, err := m.store.LatestUpstreamBalanceSnapshot(channelID)
	if err != nil {
		return Overview{}, err
	}
	history, err := m.store.UpstreamBalanceHistory(channelID, 30)
	if err != nil {
		return Overview{}, err
	}
	return Overview{Channel: channel, Profile: profile, Groups: groups, Tokens: tokens, Subscriptions: subscriptions, LatestSnapshot: latest, History: history}, nil
}

func (m *Manager) TokenModels(ctx context.Context, channelID, tokenID int64) (TokenModelsResult, error) {
	var result TokenModelsResult
	err := m.withChannelLock(ctx, channelID, func() error {
		var err error
		result, err = m.tokenModelsLocked(ctx, channelID, tokenID)
		return err
	})
	return result, err
}

func (m *Manager) UpdateTokenGroup(ctx context.Context, channelID, tokenID int64, group any) (any, error) {
	var result any
	err := m.withChannelLock(ctx, channelID, func() error {
		var err error
		result, err = m.updateTokenGroupLocked(ctx, channelID, tokenID, group)
		return err
	})
	return result, err
}

func (m *Manager) runDue(ctx context.Context) {
	// 自动任务失败只更新渠道/任务状态，不能终止下一渠道的检查。
	_ = m.runDueTasks(ctx)
}
