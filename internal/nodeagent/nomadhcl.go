package nodeagent

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// Nomad's default ports for RPC and serf; the mesh address is where both listen on a Nomad server.
const (
	nomadRPCPort  = 4647
	nomadSerfPort = 4648
)

// nomadTemplate is /etc/nomad.d/nomad.hcl in three forms (contracts/node.md §4): the first server
// (F), a Nomad server (S) and a joined server (J). Its comments are the ones deploy/install/
// nomad.hcl.tpl had, moved here.
const nomadTemplate = `# Written by ` + "`caravana node`" + `: the next sync rewrites it. Change it through Caravana.
#
# Nomad agent config for a Caravana server: ACLs on. Unlike the lab, nothing listens publicly: the
# HTTP API is on loopback, Nomad's RPC and serf (on a Nomad server) are on this server's private mesh
# address, and every allocation port is on the private caravana0 interface, so apps are reached only
# through Traefik (spec FR-005, FR-005a).

{{if not .First -}}
name       = {{q .Name}}
{{end -}}
datacenter = "dc1"

# SIGTERM makes Nomad leave the cluster before it exits; "caravana node leave" sends it. A stop, a
# restart and a reboot send SIGINT (the unit's KillSignal), which leaves nothing: a Nomad server that
# only restarts must keep its place in raft. Without a graceful leave, a server taken out of a cluster
# with two Nomad servers left the other without a quorum (cluster test, research L16).
leave_on_terminate = true
data_dir   = "/opt/nomad/data"
bind_addr  = "127.0.0.1"

addresses {
  http = "127.0.0.1"
{{- if .Server}}
  rpc  = {{q .Mesh}}
  serf = {{q .Mesh}}
{{- end}}
}

advertise {
  http = "127.0.0.1"
  rpc  = {{q .Mesh}}
  serf = {{q .Mesh}}
}

tls {
  http                   = false
  rpc                    = true
  ca_file                = "/etc/nomad.d/tls/ca.pem"
  cert_file              = "/etc/nomad.d/tls/agent.pem"
  key_file               = "/etc/nomad.d/tls/agent-key.pem"
  verify_server_hostname = true
}
{{if .Server}}
server {
  enabled          = true
{{- if .First}}
  bootstrap_expect = 1
{{- end}}
  encrypt          = {{q .GossipKey}}

  # After a new leader is elected, Nomad gives every node this long to heartbeat before it calls one
  # down. Its default is 5 minutes: when the server that was lost was the leader, its copies stayed
  # registered, and Traefik kept sending them requests, for five minutes (cluster test, research L14).
  failover_heartbeat_ttl = "1m"

  # How long past its heartbeat's time a node may be before Nomad calls it down. Nomad's default is
  # 10 seconds. A node's heartbeat goes to any Nomad server; when that server is the one that was
  # lost, the heartbeat hangs until the connection fails (24 seconds in the cluster test), and with
  # the default a healthy server was called down and its copies were moved (research L18).
  heartbeat_grace = "30s"
{{- if .RetryJoin}}
  server_join {
    retry_join = {{.RetryJoin}}
  }
{{- end}}
}
{{end}}
client {
  enabled               = true
  cni_path              = "/opt/cni/bin"
  node_pool             = {{q .Pool}}

  # Allocation ports bind to this interface's address (10.99.0.N). Nomad's port mappings are
  # DNAT rules that a host firewall can't block, so binding them to a private address is what
  # keeps http://<public-ip>:<port> closed (research R4).
  network_interface     = "caravana0"
{{- if .Subnet}}

  # Each server's containers have a subnet of their own, routed through the mesh, so a service
  # name resolves to a container on another server. The first server keeps Nomad's default.
  bridge_network_subnet = {{q .Subnet}}
{{- end}}
{{- if .ClientServers}}

  servers               = {{.ClientServers}}
{{- end}}
{{- if .CPU}}

  # ARM VMs often report no clock speed; Nomad then sees 0 MHz and places nothing.
  cpu_total_compute     = {{.CPU}}
{{- end}}
{{- if .First}}

  # Traefik, the one ingress, runs on the server marked here.
  meta {
    caravana_ingress = "true"
  }

  # Traefik keeps its Let's Encrypt certificates here, across restarts and reboots.
  host_volume "traefik-acme" {
    path      = "/var/lib/caravana-host/traefik"
    read_only = false
  }
{{- end}}
}

acl {
  enabled = true
}

plugin "docker" {
  config {
    allow_privileged = false
  }
}

# Nomad's stats hook doesn't collect task stats unless this is set (seen with Nomad 2.0.7): the
# stats endpoint answers 200 with no tasks, and the console's CPU and memory per copy (feature
# 014) would be empty. No metrics sink is configured, so nothing is published anywhere; it only
# turns the collection on.
telemetry {
  publish_allocation_metrics = true
}
`

var nomadTmpl = template.Must(template.New("nomad.hcl").Funcs(template.FuncMap{"q": strconv.Quote}).Parse(nomadTemplate))

// nomadData is what the template reads, worked out from the State.
type nomadData struct {
	First, Server bool
	Name, Pool    string
	Mesh          string
	GossipKey     string
	Subnet        string // a joined server's bridge subnet; empty on the first
	RetryJoin     string // HCL list of the other Nomad servers' serf addresses, or ""
	ClientServers string // HCL list of the other Nomad servers' RPC addresses, or ""
	CPU           int
}

// RenderNomad is the text of /etc/nomad.d/nomad.hcl for this state. gossipKey is written only on a
// Nomad server. The state is trusted: callers validate it first (State.Validate). For a state with
// an index outside 1 to 63 the text is one comment line saying so, which Nomad would refuse to run
// with rather than run wrongly.
func RenderNomad(s State, gossipKey string) string {
	out, err := renderNomad(s, gossipKey)
	if err != nil {
		return "# caravana node: " + err.Error() + "\n"
	}
	return out
}

func renderNomad(s State, gossipKey string) (string, error) {
	addrs, err := mesh.Of(s.Index)
	if err != nil {
		return "", err
	}
	d := nomadData{
		First:  s.First,
		Server: s.Role == RoleServer,
		Name:   s.Name,
		Pool:   s.Pool,
		Mesh:   addrs.Mesh.String(),
		CPU:    s.CPUTotalCompute,
	}
	if d.Server {
		d.GossipKey = gossipKey
	}
	if !s.First {
		d.Subnet = addrs.Containers.String()
	}
	var others []int
	for _, i := range s.Servers {
		if i != s.Index {
			others = append(others, i)
		}
	}
	sort.Ints(others)
	var serf, rpc []string
	for _, i := range others {
		o, err := mesh.Of(i)
		if err != nil {
			return "", err
		}
		serf = append(serf, fmt.Sprintf("%s:%d", o.Mesh, nomadSerfPort))
		rpc = append(rpc, fmt.Sprintf("%s:%d", o.Mesh, nomadRPCPort))
	}
	if d.Server {
		d.RetryJoin = hclList(serf)
	}
	// A Nomad server's own client talks to its own server. Given a servers list, it heartbeats to one
	// of the listed servers, and when that one is lost the heartbeat hangs until the connection's
	// keepalive fails: the node's TTL expires and its copies are replaced (cluster test, 2026-11-10:
	// the first server's node was marked down when server 3 was lost). Only a client lists servers.
	if !d.Server {
		d.ClientServers = hclList(rpc)
	}

	var b bytes.Buffer
	if err := nomadTmpl.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

func hclList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
