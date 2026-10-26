package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Team-only domains (feature 023, plan "Team only"). A domain with TeamOnly
// gets HTTP basic auth in front of it: the user "team" and one password per
// domain, which Caravana makes and shows once. Only its bcrypt hash is kept,
// in team_passwords and in the Nomad Variable nomad/jobs/traefik, which
// Traefik's file provider renders into the middleware "<router>-auth" the
// domain's router names. The variable is one for every app, so it is always
// rebuilt whole from the table (syncTeamPasswordsLocked), under teamMu.
//
// A deploy makes the password of each Team-only domain that has none, and
// writes the variable before Nomad gets the job: the router never names a
// middleware Traefik doesn't have (a missing one disables the router, which
// fails closed). After Nomad has the job, the rows of hosts that are no longer
// Team-only domains of their app are deleted. The password is in the answer
// of the deploy that made it (DeployResult.TeamPasswords) and of
// RotateTeamPassword, and nowhere else: not in a record, a log line, a job, an
// export, an error or any later answer.

var (
	// ErrNotTeamOnly is RotateTeamPassword for a domain without TeamOnly.
	ErrNotTeamOnly = errors.New("the domain is not Team only")
	// ErrDomainTaken is a deploy that adds a host another app already has
	// (as a domain, or as the pair its wwwRedirect redirects). The error is a
	// *DomainTakenError, which names the field and the other app.
	ErrDomainTaken = errors.New("another app has this domain")
)

// DomainTakenError is the refusal of a deploy that adds a host another app
// has. Field is the host's field in the request
// ("services[0].domains[1].host"), App the other app's ID.
type DomainTakenError struct {
	Field, Host, App string
}

func (e *DomainTakenError) Error() string {
	return fmt.Sprintf("%s: %s is a domain of app %s; remove it there first", e.Field, e.Host, e.App)
}

// Unwrap makes errors.Is(err, ErrDomainTaken) true.
func (e *DomainTakenError) Unwrap() error { return ErrDomainTaken }

// DeployResult is a deployment as a deploy answers it, with what is shown only
// once.
type DeployResult struct {
	store.DeploymentRecord
	// TeamPasswords holds, by host, the password of each domain this deploy
	// turned Team only (it had no password yet). It is nil on every other
	// deploy. Show it once; Caravana keeps only its hash.
	TeamPasswords map[string]string
}

// teamPasswordBytes is how much randomness a Team-only password has.
const teamPasswordBytes = 24

// newTeamPassword makes a password: 24 random bytes, base64url without
// padding (32 characters).
func newTeamPassword() string {
	b := make([]byte, teamPasswordBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24+)
	return base64.RawURLEncoding.EncodeToString(b)
}

// teamOnlyDomains lists a's Team-only domains: host → the service that has it.
func teamOnlyDomains(a app.App) map[string]string {
	out := map[string]string{}
	for _, svc := range a.Services {
		for _, d := range svc.Domains {
			if d.TeamOnly {
				out[d.Host] = svc.Name
			}
		}
	}
	return out
}

// teamEntries is what the variable nomad/jobs/traefik holds: for each row
// whose app still has the host as a Team-only domain, the middleware of the
// host's router and the htpasswd line of the row's hash. apps holds the apps
// by ID. A row no app claims that way is left out (and pruned later).
func teamEntries(rows []store.TeamPasswordRecord, apps map[string]app.App) map[string]string {
	out := map[string]string{}
	for _, row := range rows {
		a, ok := apps[row.AppID]
		if !ok {
			continue
		}
		svc, ok := teamOnlyDomains(a)[row.Host]
		if !ok {
			continue
		}
		out[nomadjob.TeamAuthMiddleware(nomadjob.DomainRouterName(a.ID, svc, row.Host))] = deploy.TeamLine(row.Hash)
	}
	return out
}

// storedAppsByID reads every stored app, with a in place of the stored one of
// its ID when a is set (a deploy's own app, which is newer than any copy).
func (s *Service) storedAppsByID(ctx context.Context, a *app.App) (map[string]app.App, error) {
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]app.App, len(recs)+1)
	for _, rec := range recs {
		out[rec.App.ID] = rec.App
	}
	if a != nil {
		out[a.ID] = *a
	}
	return out, nil
}

// syncTeamPasswordsLocked rewrites the variable from the table and the apps.
// The caller holds teamMu.
func (s *Service) syncTeamPasswordsLocked(ctx context.Context, apps map[string]app.App) error {
	rows, err := s.store.TeamPasswords(ctx)
	if err != nil {
		return err
	}
	if err := s.deployer.SyncTeamPasswords(ctx, teamEntries(rows, apps)); err != nil {
		return fmt.Errorf("write the Team-only passwords for Traefik: %w", err)
	}
	return nil
}

// ensureTeamPasswords makes a password for each Team-only domain of a that has
// none (or whose row belongs to another app, left from before), stores its
// hash and writes the variable, so the routers of a's job find their
// middlewares. It returns the passwords it made, by host (nil for none). An app
// without a Team-only domain writes nothing.
func (s *Service) ensureTeamPasswords(ctx context.Context, a app.App) (map[string]string, error) {
	want := teamOnlyDomains(a)
	if len(want) == 0 {
		return nil, nil
	}
	s.teamMu.Lock()
	defer s.teamMu.Unlock()

	rows, err := s.store.TeamPasswords(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, row := range rows {
		if row.AppID == a.ID {
			have[row.Host] = true
		}
	}
	var made map[string]string
	for _, host := range slices.Sorted(maps.Keys(want)) {
		if have[host] {
			continue
		}
		password := newTeamPassword()
		hash, err := deploy.TeamPasswordHash(password)
		if err != nil {
			return nil, err
		}
		if err := s.store.SetTeamPassword(ctx, host, a.ID, hash, s.clock.Now().UTC()); err != nil {
			s.dropTeamPasswordsLocked(ctx, made)
			return nil, err
		}
		if made == nil {
			made = map[string]string{}
		}
		made[host] = password
	}
	apps, err := s.storedAppsByID(ctx, &a)
	if err == nil {
		err = s.syncTeamPasswordsLocked(ctx, apps)
	}
	if err != nil {
		// Nobody will see these passwords: drop them, so the next deploy
		// makes (and shows) new ones.
		s.dropTeamPasswordsLocked(ctx, made)
		return nil, err
	}
	return made, nil
}

// dropTeamPasswordsLocked deletes the rows of the hosts in made, passwords a
// deploy made and won't show. Failures are logged. The caller holds teamMu.
func (s *Service) dropTeamPasswordsLocked(ctx context.Context, made map[string]string) {
	for host := range made {
		if err := s.store.DeleteTeamPassword(ctx, host); err != nil {
			s.log.Warn("delete a Team-only password nobody saw", "domain", host, "error", err)
		}
	}
}

// forgetTeamPasswords undoes what ensureTeamPasswords made for a deploy that
// then failed before Nomad accepted the job: the passwords were never shown,
// so their rows go and the variable is written again without them.
func (s *Service) forgetTeamPasswords(ctx context.Context, made map[string]string) {
	if len(made) == 0 {
		return
	}
	s.teamMu.Lock()
	defer s.teamMu.Unlock()
	s.dropTeamPasswordsLocked(ctx, made)
	apps, err := s.storedAppsByID(ctx, nil)
	if err == nil {
		err = s.syncTeamPasswordsLocked(ctx, apps)
	}
	if err != nil {
		s.log.Warn("write the Team-only passwords for Traefik", "error", err)
	}
}

// hasTeamPasswords says whether any Team-only password belongs to appID. A
// table that can't be read counts as yes, so the caller writes the variable.
func (s *Service) hasTeamPasswords(ctx context.Context, appID string) bool {
	rows, err := s.store.TeamPasswords(ctx)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(rows, func(r store.TeamPasswordRecord) bool { return r.AppID == appID })
}

// syncTeamPasswords writes the variable from the table and the stored apps.
func (s *Service) syncTeamPasswords(ctx context.Context) error {
	s.teamMu.Lock()
	defer s.teamMu.Unlock()
	apps, err := s.storedAppsByID(ctx, nil)
	if err != nil {
		return err
	}
	return s.syncTeamPasswordsLocked(ctx, apps)
}

// pruneTeamPasswords deletes the rows of hosts that are no longer Team-only
// domains of their app (the option was turned off, the domain removed, the
// app deleted) and, when it deleted any, writes the variable again. a, when
// set, is the app a deploy just registered, newer than any stored copy.
func (s *Service) pruneTeamPasswords(ctx context.Context, a *app.App) error {
	s.teamMu.Lock()
	defer s.teamMu.Unlock()
	rows, err := s.store.TeamPasswords(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	apps, err := s.storedAppsByID(ctx, a)
	if err != nil {
		return err
	}
	pruned := false
	for _, row := range rows {
		owner, ok := apps[row.AppID]
		if ok {
			if _, still := teamOnlyDomains(owner)[row.Host]; still {
				continue
			}
		}
		if err := s.store.DeleteTeamPassword(ctx, row.Host); err != nil {
			return err
		}
		pruned = true
	}
	if !pruned {
		return nil
	}
	return s.syncTeamPasswordsLocked(ctx, apps)
}

// RotateTeamPassword gives a Team-only domain of appID a new password, which
// it returns: the one time it is shown. The old one stops working once
// Traefik reads the variable again (no deploy, no restart).
//
// Errors: store.ErrNotFound for an unknown app, ErrAppStopped for a stopped
// one, ErrDomainNotFound when the app has no such domain, ErrNotTeamOnly when
// the domain isn't Team only. When the variable can't be written, the old
// password stays.
func (s *Service) RotateTeamPassword(ctx context.Context, appID, host string) (string, error) {
	host = normalizeHost(host)
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return "", err
	}
	if rec.StoppedAt != nil {
		return "", fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
	found, teamOnly := false, false
	for _, svc := range rec.App.Services {
		for _, d := range svc.Domains {
			if d.Host == host {
				found, teamOnly = true, d.TeamOnly
			}
		}
	}
	switch {
	case !found:
		return "", fmt.Errorf("app %q has no domain %q: %w", appID, host, ErrDomainNotFound)
	case !teamOnly:
		return "", fmt.Errorf("domain %q: %w", host, ErrNotTeamOnly)
	}

	s.teamMu.Lock()
	defer s.teamMu.Unlock()
	old, oldErr := s.store.TeamPassword(ctx, host)
	if oldErr != nil && !errors.Is(oldErr, store.ErrNotFound) {
		return "", oldErr
	}
	password := newTeamPassword()
	hash, err := deploy.TeamPasswordHash(password)
	if err != nil {
		return "", err
	}
	if err := s.store.SetTeamPassword(ctx, host, appID, hash, s.clock.Now().UTC()); err != nil {
		return "", err
	}
	apps, err := s.storedAppsByID(ctx, nil)
	if err == nil {
		err = s.syncTeamPasswordsLocked(ctx, apps)
	}
	if err != nil {
		// Traefik still has the old hash: keep the table saying so.
		var undo error
		if oldErr == nil {
			undo = s.store.SetTeamPassword(ctx, host, old.AppID, old.Hash, old.CreatedAt)
		} else {
			undo = s.store.DeleteTeamPassword(ctx, host)
		}
		if undo != nil {
			s.log.Warn("put back a Team-only password after a failed rotation", "domain", host, "error", undo)
		}
		return "", err
	}
	s.domainsChanged(appID)
	return password, nil
}

// checkDomainsTaken refuses a deploy of a that adds a host another stored app
// has as a domain or redirects with its wwwRedirect, and a wwwRedirect whose
// pair is another app's domain. A host the stored a already has is not
// checked again, so two apps that claimed one host before this check existed
// can still deploy (the Domains page says "two apps claim this address").
func (s *Service) checkDomainsTaken(ctx context.Context, a app.App) error {
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return err
	}
	had := map[string]bool{}
	claims := map[string]string{} // host → the first other app that has it
	for _, rec := range recs {
		for _, svc := range rec.App.Services {
			for _, d := range svc.Domains {
				if rec.App.ID == a.ID {
					had[d.Host] = true
					if pair := d.RedirectPair(); d.WWWRedirect && pair != "" {
						had[pair] = true
					}
					continue
				}
				if _, ok := claims[d.Host]; !ok {
					claims[d.Host] = rec.App.ID
				}
				if pair := d.RedirectPair(); d.WWWRedirect && pair != "" {
					if _, ok := claims[pair]; !ok {
						claims[pair] = rec.App.ID
					}
				}
			}
		}
	}
	for i, svc := range a.Services {
		for j, d := range svc.Domains {
			if other, ok := claims[d.Host]; ok && !had[d.Host] {
				return &DomainTakenError{Field: fmt.Sprintf("services[%d].domains[%d].host", i, j), Host: d.Host, App: other}
			}
			pair := d.RedirectPair()
			if other, ok := claims[pair]; ok && d.WWWRedirect && pair != "" && !had[pair] {
				return app.NewFieldError(fmt.Sprintf("services[%d].domains[%d].wwwRedirect", i, j),
					"%s would redirect %s, which is a domain of app %s", d.Host, pair, other)
			}
		}
	}
	return nil
}
