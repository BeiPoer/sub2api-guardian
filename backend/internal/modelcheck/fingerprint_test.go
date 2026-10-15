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

func TestClassificationDoesNotClaimUnknownOrUncertainModelIsDegraded(t *testing.T) {
	for _, test := range []struct {
		expected, prediction string
		probability          float64
		want                 string
	}{
		{"gpt-5.4", "gpt-5.4", 0.95, "consistent"},
		{"gpt-5.4", "gpt-5.5", 0.95, "suspected"},
		{"gpt-5.4", "gpt-5.5", 0.79, "inconclusive"},
		{"", "gpt-5.5", 0.99, "unverified"},
	} {
		if got, _ := classify(test.expected, test.prediction, test.probability); got != test.want {
			t.Fatalf("got %s, want %s", got, test.want)
		}
	}
}
