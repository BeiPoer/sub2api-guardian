package modelcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSettingsAndQuietHours(t *testing.T) {
	st := testStore(t)
	v, err := st.ModelCheckSettings()
	if err != nil || v.IntervalMinutes != 60 || v.QuietStart != "23:00" || v.QuietEnd != "08:00" || v.Timezone != "Asia/Shanghai" || v.Enabled {
		t.Fatalf("defaults: %+v, %v", v, err)
	}
	if err := validateSettings(v); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		stamp string
		quiet bool
	}{
		{"2026-10-15T14:59:59Z", false},
		{"2026-10-15T15:00:00Z", true},
		{"2026-10-15T23:59:59Z", true},
		{"2026-10-16T00:00:00Z", false},
	} {
		now, _ := time.Parse(time.RFC3339, test.stamp)
		if got := quietAt(v, now); got != test.quiet {
			t.Fatalf("quiet at %s: %v", test.stamp, got)
		}
	}
	v.QuietStart = "12:00"
	v.QuietEnd = "13:00"
	for _, hour := range []int{11, 12, 13} {
		now := time.Date(2026, 10, 15, hour-8, 0, 0, 0, time.UTC)
		if quietAt(v, now) != (hour == 12) {
			t.Fatal("same-day quiet window")
		}
	}
	for _, mutate := range []func(*store.ModelCheckSettings){
		func(v *store.ModelCheckSettings) { v.IntervalMinutes = 0 },
		func(v *store.ModelCheckSettings) { v.QuietEnd = v.QuietStart },
		func(v *store.ModelCheckSettings) { v.QuietStart = "8:00" },
		func(v *store.ModelCheckSettings) { v.Timezone = "unknown/place" },
		func(v *store.ModelCheckSettings) { v.Enabled = true },
		func(v *store.ModelCheckSettings) {
			v.Targets = []store.ModelCheckTarget{{AccountID: 1, Model: "gpt", ExpectedModel: "invented"}}
		},
		func(v *store.ModelCheckSettings) {
			v.Targets = []store.ModelCheckTarget{{AccountID: 1, Model: "gpt"}, {AccountID: 1, Model: "gpt"}}
		},
	} {
		invalid := v
		mutate(&invalid)
		if validateSettings(invalid) == nil {
			t.Fatalf("accepted invalid settings %+v", invalid)
		}
	}
}

func fixtureManager(t *testing.T, outputHandler http.HandlerFunc) (*Manager, *atomic.Int32) {
	t.Helper()
	st := testStore(t)
	if err := st.UpsertAccount(domain.Account{ID: 1, Name: "Fixture", Type: "apikey", Platform: "openai", GroupIDs: []int64{7}}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/accounts/data" {
			id, err := strconv.ParseInt(r.URL.Query().Get("ids"), 10, 64)
			if r.Header.Get("x-api-key") != "admin-secret" || err != nil || (id != 1 && id != 2) {
				t.Error("wrong credential export request")
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"accounts": []any{map[string]any{"id": id, "type": "apikey", "credentials": map[string]any{"api_key": "channel-secret", "base_url": server.URL + "/v1"}}}}})
			return
		}
		calls.Add(1)
		outputHandler(w, r)
	}))
	t.Cleanup(server.Close)
	m := New(st, upstream.New(server.URL, "admin-secret", time.Second))
	m.waitRetry = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	t.Cleanup(m.Stop)
	v, _ := st.ModelCheckSettings()
	v.Enabled = true
	v.QuietEnabled = false
	v.Targets = []store.ModelCheckTarget{{AccountID: 1, GroupID: 7, Model: "gpt-5.4", ExpectedModel: "gpt-5.4"}}
	if err := m.Save(v); err != nil {
		t.Fatal(err)
	}
	return m, &calls
}

func TestScheduledRunPersistsAndDoesNotRepeatBeforeInterval(t *testing.T) {
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer channel-secret" {
			t.Error("wrong direct request")
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "gpt-5.4" || len(payload.Messages) != 1 || !strings.Contains(payload.Messages[0].Content, "355") {
			t.Error("missing challenge")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": strings.Repeat("13, 42, 355, ", 110)}}}})
	})
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", calls.Load())
	}
	runs, err := m.store.ModelCheckRuns(1, "gpt-5.4", 0, 30)
	if err != nil || len(runs) != 1 || runs[0].ValidOutputs != 3 || runs[0].FinishedAt == nil || runs[0].Prediction == "" {
		t.Fatalf("result: %+v %v", runs, err)
	}
	raw, _ := json.Marshal(runs)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("credential leak")
	}
	// A new manager must use the persisted last run, rather than run immediately after restart.
	restarted := New(m.store, m.client)
	defer restarted.Stop()
	if err := restarted.runDue(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal("restart repeated recent run")
	}
	if err := m.RunNow(store.ModelCheckTarget{AccountID: 1, Model: "unsaved"}); err == nil {
		t.Fatal("unsaved target accepted")
	}
}

func TestFailureAndInvalidOutputsAreNotModelMismatch(t *testing.T) {
	for _, status := range []int{200, 401} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			m, _ := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"choices":[{"message":{"content":"Cannot comply channel-secret"}}]}`)
			})
			if err := m.runDue(); err != nil {
				t.Fatal(err)
			}
			runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
			want := "inconclusive"
			if status == 401 {
				want = "error"
			}
			if runs[0].Status != want || strings.Contains(runs[0].Message, "secret") {
				t.Fatalf("bad result %+v", runs[0])
			}
		})
	}
}

func TestStopCancelsRequestAndDuplicateTargetIsRejected(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	m, _ := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	v, _ := m.store.ModelCheckSettings()
	v.Enabled = false
	v.QuietEnabled = true
	if err := m.Save(v); err != nil {
		t.Fatal(err)
	}
	if err := m.RunNow(v.Targets[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	if err := m.RunNow(v.Targets[0]); !errors.Is(err, ErrRunning) {
		t.Fatalf("overlap accepted: %v", err)
	}
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not cancel")
	}
	runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
	if runs[0].Status != "interrupted" {
		t.Fatalf("run not interrupted: %+v", runs[0])
	}
	if err := m.RunNow(v.Targets[0]); err == nil {
		t.Fatal("run after Stop accepted")
	}
}

func TestDifferentTargetsRunConcurrentlyAndStopCancelsAll(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	defer close(release)
	m, _ := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	if err := m.store.UpsertAccount(domain.Account{ID: 2, Name: "Other", Type: "apikey", Platform: "openai", GroupIDs: []int64{7}}); err != nil {
		t.Fatal(err)
	}
	v, _ := m.store.ModelCheckSettings()
	v.Targets = append(v.Targets, store.ModelCheckTarget{AccountID: 1, GroupID: 7, Model: "gpt-5.5"}, store.ModelCheckTarget{AccountID: 2, GroupID: 7, Model: "gpt-5.4"})
	if err := m.Save(v); err != nil {
		t.Fatal(err)
	}
	for _, target := range v.Targets {
		if err := m.RunNow(target); err != nil {
			t.Fatalf("start %+v: %v", target, err)
		}
	}
	for range v.Targets {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("different targets did not reach upstream concurrently")
		}
	}
	if len(m.RunningTargets()) != 3 {
		t.Fatal("missing active targets")
	}
	for _, target := range v.Targets {
		if err := m.RunNow(target); !errors.Is(err, ErrRunning) {
			t.Fatalf("duplicate target accepted: %v", err)
		}
	}
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not cancel all targets")
	}
	if len(m.RunningTargets()) != 0 {
		t.Fatal("active targets not cleared")
	}
	for _, target := range v.Targets {
		runs, err := m.store.ModelCheckRuns(target.AccountID, target.Model, 0, 30)
		if err != nil || len(runs) != 1 || runs[0].Status != "interrupted" {
			t.Fatalf("history %+v: %+v %v", target, runs, err)
		}
	}
}

func TestScheduleSkipsRunningTargetAndRunsOtherModel(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if payload.Model == "gpt-5.4" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	v, _ := m.store.ModelCheckSettings()
	other := store.ModelCheckTarget{AccountID: 1, GroupID: 7, Model: "gpt-5.5"}
	v.Targets = append(v.Targets, other)
	if err := m.Save(v); err != nil {
		t.Fatal(err)
	}
	if err := m.RunNow(v.Targets[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("manual run not started")
	}
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 {
		t.Fatalf("expected one manual request and four scheduled attempts, got %d", calls.Load())
	}
	active := m.RunningTargets()
	if len(active) != 1 || active[0] != v.Targets[0] {
		t.Fatalf("completion cleared another target: %+v", active)
	}
	runs, _ := m.store.ModelCheckRuns(other.AccountID, other.Model, 0, 30)
	if len(runs) != 1 || runs[0].Status != "error" {
		t.Fatalf("other target did not finish: %+v", runs)
	}
	if err := m.RunNow(other); err != nil {
		t.Fatalf("failed target stayed locked: %v", err)
	}
	if err := m.RunNow(v.Targets[0]); !errors.Is(err, ErrRunning) {
		t.Fatalf("manual target unlocked: %v", err)
	}
}

func TestSavedTargetMustStillBelongToPool(t *testing.T) {
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) { t.Error("removed pool target must not be called") })
	if err := m.store.UpsertAccount(domain.Account{ID: 1, Name: "Moved", Type: "apikey", GroupIDs: []int64{8}}); err != nil {
		t.Fatal(err)
	}
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("called removed target")
	}
	runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
	if runs[0].Status != "error" {
		t.Fatalf("missing explicit failure: %+v", runs[0])
	}
}

func TestRequestProtocolsAndErrorRedaction(t *testing.T) {
	for _, protocol := range []string{"chat/completions", "messages", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/prefix/v1/"+protocol {
					t.Errorf("path %s", r.URL.Path)
				}
				if protocol == "messages" && (r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") == "") {
					t.Error("anthropic auth")
				}
				switch protocol {
				case "chat/completions":
					fmt.Fprint(w, `{"choices":[{"message":{"content":"1, 2, 3"}}]}`)
				case "messages":
					fmt.Fprint(w, `{"content":[{"type":"thinking","text":"999"},{"type":"text","text":"1, 2, 3"}]}`)
				case "responses":
					fmt.Fprint(w, `{"output":[{"type":"reasoning","content":[{"type":"output_text","text":"999"}]},{"type":"message","content":[{"type":"output_text","text":"1, 2, 3"}]}]}`)
				}
			}))
			defer server.Close()
			text, _, err := requestOutput(context.Background(), server.Client(), upstream.AccountCredentials{BaseURL: server.URL + "/prefix/v1/", APIKey: "secret"}, "model", "challenge", protocol)
			if err != nil || text != "1, 2, 3" {
				t.Fatalf("response %q, %v", text, err)
			}
		})
	}
}

func TestDisablingPlanStopsRemainingChallenges(t *testing.T) {
	var manager *Manager
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		settings, err := manager.store.ModelCheckSettings()
		if err != nil {
			t.Error(err)
			return
		}
		settings.Enabled = false
		if err := manager.Save(settings); err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": strings.Repeat("13, 42, 355, ", 110)}}}})
	})
	manager = m
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("issued %d calls after disabling", calls.Load())
	}
	runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
	if runs[0].Status != "interrupted" {
		t.Fatalf("run not interrupted: %+v", runs[0])
	}
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("disabled schedule ran again")
	}
}

func TestQuietScheduleDoesNotCallUpstream(t *testing.T) {
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) { t.Error("quiet schedule called upstream") })
	v, _ := m.store.ModelCheckSettings()
	now := time.Now().UTC()
	v.QuietEnabled = true
	v.Timezone = "UTC"
	v.QuietStart = now.Add(-time.Minute).Format("15:04")
	v.QuietEnd = now.Add(2 * time.Minute).Format("15:04")
	if err := m.Save(v); err != nil {
		t.Fatal(err)
	}
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	runs, _ := m.store.ModelCheckRuns(0, "", 0, 30)
	if calls.Load() != 0 || len(runs) != 0 {
		t.Fatal("quiet schedule ran")
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	m := New(nil, nil)
	defer m.Stop()
	_, status, err := requestOutput(context.Background(), m.http, upstream.AccountCredentials{BaseURL: origin.URL, APIKey: "private"}, "model", "prompt", "messages")
	if err == nil || status != 307 || leaked.Load() {
		t.Fatalf("redirect leaked credentials: status=%d err=%v", status, err)
	}
}

func TestTruncatedOutputIsNotAnalyzed(t *testing.T) {
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "length", "message": map[string]any{"content": strings.Repeat("13, 42, 355, ", 110)}}}})
	})
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
	if runs[0].Status != "error" || runs[0].Prediction != "" || runs[0].ValidOutputs != 0 {
		t.Fatalf("truncated output was classified: %+v", runs[0])
	}
	if calls.Load() != 1 {
		t.Fatal("truncation should not be retried as an interface error")
	}
}

func TestRequestRetryDelaysAndRunningState(t *testing.T) {
	for _, failures := range []int{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprintf("failures_%d", failures), func(t *testing.T) {
			var attempts atomic.Int32
			var manager *Manager
			var prompts []string
			m, _ := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				prompts = append(prompts, payload.Messages[0].Content)
				runs, err := manager.store.ModelCheckRuns(1, "", 0, 30)
				if err != nil || len(runs) != 1 || runs[0].Status != "running" || runs[0].FinishedAt != nil || len(manager.RunningTargets()) != 1 {
					t.Errorf("not running during request: %+v %v", runs, err)
				}
				if attempts.Add(1) <= int32(failures) {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": strings.Repeat("13, 42, 355, ", 110)}}}})
			})
			manager = m
			var delays []time.Duration
			m.waitRetry = func(ctx context.Context, delay time.Duration) error {
				delays = append(delays, delay)
				runs, err := m.store.ModelCheckRuns(1, "", 0, 30)
				if err != nil || len(runs) != 1 || runs[0].Status != "running" || runs[0].FinishedAt != nil || !strings.Contains(runs[0].Message, fmt.Sprintf("%d 秒后进行第 %d/3 次重试", int(delay/time.Second), len(delays))) {
					t.Fatalf("not running during wait: %+v %v", runs, err)
				}
				active := m.RunningTargets()
				if len(active) != 1 {
					t.Fatal("target unlocked while waiting")
				}
				if err := m.RunNow(active[0]); !errors.Is(err, ErrRunning) {
					t.Fatalf("duplicate during retry: %v", err)
				}
				return nil
			}
			if err := m.runDue(); err != nil {
				t.Fatal(err)
			}
			wantDelays := []time.Duration{3 * time.Second, 10 * time.Second, 30 * time.Second}[:min(failures, 3)]
			if len(delays) != len(wantDelays) || (len(delays) > 0 && !reflect.DeepEqual(delays, wantDelays)) {
				t.Fatalf("delays %v want %v", delays, wantDelays)
			}
			runs, err := m.store.ModelCheckRuns(1, "", 0, 30)
			if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil || len(m.RunningTargets()) != 0 {
				t.Fatalf("final record: %+v %v", runs, err)
			}
			if failures == 4 {
				if attempts.Load() != 4 || runs[0].Status != "error" || !strings.Contains(runs[0].Message, "已重试 3 次") {
					t.Fatalf("retry exhaustion: calls=%d %+v", attempts.Load(), runs[0])
				}
			} else if attempts.Load() != int32(failures+3) || runs[0].ValidOutputs != 3 || runs[0].Prediction == "" {
				t.Fatalf("recovery: calls=%d %+v", attempts.Load(), runs[0])
			}
			for i := 1; i < min(failures+1, 4); i++ {
				if prompts[i] != prompts[0] {
					t.Fatal("retry changed challenge prompt")
				}
			}
		})
	}
}

func TestRetryKeepsAlreadySuccessfulChallenges(t *testing.T) {
	var calls atomic.Int32
	var manager *Manager
	m, _ := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": strings.Repeat("13, 42, 355, ", 110)}}}})
	})
	manager = m
	m.waitRetry = func(ctx context.Context, delay time.Duration) error {
		runs, err := manager.store.ModelCheckRuns(1, "", 0, 1)
		if err != nil || runs[0].ValidOutputs != 1 || len(runs[0].NumberCounts) != 1 || !strings.Contains(runs[0].Message, "第 2/3 条挑战") {
			t.Fatalf("lost first challenge: %+v %v", runs, err)
		}
		return nil
	}
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("successful challenge repeated: %d calls", calls.Load())
	}
	runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
	if runs[0].ValidOutputs != 3 {
		t.Fatal("incomplete result after recovery")
	}
}

func TestStopCancelsRetryWait(t *testing.T) {
	waiting := make(chan struct{})
	m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	m.waitRetry = func(ctx context.Context, delay time.Duration) error {
		close(waiting)
		return waitForRetry(ctx, delay)
	}
	settings, _ := m.store.ModelCheckSettings()
	if err := m.RunNow(settings.Targets[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiting:
	case <-time.After(3 * time.Second):
		t.Fatal("did not reach retry wait")
	}
	done := make(chan struct{})
	go func() { m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel retry wait")
	}
	runs, _ := m.store.ModelCheckRuns(1, "", 0, 30)
	if calls.Load() != 1 || len(runs) != 1 || runs[0].Status != "interrupted" {
		t.Fatalf("continued after cancellation: %d %+v", calls.Load(), runs)
	}
}

func TestScheduleChangeDuringRetryWaitStopsNextRequest(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(fmt.Sprintf("quiet_%t", quiet), func(t *testing.T) {
			m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
			m.waitRetry = func(ctx context.Context, delay time.Duration) error {
				settings, err := m.store.ModelCheckSettings()
				if err != nil {
					return err
				}
				if quiet {
					now := time.Now().UTC()
					settings.QuietEnabled = true
					settings.Timezone = "UTC"
					settings.QuietStart = now.Add(-time.Minute).Format("15:04")
					settings.QuietEnd = now.Add(2 * time.Minute).Format("15:04")
				} else {
					settings.Enabled = false
				}
				return m.Save(settings)
			}
			if err := m.runDue(); err != nil {
				t.Fatal(err)
			}
			runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
			if calls.Load() != 1 || runs[0].Status != "interrupted" {
				t.Fatalf("retry ignored schedule change: %d %+v", calls.Load(), runs)
			}
		})
	}
}

func TestRetryHandlesSuccessfulHTTPWithErrorPayload(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"channel-secret"}}`, `{"status":"failed","error":null}`} {
		m, calls := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if err := m.runDue(); err != nil {
			t.Fatal(err)
		}
		runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
		if calls.Load() != 4 || runs[0].Status != "error" || strings.Contains(runs[0].Message, "channel-secret") {
			t.Fatalf("error payload: calls=%d %+v", calls.Load(), runs)
		}
	}
}

func TestInsufficientNumbersRetriesOnlyCurrentChallenge(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprintf("recover_%t", recover), func(t *testing.T) {
			var calls atomic.Int32
			var prompts []string
			m, _ := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				prompts = append(prompts, payload.Messages[0].Content)
				call := calls.Add(1)
				count := 330
				if call >= 2 && (call <= 4 || (!recover && call == 5)) {
					count = 28
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": strings.Repeat("42, ", count)}}}})
			})
			var delays []time.Duration
			m.waitRetry = func(ctx context.Context, delay time.Duration) error {
				delays = append(delays, delay)
				runs, err := m.store.ModelCheckRuns(1, "", 0, 30)
				if err != nil || len(runs) != 1 || runs[0].Status != "running" || runs[0].FinishedAt != nil || runs[0].ValidOutputs != 1 || !reflect.DeepEqual(runs[0].NumberCounts, []int{330, 28}) {
					t.Fatalf("lost progress during retry: %+v %v", runs, err)
				}
				if !strings.Contains(runs[0].Message, "有效数字不足：提取到 28 个") || !strings.Contains(runs[0].Message, "第 2/3 条挑战") {
					t.Fatalf("unclear retry message: %s", runs[0].Message)
				}
				active := m.RunningTargets()
				if len(active) != 1 {
					t.Fatal("target unlocked during wait")
				}
				if err := m.RunNow(active[0]); !errors.Is(err, ErrRunning) {
					t.Fatalf("duplicate accepted: %v", err)
				}
				return nil
			}
			if err := m.runDue(); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 6 || !reflect.DeepEqual(delays, []time.Duration{3 * time.Second, 10 * time.Second, 30 * time.Second}) {
				t.Fatalf("unexpected attempts: %d %v", calls.Load(), delays)
			}
			for i := 2; i <= 4; i++ {
				if prompts[i] != prompts[1] {
					t.Fatal("retry did not reuse second challenge")
				}
			}
			if prompts[0] == prompts[1] || prompts[5] == prompts[1] {
				t.Fatal("successful challenge repeated")
			}
			runs, err := m.store.ModelCheckRuns(1, "", 0, 30)
			if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil {
				t.Fatalf("final result: %+v %v", runs, err)
			}
			if recover {
				if runs[0].ValidOutputs != 3 || runs[0].Prediction == "" || !reflect.DeepEqual(runs[0].NumberCounts, []int{330, 330, 330}) {
					t.Fatalf("invalid retry output included: %+v", runs[0])
				}
			} else if runs[0].Status != "inconclusive" || runs[0].ValidOutputs != 2 || runs[0].Prediction != "" || !reflect.DeepEqual(runs[0].NumberCounts, []int{330, 28, 330}) || !strings.Contains(runs[0].Message, "已重试 3 次") {
				t.Fatalf("wrong exhausted verdict: %+v", runs[0])
			}
		})
	}
}
