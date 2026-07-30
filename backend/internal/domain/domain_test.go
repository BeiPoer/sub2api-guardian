package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAccountActiveStatusContract(t *testing.T) {
	tests := []struct {
		status string
		active bool
	}{
		{status: "active", active: true},
		{status: " ACTIVE ", active: true},
		{status: "inactive", active: false},
		{status: "error", active: false},
		{status: "", active: false},
	}
	for _, tc := range tests {
		account := Account{Status: tc.status, Schedulable: true}
		if got := account.IsActive(); got != tc.active {
			t.Fatalf("status %q active = %v，期望 %v", tc.status, got, tc.active)
		}
	}
}

func TestUpstreamBlockIncludesExpiryAndQuota(t *testing.T) {
	now := time.Now()
	expiredAt := now.Add(-time.Minute).Unix()
	expired := Account{
		Status: "active", Schedulable: true,
		ExpiresAt: &expiredAt, AutoPauseOnExpired: true,
	}
	if kind, _ := expired.UpstreamBlock(now); kind != BlockExpired {
		t.Fatalf("到期账号阻塞类型 = %q，期望 %q", kind, BlockExpired)
	}

	limit, used := 100.0, 100.0
	quota := Account{
		Status: "active", Schedulable: true, Type: "apikey",
		QuotaDailyLimit: &limit, QuotaDailyUsed: &used,
	}
	if kind, _ := quota.UpstreamBlock(now); kind != BlockQuotaExceeded {
		t.Fatalf("配额耗尽阻塞类型 = %q，期望 %q", kind, BlockQuotaExceeded)
	}
}

func TestAccountExpiresAtDecodesUnixSeconds(t *testing.T) {
	var account Account
	if err := json.Unmarshal([]byte(`{"status":"active","expires_at":1785326400}`), &account); err != nil {
		t.Fatalf("解码 sub2api 账号失败: %v", err)
	}
	if account.ExpiresAt == nil || *account.ExpiresAt != 1785326400 {
		t.Fatalf("expires_at = %v，期望 Unix 秒 1785326400", account.ExpiresAt)
	}
}

func TestEffectiveLoadFactorMatchesSub2APIFallback(t *testing.T) {
	loadFactor := 7
	tests := []struct {
		name        string
		loadFactor  *int
		concurrency int
		want        int
	}{
		{name: "explicit", loadFactor: &loadFactor, concurrency: 5, want: 7},
		{name: "concurrency", concurrency: 5, want: 5},
		{name: "minimum", concurrency: 0, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			account := Account{LoadFactor: tc.loadFactor, Concurrency: tc.concurrency}
			if got := account.EffectiveLoadFactor(); got != tc.want {
				t.Fatalf("effective load factor = %d，期望 %d", got, tc.want)
			}
		})
	}
}
