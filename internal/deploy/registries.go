package deploy

import (
	"context"
	"maps"
	"net/http"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Private registries (feature 017). Caravana keeps the credentials for each
// registry in a Nomad Variable of its own, like the backup destination's
// secret (constitution VII); SQLite holds only the facts the console shows.

// registryVariablePrefix is where the credentials are kept, one variable per
// registry host.
const registryVariablePrefix = "caravana/registries/"

// variableKeyer writes a host so it is a legal variable path part. Nomad
// allows only letters, digits, "-", "_", "~" and "/" (validVariablePath in its
// source), and a host has "." and maybe a ":port", so "." becomes "~" and
// ":" becomes "_". A host holds neither "~" nor "_" itself, so two hosts never
// share a key. The brackets of an IPv6 host are dropped.
var variableKeyer = strings.NewReplacer(".", "~", ":", "_", "[", "", "]", "")

// RegistryVariablePath is the Nomad Variable that holds the credentials for
// host: "caravana/registries/registry~example~com_5000" for
// "registry.example.com:5000". Its items are "username" and "password".
func RegistryVariablePath(host string) string {
	return registryVariablePrefix + variableKeyer.Replace(host)
}

// RegistryCredentials returns the user name and secret stored for host. ok is
// false when there are none. It is what the registry client calls when a
// registry asks for a login, so it is also called on every deploy of an image
// from a registry that wants one. Errors name the action, never the values.
func (d *Deployer) RegistryCredentials(ctx context.Context, host string) (user, pass string, ok bool, err error) {
	items, found, err := d.readVariableIfAny(ctx, RegistryVariablePath(host))
	if err != nil {
		return "", "", false, err // names the variable already
	}
	if !found || (items["username"] == "" && items["password"] == "") {
		return "", "", false, nil
	}
	return items["username"], items["password"], true, nil
}

// SaveRegistrySecret keeps the credentials for host, replacing any before.
func (d *Deployer) SaveRegistrySecret(ctx context.Context, host, user, pass string) error {
	return d.writeVariable(ctx, RegistryVariablePath(host), map[string]string{"username": user, "password": pass})
}

// DeleteRegistrySecret forgets the credentials for host. A host with none is
// not an error.
func (d *Deployer) DeleteRegistrySecret(ctx context.Context, host string) error {
	return d.deleteVariable(ctx, RegistryVariablePath(host))
}

// registryItem is the item of a service's pull-credentials variable that names
// the registry its credentials are for. SyncRegistryCopies finds the variables
// to rewrite by it, and it is the one item that stays non-empty when the
// credentials are removed: Nomad refuses a variable whose items are all empty
// (lab finding P7).
const registryItem = "registry"

// pullItems are the two items a task's template exports as the environment its
// docker auth block interpolates (nomadjob.RegistryUsernameEnv and
// RegistryPasswordEnv). Empty values mean "no credentials": the driver then
// pulls anonymously and a private image fails with the registry's own error.
func pullItems(user, pass string) map[string]string {
	return map[string]string{nomadjob.RegistryUsernameEnv: user, nomadjob.RegistryPasswordEnv: pass}
}

// ensureRegistryVariables writes, before the job is registered, the variable
// each service with a private image reads its pull credentials from (research
// R1, R2): its own task path, nomadjob.PullCredentialsPath. The job holds
// placeholders only; this variable is where the values are, and only that
// service's task can read it. A template reading a variable that is not there
// blocks the copy, so it has to exist before the job does.
//
// A service whose image is public gets nothing, and an old variable of a
// service that stopped being private is left where it is: a rollout that fails
// reverts to the version before, whose template still reads it. It is deleted
// with the app, and SyncRegistryCopies keeps it in step meanwhile.
//
// private is the set of hosts Caravana holds credentials for, as for
// nomadjob.Translate; dep.Digests holds each service's pinned image.
func (d *Deployer) ensureRegistryVariables(ctx context.Context, a app.App, dep app.Deployment, private map[string]bool) error {
	for _, svc := range a.Services {
		host, ok := nomadjob.PrivateRegistry(dep.Digests[svc.Name], private)
		if !ok {
			continue
		}
		// ok is false only when the credentials went between the caller reading
		// the set and now: the copy then has none, as after a removal.
		user, pass, _, err := d.RegistryCredentials(ctx, host)
		if err != nil {
			return err
		}
		items := pullItems(user, pass)
		items[registryItem] = host
		if err := d.writeVariable(ctx, nomadjob.PullCredentialsPath(a.ID, svc.Name), items); err != nil {
			return err
		}
	}
	return nil
}

// withPullCredentials returns items (a backup or restore job's variable) with
// the two pull-credential items for image: set from the credentials stored for
// its registry when private has that registry, and absent otherwise. It
// replaces whatever items held before, so a service that stopped being private
// loses them.
func (d *Deployer) withPullCredentials(ctx context.Context, items map[string]string, image string, private map[string]bool) (map[string]string, error) {
	delete(items, nomadjob.RegistryUsernameEnv)
	delete(items, nomadjob.RegistryPasswordEnv)
	host, ok := nomadjob.PrivateRegistry(image, private)
	if !ok {
		return items, nil
	}
	user, pass, _, err := d.RegistryCredentials(ctx, host)
	if err != nil {
		return nil, err
	}
	maps.Copy(items, pullItems(user, pass))
	return items, nil
}

// SyncRegistryCopies rewrites the copies of host's credentials that apps
// already deployed hold, after they were saved or removed, so those apps pick
// them up the next time their copies start (research R2). It finds:
//   - each service's pull-credentials variable (nomad/jobs/<app>/<service>/<service>)
//     whose "registry" item is host;
//   - each Postgres backup or restore job's variable whose dump or restore task
//     pulls from host (its docker auth block names it).
//
// Each gets the stored credentials, or empty ones when none are stored, which
// is a removal: the variable stays, since Nomad refuses an empty one, and the
// template then renders empty values. It registers, restarts and deploys
// nothing: the jobs are not touched, and running copies keep the environment
// they started with (FR-008).
func (d *Deployer) SyncRegistryCopies(ctx context.Context, host string) error {
	metas, _, err := d.nomad.Variables().PrefixList("nomad/jobs/", (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return d.volumeError("list the pull credentials of the apps", err)
	}
	user, pass, _, err := d.RegistryCredentials(ctx, host)
	if err != nil {
		return err
	}
	fresh := pullItems(user, pass)

	for _, m := range metas {
		rest := strings.TrimPrefix(m.Path, "nomad/jobs/")
		switch parts := strings.Split(rest, "/"); {
		case len(parts) == 3 && parts[1] == parts[2]:
			// <app>/<service>/<service>: a service's own task path.
			items, found, err := d.readVariableIfAny(ctx, m.Path)
			if err != nil {
				return err
			}
			if !found || items[registryItem] != host {
				continue
			}
			if err := d.rewritePullItems(ctx, m.Path, items, fresh); err != nil {
				return err
			}
		case len(parts) == 1:
			// <app>_<service>_backup or _restore.
			if _, _, ok := splitBackupJobID(rest); !ok {
				continue
			}
			items, found, err := d.readVariableIfAny(ctx, m.Path)
			if err != nil {
				return err
			}
			if _, has := items[nomadjob.RegistryUsernameEnv]; !found || !has {
				continue
			}
			pulls, err := d.pullsFrom(ctx, rest, host)
			if err != nil {
				return err
			}
			if pulls {
				if err := d.rewritePullItems(ctx, m.Path, items, fresh); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// rewritePullItems writes the variable at path with fresh over its pull items
// and everything else as it was, unless they are already so.
func (d *Deployer) rewritePullItems(ctx context.Context, path string, items, fresh map[string]string) error {
	next := maps.Clone(items)
	maps.Copy(next, fresh)
	if maps.Equal(items, next) {
		return nil
	}
	return d.writeVariable(ctx, path, next)
}

// pullsFrom reports whether the Postgres backup or restore job jobID has a task
// (dump or restore) whose docker auth block names host. A job Nomad no longer
// has pulls from nowhere.
func (d *Deployer) pullsFrom(ctx context.Context, jobID, host string) (bool, error) {
	job, _, err := d.nomad.Jobs().Info(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if statusOf(err) == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, d.callError("read job "+jobID, err)
	}
	for _, g := range job.TaskGroups {
		for _, t := range g.Tasks {
			if t.Name != "dump" && t.Name != "restore" {
				continue
			}
			// The JSON job format holds a block as a list of objects.
			blocks, _ := t.Config["auth"].([]any)
			for _, b := range blocks {
				if auth, _ := b.(map[string]any); auth["server_address"] == host {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// TestRegistry asks host's registry to accept the credentials stored for it,
// within the time one image lookup gets. The error wraps registry.ErrUnauthorized
// or registry.ErrUnavailable, or says that no credentials are stored.
func (d *Deployer) TestRegistry(ctx context.Context, host string) error {
	ctx, cancel := context.WithTimeout(ctx, d.Timing.withDefaults().ImageTimeout)
	defer cancel()
	return d.registry.Test(ctx, host)
}
