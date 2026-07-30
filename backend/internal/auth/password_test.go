package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("生成摘要失败: %v", err)
	}
	if !VerifyPassword(hash, "correct horse battery") {
		t.Fatal("正确口令应校验通过")
	}
	if VerifyPassword(hash, "correct horse batterY") {
		t.Fatal("错误口令不该通过")
	}
}

// TestHashIsSalted 确认相同口令两次生成的摘要不同。
//
// 没有盐的话，两个用户设了同一个口令在库里会长得一模一样，
// 泄库后可以按摘要分组批量破解。
func TestHashIsSalted(t *testing.T) {
	a, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("生成摘要失败: %v", err)
	}
	b, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("生成摘要失败: %v", err)
	}
	if a == b {
		t.Fatal("相同口令两次生成的摘要不该相同（盐没生效）")
	}
	if !VerifyPassword(a, "same-password") || !VerifyPassword(b, "same-password") {
		t.Fatal("两份摘要都应能校验通过")
	}
}

// TestHashFormatCarriesParameters 确认参数内联，将来调迭代数不破坏旧记录。
func TestHashFormatCarriesParameters(t *testing.T) {
	hash, err := HashPassword("some-password")
	if err != nil {
		t.Fatalf("生成摘要失败: %v", err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 5 {
		t.Fatalf("摘要格式应为 5 段，实际 %d 段: %q", len(parts), hash)
	}
	if parts[0] != "pbkdf2" || parts[1] != "sha256" {
		t.Fatalf("摘要前缀 = %q/%q, 期望 pbkdf2/sha256", parts[0], parts[1])
	}
}

// TestVerifyRejectsMalformedHash 确认脏数据不会被当成「匹配」。
func TestVerifyRejectsMalformedHash(t *testing.T) {
	cases := map[string]string{
		"空串":        "",
		"段数不足":      "pbkdf2$sha256$1000",
		"算法不认识":     "scrypt$sha256$1000$c2FsdA$aGFzaA",
		"迭代数非法":     "pbkdf2$sha256$abc$c2FsdA$aGFzaA",
		"盐不是base64": "pbkdf2$sha256$1000$!!!$aGFzaA",
		"明文存储":      "hunter2",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			if VerifyPassword(encoded, "hunter2") {
				t.Fatalf("非法摘要 %q 不该校验通过", encoded)
			}
		})
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Fatal("过短的口令应被拒绝")
	}
	if err := ValidatePassword("12345678"); err != nil {
		t.Fatalf("8 位口令应通过: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", MaxPasswordLength+1)); err == nil {
		t.Fatal("超长口令应被拒绝（PBKDF2 会成为拒绝服务向量）")
	}
	// 中文口令按字符数而不是字节数计算。
	if err := ValidatePassword("一二三四五六七八"); err != nil {
		t.Fatalf("8 个汉字应通过: %v", err)
	}
	if err := ValidatePassword("一二三"); err == nil {
		t.Fatal("3 个汉字应被拒绝（不能按字节算成 9）")
	}
}

func TestSessionTokenIsRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		token, err := NewSessionToken()
		if err != nil {
			t.Fatalf("生成令牌失败: %v", err)
		}
		if seen[token] {
			t.Fatal("会话令牌出现重复")
		}
		seen[token] = true
		if len(token) < 40 {
			t.Fatalf("令牌 %q 太短，熵不足", token)
		}
	}
}

// TestHashTokenIsStableAndNotReversible 确认令牌摘要可复现且不等于明文。
func TestHashTokenIsStableAndNotReversible(t *testing.T) {
	token, _ := NewSessionToken()
	if HashToken(token) != HashToken(token) {
		t.Fatal("同一个令牌两次摘要应相同，否则查不到会话")
	}
	if strings.Contains(HashToken(token), token) {
		t.Fatal("摘要里不该含有明文令牌")
	}
	other, _ := NewSessionToken()
	if HashToken(token) == HashToken(other) {
		t.Fatal("不同令牌的摘要不该相同")
	}
}
