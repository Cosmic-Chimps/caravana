package compose

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// portLabelRE is a port name the app model accepts (app.Port.Name).
var portLabelRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// service turns one Compose service and its overlay into an app.Service, and
// the warnings about what it honors differently.
func service(p *types.Project, sc types.ServiceConfig, ov Overlay) (app.Service, []app.Warning, error) {
	field := "services." + sc.Name
	svc := app.Service{Name: sc.Name, Image: sc.Image}
	var warnings []app.Warning
	warn := func(f, format string, args ...any) {
		warnings = append(warnings, app.Warning{Field: field + f, Message: fmt.Sprintf(format, args...)})
	}

	if sc.Image == "" {
		return app.Service{}, nil, app.NewFieldError(field+".image", "is required: Caravana deploys prebuilt images")
	}
	if len(sc.Command) > 0 {
		svc.Command, svc.Args = sc.Command[0], slices.Clone(sc.Command[1:])
	}

	for _, name := range sortedKeys(sc.Environment) {
		v := sc.Environment[name]
		if v == nil {
			return app.Service{}, nil, app.NewFieldError(field+".environment."+name,
				"has no value: there is no shell to take it from; write NAME=value, or send it as a secret")
		}
		svc.Env = append(svc.Env, app.EnvVar{Name: name, Value: *v})
	}

	ports, err := servicePorts(field, sc, warn)
	if err != nil {
		return app.Service{}, nil, err
	}
	svc.Ports = ports

	for i, v := range sc.Volumes {
		f := fmt.Sprintf("%s.volumes[%d]", field, i)
		switch {
		case v.Type != types.VolumeTypeVolume:
			return app.Service{}, nil, app.NewFieldError(f, "is a %s mount; only named volumes are supported (declare it under the top-level volumes:)", v.Type)
		case v.Source == "":
			return app.Service{}, nil, app.NewFieldError(f, "has no name: name the volume, so its data survives deploys")
		}
		svc.Volumes = append(svc.Volumes, app.Volume{Name: v.Source, Path: v.Target, ReadOnly: v.ReadOnly})
	}

	if sc.Deploy != nil && sc.Deploy.Replicas != nil {
		svc.Replicas = new(*sc.Deploy.Replicas)
	} else if sc.Scale != nil {
		svc.Replicas = new(*sc.Scale)
	}
	// Memory as Docker means it: a reservation the scheduler counts, and a
	// limit the service may burst to while the node has memory spare.
	ceiling, reserve, cpus := int64(sc.MemLimit), int64(sc.MemReservation), float64(sc.CPUS)
	if sc.Deploy != nil && sc.Deploy.Resources.Limits != nil {
		if b := int64(sc.Deploy.Resources.Limits.MemoryBytes); b > 0 {
			ceiling = b
		}
		if c := float64(sc.Deploy.Resources.Limits.NanoCPUs); c > 0 {
			cpus = c
		}
	}
	if sc.Deploy != nil && sc.Deploy.Resources.Reservations != nil {
		if b := int64(sc.Deploy.Resources.Reservations.MemoryBytes); b > 0 {
			reserve = b
		}
	}
	if ceiling > 0 {
		svc.Resources.MemoryMaxMB = megabytes(ceiling)
	}
	if reserve > 0 {
		svc.Resources.MemoryMB = megabytes(reserve)
	}
	if cpus > 0 {
		svc.Resources.CPUMHz = int(math.Round(cpus * 1000))
		warn(".deploy.resources.limits.cpus", "%g CPU becomes %d MHz: Nomad shares CPU by MHz, not by cores", cpus, svc.Resources.CPUMHz)
	}

	if err := applyOverlay(field, &svc, sc, ov, warn); err != nil {
		return app.Service{}, nil, err
	}

	serviceWarnings(sc, warn)
	return svc, warnings, nil
}

// servicePorts reads ports and expose. Each container port is listed once,
// named after the Compose port's name when that is a valid label, else
// "p<port>".
func servicePorts(field string, sc types.ServiceConfig, warn func(string, string, ...any)) ([]app.Port, error) {
	var ports []app.Port
	seen := map[int]bool{}
	add := func(target int, name string) {
		if seen[target] {
			return
		}
		seen[target] = true
		if !portLabelRE.MatchString(name) {
			name = "p" + strconv.Itoa(target)
		}
		ports = append(ports, app.Port{Name: name, ContainerPort: target})
	}
	for i, pc := range sc.Ports {
		f := fmt.Sprintf(".ports[%d]", i)
		if pc.Protocol != "" && pc.Protocol != "tcp" {
			return nil, app.NewFieldError(field+f+".protocol", "%q isn't supported; only tcp is", pc.Protocol)
		}
		if pc.Published != "" || pc.HostIP != "" || pc.Mode == "host" {
			warn(f, "the host port isn't opened: other services reach port %d by this service's name, and the internet through the request's domains", pc.Target)
		}
		add(int(pc.Target), pc.Name)
	}
	for i, e := range sc.Expose {
		n, err := strconv.Atoi(strings.TrimSuffix(e, "/tcp"))
		if err != nil || n < 1 || n > 65535 {
			return nil, app.NewFieldError(fmt.Sprintf("%s.expose[%d]", field, i), "%q must be one TCP port", e)
		}
		add(n, "")
	}
	return ports, nil
}

// applyOverlay adds the request's domains, health check and secrets for the
// service, and the default TCP check.
func applyOverlay(field string, svc *app.Service, sc types.ServiceConfig, ov Overlay, warn func(string, string, ...any)) error {
	// portName finds the label of a container port given in the request;
	// zero means the service's only port.
	portName := func(f string, port int) (string, error) {
		if port == 0 {
			if len(svc.Ports) != 1 {
				return "", app.NewFieldError(f, "the service has %d ports; say which one", len(svc.Ports))
			}
			return svc.Ports[0].Name, nil
		}
		for _, p := range svc.Ports {
			if p.ContainerPort == port {
				return p.Name, nil
			}
		}
		return "", app.NewFieldError(f, "the service has no port %d in the file", port)
	}

	for k, d := range ov.Domains {
		f := fmt.Sprintf("%s.domains[%d]", field, k)
		if len(svc.Ports) == 0 {
			return app.NewFieldError(f, "the service has no ports to route to")
		}
		name, err := portName(f+".port", d.Port)
		if err != nil {
			return err
		}
		svc.Domains = append(svc.Domains, app.Domain{Host: d.Host, Port: name})
	}

	composeCheck := sc.HealthCheck != nil && !sc.HealthCheck.Disable &&
		!(len(sc.HealthCheck.Test) > 0 && sc.HealthCheck.Test[0] == "NONE")
	switch {
	case ov.Health != nil:
		if len(svc.Ports) == 0 {
			return app.NewFieldError(field+".health", "the service has no ports to check")
		}
		h := &app.HealthCheck{Path: ov.Health.Path, Interval: ov.Health.Interval, Timeout: ov.Health.Timeout}
		if ov.Health.Port != 0 {
			name, err := portName(field+".health.port", ov.Health.Port)
			if err != nil {
				return err
			}
			h.Port = name
		}
		svc.Health = h
		if composeCheck {
			warn(".healthcheck", "is replaced by the request's health check: Nomad's checks are HTTP or TCP and can't run commands")
		}
	case len(svc.Ports) > 0:
		svc.Health = &app.HealthCheck{} // TCP on the domains' port or the first port
		if composeCheck {
			warn(".healthcheck", "is replaced by a TCP check on port %d: Nomad's checks are HTTP or TCP and can't run commands; send health in the request for an HTTP check", svc.Ports[0].ContainerPort)
		}
	case composeCheck:
		warn(".healthcheck", "isn't run: Nomad's checks are HTTP or TCP and need a port; Nomad restarts the service when it exits")
	}

	svc.Secrets, svc.Backup = ov.Secrets, ov.Backup
	return nil
}

// serviceWarnings are the attributes Caravana accepts but honors
// differently from Compose.
func serviceWarnings(sc types.ServiceConfig, warn func(string, string, ...any)) {
	if len(sc.DependsOn) > 0 {
		warn(".depends_on", "services start together: Nomad restarts a service that fails until what it needs answers")
	}
	switch r := sc.Restart; {
	case r == "no":
		warn(".restart", "is ignored: Nomad restarts a service that exits")
	case strings.HasPrefix(r, "on-failure"):
		warn(".restart", "is ignored: Nomad restarts a service whenever it exits, with its own limits")
	}
	if sc.ContainerName != "" {
		warn(".container_name", "is ignored: Nomad names containers")
	}
	if sc.Hostname != "" {
		warn(".hostname", "is ignored: Nomad names containers; other services reach this one by its service name")
	}
	if len(sc.Labels) > 0 {
		warn(".labels", "aren't applied: routing comes from the request's domains")
	}
	if sc.PullPolicy != "" {
		warn(".pull_policy", "is ignored: every deploy pins the image to its current digest")
	}
	if sc.Logging != nil {
		warn(".logging", "is ignored: Nomad keeps each copy's output, which the console and the logs API read")
	}
	if sc.StopGracePeriod != nil {
		warn(".stop_grace_period", "is ignored: Nomad's kill timeout applies")
	}
}

// megabytes is a byte count in whole MB, rounded up.
func megabytes(b int64) int { return int(math.Ceil(float64(b) / (1 << 20))) }
