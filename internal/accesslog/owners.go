package accesslog

import "sort"

// Owner is the app and service a Traefik service name belongs to.
type Owner struct {
	Project, Environment string
	App                  string // the app ID
	Service              string // the service's name in the app
}

// OwnerApp is what OwnersOf needs of a stored app.
type OwnerApp struct {
	ID                   string
	Project, Environment string
	Services             []string // the names of its services
}

// Owners maps the Traefik service name of every service of every app
// ("<app ID>-<service>") to its owner. App IDs and service names both hold
// "-", so a name can't be split: it is looked up.
type Owners map[string]Owner

// OwnersOf builds the map. Two services whose joined names are equal (app "a"
// service "b-c", app "a-b" service "c") would have the same Traefik name in
// Nomad too, which Caravana refuses at deploy; if both were stored anyway the
// one whose app ID sorts first owns the name, so the answer never depends on
// the order of apps.
func OwnersOf(apps []OwnerApp) Owners {
	sorted := make([]OwnerApp, len(apps))
	copy(sorted, apps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	out := Owners{}
	for _, a := range sorted {
		for _, svc := range a.Services {
			name := a.ID + "-" + svc
			if _, taken := out[name]; taken {
				continue
			}
			out[name] = Owner{Project: a.Project, Environment: a.Environment, App: a.ID, Service: svc}
		}
	}
	return out
}

// Of is the owner of a Traefik service name (without "@nomad"). A name no
// stored app owns ("caravana@file", a deleted app's, "") has none: false.
func (o Owners) Of(serviceName string) (Owner, bool) {
	own, ok := o[serviceName]
	return own, ok
}
