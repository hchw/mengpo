package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hchw/mengpo/internal/api/dto"
)

type recordingUseCases struct {
	called  Operation
	request dto.Envelope
	err     error
}

func (u *recordingUseCases) run(op Operation, req dto.Envelope) (any, error) {
	u.called, u.request = op, req
	if u.err != nil {
		return nil, u.err
	}
	return map[string]any{"accepted": true, "operation": string(op)}, nil
}
func (u *recordingUseCases) Observe(_ context.Context, e dto.Envelope) (any, error) {
	return u.run(OperationObserve, e)
}
func (u *recordingUseCases) Project(_ context.Context, e dto.Envelope) (any, error) {
	return u.run(OperationProject, e)
}
func (u *recordingUseCases) Consolidate(_ context.Context, e dto.Envelope) (any, error) {
	return u.run(OperationConsolidate, e)
}
func (u *recordingUseCases) Feedback(_ context.Context, e dto.Envelope) (any, error) {
	return u.run(OperationFeedback, e)
}
func (u *recordingUseCases) Session(_ context.Context, e dto.Envelope) (any, error) {
	return u.run(OperationSession, e)
}

type retryableError struct{}

func (retryableError) Error() string        { return "database unavailable" }
func (retryableError) APIErrorCode() string { return "DEPENDENCY_UNAVAILABLE" }
func (retryableError) Retryable() bool      { return true }

type permanentError struct{}

func (permanentError) Error() string        { return "invalid operation" }
func (permanentError) APIErrorCode() string { return "INVALID_OPERATION" }
func (permanentError) Retryable() bool      { return false }

func requestEnvelope() []byte {
	body, _ := json.Marshal(dto.Envelope{
		Version: dto.CurrentVersion, RequestID: "req-1", IdempotencyKey: "idem-1",
		Principal: dto.Principal{Type: "user", ID: "user-1"},
		Scope:     dto.Scope{TenantID: "tenant-1", UserID: "user-1", Type: "session", SessionID: "session-1"},
		Budget:    dto.Budget{Candidates: 20, Ranking: 10, InjectionTokens: 1024},
		Privacy:   dto.Privacy{Visibility: "private"},
		Payload:   json.RawMessage(`{"text":"hello"}`),
	})
	return body
}

func TestHTTPRoutesDispatchAllFiveOperations(t *testing.T) {
	routes := []struct {
		path string
		want Operation
	}{
		{ObservePath, OperationObserve}, {ProjectPath, OperationProject}, {ConsolidatePath, OperationConsolidate}, {FeedbackPath, OperationFeedback}, {SessionPath, OperationSession},
	}
	for _, route := range routes {
		t.Run(string(route.want), func(t *testing.T) {
			useCases := &recordingUseCases{}
			handler := NewHandler(useCases, DefaultMaxRequestBytes)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, route.path, strings.NewReader(string(requestEnvelope()))))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if useCases.called != route.want {
				t.Fatalf("called=%q want %q", useCases.called, route.want)
			}
			if useCases.request.Scope.TenantID != "tenant-1" || useCases.request.RequestID != "req-1" {
				t.Fatalf("envelope not forwarded: %#v", useCases.request)
			}
			if recorder.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content type=%q", recorder.Header().Get("Content-Type"))
			}
			var response Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Version != dto.CurrentVersion || response.RequestID != "req-1" || response.Error != nil {
				t.Fatalf("response=%#v", response)
			}
		})
	}
}

func TestHTTPRejectsInvalidMethodEnvelopeAndOversizedRequest(t *testing.T) {
	handler := NewHandler(&recordingUseCases{}, 128)
	cases := []struct {
		name, method, path, body string
		want                     int
		code                     string
	}{
		{"method", "GET", ObservePath, string(requestEnvelope()), http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
		{"bad-json", "POST", ObservePath, "{", http.StatusBadRequest, "INVALID_ENVELOPE"},
		{"oversized", "POST", ObservePath, strings.Repeat(" ", 256) + `{}`, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE"},
		{"unknown-route", "POST", "/api/v1/unknown", string(requestEnvelope()), http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if recorder.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.want, recorder.Body.String())
			}
			if tc.code != "" {
				var response Response
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Error == nil || response.Error.Code != tc.code {
					t.Fatalf("error=%#v", response.Error)
				}
			}
		})
	}
}

func TestHTTPMapsTypedRetryableErrorsAndDeadlines(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
	}{
		{"dependency", retryableError{}, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", true},
		{"permanent", permanentError{}, http.StatusBadRequest, "INVALID_OPERATION", false},
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "TIMEOUT", true},
		{"cancel", context.Canceled, 499, "CANCELLED", true},
		{"internal", errors.New("secret sql detail"), http.StatusInternalServerError, "INTERNAL", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useCases := &recordingUseCases{err: tc.err}
			handler := NewHandler(useCases, 0)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, ProjectPath, strings.NewReader(string(requestEnvelope()))))
			if recorder.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.status, recorder.Body.String())
			}
			var response Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error == nil || response.Error.Code != tc.code || response.Error.Retryable != tc.retryable {
				t.Fatalf("error=%#v", response.Error)
			}
			if response.RequestID != "req-1" {
				t.Fatalf("request id=%q", response.RequestID)
			}
			if tc.name == "internal" && strings.Contains(response.Error.Message, "secret sql detail") {
				t.Fatal("internal error leaked details")
			}
		})
	}
}

func TestHTTPReturnsUnavailableWhenUseCasesMissing(t *testing.T) {
	handler := NewHandler(nil, 0)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, SessionPath, strings.NewReader(string(requestEnvelope()))))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
