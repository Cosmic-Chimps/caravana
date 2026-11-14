package deploy

import (
	"context"
	"fmt"
	"maps"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Secret providers (feature 037). Like the registries' passwords and the
// notification channels' secrets, a provider's credentials live in a Nomad
// Variable of Caravana's own (constitution VII), never in SQLite. A cold start
// of the cluster never waits on a provider: only deploys read from one.

// secretProviderVariablePrefix is where the credentials are kept, one variable
// per provider.
const secretProviderVariablePrefix = "caravana/secret-providers/"

// Items of a provider's variable. For Bella: the environment API key and the
// private key Bella showed when it made the key.
const (
	SecretProviderAPIKey     = "api_key"
	SecretProviderPrivateKey = "private_key"
)

// SecretProviderVariablePath is the Nomad Variable that holds the credentials
// of the provider with this name: "caravana/secret-providers/bella-prod".
func SecretProviderVariablePath(name string) string {
	return secretProviderVariablePrefix + name
}

// checkSecretProviderName refuses a name that could change which variable the
// path names. Names are lowercase labels (app.ValidName) checked at the door,
// so this guards against a bug rather than against input.
func checkSecretProviderName(name string) error {
	if err := app.ValidName("name", name); err != nil {
		return fmt.Errorf("secret provider name %q is not a valid variable name", name)
	}
	return nil
}

// SecretProviderCredentials returns the credentials stored for a provider. ok
// is false when there are none. Errors name the action, never the values.
func (d *Deployer) SecretProviderCredentials(ctx context.Context, name string) (items map[string]string, ok bool, err error) {
	if err := checkSecretProviderName(name); err != nil {
		return nil, false, err
	}
	items, found, err := d.readVariableIfAny(ctx, SecretProviderVariablePath(name))
	if err != nil {
		return nil, false, err // names the variable already
	}
	if !found || len(items) == 0 {
		return nil, false, nil
	}
	return items, true, nil
}

// SaveSecretProviderCredentials keeps items as the provider's credentials,
// replacing whatever was stored before. Items with an empty value are left
// out, and when none is left the variable is deleted instead, because Nomad
// refuses a variable whose items are all empty.
func (d *Deployer) SaveSecretProviderCredentials(ctx context.Context, name string, items map[string]string) error {
	if err := checkSecretProviderName(name); err != nil {
		return err
	}
	kept := maps.Clone(items)
	maps.DeleteFunc(kept, func(_, v string) bool { return v == "" })
	if len(kept) == 0 {
		return d.deleteVariable(ctx, SecretProviderVariablePath(name))
	}
	return d.writeVariable(ctx, SecretProviderVariablePath(name), kept)
}

// DeleteSecretProviderCredentials forgets a provider's credentials. A
// provider with none is not an error.
func (d *Deployer) DeleteSecretProviderCredentials(ctx context.Context, name string) error {
	if err := checkSecretProviderName(name); err != nil {
		return err
	}
	return d.deleteVariable(ctx, SecretProviderVariablePath(name))
}
