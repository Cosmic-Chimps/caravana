package main

// The space gate step (feature 045, reclaim space): the orphan volumes of a server and their deletion, through
// the API with the gate's token and the real Nomad. The lab's node has no agent, so the images side (what a
// clean-up removes) is deploy/dev/install-test.sh's check_space_host; here the step checks that the API says
// so plainly. It is in its own file only because gate_test.go is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// spaceApp is a one-copy busybox httpd with the volumes given (names), each mounted at /<name>. Its command
// writes a marker (the volume's first letter, capitalised) into each volume once and serves the volume keep on
// 8080, so the health check (GET /v) passes once the marker is there. broken makes it run without serving
// anything: its check never passes, and Nomad fails the deployment only at its 10 minute progress deadline
// (an unhealthy copy is rescheduled until then, as brokenReverts finds), then reverts it.
func spaceApp(volumes []string, broken bool) string {
	script := ""
	var vols []any
	for _, v := range volumes {
		script += "[ -f /" + v + "/v ] || printf " + strings.ToUpper(v[:1]) + " > /" + v + "/v; "
		vols = append(vols, map[string]string{"name": v, "path": "/" + v})
	}
	script += "exec httpd -f -p 8080 -h /keep"
	if broken {
		script = "exec sleep 1d"
	}
	body, _ := json.Marshal(map[string]any{"source": map[string]string{"kind": "image"}, "services": []any{map[string]any{
		"name": "web", "image": "busybox:1.37", "command": "sh", "args": []string{"-c", script},
		"ports":     []any{map[string]any{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/v", "interval": "5s", "timeout": "2s"},
		"resources": map[string]any{"memoryMB": 16, "memoryMaxMB": 32},
		"volumes":   vols,
	}}})
	return string(body)
}

// gateOrphan and gateSpace are the parts of ServerSpace the step reads.
type gateOrphan struct {
	Name           string  `json:"name"`
	App            string  `json:"app"`
	AppExists      bool    `json:"appExists"`
	Bytes          *int64  `json:"bytes"`
	LastDeployedAt *string `json:"lastDeployedAt"`
	InUse          bool    `json:"inUse"`
}

type gateSpace struct {
	Server            string          `json:"server"`
	Managed           bool            `json:"managed"`
	Images            json.RawMessage `json:"images"`
	ImagesUnavailable string          `json:"imagesUnavailable"`
	DockerVolumes     json.RawMessage `json:"dockerVolumes"`
	BuildCacheBytes   json.RawMessage `json:"buildCacheBytes"`
	OrphanVolumes     []gateOrphan    `json:"orphanVolumes"`
}

// gateDeletion is a VolumeDeletion.
type gateDeletion struct {
	ID            string `json:"id"`
	Volume        string `json:"volume"`
	Status        string `json:"status"`
	BackUpFirst   bool   `json:"backUpFirst"`
	ArchiveObject string `json:"archiveObject"`
	Error         string `json:"error"`
}

// spaceStep (feature 045):
//
//  1. an app with two volumes (keep, scratch) is deployed healthy, then a broken deployment that drops scratch
//     is reverted: Nomad runs the first version again, which mounts scratch, so the volume no app declares is
//     "in use" and its deletion is refused 409 volume_in_use;
//  2. the deployment that dropped scratch warned that its data stays on the server, and a healthy one without
//     scratch follows;
//  3. the server's space read: the lab's node is not managed (no agent: images, Docker's volumes and the
//     cache are null), scratch is listed as an orphan of an app that exists, not measured, with the time it
//     was last deployed with it; Clean up now is 409 no_agent;
//  4. the deletion's refusals: a wrong name is 400 at confirm, the declared volume is 409 volume_not_orphan,
//     an unknown volume 404 volume_not_found, an unknown deletion 404 deletion_not_found;
//  5. with the bucket as the backup destination, scratch is deleted with backUpFirst: 202 with a Location,
//     archiving, then done with the archive's object, which is in the bucket and holds scratch's file; the
//     volume is gone from Nomad and from the read, and keep's data is still there;
//  6. the activity feed says it, and the clean-up settings round-trip.
func (g *gate) spaceStep(t *testing.T) {
	const appID = "gate-space"
	const keepVol, orphanVol = appID + "_keep", appID + "_scratch"
	nomad := g.target.Nomad
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+appID, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, appID+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := nomad.Jobs().Info(appID, nil)
		return err != nil
	})
	t.Cleanup(remove)
	var before struct {
		Automatic       bool `json:"automatic"`
		KeepDeployments int  `json:"keepDeployments"`
	}
	g.getJSON(t, "/v1/cleanup/settings", &before)
	t.Cleanup(func() { // the setting, as the step found it
		body, _ := json.Marshal(before)
		if resp, err := g.apiRequest(http.MethodPut, "/v1/cleanup/settings", "application/json", string(body)); err == nil {
			resp.Body.Close()
		}
	})
	volumesOf := func() []string {
		t.Helper()
		stubs, _, err := nomad.HostVolumes().List(&api.HostVolumeListRequest{}, &api.QueryOptions{Prefix: appID + "_"})
		if err != nil {
			t.Fatalf("list Nomad's host volumes: %v", err)
		}
		var names []string
		for _, s := range stubs {
			names = append(names, s.Name)
		}
		return names
	}
	deployments := "/v1/apps/" + appID + "/deployments"
	deployWith := func(body string) gateDeployment {
		t.Helper()
		status, raw := g.send(t, http.MethodPost, deployments, body)
		var d gateDeployment
		if status != http.StatusAccepted || json.Unmarshal(raw, &d) != nil || d.ID == "" {
			t.Fatalf("deploy %s = %d %s", appID, status, raw)
		}
		return d
	}

	var cluster struct {
		Servers []struct {
			Name string `json:"name"`
		} `json:"servers"`
	}
	g.getJSON(t, "/v1/servers", &cluster)
	if len(cluster.Servers) != 1 {
		t.Fatalf("GET /v1/servers has %d servers, want the lab's one", len(cluster.Servers))
	}
	server := cluster.Servers[0].Name
	spacePath := "/v1/servers/" + server + "/space"
	readSpace := func() gateSpace {
		t.Helper()
		var sp gateSpace
		g.getJSON(t, spacePath, &sp)
		return sp
	}
	orphan := func(sp gateSpace, name string) *gateOrphan {
		for i := range sp.OrphanVolumes {
			if sp.OrphanVolumes[i].Name == name {
				return &sp.OrphanVolumes[i]
			}
		}
		return nil
	}

	// 1. Both volumes; then a broken deployment that drops scratch, reverted.
	first := deployWith(spaceApp([]string{"keep", "scratch"}, false))
	if final := g.waitFinalOf(t, appID, first.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}
	if got := volumesOf(); len(got) != 2 {
		t.Fatalf("Nomad's volumes of %s = %v, want %s and %s", appID, got, keepVol, orphanVol)
	}
	if o := orphan(readSpace(), orphanVol); o != nil {
		t.Fatalf("%s is listed as an orphan while the app declares it: %+v", orphanVol, o)
	}
	broken := deployWith(spaceApp([]string{"keep"}, true))
	if !waitUntil(13*time.Minute, func() bool {
		var d gateDeployment
		g.getJSON(t, deployments+"/"+broken.ID, &d)
		return d.Status != "pending" && d.Status != "running"
	}) {
		g.dumpJob(t, appID)
		t.Fatalf("the broken deployment %s isn't final after 13 minutes", broken.ID)
	}
	if final := g.waitFinalOf(t, appID, broken.ID, time.Minute); final.Status != "reverted" {
		g.dumpJob(t, appID)
		t.Fatalf("the broken deploy ended %s (%s), want reverted", final.Status, final.Error)
	}
	// The first version runs again and holds scratch.
	var inUse *gateOrphan
	waitFor(t, orphanVol+" to be in use by the reverted copy", 3*time.Minute, func() bool {
		inUse = orphan(readSpace(), orphanVol)
		return inUse != nil && inUse.InUse
	})
	if inUse.App != appID || !inUse.AppExists {
		t.Errorf("%s = %+v, want the orphan of the app %s, which exists", orphanVol, inUse, appID)
	}
	msg := g.refusal(t, http.StatusConflict, "volume_in_use", "name",
		http.MethodPost, "/v1/volumes/"+orphanVol+"/deletions", `{"confirm":"`+orphanVol+`"}`)
	t.Logf("refused: %s", msg)
	if got := volumesOf(); len(got) != 2 {
		t.Fatalf("a refused deletion changed Nomad's volumes: %v", got)
	}

	// 2. The deployment that dropped scratch warned that its data stays on the server (it was reverted, but its
	// record keeps the warning). A healthy deployment after it, without scratch too, has nothing new to say.
	var dep struct {
		Warnings []struct {
			Message string `json:"message"`
		} `json:"warnings"`
	}
	g.getJSON(t, deployments+"/"+broken.ID, &dep)
	found := false
	for _, w := range dep.Warnings {
		if strings.Contains(w.Message, `volume "scratch" is no longer mounted: its data stays on server `+server) {
			found = true
		}
	}
	if !found {
		t.Errorf("the deployment that dropped scratch has warnings %+v, want one that says its data stays on server %s", dep.Warnings, server)
	}
	third := deployWith(spaceApp([]string{"keep"}, false))
	if final := g.waitFinalOf(t, appID, third.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s without scratch ended %s: %s", appID, final.Status, final.Error)
	}

	// 3. The space read of a node with no agent.
	var o *gateOrphan
	waitFor(t, orphanVol+" to be listed and free", time.Minute, func() bool {
		o = orphan(readSpace(), orphanVol)
		return o != nil && !o.InUse
	})
	sp := readSpace()
	if sp.Managed || string(sp.Images) != "null" || sp.ImagesUnavailable != "no agent" ||
		string(sp.DockerVolumes) != "null" || string(sp.BuildCacheBytes) != "null" {
		t.Errorf("the space of the lab's node = %+v, want managed false, no images, Docker's volumes or cache, and \"no agent\"", sp)
	}
	if o.App != appID || !o.AppExists || o.Bytes != nil || o.LastDeployedAt == nil {
		t.Errorf("%s = %+v, want app %s (it exists), not measured, with the time it was last deployed with it", orphanVol, o, appID)
	}
	if orphan(sp, keepVol) != nil {
		t.Errorf("%s is listed as an orphan while the app declares it", keepVol)
	}
	msg = g.refusal(t, http.StatusConflict, "no_agent", "", http.MethodPost, "/v1/servers/"+server+"/cleanups", "")
	t.Logf("refused: %s", msg)
	g.refusal(t, http.StatusConflict, "no_agent", "", http.MethodPost, "/v1/servers/"+server+"/cleanups", `{"dockerVolumes":true}`)
	g.refusal(t, http.StatusNotFound, "server_not_found", "", http.MethodGet, "/v1/servers/nobody/space", "")

	// 4. The deletion's refusals.
	del := "/v1/volumes/" + orphanVol + "/deletions"
	g.refusal(t, http.StatusBadRequest, "invalid_field", "confirm", http.MethodPost, del, `{"confirm":"scratch"}`)
	g.refusal(t, http.StatusBadRequest, "invalid_field", "confirm", http.MethodPost, del, `{}`)
	msg = g.refusal(t, http.StatusConflict, "volume_not_orphan", "name",
		http.MethodPost, "/v1/volumes/"+keepVol+"/deletions", `{"confirm":"`+keepVol+`"}`)
	t.Logf("refused: %s", msg)
	g.refusal(t, http.StatusNotFound, "volume_not_found", "", http.MethodPost, "/v1/volumes/"+appID+"_nothing/deletions",
		`{"confirm":"`+appID+`_nothing"}`)
	g.refusal(t, http.StatusNotFound, "deletion_not_found", "id", http.MethodGet, del+"/"+strings.Repeat("0", 32), "")
	if got := volumesOf(); len(got) != 2 {
		t.Fatalf("the refused deletions changed Nomad's volumes: %v", got)
	}

	// 5. Delete scratch, backing it up first, to the bucket.
	g.startBucket(t)
	resp := g.api(t, http.MethodPost, del, "application/json", `{"confirm":"`+orphanVol+`","backUpFirst":true}`)
	location := resp.Header.Get("Location")
	var started gateDeletion
	_ = json.NewDecoder(resp.Body).Decode(&started)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || started.ID == "" || started.Status != "archiving" || !started.BackUpFirst ||
		location != del+"/"+started.ID {
		t.Fatalf("POST %s with backUpFirst = %d %+v → %q, want 202 archiving with its Location", del, resp.StatusCode, started, location)
	}
	g.refusal(t, http.StatusConflict, "deletion_running", "name", http.MethodPost, del, `{"confirm":"`+orphanVol+`"}`)
	var done gateDeletion
	waitFor(t, "the deletion to end", 3*time.Minute, func() bool {
		g.getJSON(t, location, &done)
		return done.Status == "done" || done.Status == "failed"
	})
	if done.Status != "done" || done.ArchiveObject == "" || done.Volume != orphanVol {
		t.Fatalf("the deletion ended %+v, want done with an archive", done)
	}
	if !strings.HasPrefix(done.ArchiveObject, "backups/gate/"+appID+"/volumes/") || !strings.HasSuffix(done.ArchiveObject, "/"+orphanVol+".tar.gz") {
		t.Errorf("archiveObject = %q, want backups/gate/%s/volumes/<time>/%s.tar.gz", done.ArchiveObject, appID, orphanVol)
	}
	if listed, err := g.bucketList("/data/" + path.Dir(done.ArchiveObject)); err != nil || !strings.Contains(listed, orphanVol+".tar.gz") {
		t.Errorf("the bucket's %s lists %q (%v), want %s.tar.gz", path.Dir(done.ArchiveObject), listed, err, orphanVol)
	}
	if files, err := g.bucketExec("tar", "tzf", "/data/"+done.ArchiveObject); err != nil || !strings.Contains(files, "v") {
		t.Errorf("the archive holds %q (%v), want scratch's file v", files, err)
	}
	waitFor(t, orphanVol+" to be gone from Nomad", 30*time.Second, func() bool { return len(volumesOf()) == 1 })
	if got := volumesOf(); got[0] != keepVol {
		t.Errorf("Nomad's volumes after the deletion = %v, want only %s", got, keepVol)
	}
	if orphan(readSpace(), orphanVol) != nil {
		t.Errorf("%s is still listed after its deletion", orphanVol)
	}
	if left, _, err := nomad.Jobs().List(&api.QueryOptions{Prefix: orphanVol + "_archive"}); err != nil || len(left) != 0 {
		t.Errorf("archive jobs left after the deletion: %d (%v)", len(left), err)
	}
	if left, _, err := nomad.Variables().PrefixList("nomad/jobs/"+orphanVol+"_archive", nil); err != nil || len(left) != 0 {
		t.Errorf("archive variables left after the deletion: %d (%v)", len(left), err)
	}
	if got, err := g.exec(t, appID, "web", third.ID, "cat", "/keep/v"); err != nil || got != "K" {
		t.Errorf("keep's marker = %q, %v, want K: the other volume's data is untouched", got, err)
	}

	// 6. The feed and the setting.
	var feed struct {
		Entries []struct{ Message string } `json:"entries"`
	}
	waitFor(t, "the activity feed to have the deletion", 10*time.Second, func() bool {
		g.getJSON(t, "/v1/activity?limit=50", &feed)
		for _, e := range feed.Entries {
			if strings.Contains(e.Message, "Deleted the volume "+orphanVol) {
				return true
			}
		}
		return false
	})
	g.refusal(t, http.StatusBadRequest, "invalid_field", "keepDeployments", http.MethodPut, "/v1/cleanup/settings", `{"automatic":true,"keepDeployments":11}`)
	g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/cleanup/settings", `{"automatic":false,"keepDeployments":5}`)
	var saved struct {
		Automatic       bool `json:"automatic"`
		KeepDeployments int  `json:"keepDeployments"`
	}
	g.getJSON(t, "/v1/cleanup/settings", &saved)
	if saved.Automatic || saved.KeepDeployments != 5 {
		t.Errorf("GET /v1/cleanup/settings = %+v, want the daily run off and 5 deployments", saved)
	}
	g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/cleanup/settings", `{"automatic":true,"keepDeployments":3}`)
	g.getJSON(t, "/v1/cleanup/settings", &saved)
	if !saved.Automatic || saved.KeepDeployments != 3 {
		t.Errorf("GET /v1/cleanup/settings = %+v after putting it back, want on and 3", saved)
	}
}

// dumpJob logs what Nomad says of a job's copies and its latest deployment, for a step that waited in vain.
func (g *gate) dumpJob(t *testing.T, jobID string) {
	t.Helper()
	nomad := g.target.Nomad
	if d, _, err := nomad.Jobs().LatestDeployment(jobID, nil); err == nil && d != nil {
		t.Logf("latest Nomad deployment of %s: %s (%s)", jobID, d.Status, d.StatusDescription)
	}
	allocs, _, err := nomad.Jobs().Allocations(jobID, true, nil)
	if err != nil {
		t.Logf("list the copies of %s: %v", jobID, err)
		return
	}
	for _, a := range allocs {
		full, _, err := nomad.Allocations().Info(a.ID, nil)
		if err != nil {
			continue
		}
		t.Logf("copy %s version %d: client %s, desired %s, healthy %v", a.ID[:8], a.JobVersion, a.ClientStatus, a.DesiredStatus,
			full.DeploymentStatus != nil && full.DeploymentStatus.Healthy != nil && *full.DeploymentStatus.Healthy)
		for task, st := range full.TaskStates {
			for _, ev := range st.Events {
				t.Logf("  %s: %s %s %s", task, ev.Type, ev.DisplayMessage, ev.Message)
			}
		}
	}
}
