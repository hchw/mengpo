package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hchw/mengpo/internal/api/dto"
)

const DefaultMaxRequestBytes int64 = 1 << 20

const (
	ObservePath     = "/api/v1/observe"
	ProjectPath     = "/api/v1/project"
	ConsolidatePath = "/api/v1/consolidate"
	FeedbackPath    = "/api/v1/feedback"
	SessionPath     = "/api/v1/sessions"
)

type Operation string

const (
	OperationObserve     Operation = "observe"
	OperationProject     Operation = "project"
	OperationConsolidate Operation = "consolidate"
	OperationFeedback    Operation = "feedback"
	OperationSession     Operation = "session"
)

// UseCases is the HTTP boundary for application operations. Implementations
// must resolve and authorize the declared tenant/principal/scope against
// trusted request identity; Envelope claims alone are not authorization.
type UseCases interface {
	Observe(context.Context, dto.Envelope) (any, error)
	Project(context.Context, dto.Envelope) (any, error)
	Consolidate(context.Context, dto.Envelope) (any, error)
	Feedback(context.Context, dto.Envelope) (any, error)
	Session(context.Context, dto.Envelope) (any, error)
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Response struct {
	Version   string `json:"version"`
	RequestID string `json:"request_id,omitempty"`
	Data      any    `json:"data,omitempty"`
	Error     *Error `json:"error,omitempty"`
}

type Server struct {
	useCases UseCases
	maxBytes int64
}

func NewHandler(useCases UseCases, maxRequestBytes int64) http.Handler {
	if maxRequestBytes <= 0 {
		maxRequestBytes = DefaultMaxRequestBytes
	}
	server := &Server{useCases: useCases, maxBytes: maxRequestBytes}
	mux := http.NewServeMux()
	mux.HandleFunc(ObservePath, server.handle(OperationObserve))
	mux.HandleFunc(ProjectPath, server.handle(OperationProject))
	mux.HandleFunc(ConsolidatePath, server.handle(OperationConsolidate))
	mux.HandleFunc(FeedbackPath, server.handle(OperationFeedback))
	mux.HandleFunc(SessionPath, server.handle(OperationSession))
	return mux
}

func (s *Server) handle(operation Operation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			s.writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST is required", false, "")
			return
		}
		if s.useCases == nil {
			s.writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "API use cases are not configured", true, "")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBytes)
		envelope, err := dto.DecodeEnvelope(r.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				s.writeError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "request body exceeds configured limit", false, "")
				return
			}
			s.writeError(w, http.StatusBadRequest, "INVALID_ENVELOPE", "request envelope is invalid", false, "")
			return
		}
		var data any
		switch operation {
		case OperationObserve:
			data, err = s.useCases.Observe(r.Context(), envelope)
		case OperationProject:
			data, err = s.useCases.Project(r.Context(), envelope)
		case OperationConsolidate:
			data, err = s.useCases.Consolidate(r.Context(), envelope)
		case OperationFeedback:
			data, err = s.useCases.Feedback(r.Context(), envelope)
		case OperationSession:
			data, err = s.useCases.Session(r.Context(), envelope)
		default:
			err = errors.New("unknown operation")
		}
		if err != nil {
			status, apiError := mapError(err)
			writeJSON(w, status, Response{Version: dto.CurrentVersion, RequestID: envelope.RequestID, Error: apiError})
			return
		}
		writeJSON(w, http.StatusOK, Response{Version: dto.CurrentVersion, RequestID: envelope.RequestID, Data: data})
	}
}

type CodedError interface {
	error
	APIErrorCode() string
	Retryable() bool
}

func mapError(err error) (int, *Error) {
	var coded CodedError
	if errors.As(err, &coded) {
		status := http.StatusBadRequest
		if coded.Retryable() {
			status = http.StatusServiceUnavailable
		}
		return status, &Error{Code: coded.APIErrorCode(), Message: safeMessage(err), Retryable: coded.Retryable()}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, &Error{Code: "TIMEOUT", Message: "operation timed out", Retryable: true}
	}
	if errors.Is(err, context.Canceled) {
		return 499, &Error{Code: "CANCELLED", Message: "operation cancelled", Retryable: true}
	}
	return http.StatusInternalServerError, &Error{Code: "INTERNAL", Message: "operation failed", Retryable: false}
}

func safeMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "operation failed"
	}
	return message
}

func writeJSON(w http.ResponseWriter, status int, value Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string, retryable bool, requestID string) {
	writeJSON(w, status, Response{Version: dto.CurrentVersion, RequestID: requestID, Error: &Error{Code: code, Message: message, Retryable: retryable}})
}
