package secrets

import (
	"context"
	"errors"
)

// Source reads secret values from an outside provider (feature 037), such as
// Bella Baxter. It is not the store (Provider, above, is where values live
// once a deploy has copied them): a deploy asks a Source for the values a
// reference names, then hands them to Build.
//
// Implementations are safe for concurrent use. They never put a value, a
// credential or the text of a library's error in an error they return: errors
// are *SourceError, in Caravana's own words.
type Source interface {
	// Read returns the value of each path the provider has. A path it doesn't
	// have is absent from the map, not an error. It reads once, however many
	// paths are asked for.
	Read(ctx context.Context, paths []string) (map[string]string, error)
	// Probe reads once and says how many secrets Caravana can read.
	Probe(ctx context.Context) (int, error)
}

// The two ways a Source fails. Every *SourceError matches exactly one of
// them with errors.Is.
var (
	// ErrSourceUnavailable is a provider that can't be reached or answered
	// with a server error: down, a timeout, a 5xx. Trying again may work.
	ErrSourceUnavailable = errors.New("secret provider unavailable")
	// ErrSourceRefused is a provider that answered and said no to Caravana:
	// the credentials, the device key, the environment or an answer that
	// couldn't be decrypted. Trying again changes nothing until the provider's
	// settings do.
	ErrSourceRefused = errors.New("secret provider refused")
)

// SourceError is a Source's failure. Err is ErrSourceUnavailable or
// ErrSourceRefused and Reason is a sentence in Caravana's words, safe to show
// and to log. It never wraps a library's error, whose text may hold a
// credential or a value.
type SourceError struct {
	Err    error
	Reason string
}

// Error returns the reason.
func (e *SourceError) Error() string { return e.Reason }

// Unwrap lets errors.Is find ErrSourceUnavailable or ErrSourceRefused.
func (e *SourceError) Unwrap() error { return e.Err }

// Unavailable returns a *SourceError that matches ErrSourceUnavailable.
func Unavailable(reason string) *SourceError {
	return &SourceError{Err: ErrSourceUnavailable, Reason: reason}
}

// Refused returns a *SourceError that matches ErrSourceRefused.
func Refused(reason string) *SourceError {
	return &SourceError{Err: ErrSourceRefused, Reason: reason}
}
