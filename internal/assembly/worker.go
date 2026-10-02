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
	"github.com/hchw/mengpo/internal/adapters/llm"
	"github.com/hchw/mengpo/internal/adapters/nats"
	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/adapters/redis"
	"github.com/hchw/mengpo/internal/adapters/scheduler"
	"github.com/hchw/mengpo/internal/application/analysis"
	embeddingapp "github.com/hchw/mengpo/internal/application/embedding"
	"github.com/hchw/mengpo/internal/application/governance"
	"github.com/hchw/mengpo/internal/application/maintenance"
	"github.com/hchw/mengpo/internal/application/providerconfig"
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

// EmbeddingRunner drains one tenant's embedding jobs. RebuildForModel
// enqueues embedding jobs for memories whose stored embedding identity
// differs from the current embedder (including never-embedded pending ones).
type EmbeddingRunner interface {
	ProcessBatch(ctx context.Context, tenantID string) (int, error)
	RebuildForModel(ctx context.Context, tenantID string) (int, error)
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
	Runner          *workers.OutboxRunner
	Scheduler       ports.Scheduler
	Providers       *providerconfig.Service
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
	providerService, err := NewProviderService(cfg, router, newProviderAnalystBuilder(cfg))
	if err != nil {
		return nil, err
	}
	dynamic := &providerconfig.Dynamic{Service: providerService}
	providerService.Dynamic = dynamic
	promptVersion := "rule-v1"
	provider := ""
	if cfg.Providers.MemoryLLM.Enabled {
		promptVersion = llm.PromptVersion
		provider = "memory-llm"
	}
	dispatcher := &JobDispatcher{
		Observations:         postgres.NewObservationRepository(router),
		Normalized:           postgres.NewNormalizedEventRepository(router),
		Analysis:             analysis.NewWithPrivacy(analysis.Providers{Analyst: dynamic}, analysis.PrivacyPolicy{}),
		AnalysisReader:       postgres.NewNormalizedEventRepository(router),
		Candidates:           postgres.NewCandidateRepository(router),
		SessionOwner:         postgres.NewSessionRepository(router),
		Runs:                 postgres.NewAnalystRunRepository(router),
		Audit:                postgres.NewAuditRepository(router),
		SchemaVersion:        "schema-v1",
		NormalizationVersion: "normalize-v1",
		PromptVersion:        promptVersion,
		Provider:             provider,
		Model:                cfg.Providers.MemoryLLM.Model,
		NewID:                newUUID,
	}
	opsMetrics := observability.NewOpsMetrics()
	dispatcher.AnalysisMetrics = opsMetrics
	options.Handle = dispatcher.Handle
	options.Providers = providerService
	runner := &workers.OutboxRunner{
		Repository:   options.Outbox,
		Notifier:     options.Notifier,
		WorkerID:     options.WorkerID,
		PollInterval: options.PollInterval,
		Handle:       options.Handle,
	}
	options.Runner = runner
	if cfg.RedisURL != "" {
		if cache, err := redis.NewCacheFromURL(cfg.RedisURL); err == nil {
			dispatcher.Cache = cache
		}
	}
	if cfg.Scheduler.Adapter == "internal" {
		schedulerComponent := scheduler.New(scheduler.Options{
			Tenants:   store,
			Locker:    postgres.NewAdvisoryLocker(db),
			Overrides: postgres.NewTenantScheduleStore(db),
			Metrics:   opsMetrics,
			Logger:    options.Logger,
		})
		runsRepository := postgres.NewAnalystRunRepository(router)
		plan := &maintenance.Job{
			PlanName:  "consolidate",
			TaskType:  maintenance.TaskTypeConsolidate,
			BatchSize: cfg.Analysis.MaintenanceBatchSize,
			Reader:    postgres.NewNormalizedEventRepository(router),
			Enqueuer:  postgres.NewOutboxRepository(router),
			Cursors:   runsRepository,
		}
		if err := schedulerComponent.Register(plan, ports.Schedule{Name: plan.Name(), Cadence: cfg.Scheduler.DefaultInterval, Enabled: true}); err != nil {
			return nil, err
		}
		expiry := &maintenance.ExpiryJob{
			PlanName:   "expiry",
			Reader:     postgres.NewMemoryRepository(router),
			Governance: governance.NewService(postgres.NewMemoryRepository(router)),
			BatchSize:  cfg.Analysis.MaintenanceBatchSize,
		}
		if err := schedulerComponent.Register(expiry, ports.Schedule{Name: expiry.Name(), Cadence: cfg.Scheduler.DefaultInterval, Enabled: true}); err != nil {
			return nil, err
		}
		memoryRepository := postgres.NewMemoryRepository(router)
		dedupe := &maintenance.DedupeJob{PlanName: "dedupe", Reader: memoryRepository, Runs: runsRepository, BatchSize: cfg.Analysis.MaintenanceBatchSize}
		if err := schedulerComponent.Register(dedupe, ports.Schedule{Name: dedupe.Name(), Cadence: cfg.Scheduler.DefaultInterval, Enabled: true}); err != nil {
			return nil, err
		}
		conflict := &maintenance.ConflictJob{
			PlanName: "conflict", Reader: memoryRepository, Analysis: dispatcher.Analysis, Runs: runsRepository,
			PromptVersion: promptVersion, SchemaVersion: "schema-v1", BatchSize: cfg.Analysis.MaintenanceBatchSize,
		}
		if err := schedulerComponent.Register(conflict, ports.Schedule{Name: conflict.Name(), Cadence: cfg.Scheduler.DefaultInterval, Enabled: true}); err != nil {
			return nil, err
		}
		cleanup := &maintenance.CleanupJob{PlanName: "cleanup", Outbox: postgres.NewOutboxRepository(router), Runs: runsRepository}
		if err := schedulerComponent.Register(cleanup, ports.Schedule{Name: cleanup.Name(), Cadence: cfg.Scheduler.DefaultInterval, Enabled: true}); err != nil {
			return nil, err
		}
		evaluate := &maintenance.EvaluationJob{PlanName: "evaluation", Reader: memoryRepository, Runs: runsRepository}
		if err := schedulerComponent.Register(evaluate, ports.Schedule{Name: evaluate.Name(), Cadence: cfg.Scheduler.DefaultInterval, Enabled: true}); err != nil {
			return nil, err
		}
		options.Scheduler = schedulerComponent
	}
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

	// Memories created before the embedding pipeline sees them (or under a
	// different embedder identity) carry no embedding job; rebuild is
	// idempotent and only queues memories whose identity differs.
	if w.options.Embedding != nil {
		for _, tenantID := range tenants {
			enqueued, rebuildErr := w.options.Embedding.RebuildForModel(ctx, tenantID)
			if rebuildErr != nil {
				w.logger.Error("enqueue embedding rebuild", slog.String("tenant", tenantID), slog.String("error", rebuildErr.Error()))
				continue
			}
			if enqueued > 0 {
				w.logger.Info("enqueued embedding rebuild", slog.String("tenant", tenantID), slog.Int("memories", enqueued))
			}
		}
	}

	if w.options.Scheduler != nil {
		if err := w.options.Scheduler.Start(ctx); err != nil {
			return fmt.Errorf("start scheduler: %w", err)
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := w.options.Scheduler.Stop(stopCtx); err != nil {
				w.logger.Error("stop scheduler", slog.String("error", err.Error()))
			}
		}()
	}

	errs := make(chan error, 2)
	if w.options.Scheduler != nil {
		// The scheduler drives the durable queue and embedding one pass at a
		// time; PubSub wake-ups still trigger an immediate pass for latency.
		if err := w.options.Scheduler.Register(outboxPollJob{name: "outbox", runner: w.options.Runner}, ports.Schedule{Name: "outbox", Cadence: w.options.PollInterval, Enabled: true}); err != nil {
			return fmt.Errorf("register outbox plan: %w", err)
		}
		if w.options.Embedding != nil {
			if err := w.options.Scheduler.Register(embeddingPlan{name: "embedding", runner: w.options.Embedding}, ports.Schedule{Name: "embedding", Cadence: w.options.EmbeddingPeriod, Enabled: true}); err != nil {
				return fmt.Errorf("register embedding plan: %w", err)
			}
		}
		go w.runWakeups(ctx)
	} else {
		runner := w.options.Runner
		if runner == nil {
			runner = &workers.OutboxRunner{
				Repository:   w.options.Outbox,
				Notifier:     w.options.Notifier,
				WorkerID:     w.options.WorkerID,
				PollInterval: w.options.PollInterval,
				Handle:       w.options.Handle,
			}
		}
		runner.Tenants = tenants
		if len(tenants) > 0 {
			go func() { errs <- runner.Run(ctx) }()
		}
		if w.options.Embedding != nil {
			go func() { errs <- w.runEmbeddingLoop(ctx, tenants) }()
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		return err
	}
}

// runWakeups turns PubSub notifications into immediate single-pass polls so
// the durable queue keeps low latency even though it is scheduled, not looped.
func (w *Worker) runWakeups(ctx context.Context) {
	if w.options.Notifier == nil || w.options.Runner == nil {
		return
	}
	notifications, err := w.options.Notifier.Subscribe(ctx)
	if err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case notification, ok := <-notifications:
			if !ok {
				return
			}
			tenantID := notification.TenantID
			if tenantID == "" {
				continue
			}
			if err := w.options.Runner.PollTenant(ctx, tenantID); err != nil && ctx.Err() == nil {
				w.logger.Error("wake-up poll failed", slog.String("tenant", tenantID), slog.String("error", err.Error()))
			}
		}
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
