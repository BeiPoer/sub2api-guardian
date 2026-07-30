package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/auth"
	"sub2api-guardian/backend/internal/domain"
)

// ErrUserExists 表示用户名已被占用。
var ErrUserExists = errors.New("用户名已存在")

// ErrSetupDone 表示已经存在用户，初始化接口不能再用。
var ErrSetupDone = errors.New("系统已完成初始化")

// UserCount 返回已创建的用户数，用于判断是否需要初始化向导。
func (s *Store) UserCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// UserByName 按用户名查用户，不存在时返回 sql.ErrNoRows。
func (s *Store) UserByName(username string) (domain.User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, created_at, updated_at
		 FROM users WHERE username = ? COLLATE NOCASE`, normalizeUsername(username)))
}

// User 按 ID 查用户。
func (s *Store) User(id int64) (domain.User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, created_at, updated_at
		 FROM users WHERE id = ?`, id))
}

// CreateUser 新建用户，返回其 ID。
func (s *Store) CreateUser(username, passwordHash string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createUserLocked(username, passwordHash)
}

// CreateFirstUser 仅在一个用户都没有时创建管理员。
//
// 判定与写入必须在同一把锁里完成：分成两次调用的话，两个并发的初始化请求
// 会双双通过「还没有用户」的检查，把初始化接口变成任何人都能建号的后门。
func (s *Store) CreateFirstUser(username, passwordHash string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return 0, err
	}
	if count > 0 {
		return 0, ErrSetupDone
	}
	return s.createUserLocked(username, passwordHash)
}

func (s *Store) createUserLocked(username, passwordHash string) (int64, error) {
	name := normalizeUsername(username)
	if name == "" {
		return 0, errors.New("用户名不能为空")
	}
	now := nowString()
	res, err := s.db.Exec(
		`INSERT INTO users(username, password_hash, created_at, updated_at) VALUES(?, ?, ?, ?)`,
		name, passwordHash, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrUserExists
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateUserPassword 修改口令。
func (s *Store) UpdateUserPassword(userID int64, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, nowString(), userID)
	return err
}

// UpdateUsername 修改用户名。
func (s *Store) UpdateUsername(userID int64, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := normalizeUsername(username)
	if name == "" {
		return errors.New("用户名不能为空")
	}
	_, err := s.db.Exec(`UPDATE users SET username = ?, updated_at = ? WHERE id = ?`,
		name, nowString(), userID)
	if err != nil && isUniqueViolation(err) {
		return ErrUserExists
	}
	return err
}

// CreateSession 记录一个会话，入参是令牌明文，库里只落摘要。
func (s *Store) CreateSession(token string, userID int64, expiresAt time.Time, userAgent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO sessions(token_hash, user_id, created_at, expires_at, user_agent)
		 VALUES(?, ?, ?, ?, ?)`,
		auth.HashToken(token), userID, nowString(),
		expiresAt.Format(time.RFC3339Nano), shorten(userAgent, 200))
	return err
}

// SessionUser 用令牌明文换取用户，令牌无效或已过期时返回 sql.ErrNoRows。
func (s *Store) SessionUser(token string) (domain.User, error) {
	var (
		userID    int64
		expiresAt string
	)
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token_hash = ?`,
		auth.HashToken(token)).Scan(&userID, &expiresAt)
	if err != nil {
		return domain.User{}, err
	}
	// 过期会话等同于不存在。顺手删掉，避免过期记录长期堆积。
	if expiry := parseTime(expiresAt); expiry.IsZero() || !time.Now().Before(expiry) {
		_ = s.DeleteSession(token)
		return domain.User{}, sql.ErrNoRows
	}
	return s.User(userID)
}

// TouchSession 续期会话，让持续使用的会话不会在中途被踢。
func (s *Store) TouchSession(token string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
		expiresAt.Format(time.RFC3339Nano), auth.HashToken(token))
	return err
}

// DeleteSession 注销单个会话。
func (s *Store) DeleteSession(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, auth.HashToken(token))
	return err
}

// DeleteUserSessionsExcept 吊销某用户除当前会话外的全部会话。
//
// 改口令后调用：口令可能已经泄露，其他地方的登录态必须一并作废。
func (s *Store) DeleteUserSessionsExcept(userID int64, keepToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`,
		userID, auth.HashToken(keepToken))
	return err
}

// PurgeExpiredSessions 清理过期会话，返回删除条数。
func (s *Store) PurgeExpiredSessions() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`,
		time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) scanUser(row *sql.Row) (domain.User, error) {
	var (
		user               domain.User
		createdAt, updated string
	)
	if err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &createdAt, &updated); err != nil {
		return domain.User{}, err
	}
	user.CreatedAt = parseTime(createdAt)
	user.UpdatedAt = parseTime(updated)
	return user, nil
}

// normalizeUsername 统一用户名：去空白并转小写。
//
// 大小写不敏感能避免 "Admin" 与 "admin" 两个账号并存造成的混淆。
func normalizeUsername(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "constraint failed")
}

func shorten(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
