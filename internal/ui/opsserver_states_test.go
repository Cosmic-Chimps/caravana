package ui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// srvWait polls cond every 200 ms for up to 20 seconds: the sampler reads the node twice a second here.
func srvWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// bannersOf is the text of the page's banners (the host's needs-you rows), one per entry.
func bannersOf(body string) []string {
	var out []string
	for _, b := range strings.Split(body, `<div class="cv-panel cv-dmbanner">`)[1:] {
		end := strings.Index(b, "\n</div>")
		if end < 0 {
			end = len(b)
		}
		out = append(out, strings.Join(strings.Fields(text(b[:end])), " "))
	}
	return out
}

// A disk nearly full is a banner under the head. When one filesystem holds Nomad's data and Docker's
// images the banner says so; when they are on two, the banner names its own mount and has no such clause.
func TestOpsServerDiskBanner(t *testing.T) {
	const holdsBoth = "It holds Nomad's data and Docker's images."

	// One filesystem (the lab's): the root at 90 %.
	one := srvEnvStats(t, func(s *api.HostStats) {
		for _, d := range s.DiskStats {
			if d.Mountpoint == "/" {
				d.Used, d.Available, d.UsedPercent = 90*d.Size/100, d.Size-90*d.Size/100, 90
				s.AllocDirStats.Used, s.AllocDirStats.Available, s.AllocDirStats.UsedPercent = d.Used, d.Available, 90
			}
		}
	})
	var banners []string
	srvWait(t, "the disk banner of one filesystem", func() bool {
		_, body := one.get(t, "/ops/servers/lab")
		banners = bannersOf(body)
		return len(banners) > 0
	})
	if len(banners) != 1 || !strings.Contains(banners[0], "The disk at /") || !strings.Contains(banners[0], holdsBoth) ||
		!strings.Contains(banners[0], "Open monitoring") {
		t.Fatalf("one filesystem: banners = %q", banners)
	}

	// Two: Docker's images on /var/lib/docker at 90 %, Nomad's data on the root at 31 %.
	two := srvEnvStats(t, func(s *api.HostStats) {
		s.DiskStats = append(s.DiskStats, &api.HostDiskStats{Device: "/dev/vdc", Mountpoint: "/var/lib/docker",
			Size: 80 << 30, Used: 72 << 30, Available: 8 << 30, UsedPercent: 90})
	})
	srvWait(t, "the disk banner of two filesystems", func() bool {
		_, body := two.get(t, "/ops/servers/lab")
		banners = bannersOf(body)
		return len(banners) > 0
	})
	if len(banners) != 1 || !strings.Contains(banners[0], "The disk at /var/lib/docker") || strings.Contains(banners[0], holdsBoth) {
		t.Fatalf("two filesystems: banners = %q (the mount named, no both-clause)", banners)
	}
}

// A node Nomad has lost: the pill says down, the banner says when the heartbeat was lost and has no action.
func TestOpsServerDownNode(t *testing.T) {
	e := srvEnv(t)
	e.Nomad.SetNode("node-1", "lab", "down", deploytest.LabNodeResources(), nil)
	e.Nomad.SetNodeFacts(deploytest.X86NodeAttributes(), "default", nil)
	var body string
	srvWait(t, "the down pill", func() bool {
		_, body = e.get(t, "/ops/servers/lab")
		return strings.Contains(body, "cv-pill-failed")
	})
	head := regionBetween(t, body, `id="srv-head"`, `id="srv-body"`)
	if !strings.Contains(head, `cv-pill cv-pill-failed"><span class="cv-mark cv-mark-failed" aria-hidden="true"></span>down`) {
		t.Errorf("the head's pill:\n%s", head)
	}
	banners := bannersOf(body)
	if len(banners) == 0 || !strings.HasPrefix(banners[0], "Nomad lost this server's heartbeat") || !strings.HasSuffix(banners[0], "Apps on it may be down.") {
		t.Fatalf("banners = %q", banners)
	}
	if strings.Contains(banners[0], "Open ") {
		t.Errorf("the down node's banner has an action: %q", banners[0])
	}
}

// Recent's history holds the host's boot ("The server started (up since 02:24 UTC).") and the OS's package
// runs, among them one that needs a reboot (", a reboot is needed."), however far back they are: the page
// shows the newest 8, and the history that feeds it has all of them. The first version Caravana ever
// recorded is only the baseline the next change is measured from, so it isn't in the feed.
func TestOpsServerRecentHistory(t *testing.T) {
	e := srvEnv(t)
	got, err := e.Svc.Activity(context.Background(), service.ActivityQuery{Limit: 50, Server: true})
	if err != nil {
		t.Fatal(err)
	}
	bootWords := regexp.MustCompile(`^The server started \(up since \d\d:\d\d UTC\)\.$`)
	var boot, reboot, version bool
	for _, a := range got {
		boot = boot || bootWords.MatchString(a.Sentence)
		reboot = reboot || strings.HasPrefix(a.Sentence, "OS packages updated by Ubuntu's automatic updates:") &&
			strings.HasSuffix(a.Sentence, ", a reboot is needed.")
		version = version || strings.HasPrefix(a.Sentence, "Caravana ")
	}
	if !boot || !reboot {
		t.Errorf("the server's history lacks the boot (%v) or the kernel run with a reboot (%v):\n%+v", boot, reboot, got)
	}
	if version {
		t.Errorf("a first start's version is in the feed:\n%+v", got)
	}
}

// "last seen" is a row only when it has something to say: a node Nomad lost ("lost 3 minutes ago", at
// the time Nomad marked it down), or a reading more than two sampler ticks old ("last reading 5 minutes
// ago"). A ready node read lately has none.
func TestLastSeenFact(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC)
	const gap = time.Minute
	ready := &service.HostNode{NodeFacts: deploy.NodeFacts{State: "ready"}}
	down := &service.HostNode{NodeFacts: deploy.NodeFacts{State: "down", StatusAt: now.Add(-3 * time.Minute), LastHeartbeat: now.Add(-40 * time.Second)}}
	for _, tc := range []struct {
		name      string
		node      *service.HostNode
		read      time.Time
		key, want string // key "" for no row
		strong    bool
	}{
		{"read this tick", ready, now.Add(-10 * time.Second), "", "", false},
		{"read two ticks ago exactly", ready, now.Add(-2 * gap), "", "", false},
		{"read just over two ticks ago", ready, now.Add(-2*gap - time.Second), "last reading", "2 minutes ago", false},
		{"read five minutes ago", ready, now.Add(-5 * time.Minute), "last reading", "5 minutes ago", false},
		{"never read", ready, time.Time{}, "", "", false},
		{"a lost node says when Nomad marked it down, not when it was read", down, now.Add(-5 * time.Second), "last seen", "lost 3 minutes ago", true},
		{"a lost node with no status time has no row", &service.HostNode{NodeFacts: deploy.NodeFacts{State: "down"}}, now, "", "", false},
	} {
		f, ok := lastSeenFact(now, tc.node, tc.read, gap)
		if !ok {
			if tc.key != "" {
				t.Errorf("%s: no row, want %s %q", tc.name, tc.key, tc.want)
			}
			continue
		}
		if tc.key == "" || f.Key != tc.key || f.Value != tc.want || f.Strong != tc.strong || (f.Mark == "failed") != tc.strong {
			t.Errorf("%s: row = %+v, want %s %q", tc.name, f, tc.key, tc.want)
		}
	}
	// A sample's time is the start of its window: the reading is taken at its end.
	at := now.Add(-90 * time.Second)
	if got := readAt(service.HostView{SampledAt: &at}, gap); !got.Equal(now.Add(-30 * time.Second)) {
		t.Errorf("readAt = %v", got)
	}
	if got := readAt(service.HostView{}, gap); !got.IsZero() {
		t.Errorf("readAt of a host never read = %v", got)
	}
}

// The Up cell follows the phase: "Up" with a time, "Starting" with "for 40 s", "Stopping", and "Failed" with
// when and why, in the words the schedules' runs use. A failed copy never reads "Up: failed".
func TestCopyUpPhases(t *testing.T) {
	now := time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	exit := func(n int) *int { return &n }
	cp := func(phase string, c deploy.Copy) service.HostCopy { return service.HostCopy{Copy: c, Phase: phase} }
	for _, tc := range []struct {
		name string
		c    service.HostCopy
		want srvUp
	}{
		{"running", cp(service.CopyPhaseRunning, deploy.Copy{StartedAt: ago(76*time.Hour + 30*time.Minute)}), srvUp{Label: "Up", Text: "3 d 4 h"}},
		{"running, never started", cp(service.CopyPhaseRunning, deploy.Copy{}), srvUp{Label: "Up", Text: "—"}},
		{"starting", cp(service.CopyPhaseStarting, deploy.Copy{StartedAt: ago(40 * time.Second)}), srvUp{Label: "Starting", Text: "for 40 s"}},
		{"starting, minutes", cp(service.CopyPhaseStarting, deploy.Copy{StartedAt: ago(3 * time.Minute)}), srvUp{Label: "Starting", Text: "for 3 min"}},
		{"starting, not placed", cp(service.CopyPhaseStarting, deploy.Copy{}), srvUp{Label: "Starting", Text: "—"}},
		{"stopping", cp(service.CopyPhaseStopping, deploy.Copy{StartedAt: ago(5 * time.Hour)}), srvUp{Label: "Stopping", Text: "up 5 h"}},
		{"failed with an exit code", cp(service.CopyPhaseFailed, deploy.Copy{FinishedAt: ago(26 * time.Second), ExitCode: exit(1)}), srvUp{Label: "Failed", Word: "failed", Text: "26 s ago · exit 1"}},
		{"failed out of memory", cp(service.CopyPhaseFailed, deploy.Copy{FinishedAt: ago(3 * time.Minute), OutOfMemory: true, ExitCode: exit(137)}), srvUp{Label: "Failed", Word: "failed", Text: "3 min ago · out of memory"}},
		{"failed, couldn't start", cp(service.CopyPhaseFailed, deploy.Copy{FinishedAt: ago(3 * time.Minute), StartFailed: true}), srvUp{Label: "Failed", Word: "failed", Text: "3 min ago · couldn't start"}},
		{"failed, never started", cp(service.CopyPhaseFailed, deploy.Copy{StartFailed: true}), srvUp{Label: "Failed", Word: "failed", Text: "couldn't start"}},
		{"failed, nothing said", cp(service.CopyPhaseFailed, deploy.Copy{FinishedAt: ago(2 * time.Hour)}), srvUp{Label: "Failed", Word: "failed", Text: "2 h ago"}},
	} {
		if got := copyUp(now, tc.c); got != tc.want {
			t.Errorf("%s: Up = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// A copy whose service has an open restarting or out-of-memory item shows it beside its phase, from the
// item: its count (the item's Crashes, crashesInHour) and its level (the list's mark). A copy of another
// service shows none.
func TestCrashSignal(t *testing.T) {
	e := opsEnv(t)
	now := e.Server.now()
	att := service.Attention{Items: []service.AttentionItem{
		{Kind: service.AttentionRestarting, AppID: "blog", Service: "worker", Crashes: 4, Copies: []string{"c1"}},
		{Kind: service.AttentionOutOfMemory, AppID: "blog", Service: "web", Crashes: 2, Copies: []string{"c2"}},
		{Kind: service.AttentionOutOfMemory, AppID: "blog", Service: "worker", Crashes: 1, Copies: []string{"c3"}},
		{Kind: service.AttentionUnhealthy, AppID: "blog", Service: "api", Copies: []string{"c4"}},
		{Kind: service.AttentionBackupFailed, AppID: "blog", Service: "db"},
	}}
	got := e.Server.crashesOf(now, att)
	if len(got) != 2 {
		t.Fatalf("crash pairs = %+v, want blog/worker and blog/web", got)
	}
	worker, web := got["blog/worker"], got["blog/web"]
	if worker.Restarting == nil || *worker.Restarting != (srvCrash{Mark: "warning", Text: "restarted 4 times in 1 h"}) {
		t.Errorf("restarting = %+v, want warning \"restarted 4 times in 1 h\"", worker.Restarting)
	}
	if worker.OOM == nil || *worker.OOM != (srvCrash{Mark: "failed", Text: "out of memory 1 time in 1 h"}) {
		t.Errorf("worker's oom = %+v", worker.OOM)
	}
	if web.OOM == nil || *web.OOM != (srvCrash{Mark: "failed", Text: "out of memory 2 times in 1 h"}) || web.Restarting != nil {
		t.Errorf("out of memory = %+v, want failed \"out of memory 2 times in 1 h\"", web)
	}
	// A copy killed for memory shows the memory item, any other the restarting one.
	if c := worker.of(service.HostCopy{Copy: deploy.Copy{OutOfMemory: true}}); c != worker.OOM {
		t.Errorf("an out-of-memory copy's signal = %+v", c)
	}
	if c := worker.of(service.HostCopy{}); c != worker.Restarting {
		t.Errorf("another copy's signal = %+v", c)
	}
	if c := web.of(service.HostCopy{}); c != web.OOM {
		t.Errorf("a copy of a service with only an out-of-memory item = %+v", c)
	}
	if c := (srvCrashPair{}).of(service.HostCopy{}); c != nil {
		t.Errorf("no item, signal %+v", c)
	}

	// On the page: the signal is in the Up cell of every copy of that service, beside the phase.
	ctx := context.Background()
	sums, err := e.Svc.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := e.Server.loadPlaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	started := now.Add(-20 * time.Second)
	hour := now.Add(-time.Hour)
	copies := []service.HostCopy{
		{Copy: deploy.Copy{ID: "ffffffff-1", Service: "worker", Status: deploy.CopyStarting, StartedAt: &started}, AppID: "blog",
			Place: app.Place{Project: "default", Environment: "production", Name: "blog"}, Phase: service.CopyPhaseStarting},
		{Copy: deploy.Copy{ID: "aaaaaaaa-2", Service: "api", Status: deploy.CopyHealthy, StartedAt: &hour}, AppID: "blog",
			Place: app.Place{Project: "default", Environment: "production", Name: "blog"}, Phase: service.CopyPhaseRunning},
	}
	r := e.Server.serverCopies(pl, sums, copies, nil, false, 1000, got)
	if c := r.Rows[0].Up.Crash; c == nil || c.Text != "restarted 4 times in 1 h" || c.Mark != "warning" || r.Rows[0].Mark != "rolling" || r.Rows[0].Up.Text != "for 20 s" {
		t.Errorf("a starting copy of a crash-looping service: up %+v, mark %q (the phase's mark stays)", r.Rows[0].Up, r.Rows[0].Mark)
	}
	if r.Rows[1].Up.Crash != nil {
		t.Errorf("a copy of another service has a crash signal: %+v", r.Rows[1].Up)
	}
	var buf strings.Builder
	if err := e.Server.pages["opsserver"].ExecuteTemplate(&buf, "srv-up", r.Rows[0].Up); err != nil {
		t.Fatal(err)
	}
	if want := `for 20 s<span class="cv-srvcrash"><span class="cv-mark cv-mark-warning" aria-hidden="true"></span>restarted 4 times in 1 h</span>`; buf.String() != want {
		t.Errorf("the Up cell:\n%s\nwant\n%s", buf.String(), want)
	}
	buf.Reset()
	if err := e.Server.pages["opsserver"].ExecuteTemplate(&buf, "srv-up", srvUp{Label: "Failed", Word: "failed", Text: "26 s ago · exit 1"}); err != nil {
		t.Fatal(err)
	}
	if want := `<span class="cv-srvupword">failed </span>26 s ago · exit 1`; buf.String() != want {
		t.Errorf("a failed copy's Up cell = %s, want %s", buf.String(), want)
	}
}

// In the fold the Up cell's label is the phase's word, never "Up" for a copy that isn't up.
func TestFoldLabelFollowsThePhase(t *testing.T) {
	e := srvEnv(t)
	_, body := e.get(t, "/ops/servers/lab")
	for id, label := range map[string]string{
		"srv-copy-s1eeeeee-0000": "Failed", "srv-copy-b2dddddd-0000": "Starting", "srv-copy-w2bbbbbb-0000": "Stopping",
		"srv-copy-w1aaaaaa-0000": "Up", "srv-copy-b1cccccc-0000": "Up",
	} {
		row := body[strings.Index(body, `id="`+id+`"`):]
		row = row[:strings.Index(row, `class="cv-srvcell cv-srvup" data-label="`)+len(`class="cv-srvcell cv-srvup" data-label="`)+len(label)+2]
		if !strings.HasSuffix(row, `data-label="`+label+`">`) {
			t.Errorf("%s: the Up cell isn't labelled %q:\n%s", id, label, row[max(0, len(row)-120):])
		}
	}
}
