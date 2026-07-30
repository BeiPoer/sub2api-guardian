package engine

import (
	"context"
	"testing"

	"sub2api-guardian/backend/internal/domain"
)

// TestFusedChannelRecoversWithProbeDisabled 是回池死锁的回归。
//
// 死锁成因：熔断意味着 schedulable=false，渠道拿不到任何真实流量，
// 因此唯一的健康信号来源是主动探测。而 shouldProbe 早期实现里，
// 探测总开关一关就直接 return false，连恢复探测一起挡掉 ——
// 结果是关掉探测后，已熔断的渠道永远回不了池，只能人工干预。
//
// 正确语义：Probe.Enabled 管的是「常规巡检」；Recovery.Enabled 管的是
// 「熔断渠道的复活探测」。后者是熔断的退出条件，不能被前者连坐。
func TestFusedChannelRecoversWithProbeDisabled(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 先让 102 熔断。
	fake.setFatal(102, true)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	if state, _ := st.ChannelState(102); state.Health != domain.HealthFused {
		t.Fatalf("前提不成立：102 应先熔断，实际 %q", state.Health)
	}

	// 关掉常规探测，只留恢复探测。运维常这么配：不想给正常渠道加探测压力，
	// 但仍希望熔断的渠道能自动复活。
	p, _ := st.Policy()
	p.Probe.Enabled = false
	p.Recovery.Enabled = true
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	// 上游恢复正常。恢复探测应该持续进行，健康分随成功样本累积回升后回池。
	// 多跑几轮是因为熔断时那条致命样本还在评分窗口里压着分数 ——
	// 这正是恢复探测必须能持续跑的原因：一次成功不足以复活，探测被挡死就永远复活不了。
	fake.setFatal(102, false)
	recovered := false
	for i := 0; i < 6; i++ {
		forceProbeNow(t, st, 102)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", i+2, err)
		}
		state, err := st.ChannelState(102)
		if err != nil {
			t.Fatalf("读取状态失败: %v", err)
		}
		if state.Health != domain.HealthFused {
			recovered = true
			break
		}
	}

	if !recovered {
		state, _ := st.ChannelState(102)
		t.Fatalf("关闭常规探测后，熔断渠道仍应能靠恢复探测回池（健康分 %.1f，连续成功 %d）",
			state.HealthScore, state.ConsecutiveOK)
	}
	if value, ok := fake.schedulableOf(102); !ok || !value {
		t.Fatal("回池后应把 schedulable=true 写回 sub2api")
	}
}

// TestProbeDisabledSkipsHealthyChannels 确认修复没有把探测总开关变成空设置：
// 未熔断的渠道在关闭探测后不该再被探测。
func TestProbeDisabledSkipsHealthyChannels(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.Probe.Enabled = false
	p.Traffic.Enabled = false
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}
	if probed := eng.Status().LastSummary.Probed; probed != 0 {
		t.Fatalf("关闭探测后不该探测健康渠道，实际探测 %d 个", probed)
	}
}

// TestRecoveryDisabledStopsRecoveryProbe 确认恢复开关仍然有效：
// 关掉 Recovery 后，熔断渠道不再被探测（运维明确不想让它自动复活）。
func TestRecoveryDisabledStopsRecoveryProbe(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	fake.setFatal(102, true)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	if state, _ := st.ChannelState(102); state.Health != domain.HealthFused {
		t.Fatalf("前提不成立：102 应先熔断，实际 %q", state.Health)
	}

	p, _ := st.Policy()
	p.Recovery.Enabled = false
	p.Traffic.Enabled = false
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	// 跑同样多轮，确保「保持熔断」不是因为轮数不够，而是恢复开关真的生效了。
	fake.setFatal(102, false)
	for i := 0; i < 6; i++ {
		forceProbeNow(t, st, 102)
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", i+2, err)
		}
		if state, _ := st.ChannelState(102); state.Health != domain.HealthFused {
			t.Fatalf("关闭自动回池后应保持熔断，第 %d 轮变成了 %q", i+2, state.Health)
		}
	}
	if probed := eng.Status().LastSummary.Probed; probed != 0 {
		t.Fatalf("关闭自动回池后不该再探测熔断渠道，实际探测 %d 个", probed)
	}
}
