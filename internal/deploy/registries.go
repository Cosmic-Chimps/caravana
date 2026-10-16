package deploy

import (
	"context"
	"strings"
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

// SyncRegistryCopies rewrites the per-task copies of host's credentials after
// they were saved or removed, so apps already deployed pick them up the next
// time they start (research R2). Feature 017 task T009 writes the copies;
// until then no variable names a registry, so there is nothing to rewrite.
func (d *Deployer) SyncRegistryCopies(ctx context.Context, host string) error {
	return nil
}

// TestRegistry asks host's registry to accept the credentials stored for it,
// within the time one image lookup gets. The error wraps registry.ErrUnauthorized
// or registry.ErrUnavailable, or says that no credentials are stored.
func (d *Deployer) TestRegistry(ctx context.Context, host string) error {
	ctx, cancel := context.WithTimeout(ctx, d.Timing.withDefaults().ImageTimeout)
	defer cancel()
	return d.registry.Test(ctx, host)
}
