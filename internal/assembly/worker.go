package assembly

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hchw/mengpo/internal/adapters/embedding"
	"github.com/hchw/mengpo/internal/adapters/nats"
	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/application/analysis"
	embeddingapp "github.com/hchw/mengpo/internal/application/embedding"
	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/observability"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
	"github.com/hchw/mengpo/internal/workers"
	natsgo "github.com/nats-io/nats.go"
)

// NotificationSubject is the NATS subject used for best-effort worker wake-ups.
const NotificationSubject = "memory.jobs"

// TenantSource returns the tenants a worker should poll. It is refreshed so
// provisioning and suspension are picked up without a restart.
type TenantSource interface {
	ActiveTenants(ctx context.Context) ([]string, error)
}

// EmbeddingRunner drains one tenant's embedding jobs.
type EmbeddingRunner interface {
	ProcessBatch(ctx context.Context, tenantID string) (int, error)
}

// WorkerOptions inject the pieces a worker process needs. Optional fields
// degrade the worker instead of failing: without a Notifier it polls
// PostgreSQL only, and without an EmbeddingRunner it skips embedding work.
type WorkerOptions struct {
	DB              *sql.DB
	Tenants         TenantSource
	Outbox          ports.OutboxRepository
	Notifier        ports.PubSub
	Embedding       EmbeddingRunner
	WorkerID        string
	PollInterval    time.Duration
	EmbeddingPeriod time.Duration
	Handle          workers.JobHandler
	Logger          *slog.Logger
}

// Worker runs the durable job loop (outbox + embedding) with graceful shutdown.
type Worker struct {
	options WorkerOptions
	logger  *slog.Logger
}

// newWorkerFromConfig builds the worker for the process entrypoint, enabling the
// local embedding loop only when the embedding provider is configured.
func newWorkerFromConfig(cfg config.Config, db *sql.DB) (*Worker, error) {
	var embedder ports.Embedder
	if cfg.Providers.Embedding.Enabled {
		local, err := embedding.NewLlamaCPP(cfg.Providers.Embedding)
		if err != nil {
			return nil, err
		}
		embedder = local
	}
	return NewWorker(cfg, db, embedder)
}

// NewWorker builds a worker from a live database and optional embedder.
func NewWorker(cfg config.Config, db *sql.DB, embedder ports.Embedder) (*Worker, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	router := tenantdb.NewRouter(db, registry.NewStore(db))
	store := registry.NewStore(db)
	options := WorkerOptions{
		DB:       db,
		Tenants:  store,
		Outbox:   postgres.NewOutboxRepository(router),
		WorkerID: workerIdentity(),
		Logger:   observability.NewLogger(os.Stderr),
	}
	if cfg.Queue.Adapter == "nats-core" && cfg.Queue.URL != "" {
		connection, err := natsgo.Connect(cfg.Queue.URL, natsgo.Name("memory-worker"))
		if err != nil {
			return nil, fmt.Errorf("connect nats: %w", err)
		}
		adapter, err := nats.New(connection, NotificationSubject)
		if err != nil {
			connection.Close()
			return nil, err
		}
		options.Notifier = adapter
	}
	if embedder != nil {
		embeddings := postgres.NewEmbeddingRepository(router)
		options.Embedding = embeddingapp.NewWorker(embeddings, embeddings, embedder, 0, 0)
	}
	dispatcher := &JobDispatcher{
		Observations:         postgres.NewObservationRepository(router),
		Normalized:           postgres.NewNormalizedEventRepository(router),
		Analysis:             analysis.New(analysis.Providers{Analyst: analysis.RuleFallback{}}),
		SchemaVersion:        "schema-v1",
		NormalizationVersion: "normalize-v1",
		NewID:                newUUID,
	}
	options.Handle = dispatcher.Handle
	return NewWorkerWithOptions(options)
}

// NewWorkerWithOptions builds a worker from explicit dependencies. It exists to
// keep the process lifecycle testable without a live broker or embedder.
func NewWorkerWithOptions(options WorkerOptions) (*Worker, error) {
	if options.DB == nil || options.Tenants == nil || options.Outbox == nil {
		return nil, errors.New("worker requires database, tenant source and outbox repository")
	}
	logger := options.Logger
	if logger == nil {
		logger = observability.NewLogger(os.Stderr)
	}
	if options.WorkerID == "" {
		options.WorkerID = workerIdentity()
	}
	if options.PollInterval <= 0 {
		options.PollInterval = time.Second
	}
	if options.EmbeddingPeriod <= 0 {
		options.EmbeddingPeriod = 5 * time.Second
	}
	if options.Handle == nil {
		options.Handle = func(_ context.Context, job ports.OutboxJob) error {
			return fmt.Errorf("%w: %q", ErrUnsupportedJobType, job.JobType)
		}
	}
	return &Worker{options: options, logger: logger}, nil
}

// Run starts the outbox runner and, when configured, the embedding loop, then
// blocks until the context is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	tenants, err := w.options.Tenants.ActiveTenants(ctx)
	if err != nil {
		return fmt.Errorf("resolve active tenants: %w", err)
	}
	w.logger.Info("memory worker starting", slog.Int("tenants", len(tenants)))

	runner := &workers.OutboxRunner{
		Repository:   w.options.Outbox,
		Notifier:     w.options.Notifier,
		Tenants:      tenants,
		WorkerID:     w.options.WorkerID,
		PollInterval: w.options.PollInterval,
		Handle:       w.options.Handle,
	}
	errs := make(chan error, 2)
	if len(tenants) > 0 {
		go func() { errs <- runner.Run(ctx) }()
	}
	if w.options.Embedding != nil {
		go func() { errs <- w.runEmbeddingLoop(ctx, tenants) }()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		return err
	}
}

func (w *Worker) runEmbeddingLoop(ctx context.Context, tenants []string) error {
	ticker := time.NewTicker(w.options.EmbeddingPeriod)
	defer ticker.Stop()
	for {
		for _, tenantID := range tenants {
			if _, err := w.options.Embedding.ProcessBatch(ctx, tenantID); err != nil && ctx.Err() == nil {
				w.logger.Error("embedding batch failed", slog.String("tenant", tenantID), slog.String("error", err.Error()))
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func workerIdentity() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "memory-worker"
	}
	return host + "-" + fmt.Sprintf("%d", os.Getpid())
}
