// Package store 负责 Guardian 的 SQLite 持久化：策略、缓存、样本、状态、基线、事件。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store 是 Guardian 的持久层句柄。
//
// SQLite 连接数固定为 1（写入串行化），配合互斥锁保证事务不会交叉。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 打开（或创建）数据库并执行迁移。
func Open(path string) (*Store, error) {
	// Admin API Key 必须可逆读取以调用 sub2api，因此用文件权限保护整个数据库。
	// 对已存在的库先收紧权限，避免迁移期间仍保持旧的宽松模式。
	if _, err := os.Stat(path); err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// IsNotFound 报告错误是否为“记录不存在”。
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func (s *Store) getMeta(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	return value, err
}

func (s *Store) setMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key, value, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, nowString())
	return err
}

func (s *Store) getJSON(key string, out any) error {
	raw, err := s.getMeta(key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), out)
}

func (s *Store) setJSON(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.setMeta(key, string(raw))
}

func (s *Store) tableExists(name string) (bool, error) {
	var found string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func nowString() string { return time.Now().Format(time.RFC3339Nano) }

func timeToDB(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}

func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	t, _ := time.Parse(time.RFC3339, raw)
	return t
}

func nullTime(v sql.NullString) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return parseTime(v.String)
}
