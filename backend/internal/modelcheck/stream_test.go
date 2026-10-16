package modelcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/upstream"
)

const chatStream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"999\",\"content\":\"12, \"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"34, 56\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"completion_tokens\":123}}\n\ndata: [DONE]\n\n"

const responsesStream = "event: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"999\"}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"12, \"}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"34, 56\"}\n\n" +
	"data: {\"type\":\"response.output_text.done\",\"text\":\"12, 34, 56\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"12, 34, 56\"}]}]}}\n\n"

const messagesStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"stop_reason\":null}}\n\n" +
	"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"999\"}}\n\n" +
	"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"12, \"}}\n\n" +
	"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"34, 56\"}}\n\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

func TestStreamRequestsAndCompletionBeforeConnectionClose(t *testing.T) {
	for _, test := range []struct{ protocol, stream string }{{"chat/completions", chatStream}, {"responses", responsesStream}, {"messages", messagesStream}} {
		t.Run(test.protocol, func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["stream"] != true || r.Header.Get("Accept") != "text/event-stream" {
					t.Error("request must enable streaming")
				}
				for _, key := range []string{"max_completion_tokens", "max_output_tokens"} {
					if _, ok := body[key]; ok {
						t.Errorf("OpenAI limit %s still present", key)
					}
				}
				if test.protocol == "messages" {
					if body["max_tokens"] != float64(4096) {
						t.Error("Anthropic max_tokens missing")
					}
				} else if _, ok := body["max_tokens"]; ok {
					t.Error("OpenAI max_tokens must be omitted")
				}
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				fmt.Fprint(w, ": keepalive\n\n"+test.stream)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			output, status, err := requestOutput(ctx, server.Client(), upstream.AccountCredentials{BaseURL: server.URL, APIKey: "test"}, "model", "prompt", test.protocol)
			if err != nil || status != 200 || strings.TrimSpace(output) != "12, 34, 56" {
				t.Fatalf("stream result: %q %d %v", output, status, err)
			}
		})
	}
}

func TestStreamFramingAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, protocol, stream string
		want                   string
		failed, invalid        bool
	}{
		{"CRLF", "chat/completions", strings.ReplaceAll(chatStream, "\n", "\r\n"), "12, 34, 56", false, false},
		{"multiline data", "chat/completions", "event: message\ndata: {\"choices\":\ndata: [{\"delta\":{\"content\":\"12, 34, 56\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]", "12, 34, 56", false, false},
		{"EOF after explicit stop", "chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":\"12, 34, 56\"},\"finish_reason\":\"stop\"}]}\n\n", "12, 34, 56", false, false},
		{"missing chat stop", "chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":\"12, 34, 56\"}}]}\n\n", "", true, false},
		{"DONE is not success", "chat/completions", "data: [DONE]\n\n", "", true, false},
		{"late chat error", "chat/completions", strings.Replace(chatStream, "data: [DONE]", "data: {\"error\":{\"message\":\"private-key\"}}", 1), "", true, false},
		{"chat length", "chat/completions", strings.Replace(chatStream, "\"stop\"", "\"length\"", 1), "", true, true},
		{"chat filter", "chat/completions", strings.Replace(chatStream, "\"stop\"", "\"content_filter\"", 1), "", true, true},
		{"malformed data", "responses", "data: not-json\n\n", "", true, false},
		{"response incomplete", "responses", "data: {\"type\":\"response.incomplete\"}\n\n", "", true, true},
		{"response failed", "responses", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"private-key\"}}}\n\n", "", true, false},
		{"response missing completion", "responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"12\"}\n\n", "", true, false},
		{"anthropic overload", "messages", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"private-key\"}}\n\n", "", true, false},
		{"anthropic truncation", "messages", strings.Replace(messagesStream, "end_turn", "max_tokens", 1), "", true, true},
		{"anthropic missing stop", "messages", strings.Replace(messagesStream, "data: {\"type\":\"message_stop\"}\n\n", "", 1), "", true, false},
		{"oversized stream", "responses", strings.Repeat(": ping\n", 400000), "", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			text, err := readStreamOutput(strings.NewReader(test.stream), test.protocol)
			if (err != nil) != test.failed || errors.Is(err, errInvalidOutput) != test.invalid || text != test.want {
				t.Fatalf("got %q %v", text, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-key") {
				t.Fatal("stream error leaked upstream data")
			}
		})
	}
}

func TestInterruptedStreamRetriesWithoutKeepingPartialAnswer(t *testing.T) {
	var attempts atomic.Int32
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		text := strings.Repeat("13, 42, 355, ", 110)
		if attempts.Add(1) == 1 {
			text = strings.Repeat("99, ", 400)
		}
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}})
		fmt.Fprintf(w, "data: %s\n\n", raw)
		if attempts.Load() > 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	})
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	runs, err := m.store.ModelCheckRuns(1, "", 0, 1)
	if err != nil || len(runs) != 1 || calls.Load() != 4 || runs[0].ValidOutputs != 3 {
		t.Fatalf("retry result: %d %+v %v", calls.Load(), runs, err)
	}
	for _, count := range runs[0].NumberCounts {
		if count != 330 {
			t.Fatalf("partial answer entered fingerprint: %v", runs[0].NumberCounts)
		}
	}
}

func TestCancelWhileReadingStream(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"12, \"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := requestOutput(ctx, server.Client(), upstream.AccountCredentials{BaseURL: server.URL, APIKey: "test"}, "model", "prompt", "chat/completions")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled stream accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("stream cancellation blocked")
	}
}
