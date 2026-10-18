package store

import (
	"encoding/json"
	"time"
)

type ModelCheckTarget struct {
	AccountID     int64  `json:"account_id"`
	GroupID       int64  `json:"group_id"`
	Model         string `json:"model"`
	ExpectedModel string `json:"expected_model"`
}

type ModelCheckSettings struct {
	Enabled         bool               `json:"enabled"`
	IntervalMinutes int                `json:"interval_minutes"`
	QuietEnabled    bool               `json:"quiet_enabled"`
	QuietStart      string             `json:"quiet_start"`
	QuietEnd        string             `json:"quiet_end"`
	Timezone        string             `json:"timezone"`
	Targets         []ModelCheckTarget `json:"targets"`
}

type ModelCheckCandidate struct {
	Model       string  `json:"model"`
	Probability float64 `json:"probability"`
}

type ModelCheckRun struct {
	ID int64 `json:"id"`
	ModelCheckTarget
	AccountName  string                `json:"account_name"`
	StartedAt    time.Time             `json:"started_at"`
	FinishedAt   *time.Time            `json:"finished_at,omitempty"`
	Status       string                `json:"status"`
	Message      string                `json:"message"`
	Prediction   string                `json:"prediction"`
	Probability  float64               `json:"probability"`
	ValidOutputs int                   `json:"valid_outputs"`
	NumberCounts []int                 `json:"number_counts"`
	Candidates   []ModelCheckCandidate `json:"candidates"`
	BankVersion  string                `json:"bank_version"`
}

type ModelCheckTimeline struct {
	AccountID int64           `json:"account_id"`
	Model     string          `json:"model"`
	Runs      []ModelCheckRun `json:"runs"`
}

// ModelCheckTimelines keeps a busy model from crowding other models out of the overview.
func (s *Store) ModelCheckTimelines() ([]ModelCheckTimeline, error) {
	rows, err := s.db.Query(`SELECT DISTINCT account_id, model FROM model_check_runs ORDER BY account_id, model`)
	if err != nil {
		return nil, err
	}
	items := []ModelCheckTimeline{}
	for rows.Next() {
		var item ModelCheckTimeline
		if err := rows.Scan(&item.AccountID, &item.Model); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close() // The store has one connection; release it before reading each timeline.
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Runs, err = s.ModelCheckRuns(items[i].AccountID, items[i].Model, 0, 100)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Store) ModelCheckSettings() (ModelCheckSettings, error) {
	v := ModelCheckSettings{IntervalMinutes: 60, QuietEnabled: true, QuietStart: "23:00", QuietEnd: "08:00", Timezone: "Asia/Shanghai", Targets: []ModelCheckTarget{}}
	err := s.getJSON("model_check_settings", &v)
	if IsNotFound(err) {
		err = nil
	}
	if v.Targets == nil {
		v.Targets = []ModelCheckTarget{}
	}
	return v, err
}

func (s *Store) SaveModelCheckSettings(v ModelCheckSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setJSON("model_check_settings", v)
}

func (s *Store) CreateModelCheckRun(v ModelCheckRun) (ModelCheckRun, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	result, err := s.db.Exec(`INSERT INTO model_check_runs(account_id, model, status, json) VALUES(?, ?, ?, ?)`, v.AccountID, v.Model, v.Status, string(raw))
	if err != nil {
		return v, err
	}
	v.ID, err = result.LastInsertId()
	return v, err
}

func (s *Store) FinishModelCheckRun(v ModelCheckRun) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE model_check_runs SET status = ?, json = ? WHERE id = ?`, v.Status, string(raw), v.ID)
	return err
}

func (s *Store) InterruptModelCheckRuns() error {
	_, err := s.db.Exec(`UPDATE model_check_runs SET status = 'interrupted', json = json_set(json, '$.status', 'interrupted', '$.message', '服务重启，检测未完成') WHERE status = 'running'`)
	return err
}

func (s *Store) ModelCheckRuns(accountID int64, model string, before int64, limit int) ([]ModelCheckRun, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	query := `SELECT id, json FROM model_check_runs WHERE 1=1`
	args := []any{}
	if accountID != 0 {
		query += ` AND account_id = ?`
		args = append(args, accountID)
	}
	if model != "" {
		query += ` AND model = ?`
		args = append(args, model)
	}
	if before != 0 {
		query += ` AND id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModelCheckRun{}
	for rows.Next() {
		var v ModelCheckRun
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		v.ID = id
		items = append(items, v)
	}
	return items, rows.Err()
}
