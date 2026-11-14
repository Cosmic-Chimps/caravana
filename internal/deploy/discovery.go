package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hashicorp/nomad/api"
)

// The two documents Nomad serves outside /v1 once it has an issuer (spec 038): the OpenID
// configuration, whose `issuer` is Nomad's `oidc_issuer`, and the keys that verify workload identity
// tokens. Caravana serves both on the console's domain, so a verifier such as Bella can fetch them.
const (
	DiscoveryConfigPath = "/.well-known/openid-configuration"
	DiscoveryJWKSPath   = "/.well-known/jwks.json"
)

const (
	// discoveryTimeout is how long reading one document may take.
	discoveryTimeout = 5 * time.Second
	// discoveryMaxBytes is the largest document that is passed on.
	discoveryMaxBytes = 64 << 10
)

// ErrDiscoveryInvalid is returned when Nomad's answer is larger than 64 KiB or isn't JSON.
var ErrDiscoveryInvalid = errors.New("nomad's discovery document is not usable")

// NomadDiscovery reads one of Nomad's discovery documents (DiscoveryConfigPath or DiscoveryJWKSPath)
// from the Nomad Caravana is configured with, and returns the body as Nomad sent it. It waits 5
// seconds at most, refuses a body over 64 KiB or one that isn't valid JSON (ErrDiscoveryInvalid), and
// never puts Nomad's body in an error: every failure is ErrNomadUnavailable or ErrDiscoveryInvalid
// with the path and, for an HTTP refusal, the status code.
func (d *Deployer) NomadDiscovery(ctx context.Context, path string) ([]byte, error) {
	if path != DiscoveryConfigPath && path != DiscoveryJWKSPath {
		return nil, fmt.Errorf("read %s: not a discovery path", path)
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	body, err := d.nomad.Raw().Response(path, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if status := statusOf(err); status != 0 {
			return nil, fmt.Errorf("%w: read %s: Nomad answered %d", ErrNomadUnavailable, path, status)
		}
		return nil, fmt.Errorf("%w: read %s: Nomad is unreachable", ErrNomadUnavailable, path)
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, discoveryMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: the answer couldn't be read", ErrNomadUnavailable, path)
	}
	if len(b) > discoveryMaxBytes {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrDiscoveryInvalid, path, discoveryMaxBytes)
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("%w: %s isn't JSON", ErrDiscoveryInvalid, path)
	}
	return b, nil
}
