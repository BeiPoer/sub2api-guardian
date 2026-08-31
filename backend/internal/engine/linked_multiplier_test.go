package engine

import (
	"context"
	"testing"
)

func TestLinkedMultiplierNameIsIdempotent(t *testing.T) {
	tests := []struct {
		name  string
		ratio float64
		want  string
	}{
		{name: "渠道", ratio: 0.12, want: "渠道【x0.12】"},
		{name: "渠道【x0.5】", ratio: 0.12, want: "渠道【x0.12】"},
		{name: "渠道【x0.12】", ratio: 0.12, want: "渠道【x0.12】"},
		{name: "渠道【x格式异常", ratio: 2, want: "渠道【x2】"},
		{name: "渠道【x0.5】人工备注", ratio: 0.25, want: "渠道【x0.25】"},
		{name: "渠道 - 【x0.5】", ratio: 0.25, want: "渠道 - 【x0.25】"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linkedMultiplierName(tt.name, tt.ratio); got != tt.want {
				t.Fatalf("linkedMultiplierName() = %q, 期望 %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeLinkedURLKeepsPathAndIgnoresCase(t *testing.T) {
	got, ok := normalizeLinkedURL("HTTPS://Example.COM/sub2api///")
	if !ok || got != "https://example.com/sub2api" {
		t.Fatalf("规范化 URL = %q, ok=%v", got, ok)
	}
}

func TestLinkedCredentialUsesShortProcessCache(t *testing.T) {
	eng, st, fake := setupEngine(t)
	conn, err := st.Connection()
	if err != nil {
		t.Fatal(err)
	}
	fake.setCredentials(101, map[string]any{
		"api_key":  "cached-key",
		"base_url": conn.BaseURL,
	})

	first, err := eng.linkedCredential(context.Background(), 101)
	if err != nil || first.APIKey != "cached-key" {
		t.Fatalf("首次读取凭据失败: %+v, %v", first, err)
	}
	second, err := eng.linkedCredential(context.Background(), 101)
	if err != nil || second.APIKey != "cached-key" {
		t.Fatalf("缓存读取凭据失败: %+v, %v", second, err)
	}
	if got := fake.credentialExportCount(); got != 1 {
		t.Fatalf("短缓存应避免重复导出，实际请求次数=%d", got)
	}

	// 连接配置刷新后清空缓存，确保新连接不会复用旧凭据。
	eng.Reconfigure(conn)
	if _, err := eng.linkedCredential(context.Background(), 101); err != nil {
		t.Fatalf("清理缓存后读取凭据失败: %v", err)
	}
	if got := fake.credentialExportCount(); got != 2 {
		t.Fatalf("连接配置刷新后应重新导出，实际请求次数=%d", got)
	}
}
