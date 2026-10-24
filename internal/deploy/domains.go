package deploy

import (
	"context"
	"fmt"
	"maps"

	"golang.org/x/crypto/bcrypt"
)

// Team-only domains (feature 023). A domain with TeamOnly gets HTTP basic auth
// in front of it, with one shared password and the user "team". Traefik's
// basicauth middleware takes htpasswd lines, so the password is kept as a
// bcrypt hash: in SQLite as a verifier, and here in the Nomad Variable that
// Traefik's job renders into its file provider (a middleware named
// "<router>-auth@file", see plan.md). The password itself is shown once and
// never stored.

// TraefikVariablePath is the Nomad Variable Traefik's own job reads: the job
// "traefik" can read nomad/jobs/traefik without any ACL policy.
const TraefikVariablePath = "nomad/jobs/traefik"

// teamMarkerItem keeps the variable alive while no domain is Team only: Nomad
// refuses a variable with no items (as the registries' credential variables
// do), and Traefik's template needs the variable to exist.
const teamMarkerItem = "CARAVANA_TEAM"

// TeamUser is the user name of every Team-only domain.
const TeamUser = "team"

// teamBcryptCost is bcrypt's cost for the htpasswd hash. 10 is bcrypt's own
// default and what htpasswd -B uses; Traefik checks it on every request to the
// domain, so a higher cost would slow every page.
const teamBcryptCost = 10

// SyncTeamPasswords makes the variable nomad/jobs/traefik hold exactly
// entries: the key is the middleware name ("<router>-auth") and the value is
// its htpasswd line ("team:<hash>", from TeamLine). Whatever the variable held
// before is replaced, so a removed domain's line goes away. With no entries it
// holds a marker item only.
func (d *Deployer) SyncTeamPasswords(ctx context.Context, entries map[string]string) error {
	items := maps.Clone(entries)
	if len(items) == 0 {
		items = map[string]string{teamMarkerItem: "none"}
	}
	return d.writeVariable(ctx, TraefikVariablePath, items)
}

// TeamPasswordHash is the bcrypt hash of a Team-only domain's password, the
// verifier Caravana stores. It is never the password.
func TeamPasswordHash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), teamBcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash the team password: %w", err)
	}
	return string(h), nil
}

// CheckTeamPassword reports whether password matches hash.
func CheckTeamPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// TeamLine is the htpasswd line for a hash: what Traefik's basic auth reads.
func TeamLine(hash string) string {
	return TeamUser + ":" + hash
}
