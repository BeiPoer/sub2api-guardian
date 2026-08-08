package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

// slowProbeFake 的探测接口会一直挂着，直到收到放行信号或请求被取消。
type slowProbeFake struct {
	release chan struct{}

	started  atomic.Int64 // 已开始的探测数
	finished atomic.Int64 // 正常完成的探测数
	aborted  atomic.Int64 // 因取消而中断的探测数

	mu     sync.Mutex
	writes int
}

func newSlowProbeFake() *slowProbeFake {
	return &slowProbeFake{release: make(chan struct{})}
}

func (f *slowProbeFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/admin/groups/all", func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, []map[string]any{
			{"id": 1, "name": "分组", "platform": "anthropic", "status": "active", "rate_multiplier": 1.0},
		})
	})

	mux.HandleFunc("/api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		items := make([]map[string]any, 0, 6)
		for i := 1; i <= 6; i++ {
			items = append(items, map[string]any{
				"id": 100 + i, "name": fmt.Sprintf("渠道%d", i), "platform": "anthropic",
				"type": "apikey", "status": "active", "schedulable": true,
				"priority": 10, "concurrency": 5, "rate_multiplier": 1.0,
				"group_ids": []int64{1},
			})
		}
		writeEnvelope(w, map[string]any{
			"items": items, "total": len(items), "page": 1, "page_size": 200, "pages": 1,
		})
	})

	mux.HandleFunc("/api/v1/admin/ops/requests", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "Ops monitoring is disabled"})
	})

	mux.HandleFunc("/api/v1/admin/accounts/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/")
		parts := strings.Split(rest, "/")
		action := ""
		if len(parts) > 1 {
			action = strings.Join(parts[1:], "/")
		}

		if action == "test" && r.Method == http.MethodPost {
			f.started.Add(1)

			// 先把响应头刷出去。Go 的 server 只有在开始写响应之后才能
			// 感知客户端断开，不这么做 r.Context() 不会因取消而触发。
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				_, _ = fmt.Fprint(w, "data: {\"type\":\"test_start\"}\n\n")
				flusher.Flush()
			}

			// 挂住，直到被放行或客户端取消。
			select {
			case <-f.release:
				f.finished.Add(1)
				_, _ = fmt.Fprint(w, "data: {\"type\":\"test_complete\",\"success\":true}\n\n")
			case <-r.Context().Done():
				f.aborted.Add(1)
			}
			return
		}

		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			f.mu.Lock()
			f.writes++
			f.mu.Unlock()
		}
		writeEnvelope(w, map[string]any{"ok": true})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		f.releaseAll()
		server.Close()
	})
	return server
}

func (f *slowProbeFake) releaseAll() {
	select {
	case <-f.release:
	default:
		close(f.release)
	}
}

func (f *slowProbeFake) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func setupCancelEngine(t *testing.T, fake *slowProbeFake) (*Engine, *store.Store) {
	t.Helper()
	server := fake.server(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SaveConnection(domain.Connection{
		BaseURL: server.URL, AdminAPIKey: "k", TimeoutSeconds: 30, Enabled: false,
	}); err != nil {
		t.Fatalf("保存连接失败: %v", err)
	}

	p, _ := st.Policy()
	p.Probe.Concurrency = 2 // 并发 2，保证有排队的任务可被取消
	p.Probe.TimeoutSeconds = 30
	p.Probe.SkipWhenTrafficFresh = false
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	client := upstream.New(server.URL, "k", 30*time.Second)
	return New(st, client), st
}

// TestCancelStopsRunningRound 验证取消能中断正在执行的调度与探测。
func TestCancelStopsRunningRound(t *testing.T) {
	fake := newSlowProbeFake()
	eng, st := setupCancelEngine(t, fake)
	ctx := context.Background()

	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- eng.RunOnce(ctx) }()

	// 等到确实有探测挂住了再取消。
	deadline := time.After(5 * time.Second)
	for fake.started.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("探测始终没有开始")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !eng.Status().Running {
		t.Fatal("此时应处于运行中")
	}

	if !eng.Cancel() {
		t.Fatal("Cancel() 应返回 true（确实取消了一轮）")
	}

	select {
	case err := <-done:
		// 人工取消不算失败。
		if err != nil {
			t.Fatalf("被取消的轮次不应返回错误，实际: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后调度没有及时结束")
	}

	status := eng.Status()
	if status.Running {
		t.Fatal("取消后不应仍显示运行中")
	}
	if status.LastRunError != "" {
		t.Fatalf("人工取消不该记为错误，实际: %q", status.LastRunError)
	}

	// 挂住的探测应被中断，而不是等到 30 秒超时。
	// 服务端记录 aborted 与客户端返回之间有微小延迟，给一点等待窗口。
	deadline = time.After(3 * time.Second)
	for fake.aborted.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("进行中的探测应随取消一起中断（started=%d finished=%d aborted=%d）",
				fake.started.Load(), fake.finished.Load(), fake.aborted.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if fake.finished.Load() != 0 {
		t.Fatalf("不应有探测正常完成，实际 %d 个", fake.finished.Load())
	}

	// 取消后不应基于半截数据写回 sub2api。
	if got := fake.writeCount(); got != 0 {
		t.Fatalf("取消后不应写回 sub2api，实际写入 %d 次", got)
	}

	events, _, err := st.Events(store.EventFilter{Action: "run_canceled", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("取消应留下 run_canceled 事件")
	}
}

func TestStopCancelsAndWaitsForRunningRound(t *testing.T) {
	fake := newSlowProbeFake()
	eng, st := setupCancelEngine(t, fake)
	conn, err := st.Connection()
	if err != nil {
		t.Fatalf("读取连接失败: %v", err)
	}
	conn.Enabled = true
	if err := st.SaveConnection(conn); err != nil {
		t.Fatalf("开启自动守护失败: %v", err)
	}
	if err := eng.Sync(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- eng.RunOnce(context.Background()) }()
	deadline := time.After(5 * time.Second)
	for fake.started.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("探测始终没有开始")
		case <-time.After(10 * time.Millisecond):
		}
	}

	stopDone := make(chan struct{})
	go func() {
		eng.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop 没有及时取消并等待当前轮次")
	}
	select {
	case <-runDone:
	default:
		t.Fatal("Stop 返回时调度轮次仍未退出")
	}
	conn, err = st.Connection()
	if err != nil {
		t.Fatalf("读取连接失败: %v", err)
	}
	if !conn.Enabled {
		t.Fatal("进程停机不应持久化关闭自动守护")
	}
}

// TestCancelWithoutRunningRound 验证空转时取消是安全的。
func TestCancelWithoutRunningRound(t *testing.T) {
	fake := newSlowProbeFake()
	eng, _ := setupCancelEngine(t, fake)

	if eng.Cancel() {
		t.Fatal("没有轮次在跑时 Cancel() 应返回 false")
	}
	if eng.Status().Running {
		t.Fatal("不应显示运行中")
	}
}

// TestCancelThenRunAgain 验证取消后还能正常再跑一轮。
func TestCancelThenRunAgain(t *testing.T) {
	fake := newSlowProbeFake()
	eng, _ := setupCancelEngine(t, fake)
	ctx := context.Background()

	if err := eng.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- eng.RunOnce(ctx) }()

	deadline := time.After(5 * time.Second)
	for fake.started.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("探测始终没有开始")
		case <-time.After(10 * time.Millisecond):
		}
	}
	eng.Cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("取消后调度没有及时结束")
	}

	// 放行探测，第二轮应能正常跑完。
	fake.releaseAll()
	if err := eng.RunOnce(ctx); err != nil {
		t.Fatalf("取消后应能重新调度，实际: %v", err)
	}
	if eng.Status().LastRunError != "" {
		t.Fatalf("第二轮不应有错误: %q", eng.Status().LastRunError)
	}
}
