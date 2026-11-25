package deploy

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// Uploaded certificates (feature 034). Each is a Nomad Variable that Traefik's job lists and renders
// into the file provider's certificates.yml, in the task's secrets directory, with no restart.

// CertificatesVariablePrefix is where the uploaded certificates live, one variable per certificate.
const CertificatesVariablePrefix = "caravana/certificates/"

// certificateIDRE is what an ID is: "crt_" and 12 lowercase hex digits. It is checked before an ID
// becomes part of a variable path.
var certificateIDRE = regexp.MustCompile(`^crt_[0-9a-f]{12}$`)

// SaveCertificate stores an uploaded certificate: its chain (leaf first, no root), its private key
// and its names, which the installer reads to clean the ACME store. The service has checked all
// three; the error names the action and never holds the key.
func (d *Deployer) SaveCertificate(ctx context.Context, id, chainPEM, keyPEM string, names []string) error {
	path, err := certificatePath(id)
	if err != nil {
		return err
	}
	if chainPEM == "" || keyPEM == "" {
		return errors.New("save the certificate: the chain and the key are both required")
	}
	return d.writeVariable(ctx, path, map[string]string{
		"certificate": chainPEM,
		"key":         keyPEM,
		"names":       strings.Join(names, ","),
	})
}

// DeleteCertificate removes an uploaded certificate. One that isn't there is not an error.
func (d *Deployer) DeleteCertificate(ctx context.Context, id string) error {
	path, err := certificatePath(id)
	if err != nil {
		return err
	}
	return d.deleteVariable(ctx, path)
}

func certificatePath(id string) (string, error) {
	if !certificateIDRE.MatchString(id) {
		return "", errors.New("the certificate ID must be crt_ and 12 lowercase hex digits")
	}
	return CertificatesVariablePrefix + id, nil
}
