package observability

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// OpsMetrics collects operational counters that are not tied to HTTP requests:
// periodic schedule executions and Memory LLM analysis outcomes. Every series
// is labelled by tenant so a single noisy or failing tenant is visible.
type OpsMetrics struct {
	mu       sync.Mutex
	counters map[string]uint64
	duration map[string]int64 // summed milliseconds
}

// NewOpsMetrics builds an empty metrics registry.
func NewOpsMetrics() *OpsMetrics {
	return &OpsMetrics{counters: map[string]uint64{}, duration: map[string]int64{}}
}

// RecordScheduleRun records the outcome and duration of one schedule execution.
func (m *OpsMetrics) RecordScheduleRun(tenantID, schedule, status string, duration time.Duration) {
	m.observe("memory_schedule_runs_total", tenantID, schedule, status, duration)
}

// RecordAnalysis records the outcome of one Memory LLM analysis run.
func (m *OpsMetrics) RecordAnalysis(tenantID, task, status string, duration time.Duration) {
	m.observe("memory_analysis_runs_total", tenantID, task, status, duration)
}

func (m *OpsMetrics) observe(metric, tenantID, kind, status string, duration time.Duration) {
	if m == nil {
		return
	}
	key := metric + "\x00" + tenantID + "\x00" + kind + "\x00" + status
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[key]++
	m.duration[key] += duration.Milliseconds()
}

// Render writes the counters in Prometheus text exposition format.
func (m *OpsMetrics) Render(w io.Writer) {
	if m == nil {
		return
	}
	m.mu.Lock()
	keys := make([]string, 0, len(m.counters))
	for key := range m.counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	type row struct {
		metric, tenant, kind, status string
		count, millis                uint64
	}
	rows := make([]row, 0, len(keys))
	for _, key := range keys {
		parts := splitKey(key)
		if len(parts) != 4 {
			continue
		}
		rows = append(rows, row{metric: parts[0], tenant: parts[1], kind: parts[2], status: parts[3], count: m.counters[key], millis: uint64(m.duration[key])})
	}
	m.mu.Unlock()
	seen := map[string]bool{}
	for _, item := range rows {
		if !seen[item.metric] {
			seen[item.metric] = true
			_, _ = fmt.Fprintf(w, "# HELP %s Periodic and analysis counters by tenant.\n# TYPE %s counter\n", item.metric, item.metric)
		}
		_, _ = fmt.Fprintf(w, "%s{tenant=%q,kind=%q,status=%q} %d\n", item.metric, item.tenant, item.kind, item.status, item.count)
		_, _ = fmt.Fprintf(w, "%s_duration_ms_total{tenant=%q,kind=%q,status=%q} %d\n", item.metric, item.tenant, item.kind, item.status, item.millis)
	}
}

func splitKey(key string) []string {
	var parts []string
	current := ""
	for _, r := range key {
		if r == 0 {
			parts = append(parts, current)
			current = ""
			continue
		}
		current += string(r)
	}
	parts = append(parts, current)
	return parts
}
