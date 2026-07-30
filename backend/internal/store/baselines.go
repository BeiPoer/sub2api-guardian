package store

import (
	"encoding/json"
	"time"

	"sub2api-guardian/backend/internal/domain"
)

// SaveBaseline 写入账号基线（Guardian 接管前的原值）。
func (s *Store) SaveBaseline(base domain.Baseline) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if base.CapturedAt.IsZero() {
		base.CapturedAt = time.Now()
	}
	raw, err := json.Marshal(base)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO baselines(account_id, json, captured_at) VALUES(?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET json = excluded.json, captured_at = excluded.captured_at`,
		base.AccountID, string(raw), base.CapturedAt.Format(time.RFC3339Nano))
	return err
}

// Baseline 读取账号基线，不存在时返回 sql.ErrNoRows（用 IsNotFound 判断）。
func (s *Store) Baseline(accountID int64) (domain.Baseline, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT json FROM baselines WHERE account_id = ?`, accountID).Scan(&raw); err != nil {
		return domain.Baseline{}, err
	}
	var base domain.Baseline
	return base, json.Unmarshal([]byte(raw), &base)
}

// Baselines 读取全部基线并按账号 ID 索引。
func (s *Store) Baselines() (map[int64]domain.Baseline, error) {
	rows, err := s.db.Query(`SELECT json FROM baselines`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]domain.Baseline{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var base domain.Baseline
		if err := json.Unmarshal([]byte(raw), &base); err == nil {
			out[base.AccountID] = base
		}
	}
	return out, rows.Err()
}

// DeleteBaseline 删除账号基线（渠道已恢复原值或已从 sub2api 移除）。
func (s *Store) DeleteBaseline(accountID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM baselines WHERE account_id = ?`, accountID)
	return err
}
