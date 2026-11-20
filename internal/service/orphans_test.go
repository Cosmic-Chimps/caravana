package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestClassifyOrphans(t *testing.T) {
	apps := []store.AppRecord{
		appOf("shop"),
		appOf("blog--staging--web"),
	}
	apps[0].App.Services = []app.Service{{Name: "web", Volumes: []app.Volume{{Name: "uploads", Path: "/u"}}}, {Name: "db", Volumes: []app.Volume{{Name: "pg", Path: "/p"}}}}
	apps[1].App.Services = []app.Service{{Name: "web", Volumes: []app.Volume{{Name: "data", Path: "/d"}}}}
	stub := func(name string) deploy.HostVolumeStub {
		return deploy.HostVolumeStub{ID: "id-" + name, Name: name, NodeID: "node-1"}
	}

	got := classifyOrphans([]deploy.HostVolumeStub{
		stub("shop_uploads"),            // declared
		stub("shop_pg"),                 // declared by another service
		stub("shop_cache"),              // its app exists, no definition declares it
		stub("blog--staging--web_data"), // declared (a place ID)
		stub("blog--staging--web_old"),  // orphan of a place ID
		stub("gone_data"),               // its app was deleted
		stub("plain"),                   // no "_": nothing says whose
		stub("Bad_Name_x"),              // the first part isn't an app ID
		stub("shop_uploads_extra"),      // a different name from a declared one
	}, apps)

	type want struct {
		name, app string
		exists    bool
	}
	wants := []want{
		{"Bad_Name_x", "", false},
		{"blog--staging--web_old", "blog--staging--web", true},
		{"gone_data", "gone", false},
		{"plain", "", false},
		{"shop_cache", "shop", true},
		{"shop_uploads_extra", "shop", true},
	}
	if len(got) != len(wants) {
		t.Fatalf("%d orphans, want %d: %+v", len(got), len(wants), got)
	}
	for i, w := range wants {
		o := got[i]
		if o.Stub.Name != w.name || o.App != w.app || o.AppExists != w.exists {
			t.Errorf("orphan %d = %s app %q exists %v, want %s app %q exists %v", i, o.Stub.Name, o.App, o.AppExists, w.name, w.app, w.exists)
		}
	}
	if len(classifyOrphans(nil, apps)) != 0 || len(classifyOrphans([]deploy.HostVolumeStub{stub("x_y")}, nil)) != 1 {
		t.Error("an empty list or no apps isn't read right")
	}
}

// orphanEnv is a cluster with an app shop that declares the volume uploads, and Nomad volumes of every kind.
func orphanEnv(t *testing.T) *clusterEnv {
	t.Helper()
	ctx := context.Background()
	e := newClusterEnv(t)
	shop := app.App{ID: "shop", Name: "shop", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{Name: "web", Image: "traefik/whoami:latest", Volumes: []app.Volume{{Name: "uploads", Path: "/u"}}}}}
	if _, err := e.store.SaveApp(ctx, shop, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shop_uploads", "shop_cache", "gone_data"} {
		e.nomad.AddHostVolumeOn(name, "node-1")
	}
	return e
}

func volumeID(t *testing.T, e *clusterEnv, name string) string {
	t.Helper()
	all, err := e.svc.deployer.AllHostVolumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range all {
		if v.Name == name {
			return v.ID
		}
	}
	t.Fatalf("no volume %s", name)
	return ""
}

func TestServerSpaceListsOrphanVolumes(t *testing.T) {
	ctx := context.Background()
	e := orphanEnv(t)
	// shop_cache was declared by a recorded deployment; gone_data never was.
	old := app.App{ID: "shop", Name: "shop", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{Name: "web", Image: "traefik/whoami:latest", Volumes: []app.Volume{{Name: "uploads", Path: "/u"}, {Name: "cache", Path: "/c"}}}}}
	if _, err := e.store.CreateDeployment(ctx, "shop", app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{"web": pin("traefik/whoami", "c")}}, &old, nil, ""); err != nil {
		t.Fatal(err)
	}
	cacheID, goneID := volumeID(t, e, "shop_cache"), volumeID(t, e, "gone_data")
	sp := emptySpace()
	sp.Volumes = []NodeVolumeSize{{ID: cacheID, Bytes: 2_300_000_000}, {ID: goneID, Bytes: 50_000_000}, {ID: volumeID(t, e, "shop_uploads"), Bytes: 9_000_000_000}}
	sp.VolumesMeasuredAt = "2026-10-15T02:50:00Z"
	e.firstSync(t, spaceReportOf(sp))
	e.nomad.ClaimHostVolume("gone_data", "alloc-1") // a copy claims it

	v, err := e.svc.ServerSpace(ctx, firstServerName)
	if err != nil {
		t.Fatal(err)
	}
	if v.OrphansUnavailable || len(v.Orphans) != 2 {
		t.Fatalf("orphans = %+v (unavailable %v)", v.Orphans, v.OrphansUnavailable)
	}
	// By name: gone_data, then shop_cache.
	goneData, shopCache := v.Orphans[0], v.Orphans[1]

	if shopCache.Name != "shop_cache" || shopCache.App != "shop" || !shopCache.AppExists || shopCache.Place.Name != "shop" || shopCache.Place.Environment != "production" ||
		shopCache.Server != firstServerName || shopCache.ID != cacheID || shopCache.InUse ||
		shopCache.Bytes == nil || *shopCache.Bytes != 2_300_000_000 || !shopCache.MeasuredAt.Equal(time.Date(2026, 10, 15, 2, 50, 0, 0, time.UTC)) {
		t.Errorf("shop_cache = %+v", shopCache)
	}
	if shopCache.LastDeployedAt.IsZero() {
		t.Error("shop_cache has no last deployed time although a deployment declared it")
	}
	if goneData.App != "gone" || goneData.AppExists || !goneData.InUse || goneData.Bytes == nil || *goneData.Bytes != 50_000_000 || !goneData.LastDeployedAt.IsZero() {
		t.Errorf("gone_data = %+v", goneData)
	}
	// The declared volume isn't listed, and the reclaimable total counts the measured orphan that isn't in use only.
	if want := int64(2_300_000_000); v.ReclaimableBytes < want || v.ReclaimableBytes > want {
		t.Errorf("reclaimable = %d, want %d (images and leftovers are zero here)", v.ReclaimableBytes, want)
	}

	t.Run("a Nomad that can't be read", func(t *testing.T) {
		e.nomad.FailVolumes(500)
		defer e.nomad.FailVolumes(0)
		v, err := e.svc.ServerSpace(ctx, firstServerName)
		if err != nil || !v.OrphansUnavailable || len(v.Orphans) != 0 {
			t.Errorf("view = %+v, %v", v, err)
		}
	})
	t.Run("another node's volumes are another server's", func(t *testing.T) {
		e.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
		e.nomad.AddHostVolumeOn("elsewhere_data", "node-2")
		v, _ := e.svc.ServerSpace(ctx, firstServerName)
		for _, o := range v.Orphans {
			if o.Name == "elsewhere_data" {
				t.Error("a volume of node-2 is listed on the first server")
			}
		}
		two, err := e.svc.ServerSpace(ctx, "two")
		if err != nil || len(two.Orphans) != 1 || two.Orphans[0].Name != "elsewhere_data" || two.Managed || two.Orphans[0].Bytes != nil {
			t.Errorf("two = %+v, %v", two, err)
		}
	})
}

func TestDeleteOrphanVolume(t *testing.T) {
	ctx := context.Background()
	newE := func(t *testing.T) *clusterEnv {
		e := orphanEnv(t)
		sp := emptySpace()
		sp.Volumes = []NodeVolumeSize{{ID: volumeID(t, e, "shop_cache"), Bytes: 2_300_000_000}}
		e.firstSync(t, spaceReportOf(sp))
		return e
	}
	records := func(t *testing.T, e *clusterEnv) []store.ActivityRow {
		rows, err := e.store.ListActivity(ctx, 50, time.Time{}, false)
		if err != nil {
			t.Fatal(err)
		}
		return slices.DeleteFunc(rows, func(r store.ActivityRow) bool { return r.Source != store.ActivityDeletion })
	}

	t.Run("the name typed wrong", func(t *testing.T) {
		e := newE(t)
		for _, confirm := range []string{"", "shop_cach", "SHOP_CACHE", " shop_cache"} {
			_, err := e.svc.DeleteOrphanVolume(ctx, "shop_cache", confirm, false, app.TriggerConsole)
			if f := unreadable(t, err); f != "confirm" {
				t.Errorf("confirm %q: field %q", confirm, f)
			}
		}
		if got := e.nomad.HostVolumes(); !slices.Contains(got, "shop_cache") {
			t.Error("a volume was deleted without its name")
		}
	})
	t.Run("refusals", func(t *testing.T) {
		e := newE(t)
		_, err := e.svc.DeleteOrphanVolume(ctx, "nope_x", "nope_x", false, app.TriggerConsole)
		if !errors.Is(err, ErrVolumeNotFound) {
			t.Errorf("an unknown volume = %v", err)
		}
		_, err = e.svc.DeleteOrphanVolume(ctx, "shop_uploads", "shop_uploads", false, app.TriggerConsole)
		var no *VolumeNotOrphanError
		if !errors.Is(err, ErrVolumeNotOrphan) || !errors.As(err, &no) || no.App != "shop" ||
			!strings.Contains(err.Error(), "App shop declares the volume shop_uploads") {
			t.Errorf("a declared volume = %v", err)
		}
		e.nomad.ClaimHostVolume("shop_cache", "alloc-1")
		if _, err = e.svc.DeleteOrphanVolume(ctx, "shop_cache", "shop_cache", false, app.TriggerConsole); !errors.Is(err, ErrVolumeInUse) {
			t.Errorf("a volume a copy claims = %v", err)
		}
		e.nomad.ClaimHostVolume("shop_cache")
		if _, err = e.svc.DeleteOrphanVolume(ctx, "shop_cache", "shop_cache", true, app.TriggerConsole); !errors.Is(err, ErrNoBackupDestination) {
			t.Errorf("back up first, not built yet = %v", err)
		}
		e.svc.spaceMu.Lock()
		e.svc.deleting["shop_cache"] = true
		e.svc.spaceMu.Unlock()
		if _, err = e.svc.DeleteOrphanVolume(ctx, "shop_cache", "shop_cache", false, app.TriggerConsole); !errors.Is(err, ErrDeletionRunning) {
			t.Errorf("a deletion going = %v", err)
		}
		if got := e.nomad.HostVolumes(); !slices.Equal(got, []string{"gone_data", "shop_cache", "shop_uploads"}) {
			t.Errorf("volumes after the refusals = %v", got)
		}
		if n := len(records(t, e)); n != 0 {
			t.Errorf("%d deletions recorded for refusals", n)
		}
	})
	t.Run("deleted, recorded, in the feed", func(t *testing.T) {
		e := newE(t)
		d, err := e.svc.DeleteOrphanVolume(ctx, "shop_cache", "shop_cache", false, app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != store.DeletionDone || d.Volume != "shop_cache" || d.AppID != "shop" || d.ServerName != firstServerName || d.Bytes != 2_300_000_000 ||
			d.BackUpFirst || d.FinishedAt.IsZero() || d.Trigger != "console" || !strings.HasPrefix(d.ID, "vd_") {
			t.Errorf("deletion = %+v", d)
		}
		if got := e.nomad.HostVolumes(); slices.Contains(got, "shop_cache") || len(got) != 2 {
			t.Errorf("volumes after the delete = %v", got)
		}
		got, err := e.svc.GetVolumeDeletion(ctx, "shop_cache", d.ID)
		if err != nil || got.ID != d.ID {
			t.Errorf("GetVolumeDeletion = %+v, %v", got, err)
		}
		if _, err := e.svc.GetVolumeDeletion(ctx, "other", d.ID); !errors.Is(err, ErrDeletionNotFound) {
			t.Errorf("another volume's ID = %v", err)
		}
		// And a second delete finds nothing.
		if _, err := e.svc.DeleteOrphanVolume(ctx, "shop_cache", "shop_cache", false, app.TriggerConsole); !errors.Is(err, ErrVolumeNotFound) {
			t.Errorf("the second delete = %v", err)
		}

		entries, err := e.svc.Activity(ctx, ActivityQuery{Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, en := range entries {
			if strings.HasPrefix(en.ID, store.ActivityDeletion) {
				found = true
				if en.Kind != ActivityServer || en.Status != ActivityHealthy || en.AppID != "shop" ||
					en.Sentence != "Deleted the volume shop_cache of shop (2.1 GB)." {
					t.Errorf("entry = %+v", en)
				}
			}
		}
		if !found {
			t.Error("the deletion isn't in the feed")
		}
	})
	t.Run("a Nomad that refuses the token", func(t *testing.T) {
		e := newE(t)
		// The fake refuses every volume call alike, so the listing fails first and nothing is recorded.
		e.nomad.FailVolumes(403)
		_, err := e.svc.DeleteOrphanVolume(ctx, "shop_cache", "shop_cache", false, app.TriggerConsole)
		if !errors.Is(err, deploy.ErrNomadUnavailable) {
			t.Errorf("err = %v, want ErrNomadUnavailable", err)
		}
		e.nomad.FailVolumes(0)
		if n := len(records(t, e)); n != 0 {
			t.Errorf("%d records for a listing that failed", n)
		}
	})
}

func TestFailInterruptedDeletions(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for _, status := range []string{store.DeletionArchiving, store.DeletionDeleting} {
		if _, err := e.store.CreateVolumeDeletion(ctx, store.VolumeDeletion{ID: "vd_" + status, Volume: "x_y", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.store.CreateVolumeDeletion(ctx, store.VolumeDeletion{ID: "vd_done", Volume: "x_z", Status: store.DeletionDone, FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.failInterruptedDeletions(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"vd_archiving", "vd_deleting"} {
		d, _ := e.store.GetVolumeDeletion(ctx, id)
		if d.Status != store.DeletionFailed || d.Error != "Caravana restarted during it" || d.FinishedAt.IsZero() {
			t.Errorf("%s = %+v", id, d)
		}
	}
	if d, _ := e.store.GetVolumeDeletion(ctx, "vd_done"); d.Status != store.DeletionDone {
		t.Errorf("a finished deletion became %s", d.Status)
	}
}

// What the feed says of a clean-up and a deletion that ended.
func TestActivityWordsOfCleanupsAndDeletions(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.addReadyServer(t, "two")
	mk := func(id string, server store.Server, status string, removed int, freed int64, volumes int, errText string) {
		t.Helper()
		if _, err := e.store.CreateImageCleanup(ctx, store.ImageCleanup{ID: id, ServerIdx: server.Index, ServerName: server.Name, Trigger: store.CleanupBySchedule, RequestedAt: e.svc.clock.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := e.store.SettleImageCleanup(ctx, id, status, e.svc.clock.Now().Add(time.Minute), removed, freed, volumes, errText); err != nil {
			t.Fatal(err)
		}
	}
	one, _ := e.store.ServerByName(ctx, firstServerName)
	two, _ := e.store.ServerByName(ctx, "two")
	mk(strings.Repeat("1", 32), one, store.CleanupDone, 7, 1_200_000_000, 0, "")
	mk(strings.Repeat("2", 32), two, store.CleanupDone, 0, 0, 0, "")
	mk(strings.Repeat("3", 32), one, store.CleanupDone, 1, 0, 154, "")
	mk(strings.Repeat("4", 32), two, store.CleanupFailed, 2, 5, 0, "conflict: unable to delete.")
	mk(strings.Repeat("5", 32), one, store.CleanupLost, 0, 0, 0, "")

	sentences := func(q ActivityQuery) []string {
		q.Limit = 50
		entries, err := e.svc.Activity(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, en := range entries {
			if strings.HasPrefix(en.ID, store.ActivityCleanup) {
				out = append(out, en.Status+": "+en.Sentence)
			}
		}
		slices.Sort(out)
		return out
	}
	all := sentences(ActivityQuery{})
	want := []string{
		"failed: Cleaning up server hel-1 ended without word from the server.",
		"failed: Cleaning up server two failed: conflict: unable to delete.",
		"healthy: Cleaned up server hel-1: removed 1 image and 154 leftover volumes.",
		"healthy: Cleaned up server hel-1: removed 7 images, freed 1.1 GB.",
		"healthy: Cleaned up server two: nothing to remove.",
	}
	if !slices.Equal(all, want) {
		t.Errorf("sentences =\n%s\nwant\n%s", strings.Join(all, "\n"), strings.Join(want, "\n"))
	}
	// The server feed of one server has its own clean-ups only, and the server-only view keeps them.
	if got := sentences(ActivityQuery{Server: true, ForServer: "two"}); len(got) != 2 || !strings.Contains(strings.Join(got, ""), "server two") || strings.Contains(strings.Join(got, ""), "hel-1") {
		t.Errorf("two's feed = %v", got)
	}
	if got := sentences(ActivityQuery{Server: true}); len(got) != 5 {
		t.Errorf("the server-only feed has %d clean-ups, want 5", len(got))
	}
}

func TestDeletionWords(t *testing.T) {
	name := []TextPart{{Text: "shop", Mono: true}}
	b := int64(2_300_000_000)
	tests := []struct {
		name   string
		row    store.ActivityRow
		status string
		want   string
	}{
		{"deleted with the size", store.ActivityRow{Status: "done", Label: "shop_cache", AppID: "shop", Version: &b}, ActivityHealthy, "Deleted the volume shop_cache of shop (2.1 GB)."},
		{"archived first", store.ActivityRow{Status: "done", Label: "shop_cache", AppID: "shop", Version: &b, Detail: "shop/volumes/x.tar.gz"}, ActivityHealthy, "Deleted the volume shop_cache of shop (2.1 GB), archived to shop/volumes/x.tar.gz."},
		{"an unmeasured volume", store.ActivityRow{Status: "done", Label: "shop_cache", AppID: "shop"}, ActivityHealthy, "Deleted the volume shop_cache of shop."},
		{"a volume no app owns", store.ActivityRow{Status: "done", Label: "plain"}, ActivityHealthy, "Deleted the volume plain."},
		{"failed", store.ActivityRow{Status: "failed", Label: "shop_cache", AppID: "shop", Error: "Nomad refused."}, ActivityFailed, "Deleting the volume shop_cache of shop failed: Nomad refused."},
		{"failed with no reason", store.ActivityRow{Status: "failed", Label: "plain"}, ActivityFailed, "Deleting the volume plain failed."},
	}
	for _, tt := range tests {
		status, got := deletionWords(tt.row, name)
		if status != tt.status || plainText(got) != tt.want {
			t.Errorf("%s: %s %q, want %s %q", tt.name, status, plainText(got), tt.status, tt.want)
		}
	}
}

// The deploy that drops a volume warns where its data stays (feature 045).
func TestUnmountedVolumeWarningNamesTheServer(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	a := whoami()
	a.Services[0].Replicas = new(1)
	a.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	deployHealthy(t, e.env, a)
	if got := e.nomad.HostVolumes(); !slices.Equal(got, []string{"whoami_data"}) {
		t.Fatalf("volumes = %v", got)
	}

	a.Services[0].Volumes = nil
	rec, err := e.svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, w := range rec.Warnings {
		if strings.Contains(w.Message, "no longer mounted") {
			found = w.Message
		}
	}
	want := `volume "data" is no longer mounted: its data stays on server hel-1 until you delete it under Operator › Servers`
	if found != want {
		t.Errorf("warning = %q, want %q", found, want)
	}
}

func TestUnmountedVolumesWords(t *testing.T) {
	old := app.App{ID: "a", Services: []app.Service{{Name: "web", Volumes: []app.Volume{{Name: "one", Path: "/1"}, {Name: "two", Path: "/2"}}}, {Name: "db", Volumes: []app.Volume{{Name: "one", Path: "/3"}}}}}
	next := app.App{ID: "a", Services: []app.Service{{Name: "web", Volumes: []app.Volume{{Name: "two", Path: "/2"}}}}}
	got := unmountedVolumes(old, next, nil)
	if len(got) != 1 || got[0].Message != `volume "one" is no longer mounted: its data stays on its server until you delete it under Operator › Servers` {
		t.Errorf("without a server: %+v", got)
	}
	got = unmountedVolumes(old, next, map[string]string{"one": "fsn-2"})
	if len(got) != 1 || !strings.Contains(got[0].Message, "stays on server fsn-2 until") {
		t.Errorf("with a server: %+v", got)
	}
	if got := unmountedVolumes(old, old, nil); got != nil {
		t.Errorf("nothing dropped: %+v", got)
	}
}
