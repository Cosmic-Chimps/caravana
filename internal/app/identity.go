package app

import (
	"fmt"
	"regexp"
	"strings"
)

// Limits of a service's workload identities (spec 038 FR-002).
const (
	// MaxIdentities is how many identities one service may ask for.
	MaxIdentities = 4
	// MaxIdentityAudiences is how many audience values one identity may have.
	MaxIdentityAudiences = 4
	// MaxIdentityAudienceBytes is the longest audience value.
	MaxIdentityAudienceBytes = 255
	// MinIdentityTTL and MaxIdentityTTL bound an identity's lifetime, in seconds.
	MinIdentityTTL = 60
	MaxIdentityTTL = 86400
	// DefaultIdentityTTL is the lifetime, in seconds, of an identity that
	// sets none.
	DefaultIdentityTTL = 3600
)

// identityNameRE: the name ends up in the file nomad_<name>.jwt and in the
// variable NOMAD_TOKEN_<name>, so letters, digits and "_" only.
var identityNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,29}$`)

// Identity is a Nomad workload identity a service asks for: a signed,
// short-lived JWT that Nomad writes to /secrets/nomad_<Name>.jwt in every copy
// and renews before it expires. Caravana never reads the token.
type Identity struct {
	// Name is the identity's name, e.g. "bella". It names the token's file.
	Name string `json:"name"`
	// Audience is the JWT's aud claim. At least one value.
	Audience []string `json:"audience"`
	// TTLSeconds is the token's lifetime in seconds, 60 to 86400. 0 means
	// DefaultIdentityTTL.
	TTLSeconds int `json:"ttlSeconds,omitzero"`
}

// TTLOrDefault is the identity's lifetime in seconds.
func (i Identity) TTLOrDefault() int {
	if i.TTLSeconds == 0 {
		return DefaultIdentityTTL
	}
	return i.TTLSeconds
}

// validateIdentities checks a service's identities (FR-002). field is the
// service's path, e.g. "services[0]".
func validateIdentities(field string, identities []Identity) error {
	if len(identities) > MaxIdentities {
		return NewFieldError(field+".identities", "has %d identities; at most %d are allowed", len(identities), MaxIdentities)
	}
	seen := map[string]bool{}
	for j, id := range identities {
		f := fmt.Sprintf("%s.identities[%d]", field, j)
		switch {
		case !identityNameRE.MatchString(id.Name):
			return NewFieldError(f+".name", "%q must start with a lowercase letter and have at most 30 lowercase letters, digits and \"_\"", id.Name)
		case id.Name == "default":
			return NewFieldError(f+".name", "%q is Nomad's own identity; choose another name", id.Name)
		case strings.HasPrefix(id.Name, "vault_") || strings.HasPrefix(id.Name, "consul_"):
			return NewFieldError(f+".name", "%q must not start with vault_ or consul_, which Nomad's integrations use", id.Name)
		case seen[id.Name]:
			return NewFieldError(f+".name", "identity %q is declared twice", id.Name)
		}
		seen[id.Name] = true

		if len(id.Audience) == 0 {
			return NewFieldError(f+".audience", "needs at least one value")
		}
		if len(id.Audience) > MaxIdentityAudiences {
			return NewFieldError(f+".audience", "has %d values; at most %d are allowed", len(id.Audience), MaxIdentityAudiences)
		}
		for k, aud := range id.Audience {
			if !validAudience(aud) {
				return NewFieldError(f+".audience", "value %d must be 1 to %d printable ASCII characters without spaces", k+1, MaxIdentityAudienceBytes)
			}
		}

		if id.TTLSeconds != 0 && (id.TTLSeconds < MinIdentityTTL || id.TTLSeconds > MaxIdentityTTL) {
			return NewFieldError(f+".ttlSeconds", "is %d; it must be %d to %d seconds (leave it out for %d)", id.TTLSeconds, MinIdentityTTL, MaxIdentityTTL, DefaultIdentityTTL)
		}
	}
	return nil
}

// validAudience reports whether s is 1 to 255 printable ASCII bytes with no
// space.
func validAudience(s string) bool {
	if s == "" || len(s) > MaxIdentityAudienceBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}
