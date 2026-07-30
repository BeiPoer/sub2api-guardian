package engine

import (
	"context"
	"strings"
	"testing"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/store"
)

// TestCleanup401DeletesAfterGuardsPass 走通「401 自动删除」的完整链路。
//
// 这是用户反馈「开启了认证错误处置、配了 401，但账号没被删」的对照实验：
// 各道守卫都放开时必须真的删掉，否则说明链路本身有问题。
func TestCleanup401DeletesAfterGuardsPass(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.Cleanup.Enabled = true
	p.Cleanup.Action = policy.FatalActionDelete
	p.Cleanup.TriggerStatusCodes = []int{401}
	p.Cleanup.Occurrences = 1
	p.Cleanup.Window = 5
	p.Cleanup.MinFusedMinutes = 0 // 放开观察期
	p.Cleanup.MaxPerRound = 5
	p.Cleanup.KeepLastInGroup = false // 放开「保留最后一个」
	p.Breaker.MinPoolSize = 0         // 放开保底，让它能真的熔断
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	fake.setFatal(101, true) // 探测返回 401
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	if fake.deleteCount(101) == 0 {
		state, _ := st.ChannelState(101)
		t.Fatalf("各守卫都放开时 401 渠道应被删除，实际未删（健康态 %q）", state.Health)
	}
}

// TestCleanup401BlockedByKeepLastInGroup 说明最常见的「没删掉」原因。
//
// keep_last_in_group 默认开启，分组里只剩这一个渠道时它绝不会被删 ——
// 这是有意的保护，不是 bug。用户需要能从日志里看出是它拦住的。
func TestCleanup401BlockedByKeepLastInGroup(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.Cleanup.Enabled = true
	p.Cleanup.Action = policy.FatalActionDelete
	p.Cleanup.TriggerStatusCodes = []int{401}
	p.Cleanup.Occurrences = 1
	p.Cleanup.MinFusedMinutes = 0
	p.Cleanup.MaxPerRound = 5
	p.Cleanup.KeepLastInGroup = true
	p.Breaker.MinPoolSize = 0
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	// 两个渠道都 401：分组会被清空，保护应当拦下最后一个。
	fake.setFatal(101, true)
	fake.setFatal(102, true)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	deleted := fake.deleteCount(101) + fake.deleteCount(102)
	if deleted > 1 {
		t.Fatalf("删除了 %d 个，分组内至少要留一个", deleted)
	}

	// 关键：必须留下可诊断的日志，否则用户只会看到「没反应」。
	events, _, err := st.Events(store.EventFilter{Action: "cleanup_skipped", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("被保护规则拦下时应写入 cleanup_skipped 事件，说明为什么没删")
	}
	if !strings.Contains(events[0].Message, "最后") {
		t.Fatalf("事件应说明是「保留最后一个」拦下的，实际: %s", events[0].Message)
	}
}

// TestCleanup401BlockedByMinFusedMinutes 覆盖最短观察期。
//
// 默认 30 分钟：刚熔断的渠道不会被立刻删掉。这也是「配好了却没删」的常见原因。
func TestCleanup401BlockedByMinFusedMinutes(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.Cleanup.Enabled = true
	p.Cleanup.Action = policy.FatalActionDelete
	p.Cleanup.TriggerStatusCodes = []int{401}
	p.Cleanup.Occurrences = 1
	p.Cleanup.MinFusedMinutes = 30 // 默认值
	p.Cleanup.MaxPerRound = 5
	p.Cleanup.KeepLastInGroup = false
	p.Breaker.MinPoolSize = 0
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	fake.setFatal(101, true)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	if fake.deleteCount(101) != 0 {
		t.Fatal("熔断未满最短观察期就不该删除")
	}
	events, _, err := st.Events(store.EventFilter{Action: "cleanup_skipped", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("被观察期拦下时应写入 cleanup_skipped，说明还要等多久")
	}
	if !strings.Contains(events[0].Message, "分钟") {
		t.Fatalf("事件应说明观察期还没到，实际: %s", events[0].Message)
	}
}

// TestCleanupSkippedExplainsNotFused 覆盖「还没熔断」这一步。
//
// 保底强留的渠道健康态是 survivor 而不是 fused，因此不会被处置 ——
// 用户看到的现象同样是「配了 401 却没删」。
func TestCleanupSkippedExplainsNotFused(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.Cleanup.Enabled = true
	p.Cleanup.Action = policy.FatalActionDelete
	p.Cleanup.TriggerStatusCodes = []int{401}
	p.Cleanup.Occurrences = 1
	p.Cleanup.MinFusedMinutes = 0
	p.Cleanup.MaxPerRound = 5
	p.Cleanup.KeepLastInGroup = false
	p.Breaker.MinPoolSize = 1 // 保底：最后一个转 survivor 而不是 fused
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	fake.setFatal(101, true)
	fake.setFatal(102, true)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	var survivor bool
	for _, id := range []int64{101, 102} {
		if state, err := st.ChannelState(id); err == nil && state.Health == domain.HealthSurvivor {
			survivor = true
		}
	}
	if !survivor {
		t.Fatal("测试前提不成立：应有一个渠道被保底强留")
	}

	events, _, err := st.Events(store.EventFilter{Action: "cleanup_skipped", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	var explained bool
	for _, ev := range events {
		if strings.Contains(ev.Message, "保底") || strings.Contains(ev.Message, "未熔断") {
			explained = true
		}
	}
	if !explained {
		t.Fatal("保底强留的渠道不被处置时，应写日志说明原因")
	}
}
