package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// The ACME issuer of the installation (feature 034). Caravana writes it to a Nomad Variable that
// Traefik's job renders into its static configuration with change_mode = "restart", so a change
// restarts Traefik's task in place and needs no root and no installer run. There is no variable for
// the installer's own issuer. The EAB HMAC key is in it and nowhere else (constitution VII).

// ACMEVariablePath is the Nomad Variable that holds the custom issuer.
const ACMEVariablePath = "caravana/acme"

// ACMEIssuer is what Traefik's job reads from ACMEVariablePath. Empty strings mean "not set".
type ACMEIssuer struct {
	Server     string // the ACME directory URL
	Email      string
	EABKeyID   string
	EABHMACKey string // base64url without padding; a secret
	CA         string // PEM of the CA that signs the issuer's own HTTPS certificate
}

// ACMEStorage is the name of the file in Traefik's store that belongs to an issuer: one file per
// issuer, so a change of issuer starts a new account and going back finds the old one. The
// installer's own issuer keeps acme.json.
func ACMEStorage(server, eabKeyID string) string {
	sum := sha256.Sum256([]byte(server + "\n" + eabKeyID))
	return "acme-" + hex.EncodeToString(sum[:])[:12] + ".json"
}

// SaveACMEIssuer writes the variable. All six items are always written (an item that is absent
// would fail Traefik's template), the empty ones as empty strings; "storage" is never empty, so
// the variable is never one Nomad would refuse. The error names the action and never holds the key.
func (d *Deployer) SaveACMEIssuer(ctx context.Context, i ACMEIssuer) error {
	if i.Server == "" {
		return errors.New("save the ACME issuer: the server is empty")
	}
	return d.writeVariable(ctx, ACMEVariablePath, map[string]string{
		"server":   i.Server,
		"email":    i.Email,
		"eab_kid":  i.EABKeyID,
		"eab_hmac": i.EABHMACKey,
		"ca":       i.CA,
		"storage":  ACMEStorage(i.Server, i.EABKeyID),
	})
}

// ACMEIssuerSecret returns the stored EAB HMAC key. found is false when there is no variable or it
// holds no key.
func (d *Deployer) ACMEIssuerSecret(ctx context.Context) (hmac string, found bool, err error) {
	items, ok, err := d.readVariableIfAny(ctx, ACMEVariablePath)
	if err != nil || !ok {
		return "", false, err
	}
	hmac = items["eab_hmac"]
	return hmac, hmac != "", nil
}

// DeleteACMEIssuer removes the variable: Traefik goes back to the installer's issuer. A variable
// that isn't there is not an error.
func (d *Deployer) DeleteACMEIssuer(ctx context.Context) error {
	return d.deleteVariable(ctx, ACMEVariablePath)
}
