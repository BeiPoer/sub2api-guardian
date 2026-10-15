package modelcheck

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"sub2api-guardian/backend/internal/store"
)

func TestMatchesModelTraceReference(t *testing.T) {
	// Golden probabilities produced by upstream static/fingerprint-core.js,
	// commit 6cd4a8ff0f3f4e49d6d6969dd2fbadfa1569fde2, using the bundled bank.
	raw, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Outputs    [][]int                     `json:"outputs"`
		Candidates []store.ModelCheckCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		got := analyze(fixture.Outputs)
		if len(got) != len(fixture.Candidates) {
			t.Fatal("candidate count differs")
		}
		for i, want := range fixture.Candidates {
			if got[i].Model != want.Model || math.Abs(got[i].Probability-want.Probability) > 1e-10 {
				t.Fatalf("candidate %d: got %+v, want %+v", i, got[i], want)
			}
		}
	}
}

func TestParseNumbers(t *testing.T) {
	for _, test := range []struct {
		text string
		want []int
	}{
		{"拒绝生成数字", nil},
		{"生成 300 个：1, 355, 0, 356, 42；说明 9", []int{1, 355, 42}},
		{"1 2 prose 3 4 5", []int{3, 4, 5}},
		{"99999999999999999999999 5 6", []int{5, 6}},
	} {
		if got := parseNumbers(test.text); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parse %q: %v, want %v", test.text, got, test.want)
		}
	}
}

func TestClassificationUsesExpectedModelProbability(t *testing.T) {
	for _, test := range []struct {
		name       string
		expected   string
		candidates []store.ModelCheckCandidate
		want       string
	}{
		{"expected matches", "gpt-5.4", []store.ModelCheckCandidate{{Model: "gpt-5.4", Probability: 0.95}}, "consistent"},
		{"expected unlikely", "gpt-5.4", []store.ModelCheckCandidate{{Model: "gpt-5.5", Probability: 0.95}, {Model: "gpt-5.4", Probability: 0.05}}, "suspected"},
		{"ambiguous expected", "gpt-5.4", []store.ModelCheckCandidate{{Model: "gpt-5.5", Probability: 0.79}, {Model: "gpt-5.4", Probability: 0.21}}, "inconclusive"},
		{"expected leads but uncertain", "gpt-5.4", []store.ModelCheckCandidate{{Model: "gpt-5.4", Probability: 0.627}, {Model: "gpt-5.5", Probability: 0.373}}, "inconclusive"},
		{"split luna alternatives", "gpt-5.6-sol", []store.ModelCheckCandidate{{Model: "gpt-5.6-luna", Probability: 0.627}, {Model: "gpt-6-luna", Probability: 0.372999}, {Model: "gpt-5.6-sol", Probability: 0.000001}}, "suspected"},
		{"upper boundary", "expected", []store.ModelCheckCandidate{{Model: "expected", Probability: 0.8}}, "consistent"},
		{"lower boundary", "expected", []store.ModelCheckCandidate{{Model: "expected", Probability: 0.2}}, "suspected"},
		{"just above lower boundary", "expected", []store.ModelCheckCandidate{{Model: "expected", Probability: 0.20001}}, "inconclusive"},
		{"just below upper boundary", "expected", []store.ModelCheckCandidate{{Model: "expected", Probability: 0.79999}}, "inconclusive"},
		{"unspecified", "", []store.ModelCheckCandidate{{Model: "gpt-5.5", Probability: 0.99}}, "unverified"},
		{"missing is not zero", "unknown", []store.ModelCheckCandidate{{Model: "gpt-5.5", Probability: 0.99}}, "unverified"},
		{"explicit zero", "expected", []store.ModelCheckCandidate{{Model: "expected", Probability: 0}}, "suspected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, _ := classify(test.expected, test.candidates); got != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func TestReclassifyPreservesIncompleteAndFailedRuns(t *testing.T) {
	base := store.ModelCheckRun{ModelCheckTarget: store.ModelCheckTarget{ExpectedModel: "sol"}, Status: "inconclusive", Message: "old threshold", ValidOutputs: 3, Candidates: []store.ModelCheckCandidate{{Model: "luna-a", Probability: 0.627}, {Model: "luna-b", Probability: 0.373}, {Model: "sol", Probability: 0}}}
	corrected := base
	Reclassify(&corrected)
	if corrected.Status != "suspected" || corrected.Message == base.Message {
		t.Fatalf("legacy verdict not corrected: %+v", corrected)
	}
	if !reflect.DeepEqual(corrected.Candidates, base.Candidates) {
		t.Fatal("candidate evidence changed")
	}
	for _, status := range []string{"error", "interrupted", "running"} {
		run := base
		run.Status = status
		Reclassify(&run)
		if run.Status != status || run.Message != base.Message {
			t.Fatalf("changed %s run", status)
		}
	}
	for _, count := range []int{0, 1, 2} {
		run := base
		run.ValidOutputs = count
		Reclassify(&run)
		if run.Status != base.Status || run.Message != base.Message {
			t.Fatalf("classified incomplete run with %d outputs", count)
		}
	}
	run := base
	run.Candidates = nil
	Reclassify(&run)
	if run.Message != base.Message {
		t.Fatal("rewrote history without probability evidence")
	}
}
