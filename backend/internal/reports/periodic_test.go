package reports

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

func TestPeriodicCalendarSchedule(t *testing.T) {
	for _, tc := range []struct {
		name, zone, now, last, want string
		weekly                      bool
		weekday, hour               int
	}{
		{"before hour", "UTC", "2026-09-06T08:30:00Z", "", "2026-09-06T09:00:00Z", false, 0, 9},
		{"in hour", "UTC", "2026-09-06T09:30:00Z", "", "2026-09-06T09:30:00Z", false, 0, 9},
		{"manual before hour", "UTC", "2026-09-06T09:00:00Z", "2026-09-06T08:30:00Z", "2026-09-06T09:00:00Z", false, 0, 9},
		{"already executed", "UTC", "2026-09-06T09:30:00Z", "2026-09-06T09:01:00Z", "2026-09-07T09:00:00Z", false, 0, 9},
		{"missed hour", "UTC", "2026-09-06T10:00:00Z", "", "2026-09-07T09:00:00Z", false, 0, 9},
		{"no daily drift", "UTC", "2026-09-07T09:00:00Z", "2026-09-06T09:59:00Z", "2026-09-07T09:00:00Z", false, 0, 9},
		{"week rollover", "UTC", "2026-09-06T23:00:00Z", "", "2026-09-07T09:00:00Z", true, 1, 9},
		{"weekly manual earlier", "UTC", "2026-09-06T09:00:00Z", "2026-09-03T09:00:00Z", "2026-09-06T09:00:00Z", true, 7, 9},
		{"weekly consumed", "UTC", "2026-09-06T09:30:00Z", "2026-09-06T09:00:00Z", "2026-09-13T09:00:00Z", true, 7, 9},
		{"month rollover", "UTC", "2026-09-30T23:59:00Z", "", "2026-10-01T09:00:00Z", false, 0, 9},
		{"year rollover", "UTC", "2026-12-31T23:59:00Z", "", "2027-01-04T09:00:00Z", true, 1, 9},
		{"timezone", "Asia/Shanghai", "2026-09-06T16:30:00Z", "", "2026-09-07T01:00:00Z", true, 1, 9},
		{"DST skipped hour", "America/New_York", "2026-03-08T06:00:00Z", "", "2026-03-09T06:00:00Z", false, 0, 2},
		{"DST next week skipped", "America/New_York", "2026-03-01T08:00:00Z", "", "2026-03-15T06:00:00Z", true, 7, 2},
		{"DST repeated hour", "America/New_York", "2026-11-01T06:15:00Z", "2026-11-01T05:15:00Z", "2026-11-02T06:00:00Z", false, 0, 1},
		{"DST Europe first hour", "Europe/Berlin", "2026-10-24T23:00:00Z", "", "2026-10-25T00:00:00Z", false, 0, 2},
		{"DST second hour eligible", "America/New_York", "2026-11-01T06:15:00Z", "", "2026-11-01T06:15:00Z", false, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := defaultDailyReport()
			report.Enabled, report.StartHour, report.Timezone, report.LastRunAt = true, tc.hour, tc.zone, tc.last
			if tc.weekly {
				report.Type = store.ScheduledReportWeekly
			}
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			got := periodicNextRunAt(report, storedDailyConfig{Weekday: tc.weekday}, now)
			if got != tc.want {
				t.Fatalf("next = %s, want %s", got, tc.want)
			}
			report.Enabled = false
			if got := periodicNextRunAt(report, storedDailyConfig{Weekday: tc.weekday}, now); got != "" {
				t.Fatalf("disabled next = %s", got)
			}
		})
	}
}

func TestPeriodicWindowCalendarBoundaries(t *testing.T) {
	for _, tc := range []struct{ zone, now, weekly, daily string }{
		{"Asia/Shanghai", "2026-09-06T23:00:00+08:00", "2026-08-31T00:00:00+08:00", "2026-09-06T00:00:00+08:00"},
		{"Asia/Shanghai", "2026-09-07T00:00:00+08:00", "2026-09-07T00:00:00+08:00", "2026-09-07T00:00:00+08:00"},
		{"UTC", "2027-01-01T23:00:00Z", "2026-12-28T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"America/New_York", "2026-03-08T23:00:00-04:00", "2026-03-02T00:00:00-05:00", "2026-03-08T00:00:00-05:00"},
	} {
		location, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		now, err := time.Parse(time.RFC3339, tc.now)
		if err != nil {
			t.Fatal(err)
		}
		for reportType, want := range map[store.ScheduledReportType]string{store.ScheduledReportDaily: tc.daily, store.ScheduledReportWeekly: tc.weekly} {
			if got := periodicWindowStart(reportType, now.In(location)).Format(time.RFC3339); got != want {
				t.Fatalf("%s window = %s, want %s", reportType, got, want)
			}
		}
	}
}

func TestPeriodicConfigCompatibilityAndSharedSource(t *testing.T) {
	st := openReportStore(t)
	manager := New(st, upstream.New("https://global.invalid", "key", time.Second))
	view, err := manager.PeriodicView()
	if err != nil || view.Daily.Config.RunHour != 23 || view.Weekly.Config.RunHour != 9 || view.Weekly.Config.Weekday != 1 || view.Weekly.Config.Enabled {
		t.Fatalf("defaults: %+v %v", view, err)
	}
	if _, exists, _ := st.ScheduledReport(store.ScheduledReportWeekly); exists {
		t.Fatal("view wrote weekly report")
	}
	legacy := defaultDailyReport()
	legacy.Timezone = "America/New_York"
	legacy, err = st.SaveScheduledReportConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateScheduledReportRunState(legacy.ID, "2026-09-01T23:00:00Z", "ok", "", ""); err != nil {
		t.Fatal(err)
	}
	view, err = manager.PeriodicView()
	if err != nil || view.Weekly.Config.Timezone != legacy.Timezone {
		t.Fatalf("weekly timezone inheritance: %+v %v", view, err)
	}
	input := periodicTestInput()
	view, err = manager.SavePeriodic(input)
	if err != nil || !view.Daily.Config.Enabled || !view.Weekly.Config.Enabled || view.Daily.Config.WeComTarget != "daily-user" || view.Weekly.Config.WeComTarget != "weekly-user" || view.Daily.Config.LastRunAt != "2026-09-01T23:00:00Z" {
		t.Fatalf("saved: %+v %v", view, err)
	}
	input.Weekly.Weekday = 8
	input.Daily.RunHour = 12
	if _, err := manager.SavePeriodic(input); err == nil {
		t.Fatal("invalid weekday accepted")
	}
	view, _ = manager.PeriodicView()
	if view.Daily.Config.RunHour != 23 {
		t.Fatal("invalid weekly update changed daily")
	}
	if _, err := manager.SavePeriodic(PeriodicSaveInput{}); err == nil {
		t.Fatal("missing schedules accepted")
	}
	input = periodicTestInput()
	input.Weekly.WeComTarget = "one\ntwo"
	if _, err := manager.SavePeriodic(input); err == nil {
		t.Fatal("newline accepted")
	}
	channelInput := validSaveInput(1)
	channelInput.WeComTarget = "one\rtwo"
	if _, err := manager.Save(channelInput); err == nil {
		t.Fatal("channel newline accepted")
	}
	catalog, err := manager.SaveSourceSettings(SourceSaveInput{Name: "shared", SourceType: store.ScheduledReportSourceSub2API, BaseURL: "https://shared.invalid", Credential: "key"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := catalog.Items[len(catalog.Items)-1].ID
	if _, err := manager.SaveDaily(DailySaveInput{SourceID: sourceID, Enabled: true, RunHour: 22, Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	view, err = manager.PeriodicView()
	if err != nil || view.SourceID != sourceID || view.Weekly.Config.SourceID != sourceID || view.Weekly.Config.WeComTarget != "weekly-user" || view.Daily.Config.WeComTarget != "daily-user" {
		t.Fatalf("legacy compatibility: %+v %v", view, err)
	}
	if _, err := manager.DeleteSourceSettings(sourceID); err == nil || !strings.Contains(err.Error(), "每周报告") {
		t.Fatalf("shared source deleted: %v", err)
	}
	empty := "   "
	if _, err := manager.SaveDaily(DailySaveInput{Enabled: true, RunHour: 22, Timezone: "UTC", WeComTarget: &empty}); err != nil {
		t.Fatal(err)
	}
	view, _ = manager.PeriodicView()
	if view.Daily.Config.WeComTarget != "" || view.Weekly.Config.WeComTarget != "weekly-user" {
		t.Fatalf("clear daily target: %+v", view)
	}
}

func periodicTestInput() PeriodicSaveInput {
	return PeriodicSaveInput{
		SourceID: "global",
		Daily:    PeriodicScheduleInput{Enabled: true, RunHour: 23, Timezone: "UTC", WeComTarget: " daily-user "},
		Weekly:   PeriodicScheduleInput{Enabled: true, RunHour: 9, Timezone: "UTC", Weekday: 1, WeComTarget: "weekly-user"},
	}
}

func TestAllReportsUseEffectiveNotificationTarget(t *testing.T) {
	for _, reportType := range []store.ScheduledReportType{store.ScheduledReportChannelUsage, store.ScheduledReportDaily, store.ScheduledReportWeekly} {
		t.Run(string(reportType), func(t *testing.T) {
			var targets, messages []string
			var queryFail, sendFail bool
			wecomServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cgi-bin/gettoken" {
					_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "access_token": "token", "expires_in": 7200})
					return
				}
				var payload struct {
					Target string `json:"touser"`
					Text   struct {
						Content string `json:"content"`
					} `json:"text"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				targets = append(targets, payload.Target)
				messages = append(messages, payload.Text.Content)
				if sendFail {
					_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 40003, "errmsg": "invalid user"})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "msgid": "1"})
			}))
			defer wecomServer.Close()
			sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if queryFail {
					http.Error(w, "query rejected", http.StatusBadRequest)
					return
				}
				var data any
				switch r.URL.Path {
				case "/api/v1/admin/usage":
					record := map[string]any{"created_at": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "first_token_ms": 40000}
					data = map[string]any{"items": []any{record, record}, "pages": 1}
				case "/api/v1/admin/usage/stats":
					data = map[string]any{"total_actual_cost": 12.5, "total_tokens": 1000}
				default:
					data = map[string]any{"items": []any{}, "pages": 1}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
			}))
			defer sourceServer.Close()
			manager := New(openReportStore(t), upstream.New(sourceServer.URL, "source-key", time.Second))
			manager.wecom.SetBaseURL(wecomServer.URL)
			for _, tc := range []struct {
				name, target, globalTarget, want string
				disabled, queryFail, sendFail    bool
			}{
				{name: "override", target: " alice|bob ", globalTarget: "global-user", want: "alice|bob"},
				{name: "clear", target: "  ", globalTarget: "global-user", want: "global-user"},
				{name: "changed global", globalTarget: "new-global", want: "new-global"},
				{name: "disabled", target: "alice", globalTarget: "global-user", disabled: true},
				{name: "query failure override", target: "failure-user", globalTarget: "global-user", want: "failure-user", queryFail: true},
				{name: "query failure fallback", globalTarget: "fallback-user", want: "fallback-user", queryFail: true},
				{name: "send failure", target: "missing-user", globalTarget: "global-user", want: "missing-user", sendFail: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					queryFail, sendFail = tc.queryFail, tc.sendFail
					notification := validNotificationInput()
					notification.WeCom.Enabled, notification.WeCom.Target = !tc.disabled, tc.globalTarget
					if _, err := manager.SaveNotificationSettings(notification); err != nil {
						t.Fatal(err)
					}
					input := periodicTestInput()
					input.Daily.WeComTarget, input.Weekly.WeComTarget = tc.target, tc.target
					if _, err := manager.SavePeriodic(input); err != nil {
						t.Fatal(err)
					}
					channelInput := validSaveInput(1)
					channelInput.WeComTarget = tc.target
					if view, err := manager.Save(channelInput); err != nil || view.Config.WeComTarget != strings.TrimSpace(tc.target) {
						t.Fatalf("channel save: %+v %v", view, err)
					}
					before := len(targets)
					var run store.ScheduledReportRun
					var err error
					if reportType == store.ScheduledReportChannelUsage {
						run, err = manager.RunNow(context.Background())
					} else {
						run, err = manager.RunPeriodicNow(context.Background(), reportType)
					}
					if err != nil {
						t.Fatal(err)
					}
					wantStatus := "sent"
					if tc.disabled {
						wantStatus = "disabled"
					} else if tc.sendFail {
						wantStatus = "failed"
					}
					if run.NotificationStatus != wantStatus {
						t.Fatalf("run: %+v", run)
					}
					if tc.queryFail && run.Status != "error" {
						t.Fatalf("query failure not recorded: %+v", run)
					}
					if tc.disabled {
						if len(targets) != before {
							t.Fatal("disabled notification sent")
						}
					} else if len(targets) != before+1 || targets[before] != tc.want {
						t.Fatalf("targets = %v, wanted one message to %s", targets[before:], tc.want)
					}
					if reportType == store.ScheduledReportWeekly && !tc.queryFail {
						start, _ := time.Parse(time.RFC3339Nano, run.WindowStart)
						end, _ := time.Parse(time.RFC3339Nano, run.WindowEnd)
						summary := run.Summary.(DailyReportSummary)
						if start.Weekday() != time.Monday || summary.Date != start.Format("2006-01-02")+" 至 "+end.Format("2006-01-02") {
							t.Fatalf("weekly summary: %+v", run)
						}
						if !tc.disabled && (!strings.Contains(messages[before], "每周报告") || !strings.Contains(messages[before], "本周消耗额度：12.50")) {
							t.Fatalf("weekly text: %s", messages[before])
						}
					}
					global, err := manager.NotificationSettings()
					if err != nil || global.WeCom.Target != tc.globalTarget {
						t.Fatalf("global config overwritten: %+v %v", global, err)
					}
				})
			}
		})
	}
}

func TestPeriodicSchedulerRunsBothAndDoesNotRepeatAfterRestart(t *testing.T) {
	st := openReportStore(t)
	manager := New(st, upstream.New("", "", time.Second))
	input := periodicTestInput()
	now := time.Now().UTC()
	input.Daily.RunHour, input.Weekly.RunHour = now.Hour(), now.Hour()
	input.Weekly.Weekday = (int(now.Weekday())+6)%7 + 1
	if _, err := manager.SavePeriodic(input); err != nil {
		t.Fatal(err)
	}
	manager.runDue(context.Background())
	manager.runDue(context.Background())
	New(st, upstream.New("", "", time.Second)).runDue(context.Background())
	for _, reportType := range []store.ScheduledReportType{store.ScheduledReportDaily, store.ScheduledReportWeekly} {
		items, total, _, _, _, err := manager.PeriodicRuns(reportType, 1, 20)
		if err != nil || total != 1 || len(items) != 1 || items[0].Status != "error" {
			t.Fatalf("%s should record exactly one failed scheduled attempt: total=%d items=%+v err=%v", reportType, total, items, err)
		}
	}
}
