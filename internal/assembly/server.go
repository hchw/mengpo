package assembly

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/hchw/mengpo/internal/adapters/embedding"
	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/api/httpapi"
	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/governance"
	"github.com/hchw/mengpo/internal/application/identity"
	"github.com/hchw/mengpo/internal/application/observation"
	"github.com/hchw/mengpo/internal/application/projection"
	"github.com/hchw/mengpo/internal/application/recall"
	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/observability"
	"github.com/hchw/mengpo/internal/platform/registry"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// Authenticator resolves a trusted Identity from an inbound HTTP request. It is
// the single bridge between transport credentials and application identity;
// implementations must never trust body claims.
type Authenticator interface {
	Authenticate(r *http.Request) (agentaccess.Identity, error)
}

// BuildUseCases composes every repository and application service into the HTTP
// boundary. It requires a live *sql.DB because repositories resolve tenant
// schemas through the platform registry.
func BuildUseCases(cfg config.Config, db *sql.DB) (httpapi.UseCases, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	router := tenantdb.NewRouter(db, registry.NewStore(db))
	observations := postgres.NewObservationRepository(router)
	sessions := postgres.NewSessionRepository(router)
	memories := postgres.NewMemoryRepository(router)
	search := postgres.NewSearchRepository(router)
	relations := postgres.NewRelationRepository(router)
	feedback := postgres.NewFeedbackRepository(router)
	embeddings := postgres.NewEmbeddingRepository(router)
	projections := postgres.NewProjectionRepository(router)
	outbox := postgres.NewOutboxRepository(router)
	quarantine := postgres.NewQuarantineRepository(db)

	gateway := observation.NewGateway(observations)
	// The query vector channel needs the same embedder identity the worker used
	// to store vectors; without an enabled provider recall degrades to
	// structured plus full-text instead of failing.
	var embedder ports.Embedder
	if cfg.Providers.Embedding.Enabled {
		local, err := embedding.NewLlamaCPP(cfg.Providers.Embedding)
		if err != nil {
			return nil, fmt.Errorf("configure embedding provider: %w", err)
		}
		embedder = local
	}
	recallService := recall.NewService(search, relations, memories, embeddings, embedder)
	pipeline := projection.NewPipeline(projection.PipelinePolicy{
		Timeout:                2 * time.Second,
		CacheTTL:               30 * time.Second,
		DegradeOnDatabaseError: true,
		RankingContext:         recall.RankingContext{IncludeCandidates: true, MaxCandidates: defaultRankingBudget},
	}, recallService, nil, projection.NewPersistence(projections))
	orchestrator := projection.NewOrchestrator(projection.Policy{
		AllowDivergenceHint:      true,
		AllowWeakCandidateRecall: true,
		MaxCandidateBudget:       defaultCandidateBudget * 5,
		MaxRankingBudget:         defaultRankingBudget * 5,
		MaxInjectionTokenBudget:  defaultInjectionTokenBudget * 4,
		MaxRelationDepth:         4,
	})
	return agentaccess.NewService(agentaccess.ServiceOptions{
		Gateway:      gateway,
		Sessions:     sessions,
		Projector:    &Projector{Orchestrator: orchestrator, Pipeline: pipeline},
		Feedback:     &FeedbackRecorder{Repository: feedback},
		Consolidator: &Consolidator{Jobs: outbox, NewID: newUUID},
		Quarantine:   quarantine,
		Projections:  projections,
	}), nil
}

// NewHandler mounts the HTTP surface: health, metrics, console authentication
// and the tenant-bound /api/v1 command endpoints.
func NewHandler(cfg config.Config, useCases httpapi.UseCases, auth Authenticator, authAPI *AuthAPI, consoleAPI *ConsoleAPI, ops *ConsoleOps, readiness func(context.Context) error, logger *slog.Logger, metrics *observability.Metrics) http.Handler {
	if logger == nil {
		logger = observability.NewLogger(os.Stderr)
	}
	if metrics == nil {
		metrics = &observability.Metrics{}
	}
	mux := http.NewServeMux()
	mux.Handle("/", observability.HealthHandler(readiness))
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/api/v1/", httpapi.NewHandler(useCases, cfg.Security.MaxRequestBytes))
	if authAPI != nil {
		authAPI.Register(mux)
	}
	if consoleAPI != nil {
		consoleAPI.Register(mux)
	}
	if ops != nil {
		ops.Register(mux)
	}
	return metrics.Middleware(logger, authMiddleware(auth, mux))
}

func authMiddleware(auth Authenticator, next http.Handler) http.Handler {
	if auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := auth.Authenticate(r)
		if err == nil && identity.TenantID != "" {
			ctx := agentaccess.WithIdentity(r.Context(), identity)
			ctx = observability.WithTenant(ctx, identity.TenantID)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// Server owns the assembled HTTP process lifecycle.
type Server struct {
	cfg     config.Config
	db      *sql.DB
	logger  *slog.Logger
	metrics *observability.Metrics
	handler http.Handler
}

// NewServer assembles the API process from configuration and a database handle.
func NewServer(cfg config.Config, db *sql.DB) (*Server, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	useCases, err := BuildUseCases(cfg, db)
	if err != nil {
		return nil, err
	}
	identityRepo := postgres.NewIdentityRepository(db)
	var verifier identity.SSOVerifier
	if cfg.Environment != "production" {
		// The internal verifier trusts the assertion as a verified email subject
		// and exists for development only; production must inject OIDC/JWT.
		verifier = identity.InternalVerifier{}
	}
	identityService, err := identity.NewService(identity.Options{
		Users:       identityRepo,
		Memberships: identityRepo,
		Sessions:    identityRepo,
		Verifier:    verifier,
		SessionTTL:  cfg.Auth.SessionTTL,
	})
	if err != nil {
		return nil, err
	}
	logger := observability.NewLogger(os.Stderr)
	metrics := &observability.Metrics{}
	readiness := func(ctx context.Context) error { return pingDB(ctx, db) }
	authAPI := &AuthAPI{Service: identityService, CookieName: cfg.Auth.SessionCookieName, CookieSecure: cfg.Auth.SessionCookieSecure}
	authenticator := SessionAuthenticator{Service: identityService, CookieName: cfg.Auth.SessionCookieName, Memberships: identityRepo}
	router := tenantdb.NewRouter(db, registry.NewStore(db))
	memoryRepository := postgres.NewMemoryRepository(router)
	consoleAPI := &ConsoleAPI{
		Memories:   memoryRepository,
		List:       memoryRepository,
		Merge:      memoryRepository,
		Sessions:   postgres.NewSessionRepository(router),
		Governance: governance.NewService(memoryRepository),
		Members:    identityRepo,
		Agents:     postgres.NewAgentRepository(db),
	}
	providerService, err := NewProviderService(cfg, router, newProviderAnalystBuilder(cfg))
	if err != nil {
		return nil, err
	}
	ops := &ConsoleOps{
		Providers: providerService,
		Schedules: postgres.NewTenantScheduleStore(db),
		ScheduleStatus: func(ctx context.Context, tenantID string) ([]ports.ScheduleStatus, error) {
			return postgres.NewTenantScheduleStore(db).List(ctx, tenantID)
		},
		Runs:   postgres.NewAnalystRunRepository(router),
		Audit:  postgres.NewAuditRepository(router),
		Logger: logger,
	}
	return &Server{
		cfg:     cfg,
		db:      db,
		logger:  logger,
		metrics: metrics,
		handler: NewHandler(cfg, useCases, authenticator, authAPI, consoleAPI, ops, readiness, logger, metrics),
	}, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

// Run serves HTTP until the context is cancelled, then drains in-flight
// requests with a bounded shutdown timeout.
func (s *Server) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:              s.cfg.HTTPAddr,
		Handler:           s.handler,
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
	}
	shutdownErr := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr <- server.Shutdown(shutdownCtx)
	}()
	s.logger.Info("memory server listening", slog.String("addr", s.cfg.HTTPAddr))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if err := <-shutdownErr; err != nil {
		return err
	}
	return nil
}

func pingDB(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("database is not configured")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return db.PingContext(pingCtx)
}
