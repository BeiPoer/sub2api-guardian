package store

import (
	"encoding/json"
	"time"

	"sub2api-guardian/backend/internal/domain"
)

// SaveChannelState 写入渠道状态。
//
// health 与 health_score 冗余成列，便于后续按状态直接过滤统计。
func (s *Store) SaveChannelState(state domain.ChannelState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var groupID any
	if state.GroupID != nil {
		groupID = *state.GroupID
	}
	_, err = s.db.Exec(`INSERT INTO channel_states(account_id, group_id, health, health_score, json, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			group_id = excluded.group_id,
			health = excluded.health,
			health_score = excluded.health_score,
			json = excluded.json,
			updated_at = excluded.updated_at`,
		state.AccountID, groupID, string(state.Health), state.HealthScore,
		string(raw), state.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// ChannelState 读取单个渠道状态。
func (s *Store) ChannelState(accountID int64) (domain.ChannelState, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT json FROM channel_states WHERE account_id = ?`, accountID).Scan(&raw); err != nil {
		return domain.ChannelState{}, err
	}
	var state domain.ChannelState
	return state, json.Unmarshal([]byte(raw), &state)
}

// ChannelStates 读取全部渠道状态。
func (s *Store) ChannelStates() ([]domain.ChannelState, error) {
	rows, err := s.db.Query(`SELECT json FROM channel_states ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.ChannelState
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var state domain.ChannelState
		if err := json.Unmarshal([]byte(raw), &state); err == nil {
			out = append(out, state)
		}
	}
	return out, rows.Err()
}

// ChannelStateMap 读取全部渠道状态并按账号 ID 索引。
func (s *Store) ChannelStateMap() (map[int64]domain.ChannelState, error) {
	states, err := s.ChannelStates()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]domain.ChannelState, len(states))
	for _, state := range states {
		out[state.AccountID] = state
	}
	return out, nil
}

// DeleteChannelStates 删除不在给定集合中的渠道状态（账号已从 sub2api 移除）。
func (s *Store) DeleteChannelStates(keepAccountIDs map[int64]struct{}) error {
	states, err := s.ChannelStates()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, state := range states {
		if _, ok := keepAccountIDs[state.AccountID]; ok {
			continue
		}
		if _, err := s.db.Exec(`DELETE FROM channel_states WHERE account_id = ?`, state.AccountID); err != nil {
			return err
		}
	}
	return nil
}

// SaveGroupState 写入分组状态。
func (s *Store) SaveGroupState(state domain.GroupState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO group_states(group_id, status, json, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(group_id) DO UPDATE SET
			status = excluded.status,
			json = excluded.json,
			updated_at = excluded.updated_at`,
		state.GroupID, string(state.Status), string(raw), state.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// GroupState 读取单个分组状态。
func (s *Store) GroupState(groupID int64) (domain.GroupState, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT json FROM group_states WHERE group_id = ?`, groupID).Scan(&raw); err != nil {
		return domain.GroupState{}, err
	}
	var state domain.GroupState
	return state, json.Unmarshal([]byte(raw), &state)
}

// GroupStates 读取全部分组状态。
func (s *Store) GroupStates() ([]domain.GroupState, error) {
	rows, err := s.db.Query(`SELECT json FROM group_states ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.GroupState
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var state domain.GroupState
		if err := json.Unmarshal([]byte(raw), &state); err == nil {
			out = append(out, state)
		}
	}
	return out, rows.Err()
}
