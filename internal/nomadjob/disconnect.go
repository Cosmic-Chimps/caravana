package nomadjob

import (
	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// What happens to a service's copies when their server stops answering (the
// disconnect block, "network blips").
//
// Nomad calls a server down about 45 seconds after its last heartbeat, and by
// default replaces its copies at once and stops the old ones when it
// returns. With lost_after set, the server is "disconnected" instead: its
// copies stay "unknown", still running, until lost_after has passed, and a
// short cut costs nothing.
//
//   - replace: a stateless service gets replacements meanwhile, so it keeps its
//     count. A service whose copy can't start elsewhere (it has a volume or
//     publishes a port, see stopThenStart) gets none: it waits for its server.
//   - reconcile keep_original: when the server comes back, the original copies
//     stay and the replacements stop, the same every time. Nomad's default
//     picks by score.
//   - a service the proxy can't check (app.Service.KeepsCopiesOnDisconnect) has
//     no block. Nomad keeps the registration of an unknown copy, so Traefik
//     would send traffic to the unreachable copy for the whole lost_after (a
//     dial over a cut link hangs for Traefik's 30 s dial timeout); a routed
//     service with an HTTP check gets the Traefik health check of
//     healthCheckTags instead, which drops that copy within an interval.
//
// Only an app's own task groups have it, not the backup, restore and schedule
// run jobs, which end on their own.
func disconnectStrategy(svc app.Service) *api.DisconnectStrategy {
	if !svc.KeepsCopiesOnDisconnect() {
		return nil
	}
	return &api.DisconnectStrategy{
		LostAfter: new(svc.LostAfter()),
		Replace:   new(!stopThenStart(svc)),
		Reconcile: new(api.ReconcileOptionKeepOriginal),
	}
}

// healthCheckTags is the Traefik health check of a routed service: Traefik
// calls the check's path on every copy of the registration at the check's
// interval and stops sending to a copy that doesn't answer within its timeout.
// It is what takes the unreachable copy of a disconnected server out of the
// load balancer, since Nomad keeps its registration (see disconnectStrategy).
// It has the values of the Nomad check, so the two agree. The Traefik service
// is named after the Nomad service, name. There are no tags for a service the
// proxy can't check.
func healthCheckTags(name string, svc app.Service) []string {
	if !svc.ProxyChecks() {
		return nil
	}
	h := svc.Health
	prefix := "traefik.http.services." + name + ".loadbalancer.healthcheck."
	return []string{
		prefix + "path=" + h.Path,
		prefix + "interval=" + orDefault(h.Interval, defaultCheckInterval).String(),
		prefix + "timeout=" + orDefault(h.Timeout, defaultCheckTimeout).String(),
	}
}
