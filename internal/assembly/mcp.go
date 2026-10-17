package assembly

import (
	"context"
	"net/http"

	"github.com/hchw/mengpo/internal/api/dto"
	httpapi "github.com/hchw/mengpo/internal/api/httpapi"
	"github.com/hchw/mengpo/internal/api/mcp"
	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/domain/observation"
)

// mcpUseCases bridges the MCP transport boundary to the application command
// surface. It rebuilds the trusted agent identity the application expects and
// injects it into the context; the MCP package itself never imports the
// application layer.
type mcpUseCases struct {
	service *agentaccess.Service
}

func (u mcpUseCases) bind(ctx context.Context) (context.Context, error) {
	identity, ok := mcpserver.IdentityFromContext(ctx)
	if !ok || identity.TenantID == "" || identity.UserID == "" {
		return ctx, agentaccess.ErrNoIdentity
	}
	return agentaccess.WithIdentity(ctx, agentaccess.Identity{
		TenantID:     identity.TenantID,
		UserID:       identity.UserID,
		AgentID:      identity.AgentID,
		SourceID:     identity.AgentID,
		Capabilities: identity.Capabilities,
		Source:       agentaccess.SourceAgentContext,
		AccessLevel:  observation.Level1,
	}), nil
}

func (u mcpUseCases) Observe(ctx context.Context, envelope dto.Envelope) (any, error) {
	ctx, err := u.bind(ctx)
	if err != nil {
		return nil, err
	}
	return u.service.Observe(ctx, envelope)
}

func (u mcpUseCases) Project(ctx context.Context, envelope dto.Envelope) (any, error) {
	ctx, err := u.bind(ctx)
	if err != nil {
		return nil, err
	}
	return u.service.Project(ctx, envelope)
}

func (u mcpUseCases) Consolidate(ctx context.Context, envelope dto.Envelope) (any, error) {
	ctx, err := u.bind(ctx)
	if err != nil {
		return nil, err
	}
	return u.service.Consolidate(ctx, envelope)
}

func (u mcpUseCases) Feedback(ctx context.Context, envelope dto.Envelope) (any, error) {
	ctx, err := u.bind(ctx)
	if err != nil {
		return nil, err
	}
	return u.service.Feedback(ctx, envelope)
}

func (u mcpUseCases) Session(ctx context.Context, envelope dto.Envelope) (any, error) {
	ctx, err := u.bind(ctx)
	if err != nil {
		return nil, err
	}
	return u.service.Session(ctx, envelope)
}

// newMCPHandler assembles the MCP handler from the shared credential resolver
// and the application command surface.
func newMCPHandler(service *agentaccess.Service, agents AgentCredentialLookup) http.Handler {
	if service == nil {
		return nil
	}
	return (&mcpserver.Handler{
		UseCases: mcpUseCases{service: service},
		Resolve:  mcpResolve(agents),
	}).HTTPHandler()
}

// mcpHandlerFor resolves the concrete application service behind the HTTP
// boundary so MCP can drive the same command surface. A non-agentaccess
// implementation simply disables the MCP endpoint.
func mcpHandlerFor(useCases httpapi.UseCases, agents AgentCredentialLookup) http.Handler {
	service, ok := useCases.(*agentaccess.Service)
	if !ok {
		return nil
	}
	return newMCPHandler(service, agents)
}

// mcpResolve reuses the single "Bearer secret -> trusted identity" path. An
// agent bound to multiple users has no way to declare which one it acts for at
// connection time, so it is rejected here.
func mcpResolve(agents AgentCredentialLookup) mcpserver.ResolveFunc {
	return func(ctx context.Context, credential string) (mcpserver.Identity, error) {
		identity, err := resolveAgentCredential(ctx, agents, credential, "")
		if err != nil {
			return mcpserver.Identity{}, err
		}
		return mcpserver.Identity{
			TenantID:     identity.TenantID,
			UserID:       identity.UserID,
			AgentID:      identity.AgentID,
			Capabilities: identity.Capabilities,
		}, nil
	}
}
