package assembly

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/ports"
)

type fakeMetricsReader struct{ metrics ports.RetrievalMetrics }

func (f fakeMetricsReader) ComputeRetrievalMetrics(context.Context, string, time.Time) (ports.RetrievalMetrics, error) {
	return f.metrics, nil
}

func callEvaluation(t *testing.T, api *ConsoleAPI) (int, map[string]any) {
	t.Helper()
	body := consoleEnvelope("tenant-a", "user-1", "req-eval", map[string]any{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/evaluation", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	identity := agentaccess.Identity{TenantID: "tenant-a", UserID: "user-1", SourceID: "user-1", Source: agentaccess.SourceVerifiedUser}
	recorder := httptest.NewRecorder()
	api.handleEvaluation(recorder, request.WithContext(agentaccess.WithIdentity(request.Context(), identity)))
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded
}

// TestEvaluationEndpointReportsMeasuredMetrics proves the console evaluation
// surface reflects recorded telemetry and feedback instead of a fixed zero.
func TestEvaluationEndpointReportsMeasuredMetrics(t *testing.T) {
	api := &ConsoleAPI{Metrics: fakeMetricsReader{metrics: ports.RetrievalMetrics{
		Projections: 4, CacheHits: 3, CacheHitRate: 0.75, LatencyP95Ms: 42, TokensPerProjection: 100,
		FeedbackSamples: 2, PositiveFeedback: 1, NegativeFeedback: 1, RetrievalPrecision: 0.5, WrongMemoryRate: 0.5,
	}}}
	status, body := callEvaluation(t, api)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	data := body["data"].(map[string]any)
	if data["cache_hit_rate"].(float64) != 0.75 || data["retrieval_precision"].(float64) != 0.5 {
		t.Fatalf("snapshot = %#v", data)
	}
	if data["latency_ms_p95"].(float64) != 42 || data["projections"].(float64) != 4 || data["feedback_samples"].(float64) != 2 {
		t.Fatalf("snapshot sample fields = %#v", data)
	}
	if data["has_data"] != true {
		t.Fatalf("has_data = %v, want true", data["has_data"])
	}
	measured, _ := data["measured_metrics"].([]any)
	if len(measured) != 5 {
		t.Fatalf("measured_metrics = %#v, want 5 entries", data["measured_metrics"])
	}
}

// TestEvaluationEndpointMarksEmptyData proves an unmeasured tenant is reported
// as has_data=false rather than a misleading measured 0%.
func TestEvaluationEndpointMarksEmptyData(t *testing.T) {
	api := &ConsoleAPI{Metrics: fakeMetricsReader{}}
	status, body := callEvaluation(t, api)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	data := body["data"].(map[string]any)
	if data["has_data"] != false || data["projections"].(float64) != 0 || data["feedback_samples"].(float64) != 0 {
		t.Fatalf("empty snapshot = %#v", data)
	}
}
