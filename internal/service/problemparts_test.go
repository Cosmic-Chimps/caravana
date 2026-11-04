package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The needs-you sentence, as parts, for each kind: the words the console's list says, with the
// names set in mono. (Feature 027: the activity feed stores it when the event is written.)
func TestProblemParts(t *testing.T) {
	display := func(id string) string {
		if id == "shop" {
			return "Shop"
		}
		return id
	}
	three := uint64(3)
	exp := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		it   AttentionItem
		want string // plain text
		mono string // the mono parts, joined by "|"
	}{
		{"schedule failed", AttentionItem{Kind: AttentionScheduleFailed, AppID: "clock", Schedule: "slow", Detail: "Stopped at the 5 min limit."},
			"clock: the last run of slow failed. Stopped at the 5 min limit.", "clock|slow"},
		{"out of memory", AttentionItem{Kind: AttentionOutOfMemory, AppID: "metabase", Service: "web", Copies: []string{"a", "b"}},
			"2 copies of metabase/web ran out of memory.", "metabase/web"},
		{"restarting one", AttentionItem{Kind: AttentionRestarting, AppID: "whoami", Service: "web", Copies: []string{"a"}},
			"1 copy of whoami/web keeps restarting.", "whoami/web"},
		{"unhealthy elsewhere", AttentionItem{Kind: AttentionUnhealthy, AppID: "shop--staging--web", Project: "shop", Environment: "staging", AppName: "web",
			Service: "api", Copies: []string{"a", "b"}},
			"2 copies of Shop › staging › web/api are failing its health check.", "staging|web/api"},
		{"disk", AttentionItem{Kind: AttentionDiskNearlyFull, Mount: "/var/lib/docker", UsedPercent: 87, FreeBytes: 1 << 30},
			"The disk at /var/lib/docker is 87% full: 1.0 GB left.", "/var/lib/docker"},
		{"dns", AttentionItem{Kind: AttentionDomainDNSElsewhere, AppID: "blog", Domain: "bi.example.com", AddressSeen: "185.199.108.153"},
			"blog: bi.example.com points at 185.199.108.153, which isn't this host.", "blog|bi.example.com|185.199.108.153"},
		{"dns, also here", AttentionItem{Kind: AttentionDomainDNSElsewhere, AppID: "blog", Domain: "bi.example.com", AddressSeen: "185.199.108.153", AlsoHere: true},
			"blog: bi.example.com points here and at 185.199.108.153: remove the record to 185.199.108.153.", "blog|bi.example.com|185.199.108.153|185.199.108.153"},
		{"certificate", AttentionItem{Kind: AttentionDomainCertExpiring, AppID: "blog", Domain: "bi.example.com", ExpiresAt: &exp,
			Detail: "The certificate for bi.example.com expires in 6 days and hasn't been renewed."},
			"blog: the certificate for bi.example.com expires in 6 days and hasn't been renewed.", "blog|bi.example.com"},
		{"deploy reverted", AttentionItem{Kind: AttentionDeployFailed, AppID: "blog", Version: &three, DeploymentStatus: store.StatusReverted},
			"blog: v3 failed its health checks.", "blog|v3"},
		{"backup", AttentionItem{Kind: AttentionBackupFailed, AppID: "blog", Service: "db"},
			"The latest backup of blog/db failed.", "blog/db"},
		{"no destination", AttentionItem{Kind: AttentionNoBackupDestination},
			"Backups are set up, but no backup destination is.", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ProblemParts(tc.it, display)
			if text := plainText(got); text != tc.want {
				t.Errorf("sentence = %q, want %q", text, tc.want)
			}
			var monos []string
			for _, p := range got {
				if p.Mono {
					monos = append(monos, p.Text)
				}
			}
			if strings.Join(monos, "|") != tc.mono {
				t.Errorf("mono parts = %q, want %q", strings.Join(monos, "|"), tc.mono)
			}
			// What is stored is what is read back.
			if back, ok := decodeParts(encodeParts(got)); !ok || plainText(back) != tc.want {
				t.Errorf("round trip = %v %q", ok, plainText(back))
			}
		})
	}
}

// One event, one row: a problem with a record of its own (a failed deploy) isn't listed as opened,
// but its resolution is, with the sentence the needs-you list had, as parts.
func TestActivityListsAProblemOnceAndItsResolutionAlways(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	e := newNotifyEnv(t)
	e.clockAt(t0)
	e.svc.resolveAfter = 5 * time.Minute

	first, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, first.Deployment.ID)
	desc := "Failed due to unhealthy allocations - rolling back to job version 0"
	e.nomad.Script("whoami", 1, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: desc})
	second, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, second.Deployment.ID)
	e.clockAt(t0.Add(time.Minute))
	e.check(t)

	rows, err := e.store.ListSentProblems(ctx)
	if err != nil || len(rows) != 1 || rows[0].Kind != "deploy_failed" {
		t.Fatalf("sent problems = %+v, %v", rows, err)
	}
	if got, ok := decodeParts(rows[0].Sentence); !ok || !strings.HasPrefix(plainText(got), "whoami: ") {
		t.Errorf("the stored sentence = %q", rows[0].Sentence)
	}
	needs := func() (opened, resolved []ActivityEntry) {
		got, err := e.svc.Activity(ctx, ActivityQuery{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, en := range got {
			if en.Kind != ActivityNeedsYou {
				continue
			}
			if strings.HasPrefix(en.Sentence, "Resolved: ") {
				resolved = append(resolved, en)
			} else {
				opened = append(opened, en)
			}
		}
		return opened, resolved
	}
	if opened, resolved := needs(); len(opened) != 0 || len(resolved) != 0 {
		t.Fatalf("a failed deploy is in the feed as %v and %v: its own row says it", opened, resolved)
	}

	third, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, third.Deployment.ID)
	e.clockAt(t0.Add(10 * time.Minute))
	e.check(t) // gone: not yet resolved, it was seen a minute ago
	e.clockAt(t0.Add(20 * time.Minute))
	e.check(t)
	opened, resolved := needs()
	if len(opened) != 0 || len(resolved) != 1 {
		t.Fatalf("after the resolution: opened %v, resolved %v; want only the resolution", opened, resolved)
	}
	r := resolved[0]
	if r.Status != ActivityHealthy || r.Parts[0].Text != "Resolved: " || r.Parts[1].Text != "whoami" || !r.Parts[1].Mono {
		t.Errorf("resolved entry = %+v", r)
	}
}

// labelParts is the one wording of where an app is: the bare name at home, else the place muted
// (the environment in mono) and the name in mono, with the suffix glued to the name.
func TestLabelParts(t *testing.T) {
	display := func(id string) string { return map[string]string{"shop": "Shop"}[id] }
	home := labelParts(display, app.Place{Project: app.DefaultProject, Environment: app.DefaultEnvironment, Name: "blog"}, "blog", "/web")
	if len(home) != 1 || home[0] != mono("blog/web") {
		t.Errorf("home = %+v", home)
	}
	got := labelParts(display, app.Place{Project: "shop", Environment: "staging", Name: "web"}, "shop--staging--web", "")
	want := []TextPart{{Text: "Shop › ", Muted: true}, {Text: "staging", Mono: true, Muted: true}, {Text: " › ", Muted: true}, {Text: "web", Mono: true}}
	if len(got) != len(want) {
		t.Fatalf("label = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if plainText(got) != "Shop › staging › web" {
		t.Errorf("text = %q", plainText(got))
	}
}
