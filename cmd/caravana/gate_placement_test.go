package main

// The placement gate step (spec 042): a service's placement rules reach the
// Nomad job, a rule no server satisfies is refused naming the field before
// anything is registered, and the servers calls refuse what the one server of
// the lab can't do. The three-server checks (a label on a server, copies
// landing on it, moving work off it) are deploy/dev/cluster-test.sh's. It is
// in its own file only because gate_test.go is long; runGate lists it with
// the other steps.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// placementApp is a one-copy busybox httpd with the placement rules given as
// JSON (an object, or "" for none).
func placementApp(rules string) string {
	svc := map[string]any{
		"name": "web", "image": "busybox:1.37",
		"command": "sh", "args": []string{"-c", "mkdir -p /www; echo placed > /www/index.html; exec httpd -f -p 8080 -h /www"},
		"ports":     []any{map[string]any{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": map[string]any{"memoryMB": 16, "memoryMaxMB": 32},
	}
	if rules != "" {
		svc["placement"] = json.RawMessage(rules)
	}
	body, _ := json.Marshal(map[string]any{"source": map[string]string{"kind": "image"}, "services": []any{svc}})
	return string(body)
}

// placementStep (spec 042):
//
//  1. a label no server has is refused 409 placement_impossible on the rule's
//     field, and no job is registered;
//  2. avoiding a name no server has and preferring a label deploy healthy, and
//     the job's group carries the constraint and the affinity;
//  3. the export plans against the live job with no diff;
//  4. the same impossible rule deployed over the healthy app is refused the
//     same way, and the running copy is untouched (the job's version doesn't
//     move);
//  5. the servers calls: labels on a node Caravana doesn't manage are 404, and
//     moving work off the only server that accepts work is refused (404 for
//     the lab's unmanaged node, 409 no_other_server for a managed first server).
func (g *gate) placementStep(t *testing.T) {
	const appID = "gate-place"
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+appID, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, appID+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(appID, nil)
		return err != nil
	})
	t.Cleanup(remove)
	deployments := "/v1/apps/" + appID + "/deployments"

	// 1. A label nobody has.
	impossible := placementApp(`{"require": {"gpu": "true"}}`)
	msg := g.refusal(t, http.StatusConflict, "placement_impossible", "services[0].placement.require.gpu",
		http.MethodPost, deployments, impossible)
	t.Logf("refused: %s", msg)
	if _, _, err := g.target.Nomad.Jobs().Info(appID, nil); err == nil {
		t.Fatalf("a refused deploy registered %s", appID)
	}

	// 2. Rules every server satisfies.
	status, raw := g.send(t, http.MethodPost, deployments,
		placementApp(`{"avoid": ["nobody"], "prefer": [{"label": "ssd", "value": "true", "weight": 80}]}`))
	var d gateDeployment
	if status != http.StatusAccepted || json.Unmarshal(raw, &d) != nil || d.ID == "" {
		t.Fatalf("deploy %s = %d %s", appID, status, raw)
	}
	if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}
	job, _, err := g.target.Nomad.Jobs().Info(appID, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", appID, err)
	}
	// Nomad adds its own implied constraints (the driver, the kernel, CNI) to
	// a job it reads back, so only Caravana's are looked at.
	tg := job.TaskGroups[0]
	var ours []string
	for _, c := range tg.Constraints {
		if strings.HasPrefix(c.LTarget, "${meta.caravana.") {
			ours = append(ours, c.LTarget+" "+c.Operand+" "+c.RTarget)
		}
	}
	if len(ours) != 1 || ours[0] != "${meta.caravana.server} != nobody" {
		t.Errorf("the group's own constraints = %q, want one ${meta.caravana.server} != nobody", ours)
	}
	if len(tg.Affinities) != 1 || tg.Affinities[0].LTarget != "${meta.caravana.label.ssd}" ||
		tg.Affinities[0].RTarget != "true" || tg.Affinities[0].Weight == nil || *tg.Affinities[0].Weight != 80 {
		t.Errorf("the group's affinities = %+v, want one ${meta.caravana.label.ssd} = true with weight 80", tg.Affinities)
	}
	version := uint64(0)
	if job.Version != nil {
		version = *job.Version
	}

	// 3. The export.
	g.exportMatchesLive(t, appID)

	// 4. The impossible rule over a running app: refused, and nothing moved.
	g.refusal(t, http.StatusConflict, "placement_impossible", "services[0].placement.require.gpu",
		http.MethodPost, deployments, impossible)
	after, _, err := g.target.Nomad.Jobs().Info(appID, nil)
	if err != nil || after.Version == nil || *after.Version != version {
		t.Fatalf("the job's version moved after a refused deploy: %v, %v", after.Version, err)
	}
	var copies struct {
		Copies []struct{ Status string } `json:"copies"`
	}
	g.getJSON(t, "/v1/apps/"+appID+"/copies", &copies)
	healthy := 0
	for _, c := range copies.Copies {
		if c.Status == "healthy" {
			healthy++
		}
	}
	if healthy != 1 {
		t.Errorf("%d healthy copies after the refused deploy, want 1: %+v", healthy, copies.Copies)
	}

	// 5. The servers calls on the one server there is.
	var servers struct {
		Servers []struct {
			Name    string `json:"name"`
			Managed bool   `json:"managed"`
			Labels  map[string]string
		} `json:"servers"`
	}
	g.getJSON(t, "/v1/servers", &servers)
	if len(servers.Servers) == 0 {
		t.Fatal("GET /v1/servers lists no server")
	}
	one := servers.Servers[0]
	labels := `{"labels": {"ssd": "true"}}`
	if one.Managed {
		// A real host: its first server is managed, and the label is written to its node.
		var v struct {
			Labels        map[string]string `json:"labels"`
			LabelsApplied bool              `json:"labelsApplied"`
		}
		g.putJSON(t, "/v1/servers/"+one.Name+"/labels", labels, &v)
		if v.Labels["ssd"] != "true" {
			t.Errorf("labels after the save = %v, want ssd=true", v.Labels)
		}
		waitFor(t, "the label to reach Nomad's servers", 30*time.Second, func() bool {
			g.getJSON(t, "/v1/servers/"+one.Name, &v)
			return v.LabelsApplied
		})
		g.refusal(t, http.StatusConflict, "no_other_server", "", http.MethodPost, "/v1/servers/"+one.Name+"/drain", `{}`)
		g.putJSON(t, "/v1/servers/"+one.Name+"/labels", `{"labels": {}}`, &v)
	} else {
		// The lab: a Nomad node with no row is listed and can't be labelled or emptied.
		g.refusal(t, http.StatusNotFound, "server_not_found", "", http.MethodPut, "/v1/servers/"+one.Name+"/labels", labels)
		g.refusal(t, http.StatusNotFound, "server_not_found", "", http.MethodPost, "/v1/servers/"+one.Name+"/drain", `{}`)
	}
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "labels", http.MethodPut,
		"/v1/servers/"+one.Name+"/labels", `{"labels": {"Not A Label": "x"}}`)
}

// putJSON sends a PUT with a JSON body and reads the JSON answer, which must be 200.
func (g *gate) putJSON(t *testing.T, path, body string, into any) {
	t.Helper()
	status, raw := g.send(t, http.MethodPut, path, body)
	if status != http.StatusOK {
		t.Fatalf("PUT %s = %d %s", path, status, raw)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("PUT %s answered %s: %v", path, raw, err)
	}
}
