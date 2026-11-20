package api

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nodeagent"
)

// spaceAnswer is the part of schema ServerSpace the tests read.
type spaceAnswer struct {
	Server            string `json:"server"`
	Managed           bool   `json:"managed"`
	ReclaimableBytes  int64  `json:"reclaimableBytes"`
	ImagesUnavailable string `json:"imagesUnavailable"`
	Images            *struct {
		Count       int   `json:"count"`
		UnusedCount int   `json:"unusedCount"`
		UnusedBytes int64 `json:"unusedBytes"`
	} `json:"images"`
	DockerVolumes   *struct{ Count, Bytes int64 } `json:"dockerVolumes"`
	BuildCacheBytes *int64                        `json:"buildCacheBytes"`
	OrphanVolumes   []struct {
		Name        string `json:"name"`
		ID          string `json:"id"`
		App         string `json:"app"`
		Project     string `json:"project"`
		Environment string `json:"environment"`
		AppExists   bool   `json:"appExists"`
		Bytes       *int64 `json:"bytes"`
		InUse       bool   `json:"inUse"`
	} `json:"orphanVolumes"`
	OrphansUnavailable bool           `json:"orphanVolumesUnavailable"`
	LastCleanup        *cleanupAnswer `json:"lastCleanup"`
	OpenCleanup        *cleanupAnswer `json:"openCleanup"`
	Settings           struct {
		Automatic       bool `json:"automatic"`
		KeepDeployments int  `json:"keepDeployments"`
	} `json:"settings"`
}

type cleanupAnswer struct {
	ID            string `json:"id"`
	Server        string `json:"server"`
	Trigger       string `json:"trigger"`
	DockerVolumes bool   `json:"dockerVolumes"`
	Status        string `json:"status"`
	ImagesRemoved int    `json:"imagesRemoved"`
	BytesFreed    int64  `json:"bytesFreed"`
	// VolumesRemoved is how many of Docker's leftover volumes the agent pruned.
	VolumesRemoved int    `json:"volumesRemoved"`
	Error          string `json:"error"`
}

type deletionAnswer struct {
	ID          string  `json:"id"`
	Volume      string  `json:"volume"`
	App         string  `json:"app"`
	Server      string  `json:"server"`
	BackUpFirst bool    `json:"backUpFirst"`
	Status      string  `json:"status"`
	FinishedAt  *string `json:"finishedAt"`
	// ArchiveObject is where the archive is, once there is one.
	ArchiveObject string `json:"archiveObject"`
	Error         string `json:"error"`
}

// withShop stores an app shop that declares the volume uploads, and gives Nomad the volumes of every kind
// on the first server's node: one a definition declares, one its app no longer declares and one whose app
// was deleted.
func withShop(t *testing.T, e *testEnv) {
	t.Helper()
	shop := app.App{ID: "shop", Name: "shop", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{Name: "web", Image: "traefik/whoami:latest", Volumes: []app.Volume{{Name: "uploads", Path: "/u"}}}}}
	if _, err := e.Store.SaveApp(context.Background(), shop, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shop_uploads", "shop_cache", "gone_data"} {
		e.Nomad.AddHostVolumeOn(name, "node-1")
	}
}

// getSpace reads GET /v1/servers/{name}/space, checking its status, header and schema.
func getSpace(t *testing.T, e *testEnv, name string) spaceAnswer {
	t.Helper()
	status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/servers/"+name+"/space", "", "")
	if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("getServerSpace(%s) = %d (Cache-Control %q) %s", name, status, hdr.Get("Cache-Control"), raw)
	}
	checkSchema(t, loadContract(t), raw, "ServerSpace")
	return decodeAs[spaceAnswer](t, raw)
}

func TestGetServerSpace(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	withShop(t, e)

	t.Run("a server with no agent report: images unknown, orphans listed", func(t *testing.T) {
		v := getSpace(t, e, "hel-1")
		if v.Server != "hel-1" || !v.Managed || v.Images != nil || v.ImagesUnavailable != "no agent" ||
			v.DockerVolumes != nil || v.BuildCacheBytes != nil || v.OrphansUnavailable || v.LastCleanup != nil || v.OpenCleanup != nil {
			t.Errorf("space = %+v", v)
		}
		if !v.Settings.Automatic || v.Settings.KeepDeployments != 3 {
			t.Errorf("settings = %+v, want the defaults", v.Settings)
		}
		var names []string
		for _, o := range v.OrphanVolumes {
			names = append(names, o.Name)
			if o.Bytes != nil || o.InUse || o.ID == "" {
				t.Errorf("orphan %+v: want no size yet, not in use, with an ID", o)
			}
		}
		if !slices.Equal(names, []string{"gone_data", "shop_cache"}) {
			t.Errorf("orphans = %v, want gone_data and shop_cache (shop_uploads is declared)", names)
		}
		for _, o := range v.OrphanVolumes {
			switch o.Name {
			case "shop_cache":
				if o.App != "shop" || !o.AppExists || o.Project != "default" || o.Environment != "production" {
					t.Errorf("shop_cache = %+v", o)
				}
			case "gone_data":
				if o.App != "gone" || o.AppExists {
					t.Errorf("gone_data = %+v: its app was deleted", o)
				}
			}
		}
	})

	t.Run("a copy that claims a volume marks it in use", func(t *testing.T) {
		e.Nomad.ClaimHostVolume("gone_data", "alloc-1")
		defer e.Nomad.ClaimHostVolume("gone_data")
		for _, o := range getSpace(t, e, "hel-1").OrphanVolumes {
			if (o.Name == "gone_data") != o.InUse {
				t.Errorf("%s in use = %v", o.Name, o.InUse)
			}
		}
	})

	t.Run("a Nomad node Caravana doesn't manage has no agent and no orphans of its own", func(t *testing.T) {
		e.Nomad.AddNode("node-lab", "lab", "ready", "")
		v := getSpace(t, e, "lab")
		if v.Managed || v.Images != nil || v.ImagesUnavailable != "no agent" || len(v.OrphanVolumes) != 0 {
			t.Errorf("space = %+v", v)
		}
	})

	t.Run("an unknown server is 404", func(t *testing.T) {
		status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/servers/nobody/space", "", "")
		wantError(t, "space of nobody", status, raw, http.StatusNotFound, codeServerNotFound, "")
		if hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("an error is cached: %q", hdr.Get("Cache-Control"))
		}
	})
}

// The router can't hold GET /v1/servers/joins/{id} beside GET /v1/servers/{name}/space and /cleanups, so
// handler dispatches the last two under one pattern. Every path must still answer what its operation says.
func TestServerSubroutesKeepTheirAnswers(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	j := addServer(t, e, `{"name": "later"}`)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/servers/joins/"+j.Server.ID, "", "")
	if status != http.StatusOK || decodeAs[serverAnswer](t, raw).State != "waiting" {
		t.Errorf("getServerJoin = %d %s", status, raw)
	}
	for _, path := range []string{"/v1/servers/joins/space", "/v1/servers/joins/cleanups"} {
		status, _, raw = call(t, http.MethodGet, e.URL+path, "", "")
		wantError(t, "GET "+path, status, raw, http.StatusNotFound, codeServerNotFound, "")
	}
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/hel-1/nothing", "", "")
	wantError(t, "an unknown sub-path", status, raw, http.StatusNotFound, codeNotFound, "")
	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/servers/hel-1/space", "", "")
	wantError(t, "DELETE space", status, raw, http.StatusMethodNotAllowed, codeMethodNotAllowed, "")
	if status, _, _ = call(t, http.MethodGet, e.URL+"/v1/servers/hel-1/space", "", ""); status != http.StatusOK {
		t.Errorf("getServerSpace = %d", status)
	}
	if status, _, _ = call(t, http.MethodGet, e.URL+"/v1/servers/hel-1/cleanups", "", ""); status != http.StatusOK {
		t.Errorf("listServerCleanups = %d", status)
	}
}

// spaceAgent joins a server called name whose fake Docker holds the images of spaceHost, with its Nomad
// node ready and its first reports in, and returns the agent and its host.
func spaceAgent(t *testing.T, e *testEnv, name string) (*nodeagent.Agent, *spaceHost, *bytes.Buffer) {
	t.Helper()
	j := addServer(t, e, `{"name": "`+name+`"}`)
	host := &spaceHost{fakeHost: fakeHost{hostname: name + ".example.com"}}
	var stdout, stderr bytes.Buffer
	agent := &nodeagent.Agent{
		Root: t.TempDir(), Runner: host, HTTP: nodeagent.NewHTTPClient(false),
		Version: "0.0.1-e2e", NomadAddr: localNomad(t).URL, Stdout: &stdout, Stderr: &stderr,
	}
	ctx := context.Background()
	if err := agent.Join(ctx, nodeagent.JoinOptions{URL: e.RawURL, Token: j.Token, Address: "203.0.113.9"}); err != nil {
		t.Fatalf("Join: %v\n%s", err, stderr.String())
	}
	e.Nomad.AddNode("node-2", name, "ready", "10.99.0.2")
	for range 2 { // the first sync finds no Nomad node, the second marks it ready
		if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil {
			t.Fatalf("Sync: %v\n%s", err, stderr.String())
		}
	}
	return agent, host, &stdout
}

func TestServerCleanupOperations(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	doc := loadContract(t)
	agent, host, _ := spaceAgent(t, e, "fsn-e2e")
	ctx := context.Background()
	// Docker holds five local volumes and a container uses two: three are leftovers, which only a
	// clean-up that asks for them removes (feature 045, US5).
	host.setVolumes(5, 2)
	if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	t.Run("the space read has the agent's inventory", func(t *testing.T) {
		v := getSpace(t, e, "fsn-e2e")
		if v.DockerVolumes == nil || v.DockerVolumes.Count != 3 {
			t.Errorf("leftover volumes = %+v, want 3", v.DockerVolumes)
		}
		if v.Images == nil || v.Images.Count != 3 || v.Images.UnusedCount != 2 || v.Images.UnusedBytes != 400_000_000 || v.ImagesUnavailable != "" ||
			v.DockerVolumes == nil || v.BuildCacheBytes == nil || v.ReclaimableBytes < 400_000_000 {
			t.Errorf("space = %+v images %+v", v, v.Images)
		}
	})

	t.Run("a server with no agent can't be cleaned", func(t *testing.T) {
		status, hdr, raw := call(t, http.MethodPost, e.URL+"/v1/servers/hel-1/cleanups", "", "")
		wantError(t, "clean hel-1", status, raw, http.StatusConflict, codeNoAgent, "name")
		if hdr.Get("Cache-Control") != "no-store" || !strings.Contains(string(raw), "run the installer on it") {
			t.Errorf("answer = %s (Cache-Control %q)", raw, hdr.Get("Cache-Control"))
		}
	})

	t.Run("an unknown server is 404", func(t *testing.T) {
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/v1/servers/nobody/cleanups"},
			{http.MethodGet, "/v1/servers/nobody/cleanups"},
			{http.MethodGet, "/v1/servers/nobody/cleanups/abc"},
		} {
			status, _, raw := call(t, c.method, e.URL+c.path, "", "")
			wantError(t, c.method+" "+c.path, status, raw, http.StatusNotFound, codeServerNotFound, "")
		}
	})

	t.Run("a bad body is refused before anything is requested", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/servers/fsn-e2e/cleanups", "application/json", `{"dockerVolume": true}`)
		wantError(t, "an unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "dockerVolume")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/fsn-e2e/cleanups", "", "")
		if status != http.StatusOK || len(decodeAs[struct {
			Cleanups []cleanupAnswer `json:"cleanups"`
		}](t, raw).Cleanups) != 0 {
			t.Errorf("list = %d %s, want none", status, raw)
		}
	})

	var c cleanupAnswer
	t.Run("a clean-up is requested", func(t *testing.T) {
		status, hdr, raw := call(t, http.MethodPost, e.URL+"/v1/servers/fsn-e2e/cleanups", "application/json", `{"dockerVolumes": true}`)
		if status != http.StatusAccepted || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("cleanServer = %d (Cache-Control %q) %s", status, hdr.Get("Cache-Control"), raw)
		}
		checkSchema(t, doc, raw, "Cleanup")
		c = decodeAs[cleanupAnswer](t, raw)
		if c.Status != "requested" || c.Server != "fsn-e2e" || c.Trigger != "owner" || !c.DockerVolumes || len(c.ID) != 32 ||
			hdr.Get("Location") != "/v1/servers/fsn-e2e/cleanups/"+c.ID {
			t.Errorf("clean-up = %+v, Location %q", c, hdr.Get("Location"))
		}
		if v := getSpace(t, e, "fsn-e2e"); v.OpenCleanup == nil || v.OpenCleanup.ID != c.ID || v.LastCleanup != nil {
			t.Errorf("space open = %+v last = %+v", v.OpenCleanup, v.LastCleanup)
		}
	})

	t.Run("a second one while it is open is refused", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/servers/fsn-e2e/cleanups", "", "")
		wantError(t, "a second clean-up", status, raw, http.StatusConflict, codeCleanupRunning, "name")
	})

	t.Run("reads", func(t *testing.T) {
		status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/servers/fsn-e2e/cleanups/"+c.ID, "", "")
		if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" || decodeAs[cleanupAnswer](t, raw).ID != c.ID {
			t.Errorf("getServerCleanup = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Cleanup")
		status, hdr, raw = call(t, http.MethodGet, e.URL+"/v1/servers/fsn-e2e/cleanups", "", "")
		if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("listServerCleanups = %d %s", status, raw)
		}
		list := decodeAs[struct {
			Cleanups []cleanupAnswer `json:"cleanups"`
		}](t, raw)
		if len(list.Cleanups) != 1 || list.Cleanups[0].ID != c.ID {
			t.Errorf("list = %s", raw)
		}
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/fsn-e2e/cleanups/nope", "", "")
		wantError(t, "an unknown clean-up", status, raw, http.StatusNotFound, codeCleanupNotFound, "id")
		// Another server's ID is not found here, either.
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/hel-1/cleanups/"+c.ID, "", "")
		wantError(t, "another server's clean-up", status, raw, http.StatusNotFound, codeCleanupNotFound, "id")
	})

	t.Run("the agent runs it and the record settles", func(t *testing.T) {
		for range 2 { // handed out and run, then reported
			if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if len(host.removed) != 2 {
			t.Errorf("removed = %v", host.removed)
		}
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/servers/fsn-e2e/cleanups/"+c.ID, "", "")
		checkSchema(t, doc, raw, "Cleanup")
		got := decodeAs[cleanupAnswer](t, raw)
		if status != http.StatusOK || got.Status != "done" || got.ImagesRemoved != 2 || got.BytesFreed != 400_000_000 {
			t.Errorf("settled = %d %+v", status, got)
		}
		// The request's dockerVolumes reached the agent, which pruned, and the count came back.
		if host.pruned != 1 || got.VolumesRemoved != 3 || !got.DockerVolumes {
			t.Errorf("prunes %d, volumesRemoved %d, dockerVolumes %v: want one prune, 3 removed", host.pruned, got.VolumesRemoved, got.DockerVolumes)
		}
		if v := getSpace(t, e, "fsn-e2e"); v.OpenCleanup != nil || v.LastCleanup == nil || v.LastCleanup.ID != c.ID {
			t.Errorf("space open = %+v last = %+v", v.OpenCleanup, v.LastCleanup)
		}
	})

	t.Run("a clean-up that doesn't ask leaves Docker's volumes", func(t *testing.T) {
		host.setVolumes(4, 2) // two more leftovers appear
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/servers/fsn-e2e/cleanups", "application/json", `{}`)
		if status != http.StatusAccepted {
			t.Fatalf("cleanServer = %d %s", status, raw)
		}
		second := decodeAs[cleanupAnswer](t, raw)
		if second.DockerVolumes {
			t.Error("a clean-up that didn't ask has dockerVolumes set")
		}
		for range 2 { // handed out and run, then reported
			if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/servers/fsn-e2e/cleanups/"+second.ID, "", "")
		got := decodeAs[cleanupAnswer](t, raw)
		if got.Status != "done" || got.VolumesRemoved != 0 || host.pruned != 1 {
			t.Errorf("settled = %+v, prunes %d: want done with no volume pruned", got, host.pruned)
		}
		if v := getSpace(t, e, "fsn-e2e"); v.DockerVolumes == nil || v.DockerVolumes.Count != 2 {
			t.Errorf("leftover volumes after = %+v, want the 2 still there", v.DockerVolumes)
		}
	})
}

func TestDeleteOrphanVolume(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	withShop(t, e)
	doc := loadContract(t)
	del := func(name, body string) (int, http.Header, []byte) {
		return call(t, http.MethodPost, e.URL+"/v1/volumes/"+name+"/deletions", "application/json", body)
	}

	t.Run("the name must be typed", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"confirm": ""}`, `{"confirm": "shop_cach"}`} {
			status, hdr, raw := del("shop_cache", body)
			wantError(t, "confirm "+body, status, raw, http.StatusBadRequest, codeInvalidField, "confirm")
			if hdr.Get("Cache-Control") != "no-store" {
				t.Errorf("an error is cached: %q", hdr.Get("Cache-Control"))
			}
		}
		status, _, raw := del("shop_cache", `{"confirm": "shop_cache", "backup": true}`)
		wantError(t, "an unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "backup")
		if !slices.Contains(e.Nomad.HostVolumes(), "shop_cache") {
			t.Fatal("a refused deletion removed the volume")
		}
	})

	t.Run("refusals", func(t *testing.T) {
		status, _, raw := del("nothing_here", `{"confirm": "nothing_here"}`)
		wantError(t, "an unknown volume", status, raw, http.StatusNotFound, codeVolumeNotFound, "name")

		status, _, raw = del("shop_uploads", `{"confirm": "shop_uploads"}`)
		got := wantError(t, "a declared volume", status, raw, http.StatusConflict, codeVolumeNotOrphan, "name")
		if !strings.Contains(got.Error.Message, "App shop declares the volume") {
			t.Errorf("message = %q", got.Error.Message)
		}

		e.Nomad.ClaimHostVolume("gone_data", "alloc-1")
		status, _, raw = del("gone_data", `{"confirm": "gone_data"}`)
		wantError(t, "a volume in use", status, raw, http.StatusConflict, codeVolumeInUse, "name")
		e.Nomad.ClaimHostVolume("gone_data")

		// Without a backup destination the archive can't be made, and nothing is deleted.
		status, _, raw = del("shop_cache", `{"confirm": "shop_cache", "backUpFirst": true}`)
		wantError(t, "back up first with no destination", status, raw, http.StatusConflict, codeNoBackupDestination, "")

		if got := e.Nomad.HostVolumes(); len(got) != 3 {
			t.Errorf("volumes = %v, want all three still there", got)
		}
	})

	var d deletionAnswer
	t.Run("an orphan is deleted", func(t *testing.T) {
		status, hdr, raw := del("shop_cache", `{"confirm": "shop_cache"}`)
		if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("deleteOrphanVolume = %d (Cache-Control %q) %s", status, hdr.Get("Cache-Control"), raw)
		}
		checkSchema(t, doc, raw, "VolumeDeletion")
		d = decodeAs[deletionAnswer](t, raw)
		if d.Status != "done" || d.Volume != "shop_cache" || d.App != "shop" || d.Server != "hel-1" || d.BackUpFirst ||
			d.FinishedAt == nil || len(d.ID) == 0 {
			t.Errorf("deletion = %+v", d)
		}
		if got := e.Nomad.HostVolumes(); slices.Contains(got, "shop_cache") || len(got) != 2 {
			t.Errorf("volumes = %v, want shop_cache gone and the other two kept", got)
		}
		for _, o := range getSpace(t, e, "hel-1").OrphanVolumes {
			if o.Name == "shop_cache" {
				t.Error("the deleted volume is still listed")
			}
		}
	})

	t.Run("the record can be read", func(t *testing.T) {
		status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/volumes/shop_cache/deletions/"+d.ID, "", "")
		if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" || decodeAs[deletionAnswer](t, raw).ID != d.ID {
			t.Errorf("getVolumeDeletion = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "VolumeDeletion")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/volumes/shop_cache/deletions/nope", "", "")
		wantError(t, "an unknown deletion", status, raw, http.StatusNotFound, codeDeletionNotFound, "id")
		// The ID of another volume's deletion is not found here.
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/volumes/gone_data/deletions/"+d.ID, "", "")
		wantError(t, "another volume's deletion", status, raw, http.StatusNotFound, codeDeletionNotFound, "id")
	})

	t.Run("a second delete of the same volume is not found", func(t *testing.T) {
		status, _, raw := del("shop_cache", `{"confirm": "shop_cache"}`)
		wantError(t, "an already deleted volume", status, raw, http.StatusNotFound, codeVolumeNotFound, "name")
	})

	t.Run("back up first answers 202, then the volume is archived and deleted", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
			t.Fatalf("set the destination = %d %s", status, raw)
		}
		e.Nomad.FinishOnRegister("gone_data_archive", true, "", func(job *api.Job) (string, string) {
			env := job.TaskGroups[0].Tasks[0].Env
			return "caravana-archive: ok object=" + env["BACKUP_PATH"] + "/" + env["TS"] + "/" + env["VOLUME"] + ".tar.gz bytes=1234\n", ""
		})
		status, hdr, raw := del("gone_data", `{"confirm": "gone_data", "backUpFirst": true}`)
		if status != http.StatusAccepted || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("deleteOrphanVolume with backUpFirst = %d (Cache-Control %q) %s", status, hdr.Get("Cache-Control"), raw)
		}
		checkSchema(t, doc, raw, "VolumeDeletion")
		got := decodeAs[deletionAnswer](t, raw)
		if got.Status != "archiving" || !got.BackUpFirst || got.Volume != "gone_data" || got.App != "gone" || got.FinishedAt != nil {
			t.Errorf("answer = %+v, want the record, archiving", got)
		}
		if want := "/v1/volumes/gone_data/deletions/" + got.ID; hdr.Get("Location") != want {
			t.Errorf("Location = %q, want %q", hdr.Get("Location"), want)
		}

		// The Location is the record: it settles by itself.
		var read deletionAnswer
		deadline := time.Now().Add(5 * time.Second)
		for {
			status, _, raw = call(t, http.MethodGet, e.URL+hdr.Get("Location"), "", "")
			if status != http.StatusOK {
				t.Fatalf("getVolumeDeletion = %d %s", status, raw)
			}
			if read = decodeAs[deletionAnswer](t, raw); read.Status == "done" || read.Status == "failed" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the deletion never settled: %s", raw)
			}
			time.Sleep(time.Millisecond)
		}
		checkSchema(t, doc, raw, "VolumeDeletion")
		if read.Status != "done" || read.Error != "" || !strings.HasPrefix(read.ArchiveObject, "backups/gone/volumes/") || !strings.HasSuffix(read.ArchiveObject, "/gone_data.tar.gz") || read.FinishedAt == nil {
			t.Errorf("settled = %+v", read)
		}
		if slices.Contains(e.Nomad.HostVolumes(), "gone_data") {
			t.Error("the volume is still there")
		}
	})
}

func TestCleanupSettingsOperations(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	read := func(want cleanupSettingsJSON) {
		t.Helper()
		status, hdr, raw := call(t, http.MethodGet, e.URL+"/v1/cleanup/settings", "", "")
		if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("getCleanupSettings = %d (Cache-Control %q) %s", status, hdr.Get("Cache-Control"), raw)
		}
		checkSchema(t, doc, raw, "CleanupSettings")
		if got := decodeAs[cleanupSettingsJSON](t, raw); got != want {
			t.Errorf("settings = %+v, want %+v", got, want)
		}
	}
	put := func(body string) (int, http.Header, []byte) {
		return call(t, http.MethodPut, e.URL+"/v1/cleanup/settings", "application/json", body)
	}

	read(cleanupSettingsJSON{Automatic: true, KeepDeployments: 3})

	status, hdr, raw := put(`{"automatic": false, "keepDeployments": 5}`)
	if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("putCleanupSettings = %d (Cache-Control %q) %s", status, hdr.Get("Cache-Control"), raw)
	}
	checkSchema(t, doc, raw, "CleanupSettings")
	if got := decodeAs[cleanupSettingsJSON](t, raw); got != (cleanupSettingsJSON{Automatic: false, KeepDeployments: 5}) {
		t.Errorf("answered %+v", got)
	}
	read(cleanupSettingsJSON{Automatic: false, KeepDeployments: 5})

	for _, c := range []struct{ name, body, field, code string }{
		{"keep 0", `{"automatic": true, "keepDeployments": 0}`, "keepDeployments", codeInvalidField},
		{"keep 11", `{"automatic": true, "keepDeployments": 11}`, "keepDeployments", codeInvalidField},
		{"no automatic", `{"keepDeployments": 4}`, "automatic", codeInvalidField},
		{"no keep", `{"automatic": true}`, "keepDeployments", codeInvalidField},
		{"unknown field", `{"automatic": true, "keepDeployments": 4, "daily": true}`, "daily", codeUnknownField},
	} {
		status, hdr, raw := put(c.body)
		wantError(t, c.name, status, raw, http.StatusBadRequest, c.code, c.field)
		if hdr.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: an error is cached: %q", c.name, hdr.Get("Cache-Control"))
		}
	}
	read(cleanupSettingsJSON{Automatic: false, KeepDeployments: 5}) // a refused change left them alone

	// 10 and 1 are the ends.
	for _, keep := range []string{"1", "10"} {
		if status, _, raw := put(`{"automatic": true, "keepDeployments": ` + keep + `}`); status != http.StatusOK {
			t.Errorf("keep %s = %d %s", keep, status, raw)
		}
	}
}

// Every operation needs credentials; the scope walk (TestScopedTokenWalksEveryRoute) shows a deploy token
// is refused, since none of these routes is in scopeRules.
func TestSpaceOperationsNeedCredentials(t *testing.T) {
	e := newTestEnv(t, "")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/servers/hel-1/space"},
		{http.MethodPost, "/v1/servers/hel-1/cleanups"},
		{http.MethodGet, "/v1/servers/hel-1/cleanups"},
		{http.MethodGet, "/v1/servers/hel-1/cleanups/x"},
		{http.MethodPost, "/v1/volumes/shop_cache/deletions"},
		{http.MethodGet, "/v1/volumes/shop_cache/deletions/x"},
		{http.MethodGet, "/v1/cleanup/settings"},
		{http.MethodPut, "/v1/cleanup/settings"},
	} {
		status, _, raw := call(t, c.method, e.RawURL+c.path, "application/json", `{}`)
		wantError(t, c.method+" "+c.path+" without credentials", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
	}
}
