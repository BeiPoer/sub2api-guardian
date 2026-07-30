package engine

import (
	"context"
	"testing"

	"sub2api-guardian/backend/internal/domain"
)

// TestUnexcludedChannelClearsStaleState 是「没排除却显示已排除」的回归。
//
// 渠道被排除时状态写成 excluded；移出排除名单后，如果没有把这个陈旧状态
// 清掉，页面会一直显示「已排除」—— 因为 DTO 直接读 channel_states。
func TestUnexcludedChannelClearsStaleState(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	// 先排除 102，跑一轮让状态落成 excluded。
	p, _ := st.Policy()
	p.ExcludedAccountIDs = []int64{102}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	state, err := st.ChannelState(102)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Health != domain.HealthExcluded {
		t.Fatalf("排除后状态 = %v, 期望 excluded", state.Health)
	}

	// 移出排除名单后再跑一轮：状态必须跟着变，不能停留在 excluded。
	p, _ = st.Policy()
	p.ExcludedAccountIDs = nil
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("二轮调度失败: %v", err)
	}

	state, err = st.ChannelState(102)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if state.Health == domain.HealthExcluded {
		t.Fatal("已移出排除名单，状态不应仍为 excluded")
	}
}

// TestExcludedGroupChannelStateNotStale 覆盖分组维度的同一问题。
//
// 分组被排除时，组内渠道不再进入 r.channels，它们的状态就没人更新了；
// 分组恢复管控后，这些陈旧状态必须被纠正。
func TestExcludedGroupChannelStateNotStale(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	// 正常跑一轮，渠道状态是健康的。
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}

	// 排除整个分组后再跑一轮。
	p, _ := st.Policy()
	p.ExcludedGroupIDs = []int64{1}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("二轮调度失败: %v", err)
	}

	// 分组被排除时，组内渠道状态应显示为 excluded 而不是保留旧的 healthy。
	for _, id := range []int64{101, 102} {
		state, err := st.ChannelState(id)
		if err != nil {
			t.Fatalf("读取渠道 %d 状态失败: %v", id, err)
		}
		if state.Health == domain.HealthHealthy {
			t.Fatalf("分组已排除，渠道 %d 不该仍显示 healthy（陈旧状态）", id)
		}
	}

	// 恢复分组管控后，状态要能重新被更新。
	p, _ = st.Policy()
	p.ExcludedGroupIDs = nil
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("三轮调度失败: %v", err)
	}
	for _, id := range []int64{101, 102} {
		state, err := st.ChannelState(id)
		if err != nil {
			t.Fatalf("读取渠道 %d 状态失败: %v", id, err)
		}
		if state.Health == domain.HealthExcluded {
			t.Fatalf("分组已恢复管控，渠道 %d 不该仍为 excluded", id)
		}
	}
}
