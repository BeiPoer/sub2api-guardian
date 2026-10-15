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
	if calls.Load() != 2 {
		t.Fatalf("expected one manual and one scheduled request, got %d", calls.Load())
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
	m, _ := fixtureManager(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "length", "message": map[string]any{"content": strings.Repeat("13, 42, 355, ", 110)}}}})
	})
	if err := m.runDue(); err != nil {
		t.Fatal(err)
	}
	runs, _ := m.store.ModelCheckRuns(1, "", 0, 1)
	if runs[0].Status != "error" || runs[0].Prediction != "" || runs[0].ValidOutputs != 0 {
		t.Fatalf("truncated output was classified: %+v", runs[0])
	}
}
