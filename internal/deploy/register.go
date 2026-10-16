package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// metaDeployment is the job meta key the translator sets to the deployment
// ID. It is how Caravana recognizes its own jobs and job versions.
const metaDeployment = "caravana.deployment"

// Registered is what Register returns once Nomad has accepted the job.
type Registered struct {
	// JobVersion is the new job version. nil means Nomad accepted the job but
	// its version couldn't be read back yet; FindVersion learns it later.
	JobVersion *uint64
	// Warnings are Nomad's register warnings, if any.
	Warnings []app.Warning
}

// Register translates the app and registers it as a new version of its Nomad
// job (research R6). An existing job that isn't stopped must be Caravana's:
// its deployment meta must be a deployment owns recognizes, or Register
// fails with ErrJobConflict and registers nothing (R7). The write enforces
// the job's modify index, so a change between the check and the write fails
// too, instead of being overwritten.
//
// private is the set of registry hosts Caravana holds credentials for (as for
// nomadjob.Translate): a service whose image comes from one pulls with them,
// and gets its pull-credentials variable written before the job is.
func (d *Deployer) Register(ctx context.Context, a app.App, dep app.Deployment,
	owns func(ctx context.Context, deploymentID string) (bool, error), private map[string]bool) (Registered, error) {
	res, err := nomadjob.Translate(a, dep, d.settings, private)
	if err != nil {
		return Registered{}, fmt.Errorf("translate %q: %w", a.ID, err)
	}

	var modifyIndex uint64 // 0 with EnforceIndex means "only if the job is new"
	current, _, err := d.nomad.Jobs().Info(a.ID, (&api.QueryOptions{}).WithContext(ctx))
	switch {
	case statusOf(err) == http.StatusNotFound:
	case err != nil:
		return Registered{}, d.callError("read job "+a.ID, err)
	default:
		if current.Stop == nil || !*current.Stop {
			ours := false
			if id := current.Meta[metaDeployment]; id != "" {
				if ours, err = owns(ctx, id); err != nil {
					return Registered{}, fmt.Errorf("check who owns job %q: %w", a.ID, err)
				}
			}
			if !ours {
				return Registered{}, fmt.Errorf("%w: Nomad already runs a job %q that Caravana didn't create; stop it, or deploy under another app ID",
					ErrJobConflict, a.ID)
			}
		}
		if current.JobModifyIndex != nil {
			modifyIndex = *current.JobModifyIndex
		}
	}

	// After the ownership check, so a conflict creates nothing; before the
	// job, so Nomad can place the copies on the node that has the volumes.
	if err := d.EnsureVolumes(ctx, a); err != nil {
		return Registered{}, err
	}
	// The caravana-volumes tasks read their variable before the service
	// starts, so it has to exist before the job does.
	if err := d.ensureVolumeVariables(ctx, a); err != nil {
		return Registered{}, err
	}
	// Likewise the task of a private image reads its login before it pulls.
	if err := d.ensureRegistryVariables(ctx, a, dep, private); err != nil {
		return Registered{}, err
	}

	resp, _, err := d.nomad.Jobs().RegisterOpts(res.Job,
		&api.RegisterOptions{EnforceIndex: true, ModifyIndex: modifyIndex},
		(&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		return Registered{}, d.registerError(a.ID, err)
	}

	out := Registered{Warnings: res.Warnings}
	if w := strings.TrimSpace(resp.Warnings); w != "" {
		out.Warnings = append(out.Warnings, app.Warning{Message: "nomad: " + w})
	}
	out.JobVersion = d.versionAfterRegister(ctx, a.ID, dep.ID)
	return out, nil
}

// versionAfterRegister reads back the version Nomad gave this deployment,
// with a few retries. nil means it couldn't: the job is accepted anyway.
func (d *Deployer) versionAfterRegister(ctx context.Context, appID, deploymentID string) *uint64 {
	retries := d.Timing.withDefaults().VersionRetries
	for attempt := 0; ; attempt++ {
		v, found, err := d.FindVersion(ctx, appID, deploymentID)
		if err == nil && found {
			return &v
		}
		if attempt >= len(retries) || !sleep(ctx, retries[attempt]) {
			return nil
		}
	}
}

// FindVersion looks for the job version that carries deploymentID in its
// meta. found is false when no version does.
func (d *Deployer) FindVersion(ctx context.Context, appID, deploymentID string) (version uint64, found bool, err error) {
	versions, _, _, err := d.nomad.Jobs().Versions(appID, false, (&api.QueryOptions{}).WithContext(ctx))
	if statusOf(err) == http.StatusNotFound {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, d.callError("read versions of job "+appID, err)
	}
	for _, v := range versions {
		if v.Meta[metaDeployment] == deploymentID && v.Version != nil {
			return *v.Version, true, nil
		}
	}
	return 0, false, nil
}

// registerError turns a failed register into one of our errors, with what
// Nomad said.
func (d *Deployer) registerError(jobID string, err error) error {
	var ure api.UnexpectedResponseError
	if !errors.As(err, &ure) {
		return d.callError("register job "+jobID, err)
	}
	body := strings.TrimSpace(ure.Body())
	switch {
	case ure.StatusCode() == http.StatusForbidden:
		return d.callError("register job "+jobID, err)
	case strings.Contains(body, "conflicting job modify index"):
		return fmt.Errorf("%w: the Nomad job %q changed while deploying; try again", ErrJobConflict, jobID)
	default:
		return fmt.Errorf("%w: Nomad answered %d: %s", ErrNomadRejected, ure.StatusCode(), body)
	}
}

// callError turns a failed Nomad call (other than a rejected job) into
// ErrNomadUnavailable. Nothing in it can hold the token: the client sends
// it in a header, which neither Nomad's answers nor net errors repeat.
func (d *Deployer) callError(action string, err error) error {
	var ure api.UnexpectedResponseError
	if errors.As(err, &ure) {
		if ure.StatusCode() == http.StatusForbidden {
			return fmt.Errorf("%w: Nomad refused the token (%s); check NOMAD_TOKEN", ErrNomadUnavailable, action)
		}
		return fmt.Errorf("%w: %s: Nomad answered %d: %s", ErrNomadUnavailable, action, ure.StatusCode(), strings.TrimSpace(ure.Body()))
	}
	return fmt.Errorf("%w: Nomad is unreachable at %s (%s): %v", ErrNomadUnavailable, d.nomad.Address(), action, err)
}

// statusOf is the HTTP status of a Nomad error, or 0.
func statusOf(err error) int {
	var ure api.UnexpectedResponseError
	if errors.As(err, &ure) {
		return ure.StatusCode()
	}
	return 0
}

// sleep waits for d or until ctx ends, and reports whether it waited fully.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
