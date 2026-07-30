package engine

import (
	"context"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/store"
)

func TestManagedIntentPersistsWhenWriteFails(t *testing.T) {
	eng, st, fake := setupEngine(t)
	fake.setFailWrites(101, true)

	p := policy.Default()
	ch := &channel{
		account: domain.Account{
			ID:             101,
			Name:           "渠道",
			Status:         "active",
			Priority:       10,
			Concurrency:    5,
			RateMultiplier: 1,
			Schedulable:    true,
		},
		pol: p,
		desired: desired{
			health:      domain.HealthHealthy,
			schedulable: true,
			priority:    20,
		},
	}

	if eng.applyChannel(context.Background(), &round{now: time.Now()}, ch) {
		t.Fatal("上游写入失败时不应报告已应用")
	}
	base, err := st.Baseline(101)
	if err != nil {
		t.Fatalf("写入失败后应保留基线与所有权意图: %v", err)
	}
	if base.ManagedPriority == nil || *base.ManagedPriority != 20 {
		t.Fatalf("managed priority = %v，期望在请求前持久化为 20", base.ManagedPriority)
	}
}

func TestRestoreBaselineRestoresNilAndZeroValues(t *testing.T) {
	eng, st, fake := setupEngine(t)
	managedLoadFactor := 25
	currentLoadFactor := 25
	managedRateMultiplier := 2.0
	base := domain.Baseline{
		AccountID:             101,
		Status:                "active",
		Priority:              10,
		LoadFactor:            nil,
		Concurrency:           5,
		RateMultiplier:        0,
		Schedulable:           true,
		CapturedAt:            time.Now(),
		OwnershipVersion:      1,
		ManagedLoadFactor:     &managedLoadFactor,
		ManagedRateMultiplier: &managedRateMultiplier,
	}
	if err := st.SaveBaseline(base); err != nil {
		t.Fatalf("保存基线失败: %v", err)
	}
	ch := &channel{
		account: domain.Account{
			ID:             101,
			Name:           "渠道",
			Status:         "active",
			Priority:       10,
			LoadFactor:     &currentLoadFactor,
			Concurrency:    5,
			RateMultiplier: managedRateMultiplier,
			Schedulable:    true,
		},
		baseline: &base,
	}

	if !eng.restoreBaseline(context.Background(), ch, "测试恢复") {
		t.Fatal("基线有差异时应执行恢复")
	}
	if value, ok := fake.updateOf(101, "load_factor"); !ok || value != float64(0) {
		t.Fatalf("nil load_factor 应通过 0 清空，实际 %v / %v", value, ok)
	}
	if value, ok := fake.updateOf(101, "rate_multiplier"); !ok || value != float64(0) {
		t.Fatalf("rate_multiplier=0 应被恢复，实际 %v / %v", value, ok)
	}
	if _, err := st.Baseline(101); !store.IsNotFound(err) {
		t.Fatalf("恢复成功后应删除基线，实际 %v", err)
	}
}

func TestRestoreBaselinePreservesExternalChanges(t *testing.T) {
	eng, st, fake := setupEngine(t)
	managedPriority := 20
	base := domain.Baseline{
		AccountID:        101,
		Status:           "active",
		Priority:         10,
		Concurrency:      5,
		RateMultiplier:   1,
		Schedulable:      true,
		CapturedAt:       time.Now(),
		OwnershipVersion: 1,
		ManagedPriority:  &managedPriority,
	}
	if err := st.SaveBaseline(base); err != nil {
		t.Fatalf("保存基线失败: %v", err)
	}
	ch := &channel{
		account: domain.Account{
			ID:             101,
			Name:           "渠道",
			Status:         "active",
			Priority:       30,
			Concurrency:    5,
			RateMultiplier: 1,
			Schedulable:    true,
		},
		baseline: &base,
	}

	if eng.restoreBaseline(context.Background(), ch, "测试冲突") {
		t.Fatal("管理员已把 priority 改成其他值时不应覆盖")
	}
	if value, ok := fake.updateOf(101, "priority"); ok {
		t.Fatalf("管理员修改应被保留，Guardian 却写入了 %v", value)
	}
	if _, err := st.Baseline(101); !store.IsNotFound(err) {
		t.Fatalf("已交还控制权后应删除基线，实际 %v", err)
	}
}

func TestLeavingManagedScopeRestoresBaseline(t *testing.T) {
	eng, st, fake := setupEngine(t)
	ctx := context.Background()

	fake.setFatal(102, true)
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("熔断轮次失败: %v", err)
	}
	if schedulable, ok := fake.schedulableOf(102); !ok || schedulable {
		t.Fatalf("测试前提不成立：102 应已熔断，实际 %v / %v", schedulable, ok)
	}
	if _, err := st.Baseline(102); err != nil {
		t.Fatalf("熔断后应存在基线: %v", err)
	}

	p, _ := st.Policy()
	p.ManagedAccountTypes = []string{"oauth"}
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存受管类型失败: %v", err)
	}
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("退出受管范围轮次失败: %v", err)
	}

	if schedulable, ok := fake.schedulableOf(102); !ok || !schedulable {
		t.Fatalf("退出受管范围后应恢复 schedulable=true，实际 %v / %v", schedulable, ok)
	}
	if _, err := st.Baseline(102); !store.IsNotFound(err) {
		t.Fatalf("交还控制权后应删除基线，实际 %v", err)
	}
}

func TestManualDisabledStatusIsSentAsInactive(t *testing.T) {
	eng, _, fake := setupEngine(t)
	ctx := context.Background()
	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	if err := eng.UpdateAccountSettings(ctx, 101, map[string]any{"status": "disabled"}); err != nil {
		t.Fatalf("兼容 disabled 输入失败: %v", err)
	}
	if value, ok := fake.updateOf(101, "status"); !ok || value != "inactive" {
		t.Fatalf("sub2api 应收到 status=inactive，实际 %v / %v", value, ok)
	}
}
