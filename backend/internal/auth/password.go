// Package auth 提供口令哈希与会话令牌生成。
//
// 只用标准库：Go 1.24 起 crypto/pbkdf2 已进标准库，
// 不必为了密码存储引入第三方依赖。
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// iterations 是 PBKDF2 的迭代次数。
	//
	// 存储格式里带着它，将来调高不会让旧口令失效。
	iterations = 200_000
	saltBytes  = 16
	keyBytes   = 32

	// MinPasswordLength 是口令最短长度。
	MinPasswordLength = 8
	// MaxPasswordLength 防止超长输入把 PBKDF2 变成拒绝服务向量。
	MaxPasswordLength = 256

	// SessionTokenBytes 是会话令牌的随机字节数。
	SessionTokenBytes = 32
)

// ErrWeakPassword 表示口令不满足强度要求。
var ErrWeakPassword = fmt.Errorf("口令长度至少 %d 位", MinPasswordLength)

// ErrPasswordTooLong 表示口令过长。
var ErrPasswordTooLong = fmt.Errorf("口令长度不能超过 %d 位", MaxPasswordLength)

// HashPassword 生成可存库的口令摘要。
//
// 格式 pbkdf2$sha256$<迭代数>$<盐 base64>$<摘要 base64>，
// 参数内联使得调整迭代数时旧记录仍可校验。
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成随机盐失败: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyBytes)
	if err != nil {
		return "", fmt.Errorf("计算口令摘要失败: %w", err)
	}
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s",
		iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword 校验口令是否匹配摘要。
//
// 用常量时间比较，避免通过响应耗时逐字节猜测摘要。
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[2])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ValidatePassword 检查口令强度。
//
// 只卡长度：强制大小写与符号组合会把用户推向 "Passw0rd!" 这类可预测口令，
// 长度是更有效的约束。
func ValidatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < MinPasswordLength {
		return ErrWeakPassword
	}
	if length > MaxPasswordLength {
		return ErrPasswordTooLong
	}
	return nil
}

// NewSessionToken 生成会话令牌明文。
func NewSessionToken() (string, error) {
	raw := make([]byte, SessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("生成会话令牌失败")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashToken 返回会话令牌的存库摘要。
//
// 库里只存摘要：数据库被读走也无法直接冒用会话。
// 令牌本身是高熵随机串，不需要 PBKDF2 那样的慢哈希。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}
