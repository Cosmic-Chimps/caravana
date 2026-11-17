package compose

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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
	if len(sc.Entrypoint) > 0 {
		// compose-go has already split the string form the way a shell would.
		svc.Entrypoint = slices.Clone(sc.Entrypoint)
	}
	if len(sc.DNS) > 0 || len(sc.DNSSearch) > 0 || len(sc.DNSOpts) > 0 {
		// compose-go has already made a list of the string form.
		d := app.DNS{Servers: slices.Clone(sc.DNS), Searches: slices.Clone(sc.DNSSearch), Options: slices.Clone(sc.DNSOpts)}
		// Checked here, not after translation, so the error names the
		// attribute the user wrote.
		if err := app.CheckDNS(d, field+".dns", field+".dns_search", field+".dns_opt"); err != nil {
			return app.Service{}, nil, err
		}
		svc.DNS = &d
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

	ports, err := servicePorts(field, sc, ov, warn)
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

	if sc.StopGracePeriod != nil {
		n, err := stopTimeout(field, time.Duration(*sc.StopGracePeriod))
		if err != nil {
			return app.Service{}, nil, err
		}
		svc.StopTimeout = n
	}

	if err := applyOverlay(field, &svc, sc, ov, warn); err != nil {
		return app.Service{}, nil, err
	}

	serviceWarnings(sc, warn)
	return svc, warnings, nil
}

// servicePorts reads ports and expose. Each container port is listed once,
// named after the Compose port's name when that is a valid label, else
// "p<port>". A TCP and a UDP entry for one container port make one port that
// speaks both (spec 041). The file's published numbers are only warned about,
// unless the overlay publishes that port.
func servicePorts(field string, sc types.ServiceConfig, ov Overlay, warn func(string, string, ...any)) ([]app.Port, error) {
	var ports []app.Port
	index := map[int]int{} // container port → its place in ports
	add := func(target int, name, protocol string) {
		// TCP is the default, so it is left empty and a file without UDP
		// makes the same app as before spec 041.
		stored := protocol
		if protocol == app.ProtocolTCP {
			stored = ""
		}
		if i, ok := index[target]; ok {
			if ports[i].Protocol != stored {
				ports[i].Protocol = app.ProtocolTCPUDP
			}
			return
		}
		index[target] = len(ports)
		if !portLabelRE.MatchString(name) {
			name = "p" + strconv.Itoa(target)
		}
		ports = append(ports, app.Port{Name: name, ContainerPort: target, Protocol: stored})
	}
	published := map[int]bool{}
	for _, pub := range ov.Publish {
		published[pub.Port] = true
	}
	for i, pc := range sc.Ports {
		f := fmt.Sprintf(".ports[%d]", i)
		protocol := pc.Protocol
		if protocol == "" {
			protocol = app.ProtocolTCP
		}
		if protocol != app.ProtocolTCP && protocol != app.ProtocolUDP {
			return nil, app.NewFieldError(field+f+".protocol", "%q isn't supported; only tcp and udp are", pc.Protocol)
		}
		if (pc.Published != "" || pc.HostIP != "" || pc.Mode == "host") && !published[int(pc.Target)] {
			warn(f, "the host port isn't opened: other services reach port %d by this service's name, and the internet through the request's domains; to publish it on the server's address, add {\"port\": %d} to the overlay's publish list", pc.Target, pc.Target)
		}
		add(int(pc.Target), pc.Name, protocol)
	}
	for i, e := range sc.Expose {
		number, protocol := e, app.ProtocolTCP
		if n, ok := strings.CutSuffix(e, "/udp"); ok {
			number, protocol = n, app.ProtocolUDP
		} else if n, ok := strings.CutSuffix(e, "/tcp"); ok {
			number = n
		}
		n, err := strconv.Atoi(number)
		if err != nil || n < 1 || n > 65535 {
			return nil, app.NewFieldError(fmt.Sprintf("%s.expose[%d]", field, i), "%q must be one TCP or UDP port, like 8080 or 3478/udp", e)
		}
		add(n, "", protocol)
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
		svc.Domains = append(svc.Domains, app.Domain{Host: d.Host, Port: name,
			Path: d.Path, WWWRedirect: d.WWWRedirect, TeamOnly: d.TeamOnly})
	}

	for k, pub := range ov.Publish {
		f := fmt.Sprintf("%s.publish[%d]", field, k)
		var port *app.Port
		for i := range svc.Ports {
			if svc.Ports[i].ContainerPort == pub.Port {
				port = &svc.Ports[i]
			}
		}
		if port == nil {
			return app.NewFieldError(f+".port", "the service has no port %d in the file", pub.Port)
		}
		if port.IsPublished() {
			return app.NewFieldError(f+".port", "port %d is published twice", pub.Port)
		}
		port.Published = pub.Published
		if port.Published == 0 {
			port.Published = pub.Port
		}
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

	svc.Secrets, svc.Backup, svc.Identities = ov.Secrets, ov.Backup, ov.Identities
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
}

// stopTimeout is Compose's stop_grace_period as the service's stop timeout:
// whole seconds, rounded up. Nomad clients cap a task's kill timeout, so a
// longer period is refused rather than shortened without a word.
func stopTimeout(field string, d time.Duration) (int, error) {
	switch {
	case d <= 0:
		return 0, app.NewFieldError(field+".stop_grace_period", "must be at least 1s")
	case d > app.MaxStopTimeout*time.Second:
		return 0, app.NewFieldError(field+".stop_grace_period",
			"is %s; at most %ds is allowed: Nomad clients cap a task's kill timeout at 30 s", d, app.MaxStopTimeout)
	}
	return int((d + time.Second - 1) / time.Second), nil
}

// megabytes is a byte count in whole MB, rounded up.
func megabytes(b int64) int { return int(math.Ceil(float64(b) / (1 << 20))) }
