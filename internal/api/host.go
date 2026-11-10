package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The host's node (feature 027, schema HostNode): what Nomad says about the
// machine Caravana runs on. Every field Nomad doesn't report is left out, never
// made up.

type hostOSJSON struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type hostVersionsJSON struct {
	Nomad    string `json:"nomad,omitempty"`
	Docker   string `json:"docker,omitempty"`
	Traefik  string `json:"traefik,omitempty"`
	Caravana string `json:"caravana,omitempty"`
}

type hostNodeCPUJSON struct {
	Cores    int    `json:"cores"`
	TotalMHz int    `json:"totalMHz"`
	Model    string `json:"model,omitempty"`
	CoreMHz  int    `json:"coreMHz,omitempty"`
}

type hostAddressJSON struct {
	Public  string `json:"public,omitempty"`
	Private string `json:"private,omitempty"`
}

type hostNodeJSON struct {
	Name          string           `json:"name"`
	State         string           `json:"state"`
	Pool          string           `json:"pool,omitempty"`
	OS            hostOSJSON       `json:"os"`
	Kernel        string           `json:"kernel,omitempty"`
	Arch          string           `json:"arch,omitempty"`
	Versions      hostVersionsJSON `json:"versions"`
	CPU           hostNodeCPUJSON  `json:"cpu"`
	MemoryMB      int              `json:"memoryMB"`
	Address       hostAddressJSON  `json:"address"`
	BootTime      *time.Time       `json:"bootTime,omitempty"`
	LastHeartbeat *time.Time       `json:"lastHeartbeat,omitempty"`
	// RebootRequired is whether the host's own files ask for a reboot; left out
	// until the sampler has read them.
	RebootRequired *bool `json:"rebootRequired,omitempty"`
}

// toHostNodeJSON maps the node's facts. publicHost is the console's own host
// name, the only public address Caravana knows; version is the running
// Caravana's.
func toHostNodeJSON(n *service.HostNode, reboot *hostos.Reboot, publicHost, version string) *hostNodeJSON {
	if n == nil {
		return nil
	}
	out := &hostNodeJSON{
		Name: n.Name, State: n.State, Pool: n.Pool,
		OS:     hostOSJSON{Name: n.OS, Version: n.OSVersion},
		Kernel: n.Kernel, Arch: n.Arch,
		Versions: hostVersionsJSON{Nomad: n.NomadVersion, Docker: n.DockerVersion, Traefik: strings.TrimPrefix(n.TraefikVersion, "v"),
			Caravana: version},
		CPU:      hostNodeCPUJSON{Cores: n.Cores, TotalMHz: n.TotalMHz, Model: n.CPUModel, CoreMHz: n.CoreMHz},
		MemoryMB: n.MemoryMB,
		Address:  hostAddressJSON{Public: publicHost, Private: n.Address},
	}
	if reboot != nil {
		out.RebootRequired = new(reboot.Required)
	}
	if !n.BootTime.IsZero() {
		out.BootTime = new(n.BootTime.UTC())
	}
	if !n.LastHeartbeat.IsZero() {
		out.LastHeartbeat = new(n.LastHeartbeat.UTC())
	}
	return out
}

// consoleHost is the console's host name, without a port: the one the owner
// set as the public address, else the one this request came to.
func (s *server) consoleHost(r *http.Request) string {
	if u, err := url.Parse(s.svc.PublicURL()); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

// getHost answers GET /v1/host: the host's memory, CPU and disks, the room
// for more apps, seven days of memory and the node's facts. It reads the
// store and the sampler's last reading and never calls Nomad. ?server=<name> asks for one server's
// numbers (default: the one Caravana runs on; 404 server_not_found); only the server Caravana runs on
// has a node block.
func (s *server) getHost(w http.ResponseWriter, r *http.Request) error {
	view, err := s.svc.Host(r.Context(), r.URL.Query().Get("server"))
	if err != nil {
		return err
	}
	out := toHostJSON(view)
	out.Node = toHostNodeJSON(view.Node, view.Reboot, s.consoleHost(r), s.version)
	writeJSON(w, http.StatusOK, out)
	return nil
}
