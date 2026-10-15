// Package modelcheck implements ModelTrace's numeric fingerprint attribution.
// Algorithm and bank: https://github.com/xqy2006/ModelTrace (see MODELTRACE-LICENSE).
package modelcheck

import (
	_ "embed"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"unicode"

	"sub2api-guardian/backend/internal/store"
)

//go:embed bank.json
var bankJSON []byte

type featureArtifact struct {
	Mean         []float64     `json:"feature_mean"`
	Scale        []float64     `json:"feature_scale"`
	Basis        [][]float64   `json:"nuisance_basis"`
	Centroids    [][]float64   `json:"centroids"`
	Environments [][][]float64 `json:"environment_centroids"`
	Weight       float64       `json:"weight"`
}

type fingerprintBank struct {
	BuiltAt string `json:"built_at"`
	Models  []struct {
		ID string `json:"id"`
	} `json:"models"`
	Robust struct {
		Hellinger featureArtifact `json:"hellinger"`
		Ordered   featureArtifact `json:"ordered_blocks"`
	} `json:"robust"`
	Calibration map[string]struct {
		Beta float64 `json:"beta"`
	} `json:"calibration"`
}

var bank = func() fingerprintBank {
	var v fingerprintBank
	if err := json.Unmarshal(bankJSON, &v); err != nil {
		panic(err)
	}
	return v
}()

func Models() []string {
	models := make([]string, len(bank.Models))
	for i, model := range bank.Models {
		models[i] = model.ID
	}
	return models
}

var digits = regexp.MustCompile(`[0-9]+`)

func parseNumbers(text string) []int {
	var best, current []int
	previous := 0
	for _, match := range digits.FindAllStringIndex(text, -1) {
		for _, r := range text[previous:match[0]] {
			if unicode.IsLetter(r) {
				if len(current) > len(best) {
					best = current
				}
				current = nil
				break
			}
		}
		value, err := strconv.Atoi(text[match[0]:match[1]])
		if err == nil && value >= 1 && value <= 355 {
			current = append(current, value)
		}
		previous = match[1]
	}
	if len(current) > len(best) {
		best = current
	}
	return best
}

func dot(a, b []float64) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

func standardize(v []float64) []float64 {
	var mean, variance float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	for _, x := range v {
		variance += (x - mean) * (x - mean)
	}
	scale := math.Max(math.Sqrt(variance/float64(len(v))), 1e-12)
	result := make([]float64, len(v))
	for i, x := range v {
		result[i] = (x - mean) / scale
	}
	return result
}

func normalize(v []float64) []float64 {
	scale := math.Max(math.Sqrt(dot(v, v)), 1e-12)
	result := make([]float64, len(v))
	for i, x := range v {
		result[i] = x / scale
	}
	return result
}

func project(v []float64, basis [][]float64) []float64 {
	result := append([]float64(nil), v...)
	for _, direction := range basis {
		// Match the Python reference's simultaneous orthogonal projection.
		weight := dot(v, direction)
		for i := range result {
			result[i] -= weight * direction[i]
		}
	}
	return normalize(result)
}

func smooth(counts []float64) []float64 {
	total := float64(len(counts)) * 0.5
	for _, value := range counts {
		total += value
	}
	for i := range counts {
		counts[i] = math.Sqrt((counts[i] + 0.5) / total)
	}
	return counts
}

func featureScores(feature []float64, artifact featureArtifact, ordered bool) []float64 {
	v := make([]float64, len(feature))
	for i := range feature {
		v[i] = (feature[i] - artifact.Mean[i]) / artifact.Scale[i]
	}
	projected := project(v, artifact.Basis)
	scores := make([]float64, len(artifact.Centroids))
	for i, centroid := range artifact.Centroids {
		scores[i] = dot(projected, centroid)
	}
	scores = standardize(scores)
	if !ordered {
		return scores
	}
	unit := normalize(v)
	template := make([]float64, len(scores))
	for i := range template {
		template[i] = math.Inf(-1)
		for _, environment := range artifact.Environments {
			template[i] = math.Max(template[i], dot(unit, environment[i]))
		}
	}
	template = standardize(template)
	for i := range scores {
		scores[i] = 0.5*scores[i] + 0.5*template[i]
	}
	return standardize(scores)
}

func scoreNumbers(numbers []int) []float64 {
	counts := make([]float64, 355)
	lastDigits := make([]float64, 10)
	for _, n := range numbers {
		counts[n-1]++
		lastDigits[n%10]++
	}
	marginal := featureScores(smooth(counts), bank.Robust.Hellinger, false)
	ordered := []float64{}
	start := 0
	for i := 0; i < 4; i++ {
		size := len(numbers) / 4
		if i < len(numbers)%4 {
			size++
		}
		bins := make([]float64, 16)
		for _, n := range numbers[start : start+size] {
			bins[(n-1)*16/355]++
		}
		ordered = append(ordered, smooth(bins)...)
		start += size
	}
	ordered = append(ordered, smooth(lastDigits)...)
	orderedScores := featureScores(ordered, bank.Robust.Ordered, true)
	weight := bank.Robust.Ordered.Weight
	for i := range marginal {
		marginal[i] = (1-weight)*marginal[i] + weight*orderedScores[i]
	}
	return marginal
}

func analyze(outputs [][]int) []store.ModelCheckCandidate {
	scores := make([]float64, len(bank.Models))
	for _, numbers := range outputs {
		for i, score := range scoreNumbers(numbers) {
			scores[i] += score / float64(len(outputs))
		}
	}
	beta := bank.Calibration[strconv.Itoa(min(len(outputs), 3))].Beta
	maximum := math.Inf(-1)
	for _, score := range scores {
		maximum = math.Max(maximum, score*beta)
	}
	var total float64
	for i := range scores {
		scores[i] = math.Exp(scores[i]*beta - maximum)
		total += scores[i]
	}
	results := make([]store.ModelCheckCandidate, len(scores))
	for i, score := range scores {
		results[i] = store.ModelCheckCandidate{Model: bank.Models[i].ID, Probability: score / total}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Probability > results[j].Probability })
	return results
}
