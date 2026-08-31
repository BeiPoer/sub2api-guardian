package store

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

// UpstreamMultipliers 返回各账号最近一次成功读取到的上游倍率。
func (s *Store) UpstreamMultipliers() (map[int64]domain.UpstreamMultiplierSnapshot, error) {
	raw := map[string]domain.UpstreamMultiplierSnapshot{}
	if err := s.getJSON(metaUpstreamMultipliers, &raw); err != nil {
		if IsNotFound(err) {
			return map[int64]domain.UpstreamMultiplierSnapshot{}, nil
		}
		return nil, err
	}
	out := make(map[int64]domain.UpstreamMultiplierSnapshot, len(raw))
	for key, snapshot := range raw {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 || snapshot.Value <= 0 || math.IsNaN(snapshot.Value) || math.IsInf(snapshot.Value, 0) {
			continue
		}
		out[id] = snapshot
	}
	return out, nil
}

// SaveUpstreamMultiplier 只在成功取得有限正数倍率后更新单个账号的快照。
func (s *Store) SaveUpstreamMultiplier(accountID int64, value float64, updatedAt time.Time) error {
	if accountID <= 0 || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return errors.New("上游倍率必须是有限正数")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	raw := map[string]domain.UpstreamMultiplierSnapshot{}
	if err := s.getJSON(metaUpstreamMultipliers, &raw); err != nil && !IsNotFound(err) {
		return err
	}
	raw[strconv.FormatInt(accountID, 10)] = domain.UpstreamMultiplierSnapshot{
		Value: value, UpdatedAt: updatedAt,
	}
	return s.setJSON(metaUpstreamMultipliers, raw)
}

// PruneUpstreamMultipliers 清理已从 Sub2API 删除账号留下的倍率快照。
func (s *Store) PruneUpstreamMultipliers(keep map[int64]struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := map[string]domain.UpstreamMultiplierSnapshot{}
	if err := s.getJSON(metaUpstreamMultipliers, &raw); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return err
	}
	changed := false
	for key := range raw {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			delete(raw, key)
			changed = true
			continue
		}
		if _, ok := keep[id]; !ok {
			delete(raw, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.setJSON(metaUpstreamMultipliers, raw)
}

// Connection 读取 sub2api 连接配置。
func (s *Store) Connection() (domain.Connection, error) {
	conn := domain.DefaultConnection()
	if err := s.getJSON(metaConnection, &conn); err != nil {
		if IsNotFound(err) {
			return domain.DefaultConnection(), nil
		}
		return domain.Connection{}, err
	}
	if conn.TimeoutSeconds <= 0 {
		conn.TimeoutSeconds = domain.DefaultConnection().TimeoutSeconds
	}
	return conn, nil
}

// SaveConnection 写入 sub2api 连接配置。
func (s *Store) SaveConnection(conn domain.Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if conn.TimeoutSeconds <= 0 {
		conn.TimeoutSeconds = domain.DefaultConnection().TimeoutSeconds
	}
	return s.setJSON(metaConnection, conn)
}

// Policy 读取全局策略。
func (s *Store) Policy() (policy.Policy, error) {
	p := policy.Default()
	if err := s.getJSON(metaPolicy, &p); err != nil {
		if IsNotFound(err) {
			return policy.Default(), nil
		}
		return policy.Policy{}, err
	}
	policy.Normalize(&p)
	return p, nil
}

// SavePolicy 写入全局策略（写入前会做一次规范化）。
func (s *Store) SavePolicy(p policy.Policy) (policy.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy.Normalize(&p)
	if err := s.setJSON(metaPolicy, p); err != nil {
		return policy.Policy{}, err
	}
	return p, nil
}

// MergeAccountLinkedMultipliers 原子合并渠道管理联动倍率，只修改联动 Map。
// 自动同步可能与渠道池编辑器同时保存策略，不能用一份旧策略覆盖人工字段。
func (s *Store) MergeAccountLinkedMultipliers(values map[string]float64) (bool, error) {
	if len(values) == 0 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	p := policy.Default()
	if err := s.getJSON(metaPolicy, &p); err != nil && !IsNotFound(err) {
		return false, err
	}
	policy.Normalize(&p)
	changed := false
	for key, value := range values {
		if key == "" || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		if previous, exists := p.AccountLinkedMultipliers[key]; !exists || previous != value {
			p.AccountLinkedMultipliers[key] = value
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	if err := s.setJSON(metaPolicy, p); err != nil {
		return false, err
	}
	return true, nil
}

// GroupOverrides 读取全部分组覆盖。
func (s *Store) GroupOverrides() (map[int64]*policy.GroupOverride, error) {
	rows, err := s.db.Query(`SELECT group_id, json FROM group_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]*policy.GroupOverride{}
	for rows.Next() {
		var (
			groupID int64
			raw     string
		)
		if err := rows.Scan(&groupID, &raw); err != nil {
			return nil, err
		}
		var override policy.GroupOverride
		if err := json.Unmarshal([]byte(raw), &override); err != nil {
			continue
		}
		out[groupID] = &override
	}
	return out, rows.Err()
}

// GroupOverride 读取单个分组覆盖，不存在时返回 nil。
func (s *Store) GroupOverride(groupID int64) (*policy.GroupOverride, error) {
	var raw string
	err := s.db.QueryRow(`SELECT json FROM group_overrides WHERE group_id = ?`, groupID).Scan(&raw)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var override policy.GroupOverride
	if err := json.Unmarshal([]byte(raw), &override); err != nil {
		return nil, err
	}
	return &override, nil
}

// SaveGroupOverride 写入分组覆盖。
func (s *Store) SaveGroupOverride(groupID int64, override policy.GroupOverride) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(override)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO group_overrides(group_id, json, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(group_id) DO UPDATE SET json = excluded.json, updated_at = excluded.updated_at`,
		groupID, string(raw), nowString())
	return err
}

// DeleteGroupOverride 清除分组覆盖，使其回落到全局策略。
func (s *Store) DeleteGroupOverride(groupID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM group_overrides WHERE group_id = ?`, groupID)
	return err
}
