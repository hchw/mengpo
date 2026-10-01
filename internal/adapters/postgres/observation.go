package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/hchw/mengpo/internal/domain/observation"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

type ObservationRepository struct{ router *tenantdb.Router }

func NewObservationRepository(router *tenantdb.Router) *ObservationRepository {
	return &ObservationRepository{router: router}
}

const observationColumns = `id::text, COALESCE(source_event_id, ''), idempotency_key, COALESCE(session_id::text, ''), COALESCE(conversation_id, ''), source_type, source_id, message_type, payload, COALESCE(payload_text, ''), sequence, COALESCE(parent_event_id::text, ''), occurred_at, visibility, reliability, retention_class, access_level, trace_metadata, attribution_level`

// GetObservation loads one raw event by id within the caller's tenant schema.
func (r *ObservationRepository) GetObservation(ctx context.Context, tenantID, eventID string) (observation.Event, error) {
	if tenantID == "" || eventID == "" {
		return observation.Event{}, ports.ErrObservationNotFound
	}
	var stored observation.Event
	var storedPayload, storedTrace []byte
	var sequence sql.NullInt64
	var parent, session, conversation, sourceEvent sql.NullString
	var accessLevel, attribution string
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT `+observationColumns+` FROM observed_events WHERE id = $1::uuid`, eventID).Scan(
			&stored.ID, &sourceEvent, &stored.IdempotencyKey, &session, &conversation, &stored.SourceType, &stored.SourceID,
			&stored.MessageType, &storedPayload, &stored.PayloadText, &sequence, &parent, &stored.OccurredAt,
			&stored.Visibility, &stored.Reliability, &stored.RetentionClass, &accessLevel, &storedTrace, &attribution)
		if errors.Is(err, sql.ErrNoRows) {
			return ports.ErrObservationNotFound
		}
		if err != nil {
			return fmt.Errorf("get observation: %w", err)
		}
		return nil
	})
	if err != nil {
		return observation.Event{}, err
	}
	stored.TenantID = tenantID
	stored.SourceEventID = sourceEvent.String
	stored.SessionID, stored.ConversationID, stored.ParentEventID = session.String, conversation.String, parent.String
	stored.Payload, stored.AccessLevel = storedPayload, observation.AccessLevel(accessLevel)
	stored.Attribution.Level = observation.AttributionLevel(attribution)
	if err := json.Unmarshal(storedTrace, &stored.Trace); err != nil {
		return observation.Event{}, err
	}
	stored.Attribution.Limitations = observation.AssessAttribution(stored.SessionID, stored.ConversationID, stored.ParentEventID, stored.Trace).Limitations
	if sequence.Valid {
		stored.Sequence = &sequence.Int64
	}
	return stored, nil
}

func sameJSON(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

// StoreObservation writes the raw event and its durable normalization job in one tenant transaction.
func (r *ObservationRepository) StoreObservation(ctx context.Context, event observation.Event) (observation.Event, bool, error) {
	if err := event.Validate(); err != nil {
		return observation.Event{}, false, err
	}
	payload := event.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	traceJSON, err := json.Marshal(event.Trace)
	if err != nil {
		return observation.Event{}, false, err
	}
	if event.AccessLevel == "" {
		event.AccessLevel = observation.Level0
	}
	if event.Attribution.Level == "" {
		event.Attribution = observation.AssessAttribution(event.SessionID, event.ConversationID, event.ParentEventID, event.Trace)
	}
	var stored observation.Event
	var storedPayload, storedTrace []byte
	var sequence sql.NullInt64
	var parent, session, conversation, sourceEvent sql.NullString
	var accessLevel, attribution string
	var created bool
	err = r.router.WithTenantTx(ctx, event.TenantID, func(tx *sql.Tx) error {
		const columns = `id::text, COALESCE(source_event_id, ''), idempotency_key, COALESCE(session_id::text, ''), COALESCE(conversation_id, ''), source_type, source_id, message_type, payload, COALESCE(payload_text, ''), sequence, COALESCE(parent_event_id::text, ''), occurred_at, visibility, reliability, retention_class, access_level, trace_metadata, attribution_level`
		const scan = `SELECT `
		rowErr := tx.QueryRowContext(ctx, `
			INSERT INTO observed_events
				(id, source_event_id, idempotency_key, session_id, conversation_id, source_type, source_id, message_type, payload, payload_text, sequence, parent_event_id, occurred_at, visibility, reliability, retention_class, access_level, trace_metadata, attribution_level)
			VALUES ($1::uuid, NULLIF($2, ''), $3, NULLIF($4, '')::uuid, NULLIF($5, ''), $6, $7, $8, $9::jsonb, NULLIF($10, ''), $11, NULLIF($12, '')::uuid, $13, $14, $15, $16, $17, $18::jsonb, $19)
			ON CONFLICT (idempotency_key) DO NOTHING
			RETURNING `+columns,
			event.ID, event.SourceEventID, event.IdempotencyKey, event.SessionID, event.ConversationID, event.SourceType,
			event.SourceID, event.MessageType, []byte(payload), event.PayloadText, event.Sequence, event.ParentEventID,
			event.OccurredAt, event.Visibility, event.Reliability, event.RetentionClass, event.AccessLevel, traceJSON, event.Attribution.Level,
		).Scan(&stored.ID, &sourceEvent, &stored.IdempotencyKey, &session, &conversation, &stored.SourceType,
			&stored.SourceID, &stored.MessageType, &storedPayload, &stored.PayloadText, &sequence, &parent,
			&stored.OccurredAt, &stored.Visibility, &stored.Reliability, &stored.RetentionClass, &accessLevel, &storedTrace, &attribution)
		created = rowErr == nil
		if errors.Is(rowErr, sql.ErrNoRows) {
			rowErr = tx.QueryRowContext(ctx, scan+columns+` FROM observed_events WHERE idempotency_key = $1`, event.IdempotencyKey).Scan(
				&stored.ID, &sourceEvent, &stored.IdempotencyKey, &session, &conversation, &stored.SourceType, &stored.SourceID,
				&stored.MessageType, &storedPayload, &stored.PayloadText, &sequence, &parent, &stored.OccurredAt,
				&stored.Visibility, &stored.Reliability, &stored.RetentionClass, &accessLevel, &storedTrace, &attribution)
		}
		if rowErr != nil {
			return fmt.Errorf("store observation: %w", rowErr)
		}
		stored.SourceEventID = sourceEvent.String
		if stored.SourceType != event.SourceType || stored.SourceID != event.SourceID || stored.SourceEventID != event.SourceEventID ||
			stored.MessageType != event.MessageType || !sameJSON(storedPayload, payload) || accessLevel != string(event.AccessLevel) ||
			attribution != string(event.Attribution.Level) || !sameJSON(storedTrace, traceJSON) {
			return errors.New("observation idempotency conflict")
		}
		if created {
			jobPayload, err := json.Marshal(struct {
				RawEventID string `json:"raw_event_id"`
			}{RawEventID: stored.ID})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO outbox_jobs (id, job_type, tenant_id, aggregate_id, idempotency_key, payload) VALUES ($1::uuid, 'normalize_event', $2::uuid, $1::uuid, $3, $4::jsonb)`, stored.ID, event.TenantID, "normalize:"+stored.ID, jobPayload); err != nil {
				return fmt.Errorf("enqueue normalize event: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return observation.Event{}, false, err
	}
	stored.TenantID = event.TenantID
	stored.SessionID, stored.ConversationID, stored.ParentEventID = session.String, conversation.String, parent.String
	stored.Payload, stored.AccessLevel = storedPayload, observation.AccessLevel(accessLevel)
	stored.Attribution.Level = observation.AttributionLevel(attribution)
	if err := json.Unmarshal(storedTrace, &stored.Trace); err != nil {
		return observation.Event{}, false, err
	}
	stored.Attribution.Limitations = observation.AssessAttribution(stored.SessionID, stored.ConversationID, stored.ParentEventID, stored.Trace).Limitations
	if sequence.Valid {
		stored.Sequence = &sequence.Int64
	}
	return stored, created, nil
}
