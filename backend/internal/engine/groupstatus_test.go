package engine

import (
	"context"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
)

// TestGroupWithAllChannelsExcludedIsNotHealthy 覆盖审计第 ⑧ 条。
//
// 断供判定原先只数 FusedAccounts + PausedAccounts，人工排除与 sub2api 侧停用
// 都不算。于是把一个分组里所有渠道都排除掉之后，分组状态仍显示「健康」——
// 而它实际上一个渠道都不接流量。
//
// 「有没有渠道接流量」才是断供的判据，渠道为什么不接流量不重要。
func TestGroupWithAllChannelsExcludedIsNotHealthy(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}
	if state, _ := st.GroupState(1); state.Status != domain.GroupHealthy {
		t.Fatalf("前提不成立：分组应先是健康的，实际 %q", state.Status)
	}

	// 把组里两个渠道全部人工排除。
	p, _ := st.Policy()
	p.ExcludedAccountIDs = []int64{101, 102}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("二轮调度失败: %v", err)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.Status == domain.GroupHealthy {
		t.Fatal("全部渠道被排除的分组不该显示为健康 —— 它一个渠道都不接流量")
	}
	if state.AvailableAccounts != 0 {
		t.Fatalf("可用渠道数 = %d, 期望 0", state.AvailableAccounts)
	}
}

// TestGroupWithAllChannelsDisabledIsNotHealthy 同上，但停用发生在 sub2api 侧。
func TestGroupWithAllChannelsDisabledIsNotHealthy(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}

	fake.setStatus(101, "inactive")
	fake.setStatus(102, "inactive")
	if err := eng.SyncNow(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.Status == domain.GroupHealthy {
		t.Fatal("全部渠道在 sub2api 停用的分组不该显示为健康")
	}
	if state.AvailableAccounts != 0 {
		t.Fatalf("可用渠道数 = %d, 期望 0", state.AvailableAccounts)
	}
}

// TestUnschedulableChannelNotCountedHealthy 是「关掉调用的渠道不该算健康」的回归。
//
// 早期只判 status 非 active，漏了 schedulable=false（人工在 sub2api 后台关掉的）。
// 于是探测满分但一个请求都接不到的渠道仍被计入 HealthyAccounts，
// 矩阵上的「健康」数比实际能服务的多，与网站对不上。
func TestUnschedulableChannelNotCountedHealthy(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 两个渠道都健康，但 101 在上游被关掉了调用。
	fake.setSchedulable(101, false)
	// 关掉自动上线，否则引擎会把它重新打开，测不到这个场景。
	p, _ := st.Policy()
	p.AutoApply.Schedulable = false
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", i+1, err)
		}
		forceProbeNow(t, st, 101)
		forceProbeNow(t, st, 102)
	}

	// 前提确认：101 自身的健康态确实是 healthy（探测是成功的）。
	if state, err := st.ChannelState(101); err != nil || state.Health != domain.HealthHealthy {
		t.Fatalf("测试前提不成立：101 应为 healthy，实际 %v", state.Health)
	}

	group, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if group.HealthyAccounts != 1 {
		t.Fatalf("健康数 = %d, 期望 1（101 已被关掉调用，不该算健康）",
			group.HealthyAccounts)
	}
	if group.ExcludedAccounts != 1 {
		t.Fatalf("已排除数 = %d, 期望 1（关掉调用等同不接流量）",
			group.ExcludedAccounts)
	}
	// 可用数也不该包含它 —— 两个口径必须一致。
	if group.AvailableAccounts != 1 {
		t.Fatalf("可用数 = %d, 期望 1", group.AvailableAccounts)
	}
}

// TestFusedChannelKeepsItsOwnBucket 确认熔断渠道不被归进「已排除」。
//
// 熔断渠道本来就该是不可调度的。把它算进 ExcludedAccounts 会让
// 「为什么不接流量」这个信息丢掉 —— 熔断是自动判定，排除是人工决定。
func TestFusedChannelKeepsItsOwnBucket(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.Breaker.MinPoolSize = 0 // 让熔断能真的发生
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	fake.setFatal(101, true)

	for i := 0; i < 3; i++ {
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", i+1, err)
		}
		forceProbeNow(t, st, 101)
		forceProbeNow(t, st, 102)
	}

	group, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if group.FusedAccounts != 1 {
		t.Fatalf("熔断数 = %d, 期望 1", group.FusedAccounts)
	}
	if group.ExcludedAccounts != 0 {
		t.Fatalf("已排除数 = %d, 期望 0（熔断有自己的计数，不该混进排除）",
			group.ExcludedAccounts)
	}
}

// TestRateLimitedGroupIsNotHealthy 是「限流不该显示成健康」的回归。
//
// 限流渠道不熔断、不摘调度（sub2api 自己会排除并到点恢复），但它**不是正常状态**。
// 早期实现里限流只体现为「降级」，而分组一旦有降级就统一显示「部分异常」——
// 与真故障混成一个样子，运维分不清该不该动手。
//
// 现在给限流单独一个分组状态与计数。
func TestRateLimitedGroupIsNotHealthy(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 让两个渠道都限流。
	fake.setProbeResult(101, probeQuota)
	fake.setProbeResult(102, probeQuota)
	for i := 0; i < 3; i++ {
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", i+1, err)
		}
		forceProbeNow(t, st, 101)
		forceProbeNow(t, st, 102)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.Status == domain.GroupHealthy {
		t.Fatal("全员限流的分组不该显示为「健康」—— 限流不是正常状态")
	}
	if state.Status != domain.GroupRateLimited {
		t.Fatalf("分组状态 = %q, 期望 rate_limited（只有限流、没有真故障）", state.Status)
	}
	if state.RateLimitedAccounts == 0 {
		t.Fatal("应统计出限流渠道数，否则界面分不清限流与真故障")
	}
	// 限流渠道仍在池子里，可用数不该掉。
	if state.AvailableAccounts == 0 {
		t.Fatal("限流渠道仍应计入可用池：它们还在接流量")
	}
}

// TestRealFailureOutranksRateLimit 确认真故障优先显示。
//
// 同时存在限流与真故障时，必须显示「部分异常」而不是「限流中」——
// 后者会让人以为等等就好，从而漏掉真正需要处理的渠道。
func TestRealFailureOutranksRateLimit(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	fake.setProbeResult(101, probeQuota) // 限流
	fake.setFatal(102, true)             // 真故障（401）
	for i := 0; i < 3; i++ {
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", i+1, err)
		}
		forceProbeNow(t, st, 101)
		forceProbeNow(t, st, 102)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.Status == domain.GroupRateLimited {
		t.Fatal("有真故障时不该显示「限流中」，那会让人以为等等就好")
	}
	if state.Status == domain.GroupHealthy {
		t.Fatalf("有真故障的分组不该显示健康，实际 %q", state.Status)
	}
}

// TestHealthyGroupStaysHealthy 确认修复没有把正常分组误判成异常。
func TestHealthyGroupStaysHealthy(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.Status != domain.GroupHealthy {
		t.Fatalf("两个渠道都正常时分组状态 = %q, 期望 healthy", state.Status)
	}
	if state.AvailableAccounts != 2 {
		t.Fatalf("可用渠道数 = %d, 期望 2", state.AvailableAccounts)
	}
}

// TestPartiallyExcludedGroupIsNotAllFused 确认「部分排除」不会被夸大成整组断供。
func TestPartiallyExcludedGroupIsNotAllFused(t *testing.T) {
	eng, st, _ := setupEngine(t)
	ctx := context.Background()

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("首轮调度失败: %v", err)
	}

	p, _ := st.Policy()
	p.ExcludedAccountIDs = []int64{102}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("二轮调度失败: %v", err)
	}

	state, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if state.Status == domain.GroupAllFused {
		t.Fatal("还有一个健康渠道时不该判为整组断供")
	}
	if state.AvailableAccounts != 1 {
		t.Fatalf("可用渠道数 = %d, 期望 1", state.AvailableAccounts)
	}
}

// TestUpstreamRateLimitWindowBeatsProbeSuccess 是「矩阵数字与网站对不上」的回归。
//
// Guardian 只有探测这一个信息源，而 sub2api 在真实流量里撞到 429 时会写下
// rate_limit_reset_at 并据此停止选路。两者必然不同步：探测恰好成功一次，
// Guardian 就把渠道算回健康，而网站在整个窗口内（可能长达数天）都不会用它。
//
// 上游窗口是权威依据，探测成功也不能翻案。
func TestUpstreamRateLimitWindowBeatsProbeSuccess(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	// 101 探测成功，但上游已经把它限流到一小时后。
	fake.setRateLimitReset(101, time.Now().Add(time.Hour))

	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("调度失败: %v", err)
	}

	// 前提确认：探测确实是成功的，Guardian 自己判它健康。
	if state, err := st.ChannelState(101); err != nil || state.Health != domain.HealthHealthy {
		t.Fatalf("测试前提不成立：101 应为 healthy，实际 %v", state.Health)
	}

	group, err := st.GroupState(1)
	if err != nil {
		t.Fatalf("读取分组状态失败: %v", err)
	}
	if group.HealthyAccounts != 1 {
		t.Fatalf("健康数 = %d, 期望 1（101 在上游限流窗口里，不该算健康）",
			group.HealthyAccounts)
	}
	if group.RateLimitedAccounts != 1 {
		t.Fatalf("限流数 = %d, 期望 1", group.RateLimitedAccounts)
	}
	// 限流不等于「不可用」：渠道没被摘出池子，窗口一过自动回来。
	if group.ExcludedAccounts != 0 {
		t.Fatalf("不可用数 = %d, 期望 0（限流会自愈，不该催人动手）",
			group.ExcludedAccounts)
	}
	// 各项之和必须等于总数，否则页面数字加不起来。
	sum := group.HealthyAccounts + group.DegradedAccounts + group.FusedAccounts +
		group.PausedAccounts + group.ExcludedAccounts + group.PendingAccounts
	if sum != group.TotalAccounts {
		t.Fatalf("各项之和 = %d, 总数 = %d，两者必须相等", sum, group.TotalAccounts)
	}
}

// TestUpstreamRateLimitedChannelIsNeverFused 守着「限流一律不熔断」那条硬性约束。
//
// 限流渠道被 sub2api 自己按 rate_limit_reset_at 排除在选路之外、到点自动恢复。
// Guardian 若去改 schedulable，等于把「到点自动恢复」换成「必须探测才能恢复」，
// 高并发时白白损失容量 —— 而那正是最需要容量的时候。
//
// 只看样本时有个漏洞：被网站限流数天、但恰好探测成功过一次的渠道会重新变成
// 熔断候选。这个用例把上游窗口也纳入判定，堵住那个漏洞。
func TestUpstreamRateLimitedChannelIsNeverFused(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	p, _ := st.Policy()
	p.Breaker.MinPoolSize = 0 // 拆掉保底保护，让熔断能真的发生
	p.Breaker.HTTPFailures = 1
	p.Breaker.HTTPWindow = 3
	p.Breaker.HTTPScoreBelow = 100
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	// 上游在限流窗口里，但探测一直成功 —— 正是漏洞成立的条件。
	fake.setRateLimitReset(101, time.Now().Add(24*time.Hour))

	for i := 0; i < 3; i++ {
		if err := eng.RunOnce(ctx); err != nil {
			t.Fatalf("第 %d 轮调度失败: %v", i+1, err)
		}
		forceProbeNow(t, st, 101)
		forceProbeNow(t, st, 102)
	}

	if fake.schedulableNow(101) == false {
		t.Fatal("限流渠道的 schedulable 被改成了 false —— 这条约束不允许被破")
	}
	state, err := st.ChannelState(101)
	if err != nil {
		t.Fatalf("读取渠道状态失败: %v", err)
	}
	if state.Health == domain.HealthFused {
		t.Fatal("限流渠道被熔断了 —— sub2api 自己会到点恢复，不该插手")
	}
}
