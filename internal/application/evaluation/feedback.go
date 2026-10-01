package evaluation

import (
	"errors"
	"fmt"
)

type FeedbackType string

const (
	FeedbackHelpful   FeedbackType = "helpful"
	FeedbackHarmful   FeedbackType = "harmful"
	FeedbackCorrected FeedbackType = "corrected"
	FeedbackAccepted  FeedbackType = "accepted"
	FeedbackIgnored   FeedbackType = "ignored"
	FeedbackConflict  FeedbackType = "conflict"
	FeedbackExpired   FeedbackType = "expired"
)

var ErrInvalidFeedback = errors.New("invalid feedback event")

var feedbackOrder = []FeedbackType{
	FeedbackHelpful, FeedbackHarmful, FeedbackCorrected, FeedbackAccepted, FeedbackIgnored, FeedbackConflict, FeedbackExpired,
}

// FeedbackEvent is one user/agent signal about an injected memory.
type FeedbackEvent struct {
	MemoryID string
	Type     FeedbackType
}

type FeedbackMetrics struct {
	Total       int
	Counts      map[FeedbackType]int
	HelpfulRate float64
	HarmfulRate float64
	// IgnoredRate is signals that were neither accepted nor corrected.
	IgnoredRate    float64
	ConflictRate   float64
	CorrectionRate float64
}

// ComputeFeedbackMetrics aggregates feedback into deterministic, snapshot-able
// metrics. Negative signals (harmful, conflict, expired) are kept separate from
// positive and neutral ones so a single rate cannot hide regressions.
func ComputeFeedbackMetrics(events []FeedbackEvent) (FeedbackMetrics, error) {
	metrics := FeedbackMetrics{Counts: map[FeedbackType]int{}}
	for _, event := range events {
		if event.MemoryID == "" || !validFeedback(event.Type) {
			return FeedbackMetrics{}, fmt.Errorf("%w: %#v", ErrInvalidFeedback, event)
		}
		metrics.Counts[event.Type]++
		metrics.Total++
	}
	if metrics.Total == 0 {
		return metrics, nil
	}
	ratio := func(count int) float64 { return float64(count) / float64(metrics.Total) }
	metrics.HelpfulRate = ratio(metrics.Counts[FeedbackHelpful])
	metrics.HarmfulRate = ratio(metrics.Counts[FeedbackHarmful])
	metrics.IgnoredRate = ratio(metrics.Counts[FeedbackIgnored] + metrics.Counts[FeedbackExpired])
	metrics.ConflictRate = ratio(metrics.Counts[FeedbackConflict])
	metrics.CorrectionRate = ratio(metrics.Counts[FeedbackCorrected])
	return metrics, nil
}

// SortedCounts returns counts in the fixed feedback order for stable snapshots.
func (m FeedbackMetrics) SortedCounts() []struct {
	Type  FeedbackType `json:"type"`
	Count int          `json:"count"`
} {
	result := make([]struct {
		Type  FeedbackType `json:"type"`
		Count int          `json:"count"`
	}, 0, len(feedbackOrder))
	for _, kind := range feedbackOrder {
		result = append(result, struct {
			Type  FeedbackType `json:"type"`
			Count int          `json:"count"`
		}{Type: kind, Count: m.Counts[kind]})
	}
	return result
}

func validFeedback(kind FeedbackType) bool {
	for _, known := range feedbackOrder {
		if known == kind {
			return true
		}
	}
	return false
}
