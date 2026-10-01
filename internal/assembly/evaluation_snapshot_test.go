package assembly

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/evaluation"
	"github.com/hchw/mengpo/internal/ports"
)

type snapshotRuns struct{ runs []ports.AnalysisRunView }

func (s snapshotRuns) StartAnalysisRun(context.Context, string, ports.AnalysisRunRecord) (bool, error) {
	return true, nil
}
func (s snapshotRuns) FinishAnalysisRun(context.Context, string, string, ports.AnalysisRunUpdate) (bool, error) {
	return true, nil
}
func (s snapshotRuns) LinkRunOutputs(context.Context, string, string, []string) (bool, error) {
	return true, nil
}
func (s snapshotRuns) ListAnalysisRuns(context.Context, string, ports.AnalysisRunFilter) ([]ports.AnalysisRunView, error) {
	return s.runs, nil
}

// TestEvaluationSnapshotUsesLatestScheduledReport proves the console evaluation
// surface returns the scheduled plan's latest report rather than zeros.
func TestEvaluationSnapshotUsesLatestScheduledReport(t *testing.T) {
	metrics, _ := json.Marshal(evaluation.FeedbackMetrics{Total: 4, HelpfulRate: 0.75})
	api := &ConsoleAPI{Runs: snapshotRuns{runs: []ports.AnalysisRunView{{
		AnalysisRunRecord: ports.AnalysisRunRecord{TaskType: "evaluate_feedback", CreatedAt: time.Unix(0, 0).UTC()},
		Result:            metrics,
	}}}}
	ctx := agentaccess.WithIdentity(context.Background(), agentaccess.Identity{TenantID: "tenant-a", UserID: "u1"})
	snapshot := api.evaluationSnapshot(ctx)
	if snapshot["HelpfulRate"].(float64) != 0.75 {
		t.Fatalf("snapshot did not use the scheduled report: %#v", snapshot)
	}
	if snapshot["generated_at"] != time.Unix(0, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("generated_at = %v", snapshot["generated_at"])
	}
}
