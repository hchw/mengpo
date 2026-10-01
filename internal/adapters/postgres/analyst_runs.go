package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// AnalystRunRepository records curated analysis runs and maintenance cursors in
// the caller's tenant schema.
type AnalystRunRepository struct {
	router *tenantdb.Router
}

func NewAnalystRunRepository(router *tenantdb.Router) *AnalystRunRepository {
	return &AnalystRunRepository{router: router}
}

// StartAnalysisRun opens a run record. It is idempotent on the correlation id:
// replaying the same run inserts nothing and returns false.
func (r *AnalystRunRepository) StartAnalysisRun(ctx context.Context, tenantID string, record ports.AnalysisRunRecord) (bool, error) {
	if record.RunID == "" || record.TaskType == "" {
		return false, errors.New("analysis run requires correlation id and task type")
	}
	if record.ID == "" {
		return false, errors.New("analysis run requires an id")
	}
	status := record.Status
	if status == "" {
		status = ports.AnalysisRunRunning
	}
	inputEvents, err := json.Marshal(record.InputEventIDs)
	if err != nil {
		return false, fmt.Errorf("encode analysis run inputs: %w", err)
	}
	inserted := false
	err = r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
INSERT INTO analysis_jobs (
	id, correlation_id, outbox_job_id, run_id, session_id, task_type, provider, model, prompt_version,
	schema_version, input_watermark, input_event_ids, trigger_source, priority, status, attempts, created_at, updated_at
 ) VALUES ($1, $2, NULL, NULL, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, $13, 0, now(), now())
ON CONFLICT (correlation_id) DO NOTHING`,
			record.ID, record.RunID, nullable(record.SessionID), record.TaskType, record.Provider,
			record.Model, record.PromptVersion, record.SchemaVersion, record.InputWatermark,
			string(inputEvents), record.Trigger, record.Priority, status)
		if err != nil {
			return fmt.Errorf("insert analysis run: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check analysis run insert: %w", err)
		}
		inserted = rows > 0
		return nil
	})
	return inserted, err
}

// FinishAnalysisRun writes a run's terminal fields.
func (r *AnalystRunRepository) FinishAnalysisRun(ctx context.Context, tenantID, runID string, update ports.AnalysisRunUpdate) (bool, error) {
	if runID == "" {
		return false, errors.New("analysis run correlation id is required")
	}
	if update.Status == "" {
		return false, errors.New("analysis run update requires a status")
	}
	updated := false
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
UPDATE analysis_jobs
SET status = $2, attempts = $3, last_error = $4, latency_ms = $5, tokens_prompt = $6,
	tokens_completion = $7, cost_usd = $8, candidate_count = $9, discarded_count = $10,
	conflict_count = $11, degraded_reason = $12, result = $13::jsonb, produced_memory_ids = $14::jsonb,
	updated_at = now()
WHERE correlation_id = $1`,
			runID, update.Status, update.Attempts, update.LastError, update.LatencyMS,
			update.TokensPrompt, update.TokensCompletion, update.CostUSD, update.CandidateCount,
			update.DiscardedCount, update.ConflictCount, update.DegradedReason,
			resultJSON(update.Result), outputIDsJSON(update.OutputMemoryIDs))
		if err != nil {
			return fmt.Errorf("finish analysis run: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check analysis run update: %w", err)
		}
		updated = rows > 0
		return nil
	})
	return updated, err
}

// LinkRunOutputs records the memory ids a run produced.
func (r *AnalystRunRepository) LinkRunOutputs(ctx context.Context, tenantID, runID string, memoryIDs []string) (bool, error) {
	if runID == "" || len(memoryIDs) == 0 {
		return false, nil
	}
	linked := false
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE analysis_jobs SET produced_memory_ids = $2::jsonb, updated_at = now() WHERE correlation_id = $1`, runID, string(outputIDsJSON(memoryIDs)))
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		linked = rows > 0
		return nil
	})
	return linked, err
}

// ListAnalysisRuns returns run records matching the filter, newest first.
func resultJSON(result json.RawMessage) []byte {
	if len(result) == 0 {
		return []byte("null")
	}
	return []byte(result)
}

func outputIDsJSON(ids []string) []byte {
	if ids == nil {
		ids = []string{}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return []byte("[]")
	}
	return encoded
}

func (r *AnalystRunRepository) ListAnalysisRuns(ctx context.Context, tenantID string, filter ports.AnalysisRunFilter) ([]ports.AnalysisRunView, error) {
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	var conditions []string
	var args []any
	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(args)))
	}
	if filter.SessionID != "" {
		add("session_id = $%d", filter.SessionID)
	}
	if filter.TaskType != "" {
		add("task_type = $%d", filter.TaskType)
	}
	if filter.Status != "" {
		add("status = $%d", filter.Status)
	}
	if filter.Since != nil {
		add("created_at >= $%d", filter.Since.UTC())
	}
	if filter.Until != nil {
		add("created_at < $%d", filter.Until.UTC())
	}
	query := `SELECT id, COALESCE(correlation_id, outbox_job_id::text, ''), COALESCE(session_id::text, ''), task_type, COALESCE(trigger_source, 'event'), provider, model,
	prompt_version, schema_version, input_watermark, COALESCE(input_event_ids, '[]'::jsonb), priority, status, attempts, last_error,
	latency_ms, tokens_prompt, tokens_completion, cost_usd, candidate_count, discarded_count, conflict_count, degraded_reason, created_at, updated_at, result
	FROM analysis_jobs`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, pageSize, (page-1)*pageSize)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	var views []ports.AnalysisRunView
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var view ports.AnalysisRunView
			var sessionID, lastError string
			var watermark *int64
			var inputEvents []byte
			if err := rows.Scan(
				&view.ID, &view.RunID, &sessionID, &view.TaskType, &view.Trigger, &view.Provider, &view.Model,
				&view.PromptVersion, &view.SchemaVersion, &watermark, &inputEvents, &view.Priority, &view.Status,
				&view.Attempts, &lastError, &view.LatencyMS, &view.TokensPrompt, &view.TokensCompletion, &view.CostUSD,
				&view.CandidateCount, &view.DiscardedCount, &view.ConflictCount, &view.DegradedReason, &view.CreatedAt, &view.UpdatedAt, &view.Result,
			); err != nil {
				return err
			}
			view.SessionID = sessionID
			view.LastError = lastError
			view.InputWatermark = watermark
			if len(inputEvents) > 0 {
				_ = json.Unmarshal(inputEvents, &view.InputEventIDs)
			}
			views = append(views, view)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return views, nil
}

// LoadMaintenanceCursor returns the watermark for a tenant task, defaulting to 0.
func (r *AnalystRunRepository) LoadMaintenanceCursor(ctx context.Context, tenantID, taskType string) (int64, error) {
	var watermark int64
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT watermark FROM maintenance_cursors WHERE task_type = $1`, taskType).Scan(&watermark)
		if errors.Is(err, sql.ErrNoRows) {
			watermark = 0
			return nil
		}
		return err
	})
	return watermark, err
}

// SaveMaintenanceCursor records how far a tenant task has processed.
func (r *AnalystRunRepository) SaveMaintenanceCursor(ctx context.Context, tenantID, taskType string, watermark int64) error {
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO maintenance_cursors (task_type, watermark, updated_at) VALUES ($1, $2, now())
ON CONFLICT (task_type) DO UPDATE SET watermark = GREATEST(maintenance_cursors.watermark, EXCLUDED.watermark), updated_at = now()`,
			taskType, watermark)
		return err
	})
}

var (
	_ ports.AnalysisRunStore       = (*AnalystRunRepository)(nil)
	_ ports.MaintenanceCursorStore = (*AnalystRunRepository)(nil)
)
