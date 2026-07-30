package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/auth"
)

func newAuthStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("生成摘要失败: %v", err)
	}
	return hash
}

func TestUserLifecycle(t *testing.T) {
	st := newAuthStore(t)

	if count, err := st.UserCount(); err != nil || count != 0 {
		t.Fatalf("初始用户数 = %d (err=%v), 期望 0", count, err)
	}

	id, err := st.CreateUser("Admin", mustHash(t, "hunter2hunter2"))
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if count, _ := st.UserCount(); count != 1 {
		t.Fatalf("用户数 = %d, 期望 1", count)
	}

	// 用户名大小写不敏感，避免 Admin 与 admin 两个账号并存。
	user, err := st.UserByName("ADMIN")
	if err != nil {
		t.Fatalf("按用户名查找失败: %v", err)
	}
	if user.ID != id {
		t.Fatalf("查到的用户 ID = %d, 期望 %d", user.ID, id)
	}
	if user.Username != "admin" {
		t.Fatalf("用户名 = %q, 期望规范化为小写", user.Username)
	}

	if _, err := st.CreateUser("admin", mustHash(t, "another-password")); err != ErrUserExists {
		t.Fatalf("重名创建应返回 ErrUserExists，实际 %v", err)
	}
}

// TestCreateFirstUserIsOnlyOnce 是初始化接口的关键约束。
//
// 没有它的话，任何人都能在系统已经初始化之后再调一次初始化接口建号，
// 等于给面板留了个不需要凭据的后门。
func TestCreateFirstUserIsOnlyOnce(t *testing.T) {
	st := newAuthStore(t)

	if _, err := st.CreateFirstUser("admin", mustHash(t, "hunter2hunter2")); err != nil {
		t.Fatalf("首次初始化应成功: %v", err)
	}
	if _, err := st.CreateFirstUser("intruder", mustHash(t, "hunter2hunter2")); err != ErrSetupDone {
		t.Fatalf("二次初始化应返回 ErrSetupDone，实际 %v", err)
	}
	if count, _ := st.UserCount(); count != 1 {
		t.Fatalf("用户数 = %d, 期望仍为 1", count)
	}
}

// TestCreateFirstUserConcurrent 确认并发调用只有一个能成功。
//
// 判定与写入若不在同一把锁里，两个同时到达的请求会双双通过
// 「还没有用户」的检查。
func TestCreateFirstUserConcurrent(t *testing.T) {
	st := newAuthStore(t)
	hash := mustHash(t, "hunter2hunter2")

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := st.CreateFirstUser("admin", hash); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if success != 1 {
		t.Fatalf("并发初始化成功了 %d 次, 期望恰好 1 次", success)
	}
	if count, _ := st.UserCount(); count != 1 {
		t.Fatalf("用户数 = %d, 期望 1", count)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	st := newAuthStore(t)
	id, _ := st.CreateUser("admin", mustHash(t, "hunter2hunter2"))

	token, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	if err := st.CreateSession(token, id, time.Now().Add(time.Hour), "curl/8"); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	user, err := st.SessionUser(token)
	if err != nil {
		t.Fatalf("按令牌查用户失败: %v", err)
	}
	if user.ID != id {
		t.Fatalf("会话对应用户 = %d, 期望 %d", user.ID, id)
	}

	if err := st.DeleteSession(token); err != nil {
		t.Fatalf("删除会话失败: %v", err)
	}
	if _, err := st.SessionUser(token); err == nil {
		t.Fatal("已注销的会话不该还能换到用户")
	}
}

// TestExpiredSessionRejected 确认过期会话等同于未登录。
func TestExpiredSessionRejected(t *testing.T) {
	st := newAuthStore(t)
	id, _ := st.CreateUser("admin", mustHash(t, "hunter2hunter2"))

	token, _ := auth.NewSessionToken()
	if err := st.CreateSession(token, id, time.Now().Add(-time.Minute), ""); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	if _, err := st.SessionUser(token); err == nil {
		t.Fatal("过期会话不该通过")
	}
}

// TestSessionTokenNotStoredInPlaintext 确认库里存的是摘要而非明文令牌。
func TestSessionTokenNotStoredInPlaintext(t *testing.T) {
	st := newAuthStore(t)
	id, _ := st.CreateUser("admin", mustHash(t, "hunter2hunter2"))

	token, _ := auth.NewSessionToken()
	if err := st.CreateSession(token, id, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	var stored string
	if err := st.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if stored == token {
		t.Fatal("库里存了明文令牌：数据库泄露即可直接冒用会话")
	}
	if stored != auth.HashToken(token) {
		t.Fatal("库里存的应是令牌的 SHA-256 摘要")
	}
}

// TestDeleteUserSessionsExcept 覆盖改口令后吊销其他会话的场景。
func TestDeleteUserSessionsExcept(t *testing.T) {
	st := newAuthStore(t)
	id, _ := st.CreateUser("admin", mustHash(t, "hunter2hunter2"))

	current, _ := auth.NewSessionToken()
	other, _ := auth.NewSessionToken()
	expiry := time.Now().Add(time.Hour)
	_ = st.CreateSession(current, id, expiry, "current")
	_ = st.CreateSession(other, id, expiry, "other")

	if err := st.DeleteUserSessionsExcept(id, current); err != nil {
		t.Fatalf("吊销其他会话失败: %v", err)
	}
	if _, err := st.SessionUser(current); err != nil {
		t.Fatalf("当前会话应保留: %v", err)
	}
	if _, err := st.SessionUser(other); err == nil {
		t.Fatal("其他会话应被吊销")
	}
}

// TestPurgeExpiredSessionsKeepsValid 确认清理只删过期的。
func TestPurgeExpiredSessionsKeepsValid(t *testing.T) {
	st := newAuthStore(t)
	id, _ := st.CreateUser("admin", mustHash(t, "hunter2hunter2"))

	valid, _ := auth.NewSessionToken()
	expired, _ := auth.NewSessionToken()
	_ = st.CreateSession(valid, id, time.Now().Add(time.Hour), "")
	_ = st.CreateSession(expired, id, time.Now().Add(-time.Hour), "")

	removed, err := st.PurgeExpiredSessions()
	if err != nil {
		t.Fatalf("清理会话失败: %v", err)
	}
	if removed != 1 {
		t.Fatalf("清理了 %d 条, 期望 1 条", removed)
	}
	if _, err := st.SessionUser(valid); err != nil {
		t.Fatalf("未过期的会话不该被清掉: %v", err)
	}
}

// TestUpdateUsernameRejectsDuplicate 确认改名不能撞车。
func TestUpdateUsernameRejectsDuplicate(t *testing.T) {
	st := newAuthStore(t)
	first, _ := st.CreateUser("admin", mustHash(t, "hunter2hunter2"))
	if _, err := st.CreateUser("ops", mustHash(t, "hunter2hunter2")); err != nil {
		t.Fatalf("创建第二个用户失败: %v", err)
	}

	if err := st.UpdateUsername(first, "ops"); err != ErrUserExists {
		t.Fatalf("改成已存在的用户名应返回 ErrUserExists，实际 %v", err)
	}
	if err := st.UpdateUsername(first, "root"); err != nil {
		t.Fatalf("改成未占用的用户名应成功: %v", err)
	}
	if _, err := st.UserByName("root"); err != nil {
		t.Fatalf("改名后应能按新名字查到: %v", err)
	}
}

// TestUpdateUserPassword 确认改密后旧口令失效。
func TestUpdateUserPassword(t *testing.T) {
	st := newAuthStore(t)
	id, _ := st.CreateUser("admin", mustHash(t, "old-password"))

	if err := st.UpdateUserPassword(id, mustHash(t, "new-password")); err != nil {
		t.Fatalf("改口令失败: %v", err)
	}
	user, err := st.User(id)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if auth.VerifyPassword(user.PasswordHash, "old-password") {
		t.Fatal("旧口令改完后不该还能通过")
	}
	if !auth.VerifyPassword(user.PasswordHash, "new-password") {
		t.Fatal("新口令应能通过")
	}
}
