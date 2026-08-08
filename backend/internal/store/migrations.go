package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/scoring"
)

const (
	metaSchemaVersion = "schema_version"
	metaConnection    = "connection"
	metaPolicy        = "policy_global"

	currentSchemaVersion = "5"
)

var schemaStatements = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA busy_timeout=5000`,
	`CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS group_overrides (
		group_id INTEGER PRIMARY KEY,
		json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS groups_cache (
		id INTEGER PRIMARY KEY,
		json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS accounts_cache (
		id INTEGER PRIMARY KEY,
		json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL,
		occurred_at TEXT NOT NULL,
		source TEXT NOT NULL,
		event_type TEXT NOT NULL,
		score REAL NOT NULL,
		ttfb_ms INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		status_code INTEGER NOT NULL DEFAULT 0,
		model TEXT NOT NULL DEFAULT '',
		request_model TEXT NOT NULL DEFAULT '',
		request_id TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_samples_account_time ON samples(account_id, occurred_at DESC)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_samples_request ON samples(account_id, request_id) WHERE request_id <> ''`,
	`CREATE TABLE IF NOT EXISTS channel_states (
		account_id INTEGER PRIMARY KEY,
		group_id INTEGER,
		health TEXT NOT NULL DEFAULT 'unknown',
		health_score REAL NOT NULL DEFAULT 0,
		json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS group_states (
		group_id INTEGER PRIMARY KEY,
		status TEXT NOT NULL DEFAULT 'empty',
		json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS baselines (
		account_id INTEGER PRIMARY KEY,
		json TEXT NOT NULL,
		captured_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS actions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL,
		kind TEXT NOT NULL,
		before_json TEXT NOT NULL DEFAULT '',
		after_json TEXT NOT NULL DEFAULT '',
		ok INTEGER NOT NULL DEFAULT 1,
		error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_actions_account ON actions(account_id, id DESC)`,
	`CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		level TEXT NOT NULL,
		action TEXT NOT NULL,
		account_id INTEGER,
		group_id INTEGER,
		message TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_events_id ON events(id DESC)`,
	`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// token_hash 存的是会话令牌的 SHA-256，不是令牌本身：
	// 数据库被读走也无法直接冒用别人的会话。
	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		user_agent TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at)`,
	`CREATE TABLE IF NOT EXISTS image2_upstreams (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		slug TEXT NOT NULL UNIQUE,
		base_url TEXT NOT NULL,
		api_key TEXT NOT NULL,
		-- 旧版本字段，当前代理不再读取或写入。
		supports_url INTEGER NOT NULL DEFAULT 0,
		model_mapping TEXT NOT NULL DEFAULT ''
	)`,
}

// migrate 建表并在必要时从 0.1 版原型库升级。
func (s *Store) migrate() error {
	hasMeta, err := s.tableExists("meta")
	if err != nil {
		return err
	}
	legacy := false
	if !hasMeta {
		legacy, err = s.tableExists("config")
		if err != nil {
			return err
		}
	}

	var carried *legacySnapshot
	if legacy {
		carried, err = s.readLegacy()
		if err != nil {
			return fmt.Errorf("read legacy data: %w", err)
		}
		if err := s.renameLegacyTables(); err != nil {
			return fmt.Errorf("rename legacy tables: %w", err)
		}
	}

	for _, stmt := range schemaStatements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("apply schema %q: %w", firstLine(stmt), err)
		}
	}

	if _, err := s.getMeta(metaConnection); IsNotFound(err) {
		conn := domain.DefaultConnection()
		if carried != nil {
			conn = carried.connection
		}
		if err := s.setJSON(metaConnection, conn); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if _, err := s.getMeta(metaPolicy); IsNotFound(err) {
		p := policy.Default()
		if carried != nil {
			p = carried.policy
		}
		policy.Normalize(&p)
		if err := s.setJSON(metaPolicy, p); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if carried != nil {
		for _, base := range carried.baselines {
			if err := s.SaveBaseline(base); err != nil {
				return fmt.Errorf("carry baseline %d: %w", base.AccountID, err)
			}
		}
		for _, ev := range carried.events {
			s.AddEvent(ev)
		}
		s.AddEvent(domain.Event{
			Level:   "info",
			Action:  "migrate",
			Message: fmt.Sprintf("已从旧版数据库迁移：%d 条基线、%d 条事件", len(carried.baselines), len(carried.events)),
		})
	}

	// 增量列迁移：已存在的库不会重跑 CREATE TABLE，需要单独补列。
	if err := s.addMissingColumns(); err != nil {
		return err
	}
	if err := s.migratePolicyFields(); err != nil {
		return err
	}
	if err := s.reclassifyQuotaSamples(); err != nil {
		return err
	}

	return s.setMeta(metaSchemaVersion, currentSchemaVersion)
}

// reclassifyQuotaQuery 生成重新归类的 SQL 与参数。
//
// 关键字与状态码一律取自 scoring 包，不在这里手抄一份。
// 第一版迁移就是手抄的，漏了 "balance"，于是 Grok 的
// "Grok Build usage balance exhausted" 没被改判，那些渠道继续卡在 0 分 ——
// 两份清单只要有一份漏更新，迁移就会静默地少修一部分数据。
func reclassifyQuotaQuery(score float64) (string, []any) {
	args := []any{string(domain.EventQuotaExhausted), score, string(domain.EventFatal)}

	var quota []string
	for _, code := range scoring.QuotaStatusCodes() {
		quota = append(quota, "status_code = ?")
		args = append(args, code)
	}
	for _, pattern := range scoring.QuotaPatterns() {
		quota = append(quota, "instr(lower(message), ?) > 0")
		args = append(args, strings.ToLower(pattern))
	}

	var auth []string
	for _, pattern := range scoring.AuthFailurePatterns() {
		auth = append(auth, "instr(lower(message), ?) = 0")
		args = append(args, strings.ToLower(pattern))
	}

	query := `UPDATE samples SET event_type = ?, score = ? WHERE event_type = ?` +
		" AND (" + strings.Join(quota, " OR ") + ")"
	if len(auth) > 0 {
		// 凭据失效的措辞优先：同时提到额度与 unauthorized 时按更严重的处理。
		query += " AND (" + strings.Join(auth, " AND ") + ")"
	}
	return query, args
}

// reclassifyQuotaSamples 把历史上误判为致命的限流样本改成 quota_exhausted。
//
// 样本把分类结果一起存了下来，所以仅改判定逻辑救不了老数据：
// 限流报文（429 usage_limit_reached）此前命中致命关键字被记为 fatal、分数 0，
// 而最新一条 fatal 会一票否决健康分 —— 这些渠道会一直卡在 0 分回不了池，
// 即使上游早就恢复了。
//
// 只跑一次（靠 schema 版本控制），只动明确是限流的那些行。
func (s *Store) reclassifyQuotaSamples() error {
	version, err := s.getMeta(metaSchemaVersion)
	if err != nil && !IsNotFound(err) {
		return err
	}
	// 已经是当前版本说明迁移跑过了。
	if version == currentSchemaVersion {
		return nil
	}

	score := policy.Default().Scoring.EventScores.QuotaExhausted
	query, args := reclassifyQuotaQuery(score)

	s.mu.Lock()
	res, err := s.db.Exec(query, args...)
	var affected int64
	if err == nil {
		affected, _ = res.RowsAffected()
	}
	// AddEvent 自己会取锁，必须先放开再调，否则死锁。
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("reclassify quota samples: %w", err)
	}
	if affected > 0 {
		s.AddEvent(domain.Event{
			Level:  "info",
			Action: "migrate",
			Message: fmt.Sprintf(
				"已把 %d 条限流/额度样本从「致命错误」重新归类为「限流」，"+
					"这些渠道的健康分不再被强制归零，可随成功探测自动回池", affected),
		})
	}
	return nil
}

// migratePolicyFields 给老策略补上后续版本新增的字段。
//
// 策略以 JSON 整体存储，新增字段在老记录里会反序列化成零值。对数值字段
// Normalize 能靠「<=0 视为未设置」兜底，但对 bool 与「0 是合法值」的数值不行：
//   - HTTPDegradeOnly 的新默认是 true，老库读出来是 false，
//     用户会继续沿用旧的「网关错误就熔断」行为而不自知；
//   - QuotaExhausted 的新默认是 15，老库读出来是 0，
//     而 0 分低于回池目标分 —— 限流的渠道会被永久钉死。
//
// 因此按「字段在 JSON 里存在与否」判断，缺失才填默认值；已存在则尊重用户的设置。
func (s *Store) migratePolicyFields() error {
	raw, err := s.getMeta(metaPolicy)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var probe struct {
		Scoring struct {
			EventScores map[string]json.RawMessage `json:"event_scores"`
		} `json:"scoring"`
		Breaker map[string]json.RawMessage `json:"breaker"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		// 解析不了就不动，交给 Normalize 兜底。
		return nil
	}

	var p policy.Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil
	}
	d := policy.Default()
	changed := false

	if _, ok := probe.Scoring.EventScores["quota_exhausted"]; !ok {
		p.Scoring.EventScores.QuotaExhausted = d.Scoring.EventScores.QuotaExhausted
		changed = true
	}
	if _, ok := probe.Breaker["http_degrade_only"]; !ok {
		p.Breaker.HTTPDegradeOnly = d.Breaker.HTTPDegradeOnly
		changed = true
	}
	if _, ok := probe.Breaker["latency_degrade_only"]; !ok {
		p.Breaker.LatencyDegradeOnly = d.Breaker.LatencyDegradeOnly
		changed = true
	}
	if !changed {
		return nil
	}

	policy.Normalize(&p)
	return s.setJSON(metaPolicy, p)
}

// addMissingColumns 为老库补上后续版本新增的列。
//
// SQLite 的 ADD COLUMN 是幂等不了的，重复执行会报 duplicate column，
// 因此先查一遍现有列再决定加不加。
func (s *Store) addMissingColumns() error {
	additions := []struct {
		table  string
		column string
		ddl    string
	}{
		{"samples", "request_model", `ALTER TABLE samples ADD COLUMN request_model TEXT NOT NULL DEFAULT ''`},
	}

	for _, add := range additions {
		exists, err := s.columnExists(add.table, add.column)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.Exec(add.ddl); err != nil {
			return fmt.Errorf("add column %s.%s: %w", add.table, add.column, err)
		}
	}
	return nil
}

func (s *Store) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

type legacySnapshot struct {
	connection domain.Connection
	policy     policy.Policy
	baselines  []domain.Baseline
	events     []domain.Event
}

// legacyConfig 是 0.1 版原型的配置结构。
type legacyConfig struct {
	Sub2APIBaseURL        string            `json:"sub2api_base_url"`
	AdminAPIKey           string            `json:"admin_api_key"`
	MonitorEnabled        bool              `json:"monitor_enabled"`
	CheckIntervalSeconds  int               `json:"check_interval_seconds"`
	RequestTimeoutSeconds int               `json:"request_timeout_seconds"`
	Concurrency           int               `json:"concurrency"`
	TestModel             string            `json:"test_model"`
	AccountTestModels     map[string]string `json:"account_test_models"`
	TestPrompt            string            `json:"test_prompt"`
	Strategy              string            `json:"strategy"`
	BreakerEnabled        bool              `json:"breaker_enabled"`
	BreakerErrorPatterns  []string          `json:"breaker_error_patterns"`
	BreakerRecoverAfter   int               `json:"breaker_recover_after_seconds"`
	DegradeEnabled        bool              `json:"degrade_enabled"`
	DegradePriorityStep   int               `json:"degrade_priority_step"`
	MinLoadFactor         int               `json:"min_load_factor"`
	ManagedGroupMode      string            `json:"managed_group_mode"`
	ManagedGroupIDs       []int64           `json:"managed_group_ids"`
}

func (s *Store) readLegacy() (*legacySnapshot, error) {
	out := &legacySnapshot{
		connection: domain.DefaultConnection(),
		policy:     policy.Default(),
	}

	var raw string
	err := s.db.QueryRow(`SELECT json FROM config WHERE id = 1`).Scan(&raw)
	if err != nil && !IsNotFound(err) {
		return nil, err
	}
	if err == nil {
		var old legacyConfig
		if err := json.Unmarshal([]byte(raw), &old); err != nil {
			return nil, err
		}
		out.connection = legacyConnection(old)
		out.policy = legacyPolicy(old)
	}

	baselines, err := s.readLegacyBaselines()
	if err != nil {
		return nil, err
	}
	out.baselines = baselines

	events, err := s.readLegacyEvents()
	if err != nil {
		return nil, err
	}
	out.events = events

	return out, nil
}

func legacyConnection(old legacyConfig) domain.Connection {
	conn := domain.DefaultConnection()
	if strings.TrimSpace(old.Sub2APIBaseURL) != "" {
		conn.BaseURL = old.Sub2APIBaseURL
	}
	conn.AdminAPIKey = old.AdminAPIKey
	if old.RequestTimeoutSeconds > 0 {
		conn.TimeoutSeconds = old.RequestTimeoutSeconds
	}
	conn.Enabled = old.MonitorEnabled
	return conn
}

func legacyPolicy(old legacyConfig) policy.Policy {
	p := policy.Default()

	switch old.Strategy {
	case "first_token":
		p.Strategy = policy.StrategySpeed
	case "custom":
		p.Strategy = policy.StrategyBalanced
	case "price":
		p.Strategy = policy.StrategyPrice
	}

	if old.CheckIntervalSeconds > 0 {
		p.Probe.IntervalSeconds = old.CheckIntervalSeconds
	}
	if old.Concurrency > 0 {
		p.Probe.Concurrency = old.Concurrency
	}
	p.Probe.Model = old.TestModel
	if strings.TrimSpace(old.TestPrompt) != "" {
		p.Probe.Prompt = old.TestPrompt
	}

	p.Breaker.Enabled = old.BreakerEnabled
	if len(old.BreakerErrorPatterns) > 0 {
		p.Classify.FatalPatterns = old.BreakerErrorPatterns
	}
	if old.BreakerRecoverAfter > 0 {
		p.Recovery.ProbeIntervalSeconds = old.BreakerRecoverAfter
	}

	p.Degrade.Enabled = old.DegradeEnabled
	if old.DegradePriorityStep > 0 {
		p.Degrade.PriorityStep = old.DegradePriorityStep
	}
	if old.MinLoadFactor > 0 {
		p.Degrade.MinLoadFactor = old.MinLoadFactor
		p.Weights.MinLoadFactor = old.MinLoadFactor
	}

	if old.ManagedGroupMode == "selected" {
		p.ManagedGroupMode = "selected"
		p.ManagedGroupIDs = old.ManagedGroupIDs
	}
	if len(old.AccountTestModels) > 0 {
		p.AccountTestModels = old.AccountTestModels
	}
	return p
}

// readLegacyBaselines 把旧表里的 original_* 字段提升为基线，避免丢失用户的真实原值。
func (s *Store) readLegacyBaselines() ([]domain.Baseline, error) {
	exists, err := s.tableExists("channel_states")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT account_id, original_priority, original_load_factor,
		original_rate_multiplier, updated_at FROM channel_states`)
	if err != nil {
		// 旧库结构不符合预期时不阻塞升级。
		return nil, nil
	}
	defer rows.Close()

	var out []domain.Baseline
	for rows.Next() {
		var (
			accountID  int64
			priority   int
			loadFactor sql.NullInt64
			rate       float64
			updatedAt  sql.NullString
		)
		if err := rows.Scan(&accountID, &priority, &loadFactor, &rate, &updatedAt); err != nil {
			return nil, err
		}
		base := domain.Baseline{
			AccountID:      accountID,
			Priority:       priority,
			RateMultiplier: rate,
			Schedulable:    true,
			CapturedAt:     nullTime(updatedAt),
		}
		if base.CapturedAt.IsZero() {
			base.CapturedAt = time.Now()
		}
		if loadFactor.Valid {
			v := int(loadFactor.Int64)
			base.LoadFactor = &v
		}
		out = append(out, base)
	}
	return out, rows.Err()
}

func (s *Store) readLegacyEvents() ([]domain.Event, error) {
	exists, err := s.tableExists("events")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT level, action, account_id, message, detail, created_at
		FROM events ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	var out []domain.Event
	for rows.Next() {
		var (
			ev        domain.Event
			accountID sql.NullInt64
			created   sql.NullString
		)
		if err := rows.Scan(&ev.Level, &ev.Action, &accountID, &ev.Message, &ev.Detail, &created); err != nil {
			return nil, err
		}
		if accountID.Valid {
			v := accountID.Int64
			ev.AccountID = &v
		}
		ev.CreatedAt = nullTime(created)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 旧表是倒序读出的，这里反转回时间正序，插入后 id 顺序才与时间一致。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// renameLegacyTables 把旧表改名保留为备份，为新 schema 让出表名。
func (s *Store) renameLegacyTables() error {
	suffix := "_v0_" + time.Now().Format("20060102150405")
	for _, name := range []string{"config", "channel_states", "group_states", "events", "api_keys_cache", "groups_cache", "accounts_cache"} {
		exists, err := s.tableExists(name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s RENAME TO legacy_%s%s`, name, name, suffix)); err != nil {
			return err
		}
	}
	return nil
}

func firstLine(stmt string) string {
	stmt = strings.TrimSpace(stmt)
	if idx := strings.IndexByte(stmt, '\n'); idx > 0 {
		return stmt[:idx]
	}
	return stmt
}
