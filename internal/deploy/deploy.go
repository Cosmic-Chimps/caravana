// Package deploy runs one deploy against the outside world: it checks an
// app, pins its images through the registry, registers the translated job
// with Nomad and follows Nomad's rollout to its end. It records nothing
// itself; package service does that.
//
// This and package nomadjob are the only packages that know about Nomad
// (constitution VI).
package deploy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
)

// Errors callers can check with errors.Is. The API maps each to its own
// error code.
var (
	ErrImageNotFound       = errors.New("image not found")
	ErrRegistryUnavailable = errors.New("registry unavailable")
	ErrJobConflict         = errors.New("job conflict")
	ErrNomadUnavailable    = errors.New("nomad unavailable")
	ErrNomadRejected       = errors.New("nomad rejected the job")
)

// ImageError is a failure to pin one service's image. It matches
// ErrImageNotFound or ErrRegistryUnavailable with errors.Is, and the
// registry's own error too.
type ImageError struct {
	Field string // services[i].image
	Image string // as the user wrote it
	Err   error  // the registry's error

	kind error // ErrImageNotFound or ErrRegistryUnavailable
}

func (e *ImageError) Error() string { return e.Err.Error() }

// Unwrap returns both errors, so errors.Is finds either. (An Unwrap that
// returns a slice is Go's way of wrapping several errors at once.)
func (e *ImageError) Unwrap() []error { return []error{e.kind, e.Err} }

// Timing holds the waits the deploy flow uses. Tests shorten them; the zero
// value of each field means the default.
type Timing struct {
	// ImageTimeout bounds resolving one image. Default 10 s.
	ImageTimeout time.Duration
	// PollGap is the shortest time between two rollout queries. Default 1 s.
	PollGap time.Duration
	// BackoffStart and BackoffMax bound the wait after a failed Nomad call:
	// it doubles from start up to max. Defaults 1 s and 30 s.
	BackoffStart, BackoffMax time.Duration
	// VersionRetries are the waits between tries to read back the job
	// version after registering. Default 0.5 s, then 1 s.
	VersionRetries []time.Duration
}

func (t Timing) withDefaults() Timing {
	if t.ImageTimeout == 0 {
		t.ImageTimeout = 10 * time.Second
	}
	if t.PollGap == 0 {
		t.PollGap = time.Second
	}
	if t.BackoffStart == 0 {
		t.BackoffStart = time.Second
	}
	if t.BackoffMax == 0 {
		t.BackoffMax = 30 * time.Second
	}
	if t.VersionRetries == nil {
		t.VersionRetries = []time.Duration{500 * time.Millisecond, time.Second}
	}
	return t
}

// Deployer deploys apps to one Nomad cluster.
type Deployer struct {
	nomad    *api.Client
	registry *registry.Client
	settings nomadjob.Settings

	// Timing is read on every call; set it before the Deployer is used.
	Timing Timing
}

// clientConnTimeout is how long the Nomad client library tries a node's own
// address, before it goes through the server instead (log reads, research
// R10). It is the library's default; it is set here so that a change in the
// library doesn't slow every log read down when the node isn't reachable.
const clientConnTimeout = time.Second

// setConnTimeout sets the library's one package-wide setting, once, before
// the first client exists, so nothing reads it while it is written.
var setConnTimeout sync.Once

// NewNomadClient builds the Nomad API client. It makes no request, so it
// works while Nomad is down. Other NOMAD_* variables (TLS, namespace, region)
// are read by the Nomad client itself.
func NewNomadClient(addr, token string) (*api.Client, error) {
	setConnTimeout.Do(func() { api.ClientConnTimeout = clientConnTimeout })
	cfg := api.DefaultConfig()
	cfg.Address = addr
	cfg.SecretID = token
	c, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("nomad client for %s: %w", addr, err)
	}
	return c, nil
}

// New returns a Deployer.
func New(nomad *api.Client, reg *registry.Client, s nomadjob.Settings) *Deployer {
	return &Deployer{nomad: nomad, registry: reg, settings: s}
}

// Validate checks everything that doesn't need the registry or Nomad: the
// app's rules, Nomad's limits and every image reference. Errors are
// *app.FieldError. It is pure.
func (d *Deployer) Validate(a app.App) error {
	if err := nomadjob.Check(a, d.settings); err != nil {
		return err
	}
	for i, svc := range a.Services {
		if _, err := registry.Parse(svc.Image); err != nil {
			return app.NewFieldError(fmt.Sprintf("services[%d].image", i), "%v", err)
		}
	}
	return nil
}

// Resolve pins every service's image, in service order, and returns service
// name → pinned image. Each image gets its own time budget. A reference
// with both a tag and a digest gives a warning: the tag has no effect. The
// first failure stops it and is an *ImageError.
func (d *Deployer) Resolve(ctx context.Context, a app.App) (map[string]string, []app.Warning, error) {
	timing := d.Timing.withDefaults()
	digests := make(map[string]string, len(a.Services))
	var warnings []app.Warning
	for i, svc := range a.Services {
		field := fmt.Sprintf("services[%d].image", i)
		ref, err := registry.Parse(svc.Image)
		if err != nil {
			return nil, nil, app.NewFieldError(field, "%v", err)
		}
		if ref.IgnoredTag != "" {
			warnings = append(warnings, app.Warning{Field: field,
				Message: fmt.Sprintf("tag %q is ignored: the digest pins the image", ref.IgnoredTag)})
		}

		imageCtx, cancel := context.WithTimeout(ctx, timing.ImageTimeout)
		pinned, err := d.registry.Resolve(imageCtx, ref)
		cancel()
		if err != nil {
			kind := ErrRegistryUnavailable
			if errors.Is(err, registry.ErrNotFound) {
				kind = ErrImageNotFound
			}
			return nil, nil, &ImageError{Field: field, Image: svc.Image, Err: err, kind: kind}
		}
		digests[svc.Name] = pinned
	}
	return digests, warnings, nil
}
