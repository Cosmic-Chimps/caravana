package api

import (
	"encoding/json"
	"net/http"
)

// Error codes. Clients must accept codes they don't know; new ones are listed
// in api/openapi.yaml.
const (
	codeBadRequest           = "bad_request"
	codeInvalidField         = "invalid_field"
	codeUnknownField         = "unknown_field"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeBodyTooLarge         = "body_too_large"
	codeNotFound             = "not_found"
	codeMethodNotAllowed     = "method_not_allowed"
	codeInternal             = "internal"
	codeUnavailable          = "unavailable"
)

// Error is what every failed request returns, as {"error": {...}} with
// Status as the HTTP status. Field names the one request field at fault.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// Error makes *Error satisfy the error interface, so handlers can return it.
func (e *Error) Error() string {
	if e.Field != "" {
		return e.Code + ": " + e.Field + ": " + e.Message
	}
	return e.Code + ": " + e.Message
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent, so an encoding error can't be reported to
	// the client; our response types always encode.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes e as the standard error body.
func writeError(w http.ResponseWriter, e *Error) {
	writeJSON(w, e.Status, struct {
		Error *Error `json:"error"`
	}{e})
}
