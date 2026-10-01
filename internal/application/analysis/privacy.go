package analysis

import (
	"encoding/json"
	"strings"

	"github.com/hchw/mengpo/internal/ports"
)

// PrivacyPolicy controls which event fields may leave the service boundary.
type PrivacyPolicy struct {
	AllowedFields   map[string]bool
	RedactKeys      map[string]bool
	RejectSensitive bool
}

var sensitiveFieldNames = map[string]bool{
	"password": true, "passwd": true, "token": true, "access_token": true, "refresh_token": true,
	"authorization": true, "api_key": true, "secret": true, "credential": true, "private_key": true,
}

// PrepareBatch makes a fresh, tenant-scoped copy, strips unapproved fields, and
// redacts common credentials before passing event data to an external provider.
func PrepareBatch(input ports.AnalysisBatch, policy PrivacyPolicy) (ports.AnalysisBatch, error) {
	if input.TenantID == "" || input.RunID == "" || input.PromptVersion == "" || input.SchemaVersion == "" {
		return ports.AnalysisBatch{}, ErrInvalidBatch
	}
	result := input
	result.Events = make([]ports.AnalysisEvent, len(input.Events))
	for index, event := range input.Events {
		var fields map[string]any
		if len(event.Payload) > 0 {
			if err := json.Unmarshal(event.Payload, &fields); err != nil {
				return ports.AnalysisBatch{}, err
			}
		}
		filtered := make(map[string]any, len(fields))
		for key, value := range fields {
			if len(policy.AllowedFields) > 0 && !policy.AllowedFields[key] {
				continue
			}
			if sensitiveFieldNames[strings.ToLower(key)] || policy.RedactKeys[key] {
				if policy.RejectSensitive {
					return ports.AnalysisBatch{}, ErrSensitiveInput
				}
				filtered[key] = "[REDACTED]"
				continue
			}
			filtered[key] = value
		}
		encoded, err := json.Marshal(filtered)
		if err != nil {
			return ports.AnalysisBatch{}, err
		}
		event.Payload = encoded
		result.Events[index] = event
	}
	result.Metadata = make(map[string]string, len(input.Metadata)+2)
	for key, value := range input.Metadata {
		result.Metadata[key] = value
	}
	result.Metadata["tenant_id"] = input.TenantID
	result.Metadata["prompt_version"] = input.PromptVersion
	result.Metadata["schema_version"] = input.SchemaVersion
	return result, nil
}
