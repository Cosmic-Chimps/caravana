package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// spaceReportOf is a healthy report that also says what the server's Docker holds.
func spaceReportOf(sp *NodeSpace) NodeReport {
	r := healthyReport()
	r.Space = sp
	return r
}

// emptySpace is a space block with nothing in it: an agent that answers and a Docker that holds nothing.
func emptySpace() *NodeSpace {
	return &NodeSpace{Images: []NodeImage{}, Volumes: []NodeVolumeSize{}}
}

func (e *clusterEnv) setClock(t time.Time) { e.svc.clock.(*fakeClock).Set(t) }

// firstSync syncs the first server with the report and returns the answer.
func (e *clusterEnv) firstSync(t *testing.T, r NodeReport) NodeAnswer {
	t.Helper()
	ans, err := e.svc.NodeSync(context.Background(), e.firstToken, r)
	if err != nil {
		t.Fatalf("NodeSync: %v", err)
	}
	return ans
}

func unreadable(t *testing.T, err error) string {
	t.Helper()
	var fe *app.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want a *app.FieldError", err)
	}
	return fe.Field
}

func TestValidateSpace(t *testing.T) {
	d := dg("a")
	good := func() *NodeSpace {
		return &NodeSpace{
			Images:        []NodeImage{{ID: dg("1"), Digests: []string{d}, Tags: []string{"web:1"}, Bytes: 5, InUse: true}},
			DockerVolumes: NodeCount{Count: 154, Bytes: 91050000},
			Volumes:       []NodeVolumeSize{{ID: "e971d06a-1f46-fc04-583b-25618814daeb", Bytes: 2300000000}},
			LastClean:     &NodeClean{ID: strings.Repeat("a", 32), StartedAt: "2026-11-20T04:00:00Z", FinishedAt: "2026-11-20T04:01:00Z", ImagesRemoved: 7},
		}
	}
	tests := []struct {
		name   string
		mutate func(sp *NodeSpace)
		field  string // "" means accepted
	}{
		{"valid", func(sp *NodeSpace) {}, ""},
		{"no last clean, no time", func(sp *NodeSpace) { sp.LastClean = nil }, ""},
		{"an interrupted clean", func(sp *NodeSpace) { sp.LastClean.FinishedAt = "" }, ""},
		{"1001 images", func(sp *NodeSpace) { sp.Images = make([]NodeImage, 1001) }, "space.images"},
		{"an image with no id", func(sp *NodeSpace) { sp.Images[0].ID = "" }, "space.images[0].id"},
		{"an id of 301 characters", func(sp *NodeSpace) { sp.Images[0].ID = strings.Repeat("x", 301) }, "space.images[0].id"},
		{"an id with a control character", func(sp *NodeSpace) { sp.Images[0].ID = "a\nb" }, "space.images[0].id"},
		{"17 digests", func(sp *NodeSpace) { sp.Images[0].Digests = slicesOf(d, 17) }, "space.images[0].digests"},
		{"16 digests", func(sp *NodeSpace) { sp.Images[0].Digests = slicesOf(d, 16) }, ""},
		{"a bad digest", func(sp *NodeSpace) { sp.Images[0].Digests = []string{d, "sha256:zz"} }, "space.images[0].digests[1]"},
		{"17 tags", func(sp *NodeSpace) { sp.Images[0].Tags = slicesOf("t:1", 17) }, "space.images[0].tags"},
		{"a tag of 301", func(sp *NodeSpace) { sp.Images[0].Tags = []string{strings.Repeat("t", 301)} }, "space.images[0].tags[0]"},
		{"negative bytes", func(sp *NodeSpace) { sp.Images[0].Bytes = -1 }, "space.images[0].bytes"},
		{"bytes over 2^50", func(sp *NodeSpace) { sp.Images[0].Bytes = 1<<50 + 1 }, "space.images[0].bytes"},
		{"docker volumes count", func(sp *NodeSpace) { sp.DockerVolumes.Count = -3 }, "space.dockerVolumes.count"},
		{"docker volumes bytes", func(sp *NodeSpace) { sp.DockerVolumes.Bytes = 1 << 51 }, "space.dockerVolumes.bytes"},
		{"build cache", func(sp *NodeSpace) { sp.BuildCacheBytes = -1 }, "space.buildCacheBytes"},
		{"4097 volumes", func(sp *NodeSpace) { sp.Volumes = make([]NodeVolumeSize, 4097) }, "space.volumes"},
		{"a volume id with a slash", func(sp *NodeSpace) { sp.Volumes[0].ID = "../etc" }, "space.volumes[0].id"},
		{"a volume id of 65", func(sp *NodeSpace) { sp.Volumes[0].ID = strings.Repeat("a", 65) }, "space.volumes[0].id"},
		{"a volume's bytes", func(sp *NodeSpace) { sp.Volumes[0].Bytes = -5 }, "space.volumes[0].bytes"},
		{"a bad measured time", func(sp *NodeSpace) { sp.VolumesMeasuredAt = "yesterday" }, "space.volumesMeasuredAt"},
		{"a good measured time", func(sp *NodeSpace) { sp.VolumesMeasuredAt = "2026-11-20T03:35:20Z" }, ""},
		{"a last clean id of 31", func(sp *NodeSpace) { sp.LastClean.ID = strings.Repeat("a", 31) }, "space.lastClean.id"},
		{"a last clean id upper case", func(sp *NodeSpace) { sp.LastClean.ID = strings.Repeat("A", 32) }, "space.lastClean.id"},
		{"an empty last clean id", func(sp *NodeSpace) { sp.LastClean.ID = "" }, ""},
		{"a last clean time", func(sp *NodeSpace) { sp.LastClean.StartedAt = "04:00" }, "space.lastClean.startedAt"},
		{"images removed", func(sp *NodeSpace) { sp.LastClean.ImagesRemoved = -1 }, "space.lastClean.imagesRemoved"},
		{"an error of 501", func(sp *NodeSpace) { sp.LastClean.Error = strings.Repeat("e", 501) }, "space.lastClean.error"},
		{"an error of 500", func(sp *NodeSpace) { sp.LastClean.Error = strings.Repeat("e", 500) }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := good()
			tt.mutate(sp)
			err := validateSpace(sp)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("refused a valid block: %v", err)
				}
				return
			}
			if got := unreadable(t, err); got != tt.field {
				t.Errorf("field = %q, want %q (%v)", got, tt.field, err)
			}
		})
	}
	if err := validateSpace(nil); err != nil {
		t.Errorf("no block = %v", err)
	}
}

func slicesOf(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// A bad space block refuses the whole sync, as a bad field of the report does, and stores nothing.
func TestNodeSyncRefusesABadSpaceBlock(t *testing.T) {
	e := newClusterEnv(t)
	sp := emptySpace()
	sp.Images = []NodeImage{{ID: "x", Digests: []string{"nope"}}}
	_, err := e.svc.NodeSync(context.Background(), e.firstToken, spaceReportOf(sp))
	if got := unreadable(t, err); got != "space.images[0].digests[0]" {
		t.Errorf("field = %q", got)
	}
	row, _ := e.store.ServerByName(context.Background(), firstServerName)
	if strings.Contains(row.Report, "nope") {
		t.Error("a refused report was stored")
	}
}

func TestRequestCleanup(t *testing.T) {
	ctx := context.Background()

	t.Run("an unknown server", func(t *testing.T) {
		e := newClusterEnv(t)
		if _, err := e.svc.RequestCleanup(ctx, "nope", store.CleanupByOwner, false); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("err = %v, want ErrServerNotFound", err)
		}
	})
	t.Run("a Nomad node with no row has no agent", func(t *testing.T) {
		e := newClusterEnv(t)
		e.nomad.AddNode("node-lab", "lab", "ready", "")
		_, err := e.svc.RequestCleanup(ctx, "lab", store.CleanupByOwner, false)
		var na *NoAgentError
		if !errors.Is(err, ErrNoAgent) || !errors.As(err, &na) || na.Server != "lab" ||
			!strings.Contains(err.Error(), "run the installer on it") {
			t.Errorf("err = %v, want ErrNoAgent naming lab", err)
		}
	})
	t.Run("a server that never reported its images", func(t *testing.T) {
		e := newClusterEnv(t)
		if _, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false); !errors.Is(err, ErrNoAgent) {
			t.Errorf("no report: %v", err)
		}
		e.firstSync(t, healthyReport()) // an agent without the block
		if _, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false); !errors.Is(err, ErrNoAgent) {
			t.Errorf("a report without space: %v", err)
		}
	})
	t.Run("a report that is too old", func(t *testing.T) {
		e := newClusterEnv(t)
		e.firstSync(t, spaceReportOf(emptySpace()))
		e.setClock(e.svc.clock.Now().Add(11 * time.Minute))
		if _, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false); !errors.Is(err, ErrNoAgent) {
			t.Errorf("a stale report: %v", err)
		}
	})
	t.Run("requested, once", func(t *testing.T) {
		e := newClusterEnv(t)
		e.firstSync(t, spaceReportOf(emptySpace()))
		c, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.ID) != 32 || c.Status != store.CleanupRequested || c.Trigger != store.CleanupByOwner || !c.DockerVolumes || c.ServerName != firstServerName {
			t.Errorf("clean-up = %+v", c)
		}
		_, err = e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false)
		var run *CleanupRunningError
		if !errors.Is(err, ErrCleanupRunning) || !errors.As(err, &run) || run.Server != firstServerName {
			t.Errorf("a second request = %v, want ErrCleanupRunning", err)
		}
	})
}

// The three steps of a sync: the request is handed out with the keep list, marked running, handed out
// again until the report settles it.
func TestCleanupHandoffAndSettling(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	// An app whose healthy deployment pinned the whoami digest: its image is kept.
	a := whoami()
	if _, err := e.store.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	recordDeployment(t, e.env, "d1", &a, store.StatusHealthy)

	e.firstSync(t, spaceReportOf(emptySpace()))
	if ans := e.firstSync(t, spaceReportOf(emptySpace())); ans.Clean != nil {
		t.Fatalf("a clean block with no clean-up: %+v", ans.Clean)
	}
	c, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupBySchedule, false)
	if err != nil {
		t.Fatal(err)
	}

	e.setClock(e.svc.clock.Now().Add(30 * time.Second))
	handed := e.svc.clock.Now()
	ans := e.firstSync(t, spaceReportOf(emptySpace()))
	if ans.Clean == nil || ans.Clean.ID != c.ID || ans.Clean.DockerVolumes {
		t.Fatalf("answer's clean = %+v, want the request", ans.Clean)
	}
	if d, _ := digestOf(apptest.WhoamiDigest); !contains(ans.Clean.Keep, d) {
		t.Errorf("keep = %v: the whoami digest of a healthy deployment is missing", ans.Clean.Keep)
	}
	for _, h := range e.svc.deployer.HelperImages() {
		if d, _ := digestOf(h); !contains(ans.Clean.Keep, d) {
			t.Errorf("keep = %v: the helper image %s is missing", ans.Clean.Keep, h)
		}
	}
	got, _ := e.store.GetImageCleanup(ctx, c.ID)
	if got.Status != store.CleanupRunning || !got.StartedAt.Equal(handed.UTC()) {
		t.Errorf("after the hand-out = %+v, want running from %v", got, handed)
	}

	// Handed out again while the agent hasn't reported it, and the start doesn't move.
	e.setClock(e.svc.clock.Now().Add(30 * time.Second))
	if ans := e.firstSync(t, spaceReportOf(emptySpace())); ans.Clean == nil || ans.Clean.ID != c.ID {
		t.Errorf("the second hand-out = %+v", ans.Clean)
	}
	if got, _ := e.store.GetImageCleanup(ctx, c.ID); !got.StartedAt.Equal(handed.UTC()) {
		t.Errorf("the start moved to %v", got.StartedAt)
	}

	// Another clean-up's outcome doesn't settle it.
	other := emptySpace()
	other.LastClean = &NodeClean{ID: strings.Repeat("0", 32), StartedAt: "2026-10-01T04:00:00Z", FinishedAt: "2026-10-01T04:01:00Z"}
	if ans := e.firstSync(t, spaceReportOf(other)); ans.Clean == nil {
		t.Error("an older outcome settled the open clean-up")
	}

	// Its own outcome does: done, with the agent's end time and counts, and nothing more handed out.
	done := emptySpace()
	done.LastClean = &NodeClean{ID: c.ID, StartedAt: "2026-10-15T04:00:40Z", FinishedAt: "2026-10-15T04:02:00Z", ImagesRemoved: 7, BytesFreed: 1200000000, VolumesRemoved: 0}
	if ans := e.firstSync(t, spaceReportOf(done)); ans.Clean != nil {
		t.Errorf("a settled clean-up was handed out again: %+v", ans.Clean)
	}
	got, _ = e.store.GetImageCleanup(ctx, c.ID)
	if got.Status != store.CleanupDone || got.ImagesRemoved != 7 || got.BytesFreed != 1200000000 || got.Error != "" ||
		!got.FinishedAt.Equal(time.Date(2026, 10, 15, 4, 2, 0, 0, time.UTC)) {
		t.Errorf("settled = %+v", got)
	}
	// The report repeats the outcome at every sync, which changes nothing.
	e.firstSync(t, spaceReportOf(done))
	if again, _ := e.store.GetImageCleanup(ctx, c.ID); again != got {
		t.Errorf("a repeated report changed the record: %+v", again)
	}
	// And a new request is possible now.
	if _, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false); err != nil {
		t.Errorf("a new clean-up after the first ended = %v", err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestCleanupSettledFromReportFailures(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		last   func(id string) *NodeClean
		status string
		errHas string
	}{
		{"the agent's error", func(id string) *NodeClean {
			return &NodeClean{ID: id, StartedAt: "2026-10-15T04:00:00Z", FinishedAt: "2026-10-15T04:00:30Z", ImagesRemoved: 2, Error: "conflict: unable to delete"}
		}, store.CleanupFailed, "conflict: unable to delete"},
		{"began and never ended", func(id string) *NodeClean {
			return &NodeClean{ID: id, StartedAt: "2026-10-15T04:00:00Z"}
		}, store.CleanupFailed, "Interrupted on the server."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newClusterEnv(t)
			e.firstSync(t, spaceReportOf(emptySpace()))
			c, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false)
			if err != nil {
				t.Fatal(err)
			}
			e.firstSync(t, spaceReportOf(emptySpace())) // handed out
			sp := emptySpace()
			sp.LastClean = tt.last(c.ID)
			e.firstSync(t, spaceReportOf(sp))
			got, _ := e.store.GetImageCleanup(ctx, c.ID)
			if got.Status != tt.status || !strings.Contains(got.Error, tt.errHas) {
				t.Errorf("settled = %+v, want %s with %q", got, tt.status, tt.errHas)
			}
		})
	}
}

func TestCleanupPassGivesUpOnTheStuck(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	two := e.addReadyServer(t, "two")
	start := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	e.setClock(start)
	// Settings with the daily run off, so only the pass's first job is looked at.
	if _, err := e.svc.PutCleanupSettings(ctx, false, 3); err != nil {
		t.Fatal(err)
	}
	requested, err := e.store.CreateImageCleanup(ctx, store.ImageCleanup{ID: strings.Repeat("a", 32), ServerIdx: 1, ServerName: firstServerName, Trigger: store.CleanupByOwner, RequestedAt: start})
	if err != nil {
		t.Fatal(err)
	}
	running, err := e.store.CreateImageCleanup(ctx, store.ImageCleanup{ID: strings.Repeat("b", 32), ServerIdx: two.Index, ServerName: "two", Trigger: store.CleanupByOwner, RequestedAt: start})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.StartImageCleanup(ctx, running.ID, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	status := func(id string) store.ImageCleanup {
		c, err := e.store.GetImageCleanup(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	e.setClock(start.Add(9 * time.Minute))
	e.svc.cleanupPass(ctx)
	if status(requested.ID).Status != store.CleanupRequested || status(running.ID).Status != store.CleanupRunning {
		t.Error("the pass gave up too early")
	}

	e.setClock(start.Add(10*time.Minute + time.Second))
	e.svc.cleanupPass(ctx)
	got := status(requested.ID)
	if got.Status != store.CleanupFailed || !strings.Contains(got.Error, "caravana-node.timer") || got.FinishedAt.IsZero() {
		t.Errorf("a requested clean-up nobody picked up = %+v", got)
	}
	if status(running.ID).Status != store.CleanupRunning {
		t.Error("a running clean-up was given up after 10 minutes")
	}

	e.setClock(start.Add(31*time.Minute + 30*time.Second)) // 30 minutes after the hand-out at start+1m
	e.svc.cleanupPass(ctx)
	got = status(running.ID)
	if got.Status != store.CleanupLost || got.Error == "" {
		t.Errorf("a running clean-up with no outcome = %+v", got)
	}
}

func TestDailyCleanups(t *testing.T) {
	ctx := context.Background()
	day := func(h, m int) time.Time { return time.Date(2026, 11, 20, h, m, 0, 0, time.UTC) }

	setup := func(t *testing.T) *clusterEnv {
		e := newClusterEnv(t)
		e.setClock(day(3, 0))
		e.firstSync(t, spaceReportOf(emptySpace()))
		// A second ready server with no agent block, and a third that isn't ready.
		two := e.addReadyServer(t, "two")
		if err := e.store.SaveServerReport(ctx, two.Index, `{"agent":"0.17.0","dockerRunning":true}`, day(3, 0), time.Time{}); err != nil {
			t.Fatal(err)
		}
		return e
	}
	open := func(t *testing.T, e *clusterEnv) []store.ImageCleanup {
		list, err := e.store.OpenImageCleanups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	t.Run("not before 04:00", func(t *testing.T) {
		e := setup(t)
		e.svc.cleanupPass(ctx)
		if len(open(t, e)) != 0 {
			t.Error("a clean-up was requested at 03:00")
		}
		if s, _ := e.store.CleanupSettings(ctx); s.LastAutomaticDay != "" {
			t.Errorf("the day was written at 03:00: %q", s.LastAutomaticDay)
		}
	})
	t.Run("from 04:00, once a day, the servers that report", func(t *testing.T) {
		e := setup(t)
		e.setClock(day(4, 0))
		e.firstSync(t, spaceReportOf(emptySpace())) // fresh report
		e.svc.cleanupPass(ctx)
		list := open(t, e)
		if len(list) != 1 || list[0].ServerName != firstServerName || list[0].Trigger != store.CleanupBySchedule || list[0].DockerVolumes {
			t.Fatalf("open clean-ups = %+v, want one for %s by schedule without Docker's volumes", list, firstServerName)
		}
		if s, _ := e.store.CleanupSettings(ctx); s.LastAutomaticDay != "2026-11-20" {
			t.Errorf("the day = %q", s.LastAutomaticDay)
		}
		// Settle it, and run the pass again the same day: nothing new.
		if err := e.store.SettleImageCleanup(ctx, list[0].ID, store.CleanupDone, day(4, 2), 1, 1, 0, ""); err != nil {
			t.Fatal(err)
		}
		e.setClock(day(15, 0))
		e.svc.cleanupPass(ctx)
		if n := len(open(t, e)); n != 0 {
			t.Errorf("a second round the same day left %d open", n)
		}
		// The next day asks again.
		e.setClock(day(4, 0).AddDate(0, 0, 1))
		e.firstSync(t, spaceReportOf(emptySpace()))
		e.svc.cleanupPass(ctx)
		if n := len(open(t, e)); n != 1 {
			t.Errorf("the next day left %d open, want 1", n)
		}
	})
	t.Run("the setting off", func(t *testing.T) {
		e := setup(t)
		if _, err := e.svc.PutCleanupSettings(ctx, false, 3); err != nil {
			t.Fatal(err)
		}
		e.setClock(day(5, 0))
		e.firstSync(t, spaceReportOf(emptySpace()))
		e.svc.cleanupPass(ctx)
		if len(open(t, e)) != 0 {
			t.Error("a clean-up was requested with the daily run off")
		}
	})
	t.Run("a server with a clean-up under way is skipped without a fuss", func(t *testing.T) {
		e := setup(t)
		e.setClock(day(4, 0))
		e.firstSync(t, spaceReportOf(emptySpace()))
		first, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, true)
		if err != nil {
			t.Fatal(err)
		}
		e.svc.cleanupPass(ctx)
		list := open(t, e)
		if len(list) != 1 || list[0].ID != first.ID {
			t.Errorf("open = %+v, want only the owner's", list)
		}
		if s, _ := e.store.CleanupSettings(ctx); s.LastAutomaticDay != "2026-11-20" {
			t.Errorf("the day = %q", s.LastAutomaticDay)
		}
	})
}

func TestCleanupSettings(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	got, err := e.svc.CleanupSettings(ctx)
	if err != nil || !got.Automatic || got.KeepDeployments != 3 {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	got, err = e.svc.PutCleanupSettings(ctx, false, 10)
	if err != nil || got.Automatic || got.KeepDeployments != 10 {
		t.Fatalf("put = %+v, %v", got, err)
	}
	for _, bad := range []int{0, 11, -2} {
		_, err := e.svc.PutCleanupSettings(ctx, true, bad)
		if f := unreadable(t, err); f != "keepDeployments" {
			t.Errorf("keep %d: field %q", bad, f)
		}
	}
	if got, _ := e.svc.CleanupSettings(ctx); got.KeepDeployments != 10 || got.Automatic {
		t.Errorf("a refused put changed the settings: %+v", got)
	}
}

func TestServerCleanupReads(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.addReadyServer(t, "two")
	e.firstSync(t, spaceReportOf(emptySpace()))
	c, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false)
	if err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.ServerCleanups(ctx, firstServerName)
	if err != nil || len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("ServerCleanups = %+v, %v", list, err)
	}
	if list, _ := e.svc.ServerCleanups(ctx, "two"); len(list) != 0 {
		t.Errorf("another server's list = %+v", list)
	}
	if got, err := e.svc.ServerCleanup(ctx, firstServerName, c.ID); err != nil || got.ID != c.ID {
		t.Errorf("ServerCleanup = %+v, %v", got, err)
	}
	// An ID of another server's clean-up, or no ID, is not found.
	for _, tc := range []struct{ server, id string }{{"two", c.ID}, {firstServerName, "nope"}} {
		if _, err := e.svc.ServerCleanup(ctx, tc.server, tc.id); !errors.Is(err, ErrCleanupNotFound) {
			t.Errorf("ServerCleanup(%s, %s) = %v, want ErrCleanupNotFound", tc.server, tc.id, err)
		}
	}
	for _, name := range []string{"nope"} {
		if _, err := e.svc.ServerCleanups(ctx, name); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("ServerCleanups(%s) = %v", name, err)
		}
		if _, err := e.svc.ServerCleanup(ctx, name, c.ID); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("ServerCleanup(%s) = %v", name, err)
		}
	}
}

// ServerSpace: the images against the keep rule, Docker's leftovers, the orphan volumes on the node.
func TestServerSpace(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	a := whoami()
	if _, err := e.store.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	recordDeployment(t, e.env, "d1", &a, store.StatusHealthy)

	whoamiDigest, _ := digestOf(apptest.WhoamiDigest)
	img := func(id, tag, digest string, bytes int64, inUse bool) NodeImage {
		var ds []string
		if digest != "" {
			ds = []string{digest}
		}
		return NodeImage{ID: dg(id), Tags: []string{tag}, Digests: ds, Bytes: bytes, InUse: inUse}
	}
	sp := &NodeSpace{
		Images: []NodeImage{
			img("1", "traefik/whoami:latest", whoamiDigest, 5_000_000, false), // kept: a healthy deployment pinned it
			img("2", "old:1", dg("e"), 300_000_000, false),                    // unused
			img("3", "run:1", dg("f"), 100_000_000, true),                     // used by a container
			img("4", "tiny:1", dg("d"), 2_000_000, false),                     // unused
			img("5", "local:1", "", 7_000_000, false),                         // unused, no digest
		},
		DockerVolumes:     NodeCount{Count: 154, Bytes: 91_050_000},
		BuildCacheBytes:   10_000_000,
		Volumes:           []NodeVolumeSize{{ID: "vol-1", Bytes: 2_300_000_000}},
		VolumesMeasuredAt: "2026-10-15T02:50:00Z",
	}
	e.firstSync(t, spaceReportOf(sp))

	t.Run("images and leftovers", func(t *testing.T) {
		v, err := e.svc.ServerSpace(ctx, firstServerName)
		if err != nil {
			t.Fatal(err)
		}
		if !v.Managed || v.ImagesUnavailable != "" || v.Images == nil {
			t.Fatalf("view = %+v", v)
		}
		im := v.Images
		if im.Count != 5 || im.Bytes != 414_000_000 || im.UnusedCount != 3 || im.UnusedBytes != 309_000_000 {
			t.Errorf("images = %+v", im)
		}
		if len(im.Unused) != 3 || im.Unused[0].Tags[0] != "old:1" || im.Unused[1].Tags[0] != "local:1" || im.Unused[2].Tags[0] != "tiny:1" {
			t.Errorf("unused = %+v: want largest first", im.Unused)
		}
		if im.MeasuredAt.IsZero() {
			t.Error("no report time")
		}
		if v.DockerVolumes == nil || *v.DockerVolumes != (NodeCount{154, 91_050_000}) || v.BuildCacheBytes == nil || *v.BuildCacheBytes != 10_000_000 {
			t.Errorf("leftovers = %+v, %v", v.DockerVolumes, v.BuildCacheBytes)
		}
		if v.ReclaimableBytes != 309_000_000+91_050_000+10_000_000 {
			t.Errorf("reclaimable = %d", v.ReclaimableBytes)
		}
		if !v.Settings.Automatic || v.OpenCleanup != nil || v.LastCleanup != nil {
			t.Errorf("settings/cleanups = %+v %v %v", v.Settings, v.OpenCleanup, v.LastCleanup)
		}
	})

	t.Run("the cleanups", func(t *testing.T) {
		c, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := e.svc.ServerSpace(ctx, firstServerName)
		if v.OpenCleanup == nil || v.OpenCleanup.ID != c.ID || v.LastCleanup != nil {
			t.Errorf("open = %+v last = %+v", v.OpenCleanup, v.LastCleanup)
		}
		if err := e.store.SettleImageCleanup(ctx, c.ID, store.CleanupDone, e.svc.clock.Now(), 3, 309_000_000, 0, ""); err != nil {
			t.Fatal(err)
		}
		v, _ = e.svc.ServerSpace(ctx, firstServerName)
		if v.OpenCleanup != nil || v.LastCleanup == nil || v.LastCleanup.ID != c.ID {
			t.Errorf("after: open = %+v last = %+v", v.OpenCleanup, v.LastCleanup)
		}
	})

	t.Run("no agent, no inventory", func(t *testing.T) {
		e := newClusterEnv(t)
		v, err := e.svc.ServerSpace(ctx, firstServerName)
		if err != nil || v.Images != nil || v.ImagesUnavailable != "no agent" || v.DockerVolumes != nil || v.BuildCacheBytes != nil {
			t.Errorf("never reported: %+v, %v", v, err)
		}
		e.firstSync(t, healthyReport())
		v, _ = e.svc.ServerSpace(ctx, firstServerName)
		if v.Images != nil || v.ImagesUnavailable != "no inventory yet" {
			t.Errorf("a report without space: %+v", v)
		}
		e.nomad.AddNode("node-lab", "lab", "ready", "")
		v, err = e.svc.ServerSpace(ctx, "lab")
		if err != nil || v.Managed || v.ImagesUnavailable != "no agent" || v.OpenCleanup != nil {
			t.Errorf("an unmanaged node: %+v, %v", v, err)
		}
		if _, err := e.svc.ServerSpace(ctx, "nope"); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("an unknown server = %v", err)
		}
	})

	t.Run("a keep list that doesn't fit leaves the images unread", func(t *testing.T) {
		e := newClusterEnv(t)
		e.firstSync(t, spaceReportOf(emptySpace()))
		for i := range maxKeepDigests {
			id := fmt.Sprintf("many-%d", i)
			def := appOf(id, fmt.Sprintf("x@sha256:%064x", i))
			if _, err := e.store.SaveApp(ctx, def.App, nil); err != nil {
				t.Fatal(err)
			}
		}
		v, err := e.svc.ServerSpace(ctx, firstServerName)
		if err != nil || v.Images != nil || !strings.Contains(v.ImagesUnavailable, "too many images") {
			t.Errorf("view = %+v, %v", v, err)
		}
		if _, err := e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false); !errors.Is(err, ErrKeepListTooLong) {
			t.Errorf("RequestCleanup = %v, want ErrKeepListTooLong", err)
		}
	})
}
