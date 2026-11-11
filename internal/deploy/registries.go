package deploy

import (
	"context"
	"maps"
	"net/http"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
)

// Private registries (feature 017). Caravana keeps the credentials for each
// registry in a Nomad Variable of its own, like the backup destination's
// secret (constitution VII); SQLite holds only the facts the console shows.

// registryVariablePrefix is where the credentials are kept, one variable per
// registry entry (a host, or a host with a path prefix).
const registryVariablePrefix = "caravana/registries/"

// variableKeyer writes a host so it is a legal variable path part. Nomad
// allows only letters, digits, "-", "_", "~" and "/" (validVariablePath in its
// source), and a host has "." and maybe a ":port", so "." becomes "~" and
// ":" becomes "_". A host holds neither "~" nor "_" itself, so two hosts never
// share a key. The brackets of an IPv6 host are dropped.
var variableKeyer = strings.NewReplacer(".", "~", ":", "_", "[", "", "]", "")

// pathKeyer writes an entry's path prefix the same way: its "." becomes "~".
// A path already holds only lowercase letters, digits, "." "_" "-" and "/", all
// but "." legal, and no "~" of its own.
var pathKeyer = strings.NewReplacer(".", "~")

// RegistryVariablePath is the Nomad Variable that holds the credentials for an
// entry: "caravana/registries/registry~example~com_5000" for the host
// "registry.example.com:5000", and "caravana/registries/ghcr~io/acme" for
// "ghcr.io/acme". Its items are "username" and "password".
//
// Two entries never share a path: a host has no "/", so the key's first
// segment is the whole host, and the rest is the path prefix, where "." can
// only come from a "~" the keyer wrote (a path has none of its own). The keys
// of host-only entries, the only ones before paths existed, are unchanged.
func RegistryVariablePath(entry string) string {
	host, path := registry.SplitEntry(entry)
	key := variableKeyer.Replace(host)
	if path != "" {
		key += "/" + pathKeyer.Replace(path)
	}
	return registryVariablePrefix + key
}

// RegistryCredentials returns the user name and secret stored for exactly this
// entry (a host, or a host with a path prefix). ok is false when there are
// none. Errors name the action, never the values.
func (d *Deployer) RegistryCredentials(ctx context.Context, entry string) (user, pass string, ok bool, err error) {
	items, found, err := d.readVariableIfAny(ctx, RegistryVariablePath(entry))
	if err != nil {
		return "", "", false, err // names the variable already
	}
	if !found || (items["username"] == "" && items["password"] == "") {
		return "", "", false, nil
	}
	return items["username"], items["password"], true, nil
}

// pullCredentials is what the registry client calls when a registry asks for a
// login, so it is also called on every deploy of an image from a registry that
// wants one: the credentials of the most specific stored entry that covers the
// image's repository, the host's own entry being the last one tried.
func (d *Deployer) pullCredentials(ctx context.Context, host, repository string) (user, pass string, ok bool, err error) {
	for _, entry := range registry.EntryCandidates(host, repository) {
		user, pass, ok, err = d.RegistryCredentials(ctx, entry)
		if err != nil || ok {
			return user, pass, ok, err
		}
	}
	return "", "", false, nil
}

// SaveRegistrySecret keeps the credentials for an entry, replacing any before.
func (d *Deployer) SaveRegistrySecret(ctx context.Context, entry, user, pass string) error {
	return d.writeVariable(ctx, RegistryVariablePath(entry), map[string]string{"username": user, "password": pass})
}

// DeleteRegistrySecret forgets the credentials for an entry. An entry with none
// is not an error.
func (d *Deployer) DeleteRegistrySecret(ctx context.Context, entry string) error {
	return d.deleteVariable(ctx, RegistryVariablePath(entry))
}

// registryItem is the item of a service's pull-credentials variable that names
// the registry entry its credentials are for (a host, or host/path). SyncRegistryCopies finds the variables
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
// private is the set of entries Caravana holds credentials for, as for
// nomadjob.Translate; dep.Digests holds each service's pinned image.
func (d *Deployer) ensureRegistryVariables(ctx context.Context, a app.App, dep app.Deployment, private map[string]bool) error {
	for _, svc := range a.Services {
		entry, ok := nomadjob.PrivateRegistry(dep.Digests[svc.Name], private)
		if !ok {
			continue
		}
		// ok is false only when the credentials went between the caller reading
		// the set and now: the copy then has none, as after a removal.
		user, pass, _, err := d.RegistryCredentials(ctx, entry)
		if err != nil {
			return err
		}
		items := pullItems(user, pass)
		items[registryItem] = entry
		if err := d.writeVariable(ctx, nomadjob.PullCredentialsPath(a.ID, svc.Name), items); err != nil {
			return err
		}
	}
	return nil
}

// withPullCredentials returns items (a backup or restore job's variable) with
// the two pull-credential items for image: set from the credentials of the
// entry of private that covers it, and absent when none does. It
// replaces whatever items held before, so a service that stopped being private
// loses them.
func (d *Deployer) withPullCredentials(ctx context.Context, items map[string]string, image string, private map[string]bool) (map[string]string, error) {
	delete(items, nomadjob.RegistryUsernameEnv)
	delete(items, nomadjob.RegistryPasswordEnv)
	entry, ok := nomadjob.PrivateRegistry(image, private)
	if !ok {
		return items, nil
	}
	user, pass, _, err := d.RegistryCredentials(ctx, entry)
	if err != nil {
		return nil, err
	}
	maps.Copy(items, pullItems(user, pass))
	return items, nil
}

// SyncRegistryCopies rewrites the copies of registry credentials that apps
// already deployed hold, after the entry was saved or removed, so those apps
// pick them up the next time their copies start (research R2). private is the
// set of entries after the change. Only the copies that use the entry, before
// or after, are touched; it finds, among the copies of entry's host:
//   - each service's pull-credentials variable (nomad/jobs/<app>/<service>/<service>)
//     whose "registry" item is on that host;
//   - each Postgres backup or restore job's variable whose dump or restore task
//     pulls from that host (its docker auth block names it).
//
// For each, the entry that now covers its image (nomadjob.PrivateRegistry over
// private, the image read from the job) decides the credentials, so adding a
// prefix moves the copies under it off the host entry, and removing one moves
// them back. A copy gets the credentials of that entry, or empty ones when
// none covers it: the variable stays, since Nomad refuses an empty one, and the
// template then renders empty values. Variables already right are not written.
// It registers, restarts and deploys nothing: the jobs are not touched, and
// running copies keep the environment they started with (FR-008).
func (d *Deployer) SyncRegistryCopies(ctx context.Context, entry string, private map[string]bool) error {
	host := registry.EntryHost(entry)
	metas, _, err := d.nomad.Variables().PrefixList("nomad/jobs/", (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return d.volumeError("list the pull credentials of the apps", err)
	}
	s := &registrySync{d: d, entry: entry, host: host, private: private,
		creds: map[string]pullCreds{}, images: map[string]map[string]string{}}

	for _, m := range metas {
		rest := strings.TrimPrefix(m.Path, "nomad/jobs/")
		switch parts := strings.Split(rest, "/"); {
		case len(parts) == 3 && parts[1] == parts[2]:
			// <app>/<service>/<service>: a service's own task path.
			items, found, err := d.readVariableIfAny(ctx, m.Path)
			if err != nil {
				return err
			}
			if !found || registry.EntryHost(items[registryItem]) != host {
				continue
			}
			if err := s.syncService(ctx, m.Path, parts[0], parts[1], items); err != nil {
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
			image, err := d.pullImage(ctx, rest, host)
			if err != nil {
				return err
			}
			if image == "" {
				continue
			}
			fresh, _, err := s.freshFor(ctx, image)
			if err != nil {
				return err
			}
			if err := d.rewritePullItems(ctx, m.Path, items, fresh); err != nil {
				return err
			}
		}
	}
	return nil
}

// pullCreds is what an entry holds, read once per sync.
type pullCreds struct{ user, pass string }

// registrySync is one SyncRegistryCopies call's state: the reads it caches.
type registrySync struct {
	d       *Deployer
	entry   string
	host    string
	private map[string]bool
	creds   map[string]pullCreds         // by entry
	images  map[string]map[string]string // app -> service -> image from its job
}

// entryCreds is the credentials stored for an entry; empty when none.
func (s *registrySync) entryCreds(ctx context.Context, entry string) (pullCreds, error) {
	if c, ok := s.creds[entry]; ok {
		return c, nil
	}
	user, pass, _, err := s.d.RegistryCredentials(ctx, entry)
	if err != nil {
		return pullCreds{}, err
	}
	c := pullCreds{user, pass}
	s.creds[entry] = c
	return c, nil
}

// freshFor returns the pull items an image gets from the entry that covers it
// now, and that entry ("" when none does).
func (s *registrySync) freshFor(ctx context.Context, image string) (items map[string]string, entry string, err error) {
	entry, _ = nomadjob.PrivateRegistry(image, s.private)
	return s.itemsOf(ctx, entry)
}

// itemsOf returns the pull items of entry's stored credentials, empty ones when
// entry is "" (no entry covers the copy) or has none, and entry itself.
func (s *registrySync) itemsOf(ctx context.Context, entry string) (map[string]string, string, error) {
	if entry == "" {
		return pullItems("", ""), "", nil
	}
	c, err := s.entryCreds(ctx, entry)
	if err != nil {
		return nil, "", err
	}
	return pullItems(c.user, c.pass), entry, nil
}

// syncService rewrites one service's pull-credentials variable.
func (s *registrySync) syncService(ctx context.Context, path, appID, service string, items map[string]string) error {
	image, err := s.serviceImage(ctx, appID, service)
	if err != nil {
		return err
	}
	var fresh map[string]string
	var entry string
	if ref, err := registry.Parse(image); image != "" && err == nil && ref.Registry == s.host {
		if fresh, entry, err = s.freshFor(ctx, image); err != nil {
			return err
		}
	} else {
		// The job is gone, or its image is somewhere else: all that is known is
		// which entry the copy holds, so only that entry's own change applies.
		if items[registryItem] != s.entry {
			return nil
		}
		if s.private[s.entry] {
			entry = s.entry
		}
		if fresh, entry, err = s.itemsOf(ctx, entry); err != nil {
			return err
		}
	}
	if entry != "" {
		fresh[registryItem] = entry
	}
	// With no entry the registry item keeps its old value: it must stay
	// non-empty (see registryItem).
	return s.d.rewritePullItems(ctx, path, items, fresh)
}

// serviceImage is the image of service's task in app's job, "" when Nomad has
// no such job.
func (s *registrySync) serviceImage(ctx context.Context, appID, service string) (string, error) {
	images, ok := s.images[appID]
	if !ok {
		var err error
		if images, err = s.d.jobImages(ctx, appID); err != nil {
			return "", err
		}
		s.images[appID] = images
	}
	return images[service], nil
}

// jobImages maps each service of an app's job to the image of its main task
// (the task named like its group). A job Nomad no longer has gives none.
func (d *Deployer) jobImages(ctx context.Context, appID string) (map[string]string, error) {
	job, _, err := d.nomad.Jobs().Info(appID, (&api.QueryOptions{}).WithContext(ctx))
	if statusOf(err) == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, d.callError("read job "+appID, err)
	}
	images := map[string]string{}
	for _, g := range job.TaskGroups {
		if g.Name == nil {
			continue
		}
		for _, t := range g.Tasks {
			if t.Name == *g.Name {
				if image, ok := t.Config["image"].(string); ok {
					images[*g.Name] = image
				}
			}
		}
	}
	return images, nil
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

// pullImage is the image of the dump or restore task of the Postgres backup or
// restore job jobID whose docker auth block names host, "" when none does. A
// job Nomad no longer has pulls from nowhere.
func (d *Deployer) pullImage(ctx context.Context, jobID, host string) (string, error) {
	job, _, err := d.nomad.Jobs().Info(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if statusOf(err) == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", d.callError("read job "+jobID, err)
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
					image, _ := t.Config["image"].(string)
					return image, nil
				}
			}
		}
	}
	return "", nil
}

// TestRegistry asks the registry of an entry to accept the credentials stored
// for it, within the time one image lookup gets. The error wraps
// registry.ErrUnauthorized or registry.ErrUnavailable, or says that no
// credentials are stored. For an entry with a path it checks the login only:
// the registry doesn't say which images the login can read.
func (d *Deployer) TestRegistry(ctx context.Context, entry string) error {
	ctx, cancel := context.WithTimeout(ctx, d.Timing.withDefaults().ImageTimeout)
	defer cancel()
	host, path := registry.SplitEntry(entry)
	return d.registry.Test(ctx, host, path)
}
