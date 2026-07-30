package store

import (
	"encoding/json"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

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
