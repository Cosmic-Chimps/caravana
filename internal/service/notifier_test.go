package service

import (
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const publicBase = "https://caravana.example.com"

// notedAt is "when it was noticed", for items that know no time of their own.
var notedAt = time.Date(2026, 10, 17, 3, 0, 41, 0, time.UTC)

// Every kind of needs-you item has a title, a sentence in the console's words,
// its raw detail and a link to the page the console would send the owner to
// (research R6). Without a public address there is no link.
func TestProblemEvent(t *testing.T) {
	itemAt := time.Date(2026, 10, 17, 2, 59, 0, 0, time.UTC)
	tests := []struct {
		name  string
		item  AttentionItem
		title string
		text  string
		path  string // the link, after the public address
	}{
		{
			name:  "deploy failed with a version",
			item:  AttentionItem{Kind: AttentionDeployFailed, AppID: "hollo", Deployment: "dep_1", DeploymentStatus: store.StatusFailed, Version: ver(8), Detail: "deadline reached"},
			title: "hollo: deploy v8 failed", text: "hollo's deploy v8 failed.", path: "/apps/hollo/deployments#deployment-dep_1",
		},
		{
			name:  "deploy reverted",
			item:  AttentionItem{Kind: AttentionDeployFailed, AppID: "hollo", Deployment: "dep_2", DeploymentStatus: store.StatusReverted, Version: ver(9)},
			title: "hollo: deploy v9 was reverted", text: "hollo's deploy v9 was reverted.", path: "/apps/hollo/deployments#deployment-dep_2",
		},
		{
			name:  "deploy failed before Nomad had a version",
			item:  AttentionItem{Kind: AttentionDeployFailed, AppID: "hollo", Deployment: "dep_3", DeploymentStatus: store.StatusFailed, Detail: "image not found"},
			title: "hollo: deploy failed", text: "hollo's deploy failed.", path: "/apps/hollo/deployments#deployment-dep_3",
		},
		{
			name:  "one copy out of memory",
			item:  AttentionItem{Kind: AttentionOutOfMemory, AppID: "blog", Service: "web", Copies: []string{"a1"}, Detail: "OOM Killed"},
			title: "blog/web: ran out of memory", text: "1 copy of blog/web ran out of memory.", path: "/apps/blog",
		},
		{
			name:  "two copies out of memory",
			item:  AttentionItem{Kind: AttentionOutOfMemory, AppID: "blog", Service: "web", Copies: []string{"a1", "a2"}},
			title: "blog/web: ran out of memory", text: "2 copies of blog/web ran out of memory.", path: "/apps/blog",
		},
		{
			name:  "one copy restarting",
			item:  AttentionItem{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"a1"}},
			title: "blog/web: keeps restarting", text: "1 copy of blog/web keeps restarting.", path: "/apps/blog",
		},
		{
			name:  "three copies restarting",
			item:  AttentionItem{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"a1", "a2", "a3"}},
			title: "blog/web: keeps restarting", text: "3 copies of blog/web keep restarting.", path: "/apps/blog",
		},
		{
			name:  "one copy unhealthy",
			item:  AttentionItem{Kind: AttentionUnhealthy, AppID: "blog", Service: "web", Copies: []string{"a1"}},
			title: "blog/web: failing its health check", text: "1 copy of blog/web is failing its health check.", path: "/apps/blog",
		},
		{
			name:  "two copies unhealthy",
			item:  AttentionItem{Kind: AttentionUnhealthy, AppID: "blog", Service: "web", Copies: []string{"a1", "a2"}},
			title: "blog/web: failing its health check", text: "2 copies of blog/web are failing its health check.", path: "/apps/blog",
		},
		{
			name:  "backup failed",
			item:  AttentionItem{Kind: AttentionBackupFailed, AppID: "kuma", Service: "uptime-kuma", When: itemAt, Detail: "upload of data: timeout"},
			title: "kuma: the backup of uptime-kuma failed", text: "The latest backup of kuma/uptime-kuma failed.", path: "/apps/kuma/backups",
		},
		{
			name:  "restore failed",
			item:  AttentionItem{Kind: AttentionRestoreFailed, AppID: "kuma", Service: "uptime-kuma"},
			title: "kuma: the restore of uptime-kuma failed", text: "The latest restore of kuma/uptime-kuma failed.", path: "/apps/kuma/backups",
		},
		{
			name:  "no backup destination",
			item:  AttentionItem{Kind: AttentionNoBackupDestination},
			title: "Backups have nowhere to go", text: "Backups are set up, but no backup destination is.", path: "/settings/backup-storage",
		},
		{
			name:  "a kind added later still says something",
			item:  AttentionItem{Kind: "something_new", AppID: "blog", Service: "web"},
			title: "blog/web: needs you (something_new)", text: "blog/web: needs you (something_new).", path: "/apps/blog",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := problemEvent(tc.item, publicBase, notedAt)
			if ev.Type != notify.EventProblemOpened || ev.Kind != string(tc.item.Kind) ||
				ev.AppID != tc.item.AppID || ev.Service != tc.item.Service {
				t.Errorf("identity = %q %q %q %q", ev.Type, ev.Kind, ev.AppID, ev.Service)
			}
			if ev.Title != tc.title || ev.Text != tc.text {
				t.Errorf("words = %q / %q, want %q / %q", ev.Title, ev.Text, tc.title, tc.text)
			}
			if ev.Detail != tc.item.Detail {
				t.Errorf("Detail = %q, want the raw %q", ev.Detail, tc.item.Detail)
			}
			if ev.URL != publicBase+tc.path {
				t.Errorf("URL = %q, want %q", ev.URL, publicBase+tc.path)
			}
			wantAt := notedAt
			if !tc.item.When.IsZero() {
				wantAt = tc.item.When
			}
			if !ev.At.Equal(wantAt) {
				t.Errorf("At = %v, want %v", ev.At, wantAt)
			}
			if noLink := problemEvent(tc.item, "", notedAt); noLink.URL != "" {
				t.Errorf("URL without a public address = %q, want none", noLink.URL)
			}
		})
	}
}

func TestProblemEventLimitsLongDetail(t *testing.T) {
	long := strings.Repeat("x", maxEventDetail+100)
	ev := problemEvent(AttentionItem{Kind: AttentionBackupFailed, AppID: "kuma", Service: "db", Detail: long}, "", notedAt)
	if got := []rune(ev.Detail); len(got) != maxEventDetail+1 || got[len(got)-1] != '…' {
		t.Errorf("Detail has %d characters, want %d and an ellipsis", len(got), maxEventDetail)
	}
}

// A resolved message reuses the title that was sent, links to the same page and
// is a problem.resolved event.
func TestResolvedEvent(t *testing.T) {
	tests := []struct {
		name string
		p    store.SentProblem
		path string
	}{
		{"deploy", store.SentProblem{Key: "deploy_failed|hollo||dep_1", Kind: "deploy_failed", AppID: "hollo", Summary: "hollo: deploy v8 failed"}, "/apps/hollo/deployments#deployment-dep_1"},
		{"copies", store.SentProblem{Key: "restarting|blog|web|", Kind: "restarting", AppID: "blog", Service: "web", Summary: "blog/web: keeps restarting"}, "/apps/blog"},
		{"backup", store.SentProblem{Key: "backup_failed|kuma|db|", Kind: "backup_failed", AppID: "kuma", Service: "db", Summary: "kuma: the backup of db failed"}, "/apps/kuma/backups"},
		{"destination", store.SentProblem{Key: "no_backup_destination|||", Kind: "no_backup_destination", Summary: "Backups have nowhere to go"}, "/settings/backup-storage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := resolvedEvent(tc.p, publicBase, notedAt)
			if ev.Type != notify.EventProblemResolved || ev.Kind != tc.p.Kind || ev.AppID != tc.p.AppID || ev.Service != tc.p.Service {
				t.Errorf("identity = %+v", ev)
			}
			if ev.Title != "Resolved: "+tc.p.Summary || ev.Text == "" || !ev.At.Equal(notedAt) {
				t.Errorf("words = %q / %q at %v", ev.Title, ev.Text, ev.At)
			}
			if ev.URL != publicBase+tc.path {
				t.Errorf("URL = %q, want %q", ev.URL, publicBase+tc.path)
			}
			if resolvedEvent(tc.p, "", notedAt).URL != "" {
				t.Error("a link without a public address")
			}
		})
	}
}

// Each way a deployment can end is told once, with the trigger in words.
func TestDeployEvent(t *testing.T) {
	updated := time.Date(2026, 10, 17, 4, 0, 0, 0, time.UTC)
	rec := func(status store.Status, version *uint64, trigger app.Trigger, errText string) store.DeploymentRecord {
		return store.DeploymentRecord{
			Deployment: app.Deployment{ID: "dep_7", Trigger: trigger}, AppID: "hollo",
			Status: status, JobVersion: version, Error: errText, UpdatedAt: updated,
		}
	}
	tests := []struct {
		name    string
		rec     store.DeploymentRecord
		typ     string
		title   string
		text    string
		trigger string
	}{
		{"healthy from the console", rec(store.StatusHealthy, ver(8), app.TriggerConsole, ""),
			notify.EventDeploySucceeded, "hollo: deployed v8", "hollo's deploy v8 is healthy, started from the console.", "console"},
		{"healthy by the API", rec(store.StatusHealthy, ver(2), app.TriggerManual, ""),
			notify.EventDeploySucceeded, "hollo: deployed v2", "hollo's deploy v2 is healthy, started by the API.", "manual"},
		{"failed by the webhook", rec(store.StatusFailed, ver(8), app.TriggerWebhook, "deadline reached"),
			notify.EventDeployFailed, "hollo: deploy v8 failed", "hollo's deploy v8 failed, started by its webhook.", "webhook"},
		{"reverted", rec(store.StatusReverted, ver(9), app.TriggerConsole, "unhealthy allocations"),
			notify.EventDeployFailed, "hollo: deploy v9 was reverted", "hollo's deploy v9 was reverted, started from the console.", "console"},
		{"superseded", rec(store.StatusSuperseded, ver(10), app.TriggerManual, "replaced by deployment dep_8"),
			notify.EventDeployFailed, "hollo: deploy v10 was replaced",
			"hollo's deploy v10 was replaced by a newer deploy before it ended, started by the API.", "manual"},
		{"failed before Nomad (no version)", rec(store.StatusFailed, nil, app.TriggerManual, "image not found"),
			notify.EventDeployFailed, "hollo: deploy failed", "hollo's deploy failed, started by the API.", "manual"},
		{"an unknown trigger says nothing of it", rec(store.StatusHealthy, ver(1), "scheduled", ""),
			notify.EventDeploySucceeded, "hollo: deployed v1", "hollo's deploy v1 is healthy.", "scheduled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := deployEvent(tc.rec, publicBase)
			if !ok {
				t.Fatal("deployEvent said there is nothing to tell")
			}
			if ev.Type != tc.typ || ev.Title != tc.title || ev.Text != tc.text {
				t.Errorf("event = %q %q / %q, want %q %q / %q", ev.Type, ev.Title, ev.Text, tc.typ, tc.title, tc.text)
			}
			if ev.AppID != "hollo" || ev.Deployment != "dep_7" || ev.Trigger != tc.trigger || ev.Outcome != string(tc.rec.Status) {
				t.Errorf("deploy fields = %+v", ev)
			}
			if (ev.Version == nil) != (tc.rec.JobVersion == nil) || (ev.Version != nil && *ev.Version != *tc.rec.JobVersion) {
				t.Errorf("Version = %v, want %v", ev.Version, tc.rec.JobVersion)
			}
			if ev.Detail != tc.rec.Error || !ev.At.Equal(updated) {
				t.Errorf("Detail = %q at %v", ev.Detail, ev.At)
			}
			if ev.URL != publicBase+"/apps/hollo/deployments#deployment-dep_7" {
				t.Errorf("URL = %q", ev.URL)
			}
			if noLink, _ := deployEvent(tc.rec, ""); noLink.URL != "" {
				t.Errorf("URL without a public address = %q", noLink.URL)
			}
		})
	}

	for _, status := range []store.Status{store.StatusPending, store.StatusRunning} {
		if _, ok := deployEvent(rec(status, nil, app.TriggerManual, ""), ""); ok {
			t.Errorf("a %s deployment has an event; it has not ended", status)
		}
	}
}
