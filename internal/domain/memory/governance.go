package memory

import (
	"errors"
	"math"
)

var ErrInvalidGovernanceInput = errors.New("invalid governance input")

type GovernancePolicy struct {
	Version                     string
	MinimumActivationConfidence float64
	MinimumStableConfidence     float64
	MinimumIndependentSessions  int
	ReliabilityWeights          map[EvidenceReliability]float64
	AttributionWeights          map[Attribution]float64
}

func DefaultGovernancePolicy() GovernancePolicy {
	return GovernancePolicy{
		Version:                     "conservative-v1",
		MinimumActivationConfidence: 0.65,
		MinimumStableConfidence:     0.90,
		MinimumIndependentSessions:  2,
		ReliabilityWeights: map[EvidenceReliability]float64{
			ReliabilityHigh: 1, ReliabilityMedium: 0.75, ReliabilityLow: 0.4, ReliabilityUnknown: 0.2,
		},
		AttributionWeights: map[Attribution]float64{
			AttributionDirect: 1, AttributionCorrelated: 0.75, AttributionInferred: 0.5, AttributionUnknown: 0.25,
		},
	}
}

type GovernanceInput struct {
	VerifiedEvidence []Evidence
	ConfirmByUser    bool
	RejectByUser     bool
	ConflictDetected bool
	Expired          bool
}

type GovernanceDecision struct {
	Memory          Memory
	ConfidenceScore float64
	VerifiedEvents  int
	Reason          string
}

func (p GovernancePolicy) Validate() error {
	if p.Version == "" || !finiteUnitInterval(p.MinimumActivationConfidence) || !finiteUnitInterval(p.MinimumStableConfidence) ||
		p.MinimumStableConfidence < p.MinimumActivationConfidence || p.MinimumIndependentSessions < 2 {
		return ErrInvalidGovernanceInput
	}
	for _, reliability := range []EvidenceReliability{ReliabilityHigh, ReliabilityMedium, ReliabilityLow, ReliabilityUnknown} {
		if weight, ok := p.ReliabilityWeights[reliability]; !ok || !finiteUnitInterval(weight) {
			return ErrInvalidGovernanceInput
		}
	}
	for _, attribution := range []Attribution{AttributionDirect, AttributionCorrelated, AttributionInferred, AttributionUnknown} {
		if weight, ok := p.AttributionWeights[attribution]; !ok || !finiteUnitInterval(weight) {
			return ErrInvalidGovernanceInput
		}
	}
	return nil
}

// EvaluateGovernance consumes only evidence already validated by the repository
// or Evidence Validator. Retrieval counts are intentionally not an input and can
// never promote memory authority.
func EvaluateGovernance(memory Memory, input GovernanceInput, policy GovernancePolicy) (GovernanceDecision, error) {
	if err := policy.Validate(); err != nil || memory.Validate() != nil {
		return GovernanceDecision{}, ErrInvalidGovernanceInput
	}
	signals := 0
	for _, signal := range []bool{input.ConfirmByUser, input.RejectByUser, input.ConflictDetected, input.Expired} {
		if signal {
			signals++
		}
	}
	if signals > 1 {
		return GovernanceDecision{}, ErrInvalidGovernanceInput
	}
	confidence, eventCount, independentSessions, hasHighDirectEvidence, err := evidenceConfidence(memory.ID, input.VerifiedEvidence, policy)
	if err != nil {
		return GovernanceDecision{}, err
	}
	decision := GovernanceDecision{Memory: memory, ConfidenceScore: confidence, VerifiedEvents: eventCount}

	switch {
	case input.RejectByUser:
		if err := transitionTo(&decision.Memory, StatusRejected); err != nil {
			return GovernanceDecision{}, err
		}
		decision.Memory.DefaultRetrieval = false
		decision.Reason = "explicit_user_rejection"
		return decision, nil
	case input.Expired:
		if err := transitionTo(&decision.Memory, StatusExpired); err != nil {
			return GovernanceDecision{}, err
		}
		decision.Memory.DefaultRetrieval = false
		decision.Reason = "expired_by_policy"
		return decision, nil
	case input.ConflictDetected:
		if err := transitionTo(&decision.Memory, StatusConflicted); err != nil {
			return GovernanceDecision{}, err
		}
		decision.Memory.DefaultRetrieval = false
		decision.Reason = "conflicting_evidence"
		return decision, nil
	case input.ConfirmByUser:
		if decision.Memory.Status == StatusCandidate {
			if err := decision.Memory.Transition(StatusActive); err != nil {
				return GovernanceDecision{}, err
			}
		}
		if decision.Memory.Status != StatusStable {
			if err := decision.Memory.Transition(StatusStable); err != nil {
				return GovernanceDecision{}, err
			}
		}
		decision.Memory.Confidence = 1
		decision.Memory.DefaultRetrieval = true
		decision.Reason = "explicit_user_confirmation"
		return decision, nil
	}

	if confidence > decision.Memory.Confidence {
		decision.Memory.Confidence = confidence
	}
	if decision.Memory.Status == StatusCandidate && hasHighDirectEvidence && confidence >= policy.MinimumActivationConfidence {
		if err := decision.Memory.Transition(StatusActive); err != nil {
			return GovernanceDecision{}, err
		}
		decision.Memory.DefaultRetrieval = true
		decision.Reason = "verified_direct_evidence"
		return decision, nil
	}
	if decision.Memory.Status == StatusActive && hasHighDirectEvidence && confidence >= policy.MinimumStableConfidence && independentSessions >= policy.MinimumIndependentSessions {
		if err := decision.Memory.Transition(StatusStable); err != nil {
			return GovernanceDecision{}, err
		}
		decision.Memory.DefaultRetrieval = true
		decision.Reason = "independent_session_corroboration"
		return decision, nil
	}
	if decision.Memory.Status == StatusActive || decision.Memory.Status == StatusStable {
		decision.Memory.DefaultRetrieval = true
	} else {
		decision.Memory.DefaultRetrieval = false
	}
	decision.Reason = "evidence_insufficient_for_promotion"
	return decision, nil
}

func evidenceConfidence(memoryID string, evidence []Evidence, policy GovernancePolicy) (float64, int, int, bool, error) {
	type eventEvidence struct {
		score      float64
		highDirect bool
		sessionID  string
	}
	byEvent := make(map[string]eventEvidence, len(evidence))
	for _, item := range evidence {
		if item.ID == "" || item.MemoryID != memoryID || item.RawEventID == "" || !finiteUnitInterval(item.Confidence) {
			return 0, 0, 0, false, ErrInvalidGovernanceInput
		}
		reliabilityWeight, reliabilityOK := policy.ReliabilityWeights[item.Reliability]
		attributionWeight, attributionOK := policy.AttributionWeights[item.Attribution]
		if !reliabilityOK || !attributionOK {
			return 0, 0, 0, false, ErrInvalidGovernanceInput
		}
		score := item.Confidence * reliabilityWeight * attributionWeight
		highDirect := item.Reliability == ReliabilityHigh && item.Attribution == AttributionDirect
		previous, exists := byEvent[item.RawEventID]
		if !exists {
			byEvent[item.RawEventID] = eventEvidence{score: score, highDirect: highDirect, sessionID: item.SourceSessionID}
			continue
		}
		if highDirect && previous.highDirect && previous.sessionID != "" && item.SourceSessionID != "" && previous.sessionID != item.SourceSessionID {
			return 0, 0, 0, false, ErrInvalidGovernanceInput
		}
		if score > previous.score {
			previous.score = score
		}
		if highDirect {
			previous.highDirect = true
			if item.SourceSessionID != "" {
				previous.sessionID = item.SourceSessionID
			}
		}
		byEvent[item.RawEventID] = previous
	}
	remaining := 1.0
	independentSessions := make(map[string]struct{})
	hasHighDirect := false
	for _, item := range byEvent {
		remaining *= 1 - item.score
		if item.highDirect {
			hasHighDirect = true
			if item.sessionID != "" {
				independentSessions[item.sessionID] = struct{}{}
			}
		}
	}
	return 1 - remaining, len(byEvent), len(independentSessions), hasHighDirect, nil
}

func transitionTo(memory *Memory, target MemoryStatus) error {
	if memory.Status == target {
		return nil
	}
	return memory.Transition(target)
}

func finiteUnitInterval(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
