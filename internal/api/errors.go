package api

import (
	"net/http"
)

// Error codes. Clients must accept codes they don't know; new ones are listed
// in api/openapi.yaml.
const (
	codeBadRequest            = "bad_request"
	codeInvalidField          = "invalid_field"
	codeUnknownField          = "unknown_field"
	codeUnsupportedMediaType  = "unsupported_media_type"
	codeBodyTooLarge          = "body_too_large"
	codeNotFound              = "not_found"
	codeMethodNotAllowed      = "method_not_allowed"
	codeInternal              = "internal"
	codeUnavailable           = "unavailable"
	codeImageNotFound         = "image_not_found"
	codeJobConflict           = "job_conflict"
	codeRegistryUnavailable   = "registry_unavailable"
	codeNomadUnavailable      = "nomad_unavailable"
	codeNomadRejected         = "nomad_rejected"
	codeUnauthorized          = "unauthorized"
	codeCrossOrigin           = "cross_origin"
	codeTokenNameTaken        = "token_name_taken"
	codeAppExists             = "app_exists"
	codeSecretStoreUnavail    = "secret_store_unavailable"
	codeBackupRunning         = "backup_running"
	codeNoBackupDestination   = "no_backup_destination"
	codeNoRunningCopy         = "no_running_copy"
	codeDeploymentNotHealthy  = "deployment_not_healthy"
	codeDefinitionNotRecorded = "definition_not_recorded"
	codeNoLiveDeployment      = "no_live_deployment"
	codeAppChanged            = "app_changed"
	codeComposeNotKept        = "compose_not_kept"
)

// Error is what every failed request returns, as {"error": {...}} with
// Status as the HTTP status. Field names the one request field at fault.
// Location, when set, is sent as the Location header: the deployment record
// a failed deploy left behind.
type Error struct {
	Status   int    `json:"-"`
	Location string `json:"-"`
	// Authenticate, when set, is sent as the WWW-Authenticate header.
	Authenticate string `json:"-"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	Field        string `json:"field,omitempty"`
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
	_ = encodeJSON(w, v)
}

// writeError writes e as the standard error body.
func writeError(w http.ResponseWriter, e *Error) {
	if e.Location != "" {
		w.Header().Set("Location", e.Location)
	}
	if e.Authenticate != "" {
		w.Header().Set("WWW-Authenticate", e.Authenticate)
	}
	writeJSON(w, e.Status, struct {
		Error *Error `json:"error"`
	}{e})
}
