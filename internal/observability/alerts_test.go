package observability

import (
	"testing"
	"time"
)

func TestEvaluateAlertsStaysQuietWhenHealthy(t *testing.T) {
	if alerts := EvaluateAlerts(OperationalSignals{TenantID: "tenant-a", QueueDepth: 10, OldestJobAge: time.Second, LLMErrorRate: 0.01, TraceLatencyP95: 100 * time.Millisecond}, DefaultThresholds()); len(alerts) != 0 {
		t.Fatalf("healthy signals produced alerts: %#v", alerts)
	}
}

func TestEvaluateAlertsFiresForEachFaultInjectedSignal(t *testing.T) {
	thresholds := DefaultThresholds()
	cases := []struct {
		name    string
		signals OperationalSignals
		kind    AlertKind
	}{
		{"queue backlog", OperationalSignals{TenantID: "t", QueueDepth: 2000}, AlertQueueBacklog},
		{"stuck job", OperationalSignals{TenantID: "t", OldestJobAge: 30 * time.Minute}, AlertQueueBacklog},
		{"dead letter", OperationalSignals{TenantID: "t", DeadLetterCount: 3}, AlertDeadLetter},
		{"llm error", OperationalSignals{TenantID: "t", LLMErrorRate: 0.5}, AlertLLMError},
		{"degradation", OperationalSignals{TenantID: "t", DegradationCount: 2}, AlertDegradation},
		{"slow trace", OperationalSignals{TenantID: "t", TraceLatencyP95: 5 * time.Second}, AlertSlowTrace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alerts := EvaluateAlerts(tc.signals, thresholds)
			found := false
			for _, alert := range alerts {
				if alert.Kind == tc.kind {
					found = true
					if alert.TenantID != "t" {
						t.Fatalf("alert lost tenant: %#v", alert)
					}
				}
			}
			if !found {
				t.Fatalf("no %s alert for %#v", tc.kind, tc.signals)
			}
		})
	}
}

func TestFaultDrillEscalatesToCritical(t *testing.T) {
	// Drill: queue backlog plus dead-lettered jobs must surface at least one
	// critical alert so the drill is not silently green.
	alerts := EvaluateAlerts(OperationalSignals{TenantID: "tenant-a", QueueDepth: 5000, DeadLetterCount: 2, LLMErrorRate: 0.9}, DefaultThresholds())
	if !HasCritical(alerts) {
		t.Fatalf("drill did not escalate: %#v", alerts)
	}
}
