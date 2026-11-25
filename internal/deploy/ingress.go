package deploy

import (
	"context"
	"errors"
)

// certificatesMeta is the job meta of Traefik's job that says it renders the ACME issuer and the
// uploaded certificates from Nomad Variables (feature 034, contracts/traefik.md). A job the
// installer made before the feature doesn't have it.
const certificatesMeta = "caravana_certificates"

// IngressFeatures is what Traefik's job, as installed, can do beyond routing.
type IngressFeatures struct {
	// Certificates: the job reads the variables caravana/acme and caravana/certificates/*, so the
	// issuer can be set and certificates uploaded from the console.
	Certificates bool
}

// IngressFeatures reads Traefik's job. A host with no job named traefik has no features, and that
// is not an error: the caller says what to run. Any other failure to read it is one.
func (d *Deployer) IngressFeatures(ctx context.Context) (IngressFeatures, error) {
	job, err := d.proxyJob(ctx)
	if errors.Is(err, ErrNoProxy) {
		return IngressFeatures{}, nil
	}
	if err != nil {
		return IngressFeatures{}, err
	}
	return IngressFeatures{Certificates: job.Meta[certificatesMeta] == "1"}, nil
}
