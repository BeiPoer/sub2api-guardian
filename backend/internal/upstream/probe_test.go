package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// probeServer 起一个只回放给定 SSE 事件流的假 sub2api。
func probeServer(t *testing.T, events ...string) *Client {
	return probeServerDelayed(t, 0, events...)
}

// probeServerDelayed 在每个事件之间插入延迟，用于区分首字时间与总耗时。
func probeServerDelayed(t *testing.T, gap time.Duration, events ...string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			if gap > 0 {
				time.Sleep(gap)
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	return New(server.URL, "k", 5*time.Second)
}

// TestProbeCompleteWithoutSuccessIsFailure 是探针成功判定的核心回归。
//
// sub2api 的 TestEvent.Success 带 omitempty：success=false 时该字段**根本不出现**
// 在 JSON 里。早期实现用 `event.Success || event.Error == ""` 判定，于是
// 「success 缺失且 error 缺失」被当成成功 —— 一个明确没说自己成功的完成事件
// 会被记为满分样本，把不可用的渠道留在池子里。
//
// 正确口径：只认 success == true。
func TestProbeCompleteWithoutSuccessIsFailure(t *testing.T) {
	client := probeServer(t, `{"type":"test_complete"}`)

	result, err := client.Probe(context.Background(), 1, "m", "hi")
	if err == nil {
		t.Fatal("未声明 success 的完成事件应判为失败")
	}
	if result.Success {
		t.Fatal("未声明 success 的完成事件不能记为成功样本")
	}
}

// TestProbeExplicitSuccessIsSuccess 确认正常路径不受影响。
func TestProbeExplicitSuccessIsSuccess(t *testing.T) {
	client := probeServer(t,
		`{"type":"content","text":"hi"}`,
		`{"type":"test_complete","success":true}`,
	)

	result, err := client.Probe(context.Background(), 1, "m", "hi")
	if err != nil {
		t.Fatalf("明确成功的探测不该报错: %v", err)
	}
	if !result.Success {
		t.Fatal("success=true 应判为成功")
	}
}

// TestProbeTTFBMeasuredAtFirstContent 确认首字时间取自第一个 content 事件，
// 而不是整条流结束的时间。
//
// 顺带守住一个坑：TTFBMs 不能拿 0 兼职表达「还没收到 content」。本机测试里
// 首字时间常常真的是 0ms，用零值判断会把它当成没测到，退化成总耗时。
func TestProbeTTFBMeasuredAtFirstContent(t *testing.T) {
	// 内容之后再压 120ms 才结束，TTFB 与总耗时必须能区分开。
	client := probeServerDelayed(t, 60*time.Millisecond,
		`{"type":"content","text":"hi"}`,
		`{"type":"content","text":"there"}`,
		`{"type":"test_complete","success":true}`,
	)

	result, err := client.Probe(context.Background(), 1, "m", "hi")
	if err != nil {
		t.Fatalf("探测不该报错: %v", err)
	}
	if result.TTFBMs >= result.DurationMs {
		t.Fatalf("首字时间 %dms 应明显小于总耗时 %dms", result.TTFBMs, result.DurationMs)
	}
}

// TestProbeZeroTTFBStillCountsAsMeasured 覆盖首字时间恰为 0ms 的情况：
// 不能因为值是 0 就退化成总耗时。
func TestProbeZeroTTFBStillCountsAsMeasured(t *testing.T) {
	// content 立刻返回（本机 0ms），随后压 150ms 再结束。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content\",\"text\":\"hi\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"type\":\"test_complete\",\"success\":true}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)
	client := New(server.URL, "k", 5*time.Second)

	result, err := client.Probe(context.Background(), 1, "m", "hi")
	if err != nil {
		t.Fatalf("探测不该报错: %v", err)
	}
	if result.DurationMs < 100 {
		t.Fatalf("测试前提不成立：总耗时 %dms 应包含那 150ms 等待", result.DurationMs)
	}
	if result.TTFBMs > 50 {
		t.Fatalf("首字时间 %dms 被错误地退化成了总耗时 %dms",
			result.TTFBMs, result.DurationMs)
	}
}

// TestProbeErrorNotOverriddenByLaterComplete 确认失败是粘性的。
//
// 上游若在 error 之后又发一个 test_complete（重试逻辑、多段流、代理拼接都可能
// 造成这种序列），不能把已经确认的失败翻转成成功。
func TestProbeErrorNotOverriddenByLaterComplete(t *testing.T) {
	client := probeServer(t,
		`{"type":"error","error":"401 Unauthorized"}`,
		`{"type":"test_complete","success":true}`,
	)

	result, err := client.Probe(context.Background(), 1, "m", "hi")
	if err == nil {
		t.Fatal("出现过 error 事件的探测应判为失败")
	}
	if result.Success {
		t.Fatal("error 之后的 test_complete 不能把失败翻转成成功")
	}
	if result.Message != "401 Unauthorized" {
		t.Fatalf("应保留原始错误信息，实际 %q", result.Message)
	}
}

// TestProbeCompleteWithErrorTextIsFailure 覆盖「同时带 success 与 error」的情况：
// 有错误信息就是失败，不管 success 写了什么。
func TestProbeCompleteWithErrorTextIsFailure(t *testing.T) {
	client := probeServer(t, `{"type":"test_complete","success":true,"error":"quota exceeded"}`)

	result, err := client.Probe(context.Background(), 1, "m", "hi")
	if err == nil {
		t.Fatal("带错误信息的完成事件应判为失败")
	}
	if result.Success {
		t.Fatal("带错误信息的完成事件不能记为成功")
	}
}

// TestProbeStreamEndsWithoutResultIsFailure 确认空流不算成功。
func TestProbeStreamEndsWithoutResultIsFailure(t *testing.T) {
	client := probeServer(t, `{"type":"test_start","model":"m"}`)

	result, err := client.Probe(context.Background(), 1, "m", "hi")
	if err == nil {
		t.Fatal("没有结论的流应判为失败")
	}
	if result.Success {
		t.Fatal("没有结论的流不能记为成功")
	}
}
