// Package nomadjob turns an app.App into a Nomad job using the
// official Go client types. Jobs are submitted as JSON through the
// HTTP API, so we never have to write HCL.
package nomadjob

import (
	"fmt"
	"strings"
	"time"

	nomad "github.com/hashicorp/nomad/api"

	"github.com/you/nomadpaas/internal/app"
)

const certResolver = "letsencrypt" // must match Traefik's static config

// Translate builds a Nomad service job for the app. digests maps
// service name -> resolved "repo@sha256:..." reference.
func Translate(a app.App, d app.Deployment) (*nomad.Job, error) {
	job := &nomad.Job{
		ID:          ptr(a.ID),
		Name:        ptr(a.Name),
		Type:        ptr("service"),
		Datacenters: []string{"*"},
		Meta: map[string]string{
			"paas.app":        a.ID,
			"paas.deployment": d.ID, // changes every deploy -> forces a new version
		},
		Update: &nomad.UpdateStrategy{
			MaxParallel:     ptr(1),
			HealthCheck:     ptr("checks"),
			MinHealthyTime:  ptr(10 * time.Second),
			HealthyDeadline: ptr(3 * time.Minute),
			AutoRevert:      ptr(true), // failed deploy -> back to last good version
		},
	}

	for _, s := range a.Services {
		image, ok := d.Digests[s.Name]
		if !ok {
			return nil, fmt.Errorf("service %q: no resolved digest", s.Name)
		}
		tg, err := taskGroup(a, s, image)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", s.Name, err)
		}
		job.TaskGroups = append(job.TaskGroups, tg)
	}
	return job, nil
}

func taskGroup(a app.App, s app.Service, image string) (*nomad.TaskGroup, error) {
	replicas := s.Replicas
	if replicas < 1 {
		replicas = 1
	}

	tg := &nomad.TaskGroup{
		Name:  ptr(s.Name),
		Count: ptr(replicas),
		Networks: []*nomad.NetworkResource{{
			Mode:         "bridge",
			DynamicPorts: dynamicPorts(s.Ports),
		}},
	}

	if s.PinToNode != "" {
		tg.Constraints = append(tg.Constraints,
			nomad.NewConstraint("${node.unique.name}", "=", s.PinToNode))
	}

	svc, err := routingService(a, s)
	if err != nil {
		return nil, err
	}
	if svc != nil {
		tg.Services = []*nomad.Service{svc}
	}

	task := &nomad.Task{
		Name:   s.Name,
		Driver: "docker",
		Config: map[string]interface{}{"image": image},
		Env:    s.Env,
		Resources: &nomad.Resources{
			CPU:      ptr(orDefault(s.Resources.CPUMHz, 100)),
			MemoryMB: ptr(orDefault(s.Resources.MemoryMB, 128)),
		},
	}
	if len(s.Command) > 0 {
		task.Config["command"] = s.Command[0]
		task.Config["args"] = s.Command[1:]
	}

	// Volumes: host volumes only in v1 (pin the group with PinToNode).
	if len(s.Volumes) > 0 {
		tg.Volumes = map[string]*nomad.VolumeRequest{}
		for i, v := range s.Volumes {
			name := fmt.Sprintf("vol%d", i)
			tg.Volumes[name] = &nomad.VolumeRequest{
				Name: name, Type: "host", Source: v.HostVolume, ReadOnly: v.ReadOnly,
			}
			task.VolumeMounts = append(task.VolumeMounts, &nomad.VolumeMount{
				Volume: ptr(name), Destination: ptr(v.MountPath), ReadOnly: ptr(v.ReadOnly),
			})
		}
	}

	// Secrets: stored as a Nomad Variable at nomad/jobs/<job>, which the
	// job can read via workload identity with no extra ACL setup.
	if s.Secrets {
		tmpl := fmt.Sprintf(`{{- with nomadVar "nomad/jobs/%s" -}}
{{- range $k, $v := . }}{{ $k }}={{ $v }}
{{ end -}}
{{- end -}}`, a.ID)
		task.Templates = append(task.Templates, &nomad.Template{
			EmbeddedTmpl: ptr(tmpl),
			DestPath:     ptr("secrets/app.env"),
			Envvars:      ptr(true),
			ChangeMode:   ptr("restart"),
		})
	}

	tg.Tasks = []*nomad.Task{task}
	return tg, nil
}

// routingService registers the service with Nomad's native service
// discovery. Traefik's Nomad provider reads these tags.
func routingService(a app.App, s app.Service) (*nomad.Service, error) {
	if len(s.Ports) == 0 {
		return nil, nil // e.g. a background worker
	}
	port := s.Ports[0].Name
	if len(s.Domains) > 0 {
		port = s.Domains[0].Port
	}

	svc := &nomad.Service{
		Name:      fmt.Sprintf("%s-%s", a.ID, s.Name),
		PortLabel: port,
		Provider:  "nomad", // no Consul needed
	}

	if len(s.Domains) > 0 {
		router := fmt.Sprintf("%s-%s", a.ID, s.Name)
		hosts := make([]string, 0, len(s.Domains))
		tls := false
		for _, d := range s.Domains {
			hosts = append(hosts, fmt.Sprintf("Host(`%s`)", d.Host))
			tls = tls || d.TLS
		}
		svc.Tags = []string{
			"traefik.enable=true",
			fmt.Sprintf("traefik.http.routers.%s.rule=%s", router, strings.Join(hosts, " || ")),
		}
		if tls {
			svc.Tags = append(svc.Tags,
				fmt.Sprintf("traefik.http.routers.%s.entrypoints=websecure", router),
				fmt.Sprintf("traefik.http.routers.%s.tls.certresolver=%s", router, certResolver),
			)
		} else {
			svc.Tags = append(svc.Tags,
				fmt.Sprintf("traefik.http.routers.%s.entrypoints=web", router))
		}
	}

	if h := s.Health; h != nil {
		check := nomad.ServiceCheck{
			Name:      "health",
			Type:      "tcp",
			PortLabel: orDefaultStr(h.Port, port),
			Interval:  orDefaultDur(h.Interval, 10*time.Second),
			Timeout:   orDefaultDur(h.Timeout, 2*time.Second),
		}
		if h.Path != "" {
			check.Type = "http"
			check.Path = h.Path
		}
		svc.Checks = []nomad.ServiceCheck{check}
	}
	return svc, nil
}

func dynamicPorts(ports []app.Port) []nomad.Port {
	out := make([]nomad.Port, 0, len(ports))
	for _, p := range ports {
		// Host port chosen by Nomad, mapped to the container port.
		out = append(out, nomad.Port{Label: p.Name, To: p.ContainerPort})
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func orDefaultStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func orDefaultDur(v, d time.Duration) time.Duration {
	if v == 0 {
		return d
	}
	return v
}
