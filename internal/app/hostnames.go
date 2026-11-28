package app

import "sort"

// Names across apps (task 2xg2). Apps of one project and environment reach
// each other's services by name, the way the services of one app already do
// (009). Nomad without Consul has no DNS, so every copy's names sidecar writes
// them into its /etc/hosts. This file is the one rule for which name points at
// what; nomadjob writes it into the job (tags and the sidecar's template) and
// the console reads it to say where a service answers.
//
//   - Inside an app, each service with ports answers to its bare name, as in
//     Docker Compose. That stays the app's own business: other apps never
//     see it.
//   - Across the place, each service with ports claims "<app>-<service>",
//     and an app with one service that has ports also claims "<app>", so a
//     Postgres installed as "postgres" is reached at "postgres".
//   - A name two services of the place claim resolves to neither of them, for
//     every app, so a new app can't quietly take a name another app uses.
//   - In an app's own copies its bare names win: another app's claim on one of
//     them is left out there.

// PlaceTagPrefix and NameTagPrefix start the tags of a service's internal
// registration that the names sidecar reads: the registration's place, and
// each name it claims across the place.
const (
	PlaceTagPrefix = "caravana-place="
	NameTagPrefix  = "caravana-name="
)

// HostAppName is the name an app goes by in its place: the name part of its
// ID. An old ID that PlaceOf refuses (it had "--" before feature 019) lives in
// Default › production under its whole ID, which is then its name.
func HostAppName(id string) string {
	if p, err := PlaceOf(id); err == nil {
		return p.Name
	}
	return id
}

// HostPlace is "<project>/<environment>" for an app ID, the value of its
// place tag.
func HostPlace(id string) string {
	p, err := PlaceOf(id)
	if err != nil {
		p = Place{Project: DefaultProject, Environment: DefaultEnvironment}
	}
	return p.Project + "/" + p.Environment
}

// servicesWithPorts counts the services that answer on a port: only they have
// a name worth resolving.
func (a App) servicesWithPorts() int {
	n := 0
	for _, svc := range a.Services {
		if len(svc.Ports) > 0 {
			n++
		}
	}
	return n
}

// ClaimedNames are the names a service claims across its place, most
// specific first: "<app>-<service>", then "<app>" when it is the app's only
// service with ports. A service without ports claims nothing.
func (a App) ClaimedNames(svc Service) []string {
	if len(svc.Ports) == 0 {
		return nil
	}
	name := HostAppName(a.ID)
	names := []string{name + "-" + svc.Name}
	if a.servicesWithPorts() == 1 {
		names = append(names, name)
	}
	return names
}

// Address is where a service of an app answers by name.
type Address struct {
	// Service is the service's name; Ports are its container ports, the
	// first being the one its registration names.
	Service string
	Ports   []int
	// Names are what the other apps of the place reach it at: the names it
	// claims that no other service claims too. Empty when every claim
	// collides.
	Names []string
	// Own is the name the app's other services reach it at: its bare name,
	// set only in an app with more than one service.
	Own string
	// Conflicts are the claims another service of the place makes too, which
	// resolve to neither.
	Conflicts []NameConflict
}

// NameConflict is a name two services claim: Name, and the other app and
// service that claim it.
type NameConflict struct {
	Name    string
	App     string // the other app's ID
	Service string
}

// Addresses says where each service with ports of a answers, given the other
// apps of its place (any app of others with a's ID is skipped, as is any app
// that isn't in a's place). The caller decides which apps claim names: the
// console passes the stored apps that are defined and not stopped, which is
// what will be registered.
func Addresses(a App, others []App) []Address {
	place := HostPlace(a.ID)
	// claimants maps each name to the (app, service) pairs other than a's own
	// that claim it.
	type claimant struct{ app, service string }
	claimants := map[string][]claimant{}
	for _, o := range others {
		if o.ID == a.ID || HostPlace(o.ID) != place {
			continue
		}
		for _, svc := range o.Services {
			for _, n := range o.ClaimedNames(svc) {
				claimants[n] = append(claimants[n], claimant{o.ID, svc.Name})
			}
		}
	}
	// An app's own claims never collide with each other: "<app>-<service>"
	// differs per service, and "<app>" is claimed only by a lone service.
	var out []Address
	for _, svc := range a.Services {
		if len(svc.Ports) == 0 {
			continue
		}
		addr := Address{Service: svc.Name}
		for _, p := range svc.Ports {
			addr.Ports = append(addr.Ports, p.ContainerPort)
		}
		if len(a.Services) > 1 {
			addr.Own = svc.Name
		}
		for _, n := range a.ClaimedNames(svc) {
			taken := false
			for _, c := range claimants[n] {
				addr.Conflicts = append(addr.Conflicts, NameConflict{Name: n, App: c.app, Service: c.service})
				taken = true
			}
			if !taken {
				addr.Names = append(addr.Names, n)
			}
		}
		sort.SliceStable(addr.Conflicts, func(i, j int) bool {
			if addr.Conflicts[i].Name != addr.Conflicts[j].Name {
				return addr.Conflicts[i].Name < addr.Conflicts[j].Name
			}
			return addr.Conflicts[i].App < addr.Conflicts[j].App
		})
		out = append(out, addr)
	}
	return out
}
