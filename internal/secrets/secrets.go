// Package secrets is the seam between Caravana and the store that holds
// secret values. The service layer only talks to a Provider; Nomad Variables
// (deploy.Variables) is the built-in one, and package secretstest has an
// in-memory one for tests. Nothing here knows about Nomad.
//
// Values pass through this package but are never logged and never put in an
// error message: errors name the field and the secret's name only.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Sets are an app's secrets: service name → secret name → value. A service
// without secrets is absent.
type Sets map[string]map[string]string

// ErrUnavailable is a store that can't be reached, or that refuses Caravana.
// Every Provider error of that kind matches it with errors.Is.
var ErrUnavailable = errors.New("secret store unavailable")

// Provider holds secret values, a whole set per service. Implementations are
// safe for concurrent use; the service layer still calls them for one app at
// a time.
type Provider interface {
	// Read returns the secrets every service of appID has now. No secrets is
	// an empty Sets, not an error.
	Read(ctx context.Context, appID string) (Sets, error)
	// Replace makes appID's secrets exactly sets, on behalf of deploymentID.
	// The sets it replaces are kept until Commit or Restore for that
	// deployment, or until the next Replace takes them over.
	Replace(ctx context.Context, appID, deploymentID string, sets Sets) error
	// Commit forgets the kept sets, if deploymentID owns them: its version
	// is the one running now.
	Commit(ctx context.Context, appID, deploymentID string) error
	// Restore makes the kept sets live again and forgets them, if
	// deploymentID owns them: its version never got healthy.
	Restore(ctx context.Context, appID, deploymentID string) error
	// Delete removes every secret of appID, kept ones included.
	Delete(ctx context.Context, appID string) error
}

// MaxSize is how many bytes an app's secrets may hold in total, counted as
// len(service)+1+len(name)+len(value) per secret. That is their size in the
// store's kept copy, which holds every service at once; Nomad caps a
// variable at 64 KiB, and this leaves room for the copy's own item.
const MaxSize = 60 * 1024

// Build applies a's definition to the current values and returns the full
// sets a deploy must make live: a secret with a value takes it, a secret
// without one keeps its current value, and every secret a doesn't name is
// gone. Errors are *app.FieldError.
func Build(a app.App, current Sets) (Sets, error) {
	out := Sets{}
	size := 0
	for i, svc := range a.Services {
		if len(svc.Secrets) == 0 {
			continue
		}
		set := make(map[string]string, len(svc.Secrets))
		for j, s := range svc.Secrets {
			var v string
			switch {
			case s.Value != nil:
				v = *s.Value
			default:
				cur, ok := current[svc.Name][s.Name]
				if !ok {
					return nil, app.NewFieldError(fmt.Sprintf("services[%d].secrets[%d].value", i, j),
						"secret %q has no value yet: send one", s.Name)
				}
				v = cur
			}
			set[s.Name] = v
			size += len(svc.Name) + 1 + len(s.Name) + len(v)
		}
		if size > MaxSize {
			return nil, app.NewFieldError(fmt.Sprintf("services[%d].secrets", i),
				"the app's secrets would hold %d bytes; at most %d fit", size, MaxSize)
		}
		out[svc.Name] = set
	}
	return out, nil
}

// Equal reports whether s and o hold the same secrets. A service with an
// empty set counts as absent.
func (s Sets) Equal(o Sets) bool {
	for name, set := range s {
		if len(set) > 0 && !maps.Equal(set, o[name]) {
			return false
		}
	}
	for name, set := range o {
		if len(set) > 0 && len(s[name]) == 0 {
			return false
		}
	}
	return true
}
