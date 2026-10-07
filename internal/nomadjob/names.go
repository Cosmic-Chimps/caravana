package nomadjob

import (
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Services by name (research R3). In an app with two or more services, each
// service reaches the others by their names, as in Docker Compose. Nomad
// without Consul has no DNS for that, so every group gets a small sidecar task
// that keeps the group's shared /etc/hosts pointing each name at that
// service's live allocations. A single-service app gets none of this.

// namesTaskName is the sidecar's task name. It must not be "hosts": that
// task's directory would collide with the allocation's hosts file.
const namesTaskName = "names"

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

// sharesNames reports whether an app gets the internal registrations and the
// names sidecar: only apps with two or more services, at least one of them
// with ports. Without ports there is no name worth resolving, and Nomad
// refuses a template with nothing in it.
func sharesNames(a app.App) bool {
	if len(a.Services) < 2 {
		return false
	}
	for _, svc := range a.Services {
		if len(svc.Ports) > 0 {
			return true
		}
	}
	return false
}

// internalName is the registration other services find a service by.
func internalName(appID, service string) string {
	return appID + "-" + service + "-internal"
}

// checkNames checks what the names sidecar needs, for apps that get it: a free
// task name in every group, and internal registration names Nomad accepts for
// every service with ports.
func checkNames(a app.App) error {
	if !sharesNames(a) {
		return nil
	}
	for i, svc := range a.Services {
		field := fmt.Sprintf("services[%d].name", i)
		if svc.Name == namesTaskName {
			return app.NewFieldError(field, "%q is the name of Caravana's own task in apps with more than one service; choose another name", svc.Name)
		}
		if len(svc.Ports) == 0 {
			continue
		}
		name := internalName(a.ID, svc.Name)
		if len(name) > maxServiceNameLen {
			return app.NewFieldError(field, "%s is %d characters; Nomad service names allow at most %d", name, len(name), maxServiceNameLen)
		}
	}
	return nil
}

// internalRegistration registers a service's first port under its internal
// name, with the allocation's own address ("alloc" mode: the bridge IP and the
// container port), so other services of the app can reach it directly. It has
// no check, since the main registration's check already gates deploys, and its
// canaries are tagged so the names template can leave them out.
func internalRegistration(appID string, svc app.Service) *api.Service {
	return &api.Service{
		Name:        internalName(appID, svc.Name),
		PortLabel:   svc.Ports[0].Name,
		Provider:    "nomad",
		AddressMode: "alloc",
		CanaryTags:  canaryTags,
	}
}

// namesTask is the sidecar that keeps /etc/hosts up to date. It starts before
// the service's own task (prestart) and runs beside it (sidecar).
func namesTask(a app.App, s Settings) *api.Task {
	return &api.Task{
		Name:      namesTaskName,
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

// namesTemplate renders one "<address> <service>" line per live allocation of
// each service with ports, in the app's order. Canaries are left out, so a
// name keeps pointing at the old version until the new one is promoted. A
// service that isn't registered yet renders nothing and simply doesn't
// resolve.
//
// The list includes the group's own service: Compose resolves a service's own
// name too, and some images (a database or broker that names itself in its
// config) depend on it. During a blue/green rollout, a canary's own name
// still points at the old version until promotion, like every other name.
func namesTemplate(a app.App) string {
	var b strings.Builder
	for _, svc := range a.Services {
		if len(svc.Ports) == 0 {
			continue
		}
		fmt.Fprintf(&b, `{{ range nomadService %q }}{{ if not (.Tags | contains %q) }}{{ .Address }} %s`+"\n"+`{{ end }}{{ end }}`,
			internalName(a.ID, svc.Name), canaryTag, svc.Name)
	}
	return b.String()
}
