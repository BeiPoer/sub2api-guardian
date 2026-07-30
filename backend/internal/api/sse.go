package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"sub2api-guardian/backend/internal/store"
)

// hub 是极简的 SSE 广播器：状态变化时通知所有已连接的前端刷新。
type hub struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
	closed  bool
}

func newHub() *hub {
	return &hub{clients: map[chan struct{}]struct{}{}}
}

func (h *hub) subscribe() chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	ch := make(chan struct{}, 1)
	h.clients[ch] = struct{}{}
	return ch
}

func (h *hub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
}

// broadcast 通知所有订阅者。已有待处理通知的订阅者会被跳过（合并通知）。
func (h *hub) broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.clients {
		delete(h.clients, ch)
		close(ch)
	}
}

// stream 是 SSE 端点：引擎每完成一轮调度就推送一次 tick，前端据此拉取最新数据。
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "当前服务器不支持流式响应"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	notify := s.hub.subscribe()
	if notify == nil {
		return
	}
	defer s.hub.unsubscribe(notify)

	writeSSE(w, flusher, "status", s.engine.Status())

	// 心跳兼顾两个目的：保持连接活跃，并在没有事件时定期同步状态。
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case _, alive := <-notify:
			if !alive {
				return
			}
			writeSSE(w, flusher, "tick", s.engine.Status())
		case <-heartbeat.C:
			writeSSE(w, flusher, "ping", s.engine.Status())
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("event: " + event + "\ndata: "))
	_, _ = w.Write(raw)
	_, _ = w.Write([]byte("\n\n"))
	flusher.Flush()
}

func store2EventFilter(page, pageSize int) store.EventFilter {
	return store.EventFilter{Page: page, PageSize: pageSize}
}
