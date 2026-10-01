package analysis

import (
	"context"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/ports"
)

var (
	ErrInvalidBatch   = errors.New("analysis batch requires tenant, run, and versioned schema context")
	ErrSensitiveInput = errors.New("analysis batch contains prohibited sensitive input")
)

type Providers struct {
	Analyst          ports.MemoryAnalyst
	Classifier       ports.Classifier
	FailureAnalyzer  ports.FailureAnalyzer
	Consolidator     ports.Consolidator
	ConflictAnalyzer ports.ConflictAnalyzer
}

type Service struct {
	providers Providers
	privacy   PrivacyPolicy
}

func New(providers Providers) *Service { return &Service{providers: providers} }

func NewWithPrivacy(providers Providers, policy PrivacyPolicy) *Service {
	return &Service{providers: providers, privacy: policy}
}

func (s *Service) Analyze(ctx context.Context, batch ports.AnalysisBatch) (ports.AnalystResult, error) {
	if s == nil || batch.TenantID == "" || batch.RunID == "" || batch.PromptVersion == "" || batch.SchemaVersion == "" || (len(batch.Events) == 0 && len(batch.Candidates) == 0) {
		return ports.AnalystResult{}, ErrInvalidBatch
	}
	batch, err := PrepareBatch(batch, s.privacy)
	if err != nil {
		return ports.AnalystResult{}, err
	}
	if s.providers.Analyst != nil {
		result, err := s.providers.Analyst.Analyze(ctx, batch)
		if err != nil {
			return ports.AnalystResult{}, fmt.Errorf("memory analyst: %w", err)
		}
		if err := ValidateResult(batch, result); err != nil {
			return ports.AnalystResult{}, err
		}
		return result, nil
	}
	var result ports.AnalystResult
	if s.providers.Classifier != nil {
		result.Classifications, err = s.providers.Classifier.Classify(ctx, batch)
		if err != nil {
			return ports.AnalystResult{}, fmt.Errorf("classify events: %w", err)
		}
	}
	if s.providers.FailureAnalyzer != nil {
		result.Failures, err = s.providers.FailureAnalyzer.AnalyzeFailures(ctx, batch)
		if err != nil {
			return ports.AnalystResult{}, fmt.Errorf("analyze failures: %w", err)
		}
	}
	if s.providers.Consolidator != nil {
		result.Candidates, err = s.providers.Consolidator.Consolidate(ctx, batch)
		if err != nil {
			return ports.AnalystResult{}, fmt.Errorf("consolidate events: %w", err)
		}
	}
	if s.providers.ConflictAnalyzer != nil && len(result.Candidates) > 0 {
		result.Conflicts, err = s.providers.ConflictAnalyzer.AnalyzeConflicts(ctx, batch, result.Candidates)
		if err != nil {
			return ports.AnalystResult{}, fmt.Errorf("analyze conflicts: %w", err)
		}
	}
	if err := ValidateResult(batch, result); err != nil {
		return ports.AnalystResult{}, err
	}
	return result, nil
}
