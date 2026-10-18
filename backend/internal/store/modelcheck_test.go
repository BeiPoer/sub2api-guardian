package store

import (
	"reflect"
	"testing"
	"time"
)

func TestModelCheckHistoryFiltersAndRestartRecovery(t *testing.T) {
	s := openTemp(t)
	settings, err := s.ModelCheckSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Targets = []ModelCheckTarget{{AccountID: 1, GroupID: 2, Model: "gpt-5.4"}}
	if err := s.SaveModelCheckSettings(settings); err != nil {
		t.Fatal(err)
	}
	got, err := s.ModelCheckSettings()
	if err != nil || !reflect.DeepEqual(got, settings) {
		t.Fatalf("settings %+v %v", got, err)
	}
	for i := 0; i < 35; i++ {
		_, err := s.CreateModelCheckRun(ModelCheckRun{ModelCheckTarget: settings.Targets[0], Status: "running", StartedAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
	}
	other, err := s.CreateModelCheckRun(ModelCheckRun{ModelCheckTarget: ModelCheckTarget{AccountID: 2, Model: "claude"}, Status: "error"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ModelCheckRuns(1, "gpt-5.4", 0, 30)
	if err != nil || len(page) != 30 || page[0].ID != other.ID-1 {
		t.Fatalf("first page %d, %v", len(page), err)
	}
	older, err := s.ModelCheckRuns(1, "gpt-5.4", page[29].ID, 30)
	if err != nil || len(older) != 5 || older[0].ID >= page[29].ID {
		t.Fatalf("older page %d %v", len(older), err)
	}
	if err := s.InterruptModelCheckRuns(); err != nil {
		t.Fatal(err)
	}
	page, _ = s.ModelCheckRuns(1, "gpt-5.4", 0, 1)
	if page[0].Status != "interrupted" {
		t.Fatalf("stale run %+v", page[0])
	}
	page[0].Status = "consistent"
	if err := s.FinishModelCheckRun(page[0]); err != nil {
		t.Fatal(err)
	}
	page, _ = s.ModelCheckRuns(1, "gpt-5.4", 0, 1)
	if page[0].Status != "consistent" {
		t.Fatal("result not updated")
	}
	all, _ := s.ModelCheckRuns(0, "", 0, 1)
	if all[0].ID != other.ID || all[0].Status != "error" {
		t.Fatal("unrelated completed result changed")
	}
}

func TestModelCheckTimelinesKeepEachChannelModelHistory(t *testing.T) {
	s := openTemp(t)
	items, err := s.ModelCheckTimelines()
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty timelines: %v %v", items, err)
	}
	for _, target := range []ModelCheckTarget{{AccountID: 1, Model: "gpt"}, {AccountID: 1, Model: "claude"}, {AccountID: 2, Model: "gpt"}} {
		for i := 0; i < 105; i++ {
			if _, err := s.CreateModelCheckRun(ModelCheckRun{ModelCheckTarget: target, Status: "consistent"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	items, err = s.ModelCheckTimelines()
	if err != nil || len(items) != 3 {
		t.Fatalf("timelines: %d %v", len(items), err)
	}
	for _, item := range items {
		if len(item.Runs) != 100 {
			t.Fatalf("%d/%s has %d entries", item.AccountID, item.Model, len(item.Runs))
		}
		for i, run := range item.Runs {
			if run.AccountID != item.AccountID || run.Model != item.Model {
				t.Fatal("mixed channel/model history")
			}
			if i > 0 && run.ID >= item.Runs[i-1].ID {
				t.Fatal("history not newest first")
			}
		}
	}
}
