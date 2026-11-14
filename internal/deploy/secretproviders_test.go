package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// providerSecret is a value no error may contain.
const providerSecret = "bax-SENTINEL-037-provider-credential"

func TestSecretProviderVariablePath(t *testing.T) {
	got := SecretProviderVariablePath("bella-prod")
	if got != "caravana/secret-providers/bella-prod" {
		t.Errorf("SecretProviderVariablePath() = %q", got)
	}
	if !nomadPath.MatchString(got) {
		t.Errorf("%q is not a legal Nomad variable path", got)
	}
}

func TestSecretProviderCredentials(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	const name = "bella-prod"
	creds := map[string]string{SecretProviderAPIKey: providerSecret, SecretProviderPrivateKey: "pem"}

	t.Run("a provider with none says so", func(t *testing.T) {
		got, ok, err := d.SecretProviderCredentials(ctx, name)
		if err != nil || ok || len(got) != 0 {
			t.Errorf("SecretProviderCredentials() before a save = %v, %v, %v; want none", got, ok, err)
		}
	})

	t.Run("save, read, replace", func(t *testing.T) {
		if err := d.SaveSecretProviderCredentials(ctx, name, creds); err != nil {
			t.Fatalf("SaveSecretProviderCredentials() = %v", err)
		}
		if got := nomad.VariablePaths(); len(got) != 1 || got[0] != "caravana/secret-providers/"+name {
			t.Errorf("variables = %v, want the one provider variable", got)
		}
		got, ok, err := d.SecretProviderCredentials(ctx, name)
		if err != nil || !ok || !reflect.DeepEqual(got, creds) {
			t.Errorf("SecretProviderCredentials() = %v, %v, %v", got, ok, err)
		}
		// A second save replaces the items, it does not merge them.
		if err := d.SaveSecretProviderCredentials(ctx, name, map[string]string{SecretProviderAPIKey: "bax-new"}); err != nil {
			t.Fatal(err)
		}
		got, _, _ = d.SecretProviderCredentials(ctx, name)
		if !reflect.DeepEqual(got, map[string]string{SecretProviderAPIKey: "bax-new"}) {
			t.Errorf("credentials after a second save = %v, want only the API key", got)
		}
	})

	t.Run("saving nothing deletes the variable", func(t *testing.T) {
		if err := d.SaveSecretProviderCredentials(ctx, name, map[string]string{SecretProviderAPIKey: ""}); err != nil {
			t.Fatal(err)
		}
		if got := nomad.VariablePaths(); len(got) != 0 {
			t.Errorf("variables = %v, want none", got)
		}
	})

	t.Run("delete, also of a provider with none", func(t *testing.T) {
		if err := d.SaveSecretProviderCredentials(ctx, name, creds); err != nil {
			t.Fatal(err)
		}
		if err := d.DeleteSecretProviderCredentials(ctx, name); err != nil {
			t.Fatalf("DeleteSecretProviderCredentials() = %v", err)
		}
		if _, ok, _ := d.SecretProviderCredentials(ctx, name); ok {
			t.Error("credentials found after a delete")
		}
		if err := d.DeleteSecretProviderCredentials(ctx, name); err != nil {
			t.Errorf("DeleteSecretProviderCredentials() of a provider with none = %v, want nil", err)
		}
	})

	t.Run("a name that is not a variable name is refused", func(t *testing.T) {
		for _, bad := range []string{"", "../registries/ghcr~io", "a/b", "A", "a b"} {
			if _, _, err := d.SecretProviderCredentials(ctx, bad); err == nil {
				t.Errorf("SecretProviderCredentials(%q) succeeded", bad)
			}
			if err := d.SaveSecretProviderCredentials(ctx, bad, creds); err == nil {
				t.Errorf("SaveSecretProviderCredentials(%q) succeeded", bad)
			}
			if err := d.DeleteSecretProviderCredentials(ctx, bad); err == nil {
				t.Errorf("DeleteSecretProviderCredentials(%q) succeeded", bad)
			}
		}
	})

	t.Run("a refused token is an error and names no value", func(t *testing.T) {
		nomad.FailVariables(403)
		defer nomad.FailVariables(0)
		if _, _, err := d.SecretProviderCredentials(ctx, name); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("SecretProviderCredentials() = %v, want ErrNomadUnavailable", err)
		}
		err := d.SaveSecretProviderCredentials(ctx, name, creds)
		if !errors.Is(err, ErrNomadUnavailable) {
			t.Fatalf("SaveSecretProviderCredentials() = %v, want ErrNomadUnavailable", err)
		}
		if strings.Contains(err.Error(), providerSecret) {
			t.Errorf("the error holds the credential: %v", err)
		}
	})
}
