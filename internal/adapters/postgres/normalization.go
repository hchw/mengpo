package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// NormalizedEventRepository persists normalized observations derived from raw
// events. It never creates memory nodes: rule-based normalization only enriches
// evidence, and candidate creation requires an analyst.
type NormalizedEventRepository struct{ router *tenantdb.Router }

func NewNormalizedEventRepository(router *tenantdb.Router) *NormalizedEventRepository {
	return &NormalizedEventRepository{router: router}
}

// StoreNormalizedEvents upserts normalized events in one tenant transaction.
// raw_event_id is unique, so a redelivered normalization job rewrites the same
// row instead of duplicating evidence.
func (r *NormalizedEventRepository) StoreNormalizedEvents(ctx context.Context, tenantID string, events []ports.NormalizedEventRecord) error {
	if tenantID == "" || len(events) == 0 {
		return ports.ErrInvalidNormalizedEvent
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		for _, event := range events {
			if event.ID == "" || event.RawEventID == "" || event.SchemaVersion == "" || event.NormalizationVersion == "" {
				return ports.ErrInvalidNormalizedEvent
			}
			payload := event.Payload
			if len(payload) == 0 {
				payload = json.RawMessage(`{}`)
			}
			if !json.Valid(payload) {
				return ports.ErrInvalidNormalizedEvent
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO normalized_events (id, raw_event_id, schema_version, normalized_payload, normalization_version, sequence, parent_event_id)
				VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6, NULLIF($7, '')::uuid)
				ON CONFLICT (raw_event_id) DO UPDATE SET
					normalized_payload = EXCLUDED.normalized_payload,
					schema_version = EXCLUDED.schema_version,
					normalization_version = EXCLUDED.normalization_version,
					sequence = EXCLUDED.sequence,
					parent_event_id = EXCLUDED.parent_event_id,
					updated_at = now()`,
				event.ID, event.RawEventID, event.SchemaVersion, []byte(payload), event.NormalizationVersion, event.Sequence, event.ParentEventID)
			if err != nil {
				return fmt.Errorf("store normalized event: %w", err)
			}
		}
		return nil
	})
}

// MarkRawEventProcessed advances the normalization status of one raw event.
func (r *NormalizedEventRepository) MarkRawEventProcessed(ctx context.Context, tenantID, rawEventID string) error {
	if tenantID == "" || rawEventID == "" {
		return ports.ErrInvalidNormalizedEvent
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE normalized_events SET processing_status = 'processed', updated_at = now() WHERE raw_event_id = $1::uuid`, rawEventID); err != nil {
			return fmt.Errorf("mark raw event processed: %w", err)
		}
		return nil
	})
}

// ListPendingAnalysisEvents returns normalized events in stable order, starting
// at offset. Maintenance uses the offset as its cursor so replays are stable.
func (r *NormalizedEventRepository) ListPendingAnalysisEvents(ctx context.Context, tenantID string, offset, limit int) ([]ports.PendingAnalysisEvent, error) {
	if tenantID == "" || limit <= 0 {
		return nil, ports.ErrInvalidNormalizedEvent
	}
	var result []ports.PendingAnalysisEvent
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
SELECT ne.id::text, ne.raw_event_id::text, COALESCE(oe.session_id::text, ''), oe.occurred_at, ne.normalized_payload, ne.sequence
FROM normalized_events ne
JOIN observed_events oe ON oe.id = ne.raw_event_id
ORDER BY ne.created_at, ne.id
OFFSET $1 LIMIT $2`, offset, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ports.PendingAnalysisEvent
			var sequence sql.NullInt64
			var payload []byte
			if err := rows.Scan(&item.NormalizedID, &item.RawEventID, &item.SessionID, &item.OccurredAt, &payload, &sequence); err != nil {
				return err
			}
			item.Payload = json.RawMessage(payload)
			if sequence.Valid {
				value := sequence.Int64
				item.Sequence = &value
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

// LoadAnalysisEventsByRawIDs returns the normalized events for the given raw
// event ids, preserving the input order where possible.
func (r *NormalizedEventRepository) LoadAnalysisEventsByRawIDs(ctx context.Context, tenantID string, rawEventIDs []string) ([]ports.PendingAnalysisEvent, error) {
	if tenantID == "" || len(rawEventIDs) == 0 {
		return nil, ports.ErrInvalidNormalizedEvent
	}
	var result []ports.PendingAnalysisEvent
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
SELECT ne.id::text, ne.raw_event_id::text, COALESCE(oe.session_id::text, ''), oe.occurred_at, ne.normalized_payload, ne.sequence
FROM normalized_events ne
JOIN observed_events oe ON oe.id = ne.raw_event_id
WHERE ne.raw_event_id = ANY($1::uuid[])`, uuidArrayLiteral(rawEventIDs))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ports.PendingAnalysisEvent
			var sequence sql.NullInt64
			var payload []byte
			if err := rows.Scan(&item.NormalizedID, &item.RawEventID, &item.SessionID, &item.OccurredAt, &payload, &sequence); err != nil {
				return err
			}
			item.Payload = json.RawMessage(payload)
			if sequence.Valid {
				value := sequence.Int64
				item.Sequence = &value
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

var _ ports.NormalizedEventReader = (*NormalizedEventRepository)(nil)
