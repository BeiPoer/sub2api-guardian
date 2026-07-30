package engine

import (
	"context"
	"testing"

	"sub2api-guardian/backend/internal/domain"
)

// TestHealthyButUnschedulableChannelGetsReenabled 是「测出健康却不上线」的回归。
//
// 场景：渠道在 sub2api 侧 schedulable=false（人工关过、或上游限流后被置为不可调度），
// 但 Guardian 从没熔断过它，所以 channel_states 里的健康态不是 fused。
//
// 旧实现只在 applyRecovery 里把 schedulable 写回 true，而那条路径要求
// `state.Health == fused`。于是这类渠道即使探测满分、健康分 100，也永远不会被
// 放回流量 —— 用户看到的现象就是「测试通过了但它就是不参与调度」。
func TestHealthyButManuallyUnschedulableChannelStaysClosed(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// sub2api 侧把 101 设为不可调度，Guardian 侧没有任何熔断记录。
	fake.setSchedulable(101, false)

	// 先跑一轮建立状态记录（forceProbeNow 需要已有 channel_states 行）。
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	// 多跑几轮：健康分需要样本累积。
	for i := 0; i < 4; i++ {
		forceProbeNow(t, st, 101)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", i+1, err)
		}
	}

	state, err := st.ChannelState(101)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Health != domain.HealthHealthy {
		t.Fatalf("测试前提不成立：渠道应被判为健康，实际 %q（分 %.1f）",
			state.Health, state.HealthScore)
	}

	if value, ok := fake.schedulableOf(101); ok && value {
		t.Fatalf("管理员手工关闭的健康渠道不应被 Guardian 重开，实际 ok=%v value=%v",
			ok, value)
	}
}

// TestPausedChannelStaysUnschedulable 确认自动上线不会踩掉人工暂停。
//
// 暂停是运维的显式意图，健康分再高也不能把它拉回流量。
func TestPausedChannelStaysUnschedulable(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.PausedAccountIDs = []int64{101}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	fake.setSchedulable(101, false)

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		forceProbeNow(t, st, 101)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("调度失败: %v", err)
		}
	}

	if value, ok := fake.schedulableOf(101); ok && value {
		t.Fatal("人工暂停的渠道不该被自动放回流量")
	}
}

// TestExcludedChannelNotReenabled 确认排除的渠道也不会被自动上线。
func TestExcludedChannelNotReenabled(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.ExcludedAccountIDs = []int64{101}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	fake.setSchedulable(101, false)

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		forceProbeNow(t, st, 101)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("调度失败: %v", err)
		}
	}

	if value, ok := fake.schedulableOf(101); ok && value {
		t.Fatal("已排除的渠道不该被自动放回流量")
	}
}

// TestDisabledAccountNotReenabled 确认 sub2api 侧停用的账号不会被强行拉起。
//
// status=inactive/error 是 sub2api 管理员的决定，Guardian 只管 schedulable，
// 不该越过它去改动账号的启用状态。
func TestDisabledAccountNotReenabled(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	fake.setStatus(101, "inactive")
	fake.setSchedulable(101, false)

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		forceProbeNow(t, st, 101)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("调度失败: %v", err)
		}
	}

	if value, ok := fake.schedulableOf(101); ok && value {
		t.Fatal("sub2api 侧已停用的账号不该被自动放回流量")
	}
}

// TestAutoApplyOffKeepsChannelUnschedulable 确认关掉写回开关时不会偷偷改上游。
func TestAutoApplyOffKeepsChannelUnschedulable(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.AutoApply.Schedulable = false
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	fake.setSchedulable(101, false)

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		forceProbeNow(t, st, 101)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("调度失败: %v", err)
		}
	}

	if value, ok := fake.schedulableOf(101); ok && value {
		t.Fatal("关闭「自动写回可调度状态」后不该改动 sub2api")
	}
}
