package store

import (
	"encoding/json"
	"sort"

	"sub2api-guardian/backend/internal/domain"
)

// ReplaceGroups 用最新一次同步结果整体替换分组缓存。
func (s *Store) ReplaceGroups(groups []domain.Group) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM groups_cache`); err != nil {
		return err
	}
	stamp := nowString()
	for _, group := range groups {
		raw, err := json.Marshal(group)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO groups_cache(id, json, updated_at) VALUES(?, ?, ?)`,
			group.ID, string(raw), stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Groups 返回分组缓存，按 sort_order 再按 id 排序。
func (s *Store) Groups() ([]domain.Group, error) {
	rows, err := s.db.Query(`SELECT json FROM groups_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Group
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var group domain.Group
		if err := json.Unmarshal([]byte(raw), &group); err == nil {
			out = append(out, group)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ReplaceAccounts 用最新一次同步结果整体替换账号缓存。
func (s *Store) ReplaceAccounts(accounts []domain.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM accounts_cache`); err != nil {
		return err
	}
	stamp := nowString()
	for _, account := range accounts {
		raw, err := json.Marshal(account)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO accounts_cache(id, json, updated_at) VALUES(?, ?, ?)`,
			account.ID, string(raw), stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Accounts 返回账号缓存，按 id 升序。
func (s *Store) Accounts() ([]domain.Account, error) {
	rows, err := s.db.Query(`SELECT json FROM accounts_cache ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Account
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var account domain.Account
		if err := json.Unmarshal([]byte(raw), &account); err == nil {
			out = append(out, account)
		}
	}
	return out, rows.Err()
}

// UpsertAccount 只更新单个账号缓存。
//
// 手动改动单个渠道后用它做定点刷新，避免为一条记录重建整张目录表。
func (s *Store) UpsertAccount(account domain.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.Marshal(account)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO accounts_cache(id, json, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET json = excluded.json, updated_at = excluded.updated_at`,
		account.ID, string(raw), nowString())
	return err
}

// Account 返回单个账号缓存。
func (s *Store) Account(id int64) (domain.Account, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT json FROM accounts_cache WHERE id = ?`, id).Scan(&raw); err != nil {
		return domain.Account{}, err
	}
	var account domain.Account
	return account, json.Unmarshal([]byte(raw), &account)
}
