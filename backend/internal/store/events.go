package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/domain"
)

// EventFilter 是事件列表的查询条件。
type EventFilter struct {
	Level     string
	Action    string
	AccountID *int64
	GroupID   *int64
	Page      int
	PageSize  int
}

// AddEvent 追加一条事件。写事件不应影响主流程，因此错误被吞掉。
func (s *Store) AddEvent(event domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	if event.Level == "" {
		event.Level = "info"
	}
	_, _ = s.db.Exec(`INSERT INTO events(level, action, account_id, group_id, message, detail, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		event.Level, event.Action, nullableID(event.AccountID), nullableID(event.GroupID),
		truncate(event.Message, 1000), truncate(event.Detail, 4000), event.CreatedAt.Format(time.RFC3339Nano))
}

// Log 是 AddEvent 的便捷包装，detail 会被序列化为 JSON。
func (s *Store) Log(level, action string, accountID, groupID *int64, message string, detail any) {
	s.AddEvent(domain.Event{
		Level:     level,
		Action:    action,
		AccountID: accountID,
		GroupID:   groupID,
		Message:   message,
		Detail:    encodeDetail(detail),
	})
}

// Events 按条件分页返回事件（最新在前）以及总条数。
func (s *Store) Events(filter EventFilter) ([]domain.Event, int64, error) {
	where := []string{"1 = 1"}
	args := []any{}

	if level := strings.TrimSpace(filter.Level); level != "" && level != "all" {
		where = append(where, "level = ?")
		args = append(args, level)
	}
	if action := strings.TrimSpace(filter.Action); action != "" {
		where = append(where, "action = ?")
		args = append(args, action)
	}
	if filter.AccountID != nil {
		where = append(where, "account_id = ?")
		args = append(args, *filter.AccountID)
	}
	if filter.GroupID != nil {
		where = append(where, "group_id = ?")
		args = append(args, *filter.GroupID)
	}
	clause := strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}

	rows, err := s.db.Query(`SELECT id, level, action, account_id, group_id, message, detail, created_at
		FROM events WHERE `+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []domain.Event
	for rows.Next() {
		var (
			event     domain.Event
			accountID sql.NullInt64
			groupID   sql.NullInt64
			created   string
		)
		if err := rows.Scan(&event.ID, &event.Level, &event.Action, &accountID, &groupID,
			&event.Message, &event.Detail, &created); err != nil {
			return nil, 0, err
		}
		if accountID.Valid {
			v := accountID.Int64
			event.AccountID = &v
		}
		if groupID.Valid {
			v := groupID.Int64
			event.GroupID = &v
		}
		event.CreatedAt = parseTime(created)
		out = append(out, event)
	}
	return out, total, rows.Err()
}

// PruneEvents 只保留最近 keep 条事件。
func (s *Store) PruneEvents(keep int) error {
	if keep <= 0 {
		keep = 5000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM events WHERE id NOT IN (
		SELECT id FROM events ORDER BY id DESC LIMIT ?
	)`, keep)
	return err
}

func nullableID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func encodeDetail(detail any) string {
	switch v := detail.(type) {
	case nil:
		return ""
	case string:
		return v
	case error:
		return v.Error()
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}
