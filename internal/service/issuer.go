package service

import (
	"time"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// issuerHandoff is how long a Nomad server that was given a new issuer holds back the others. Nomad
// servers take the issuer one at a time, so the cluster keeps a quorum while each one restarts
// (spec 038, plan D9).
const issuerHandoff = 2 * time.Minute

// ClusterIssuer is the issuer of the workload identity tokens of this installation: the console's
// public URL when it is https://host or https://host:port with no path, query, fragment or user
// information, and "" otherwise (dev builds and the lab have no https address, so no issuer).
func (s *Service) ClusterIssuer() string {
	return clusterIssuerOf(s.PublicURL())
}

// clusterIssuerOf is ClusterIssuer's rule on a URL: the rule the node agent checks an answer with
// (mesh.CleanIssuer), so no answer is one the agent would refuse. A URL that isn't one gives "".
func clusterIssuerOf(raw string) string {
	issuer, ok := mesh.CleanIssuer(raw)
	if !ok {
		return ""
	}
	return issuer
}

// issuerFor decides which issuer a server's sync answer carries. server says the server is a Nomad
// server (a client only stores the issuer: it is a setting of Nomad servers), reported is what its
// agent says it last rendered, current is ClusterIssuer, and inFlight holds the Nomad servers that
// were handed a new issuer and when. start says this answer hands the server the new issuer, so the
// caller records it in inFlight. The function changes nothing: it is the one-at-a-time rule alone.
func issuerFor(name string, server bool, reported, current string, inFlight map[string]time.Time, now time.Time) (send string, start bool) {
	if !server || reported == current {
		return current, false
	}
	if _, ok := inFlight[name]; ok {
		return current, false
	}
	for other, since := range inFlight {
		if other != name && now.Sub(since) < issuerHandoff {
			return reported, false // another server is restarting with it: this one keeps what it has
		}
	}
	return current, true
}

// noteIssuer applies issuerFor for one sync and keeps inFlight. The caller holds serverMu.
func (s *Service) noteIssuer(name string, server bool, reported string, now time.Time) string {
	current := s.ClusterIssuer()
	if server && reported == current {
		delete(s.issuerInFlight, name)
	}
	send, start := issuerFor(name, server, reported, current, s.issuerInFlight, now)
	if start {
		if s.issuerInFlight == nil {
			s.issuerInFlight = map[string]time.Time{}
		}
		s.issuerInFlight[name] = now
	}
	return send
}
