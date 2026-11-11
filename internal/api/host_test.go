package api

import (
	"net/http"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 027: getHost's node. Before the sampler's first reading it is null;
// after one it is what Nomad's node record says, checked against the contract.

func TestGetHostNodeIsNullBeforeTheFirstReading(t *testing.T) {
	e := monEnv(t)
	host := getJSON(t, e, "/v1/host", "Host")
	if v, ok := host["node"]; !ok || v != nil {
		t.Errorf("node = %v (present %v), want null", v, ok)
	}
}

func TestGetHostNode(t *testing.T) {
	e := newTestEnvWith(t, "", func(s *service.Service) {
		s.SetMetricsSampleGap(500 * time.Millisecond)
		s.SetHostRoot(fstest.MapFS{"run/reboot-required": {Data: []byte("*** System restart required ***\n")}})
	})
	// The fake Nomad knows the node before the sampler's first tick, up for a day.
	e.Nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	stats := deploytest.LabHostStats()
	stats.Uptime = uint64(24 * time.Hour / time.Second)
	e.Nomad.SetHostStats(stats)
	e.Nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", []*api.NodeEvent{
		{Subsystem: "Cluster", Message: "Node registered", Timestamp: time.Now().Add(-time.Hour)},
	})
	e.Nomad.SetNodeNetworks(deploytest.Caravana0Networks("10.99.0.1"))
	e.Nomad.SetTraefikJob("traefik:v3.7.13")

	var host map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for {
		host = getJSON(t, e, "/v1/host", "Host")
		if host["node"] != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	node := obj(t, host["node"], "node")
	if node["name"] != "lab" || node["state"] != "ready" || node["pool"] != "default" {
		t.Errorf("node = %v", node)
	}
	if os := obj(t, node["os"], "os"); os["name"] != "ubuntu" || os["version"] != "26.04" {
		t.Errorf("os = %v", os)
	}
	if node["kernel"] != "7.0.0-14-generic" || node["arch"] != "arm64" {
		t.Errorf("kernel/arch = %v / %v", node["kernel"], node["arch"])
	}
	versions := obj(t, node["versions"], "versions")
	if versions["nomad"] != "2.0.1" || versions["docker"] != "29.1.3" || versions["traefik"] != "3.7.13" || versions["caravana"] != "1.2.3-test" {
		t.Errorf("versions = %v", versions)
	}
	if cpu := obj(t, node["cpu"], "cpu"); cpu["cores"] != float64(2) || cpu["totalMHz"] != float64(4000) {
		t.Errorf("cpu = %v", cpu)
	}
	if node["memoryMB"] != float64(1949) {
		t.Errorf("memoryMB = %v", node["memoryMB"])
	}
	addr := obj(t, node["address"], "address")
	if addr["private"] != "10.99.0.1" || addr["public"] == "" {
		t.Errorf("address = %v", addr)
	}
	if node["bootTime"] == nil {
		t.Error("no bootTime")
	}
	if node["rebootRequired"] != true {
		t.Errorf("rebootRequired = %v, want true: the host's files ask for a reboot", node["rebootRequired"])
	}
}

func TestHostAndActivityAreRefusedToADeployToken(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	for _, path := range []string{"/v1/host", "/v1/activity"} {
		req, err := http.NewRequest(http.MethodGet, e.RawURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s with a deploy token = %d, want 403", path, resp.StatusCode)
		}
	}
}

// Feature 031: getHost?server= reads one server's numbers. A name no server has is 404
// server_not_found; a server that joined and has no Nomad node yet has a host with nothing sampled.
func TestGetHostOfAServer(t *testing.T) {
	e := monEnv(t)
	withCluster(t, e)
	addServer(t, e, `{"name": "later"}`)

	for _, path := range []string{"/v1/host", "/v1/host?server=hel-1", "/v1/host?server=later"} {
		body := getJSON(t, e, path, "Host")
		if body["sampledAt"] != nil {
			t.Errorf("GET %s: sampledAt = %v, want null (never sampled)", path, body["sampledAt"])
		}
	}
	// The answer names the server the numbers are of, and says whether it is the one Caravana runs on
	// (the only one whose operating system is read); the console's pages draw nothing the API doesn't say.
	for path, want := range map[string]struct {
		server string
		osRead bool
	}{"/v1/host": {"hel-1", true}, "/v1/host?server=hel-1": {"hel-1", true}, "/v1/host?server=later": {"later", false}} {
		body := getJSON(t, e, path, "Host")
		if body["server"] != want.server || body["osRead"] != want.osRead {
			t.Errorf("GET %s: server = %v, osRead = %v; want %q and %v", path, body["server"], body["osRead"], want.server, want.osRead)
		}
	}
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/host?server=nobody", "", "")
	wantError(t, "GET /v1/host?server=nobody", status, raw, http.StatusNotFound, codeServerNotFound, "")
}
