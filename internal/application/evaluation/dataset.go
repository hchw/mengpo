// Package evaluation defines the offline datasets and metrics used to judge
// memory quality. Datasets are versioned JSONL: one annotated sample per line,
// so they can be reviewed and diffed like code.
package evaluation

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type DatasetKind string

const (
	DatasetRetrieval     DatasetKind = "retrieval"
	DatasetConsolidation DatasetKind = "consolidation"
	DatasetFailure       DatasetKind = "failure"
)

var (
	ErrInvalidDataset = errors.New("invalid evaluation dataset")
	ErrInvalidSample  = errors.New("invalid evaluation sample")
)

// RetrievalSample measures whether the right memories are recalled or injected.
type RetrievalSample struct {
	ID                 string   `json:"id"`
	TenantID           string   `json:"tenant_id"`
	Query              string   `json:"query"`
	ScopeType          string   `json:"scope_type"`
	ExpectedMemoryIDs  []string `json:"expected_memory_ids"`
	ForbiddenMemoryIDs []string `json:"forbidden_memory_ids"`
	Mode               string   `json:"mode"`
}

// ConsolidationSample measures whether raw evidence becomes the right candidate.
type ConsolidationSample struct {
	ID                  string   `json:"id"`
	TenantID            string   `json:"tenant_id"`
	EventIDs            []string `json:"event_ids"`
	ExpectedType        string   `json:"expected_type"`
	ExpectedSummary     string   `json:"expected_summary"`
	ExpectedEvidenceIDs []string `json:"expected_evidence_ids"`
	ShouldPromote       bool     `json:"should_promote"`
}

// FailureSample separates failure credibility from attribution completeness.
type FailureSample struct {
	ID                  string `json:"id"`
	TenantID            string `json:"tenant_id"`
	Transcript          string `json:"transcript"`
	ExpectedConfidence  string `json:"expected_confidence"`
	ExpectedAttribution string `json:"expected_attribution"`
}

type Dataset struct {
	Kind          DatasetKind
	Version       string
	Retrieval     []RetrievalSample
	Consolidation []ConsolidationSample
	Failure       []FailureSample
}

func (d Dataset) Len() int {
	return len(d.Retrieval) + len(d.Consolidation) + len(d.Failure)
}

// LoadDataset strictly decodes a versioned JSONL dataset. The version is taken
// from the first line's metadata object; every later line is one sample.
func LoadDataset(kind DatasetKind, reader io.Reader) (Dataset, error) {
	dataset := Dataset{Kind: kind}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "//") {
			continue
		}
		if line == 1 {
			var header struct {
				Version string `json:"version"`
				Kind    string `json:"kind"`
			}
			if err := decodeStrict(text, &header); err != nil {
				return Dataset{}, fmt.Errorf("%w: header on line 1: %v", ErrInvalidDataset, err)
			}
			if header.Version == "" || header.Kind != string(kind) {
				return Dataset{}, fmt.Errorf("%w: header kind %q version %q", ErrInvalidDataset, header.Kind, header.Version)
			}
			dataset.Version = header.Version
			continue
		}
		if dataset.Version == "" {
			return Dataset{}, fmt.Errorf("%w: missing version header", ErrInvalidDataset)
		}
		switch kind {
		case DatasetRetrieval:
			var sample RetrievalSample
			if err := decodeStrict(text, &sample); err != nil {
				return Dataset{}, fmt.Errorf("line %d: %w: %v", line, ErrInvalidSample, err)
			}
			if err := sample.Validate(); err != nil {
				return Dataset{}, fmt.Errorf("line %d: %w", line, err)
			}
			dataset.Retrieval = append(dataset.Retrieval, sample)
		case DatasetConsolidation:
			var sample ConsolidationSample
			if err := decodeStrict(text, &sample); err != nil {
				return Dataset{}, fmt.Errorf("line %d: %w: %v", line, ErrInvalidSample, err)
			}
			if err := sample.Validate(); err != nil {
				return Dataset{}, fmt.Errorf("line %d: %w", line, err)
			}
			dataset.Consolidation = append(dataset.Consolidation, sample)
		case DatasetFailure:
			var sample FailureSample
			if err := decodeStrict(text, &sample); err != nil {
				return Dataset{}, fmt.Errorf("line %d: %w: %v", line, ErrInvalidSample, err)
			}
			if err := sample.Validate(); err != nil {
				return Dataset{}, fmt.Errorf("line %d: %w", line, err)
			}
			dataset.Failure = append(dataset.Failure, sample)
		default:
			return Dataset{}, fmt.Errorf("%w: unknown dataset kind %q", ErrInvalidDataset, kind)
		}
	}
	if err := scanner.Err(); err != nil {
		return Dataset{}, fmt.Errorf("%w: read: %v", ErrInvalidDataset, err)
	}
	if dataset.Version == "" || dataset.Len() == 0 {
		return Dataset{}, fmt.Errorf("%w: dataset has no versioned samples", ErrInvalidDataset)
	}
	return dataset, nil
}

func decodeStrict(text string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing JSON value")
	}
	return nil
}

func (s RetrievalSample) Validate() error {
	if s.ID == "" || s.TenantID == "" || strings.TrimSpace(s.Query) == "" {
		return fmt.Errorf("%w: retrieval sample requires id, tenant and query", ErrInvalidSample)
	}
	if s.Mode != "" && s.Mode != "auto" && s.Mode != "focus" && s.Mode != "diverge" {
		return fmt.Errorf("%w: retrieval sample unknown mode %q", ErrInvalidSample, s.Mode)
	}
	if s.ScopeType != "" && s.ScopeType != "user-global" && s.ScopeType != "session" {
		return fmt.Errorf("%w: retrieval sample unknown scope %q", ErrInvalidSample, s.ScopeType)
	}
	if len(s.ExpectedMemoryIDs) == 0 {
		return fmt.Errorf("%w: retrieval sample needs expected memories", ErrInvalidSample)
	}
	for _, id := range s.ExpectedMemoryIDs {
		for _, forbidden := range s.ForbiddenMemoryIDs {
			if id == forbidden {
				return fmt.Errorf("%w: memory %q both expected and forbidden", ErrInvalidSample, id)
			}
		}
	}
	return nil
}

func (s ConsolidationSample) Validate() error {
	if s.ID == "" || s.TenantID == "" || len(s.EventIDs) == 0 {
		return fmt.Errorf("%w: consolidation sample requires id, tenant and events", ErrInvalidSample)
	}
	if s.ShouldPromote && (s.ExpectedType == "" || s.ExpectedSummary == "") {
		return fmt.Errorf("%w: promotable sample needs expected type and summary", ErrInvalidSample)
	}
	return nil
}

func (s FailureSample) Validate() error {
	if s.ID == "" || s.TenantID == "" || strings.TrimSpace(s.Transcript) == "" {
		return fmt.Errorf("%w: failure sample requires id, tenant and transcript", ErrInvalidSample)
	}
	if !validConfidence(s.ExpectedConfidence) {
		return fmt.Errorf("%w: failure sample unknown confidence %q", ErrInvalidSample, s.ExpectedConfidence)
	}
	if !validAttribution(s.ExpectedAttribution) {
		return fmt.Errorf("%w: failure sample unknown attribution %q", ErrInvalidSample, s.ExpectedAttribution)
	}
	return nil
}

func validConfidence(value string) bool {
	switch value {
	case "confirmed", "inferred", "suspected", "unknown":
		return true
	default:
		return false
	}
}

func validAttribution(value string) bool {
	switch value {
	case "direct", "correlated", "inferred", "unknown":
		return true
	default:
		return false
	}
}
