package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/store"
)

func TestModelCheckSettingsAPI(t *testing.T) {
	handler, st := setupAPI(t, &fakeUpstream{groupCount: 1})
	if err := st.UpsertAccount(domain.Account{ID: 101, Type: "apikey", GroupIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	res := doJSON(t, handler, http.MethodGet, "/api/model-check", nil)
	if res.Code != http.StatusOK {
		t.Fatal(res.Body.String())
	}
	var view struct {
		Settings       store.ModelCheckSettings `json:"settings"`
		Models         []string                 `json:"models"`
		Running        bool                     `json:"running"`
		RunningTargets []store.ModelCheckTarget `json:"running_targets"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Models) == 0 || view.Settings.IntervalMinutes != 60 || view.Settings.QuietStart != "23:00" {
		t.Fatalf("wrong defaults %+v", view)
	}
	if view.Running || view.RunningTargets == nil || len(view.RunningTargets) != 0 {
		t.Fatalf("idle running targets: %+v", view)
	}
	view.Settings.Targets = []store.ModelCheckTarget{{AccountID: 101, GroupID: 1, Model: "alias", ExpectedModel: view.Models[0]}}
	res = doJSON(t, handler, http.MethodPut, "/api/model-check", view.Settings)
	if res.Code != http.StatusOK {
		t.Fatal(res.Body.String())
	}
	view.Settings.IntervalMinutes = 0
	res = doJSON(t, handler, http.MethodPut, "/api/model-check", view.Settings)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid interval accepted: %d", res.Code)
	}
	saved, _ := st.ModelCheckSettings()
	if saved.IntervalMinutes != 60 {
		t.Fatal("invalid save overwrote settings")
	}
	res = doJSON(t, handler, http.MethodPost, "/api/model-check/run", store.ModelCheckTarget{AccountID: 101, Model: "unsaved"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unsaved target accepted: %d", res.Code)
	}
	res = doJSON(t, handler, http.MethodGet, "/api/model-check/runs?account_id=101&model=alias", nil)
	if res.Code != http.StatusOK || res.Body.String() != "{\"items\":[]}\n" {
		t.Fatalf("empty timeline: %s", res.Body.String())
	}
	if _, err := st.CreateModelCheckRun(store.ModelCheckRun{ModelCheckTarget: saved.Targets[0], Status: "consistent"}); err != nil {
		t.Fatal(err)
	}
	res = doJSON(t, handler, http.MethodGet, "/api/model-check/timelines", nil)
	var timelines struct {
		Items []store.ModelCheckTimeline `json:"items"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &timelines); err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || len(timelines.Items) != 1 || len(timelines.Items[0].Runs) != 1 || timelines.Items[0].Model != "alias" {
		t.Fatalf("timelines response: %s", res.Body.String())
	}
}

func TestModelCheckHistoryReclassifiesExpectedModelWithoutNewProbe(t *testing.T) {
	handler, st := setupAPI(t, &fakeUpstream{groupCount: 1})
	legacy := store.ModelCheckRun{
		ModelCheckTarget: store.ModelCheckTarget{AccountID: 101, Model: "gpt-5.6-sol", ExpectedModel: "gpt-5.6-sol"},
		Status:           "inconclusive", Message: "最高候选概率低于 80%，证据不足", ValidOutputs: 3,
		Prediction: "gpt-5.6-luna", Probability: 0.627,
		Candidates: []store.ModelCheckCandidate{{Model: "gpt-5.6-luna", Probability: 0.627}, {Model: "gpt-6-luna", Probability: 0.372999}, {Model: "gpt-5.6-sol", Probability: 0.000001}},
	}
	if _, err := st.CreateModelCheckRun(legacy); err != nil {
		t.Fatal(err)
	}
	res := doJSON(t, handler, http.MethodGet, "/api/model-check/runs?account_id=101&model=gpt-5.6-sol", nil)
	var history struct {
		Items []store.ModelCheckRun `json:"items"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || len(history.Items) != 1 || history.Items[0].Status != "suspected" {
		t.Fatalf("wrong history verdict: %s", res.Body.String())
	}
	res = doJSON(t, handler, http.MethodGet, "/api/model-check/timelines", nil)
	var timelines struct {
		Items []store.ModelCheckTimeline `json:"items"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &timelines); err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || len(timelines.Items) != 1 || len(timelines.Items[0].Runs) != 1 || timelines.Items[0].Runs[0].Status != "suspected" {
		t.Fatalf("wrong timeline verdict: %s", res.Body.String())
	}
	stored, err := st.ModelCheckRuns(101, "gpt-5.6-sol", 0, 30)
	if err != nil || len(stored) != 1 || stored[0].Status != legacy.Status || stored[0].Message != legacy.Message {
		t.Fatalf("read changed stored history: %+v %v", stored, err)
	}
}
