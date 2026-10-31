package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// One source for the needs-you wording (feature 027, T034a): the service's parts (service.ProblemParts)
// are what the needs-you list, the activity feed and the host pages' banners draw. This walks every
// kind of item and checks that the three say the same words with the same names in mono, so one can't
// drift from the others.

// wordsOf is a sentence as text with its mono parts marked, so two draws can be compared word for word.
func monoWords(s sentence) string {
	var b strings.Builder
	for _, p := range s {
		if p.Mono {
			b.WriteString("[" + p.Text + "]")
		} else {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// itemFor is an item of every kind, with the facts that kind says. An app at home, or an app in
// another project and environment, which says its place in front.
func itemFor(kind service.AttentionKind, elsewhere bool) service.AttentionItem {
	three := uint64(3)
	exp := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	it := service.AttentionItem{Kind: kind, AppID: "blog", AppName: "blog", Service: "web", Copies: []string{"c1", "c2"},
		Deployment: "d1", DeploymentStatus: store.StatusReverted, Version: &three, Schedule: "nightly", ScheduleID: "s1",
		Domain: "bi.example.com", AddressSeen: "185.199.108.153", ExpiresAt: &exp, Mount: "/var/lib/docker", UsedPercent: 87, FreeBytes: 1 << 30,
		InstalledKernel: "7.0.0-40-generic", RunningKernel: "7.0.0-38-generic", Packages: []string{"libc6", "linux-base"},
		Updates: 14, Security: 9}
	if elsewhere {
		it.AppID, it.Project, it.Environment = "shop--staging--web", "shop", "staging"
		it.AppName = "web"
	}
	switch kind {
	case service.AttentionDomainCertExpiring:
		it.Detail = "The certificate for bi.example.com expires in 6 days and hasn't been renewed."
	case service.AttentionScheduleFailed:
		it.Detail = "Exited with code 3."
	case service.AttentionScheduleSkipping:
		it.Detail = "nightly has been skipped 3 times: each run takes longer than the time between runs."
	case service.AttentionDeployFailed:
		it.Detail = "ghcr.io: x:1: image not found"
	}
	switch kind {
	case service.AttentionNoBackupDestination, service.AttentionDiskNearlyFull, service.AttentionOSRebootRequired, service.AttentionOSSecurityUpdates:
		it.AppID, it.Project, it.Environment, it.AppName, it.Service = "", "", "", "", ""
	}
	return it
}

func TestEveryKindSaysTheSameWordsEverywhere(t *testing.T) {
	s := newConsoleEnv(t).Server
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	pl := places{Projects: []service.Project{{ID: "shop", DisplayName: "Shop"}}}
	for _, kind := range service.AttentionKinds {
		for _, elsewhere := range []bool{false, true} {
			name := string(kind)
			if elsewhere {
				name += " in another place"
			}
			t.Run(name, func(t *testing.T) {
				it := itemFor(kind, elsewhere)
				parts := service.ProblemParts(it, pl.display)
				if strings.Contains(plainOf(parts), "needs you (") {
					t.Fatalf("%s has no wording of its own: %q", kind, plainOf(parts))
				}
				// The feed: the entry's parts as the Overview draws them.
				feed := monoWords(opsEventOf(service.ActivityEntry{Parts: parts}).Sentence)
				// The list: the row of the needs-you list.
				list := monoWords(s.attentionRow(now, it, pl, nil).Text)
				// A failed deploy's row adds what runs now, which needs the app's state; with none given, as
				// here, nothing is added and the row is the feed's words.
				if list != feed {
					t.Errorf("list = %q, feed = %q", list, feed)
				}
				// The banners: the host kinds' banners on the Server and Updates pages.
				att := service.Attention{Items: []service.AttentionItem{it}}
				for _, b := range s.hostItems(now, att) {
					if got := monoWords(b.Line); got != feed {
						t.Errorf("the Server page's banner = %q, feed = %q", got, feed)
					}
				}
				for _, b := range upBanners(att, pl) {
					if got := monoWords(b.Line); got != feed {
						t.Errorf("the Updates page's banner = %q, feed = %q", got, feed)
					}
				}
			})
		}
	}
}

// The kernels, the packages and the versions of the OS items are mono, and a kernel never breaks.
func TestOSWordsDrawNamesInMono(t *testing.T) {
	s := newConsoleEnv(t).Server
	now := time.Now()
	row := s.attentionRow(now, itemFor(service.AttentionOSRebootRequired, false), places{}, nil)
	if got := monoWords(row.Text); got != "This server needs a reboot: kernel [7.0.0-40] is installed, but [7.0.0-38] is still running." {
		t.Errorf("kernels = %q", got)
	}
	for _, p := range row.Text {
		if p.Mono && !p.Nowrap {
			t.Errorf("the mono part %q can break", p.Text)
		}
	}
	it := itemFor(service.AttentionOSRebootRequired, false)
	it.InstalledKernel = ""
	if got := monoWords(s.attentionRow(now, it, places{}, nil).Text); got != "This server needs a reboot: [libc6] and [linux-base] were updated and need a reboot." {
		t.Errorf("packages = %q", got)
	}
}

func plainOf(parts []service.TextPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
