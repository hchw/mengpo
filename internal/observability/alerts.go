package observability

import (
	"fmt"
	"time"
)

type AlertKind string

const (
	AlertQueueBacklog AlertKind = "queue_backlog"
	AlertDeadLetter   AlertKind = "dead_letter"
	AlertLLMError     AlertKind = "llm_error"
	AlertDegradation  AlertKind = "degradation"
	AlertSlowTrace    AlertKind = "slow_trace"
)

type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// OperationalSignals is the small set of health signals the worker and server
// expose for alerting. Keeping them in one struct makes a fault drill a matter
// of injecting one field.
type OperationalSignals struct {
	TenantID         string
	QueueDepth       int
	OldestJobAge     time.Duration
	DeadLetterCount  int
	LLMErrorRate     float64
	DegradationCount int
	TraceLatencyP95  time.Duration
}

type AlertThresholds struct {
	QueueDepth      int
	OldestJobAge    time.Duration
	DeadLetterCount int
	LLMErrorRate    float64
	Degradations    int
	TraceLatencyP95 time.Duration
}

func DefaultThresholds() AlertThresholds {
	return AlertThresholds{
		QueueDepth:      1000,
		OldestJobAge:    10 * time.Minute,
		DeadLetterCount: 1,
		LLMErrorRate:    0.2,
		Degradations:    1,
		TraceLatencyP95: 2 * time.Second,
	}
}

type Alert struct {
	Kind     AlertKind `json:"kind"`
	Severity Severity  `json:"severity"`
	TenantID string    `json:"tenant_id"`
	Message  string    `json:"message"`
}

// EvaluateAlerts turns signals into deterministic alerts. Every alert carries
// the tenant so an operator can scope the incident.
func EvaluateAlerts(signals OperationalSignals, thresholds AlertThresholds) []Alert {
	alerts := make([]Alert, 0, 5)
	if thresholds.QueueDepth > 0 && signals.QueueDepth >= thresholds.QueueDepth {
		alerts = append(alerts, Alert{Kind: AlertQueueBacklog, Severity: SeverityWarning, TenantID: signals.TenantID, Message: fmt.Sprintf("queue depth %d >= %d", signals.QueueDepth, thresholds.QueueDepth)})
	}
	if thresholds.OldestJobAge > 0 && signals.OldestJobAge >= thresholds.OldestJobAge {
		alerts = append(alerts, Alert{Kind: AlertQueueBacklog, Severity: SeverityCritical, TenantID: signals.TenantID, Message: fmt.Sprintf("oldest job age %s >= %s", signals.OldestJobAge, thresholds.OldestJobAge)})
	}
	if thresholds.DeadLetterCount > 0 && signals.DeadLetterCount >= thresholds.DeadLetterCount {
		alerts = append(alerts, Alert{Kind: AlertDeadLetter, Severity: SeverityCritical, TenantID: signals.TenantID, Message: fmt.Sprintf("%d dead-letter jobs", signals.DeadLetterCount)})
	}
	if thresholds.LLMErrorRate > 0 && signals.LLMErrorRate >= thresholds.LLMErrorRate {
		alerts = append(alerts, Alert{Kind: AlertLLMError, Severity: SeverityWarning, TenantID: signals.TenantID, Message: fmt.Sprintf("LLM error rate %.2f >= %.2f", signals.LLMErrorRate, thresholds.LLMErrorRate)})
	}
	if thresholds.Degradations > 0 && signals.DegradationCount >= thresholds.Degradations {
		alerts = append(alerts, Alert{Kind: AlertDegradation, Severity: SeverityWarning, TenantID: signals.TenantID, Message: fmt.Sprintf("%d degraded projections", signals.DegradationCount)})
	}
	if thresholds.TraceLatencyP95 > 0 && signals.TraceLatencyP95 >= thresholds.TraceLatencyP95 {
		alerts = append(alerts, Alert{Kind: AlertSlowTrace, Severity: SeverityWarning, TenantID: signals.TenantID, Message: fmt.Sprintf("trace p95 %s >= %s", signals.TraceLatencyP95, thresholds.TraceLatencyP95)})
	}
	return alerts
}

// HasCritical reports whether any alert is critical, used to fail a drill.
func HasCritical(alerts []Alert) bool {
	for _, alert := range alerts {
		if alert.Severity == SeverityCritical {
			return true
		}
	}
	return false
}
