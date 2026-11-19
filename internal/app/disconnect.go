package app

import "time"

// Limits of the disconnect policy (network blips).
const (
	// DefaultLostAfter is how long, in seconds, Nomad keeps the copies of a
	// server it can't reach before it calls them lost, for a service that sets
	// no disconnect policy of its own.
	DefaultLostAfter = 300
	// MinLostAfter and MaxLostAfter bound a set value, in seconds.
	MinLostAfter = 60
	MaxLostAfter = 86400
)

// Disconnect is what happens to a service's copies when the server they run
// on stops answering. Without it a server cut off for 45 seconds is called
// down, its copies are replaced elsewhere and, when it comes back, they are
// stopped: a short blip costs a full replacement. With it (and every service
// gets the default), the copies keep running until LostAfter and only then
// count as lost. nil means the default.
type Disconnect struct {
	// LostAfterSeconds is how long the copies of an unreachable server are
	// kept before they are called lost, 60 to 86400. 0 is not allowed in a
	// stored policy: leave the whole object out for DefaultLostAfter.
	LostAfterSeconds int `json:"lostAfterSeconds,omitzero"`
}

// LostAfter is how long the service's copies are kept on an unreachable
// server: the service's own setting, or DefaultLostAfter.
func (svc Service) LostAfter() time.Duration {
	if svc.Disconnect == nil || svc.Disconnect.LostAfterSeconds == 0 {
		return DefaultLostAfter * time.Second
	}
	return time.Duration(svc.Disconnect.LostAfterSeconds) * time.Second
}

// ProxyChecks reports whether the proxy can tell that a copy of the service is
// unreachable: the service has domains and an HTTP health check on the
// domains' port. Traefik then checks that path on each copy and stops sending
// to one that doesn't answer. Nomad keeps the registration of a copy on a
// disconnected server until the copy is lost, so a service whose copies the
// proxy can't check would be sent traffic that hangs; such a service gets no
// disconnect policy, and an explicit one is refused. A service with no
// domains has no proxy, so it is always fine.
func (svc Service) ProxyChecks() bool {
	if len(svc.Domains) == 0 || svc.Health == nil || svc.Health.Path == "" {
		return false
	}
	return svc.Health.Port == "" || svc.Health.Port == svc.Domains[0].Port
}

// KeepsCopiesOnDisconnect reports whether the service's copies are kept when
// their server is unreachable: every service is, except one with domains the
// proxy can't check (see ProxyChecks).
func (svc Service) KeepsCopiesOnDisconnect() bool {
	return len(svc.Domains) == 0 || svc.ProxyChecks()
}

// validateDisconnect checks a service's disconnect policy. field is the
// service's path, e.g. "services[0]". It runs after the domains and the health
// check were checked, since it reads both.
func validateDisconnect(field string, svc Service) error {
	d := svc.Disconnect
	if d == nil {
		return nil
	}
	switch {
	case d.LostAfterSeconds == 0:
		return NewFieldError(field+".disconnect.lostAfterSeconds", "is required: set %d to %d seconds, or leave disconnect out for %d", MinLostAfter, MaxLostAfter, DefaultLostAfter)
	case d.LostAfterSeconds < MinLostAfter || d.LostAfterSeconds > MaxLostAfter:
		return NewFieldError(field+".disconnect.lostAfterSeconds", "is %d; it must be %d to %d seconds (leave disconnect out for %d)", d.LostAfterSeconds, MinLostAfter, MaxLostAfter, DefaultLostAfter)
	case !svc.KeepsCopiesOnDisconnect():
		return NewFieldError(field+".disconnect", "needs an HTTP health check (health.path) on the domains' port: the proxy can only stop sending traffic to an unreachable copy by checking that path")
	}
	return nil
}
