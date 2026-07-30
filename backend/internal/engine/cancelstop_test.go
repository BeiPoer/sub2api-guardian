package engine

import (
	"context"
	"testing"
)

// TestCancelStopsFutureRounds 是「取消调度没真停下来」的回归。
//
// 早期实现只中断当前轮次，而心跳 15 秒后照常再起一轮 —— 用户点了取消，
// 探测和写回却还在继续。现在取消会持久化关闭自动守护，心跳不再发起新轮次。
func TestCancelStopsFutureRounds(t *testing.T) {
	eng, st, _ := setupEngine(t)

	// setupEngine 里 Enabled=true（自动守护开着）。
	if conn, _ := st.Connection(); !conn.Enabled {
		t.Fatal("测试前提不成立：自动守护应先是开启的")
	}

	if eng.Cancel(); false {
		t.Fatal("unreachable")
	}

	conn, err := st.Connection()
	if err != nil {
		t.Fatalf("读取连接失败: %v", err)
	}
	if conn.Enabled {
		t.Fatal("取消调度后自动守护应被关闭，否则心跳会立刻再起一轮")
	}

	// 心跳此时应该只同步目录，不再跑调度。
	if status := eng.Status(); status.AutoEnabled {
		t.Fatal("状态里的 auto_enabled 应为 false，前端据此把按钮切成「启动调度」")
	}
}

// TestResumeRestartsScheduling 确认能重新启动。
func TestResumeRestartsScheduling(t *testing.T) {
	eng, st, _ := setupEngine(t)

	eng.Cancel()
	if conn, _ := st.Connection(); conn.Enabled {
		t.Fatal("取消后应为关闭")
	}

	if err := eng.Resume(); err != nil {
		t.Fatalf("恢复调度失败: %v", err)
	}
	conn, err := st.Connection()
	if err != nil {
		t.Fatalf("读取连接失败: %v", err)
	}
	if !conn.Enabled {
		t.Fatal("恢复后自动守护应重新开启")
	}
	if status := eng.Status(); !status.AutoEnabled {
		t.Fatal("状态里的 auto_enabled 应为 true")
	}
}

// TestCancelSurvivesRestart 确认停止状态跨重启保持。
//
// 存在连接配置里而不是内存里：进程重启后不该悄悄又开始跑。
func TestCancelSurvivesRestart(t *testing.T) {
	eng, st, _ := setupEngine(t)
	eng.Cancel()

	// 用同一个库新建一个引擎实例，模拟重启。
	restarted := New(st, eng.client)
	if status := restarted.Status(); status.AutoEnabled {
		t.Fatal("重启后仍应保持停止状态")
	}
}

// TestCancelWithoutRunningRoundStillStops 确认没有轮次在跑时也能停。
//
// 早期实现在 cancelRun == nil 时直接 return false，什么都不做 ——
// 于是「当前没在跑」的那一刻点取消完全无效，下一轮照常开始。
func TestCancelWithoutRunningRoundStillStops(t *testing.T) {
	eng, st, _ := setupEngine(t)

	// 此刻没有任何轮次在执行。
	stopped := eng.Cancel()
	if stopped {
		t.Fatal("没有轮次在跑时不该报告「已中断轮次」")
	}

	conn, _ := st.Connection()
	if conn.Enabled {
		t.Fatal("即使没有轮次在跑，取消也必须把自动守护关掉")
	}
}

// TestManualRunStillWorksAfterCancel 确认停止自动调度不影响手动「立即调度」。
//
// 停止的是心跳自动触发，不是禁用调度能力本身。
func TestManualRunStillWorksAfterCancel(t *testing.T) {
	eng, st, _ := setupEngine(t)
	eng.Cancel()

	if err := eng.RunOnce(context.Background()); err != nil {
		t.Fatalf("停止自动调度后手动调度仍应可用: %v", err)
	}
	if _, err := st.ChannelState(101); err != nil {
		t.Fatalf("手动调度应产生渠道状态: %v", err)
	}
}
