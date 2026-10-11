package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

// fakeIdleSessions serves a fixed idle list and remembers which ids the job
// asked to delete. deleteResult decides whether the conditional delete wins.
type fakeIdleSessions struct {
	idle         []ports.SessionSummaryRecord
	deleted      []string
	deleteResult bool
	listCutoff   time.Time
}

func (f *fakeIdleSessions) ListIdleSessions(_ context.Context, _ string, olderThan time.Time, _ int) ([]ports.SessionSummaryRecord, error) {
	f.listCutoff = olderThan
	return f.idle, nil
}

func (f *fakeIdleSessions) DeleteIdleSession(_ context.Context, _, sessionID string, _ time.Time) (bool, error) {
	f.deleted = append(f.deleted, sessionID)
	return f.deleteResult, nil
}

type fakeAudit struct {
	events []ports.AuditEventRecord
}

func (f *fakeAudit) RecordAuditEvent(_ context.Context, _ string, event ports.AuditEventRecord) error {
	f.events = append(f.events, event)
	return nil
}

func TestSessionCleanupRemovesIdleSessionsAndAudits(t *testing.T) {
	store := &fakeIdleSessions{
		idle:         []ports.SessionSummaryRecord{{ID: "s1", Title: "empty"}, {ID: "s2"}},
		deleteResult: true,
	}
	audit := &fakeAudit{}
	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	job := &SessionCleanupJob{PlanName: "session-cleanup", Sessions: store, Audit: audit, Retention: 7 * 24 * time.Hour, Clock: clock}

	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(store.deleted) != 2 {
		t.Fatalf("deleted = %#v, want both idle sessions", store.deleted)
	}
	wantCutoff := clock().Add(-7 * 24 * time.Hour)
	if !store.listCutoff.Equal(wantCutoff) {
		t.Fatalf("list cutoff = %v, want %v", store.listCutoff, wantCutoff)
	}
	if len(audit.events) != 2 {
		t.Fatalf("audit events = %d, want 2", len(audit.events))
	}
	for _, event := range audit.events {
		if event.ActorType != "system" || event.Action != "session.cleanup" || event.ResourceType != "session" {
			t.Fatalf("unexpected audit event %#v", event)
		}
		var changes map[string]any
		if err := json.Unmarshal(event.Changes, &changes); err != nil {
			t.Fatalf("audit changes are not JSON: %v", err)
		}
		if changes["reason"] == "" || changes["removed_at"] == "" {
			t.Fatalf("audit changes missing reason/removed_at: %#v", changes)
		}
	}
}

func TestSessionCleanupKeepsSessionThatRegainedActivity(t *testing.T) {
	store := &fakeIdleSessions{
		idle:         []ports.SessionSummaryRecord{{ID: "s1"}},
		deleteResult: false,
	}
	audit := &fakeAudit{}
	job := &SessionCleanupJob{PlanName: "session-cleanup", Sessions: store, Audit: audit}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(audit.events) != 0 {
		t.Fatalf("audited %d sessions that were not removed, want 0", len(audit.events))
	}
}

func TestSessionCleanupSkipsEmptyTenant(t *testing.T) {
	store := &fakeIdleSessions{}
	audit := &fakeAudit{}
	job := &SessionCleanupJob{PlanName: "session-cleanup", Sessions: store, Audit: audit}
	if err := job.Run(context.Background(), "tenant-a"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(store.deleted) != 0 || len(audit.events) != 0 {
		t.Fatalf("empty tenant touched: deleted=%d audit=%d", len(store.deleted), len(audit.events))
	}
}
