package httpapi

import (
	"encoding/json"
	"net/http"
)

// Standard error codes shared between REST and WebSocket
const (
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeValidationFailed    = "validation_failed"
	CodeUnsupportedPair     = "unsupported_pair"
	CodeRateLimited         = "rate_limited"
	CodeQuotaExceeded       = "quota_exceeded"
	CodePreconditionFailed  = "precondition_failed"
	CodeDeviceRevoked       = "device_revoked"
	CodeBadState            = "bad_state"
	CodeBadFrame            = "bad_frame"
	CodeTurnTimeout         = "turn_timeout"
	CodeProviderUnavailable = "provider_unavailable"
	CodeProviderError       = "provider_error"
	CodeInternal            = "internal"
)

// ProblemDetails implements RFC 9457 HTTP Problem Details.
type ProblemDetails struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteProblem responds with an RFC 9457 application/problem+json payload.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	reqID := r.Header.Get("X-Request-Id")
	if reqID == "" {
		reqID = w.Header().Get("X-Request-Id")
	}

	p := ProblemDetails{
		Type:      "about:blank",
		Title:     title,
		Status:    status,
		Detail:    detail,
		Code:      code,
		RequestID: reqID,
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteJSON sends a JSON response with status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
