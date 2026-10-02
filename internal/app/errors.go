package app

import "fmt"

// FieldError is input Caravana can't honor. Field names the exact input
// field, as a path like "services[0].domains[1].port", so the API can return
// it and the user knows what to fix.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error makes *FieldError satisfy the error interface.
func (e *FieldError) Error() string {
	return e.Field + ": " + e.Message
}

// NewFieldError returns a FieldError for field with a formatted message.
func NewFieldError(field, format string, args ...any) *FieldError {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Warning is about input that is supported but changed in meaning. Nothing in
// Phase 1 produces one: every field is either honored exactly or rejected.
type Warning struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}
