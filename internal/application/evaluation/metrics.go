package evaluation

import (
	"fmt"
	"math"
	"sort"
)

// RetrievalPrediction is one system output for a retrieval sample.
type RetrievalPrediction struct {
	SampleID    string
	RecalledIDs []string
	PromotedIDs []string
	LatencyMs   int
	Tokens      int
	CostUSD     float64
	CacheHit    bool
}

// FailurePrediction is one system output for a failure sample.
type FailurePrediction struct {
	SampleID    string
	Confidence  string
	Attribution string
}

type EvaluationReport struct {
	RetrievalSamples    int     `json:"retrieval_samples"`
	RecallPrecision     float64 `json:"recall_precision"`
	PromotionPrecision  float64 `json:"promotion_precision"`
	WrongMemoryRate     float64 `json:"wrong_memory_rate"`
	FailureSamples      int     `json:"failure_samples"`
	AttributionAccuracy float64 `json:"attribution_accuracy"`
	LatencyP95Ms        int     `json:"latency_p95_ms"`
	TokensPerPrediction float64 `json:"tokens_per_prediction"`
	AverageCostUSD      float64 `json:"average_cost_usd"`
	CacheHitRate        float64 `json:"cache_hit_rate"`
}

// EvaluateRetrieval computes recall precision, promotion precision and the
// wrong-memory rate. A forbidden memory that is recalled counts as wrong and
// directly lowers precision.
func EvaluateRetrieval(dataset Dataset, predictions []RetrievalPrediction) (EvaluationReport, error) {
	if dataset.Kind != DatasetRetrieval {
		return EvaluationReport{}, fmt.Errorf("%w: not a retrieval dataset", ErrInvalidDataset)
	}
	byID := make(map[string]RetrievalPrediction, len(predictions))
	for _, prediction := range predictions {
		byID[prediction.SampleID] = prediction
	}
	report := EvaluationReport{RetrievalSamples: len(dataset.Retrieval)}
	var expectedTotal, recalledCorrect, promotedTotal, promotedCorrect, wrong, forbiddenTotal int
	var latency []int
	var tokens int
	var cost float64
	var cacheHits int
	seen := 0
	for _, sample := range dataset.Retrieval {
		prediction, ok := byID[sample.ID]
		if !ok {
			continue
		}
		seen++
		expected := stringSet(sample.ExpectedMemoryIDs)
		forbidden := stringSet(sample.ForbiddenMemoryIDs)
		forbiddenTotal += len(forbidden)
		expectedTotal += len(expected)
		for _, id := range prediction.RecalledIDs {
			if _, bad := forbidden[id]; bad {
				wrong++
				continue
			}
			if _, want := expected[id]; want {
				recalledCorrect++
			}
		}
		for _, id := range prediction.PromotedIDs {
			promotedTotal++
			if _, want := expected[id]; want {
				promotedCorrect++
			}
		}
		latency = append(latency, prediction.LatencyMs)
		tokens += prediction.Tokens
		cost += prediction.CostUSD
		if prediction.CacheHit {
			cacheHits++
		}
	}
	if seen != len(dataset.Retrieval) {
		return EvaluationReport{}, fmt.Errorf("%w: missing predictions for %d samples", ErrInvalidDataset, len(dataset.Retrieval)-seen)
	}
	if expectedTotal > 0 {
		report.RecallPrecision = float64(recalledCorrect) / float64(expectedTotal)
	}
	if promotedTotal > 0 {
		report.PromotionPrecision = float64(promotedCorrect) / float64(promotedTotal)
	}
	if expectedTotal > 0 {
		report.WrongMemoryRate = float64(wrong) / float64(expectedTotal)
	}
	report.LatencyP95Ms = percentile(latency, 0.95)
	if seen > 0 {
		report.TokensPerPrediction = float64(tokens) / float64(seen)
		report.AverageCostUSD = cost / float64(seen)
		report.CacheHitRate = float64(cacheHits) / float64(seen)
	}
	return report, nil
}

// EvaluateFailure computes attribution accuracy across failure samples. Missing
// predictions lower accuracy instead of being skipped.
func EvaluateFailure(dataset Dataset, predictions []FailurePrediction) (EvaluationReport, error) {
	if dataset.Kind != DatasetFailure {
		return EvaluationReport{}, fmt.Errorf("%w: not a failure dataset", ErrInvalidDataset)
	}
	byID := make(map[string]FailurePrediction, len(predictions))
	for _, prediction := range predictions {
		byID[prediction.SampleID] = prediction
	}
	report := EvaluationReport{FailureSamples: len(dataset.Failure)}
	correct := 0
	for _, sample := range dataset.Failure {
		prediction, ok := byID[sample.ID]
		if ok && prediction.Attribution == sample.ExpectedAttribution {
			correct++
		}
	}
	if len(dataset.Failure) > 0 {
		report.AttributionAccuracy = float64(correct) / float64(len(dataset.Failure))
	}
	return report, nil
}

// MergeReports combines retrieval and failure reports into one snapshot.
func MergeReports(retrieval, failure EvaluationReport) EvaluationReport {
	merged := retrieval
	merged.FailureSamples = failure.FailureSamples
	merged.AttributionAccuracy = failure.AttributionAccuracy
	return merged
}

// percentile uses the nearest-rank definition so p95 of a small sample still
// reflects its worst case rather than always returning the minimum.
func percentile(values []int, fraction float64) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	index := int(math.Ceil(fraction*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}
