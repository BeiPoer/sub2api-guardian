package store

import (
	"database/sql"
	"strings"

	"sub2api-guardian/backend/internal/domain"
)

// maxSamplesPerAccount 是每个账号保留的样本上限，超出后按时间裁剪。
const maxSamplesPerAccount = 200

// AddSample 追加一条健康样本。
//
// 带 request_id 的真实流量样本依赖唯一索引去重，重复插入会被静默忽略。
func (s *Store) AddSample(sample domain.Sample) error {
	_, err := s.AddSampleIfNew(sample)
	return err
}

// AddSampleIfNew 追加样本，并报告 INSERT OR IGNORE 是否真的插入了新行。
func (s *Store) AddSampleIfNew(sample domain.Sample) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`INSERT OR IGNORE INTO samples(
		account_id, occurred_at, source, event_type, score,
		ttfb_ms, duration_ms, status_code, model, request_model, request_id, message
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sample.AccountID, timeToDB(sample.OccurredAt), string(sample.Source), string(sample.EventType),
		sample.Score, sample.TTFBMs, sample.DurationMs, sample.StatusCode,
		sample.Model, sample.RequestModel, sample.RequestID, truncate(sample.Message, 500))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// AddSamples 批量追加样本。
func (s *Store) AddSamples(samples []domain.Sample) error {
	for _, sample := range samples {
		if err := s.AddSample(sample); err != nil {
			return err
		}
	}
	return nil
}

// RecentSamples 返回某账号最近的 limit 条样本，按时间倒序（最新在前）。
func (s *Store) RecentSamples(accountID int64, limit int) ([]domain.Sample, error) {
	if limit <= 0 || limit > maxSamplesPerAccount {
		limit = maxSamplesPerAccount
	}
	rows, err := s.db.Query(`SELECT id, account_id, occurred_at, source, event_type, score,
		ttfb_ms, duration_ms, status_code, model, request_model, request_id, message
		FROM samples WHERE account_id = ? ORDER BY occurred_at DESC, id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSamples(rows)
}

// LatestRequestID 返回某账号最近一条真实流量样本的 request_id，用于增量拉取去重。
func (s *Store) LatestRequestID(accountID int64) (string, error) {
	var requestID sql.NullString
	err := s.db.QueryRow(`SELECT request_id FROM samples
		WHERE account_id = ? AND source = ? AND request_id <> ''
		ORDER BY occurred_at DESC, id DESC LIMIT 1`, accountID, string(domain.SourceTraffic)).Scan(&requestID)
	if err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if !requestID.Valid {
		return "", nil
	}
	return requestID.String, nil
}

// PruneSamples 把每个账号的样本裁剪到上限以内，并删除已不存在账号的孤立样本。
func (s *Store) PruneSamples(keepAccountIDs []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(`DELETE FROM samples WHERE id IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (
				PARTITION BY account_id ORDER BY occurred_at DESC, id DESC
			) AS rn FROM samples
		) WHERE rn > ?
	)`, maxSamplesPerAccount); err != nil {
		return err
	}

	if len(keepAccountIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(keepAccountIDs))
	args := make([]any, len(keepAccountIDs))
	for i, id := range keepAccountIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.db.Exec(`DELETE FROM samples WHERE account_id NOT IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

func scanSamples(rows *sql.Rows) ([]domain.Sample, error) {
	var out []domain.Sample
	for rows.Next() {
		var (
			sample     domain.Sample
			occurredAt string
			source     string
			eventType  string
		)
		if err := rows.Scan(&sample.ID, &sample.AccountID, &occurredAt, &source, &eventType,
			&sample.Score, &sample.TTFBMs, &sample.DurationMs, &sample.StatusCode,
			&sample.Model, &sample.RequestModel, &sample.RequestID, &sample.Message); err != nil {
			return nil, err
		}
		sample.OccurredAt = parseTime(occurredAt)
		sample.Source = domain.SampleSource(source)
		sample.EventType = domain.EventType(eventType)
		out = append(out, sample)
	}
	return out, rows.Err()
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
