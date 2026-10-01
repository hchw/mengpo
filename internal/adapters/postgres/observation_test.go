package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func newObservationTestRepository(t *testing.T) (*ObservationRepository, string) {
	t.Helper()
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApplyPlatformMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "observation trace")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
	})
	return NewObservationRepository(tenantdb.NewRouter(db, store)), tenant.ID
}

func observationTraceEvent(tenantID, id string) observation.Event {
	sequence := int64(7)
	return observation.Event{
		ID:             id,
		IdempotencyKey: "idem-" + id,
		TenantID:       tenantID,
		ConversationID: "conversation-1",
		SourceType:     observation.SourceTool,
		SourceID:       "source-1",
		MessageType:    "tool.result",
		Payload:        json.RawMessage(`{"status":"error"}`),
		Sequence:       &sequence,
		ParentEventID:  "33333333-3333-4333-8333-333333333333",
		OccurredAt:     time.Now().UTC().Truncate(time.Microsecond),
		Visibility:     observation.VisibilityPrivate,
		Reliability:    observation.ReliabilityHigh,
		RetentionClass: "standard",
		AccessLevel:    observation.Level1,
		Trace: observation.Trace{
			TaskID:        "task-1",
			AttemptID:     "attempt-2",
			ProjectionID:  "44444444-4444-4444-8444-444444444444",
			UsedMemoryIDs: []string{"55555555-5555-4555-8555-555555555555"},
			ToolResultID:  "call-3",
			OutcomeID:     "outcome-4",
		},
	}
}

// TestObservationTraceRoundTrip proves the ordering, causal link and runtime
// trace accepted at the ingress survive a real PostgreSQL round trip, and that
// a complete trace yields direct attribution.
func TestObservationTraceRoundTrip(t *testing.T) {
	repository, tenantID := newObservationTestRepository(t)
	ctx := context.Background()

	event := observationTraceEvent(tenantID, "11111111-1111-4111-8111-111111111111")
	event.Attribution = observation.AssessAttribution(event.SessionID, event.ConversationID, event.ParentEventID, event.Trace)
	if event.Attribution.Level != observation.AttributionDirect {
		t.Fatalf("precondition: attribution = %q, want direct", event.Attribution.Level)
	}
	stored, created, err := repository.StoreObservation(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected a newly created observation")
	}
	if stored.Attribution.Level != observation.AttributionDirect {
		t.Fatalf("stored attribution = %q, want direct", stored.Attribution.Level)
	}

	got, err := repository.GetObservation(ctx, tenantID, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sequence == nil || *got.Sequence != 7 {
		t.Fatalf("sequence = %v, want 7", got.Sequence)
	}
	if got.ParentEventID != event.ParentEventID {
		t.Fatalf("parent event = %q, want %q", got.ParentEventID, event.ParentEventID)
	}
	if !reflect.DeepEqual(got.Trace, event.Trace) {
		t.Fatalf("trace = %#v, want %#v", got.Trace, event.Trace)
	}
	if got.Attribution.Level != observation.AttributionDirect || len(got.Attribution.Limitations) != 0 {
		t.Fatalf("attribution = %#v, want direct without limitations", got.Attribution)
	}
}

// TestObservationAttributionDegradesWithoutTrace proves a partial link is
// inferred (with the missing links named) and no link at all is unknown, so the
// service never fabricates a direct link.
func TestObservationAttributionDegradesWithoutTrace(t *testing.T) {
	repository, tenantID := newObservationTestRepository(t)
	ctx := context.Background()

	partial := observationTraceEvent(tenantID, "22222222-2222-4222-8222-222222222222")
	partial.Trace = observation.Trace{}
	partial.ParentEventID = ""
	partial.Sequence = nil
	partial.Attribution = observation.AssessAttribution(partial.SessionID, partial.ConversationID, partial.ParentEventID, partial.Trace)
	if partial.Attribution.Level != observation.AttributionInferred {
		t.Fatalf("precondition: partial attribution = %q, want inferred", partial.Attribution.Level)
	}
	if _, _, err := repository.StoreObservation(ctx, partial); err != nil {
		t.Fatal(err)
	}
	storedPartial, err := repository.GetObservation(ctx, tenantID, partial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedPartial.Attribution.Level != observation.AttributionInferred {
		t.Fatalf("partial attribution = %q, want inferred", storedPartial.Attribution.Level)
	}
	if len(storedPartial.Attribution.Limitations) == 0 {
		t.Fatal("inferred attribution must name its missing links")
	}

	unlinked := observationTraceEvent(tenantID, "33333333-3333-4333-8333-333333333334")
	unlinked.ConversationID = ""
	unlinked.Trace = observation.Trace{}
	unlinked.ParentEventID = ""
	unlinked.Sequence = nil
	unlinked.Attribution = observation.AssessAttribution(unlinked.SessionID, unlinked.ConversationID, unlinked.ParentEventID, unlinked.Trace)
	if unlinked.Attribution.Level != observation.AttributionUnknown {
		t.Fatalf("precondition: unlinked attribution = %q, want unknown", unlinked.Attribution.Level)
	}
	if _, _, err := repository.StoreObservation(ctx, unlinked); err != nil {
		t.Fatal(err)
	}
	storedUnlinked, err := repository.GetObservation(ctx, tenantID, unlinked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedUnlinked.Attribution.Level != observation.AttributionUnknown {
		t.Fatalf("unlinked attribution = %q, want unknown", storedUnlinked.Attribution.Level)
	}
	if !reflect.DeepEqual(storedUnlinked.Trace, observation.Trace{}) {
		t.Fatalf("trace must stay empty, got %#v", storedUnlinked.Trace)
	}
}
