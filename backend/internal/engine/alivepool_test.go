package engine

import (
	"context"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/scoring"
)

// TestDisabledAccountNeverCountsAsAlive 是可用池口径的回归。
//
// sub2api 侧 status=inactive/error 或 schedulable=false 的账号根本不接流量。
// 早期实现只在「尚无样本」的分支里检查这一点，一旦渠道采到过样本，
// 就只看健康分 —— 一个刚被人在 sub2api 后台停用、但历史分数还很高的渠道
// 会被算作「可用」。后果是保底判定以为分组还有活口，于是放心熔断真正健康的渠道，
// 整组实际断供。
func TestDisabledAccountNeverCountsAsAlive(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		schedulable bool
		sampleCount int
	}{
		{"停用且有样本", "inactive", true, 10},
		{"停用且无样本", "inactive", true, 0},
		{"错误状态且有样本", "error", true, 10},
		{"错误状态且无样本", "error", true, 0},
		{"不可调度且有样本", "active", false, 10},
		{"不可调度且无样本", "active", false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := &channel{
				account: domain.Account{
					ID:          1,
					Status:      tc.status,
					Schedulable: tc.schedulable,
				},
				// 分数很高：不能靠健康分把它救回可用池。
				score: scoring.Result{Final: 95, SampleCount: tc.sampleCount},
			}
			ch.desired.health = domain.HealthHealthy

			if got := aliveCount([]*channel{ch}, 60, time.Now()); got != 0 {
				t.Fatalf("可用数 = %d, 期望 0（sub2api 侧不接流量的渠道不算可用）", got)
			}
		})
	}
}

// TestActiveAccountWithSamplesCountsAsAlive 确认正常渠道仍然计入，
// 修复没有把可用池收得过紧。
func TestActiveAccountWithSamplesCountsAsAlive(t *testing.T) {
	ch := &channel{
		account: domain.Account{ID: 1, Status: "active", Schedulable: true},
		score:   scoring.Result{Final: 95, SampleCount: 10},
	}
	ch.desired.health = domain.HealthHealthy

	if got := aliveCount([]*channel{ch}, 60, time.Now()); got != 1 {
		t.Fatalf("可用数 = %d, 期望 1", got)
	}
}

// TestAliveCountMatchesCurrentSchedulablePool 固定首页“存活”与熔断保底共用的口径。
func TestAliveCountMatchesCurrentSchedulablePool(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)

	cases := []struct {
		name          string
		account       domain.Account
		health        domain.ChannelHealth
		score         scoring.Result
		excluded      bool
		paused        bool
		wantAvailable int
	}{
		{
			name:          "开启调度且分数达到阈值",
			account:       domain.Account{ID: 1, Status: "active", Schedulable: true},
			health:        domain.HealthHealthy,
			score:         scoring.Result{Final: 60, SampleCount: 1},
			wantAvailable: 1,
		},
		{
			name:          "尚无样本但上游可调度",
			account:       domain.Account{ID: 1, Status: "active", Schedulable: true},
			health:        domain.HealthUnknown,
			wantAvailable: 1,
		},
		{
			name:    "分数低于阈值",
			account: domain.Account{ID: 1, Status: "active", Schedulable: true},
			health:  domain.HealthHealthy,
			score:   scoring.Result{Final: 59, SampleCount: 1},
		},
		{
			name:    "已熔断",
			account: domain.Account{ID: 1, Status: "active", Schedulable: true},
			health:  domain.HealthFused,
			score:   scoring.Result{Final: 95, SampleCount: 1},
		},
		{
			name:    "未开启调度",
			account: domain.Account{ID: 1, Status: "active", Schedulable: false},
			health:  domain.HealthHealthy,
			score:   scoring.Result{Final: 95, SampleCount: 1},
		},
		{
			name:    "上游限流窗口内",
			account: domain.Account{ID: 1, Status: "active", Schedulable: true, RateLimitResetAt: &future},
			health:  domain.HealthHealthy,
			score:   scoring.Result{Final: 95, SampleCount: 1},
		},
		{
			name:     "Guardian 人工排除",
			account:  domain.Account{ID: 1, Status: "active", Schedulable: true},
			health:   domain.HealthHealthy,
			score:    scoring.Result{Final: 95, SampleCount: 1},
			excluded: true,
		},
		{
			name:    "Guardian 人工暂停",
			account: domain.Account{ID: 1, Status: "active", Schedulable: true},
			health:  domain.HealthHealthy,
			score:   scoring.Result{Final: 95, SampleCount: 1},
			paused:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := &channel{
				account:  tc.account,
				score:    tc.score,
				excluded: tc.excluded,
				paused:   tc.paused,
			}
			ch.desired.health = tc.health

			if got := aliveCount([]*channel{ch}, 60, now); got != tc.wantAvailable {
				t.Fatalf("存活数 = %d, 期望 %d", got, tc.wantAvailable)
			}
		})
	}
}

// TestDisabledChannelDoesNotAuthorizeFuse 是这条口径真正的意义所在。
//
// 场景：分组里两个渠道，一个在 sub2api 后台被停用（历史分数仍高），
// 另一个刚出致命错误。保底要求分组至少留一个能接流量的渠道 ——
// 停用的那个不算，所以出错的这个必须被强留，而不能熔断。
func TestDisabledChannelDoesNotAuthorizeFuse(t *testing.T) {
	p := policy.Default()
	p.Breaker.MinPoolSize = 1
	p.Breaker.MinPoolScore = 60

	disabled := &channel{
		account: domain.Account{ID: 1, Name: "已停用", Status: "inactive", Schedulable: true},
		score:   scoring.Result{Final: 95, SampleCount: 10},
		pol:     p,
	}
	disabled.desired.health = domain.HealthHealthy

	failing := &channel{
		account: domain.Account{ID: 2, Name: "出错渠道", Status: "active", Schedulable: true},
		score:   scoring.Result{Final: 0, SampleCount: 5, FatalOverride: true},
		pol:     p,
	}
	failing.desired.health = domain.HealthHealthy

	// 把出错的这个摘掉后，组内还剩几个可用？停用的不算，答案必须是 0。
	if got := aliveCountExcluding([]*channel{disabled, failing}, p.Breaker.MinPoolScore, 2, time.Now()); got != 0 {
		t.Fatalf("剔除出错渠道后可用数 = %d, 期望 0（停用渠道不能充当活口）", got)
	}
}

// TestDisabledAccountExcludedFromGroupAvailability 端到端确认健康矩阵口径：
// sub2api 侧停用的渠道不该被算进分组的可用数。
func TestDisabledAccountExcludedFromGroupAvailability(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 先跑一轮让两个渠道都采到样本、分数都很高。
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	if state, _ := st.GroupState(1); state.AvailableAccounts != 2 {
		t.Fatalf("前提不成立：应有 2 个可用渠道，实际 %d", state.AvailableAccounts)
	}

	// 在 sub2api 后台把 102 停用。
	fake.setStatus(102, "inactive")
	if err := eng.SyncNow(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.AvailableAccounts != 1 {
		t.Fatalf("可用渠道数 = %d, 期望 1（102 已在 sub2api 停用）", state.AvailableAccounts)
	}
}
