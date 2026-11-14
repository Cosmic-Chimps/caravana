package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// providerSentinel is a credential no answer, row or error may contain.
const providerSentinel = "bax-s3cret-sentinel-037-service"

func bellaInput(name string) SecretProviderInput {
	return SecretProviderInput{
		Name: name, Kind: store.SecretProviderBella,
		URL: "https://api.bella-baxter.io", Project: "shop", Environment: "production",
		APIKey: new(providerSentinel), PrivateKey: new("-----BEGIN PRIVATE KEY-----sentinel-----END PRIVATE KEY-----"),
	}
}

func TestSaveSecretProviderValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		change    func(*SecretProviderInput)
		wantField string
	}{
		{"fine", func(*SecretProviderInput) {}, ""},
		{"an uppercase name", func(in *SecretProviderInput) { in.Name = "Bella" }, "name"},
		{"an unknown kind", func(in *SecretProviderInput) { in.Kind = "vault" }, "kind"},
		{"no URL", func(in *SecretProviderInput) { in.URL = "" }, "bella.url"},
		{"a bad key", func(in *SecretProviderInput) { in.APIKey = new("bad") }, "apiKey"},
		{"no API key the first time", func(in *SecretProviderInput) { in.APIKey = nil }, "apiKey"},
		{"an empty API key", func(in *SecretProviderInput) { in.APIKey = new("") }, "apiKey"},
		{"no private key the first time", func(in *SecretProviderInput) { in.PrivateKey = nil }, "privateKey"},
		{"an empty private key", func(in *SecretProviderInput) { in.PrivateKey = new("") }, "privateKey"},
		{"a project place", func(in *SecretProviderInput) { in.Allowed = []string{"shop"} }, ""},
		{"an environment place", func(in *SecretProviderInput) { in.Allowed = []string{"shop/production"} }, ""},
		{"an unknown project", func(in *SecretProviderInput) { in.Allowed = []string{"shop", "nope"} }, "allowed[1]"},
		{"an unknown environment", func(in *SecretProviderInput) { in.Allowed = []string{"shop/qa"} }, "allowed[0]"},
		{"not a place", func(in *SecretProviderInput) { in.Allowed = []string{"a/b/c"} }, "allowed[0]"},
		{"an empty place", func(in *SecretProviderInput) { in.Allowed = []string{""} }, "allowed[0]"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := bellaInput(fmt.Sprintf("bella-v%d", i)) // its own provider: "the first time" needs no stored keys
			tc.change(&in)
			_, _, err := e.svc.SaveSecretProvider(ctx, in)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("SaveSecretProvider() = %v", err)
				}
				return
			}
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.wantField {
				t.Fatalf("SaveSecretProvider() = %v, want a field error on %q", err, tc.wantField)
			}
			for _, secret := range []string{providerSentinel, "sentinel-----END"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error holds a credential: %v", err)
				}
			}
		})
	}
}

// The credentials live only in the Nomad Variable: a save keeps them when the
// next one leaves them out, and nothing refused writes anything.
func TestSaveSecretProviderCredentials(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	bad := bellaInput("bella-prod")
	bad.APIKey = new("bad")
	if _, _, err := e.svc.SaveSecretProvider(ctx, bad); err == nil {
		t.Fatal("a refused key was saved")
	}
	if got := e.nomad.VariablePaths(); len(got) != 0 {
		t.Errorf("variables after a refused save = %v, want none", got)
	}
	if list, _ := e.store.ListSecretProviders(ctx); len(list) != 0 {
		t.Errorf("providers after a refused save = %v, want none", list)
	}

	info, created, err := e.svc.SaveSecretProvider(ctx, bellaInput("bella-prod"))
	if err != nil || !created || info.Name != "bella-prod" {
		t.Fatalf("SaveSecretProvider() = %+v, %v, %v", info, created, err)
	}
	creds, ok, err := e.svc.deployer.SecretProviderCredentials(ctx, "bella-prod")
	if err != nil || !ok || creds[deploy.SecretProviderAPIKey] != providerSentinel {
		t.Fatalf("credentials in Nomad = %v, %v, %v", creds, ok, err)
	}
	if got := e.sources.lastCreds("bella-prod"); got[deploy.SecretProviderAPIKey] != providerSentinel {
		t.Errorf("the source was built with %v, want the credentials", got)
	}

	// Leaving them out keeps them, and the source is built with the stored ones.
	again := bellaInput("bella-prod")
	again.APIKey, again.PrivateKey, again.Environment = nil, nil, "staging"
	info, created, err = e.svc.SaveSecretProvider(ctx, again)
	if err != nil || created || info.Environment != "staging" {
		t.Fatalf("second SaveSecretProvider() = %+v, %v, %v", info, created, err)
	}
	creds, _, _ = e.svc.deployer.SecretProviderCredentials(ctx, "bella-prod")
	if creds[deploy.SecretProviderAPIKey] != providerSentinel {
		t.Errorf("credentials after a save without them = %v, want the old ones", creds)
	}
	if got := e.sources.lastCreds("bella-prod"); got[deploy.SecretProviderAPIKey] != providerSentinel {
		t.Errorf("the source was built with %v, want the stored credentials", got)
	}

	// SQLite and the answer have no credential.
	rows, _ := e.store.ListSecretProviders(ctx)
	if got := strings.Join([]string{rows[0].Name, rows[0].URL, rows[0].Project, rows[0].Environment}, " "); strings.Contains(got, providerSentinel) {
		t.Errorf("a stored row holds a credential: %s", got)
	}
}

func TestSecretProviderTestAndClear(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, _, err := e.svc.SaveSecretProvider(ctx, bellaInput("bella-prod")); err != nil {
		t.Fatal(err)
	}
	src := e.sources.source("bella-prod")
	src.Set("A", "1")
	src.Set("B", "2")

	info, err := e.svc.TestSecretProvider(ctx, "bella-prod")
	if err != nil || info.LastTest == nil || !info.LastTest.OK || info.LastTest.Count != 2 || info.LastTest.Message != "2 secrets readable" {
		t.Fatalf("TestSecretProvider() = %+v, %v; want 2 secrets readable", info.LastTest, err)
	}
	src.Delete("B")
	if info, _ = e.svc.TestSecretProvider(ctx, "bella-prod"); info.LastTest.Message != "1 secret readable" {
		t.Errorf("message = %q, want the singular", info.LastTest.Message)
	}

	for _, tc := range []struct {
		name  string
		set   func()
		reset func()
		want  string
	}{
		{"refused", func() { src.SetRefused(true) }, func() { src.SetRefused(false) }, "refuses Caravana"},
		{"down", func() { src.SetDown(true) }, func() { src.SetDown(false) }, "switched off"},
	} {
		tc.set()
		info, err = e.svc.TestSecretProvider(ctx, "bella-prod")
		tc.reset()
		if err != nil {
			t.Fatalf("%s: a failed test is an outcome, got error %v", tc.name, err)
		}
		if info.LastTest.OK || !strings.Contains(info.LastTest.Message, tc.want) {
			t.Errorf("%s: LastTest = %+v, want a failure saying %q", tc.name, info.LastTest, tc.want)
		}
	}

	// A save that changes nothing about the connection keeps the test; one
	// that changes the project clears it; new credentials clear it too.
	keep := bellaInput("bella-prod")
	keep.APIKey, keep.PrivateKey, keep.Allowed = nil, nil, nil
	if got, _, err := e.svc.SaveSecretProvider(ctx, keep); err != nil || got.LastTest == nil {
		t.Errorf("save without a change: LastTest = %+v, %v; want it kept", got.LastTest, err)
	}
	keep.Project = "other"
	if got, _, err := e.svc.SaveSecretProvider(ctx, keep); err != nil || got.LastTest != nil {
		t.Errorf("save with another project: LastTest = %+v, %v; want it cleared", got.LastTest, err)
	}
	e.svc.TestSecretProvider(ctx, "bella-prod")
	renew := keep
	renew.APIKey = new("bax-new")
	if got, _, err := e.svc.SaveSecretProvider(ctx, renew); err != nil || got.LastTest != nil {
		t.Errorf("save with a new key: LastTest = %+v, %v; want it cleared", got.LastTest, err)
	}

	if _, err := e.svc.TestSecretProvider(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("TestSecretProvider(unknown) = %v, want ErrNotFound", err)
	}
}

func TestDeleteSecretProvider(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, _, err := e.svc.SaveSecretProvider(ctx, bellaInput("bella-prod")); err != nil {
		t.Fatal(err)
	}
	// Two apps reference it, one doesn't.
	for _, id := range []string{"web", "api"} {
		a := whoami()
		a.ID = id
		a.Services[0].Secrets = []app.Secret{{Name: "K", From: &app.SecretRef{Provider: "bella-prod", Path: "K"}}}
		if _, err := e.store.InsertApp(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
	}
	other := whoami()
	other.ID = "other"
	if _, err := e.store.InsertApp(ctx, other, nil); err != nil {
		t.Fatal(err)
	}

	list, err := e.svc.SecretProviders(ctx)
	if err != nil || len(list) != 1 || strings.Join(list[0].UsedBy, ",") != "api,web" {
		t.Fatalf("SecretProviders() = %+v, %v; want usedBy api,web", list, err)
	}

	err = e.svc.DeleteSecretProvider(ctx, "bella-prod")
	var inUse *ProviderInUseError
	if !errors.As(err, &inUse) || !errors.Is(err, ErrSecretProviderInUse) || strings.Join(inUse.Apps, ",") != "api,web" {
		t.Fatalf("DeleteSecretProvider() = %v, want a ProviderInUseError naming api and web", err)
	}
	if !strings.Contains(err.Error(), "api, web") {
		t.Errorf("the message %q doesn't name the apps", err)
	}
	if _, ok, _ := e.svc.deployer.SecretProviderCredentials(ctx, "bella-prod"); !ok {
		t.Error("a refused delete removed the credentials")
	}

	for _, id := range []string{"web", "api"} {
		if err := e.store.DeleteApp(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.DeleteSecretProvider(ctx, "bella-prod"); err != nil {
		t.Fatalf("DeleteSecretProvider() = %v", err)
	}
	if got := e.nomad.VariablePaths(); len(got) != 0 {
		t.Errorf("variables after the delete = %v, want none", got)
	}
	if err := e.svc.DeleteSecretProvider(ctx, "bella-prod"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}
