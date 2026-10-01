package observability

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestOpsMetricsRendersTenantLabels(t *testing.T) {
	metrics := NewOpsMetrics()
	metrics.RecordScheduleRun("tenant-a", "consolidate", "ok", 120*time.Millisecond)
	metrics.RecordScheduleRun("tenant-b", "consolidate", "error", 30*time.Millisecond)
	metrics.RecordAnalysis("tenant-a", "consolidate_memory", "succeeded", 200*time.Millisecond)

	var buffer bytes.Buffer
	metrics.Render(&buffer)
	output := buffer.String()
	for _, want := range []string{
		`memory_schedule_runs_total{tenant="tenant-a",kind="consolidate",status="ok"} 1`,
		`memory_schedule_runs_total{tenant="tenant-b",kind="consolidate",status="error"} 1`,
		`memory_schedule_runs_total_duration_ms_total{tenant="tenant-a",kind="consolidate",status="ok"} 120`,
		`memory_analysis_runs_total{tenant="tenant-a",kind="consolidate_memory",status="succeeded"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metrics output missing %q\n%s", want, output)
		}
	}
	if strings.Contains(output, `tenant="tenant-c"`) {
		t.Fatal("metrics leaked an unrecorded tenant")
	}
}
