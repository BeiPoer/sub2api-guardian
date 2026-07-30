package store

import (
	"database/sql"
	"time"

	"sub2api-guardian/backend/internal/domain"
)

// AddAction 记录一次对 sub2api 的写操作（含前后值与结果），用于审计和排障。
func (s *Store) AddAction(action domain.Action) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if action.CreatedAt.IsZero() {
		action.CreatedAt = time.Now()
	}
	ok := 0
	if action.OK {
		ok = 1
	}
	_, _ = s.db.Exec(`INSERT INTO actions(account_id, kind, before_json, after_json, ok, error, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		action.AccountID, action.Kind, action.Before, action.After, ok,
		truncate(action.Error, 500), action.CreatedAt.Format(time.RFC3339Nano))
}

// Actions 返回最近的写操作记录，accountID 为 0 时返回全部账号。
func (s *Store) Actions(accountID int64, limit int) ([]domain.Action, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		rows *sql.Rows
		err  error
	)
	if accountID > 0 {
		rows, err = s.db.Query(`SELECT id, account_id, kind, before_json, after_json, ok, error, created_at
			FROM actions WHERE account_id = ? ORDER BY id DESC LIMIT ?`, accountID, limit)
	} else {
		rows, err = s.db.Query(`SELECT id, account_id, kind, before_json, after_json, ok, error, created_at
			FROM actions ORDER BY id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Action
	for rows.Next() {
		var (
			action  domain.Action
			ok      int
			created string
		)
		if err := rows.Scan(&action.ID, &action.AccountID, &action.Kind,
			&action.Before, &action.After, &ok, &action.Error, &created); err != nil {
			return nil, err
		}
		action.OK = ok == 1
		action.CreatedAt = parseTime(created)
		out = append(out, action)
	}
	return out, rows.Err()
}

// PruneActions 只保留最近 keep 条写操作记录。
func (s *Store) PruneActions(keep int) error {
	if keep <= 0 {
		keep = 2000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM actions WHERE id NOT IN (
		SELECT id FROM actions ORDER BY id DESC LIMIT ?
	)`, keep)
	return err
}
