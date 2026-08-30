package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// UpstreamChannelType 是外部渠道的协议族，与 Guardian 自身的渠道池无关。
type UpstreamChannelType string

const (
	UpstreamChannelSub2API UpstreamChannelType = "sub2api"
	UpstreamChannelNewAPI  UpstreamChannelType = "newapi"
	UpstreamChannelOther   UpstreamChannelType = "other"
)

const (
	UpstreamRechargeAlipay UpstreamRechargeMethod = "alipay"
	UpstreamRechargeWechat UpstreamRechargeMethod = "wechat"
	UpstreamRechargeCard   UpstreamRechargeMethod = "card"
)

type UpstreamRechargeMethod string

func (m UpstreamRechargeMethod) Valid() bool {
	return m == UpstreamRechargeAlipay || m == UpstreamRechargeWechat || m == UpstreamRechargeCard
}

func (t UpstreamChannelType) Valid() bool {
	return t == UpstreamChannelSub2API || t == UpstreamChannelNewAPI || t == UpstreamChannelOther
}

// UpstreamChannel 包含用户明确要求可在已登录面板中查看的上游凭据。
// access / refresh 会话令牌只供服务端刷新使用，永不序列化给前端。
type UpstreamChannel struct {
	ID                    int64                    `json:"id"`
	Name                  string                   `json:"name"`
	Type                  UpstreamChannelType      `json:"type"`
	BaseURL               string                   `json:"base_url"`
	Username              string                   `json:"username"`
	Password              string                   `json:"password"`
	NewAPIAccessToken     string                   `json:"newapi_access_token"`
	NewAPIUserID          string                   `json:"newapi_user_id"`
	RechargeRatio         float64                  `json:"recharge_ratio"`
	RechargeMethods       []UpstreamRechargeMethod `json:"recharge_methods"`
	RechargeFee           string                   `json:"recharge_fee"`
	Sub2APIAccessToken    string                   `json:"-"`
	Sub2APIRefreshToken   string                   `json:"-"`
	Sub2APITokenExpiresAt string                   `json:"-"`
	Ignored               bool                     `json:"ignored"`
	Status                string                   `json:"status"`
	LastSyncAt            string                   `json:"last_sync_at"`
	LastError             string                   `json:"last_error"`
	CreatedAt             string                   `json:"created_at"`
	UpdatedAt             string                   `json:"updated_at"`
}

type UpstreamChannelInput struct {
	Name              string
	Type              UpstreamChannelType
	BaseURL           string
	Username          string
	Password          string
	NewAPIAccessToken string
	NewAPIUserID      string
	RechargeRatio     float64
	RechargeMethods   []UpstreamRechargeMethod
	RechargeFee       string
	Ignored           bool
}

var (
	ErrUpstreamChannelNotFound = errors.New("上游渠道不存在")
	ErrUpstreamTaskNotFound    = errors.New("上游自动任务不存在")
)

const upstreamChannelColumns = `id, name, type, base_url, username, password,
	newapi_access_token, newapi_user_id, sub2api_access_token, sub2api_refresh_token,
	sub2api_token_expires_at, recharge_ratio, recharge_methods, recharge_fee,
	ignored, status, last_sync_at, last_error, created_at, updated_at`

type upstreamScanner interface {
	Scan(...any) error
}

func scanUpstreamChannel(row upstreamScanner) (UpstreamChannel, error) {
	var (
		channel             UpstreamChannel
		typeName            string
		ignored             int
		expiresAt           sql.NullString
		lastSyncAt          sql.NullString
		rechargeMethodsJSON string
	)
	err := row.Scan(
		&channel.ID, &channel.Name, &typeName, &channel.BaseURL, &channel.Username, &channel.Password,
		&channel.NewAPIAccessToken, &channel.NewAPIUserID, &channel.Sub2APIAccessToken, &channel.Sub2APIRefreshToken,
		&expiresAt, &channel.RechargeRatio, &rechargeMethodsJSON, &channel.RechargeFee,
		&ignored, &channel.Status, &lastSyncAt, &channel.LastError, &channel.CreatedAt, &channel.UpdatedAt,
	)
	if err != nil {
		return UpstreamChannel{}, err
	}
	channel.Type = UpstreamChannelType(typeName)
	if channel.RechargeRatio <= 0 {
		channel.RechargeRatio = 1
	}
	if rechargeMethodsJSON != "" {
		if err := json.Unmarshal([]byte(rechargeMethodsJSON), &channel.RechargeMethods); err != nil {
			return UpstreamChannel{}, fmt.Errorf("解析上游充值方式: %w", err)
		}
	}
	channel.RechargeMethods = normalizeUpstreamRechargeMethods(channel.RechargeMethods)
	channel.Ignored = ignored != 0
	if expiresAt.Valid {
		channel.Sub2APITokenExpiresAt = expiresAt.String
	}
	if lastSyncAt.Valid {
		channel.LastSyncAt = lastSyncAt.String
	}
	return channel, nil
}

func (s *Store) UpstreamChannels() ([]UpstreamChannel, error) {
	rows, err := s.db.Query(`SELECT ` + upstreamChannelColumns + ` FROM upstream_channels ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]UpstreamChannel, 0)
	for rows.Next() {
		channel, err := scanUpstreamChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, channel)
	}
	return out, rows.Err()
}

func (s *Store) UpstreamChannel(id int64) (UpstreamChannel, error) {
	if id <= 0 {
		return UpstreamChannel{}, ErrUpstreamChannelNotFound
	}
	channel, err := scanUpstreamChannel(s.db.QueryRow(`SELECT `+upstreamChannelColumns+` FROM upstream_channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return UpstreamChannel{}, ErrUpstreamChannelNotFound
	}
	return channel, err
}

func (s *Store) CreateUpstreamChannel(input UpstreamChannelInput) (UpstreamChannel, error) {
	if !input.Type.Valid() {
		return UpstreamChannel{}, errors.New("上游渠道类型无效")
	}
	now := nowString()
	status := "syncing"
	if input.Type == UpstreamChannelOther {
		status = "active"
	}
	if input.RechargeRatio <= 0 {
		input.RechargeRatio = 1
	}
	input.RechargeMethods = normalizeUpstreamRechargeMethods(input.RechargeMethods)
	rechargeMethodsJSON, err := marshalJSON(input.RechargeMethods)
	if err != nil {
		return UpstreamChannel{}, err
	}
	s.mu.Lock()
	result, err := s.db.Exec(`INSERT INTO upstream_channels (
		name, type, base_url, username, password, newapi_access_token, newapi_user_id,
		recharge_ratio, recharge_methods, recharge_fee, ignored, status, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.Name, string(input.Type), input.BaseURL, input.Username, input.Password,
		input.NewAPIAccessToken, input.NewAPIUserID, input.RechargeRatio, rechargeMethodsJSON, input.RechargeFee,
		boolInt(input.Ignored), status, now, now)
	s.mu.Unlock()
	if err != nil {
		return UpstreamChannel{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return UpstreamChannel{}, err
	}
	return s.UpstreamChannel(id)
}

// UpdateUpstreamChannel 有意不修改 Type：跨协议编辑会留下不兼容的缓存和会话。
func (s *Store) UpdateUpstreamChannel(id int64, input UpstreamChannelInput) (UpstreamChannel, error) {
	now := nowString()
	if input.RechargeRatio <= 0 {
		input.RechargeRatio = 1
	}
	input.RechargeMethods = normalizeUpstreamRechargeMethods(input.RechargeMethods)
	rechargeMethodsJSON, err := marshalJSON(input.RechargeMethods)
	if err != nil {
		return UpstreamChannel{}, err
	}
	s.mu.Lock()
	result, err := s.db.Exec(`UPDATE upstream_channels SET
		name = ?, base_url = ?, username = ?, password = ?, newapi_access_token = ?,
		newapi_user_id = ?, recharge_ratio = ?, recharge_methods = ?, recharge_fee = ?,
		ignored = ?, updated_at = ? WHERE id = ?`,
		input.Name, input.BaseURL, input.Username, input.Password, input.NewAPIAccessToken,
		input.NewAPIUserID, input.RechargeRatio, rechargeMethodsJSON, input.RechargeFee,
		boolInt(input.Ignored), now, id)
	s.mu.Unlock()
	if err != nil {
		return UpstreamChannel{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return UpstreamChannel{}, ErrUpstreamChannelNotFound
	}
	return s.UpstreamChannel(id)
}

func normalizeUpstreamRechargeMethods(methods []UpstreamRechargeMethod) []UpstreamRechargeMethod {
	out := make([]UpstreamRechargeMethod, 0, len(methods))
	seen := make(map[UpstreamRechargeMethod]struct{}, len(methods))
	for _, method := range methods {
		method = UpstreamRechargeMethod(strings.ToLower(strings.TrimSpace(string(method))))
		if !method.Valid() {
			continue
		}
		if _, exists := seen[method]; exists {
			continue
		}
		seen[method] = struct{}{}
		out = append(out, method)
	}
	return out
}

func (s *Store) DeleteUpstreamChannel(id int64) error {
	s.mu.Lock()
	result, err := s.db.Exec(`DELETE FROM upstream_channels WHERE id = ?`, id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrUpstreamChannelNotFound
	}
	return nil
}

func (s *Store) SetUpstreamChannelStatus(id int64, status, message string) error {
	s.mu.Lock()
	result, err := s.db.Exec(`UPDATE upstream_channels SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, truncate(message, 2000), nowString(), id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrUpstreamChannelNotFound
	}
	return nil
}

func (s *Store) MarkUpstreamChannelSynced(id int64) error {
	now := nowString()
	s.mu.Lock()
	result, err := s.db.Exec(`UPDATE upstream_channels SET status = 'active', last_sync_at = ?, last_error = '', updated_at = ? WHERE id = ?`, now, now, id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrUpstreamChannelNotFound
	}
	return nil
}

func (s *Store) SaveUpstreamChannelSession(id int64, accessToken, refreshToken, expiresAt string) error {
	s.mu.Lock()
	result, err := s.db.Exec(`UPDATE upstream_channels SET sub2api_access_token = ?, sub2api_refresh_token = ?,
		sub2api_token_expires_at = ?, updated_at = ? WHERE id = ?`,
		accessToken, refreshToken, nullableString(expiresAt), nowString(), id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrUpstreamChannelNotFound
	}
	return nil
}

type UpstreamCacheEntry struct {
	Exists   bool
	Raw      any
	Value    any
	SyncedAt string
}

func (s *Store) UpstreamCache(channelID int64, key string) (UpstreamCacheEntry, error) {
	var raw, normalized, syncedAt string
	err := s.db.QueryRow(`SELECT raw_json, normalized_json, synced_at FROM upstream_channel_cache
		WHERE channel_id = ? AND cache_key = ?`, channelID, key).Scan(&raw, &normalized, &syncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UpstreamCacheEntry{}, nil
	}
	if err != nil {
		return UpstreamCacheEntry{}, err
	}
	entry := UpstreamCacheEntry{Exists: true, SyncedAt: syncedAt}
	if err := json.Unmarshal([]byte(raw), &entry.Raw); err != nil {
		return UpstreamCacheEntry{}, fmt.Errorf("解析上游缓存 %s: %w", key, err)
	}
	if err := json.Unmarshal([]byte(normalized), &entry.Value); err != nil {
		return UpstreamCacheEntry{}, fmt.Errorf("解析上游缓存 %s: %w", key, err)
	}
	return entry, nil
}

func (s *Store) SaveUpstreamCache(channelID int64, key string, raw, normalized any) error {
	rawJSON, err := marshalJSON(raw)
	if err != nil {
		return err
	}
	normalizedJSON, err := marshalJSON(normalized)
	if err != nil {
		return err
	}
	s.mu.Lock()
	_, err = s.db.Exec(`INSERT INTO upstream_channel_cache(channel_id, cache_key, raw_json, normalized_json, synced_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(channel_id, cache_key) DO UPDATE SET raw_json = excluded.raw_json,
		normalized_json = excluded.normalized_json, synced_at = excluded.synced_at`,
		channelID, key, rawJSON, normalizedJSON, nowString())
	s.mu.Unlock()
	return err
}

type UpstreamBalanceSnapshot struct {
	ID          int64    `json:"id"`
	ChannelID   int64    `json:"channel_id"`
	Balance     float64  `json:"balance"`
	UsedBalance *float64 `json:"used_balance"`
	Unit        string   `json:"unit"`
	Raw         any      `json:"raw,omitempty"`
	CapturedAt  string   `json:"captured_at"`
}

func (s *Store) AddUpstreamBalanceSnapshot(snapshot UpstreamBalanceSnapshot) (UpstreamBalanceSnapshot, error) {
	if snapshot.CapturedAt == "" {
		snapshot.CapturedAt = nowString()
	}
	raw, err := marshalJSON(snapshot.Raw)
	if err != nil {
		return UpstreamBalanceSnapshot{}, err
	}
	s.mu.Lock()
	result, err := s.db.Exec(`INSERT INTO upstream_balance_snapshots(channel_id, balance, used_balance, unit, raw_json, captured_at)
		VALUES (?, ?, ?, ?, ?, ?)`, snapshot.ChannelID, snapshot.Balance, nullableFloat(snapshot.UsedBalance), snapshot.Unit, raw, snapshot.CapturedAt)
	s.mu.Unlock()
	if err != nil {
		return UpstreamBalanceSnapshot{}, err
	}
	snapshot.ID, _ = result.LastInsertId()
	return snapshot, nil
}

func scanUpstreamBalanceSnapshot(row upstreamScanner) (UpstreamBalanceSnapshot, error) {
	var (
		snapshot UpstreamBalanceSnapshot
		used     sql.NullFloat64
		raw      string
	)
	if err := row.Scan(&snapshot.ID, &snapshot.ChannelID, &snapshot.Balance, &used, &snapshot.Unit, &raw, &snapshot.CapturedAt); err != nil {
		return UpstreamBalanceSnapshot{}, err
	}
	if used.Valid {
		value := used.Float64
		snapshot.UsedBalance = &value
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &snapshot.Raw); err != nil {
			snapshot.Raw = raw
		}
	}
	return snapshot, nil
}

func (s *Store) LatestUpstreamBalanceSnapshot(channelID int64) (*UpstreamBalanceSnapshot, error) {
	snapshot, err := scanUpstreamBalanceSnapshot(s.db.QueryRow(`SELECT id, channel_id, balance, used_balance, unit, raw_json, captured_at
		FROM upstream_balance_snapshots WHERE channel_id = ? ORDER BY captured_at DESC, id DESC LIMIT 1`, channelID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Store) UpstreamBalanceHistory(channelID int64, limit int) ([]UpstreamBalanceSnapshot, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, channel_id, balance, used_balance, unit, raw_json, captured_at
		FROM upstream_balance_snapshots WHERE channel_id = ? ORDER BY captured_at ASC, id ASC LIMIT ?`, channelID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUpstreamBalanceSnapshots(rows)
}

func (s *Store) UpstreamBalanceSnapshotsSince(channelID int64, since time.Time) ([]UpstreamBalanceSnapshot, error) {
	rows, err := s.db.Query(`SELECT id, channel_id, balance, used_balance, unit, raw_json, captured_at
		FROM upstream_balance_snapshots WHERE channel_id = ? AND captured_at >= ? ORDER BY captured_at ASC, id ASC`,
		channelID, since.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUpstreamBalanceSnapshots(rows)
}

func scanUpstreamBalanceSnapshots(rows *sql.Rows) ([]UpstreamBalanceSnapshot, error) {
	out := make([]UpstreamBalanceSnapshot, 0)
	for rows.Next() {
		snapshot, err := scanUpstreamBalanceSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot)
	}
	return out, rows.Err()
}

type UpstreamBalanceQueryLog struct {
	ID          int64    `json:"id"`
	ChannelID   int64    `json:"channel_id"`
	Status      string   `json:"status"`
	Balance     *float64 `json:"balance"`
	UsedBalance *float64 `json:"used_balance"`
	Unit        string   `json:"unit"`
	Message     string   `json:"message"`
	Error       string   `json:"error"`
	Raw         any      `json:"raw,omitempty"`
	CreatedAt   string   `json:"created_at"`
}

func (s *Store) AddUpstreamBalanceQueryLog(log UpstreamBalanceQueryLog) error {
	if log.CreatedAt == "" {
		log.CreatedAt = nowString()
	}
	raw, err := marshalJSON(log.Raw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	_, err = s.db.Exec(`INSERT INTO upstream_balance_query_logs(
		channel_id, status, balance, used_balance, unit, message, error, raw_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ChannelID, log.Status, nullableFloat(log.Balance), nullableFloat(log.UsedBalance), nullableString(log.Unit),
		truncate(log.Message, 2000), truncate(log.Error, 2000), raw, log.CreatedAt)
	s.mu.Unlock()
	return err
}

func (s *Store) UpstreamBalanceQueryLogs(channelID int64, page, pageSize int) ([]UpstreamBalanceQueryLog, int64, int, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM upstream_balance_query_logs WHERE channel_id = ?`, channelID).Scan(&total); err != nil {
		return nil, 0, 0, 0, err
	}
	pages := max(1, int((total+int64(pageSize)-1)/int64(pageSize)))
	if page > pages {
		page = pages
	}
	rows, err := s.db.Query(`SELECT id, channel_id, status, balance, used_balance, unit, message, error, raw_json, created_at
		FROM upstream_balance_query_logs WHERE channel_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		channelID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer rows.Close()
	items := make([]UpstreamBalanceQueryLog, 0)
	for rows.Next() {
		var (
			item          UpstreamBalanceQueryLog
			balance, used sql.NullFloat64
			unit, raw     string
		)
		if err := rows.Scan(&item.ID, &item.ChannelID, &item.Status, &balance, &used, &unit, &item.Message, &item.Error, &raw, &item.CreatedAt); err != nil {
			return nil, 0, 0, 0, err
		}
		if balance.Valid {
			value := balance.Float64
			item.Balance = &value
		}
		if used.Valid {
			value := used.Float64
			item.UsedBalance = &value
		}
		item.Unit = unit
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &item.Raw); err != nil {
				item.Raw = raw
			}
		}
		items = append(items, item)
	}
	return items, total, page, pageSize, rows.Err()
}

type UpstreamTaskType string

const (
	UpstreamTaskLowBalance       UpstreamTaskType = "low_balance"
	UpstreamTaskBurnRate         UpstreamTaskType = "burn_rate"
	UpstreamTaskGroupAdded       UpstreamTaskType = "group_added"
	UpstreamTaskGroupRemoved     UpstreamTaskType = "group_removed"
	UpstreamTaskGroupRatioChange UpstreamTaskType = "group_ratio_changed"
)

func (t UpstreamTaskType) Valid() bool {
	switch t {
	case UpstreamTaskLowBalance, UpstreamTaskBurnRate, UpstreamTaskGroupAdded, UpstreamTaskGroupRemoved, UpstreamTaskGroupRatioChange:
		return true
	}
	return false
}

func (t UpstreamTaskType) IsGroupTask() bool {
	return t == UpstreamTaskGroupAdded || t == UpstreamTaskGroupRemoved || t == UpstreamTaskGroupRatioChange
}

type UpstreamAutomationTask struct {
	ID              int64            `json:"id"`
	ChannelID       int64            `json:"channel_id"`
	Type            UpstreamTaskType `json:"type"`
	Enabled         bool             `json:"enabled"`
	IntervalMinutes int              `json:"interval_minutes"`
	Threshold       float64          `json:"threshold"`
	LookbackMinutes int              `json:"lookback_minutes"`
	CooldownMinutes int              `json:"cooldown_minutes"`
	Recipients      []string         `json:"recipients"`
	LastRunAt       string           `json:"last_run_at"`
	LastAlertAt     string           `json:"last_alert_at"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
}

func scanUpstreamTask(row upstreamScanner) (UpstreamAutomationTask, error) {
	var (
		task       UpstreamAutomationTask
		typeName   string
		enabled    int
		recipients string
		lastRun    sql.NullString
		lastAlert  sql.NullString
	)
	if err := row.Scan(&task.ID, &task.ChannelID, &typeName, &enabled, &task.IntervalMinutes,
		&task.Threshold, &task.LookbackMinutes, &task.CooldownMinutes, &recipients,
		&lastRun, &lastAlert, &task.CreatedAt, &task.UpdatedAt); err != nil {
		return UpstreamAutomationTask{}, err
	}
	task.Type = UpstreamTaskType(typeName)
	task.Enabled = enabled != 0
	if lastRun.Valid {
		task.LastRunAt = lastRun.String
	}
	if lastAlert.Valid {
		task.LastAlertAt = lastAlert.String
	}
	_ = json.Unmarshal([]byte(recipients), &task.Recipients)
	if task.Recipients == nil {
		task.Recipients = []string{}
	}
	return task, nil
}

const upstreamTaskColumns = `id, channel_id, type, enabled, interval_minutes, threshold, lookback_minutes,
	cooldown_minutes, recipients_json, last_run_at, last_alert_at, created_at, updated_at`

func (s *Store) UpstreamAutomationTasks(channelID int64) ([]UpstreamAutomationTask, error) {
	rows, err := s.db.Query(`SELECT `+upstreamTaskColumns+` FROM upstream_automation_tasks WHERE channel_id = ? ORDER BY id DESC`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UpstreamAutomationTask, 0)
	for rows.Next() {
		task, err := scanUpstreamTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, task)
	}
	return items, rows.Err()
}

func (s *Store) UpstreamAutomationTask(channelID, taskID int64) (UpstreamAutomationTask, error) {
	task, err := scanUpstreamTask(s.db.QueryRow(`SELECT `+upstreamTaskColumns+` FROM upstream_automation_tasks WHERE id = ? AND channel_id = ?`, taskID, channelID))
	if errors.Is(err, sql.ErrNoRows) {
		return UpstreamAutomationTask{}, ErrUpstreamTaskNotFound
	}
	return task, err
}

func (s *Store) CreateUpstreamAutomationTask(task UpstreamAutomationTask) (UpstreamAutomationTask, error) {
	if !task.Type.Valid() {
		return UpstreamAutomationTask{}, errors.New("上游自动任务类型无效")
	}
	if task.IntervalMinutes <= 0 || task.LookbackMinutes <= 0 || task.CooldownMinutes < 0 {
		return UpstreamAutomationTask{}, errors.New("上游自动任务时间参数无效")
	}
	recipients, err := marshalJSON(task.Recipients)
	if err != nil {
		return UpstreamAutomationTask{}, err
	}
	now := nowString()
	s.mu.Lock()
	result, err := s.db.Exec(`INSERT INTO upstream_automation_tasks(
		channel_id, type, enabled, interval_minutes, threshold, lookback_minutes, cooldown_minutes,
		recipients_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ChannelID, string(task.Type), boolInt(task.Enabled), task.IntervalMinutes, task.Threshold,
		task.LookbackMinutes, task.CooldownMinutes, recipients, now, now)
	s.mu.Unlock()
	if err != nil {
		return UpstreamAutomationTask{}, err
	}
	id, _ := result.LastInsertId()
	return s.UpstreamAutomationTask(task.ChannelID, id)
}

func (s *Store) UpdateUpstreamAutomationTask(task UpstreamAutomationTask) (UpstreamAutomationTask, error) {
	if !task.Type.Valid() || task.IntervalMinutes <= 0 || task.LookbackMinutes <= 0 || task.CooldownMinutes < 0 {
		return UpstreamAutomationTask{}, errors.New("上游自动任务参数无效")
	}
	recipients, err := marshalJSON(task.Recipients)
	if err != nil {
		return UpstreamAutomationTask{}, err
	}
	s.mu.Lock()
	result, err := s.db.Exec(`UPDATE upstream_automation_tasks SET type = ?, enabled = ?, interval_minutes = ?, threshold = ?,
		lookback_minutes = ?, cooldown_minutes = ?, recipients_json = ?, updated_at = ? WHERE id = ? AND channel_id = ?`,
		string(task.Type), boolInt(task.Enabled), task.IntervalMinutes, task.Threshold, task.LookbackMinutes,
		task.CooldownMinutes, recipients, nowString(), task.ID, task.ChannelID)
	s.mu.Unlock()
	if err != nil {
		return UpstreamAutomationTask{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return UpstreamAutomationTask{}, ErrUpstreamTaskNotFound
	}
	return s.UpstreamAutomationTask(task.ChannelID, task.ID)
}

func (s *Store) DeleteUpstreamAutomationTask(channelID, taskID int64) error {
	s.mu.Lock()
	result, err := s.db.Exec(`DELETE FROM upstream_automation_tasks WHERE id = ? AND channel_id = ?`, taskID, channelID)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrUpstreamTaskNotFound
	}
	return nil
}

func (s *Store) MarkUpstreamTaskRun(taskID int64, at time.Time) error {
	s.mu.Lock()
	result, err := s.db.Exec(`UPDATE upstream_automation_tasks SET last_run_at = ?, updated_at = ? WHERE id = ?`,
		at.Format(time.RFC3339Nano), nowString(), taskID)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrUpstreamTaskNotFound
	}
	return nil
}

func (s *Store) MarkUpstreamTaskAlert(taskID int64, at time.Time) error {
	s.mu.Lock()
	result, err := s.db.Exec(`UPDATE upstream_automation_tasks SET last_alert_at = ?, updated_at = ? WHERE id = ?`,
		at.Format(time.RFC3339Nano), nowString(), taskID)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrUpstreamTaskNotFound
	}
	return nil
}

func (s *Store) UpstreamTaskState(taskID int64, key string) (value any, exists bool, err error) {
	var raw string
	err = s.db.QueryRow(`SELECT value_json FROM upstream_automation_task_state WHERE task_id = ? AND state_key = ?`, taskID, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (s *Store) SaveUpstreamTaskState(taskID int64, key string, value any) error {
	raw, err := marshalJSON(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	_, err = s.db.Exec(`INSERT INTO upstream_automation_task_state(task_id, state_key, value_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(task_id, state_key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		taskID, key, raw, nowString())
	s.mu.Unlock()
	return err
}

type UpstreamAlertEvent struct {
	ID          int64  `json:"id"`
	ChannelID   int64  `json:"channel_id"`
	ChannelName string `json:"channel_name,omitempty"`
	TaskID      *int64 `json:"task_id,omitempty"`
	Type        string `json:"type"`
	Message     string `json:"message"`
	Snapshot    any    `json:"snapshot,omitempty"`
	EmailSent   bool   `json:"email_sent"`
	EmailError  string `json:"email_error"`
	WeComSent   bool   `json:"wecom_sent"`
	WeComError  string `json:"wecom_error"`
	CreatedAt   string `json:"created_at"`
}

func (s *Store) AddUpstreamAlertEvent(event UpstreamAlertEvent) error {
	if event.CreatedAt == "" {
		event.CreatedAt = nowString()
	}
	raw, err := marshalJSON(event.Snapshot)
	if err != nil {
		return err
	}
	s.mu.Lock()
	_, err = s.db.Exec(`INSERT INTO upstream_alert_events(channel_id, task_id, type, message, snapshot_json, email_sent, email_error, wecom_sent, wecom_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ChannelID, nullableID(event.TaskID), event.Type,
		truncate(event.Message, 4000), raw, boolInt(event.EmailSent), truncate(event.EmailError, 2000),
		boolInt(event.WeComSent), truncate(event.WeComError, 2000), event.CreatedAt)
	s.mu.Unlock()
	return err
}

const upstreamAlertSelect = `SELECT a.id, a.channel_id, c.name, a.task_id, a.type, a.message,
	a.snapshot_json, a.email_sent, a.email_error, a.wecom_sent, a.wecom_error, a.created_at
	FROM upstream_alert_events a JOIN upstream_channels c ON c.id = a.channel_id`

func (s *Store) UpstreamAlertEvents(channelID int64, limit int) ([]UpstreamAlertEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args := []any{limit}
	query := upstreamAlertSelect
	if channelID > 0 {
		query += ` WHERE a.channel_id = ?`
		args = []any{channelID, limit}
	}
	query += ` ORDER BY a.created_at DESC, a.id DESC LIMIT ?`
	return s.queryUpstreamAlertEvents(query, args...)
}

func (s *Store) UpstreamAlertEventsSince(channelID int64, eventType string, since time.Time) ([]UpstreamAlertEvent, error) {
	query := upstreamAlertSelect + ` WHERE a.channel_id = ? AND a.type = ?
		AND julianday(a.created_at) >= julianday(?) ORDER BY julianday(a.created_at) DESC, a.id DESC`
	return s.queryUpstreamAlertEvents(query, channelID, eventType, since.Format(time.RFC3339Nano))
}

func (s *Store) queryUpstreamAlertEvents(query string, args ...any) ([]UpstreamAlertEvent, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UpstreamAlertEvent, 0)
	for rows.Next() {
		var taskID sql.NullInt64
		var sent, wecomSent int
		var snapshot string
		var item UpstreamAlertEvent
		if err := rows.Scan(&item.ID, &item.ChannelID, &item.ChannelName, &taskID, &item.Type, &item.Message,
			&snapshot, &sent, &item.EmailError, &wecomSent, &item.WeComError, &item.CreatedAt); err != nil {
			return nil, err
		}
		if taskID.Valid {
			value := taskID.Int64
			item.TaskID = &value
		}
		item.EmailSent = sent != 0
		item.WeComSent = wecomSent != 0
		if snapshot != "" {
			if err := json.Unmarshal([]byte(snapshot), &item.Snapshot); err != nil {
				item.Snapshot = snapshot
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CleanupUpstreamHistory 仅清理可重建的上游历史，不触碰 Guardian 本身的事件与样本。
func (s *Store) CleanupUpstreamHistory(before time.Time) error {
	cutoff := before.Format(time.RFC3339Nano)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, table := range []string{"upstream_balance_snapshots", "upstream_balance_query_logs", "upstream_alert_events", "scheduled_report_runs"} {
		column := "captured_at"
		if table == "upstream_balance_query_logs" || table == "upstream_alert_events" {
			column = "created_at"
		}
		if table == "scheduled_report_runs" {
			column = "started_at"
		}
		if _, err := s.db.Exec(`DELETE FROM `+table+` WHERE `+column+` < ?`, cutoff); err != nil {
			return err
		}
	}
	return nil
}

const metaUpstreamEmailSettings = "upstream_email_settings"

const metaUpstreamWeComSettings = "upstream_wecom_settings"

type UpstreamEmailSettings struct {
	SMTPHost               string   `json:"smtp_host"`
	SMTPPort               int      `json:"smtp_port"`
	SMTPSecure             bool     `json:"smtp_secure"`
	SMTPUser               string   `json:"smtp_user"`
	SMTPPassword           string   `json:"-"`
	SMTPFrom               string   `json:"smtp_from"`
	SubjectPrefix          string   `json:"subject_prefix"`
	DefaultRecipients      []string `json:"default_recipients"`
	DefaultIntervalMinutes int      `json:"default_interval_minutes"`
	HasSMTPPassword        bool     `json:"has_smtp_password"`
}

type upstreamEmailSettingsRecord struct {
	SMTPHost               string   `json:"smtp_host"`
	SMTPPort               int      `json:"smtp_port"`
	SMTPSecure             bool     `json:"smtp_secure"`
	SMTPUser               string   `json:"smtp_user"`
	SMTPPassword           string   `json:"smtp_password"`
	SMTPFrom               string   `json:"smtp_from"`
	SubjectPrefix          string   `json:"subject_prefix"`
	DefaultRecipients      []string `json:"default_recipients"`
	DefaultIntervalMinutes int      `json:"default_interval_minutes"`
}

func DefaultUpstreamEmailSettings() UpstreamEmailSettings {
	return UpstreamEmailSettings{SMTPPort: 587, DefaultRecipients: []string{}, DefaultIntervalMinutes: 30}
}

func normalizeUpstreamEmailSettings(settings *UpstreamEmailSettings) {
	settings.SMTPHost = strings.TrimSpace(settings.SMTPHost)
	settings.SMTPUser = strings.TrimSpace(settings.SMTPUser)
	settings.SMTPFrom = strings.TrimSpace(settings.SMTPFrom)
	settings.SubjectPrefix = strings.TrimSpace(settings.SubjectPrefix)
	if settings.SMTPPort <= 0 {
		settings.SMTPPort = 587
	}
	if settings.DefaultIntervalMinutes <= 0 {
		settings.DefaultIntervalMinutes = 30
	}
	if settings.DefaultRecipients == nil {
		settings.DefaultRecipients = []string{}
	}
	settings.HasSMTPPassword = settings.SMTPPassword != ""
}

func (s *Store) UpstreamEmailSettings() (UpstreamEmailSettings, error) {
	settings := DefaultUpstreamEmailSettings()
	var record upstreamEmailSettingsRecord
	if err := s.getJSON(metaUpstreamEmailSettings, &record); err != nil {
		if IsNotFound(err) {
			return settings, nil
		}
		return UpstreamEmailSettings{}, err
	}
	settings = UpstreamEmailSettings{
		SMTPHost: record.SMTPHost, SMTPPort: record.SMTPPort, SMTPSecure: record.SMTPSecure,
		SMTPUser: record.SMTPUser, SMTPPassword: record.SMTPPassword, SMTPFrom: record.SMTPFrom,
		SubjectPrefix: record.SubjectPrefix, DefaultRecipients: record.DefaultRecipients,
		DefaultIntervalMinutes: record.DefaultIntervalMinutes,
	}
	normalizeUpstreamEmailSettings(&settings)
	return settings, nil
}

func (s *Store) SaveUpstreamEmailSettings(settings UpstreamEmailSettings) (UpstreamEmailSettings, error) {
	normalizeUpstreamEmailSettings(&settings)
	record := upstreamEmailSettingsRecord{
		SMTPHost: settings.SMTPHost, SMTPPort: settings.SMTPPort, SMTPSecure: settings.SMTPSecure,
		SMTPUser: settings.SMTPUser, SMTPPassword: settings.SMTPPassword, SMTPFrom: settings.SMTPFrom,
		SubjectPrefix: settings.SubjectPrefix, DefaultRecipients: settings.DefaultRecipients,
		DefaultIntervalMinutes: settings.DefaultIntervalMinutes,
	}
	s.mu.Lock()
	err := s.setJSON(metaUpstreamEmailSettings, record)
	s.mu.Unlock()
	if err != nil {
		return UpstreamEmailSettings{}, err
	}
	return settings, nil
}

// UpstreamWeComSettings 保存直接调用企业微信应用 API 所需的配置。
// Secret 按当前面板要求随配置响应返回；数据库文件本身仍由 Store 以私有权限保护。
type UpstreamWeComSettings struct {
	CorpID    string `json:"corp_id"`
	AgentID   int64  `json:"agent_id"`
	Secret    string `json:"secret"`
	Target    string `json:"target"`
	HasSecret bool   `json:"has_secret"`
}

type upstreamWeComSettingsRecord struct {
	CorpID  string `json:"corp_id"`
	AgentID int64  `json:"agent_id"`
	Secret  string `json:"secret"`
	Target  string `json:"target"`
}

func DefaultUpstreamWeComSettings() UpstreamWeComSettings {
	return UpstreamWeComSettings{}
}

func normalizeUpstreamWeComSettings(settings *UpstreamWeComSettings) {
	settings.CorpID = strings.TrimSpace(settings.CorpID)
	settings.Secret = strings.TrimSpace(settings.Secret)
	settings.Target = strings.TrimSpace(settings.Target)
	settings.HasSecret = settings.Secret != ""
}

func (s *Store) UpstreamWeComSettings() (UpstreamWeComSettings, error) {
	settings := DefaultUpstreamWeComSettings()
	var record upstreamWeComSettingsRecord
	if err := s.getJSON(metaUpstreamWeComSettings, &record); err != nil {
		if IsNotFound(err) {
			return settings, nil
		}
		return UpstreamWeComSettings{}, err
	}
	settings = UpstreamWeComSettings{
		CorpID:  record.CorpID,
		AgentID: record.AgentID,
		Secret:  record.Secret,
		Target:  record.Target,
	}
	normalizeUpstreamWeComSettings(&settings)
	return settings, nil
}

func (s *Store) SaveUpstreamWeComSettings(settings UpstreamWeComSettings) (UpstreamWeComSettings, error) {
	normalizeUpstreamWeComSettings(&settings)
	record := upstreamWeComSettingsRecord{
		CorpID:  settings.CorpID,
		AgentID: settings.AgentID,
		Secret:  settings.Secret,
		Target:  settings.Target,
	}
	s.mu.Lock()
	err := s.setJSON(metaUpstreamWeComSettings, record)
	s.mu.Unlock()
	if err != nil {
		return UpstreamWeComSettings{}, err
	}
	return settings, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func marshalJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
