package engine

import (
	"context"
	"testing"

	"sub2api-guardian/backend/internal/domain"
)

// TestFailedWriteDoesNotClaimFused 是写回一致性的核心回归。
//
// 熔断要靠把 schedulable=false 写回 sub2api 才真正生效。写回失败时渠道仍在接流量
// —— 此时若把状态落库成「已熔断」，页面会给出虚假的安全感：运维以为流量已经摘掉，
// 实际并没有。而且下一轮 initDesired 会拿这个假状态当起点，错误会自我延续。
func TestFailedWriteDoesNotClaimFused(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 101 探测致命错误 → 应当熔断；但它的写回会失败。
	fake.setFatal(101, true)
	fake.setFailWrites(101, true)

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	state, err := st.ChannelState(101)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}

	// 期望值要记下来（下一轮继续重试），但生效状态不能撒谎。
	if state.DesiredHealth != domain.HealthFused {
		t.Fatalf("期望状态 = %q, 应记为 fused 以便下轮重试", state.DesiredHealth)
	}
	if state.Health == domain.HealthFused {
		t.Fatal("写回失败时不能把生效状态标成 fused —— sub2api 侧仍在接流量")
	}
	if !state.ApplyPending {
		t.Fatal("写回未落地时应标记 ApplyPending，让界面能如实提示")
	}
	if state.LastApplyError == "" {
		t.Fatal("应记录写回失败的原因")
	}

	// sub2api 侧确实没被改动，这才是「不能显示成已熔断」的依据。
	if value, ok := fake.schedulableOf(101); ok && !value {
		t.Fatal("假 sub2api 不该记录到 schedulable=false，写回本应失败")
	}
}

// TestSuccessfulWriteMarksFused 确认修复没有妨碍正常路径：
// 写回成功时，状态照旧推进到 fused，且不留待生效标记。
func TestSuccessfulWriteMarksFused(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	fake.setFatal(101, true) // 写回不失败

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	state, err := st.ChannelState(101)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Health != domain.HealthFused {
		t.Fatalf("写回成功时生效状态 = %q, 期望 fused", state.Health)
	}
	if state.DesiredHealth != domain.HealthFused {
		t.Fatalf("期望状态 = %q, 期望 fused", state.DesiredHealth)
	}
	if state.ApplyPending {
		t.Fatalf("写回成功后不该仍标记 ApplyPending（原因: %s）", state.LastApplyError)
	}
	if state.LastApplyError != "" {
		t.Fatalf("写回成功后不该留有错误: %q", state.LastApplyError)
	}
}

// TestPendingWriteRetriedNextRound 验证写回失败不会让熔断被无限期搁置：
// 上游恢复可写后，下一轮必须继续尝试并把状态转正。
//
// 这是选择「分离字段」而非「失败就不推进健康态」的原因 —— 后者会让期望值丢失，
// 熔断在上游抖动期间被永久推迟。
func TestPendingWriteRetriedNextRound(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	fake.setFatal(101, true)
	fake.setFailWrites(101, true)

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	state, err := st.ChannelState(101)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Health == domain.HealthFused {
		t.Fatal("首轮写回失败，不该标成 fused")
	}

	// 上游恢复可写，下一轮应把熔断真正落地。
	fake.setFailWrites(101, false)
	forceProbeNow(t, st, 101)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("二轮调度失败: %v", err)
	}

	state, err = st.ChannelState(101)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Health != domain.HealthFused {
		t.Fatalf("写回成功后生效状态 = %q, 期望 fused", state.Health)
	}
	if state.ApplyPending {
		t.Fatal("写回成功后应清除 ApplyPending")
	}
	if value, ok := fake.schedulableOf(101); !ok || value {
		t.Fatal("二轮应把 schedulable=false 写进 sub2api")
	}
}

// TestNoWriteNeededIsNotPending 确认「本来就不需要写回」不会被误标为待生效。
//
// 没有这条约束，界面会在一切正常时也满屏挂着「待生效」提示，等于没有提示。
func TestNoWriteNeededIsNotPending(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	// 跑两轮：第二轮时期望值已经稳定，不该有任何待生效标记。
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	forceProbeNow(t, st, 101)
	forceProbeNow(t, st, 102)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("二轮调度失败: %v", err)
	}

	states, err := st.ChannelStateMap()
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if len(states) == 0 {
		t.Fatal("没有任何渠道状态，测试前提不成立")
	}
	for id, state := range states {
		if state.ApplyPending {
			t.Fatalf("渠道 %d 无需写回却被标为待生效（原因: %s）", id, state.LastApplyError)
		}
		if state.Health != state.DesiredHealth {
			t.Fatalf("渠道 %d 生效状态 %q 与期望状态 %q 不一致，但没有待生效标记",
				id, state.Health, state.DesiredHealth)
		}
	}
}

// TestCleanupSkipsChannelWithPendingFuse 确认写回没落地时不会误删渠道。
//
// 清理（含 401 自动删除）是不可逆操作，前提是「熔断已经真正生效」。
// 如果写回失败却仍按熔断处置，就会在渠道还在接流量时把它删掉。
func TestCleanupSkipsChannelWithPendingFuse(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.Cleanup.Enabled = true
	p.Cleanup.Action = "delete"
	p.Cleanup.Occurrences = 1
	p.Cleanup.MaxPerRound = 5
	p.Cleanup.OnlyAuthErrors = true
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	fake.setFatal(101, true)
	fake.setFailWrites(101, true)

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	if fake.deleteCount(101) != 0 {
		t.Fatal("熔断写回未生效时不该删除渠道")
	}
}
