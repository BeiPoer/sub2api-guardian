package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestFreshDatabaseHasDefaults(t *testing.T) {
	st := openTemp(t)

	conn, err := st.Connection()
	if err != nil {
		t.Fatalf("Connection() 失败: %v", err)
	}
	if conn.BaseURL != domain.DefaultConnection().BaseURL {
		t.Fatalf("默认地址 = %q, 期望 %q", conn.BaseURL, domain.DefaultConnection().BaseURL)
	}

	p, err := st.Policy()
	if err != nil {
		t.Fatalf("Policy() 失败: %v", err)
	}
	if p.Strategy != policy.StrategyPrice {
		t.Fatalf("默认策略 = %v, 期望 price", p.Strategy)
	}
	if p.Breaker.MinPoolSize != 1 {
		t.Fatalf("默认保底池 = %d, 期望 1", p.Breaker.MinPoolSize)
	}
}

func TestDatabaseFileUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file permission bits")
	}
	path := filepath.Join(t.TempDir(), "guardian.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	defer func() { _ = st.Close() }()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取数据库权限失败: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("数据库权限 = %o，期望 600", got)
	}
}

func TestPolicyRoundTripNormalizes(t *testing.T) {
	st := openTemp(t)

	p, _ := st.Policy()
	p.Strategy = "nonsense"
	p.Scoring.ShortWindow = -5
	p.Weights.ChangeThreshold = 99

	saved, err := st.SavePolicy(p)
	if err != nil {
		t.Fatalf("SavePolicy() 失败: %v", err)
	}
	if saved.Strategy != policy.StrategyPrice {
		t.Fatalf("非法策略应回落为默认值，实际 %v", saved.Strategy)
	}
	if saved.Scoring.ShortWindow != 10 {
		t.Fatalf("非法窗口应回落为 10，实际 %d", saved.Scoring.ShortWindow)
	}
	if saved.Weights.ChangeThreshold != 0.1 {
		t.Fatalf("非法阈值应回落为 0.1，实际 %v", saved.Weights.ChangeThreshold)
	}

	reloaded, err := st.Policy()
	if err != nil || reloaded.Strategy != policy.StrategyPrice {
		t.Fatalf("重新读取失败: %v / %v", err, reloaded.Strategy)
	}
}

func TestGroupOverrideRoundTrip(t *testing.T) {
	st := openTemp(t)

	strategy := policy.StrategySpeed
	minPool := 2
	if err := st.SaveGroupOverride(7, policy.GroupOverride{
		Strategy:    &strategy,
		MinPoolSize: &minPool,
	}); err != nil {
		t.Fatalf("SaveGroupOverride() 失败: %v", err)
	}

	global, _ := st.Policy()
	override, err := st.GroupOverride(7)
	if err != nil || override == nil {
		t.Fatalf("GroupOverride() = %v / %v", override, err)
	}
	effective := global.ForGroup(override)
	if effective.Strategy != policy.StrategySpeed {
		t.Fatalf("分组策略 = %v, 期望 speed", effective.Strategy)
	}
	if effective.Breaker.MinPoolSize != 2 {
		t.Fatalf("分组保底池 = %d, 期望 2", effective.Breaker.MinPoolSize)
	}
	// 全局策略不应被分组覆盖污染。
	if global.Strategy != policy.StrategyPrice {
		t.Fatalf("全局策略被污染为 %v", global.Strategy)
	}

	if err := st.DeleteGroupOverride(7); err != nil {
		t.Fatalf("DeleteGroupOverride() 失败: %v", err)
	}
	if override, err := st.GroupOverride(7); err != nil || override != nil {
		t.Fatalf("删除后应返回 nil，实际 %v / %v", override, err)
	}
}

func TestSampleDedupAndOrdering(t *testing.T) {
	st := openTemp(t)
	base := time.Now()

	// 同一个 request_id 重复写入只保留一条。
	for i := 0; i < 3; i++ {
		if err := st.AddSample(domain.Sample{
			AccountID:  1,
			OccurredAt: base,
			Source:     domain.SourceTraffic,
			EventType:  domain.EventPerfect,
			Score:      100,
			RequestID:  "req-1",
		}); err != nil {
			t.Fatalf("AddSample() 失败: %v", err)
		}
	}
	// 探针样本没有 request_id，不受唯一索引约束。
	for i := 0; i < 2; i++ {
		if err := st.AddSample(domain.Sample{
			AccountID:  1,
			OccurredAt: base.Add(time.Duration(i+1) * time.Second),
			Source:     domain.SourceProbe,
			EventType:  domain.EventGatewayError,
			Score:      25,
		}); err != nil {
			t.Fatalf("AddSample() 失败: %v", err)
		}
	}

	samples, err := st.RecentSamples(1, 10)
	if err != nil {
		t.Fatalf("RecentSamples() 失败: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("样本数 = %d, 期望 3（1 条流量 + 2 条探针）", len(samples))
	}
	if samples[0].OccurredAt.Before(samples[len(samples)-1].OccurredAt) {
		t.Fatal("样本应按时间倒序返回")
	}

	latest, err := st.LatestRequestID(1)
	if err != nil || latest != "req-1" {
		t.Fatalf("LatestRequestID() = %q / %v, 期望 req-1", latest, err)
	}
}

func TestAddSampleIfNewReportsDuplicates(t *testing.T) {
	st := openTemp(t)
	sample := domain.Sample{
		AccountID:  1,
		OccurredAt: time.Now(),
		Source:     domain.SourceTraffic,
		EventType:  domain.EventPerfect,
		Score:      100,
		RequestID:  "req-dedup",
	}

	inserted, err := st.AddSampleIfNew(sample)
	if err != nil || !inserted {
		t.Fatalf("首次写入 = %v / %v，期望 inserted=true", inserted, err)
	}
	inserted, err = st.AddSampleIfNew(sample)
	if err != nil || inserted {
		t.Fatalf("重复写入 = %v / %v，期望 inserted=false", inserted, err)
	}
}

func TestPruneSamplesDropsUnknownAccounts(t *testing.T) {
	st := openTemp(t)
	for _, accountID := range []int64{1, 2} {
		if err := st.AddSample(domain.Sample{
			AccountID:  accountID,
			OccurredAt: time.Now(),
			Source:     domain.SourceProbe,
			EventType:  domain.EventPerfect,
			Score:      100,
		}); err != nil {
			t.Fatalf("AddSample() 失败: %v", err)
		}
	}
	if err := st.PruneSamples([]int64{1}); err != nil {
		t.Fatalf("PruneSamples() 失败: %v", err)
	}
	if samples, _ := st.RecentSamples(2, 10); len(samples) != 0 {
		t.Fatalf("账号 2 的样本应被清理，实际剩余 %d 条", len(samples))
	}
	if samples, _ := st.RecentSamples(1, 10); len(samples) != 1 {
		t.Fatalf("账号 1 的样本应保留，实际剩余 %d 条", len(samples))
	}
}

func TestBaselineRoundTrip(t *testing.T) {
	st := openTemp(t)
	loadFactor := 20
	base := domain.Baseline{
		AccountID:      5,
		Priority:       33,
		LoadFactor:     &loadFactor,
		Concurrency:    12,
		RateMultiplier: 1.5,
		Schedulable:    true,
	}
	if err := st.SaveBaseline(base); err != nil {
		t.Fatalf("SaveBaseline() 失败: %v", err)
	}
	got, err := st.Baseline(5)
	if err != nil {
		t.Fatalf("Baseline() 失败: %v", err)
	}
	if got.Priority != 33 || got.LoadFactor == nil || *got.LoadFactor != 20 || got.Concurrency != 12 {
		t.Fatalf("基线读回不一致: %+v", got)
	}
	if err := st.DeleteBaseline(5); err != nil {
		t.Fatalf("DeleteBaseline() 失败: %v", err)
	}
	if _, err := st.Baseline(5); !IsNotFound(err) {
		t.Fatalf("删除后应返回 NotFound，实际 %v", err)
	}
}

func TestEventFilterAndPaging(t *testing.T) {
	st := openTemp(t)
	groupID := int64(3)
	for i := 0; i < 5; i++ {
		st.AddEvent(domain.Event{Level: "info", Action: "probe", Message: "ok"})
	}
	st.AddEvent(domain.Event{Level: "error", Action: "breaker_open", GroupID: &groupID, Message: "熔断"})

	items, total, err := st.Events(EventFilter{Level: "error", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Events() 失败: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Action != "breaker_open" {
		t.Fatalf("按等级过滤失败: total=%d items=%d", total, len(items))
	}

	items, total, err = st.Events(EventFilter{GroupID: &groupID, Page: 1, PageSize: 10})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("按分组过滤失败: total=%d items=%d err=%v", total, len(items), err)
	}

	items, total, err = st.Events(EventFilter{Page: 2, PageSize: 4})
	if err != nil {
		t.Fatalf("Events() 失败: %v", err)
	}
	if total != 6 || len(items) != 2 {
		t.Fatalf("分页失败: total=%d 第二页 %d 条, 期望 total=6 第二页 2 条", total, len(items))
	}
}

// TestLegacyMigration 验证 0.1 版原型库能被无损升级：
// 配置拆成连接 + 策略，original_* 字段提升为基线，历史事件保留。
func TestLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	legacySchema := []string{
		`CREATE TABLE config (id INTEGER PRIMARY KEY CHECK(id = 1), json TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE channel_states (
			account_id INTEGER PRIMARY KEY,
			original_priority INTEGER NOT NULL DEFAULT 0,
			original_load_factor INTEGER,
			original_rate_multiplier REAL NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE group_states (group_id INTEGER PRIMARY KEY, status TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			level TEXT NOT NULL, action TEXT NOT NULL, account_id INTEGER,
			message TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
		)`,
		`CREATE TABLE api_keys_cache (id INTEGER PRIMARY KEY, group_id INTEGER, json TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	}
	for _, stmt := range legacySchema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("建旧表失败: %v", err)
		}
	}

	legacyConfigJSON := `{
		"sub2api_base_url": "http://10.0.0.9:9000",
		"admin_api_key": "legacy-key",
		"monitor_enabled": false,
		"check_interval_seconds": 600,
		"request_timeout_seconds": 45,
		"concurrency": 8,
		"test_model": "gpt-4o-mini",
		"test_prompt": "ping",
		"strategy": "first_token",
		"breaker_enabled": true,
		"breaker_error_patterns": ["custom pattern", "401"],
		"breaker_recover_after_seconds": 900,
		"degrade_enabled": true,
		"degrade_priority_step": 7,
		"min_load_factor": 4,
		"managed_group_mode": "selected",
		"managed_group_ids": [11, 22],
		"account_test_models": {"5": "claude-sonnet-5"}
	}`
	if _, err := db.Exec(`INSERT INTO config(id, json, updated_at) VALUES(1, ?, ?)`,
		legacyConfigJSON, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("写旧配置失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO channel_states(
		account_id, original_priority, original_load_factor, original_rate_multiplier, updated_at
	) VALUES(?, ?, ?, ?, ?)`, 42, 25, 30, 1.25, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("写旧状态失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO events(level, action, message, detail, created_at)
		VALUES('warn', 'test_failed', '旧事件', '', ?)`, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("写旧事件失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("升级旧库失败: %v", err)
	}
	defer func() { _ = st.Close() }()

	conn, err := st.Connection()
	if err != nil {
		t.Fatalf("Connection() 失败: %v", err)
	}
	if conn.BaseURL != "http://10.0.0.9:9000" || conn.AdminAPIKey != "legacy-key" {
		t.Fatalf("连接配置未迁移: %+v", conn)
	}
	if conn.TimeoutSeconds != 45 || conn.Enabled {
		t.Fatalf("超时/开关未迁移: %+v", conn)
	}

	p, err := st.Policy()
	if err != nil {
		t.Fatalf("Policy() 失败: %v", err)
	}
	if p.Strategy != policy.StrategySpeed {
		t.Fatalf("首 T 优先应迁移为 speed，实际 %v", p.Strategy)
	}
	if p.Probe.IntervalSeconds != 600 || p.Probe.Concurrency != 8 {
		t.Fatalf("探测配置未迁移: %+v", p.Probe)
	}
	if p.Probe.Model != "gpt-4o-mini" || p.Probe.Prompt != "ping" {
		t.Fatalf("测试模型/提示词未迁移: %+v", p.Probe)
	}
	if p.Recovery.ProbeIntervalSeconds != 900 {
		t.Fatalf("恢复探测间隔未迁移: %d", p.Recovery.ProbeIntervalSeconds)
	}
	if p.Degrade.PriorityStep != 7 || p.Degrade.MinLoadFactor != 4 {
		t.Fatalf("降级配置未迁移: %+v", p.Degrade)
	}
	if p.ManagedGroupMode != "selected" || len(p.ManagedGroupIDs) != 2 {
		t.Fatalf("受管分组未迁移: %v %v", p.ManagedGroupMode, p.ManagedGroupIDs)
	}
	if p.AccountTestModels["5"] != "claude-sonnet-5" {
		t.Fatalf("账号测试模型未迁移: %v", p.AccountTestModels)
	}
	if len(p.Classify.FatalPatterns) != 2 {
		t.Fatalf("错误关键字未迁移: %v", p.Classify.FatalPatterns)
	}

	base, err := st.Baseline(42)
	if err != nil {
		t.Fatalf("旧 original_* 应提升为基线: %v", err)
	}
	if base.Priority != 25 || base.LoadFactor == nil || *base.LoadFactor != 30 || base.RateMultiplier != 1.25 {
		t.Fatalf("基线迁移不正确: %+v", base)
	}

	events, total, err := st.Events(EventFilter{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("Events() 失败: %v", err)
	}
	if total < 2 {
		t.Fatalf("历史事件与迁移事件都应保留，实际 %d 条", total)
	}
	foundLegacy := false
	for _, event := range events {
		if event.Message == "旧事件" {
			foundLegacy = true
		}
	}
	if !foundLegacy {
		t.Fatal("旧事件未被保留")
	}

	// 再次打开不应重复迁移或报错。
	if err := st.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("二次打开失败: %v", err)
	}
	defer func() { _ = again.Close() }()
	_, totalAgain, err := again.Events(EventFilter{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("二次读取事件失败: %v", err)
	}
	if totalAgain != total {
		t.Fatalf("二次打开后事件数从 %d 变为 %d，说明迁移被重复执行", total, totalAgain)
	}
}
