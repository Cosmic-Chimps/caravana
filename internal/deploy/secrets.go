package deploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/secrets"
)

// Variables keeps secrets in Nomad Variables. It satisfies secrets.Provider.
//
// Each service with secrets has one variable, at nomadjob.SecretsPath (its
// tasks can read that path and no other service's). The sets a deployment
// replaces are kept in one more variable, keptPath, until that deployment
// ends (research R4). No task can read keptPath: it would be the task path
// of a group "caravana" with a task "previous", and Caravana names every
// task after its group.
type Variables struct {
	nomad *api.Client
}

// NewVariables returns the Nomad Variables store.
func NewVariables(nomad *api.Client) *Variables {
	return &Variables{nomad: nomad}
}

// keptOwner is the kept copy's item holding the deployment that owns it. The
// other items are "<service>/<NAME>", which always contain a "/".
const keptOwner = "deployment"

func appPrefix(appID string) string { return "nomad/jobs/" + appID + "/" }

func keptPath(appID string) string { return appPrefix(appID) + "caravana/previous" }

// Read implements secrets.Provider: every variable directly under the app's
// prefix is one service's set.
func (v *Variables) Read(ctx context.Context, appID string) (secrets.Sets, error) {
	metas, _, err := v.nomad.Variables().PrefixList(appPrefix(appID), (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, v.error("list the secrets of "+appID, err)
	}
	out := secrets.Sets{}
	for _, m := range metas {
		service := strings.TrimPrefix(m.Path, appPrefix(appID))
		if strings.Contains(service, "/") {
			continue // the kept copy, or something that isn't a service's set
		}
		items, err := v.read(ctx, m.Path)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			out[service] = items
		}
	}
	return out, nil
}

// Replace implements secrets.Provider.
func (v *Variables) Replace(ctx context.Context, appID, deploymentID string, sets secrets.Sets) error {
	current, err := v.Read(ctx, appID)
	if err != nil {
		return err
	}
	kept, err := v.read(ctx, keptPath(appID))
	if err != nil {
		return err
	}
	switch {
	case kept != nil:
		// An earlier deploy hasn't got healthy: its copy still holds the sets
		// of the version running now, so it stays, under the new owner.
		kept[keptOwner] = deploymentID
		if err := v.write(ctx, keptPath(appID), kept); err != nil {
			return err
		}
	case !current.Equal(sets):
		if err := v.write(ctx, keptPath(appID), keptItems(deploymentID, current)); err != nil {
			return err
		}
	}
	return v.makeLive(ctx, appID, current, sets)
}

// Commit implements secrets.Provider.
func (v *Variables) Commit(ctx context.Context, appID, deploymentID string) error {
	kept, err := v.read(ctx, keptPath(appID))
	if err != nil || kept[keptOwner] != deploymentID {
		return err
	}
	return v.delete(ctx, keptPath(appID))
}

// Restore implements secrets.Provider.
func (v *Variables) Restore(ctx context.Context, appID, deploymentID string) error {
	kept, err := v.read(ctx, keptPath(appID))
	if err != nil || kept[keptOwner] != deploymentID {
		return err
	}
	current, err := v.Read(ctx, appID)
	if err != nil {
		return err
	}
	if err := v.makeLive(ctx, appID, current, keptSets(kept)); err != nil {
		return err
	}
	return v.delete(ctx, keptPath(appID))
}

// Delete implements secrets.Provider.
func (v *Variables) Delete(ctx context.Context, appID string) error {
	metas, _, err := v.nomad.Variables().PrefixList(appPrefix(appID), (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return v.error("list the secrets of "+appID, err)
	}
	for _, m := range metas {
		if err := v.delete(ctx, m.Path); err != nil {
			return err
		}
	}
	return nil
}

// makeLive writes the variables that change from current to sets: a changed
// set is written whole, a set that is gone is deleted.
func (v *Variables) makeLive(ctx context.Context, appID string, current, sets secrets.Sets) error {
	for service, set := range sets {
		if len(set) > 0 && !maps.Equal(set, current[service]) {
			if err := v.write(ctx, nomadjob.SecretsPath(appID, service), set); err != nil {
				return err
			}
		}
	}
	for service := range current {
		if len(sets[service]) == 0 {
			if err := v.delete(ctx, nomadjob.SecretsPath(appID, service)); err != nil {
				return err
			}
		}
	}
	return nil
}

// keptItems turns sets into the kept copy's items.
func keptItems(deploymentID string, sets secrets.Sets) map[string]string {
	items := map[string]string{keptOwner: deploymentID}
	for service, set := range sets {
		for name, value := range set {
			items[service+"/"+name] = value
		}
	}
	return items
}

// keptSets turns the kept copy's items back into sets.
func keptSets(items map[string]string) secrets.Sets {
	out := secrets.Sets{}
	for key, value := range items {
		service, name, ok := strings.Cut(key, "/")
		if !ok {
			continue // the owner
		}
		if out[service] == nil {
			out[service] = map[string]string{}
		}
		out[service][name] = value
	}
	return out
}

// read returns the items at path, or nil when there is no variable there.
func (v *Variables) read(ctx context.Context, path string) (map[string]string, error) {
	vr, _, err := v.nomad.Variables().Read(path, (&api.QueryOptions{}).WithContext(ctx))
	if errors.Is(err, api.ErrVariablePathNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, v.error("read "+path, err)
	}
	return vr.Items, nil
}

func (v *Variables) write(ctx context.Context, path string, items map[string]string) error {
	_, _, err := v.nomad.Variables().Create(&api.Variable{Path: path, Items: items}, (&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		return v.error("write "+path, err)
	}
	return nil
}

func (v *Variables) delete(ctx context.Context, path string) error {
	if _, err := v.nomad.Variables().Delete(path, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		return v.error("delete "+path, err)
	}
	return nil
}

// error turns a failed Nomad Variables call into secrets.ErrUnavailable,
// with what Nomad said. Nomad's answers name paths and never echo items, so
// no value can end up in the message. When Nomad can't be reached at all,
// the error is ErrNomadUnavailable too: that is the cause to report, even
// for an app without secrets (the API checks it first).
func (v *Variables) error(action string, err error) error {
	var ure api.UnexpectedResponseError
	if errors.As(err, &ure) {
		if ure.StatusCode() == http.StatusForbidden {
			return fmt.Errorf("%w: Nomad refused the token (%s); check NOMAD_TOKEN and that its policy allows variables on nomad/jobs/*",
				secrets.ErrUnavailable, action)
		}
		return fmt.Errorf("%w: %s: Nomad answered %d: %s", secrets.ErrUnavailable, action, ure.StatusCode(), strings.TrimSpace(ure.Body()))
	}
	return fmt.Errorf("%w: %w: Nomad is unreachable at %s (%s): %v", secrets.ErrUnavailable, ErrNomadUnavailable, v.nomad.Address(), action, err)
}
