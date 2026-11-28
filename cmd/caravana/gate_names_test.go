package main

// The names-across-apps gate step (task 2xg2, spec 062): the apps of one
// project and environment reach each other by name. It is in its own file only
// because gate_test.go is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

const (
	namesProject = "lab062"
	namesPgImage = "postgres:17-alpine"
)

// namesApp is a one-service app body for deployApp. A service with port 0 has
// no ports: a worker that only sleeps.
func namesApp(id, service, image string, port int, command ...string) string {
	svc := map[string]any{
		"name":      service,
		"image":     image,
		"resources": map[string]any{"memoryMB": 64, "memoryMaxMB": 256},
	}
	if port > 0 {
		svc["ports"] = []map[string]any{{"name": "main", "containerPort": port}}
		svc["health"] = map[string]any{"interval": "5s", "timeout": "2s"}
	}
	if len(command) > 0 {
		svc["command"], svc["args"] = command[0], command[1:]
	}
	if image == namesPgImage && port > 0 {
		svc["env"] = []map[string]any{{"name": "POSTGRES_PASSWORD", "value": "gate-names"}}
	}
	b, _ := json.Marshal(map[string]any{"id": id, "source": map[string]string{"kind": "image"}, "services": []any{svc}})
	return string(b)
}

// namesAddress is one entry of an AppRecord's addresses.
type namesAddress struct {
	Service   string
	Ports     []int
	Names     []string
	OwnName   string
	Conflicts []struct{ Name, App, Service string }
}

// namesAlwaysUp is a worker's command: sleep until stopped.
var namesAlwaysUp = []string{"sh", "-c", "trap 'exit 0' TERM; while :; do sleep 3600 & wait $!; done"}

// namesAcrossApps (task 2xg2):
//
//  1. Postgres deploys alone in lab062 › production, and its record says other
//     apps reach it at postgres and postgres-postgres on 5432;
//  2. a worker without ports in the same environment reaches it by both names
//     (pg_isready), and its /etc/hosts has the address Nomad registered;
//  3. Postgres's copy is moved (stopped and placed again, a new address): the
//     worker reaches it again, at the new address, without being deployed;
//  4. the same worker in lab062 › staging resolves neither name;
//  5. an app named postgres-postgres claims postgres-postgres too: both records
//     name the other, the worker loses that name and keeps postgres.
func (g *gate) namesAcrossApps(t *testing.T) {
	pgID := namesProject + "--production--postgres"
	clientID := namesProject + "--production--client"
	stagingID := namesProject + "--staging--client"
	rivalID := namesProject + "--production--postgres-postgres"
	cleanup := func() {
		for _, path := range []string{"/v1/apps/" + rivalID, "/v1/apps/" + stagingID, "/v1/apps/" + clientID, "/v1/apps/" + pgID,
			"/v1/projects/" + namesProject + "/environments/staging", "/v1/projects/" + namesProject} {
			if resp, err := g.apiRequest(http.MethodDelete, path, "", ""); err == nil {
				resp.Body.Close()
			}
			if strings.HasPrefix(path, "/v1/apps/") {
				id := strings.TrimPrefix(path, "/v1/apps/")
				waitUntil(time.Minute, func() bool { _, _, err := g.target.Nomad.Jobs().Info(id, nil); return err != nil })
			}
		}
	}
	cleanup() // a leftover from an aborted run, if any
	t.Cleanup(cleanup)
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects", `{"name":"`+namesProject+`"}`)
	g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/projects/"+namesProject+"/environments", `{"name":"staging"}`)

	deploy := func(id, body string) string {
		t.Helper()
		raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", body)
		var d gateDeployment
		if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
			t.Fatalf("deploy %s: %s", id, raw)
		}
		if final := g.waitFinalOf(t, id, d.ID, 5*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
		}
		return d.ID
	}
	addresses := func(id string) []namesAddress {
		t.Helper()
		raw := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/apps/"+id, "")
		var rec struct{ Addresses []namesAddress }
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return rec.Addresses
	}

	// 1. Postgres alone.
	deploy(pgID, namesApp(pgID, "postgres", namesPgImage, 5432))
	addr := addresses(pgID)
	if len(addr) != 1 || addr[0].Service != "postgres" || !slices.Equal(addr[0].Ports, []int{5432}) ||
		!slices.Equal(addr[0].Names, []string{"postgres-postgres", "postgres"}) || addr[0].OwnName != "" || len(addr[0].Conflicts) != 0 {
		t.Fatalf("postgres's addresses = %+v, want postgres-postgres and postgres on 5432", addr)
	}

	// 2. A worker of the same environment, by both names.
	clientDep := deploy(clientID, namesApp(clientID, "client", namesPgImage, 0, namesAlwaysUp...))
	reaches := func(appID, deployment, name string) (string, bool) {
		out, err := g.exec(t, appID, "client", deployment, "pg_isready", "-h", name, "-p", "5432", "-t", "3")
		return out, err == nil && strings.Contains(out, "accepting connections")
	}
	for _, name := range []string{"postgres", "postgres-postgres"} {
		var out string
		if !waitUntil(time.Minute, func() bool { var ok bool; out, ok = reaches(clientID, clientDep, name); return ok }) {
			t.Fatalf("the client can't reach %s:5432: %q", name, out)
		}
	}
	pgAddress := func() string {
		t.Helper()
		regs, _, err := g.target.Nomad.Services().Get(pgID+"-postgres-internal", nil)
		if err != nil || len(regs) != 1 {
			t.Fatalf("postgres's internal registration: %d, %v", len(regs), err)
		}
		if !slices.Contains(regs[0].Tags, "caravana-place="+namesProject+"/production") || !slices.Contains(regs[0].Tags, "caravana-name=postgres") {
			t.Errorf("postgres's internal tags = %v", regs[0].Tags)
		}
		return regs[0].Address
	}
	// hosts reads a copy's /etc/hosts, ending with a newline so that every
	// line, the last too, can be looked for as "<address> <name>\n" (exec's
	// output comes trimmed).
	hosts := func(appID, deployment string) string {
		t.Helper()
		out, err := g.exec(t, appID, "client", deployment, "cat", "/etc/hosts")
		if err != nil {
			t.Fatalf("read %s's /etc/hosts: %v", appID, err)
		}
		return strings.TrimRight(out, "\n") + "\n"
	}
	before := pgAddress()
	if h := hosts(clientID, clientDep); !strings.Contains(h, before+" postgres\n") {
		t.Errorf("the client's /etc/hosts has no %q:\n%s", before+" postgres", h)
	}

	// 3. Postgres's copy moves to a new address; the client follows it.
	stubs, _, err := g.target.Nomad.Jobs().Allocations(pgID, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stubs {
		if s.ClientStatus == "running" {
			if _, err := g.target.Nomad.Allocations().Stop(&api.Allocation{ID: s.ID}, nil); err != nil {
				t.Fatalf("stop postgres's copy: %v", err)
			}
		}
	}
	var after string
	waitFor(t, "postgres's copy at a new address", 3*time.Minute, func() bool {
		regs, _, err := g.target.Nomad.Services().Get(pgID+"-postgres-internal", nil)
		if err != nil || len(regs) != 1 || regs[0].Address == before {
			return false
		}
		after = regs[0].Address
		return true
	})
	var out string
	if !waitUntil(2*time.Minute, func() bool { var ok bool; out, ok = reaches(clientID, clientDep, "postgres"); return ok }) {
		t.Fatalf("the client can't reach postgres after it moved to %s: %q\n%s", after, out, hosts(clientID, clientDep))
	}
	if h := hosts(clientID, clientDep); !strings.Contains(h, after+" postgres\n") || strings.Contains(h, before+" ") {
		t.Errorf("the client's /etc/hosts after the move (was %s, now %s):\n%s", before, after, h)
	}

	// 4. Another environment resolves neither name.
	stagingDep := deploy(stagingID, namesApp(stagingID, "client", namesPgImage, 0, namesAlwaysUp...))
	if h := hosts(stagingID, stagingDep); strings.Contains(h, " postgres") {
		t.Errorf("staging's /etc/hosts names production's postgres:\n%s", h)
	}
	if out, ok := reaches(stagingID, stagingDep, "postgres"); ok {
		t.Errorf("staging reaches production's postgres: %q", out)
	}

	// 5. A second app claims postgres-postgres.
	deploy(rivalID, namesApp(rivalID, "web", "busybox:1.37", 8080, "sh", "-c", "mkdir -p /www; exec httpd -f -p 8080 -h /www"))
	conflictWith := func(id, other string) bool {
		for _, a := range addresses(id) {
			for _, c := range a.Conflicts {
				if c.Name == "postgres-postgres" && c.App == other {
					return true
				}
			}
		}
		return false
	}
	if !conflictWith(pgID, rivalID) || !conflictWith(rivalID, pgID) {
		t.Errorf("addresses don't name the conflict: postgres %+v, rival %+v", addresses(pgID), addresses(rivalID))
	}
	if addr := addresses(pgID); len(addr) != 1 || !slices.Equal(addr[0].Names, []string{"postgres"}) {
		t.Errorf("postgres's names with a rival = %+v, want postgres only", addr)
	}
	waitFor(t, "postgres-postgres to leave the client's /etc/hosts", time.Minute, func() bool {
		return !strings.Contains(hosts(clientID, clientDep), " postgres-postgres\n")
	})
	h := hosts(clientID, clientDep)
	if !strings.Contains(h, after+" postgres\n") || !strings.Contains(h, " postgres-postgres-web\n") {
		t.Errorf("the client's /etc/hosts with a rival (postgres at %s, now registered at %s):\n%s", after, pgAddress(), h)
	}
	if _, ok := reaches(clientID, clientDep, "postgres"); !ok {
		t.Errorf("the client lost postgres when a rival claimed postgres-postgres:\n%s", h)
	}
}
