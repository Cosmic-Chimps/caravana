package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// registryPassword is a secret no page, answer, record or file outside the
// Nomad Variable may contain.
const registryPassword = "s3cr3t-SENTINEL-registry-pw"

// US2: each input is validated, and the host is stored in its canonical form.
func TestSaveRegistryValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	tests := []struct {
		name            string
		host, user      string
		password        *string
		wantField, want string
	}{
		{"empty host", "", "u", new("p"), "host", ""},
		{"a Docker Hub user is not a host", "acme", "u", new("p"), "host", ""},
		{"an image is not a host", "ghcr.io/acme/shop", "u", new("p"), "host", ""},
		{"a bad port", "ghcr.io:port", "u", new("p"), "host", ""},
		{"no user name", "ghcr.io", "  ", new("p"), "username", ""},
		{"no password the first time", "ghcr.io", "u", nil, "password", "is required the first time"},
		{"an empty password", "ghcr.io", "u", new(""), "password", "is empty"},
		{"a port and capitals", "Registry.Example.COM:5000", "u", new("p"), "", "registry.example.com:5000"},
		{"the Docker Hub alias", "Index.Docker.IO", "u", new("p"), "", "docker.io"},
		{"localhost", "localhost:5000", "u", new("p"), "", "localhost:5000"},
		{"an IPv6 loopback", "[::1]:5000", "u", new("p"), "", "[::1]:5000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.svc.SaveRegistry(ctx, tc.host, tc.user, tc.password)
			if tc.wantField != "" {
				var fe *app.FieldError
				if !errors.As(err, &fe) || fe.Field != tc.wantField || !strings.Contains(fe.Message, tc.want) {
					t.Fatalf("SaveRegistry() = %v, want a field error on %q containing %q", err, tc.wantField, tc.want)
				}
				return
			}
			if err != nil || got.Host != tc.want {
				t.Fatalf("SaveRegistry() = %+v, %v; want host %q", got, err, tc.want)
			}
			if _, err := e.store.GetRegistry(ctx, tc.want); err != nil {
				t.Errorf("registry %q isn't stored: %v", tc.want, err)
			}
		})
	}
}

// US2: the secret lives only in the Nomad Variable; a save without one keeps it,
// and changing the user name keeps it too.
func TestSaveRegistryKeepsTheSecret(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	rec, err := e.svc.SaveRegistry(ctx, "ghcr.io", "acme-bot", new(registryPassword))
	if err != nil || rec.Host != "ghcr.io" || rec.Username != "acme-bot" || rec.LastTest != nil || rec.CreatedAt.IsZero() {
		t.Fatalf("SaveRegistry() = %+v, %v", rec, err)
	}
	user, pass, ok, err := e.svc.deployer.RegistryCredentials(ctx, "ghcr.io")
	if user != "acme-bot" || pass != registryPassword || !ok || err != nil {
		t.Fatalf("stored credentials = %q, (password ok %v), %v, %v", user, pass == registryPassword, ok, err)
	}

	rec, err = e.svc.SaveRegistry(ctx, "ghcr.io", "other-bot", nil)
	if err != nil || rec.Username != "other-bot" {
		t.Fatalf("SaveRegistry() without a password = %+v, %v", rec, err)
	}
	if user, pass, _, _ := e.svc.deployer.RegistryCredentials(ctx, "ghcr.io"); user != "other-bot" || pass != registryPassword {
		t.Errorf("after a save without a password: user %q, password kept %v; want other-bot and the old password", user, pass == registryPassword)
	}

	list, err := e.svc.Registries(ctx)
	if err != nil || len(list) != 1 || list[0].Host != "ghcr.io" {
		t.Errorf("Registries() = %+v, %v", list, err)
	}
}

// FR-002: the password is in no SQLite file, which includes the write-ahead log.
func TestRegistrySecretIsNotInSQLite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg, nomad := registrytest.New(t), deploytest.New(t)
	svc := startService(t, st, reg, nomad, secretstest.New())

	if _, err := svc.SaveRegistry(ctx, "ghcr.io", "acme-bot", new(registryPassword)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TestRegistry(ctx, "ghcr.io"); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("data dir: %v, %v", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(registryPassword)) {
			t.Errorf("%s holds the password", f.Name())
		}
	}
	if vars := nomad.VariablePaths(); len(vars) != 1 || vars[0] != "caravana/registries/ghcr~io" {
		t.Errorf("variables = %v, want only the registry's", vars)
	}
}

// US2: Test records the outcome, refused or ok, and a change of credentials
// forgets it.
func TestTestRegistry(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.registry.RequireToken(true)
	e.registry.RequireLogin("deploy", registryPassword)
	host := e.registry.Host()

	if _, err := e.svc.TestRegistry(ctx, host); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TestRegistry() of a host with none = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.TestRegistry(ctx, "not a host"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TestRegistry() of an invalid host = %v, want ErrNotFound", err)
	}

	if _, err := e.svc.SaveRegistry(ctx, host, "deploy", new("wrong-"+registryPassword)); err != nil {
		t.Fatal(err)
	}
	rec, err := e.svc.TestRegistry(ctx, host)
	if err != nil {
		t.Fatalf("TestRegistry() with a wrong secret = %v; a refusal is an outcome, not an error", err)
	}
	if rec.LastTest == nil || rec.LastTest.OK || rec.LastTest.Message == "" || rec.LastTest.At.IsZero() {
		t.Fatalf("LastTest = %+v, want a refusal with its message", rec.LastTest)
	}
	if strings.Contains(rec.LastTest.Message, registryPassword) {
		t.Errorf("the recorded message %q holds the secret", rec.LastTest.Message)
	}

	// Saving the right secret forgets the refusal; testing records the success.
	rec, err = e.svc.SaveRegistry(ctx, host, "deploy", new(registryPassword))
	if err != nil || rec.LastTest != nil {
		t.Fatalf("SaveRegistry() with a new secret = %+v, %v; want the old test forgotten", rec, err)
	}
	rec, err = e.svc.TestRegistry(ctx, host)
	if err != nil || rec.LastTest == nil || !rec.LastTest.OK || rec.LastTest.Message != "" {
		t.Fatalf("TestRegistry() with the right secret = %+v, %v", rec.LastTest, err)
	}
	// A save that changes nothing keeps the test.
	if rec, err = e.svc.SaveRegistry(ctx, host, "deploy", nil); err != nil || rec.LastTest == nil || !rec.LastTest.OK {
		t.Errorf("SaveRegistry() with the same user and no password = %+v, %v; want the test kept", rec, err)
	}
}

// US2: removing forgets the secret and the entry; an unknown host is not found.
func TestDeleteRegistry(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	if err := e.svc.DeleteRegistry(ctx, "ghcr.io"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteRegistry() of an unknown host = %v, want ErrNotFound", err)
	}
	if err := e.svc.DeleteRegistry(ctx, "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteRegistry() of an invalid host = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.SaveRegistry(ctx, "ghcr.io", "acme-bot", new(registryPassword)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteRegistry(ctx, "GHCR.io"); err != nil {
		t.Fatalf("DeleteRegistry() = %v", err)
	}
	if _, err := e.store.GetRegistry(ctx, "ghcr.io"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the registry is still stored: %v", err)
	}
	if _, _, ok, _ := e.svc.deployer.RegistryCredentials(ctx, "ghcr.io"); ok {
		t.Error("the credentials are still in Nomad")
	}
}
