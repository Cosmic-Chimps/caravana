package nomadjob

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Services by name (research R3, task 2xg2). Each service reaches the other
// services of its app by their bare names, as in Docker Compose, and the
// services of the other apps of its project and environment by the names
// app.ClaimedNames gives them ("<app>-<service>", and "<app>" for an app with
// one service that has ports). Nomad without Consul has no DNS for that, so
// every group of every app gets a small sidecar task that keeps the group's
// shared /etc/hosts pointing each name at that service's live allocations.
// The rule itself is in internal/app (hostnames.go); this file writes it into
// the job.

// namesTaskName is the sidecar's task name. It must not be "hosts": that
// task's directory would collide with the allocation's hosts file. In the
// group of a service that is itself called "names", the sidecar is
// namesTaskAltName instead (namesTaskFor).
const (
	namesTaskName    = "names"
	namesTaskAltName = "caravana-names"
)

// maxServiceNameLen is the longest service name Nomad allows.
const maxServiceNameLen = 63

// Resources and stop time of the names sidecar. It only sleeps and copies a
// small file now and then.
const (
	namesCPUMHz      = 20
	namesMemoryMB    = 16
	namesKillTimeout = time.Second
)

// namesScript is the sidecar's command. It saves the allocation's own hosts
// file once (a restarted task finds it already saved), writes that plus the
// rendered names over /etc/hosts, then waits until Nomad stops it. "cat >"
// writes the file in place, which keeps Docker's bind mount of /etc/hosts
// working. Nomad interpolates "${...}" in task config, so the script avoids it.
const namesScript = `[ -f /local/base ] || cp /etc/hosts /local/base
cat /local/base /local/names > /etc/hosts
trap 'exit 0' TERM INT
while :; do sleep 3600 & wait $!; done
`

// namesRewrite is what the template runs when the names change: the same
// write as at start, without saving the base again.
const namesRewrite = "cat /local/base /local/names > /etc/hosts"

// namesTaskFor is the sidecar's task name in a service's group: "names",
// unless the service's own task already has that name.
func namesTaskFor(service string) string {
	if service == namesTaskName {
		return namesTaskAltName
	}
	return namesTaskName
}

// internalName is the registration other services find a service by:
// "<app>-<service>-internal". A name Nomad wouldn't take (over 63 characters,
// possible since every app has one, not only those with two services) keeps
// as much of it as fits and ends with a hash of the whole, so it stays unique.
// Names that fit are unchanged, so no registration made before moved.
func internalName(appID, service string) string {
	name := appID + "-" + service + "-internal"
	if len(name) <= maxServiceNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:4]) + "-internal"
	return name[:maxServiceNameLen-len(suffix)] + suffix
}

// internalTags are the tags of a service's internal registration that the
// names sidecars of its place read: the place, and each name the service
// claims there.
func internalTags(a app.App, svc app.Service) []string {
	tags := []string{app.PlaceTagPrefix + app.HostPlace(a.ID)}
	for _, n := range a.ClaimedNames(svc) {
		tags = append(tags, app.NameTagPrefix+n)
	}
	return tags
}

// internalRegistration registers a service's first port under its internal
// name, with the allocation's own address ("alloc" mode: the bridge IP and the
// container port), so other services of the app can reach it directly. It has
// no check, since the main registration's check already gates deploys, and its
// canaries are tagged so the names template can leave them out. Its tags say
// its place and the names it claims there; a canary keeps them, with the
// canary tag added (a service's canary tags replace its tags).
func internalRegistration(a app.App, svc app.Service) *api.Service {
	tags := internalTags(a, svc)
	return &api.Service{
		Name:        internalName(a.ID, svc.Name),
		PortLabel:   svc.Ports[0].Name,
		Provider:    "nomad",
		AddressMode: "alloc",
		Tags:        tags,
		CanaryTags:  append(slices.Clone(tags), canaryTag),
	}
}

// namesTask is the sidecar that keeps /etc/hosts up to date. It starts before
// the service's own task (prestart) and runs beside it (sidecar).
func namesTask(a app.App, service string, s Settings) *api.Task {
	return &api.Task{
		Name:      namesTaskFor(service),
		Driver:    "docker",
		Lifecycle: &api.TaskLifecycle{Hook: "prestart", Sidecar: true},
		Config: map[string]any{
			"image":   s.namesImage(),
			"command": "/bin/sh",
			"args":    []string{"-c", namesScript},
		},
		Resources: &api.Resources{
			CPU:      new(namesCPUMHz),
			MemoryMB: new(namesMemoryMB),
		},
		KillTimeout: new(namesKillTimeout),
		Templates: []*api.Template{{
			EmbeddedTmpl: new(namesTemplate(a)),
			DestPath:     new("local/names"),
			ChangeMode:   new("script"),
			ChangeScript: &api.ChangeScript{
				Command:     new("/bin/sh"),
				Args:        []string{"-c", namesRewrite},
				Timeout:     new(5 * time.Second),
				FailOnError: new(false),
			},
		}},
	}
}

// namesTemplate renders the group's names, one "<address> <name>" line per
// live allocation. Canaries are left out, so a name keeps pointing at the old
// version until the new one is promoted. A service that isn't registered yet
// renders nothing and simply doesn't resolve. It reads Nomad's live list of
// registrations, so another app deployed, moved or deleted shows up here
// without this app being deployed again, and Translate stays pure.
//
// First the app's own services, by their bare names, in the app's order. The
// list includes the group's own service: Compose resolves a service's own
// name too, and some images (a database or broker that names itself in its
// config) depend on it. During a blue/green rollout, a canary's own name
// still points at the old version until promotion, like every other name.
//
// Then the names the services of the place claim (lab-checked, task 2xg2).
// The template makes two passes over the place's registrations: the first
// marks each name with the registration that claims it, or with "" when a
// second one claims it too (scratch is reset on every render); the second
// writes each name whose mark is its own registration, except the app's own
// bare names, which win here.
func namesTemplate(a app.App) string {
	var b strings.Builder
	var own []string
	for _, svc := range a.Services {
		if len(svc.Ports) == 0 {
			continue
		}
		own = append(own, svc.Name)
		fmt.Fprintf(&b, `{{ range nomadService %q }}{{ if not (.Tags | contains %q) }}{{ .Address }} %s`+"\n"+`{{ end }}{{ end }}`,
			internalName(a.ID, svc.Name), canaryTag, svc.Name)
	}

	place := strconv.Quote(app.PlaceTagPrefix + app.HostPlace(a.ID))
	nameTag := strconv.Quote("^" + app.NameTagPrefix)
	// Each claimed name, as $n, inside a registration of the place, as $svc.
	forEachName := func(body string) string {
		return `{{ range nomadServices }}{{ if .Tags | contains ` + place + ` }}{{ $svc := .Name }}{{ range .Tags }}` +
			`{{ if regexMatch ` + nameTag + ` . }}{{ $n := regexReplaceAll ` + nameTag + ` "" . }}` + body +
			`{{ end }}{{ end }}{{ end }}{{ end }}`
	}
	b.WriteString(forEachName(`{{ $k := printf "n:%s" $n }}{{ if scratch.Key $k }}{{ scratch.Set $k "" }}{{ else }}{{ scratch.Set $k $svc }}{{ end }}`))

	keep := `(eq (scratch.Get (printf "n:%s" $n)) $svc)`
	for _, n := range own {
		keep += fmt.Sprintf(` (ne $n %q)`, n)
	}
	b.WriteString(forEachName(`{{ if and ` + keep + ` }}{{ range nomadService $svc }}{{ if not (.Tags | contains ` + strconv.Quote(canaryTag) + `) }}{{ .Address }} {{ $n }}` + "\n" + `{{ end }}{{ end }}{{ end }}`))
	return b.String()
}
