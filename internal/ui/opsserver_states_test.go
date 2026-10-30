package ui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

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
