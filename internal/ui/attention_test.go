package ui

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Each kind of item becomes one sentence, a mark, one action and its link.
func TestAttentionRows(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, blogApp(),
		seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 7},
		seedDeploy{ID: "b2", Trigger: app.TriggerWebhook, Status: store.StatusReverted, Version: 8, Error: "health checks failed"})
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: "no such image.\n"})
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	then := now.Add(-5 * time.Minute)
	v8, v7 := uint64(8), uint64(7)
	// What Nomad says runs now, for the deploy item's second sentence: blog keeps v7, whoami has nothing.
	sums := map[string]service.AppSummary{
		"blog":   {AppRecord: store.AppRecord{App: app.App{ID: "blog"}}, Status: service.AppStatus{State: service.StateHealthy, HealthyCopies: 1, WantedCopies: 1, LiveVersion: &v7}},
		"whoami": {AppRecord: store.AppRecord{App: app.App{ID: "whoami"}}, Status: service.AppStatus{State: service.StateNotRunning, WantedCopies: 1}},
	}

	tests := []struct {
		name   string
		item   service.AttentionItem
		mark   string
		text   string
		action string
		href   string
	}{
		{"a reverted deploy", service.AttentionItem{Kind: service.AttentionDeployFailed, AppID: "blog", Deployment: "b2", DeploymentStatus: store.StatusReverted, Version: &v8, Detail: "health checks failed"},
			"failed", "blog: v8 failed its health checks. v7 keeps running.", "View deployment", "/apps/blog/deployments#deployment-b2"},
		{"a failed deploy that never got a version, with nothing running", service.AttentionItem{Kind: service.AttentionDeployFailed, AppID: "whoami", Deployment: "w1", DeploymentStatus: store.StatusFailed, Detail: "no such image.\n"},
			"failed", "whoami: This deploy didn't start. No copies are running.", "View deployment", "/apps/whoami/deployments#deployment-w1"},
		{"a failed deploy whose cause is known", service.AttentionItem{Kind: service.AttentionDeployFailed, AppID: "whoami", Deployment: "w1", DeploymentStatus: store.StatusFailed, Detail: "ghcr.io: x:1: image not found"},
			"failed", "whoami: This deploy didn't start: the image wasn't found. No copies are running.", "View deployment", "/apps/whoami/deployments#deployment-w1"},
		{"a failed rollout", service.AttentionItem{Kind: service.AttentionDeployFailed, AppID: "blog", Deployment: "b2", DeploymentStatus: store.StatusFailed, Version: &v8},
			"failed", "blog: v8 failed during its rollout. v7 keeps running.", "View deployment", "/apps/blog/deployments#deployment-b2"},
		{"one copy out of memory", service.AttentionItem{Kind: service.AttentionOutOfMemory, AppID: "blog", Service: "web", Copies: []string{"c1"}},
			"failed", "1 copy of blog/web ran out of memory.", "Raise memory", "/apps/blog/configure#svc-web-memory"},
		{"two copies out of memory", service.AttentionItem{Kind: service.AttentionOutOfMemory, AppID: "blog", Service: "web", Copies: []string{"c1", "c2"}},
			"failed", "2 copies of blog/web ran out of memory.", "Raise memory", "/apps/blog/configure#svc-web-memory"},
		{"one copy restarting", service.AttentionItem{Kind: service.AttentionRestarting, AppID: "blog", Service: "worker", Copies: []string{"c1"}},
			"warning", "1 copy of blog/worker keeps restarting.", "View logs", "/apps/blog/logs?copy=c1"},
		{"two copies restarting link to the first", service.AttentionItem{Kind: service.AttentionRestarting, AppID: "blog", Service: "worker", Copies: []string{"c1", "c2"}},
			"warning", "2 copies of blog/worker keep restarting.", "View logs", "/apps/blog/logs?copy=c1"},
		{"one copy unhealthy", service.AttentionItem{Kind: service.AttentionUnhealthy, AppID: "blog", Service: "web", Copies: []string{"c1"}},
			"warning", "1 copy of blog/web is failing its health check.", "View logs", "/apps/blog/logs?copy=c1"},
		{"two copies unhealthy", service.AttentionItem{Kind: service.AttentionUnhealthy, AppID: "blog", Service: "web", Copies: []string{"c1", "c2"}},
			"warning", "2 copies of blog/web are failing its health check.", "View logs", "/apps/blog/logs?copy=c1"},
		{"a failed backup", service.AttentionItem{Kind: service.AttentionBackupFailed, AppID: "blog", Service: "db"},
			"failed", "The latest backup of blog/db failed.", "Open backups", "/apps/blog/backups"},
		{"a failed restore", service.AttentionItem{Kind: service.AttentionRestoreFailed, AppID: "blog", Service: "db"},
			"failed", "The latest restore of blog/db failed.", "Open backups", "/apps/blog/backups"},
		{"no destination", service.AttentionItem{Kind: service.AttentionNoBackupDestination},
			"warning", "Backups are set up, but no backup destination is.", "Set destination", "/settings/backup-storage"},
		{"a failed schedule run", service.AttentionItem{Kind: service.AttentionScheduleFailed, AppID: "blog", Service: "web", Schedule: "nightly", ScheduleID: "abc123", Detail: "Exited with code 3."},
			"failed", "blog: the last run of nightly failed. Exited with code 3.", "Open schedule", "/schedules?open=abc123#schedule-abc123"},
		{"a schedule being skipped", service.AttentionItem{Kind: service.AttentionScheduleSkipping, AppID: "blog", Service: "web", Schedule: "sync", ScheduleID: "def456", Detail: "sync has been skipped 3 times: each run takes longer than the time between runs."},
			"warning", "blog: sync has been skipped 3 times: each run takes longer than the time between runs.", "Open schedule", "/schedules?open=def456#schedule-def456"},
		{"a domain pointing elsewhere", service.AttentionItem{Kind: service.AttentionDomainDNSElsewhere, AppID: "blog", Service: "web", Domain: "bi.example.com", AddressSeen: "185.199.108.153"},
			"failed", "blog: bi.example.com points at 185.199.108.153, which isn't this host.", "Open domain", "/domains?open=bi.example.com#domain-bi.example.com"},
		{"a domain pointing elsewhere with no address", service.AttentionItem{Kind: service.AttentionDomainDNSElsewhere, AppID: "blog", Service: "web", Domain: "bi.example.com"},
			"failed", "blog: bi.example.com points at another host.", "Open domain", "/domains?open=bi.example.com#domain-bi.example.com"},
		{"a certificate expiring in days", service.AttentionItem{Kind: service.AttentionDomainCertExpiring, AppID: "blog", Service: "web", Domain: "bi.example.com", ExpiresAt: new(now.Add(6*24*time.Hour + time.Hour))},
			"warning", "blog: the certificate for bi.example.com expires in 6 days and hasn't been renewed.", "Open domain", "/domains?open=bi.example.com#domain-bi.example.com"},
		{"a certificate expiring in a day", service.AttentionItem{Kind: service.AttentionDomainCertExpiring, AppID: "blog", Service: "web", Domain: "bi.example.com", ExpiresAt: new(now.Add(30 * time.Hour))},
			"warning", "blog: the certificate for bi.example.com expires in 1 day and hasn't been renewed.", "Open domain", "/domains?open=bi.example.com#domain-bi.example.com"},
		{"a certificate expiring today", service.AttentionItem{Kind: service.AttentionDomainCertExpiring, AppID: "blog", Service: "web", Domain: "bi.example.com", ExpiresAt: new(now.Add(3 * time.Hour))},
			"warning", "blog: the certificate for bi.example.com expires today and hasn't been renewed.", "Open domain", "/domains?open=bi.example.com#domain-bi.example.com"},
		{"a certificate that expired", service.AttentionItem{Kind: service.AttentionDomainCertExpiring, AppID: "blog", Service: "web", Domain: "bi.example.com", ExpiresAt: new(now.Add(-time.Hour))},
			"warning", "blog: the certificate for bi.example.com has expired and hasn't been renewed.", "Open domain", "/domains?open=bi.example.com#domain-bi.example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.item.When = then
			row := e.Server.attentionRow(now, tc.item, places{}, sums)
			if got := row.Text.String(); got != tc.text {
				t.Errorf("text = %q, want %q", got, tc.text)
			}
			if row.Mark != tc.mark || row.Action != tc.action || row.Href != tc.href {
				t.Errorf("row = mark %q, action %q, href %q; want %q, %q, %q", row.Mark, row.Action, row.Href, tc.mark, tc.action, tc.href)
			}
			if row.When.Text != "5 minutes ago" {
				t.Errorf("when = %q, want 5 minutes ago", row.When.Text)
			}
		})
	}

	t.Run("names are set apart as mono", func(t *testing.T) {
		row := e.Server.attentionRow(now, tests[4].item, places{}, sums)
		var monos []string
		for _, p := range row.Text {
			if p.Mono {
				monos = append(monos, p.Text)
			}
		}
		if len(monos) != 1 || monos[0] != "blog/web" {
			t.Errorf("mono parts = %v, want blog/web", monos)
		}
	})

	t.Run("Nomad unreadable: the sentence doesn't say what runs", func(t *testing.T) {
		down := map[string]service.AppSummary{"blog": {Status: service.AppStatus{State: service.StateUnknown}}}
		got := e.Server.attentionRow(now, tests[0].item, places{}, down).Text.String()
		if got != "blog: v8 failed its health checks." {
			t.Errorf("text = %q", got)
		}
	})

	t.Run("the raw error is not in the row", func(t *testing.T) {
		item := service.AttentionItem{Kind: service.AttentionDeployFailed, AppID: "blog", Deployment: "b2", DeploymentStatus: store.StatusFailed, Detail: strings.Repeat("x ", 300)}
		if got := e.Server.attentionRow(now, item, places{}, nil).Text.String(); strings.Contains(got, "x x") {
			t.Errorf("text = %q, want the sentence only: the error is on the deployment", got)
		}
	})
}

// US5 on the Apps page: the list under the headline, newest first, with its links.
func TestAppsNeedsYou(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: "no such image"})
	e.seed(t, blogApp(), seedDeploy{ID: "b1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 7})
	e.Nomad.SetCopies("blog",
		api.AllocationListStub{ID: "w1c", TaskGroup: "web", JobVersion: 7, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {Restarts: 1, LastRestart: time.Now().Add(-time.Minute), Events: []*api.TaskEvent{
				{Type: api.TaskTerminated, Details: map[string]string{"oom_killed": "true"}}}}}},
		api.AllocationListStub{ID: "k1", TaskGroup: "worker", JobVersion: 7, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"worker": {Restarts: 4, LastRestart: time.Now().Add(-2 * time.Minute)}}})

	resp, page := e.get(t, "/apps")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps = %d", resp.StatusCode)
	}
	for _, want := range []string{
		`aria-label="Needs you"`,
		`hx-get="/parts/attention" hx-trigger="sse:attention"`,
		`href="/apps/whoami/deployments#deployment-w1"`,
		`href="/apps/blog/configure#svc-web-memory"`,
		`href="/apps/blog/logs?copy=k1"`,
		`>Raise memory</a>`, `>View logs</a>`, `>View deployment</a>`,
		`cv-mark cv-mark-failed`, `cv-mark cv-mark-warning`,
		"ran out of memory.", "keeps restarting.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the Apps page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Nothing needs you.") {
		t.Error("the page says nothing needs the owner")
	}
	// The failed deploy says what runs now: nothing, here.
	if got := text(page); !strings.Contains(got, "whoami: This deploy didn't start. No copies are running.") {
		t.Errorf("the needs-you list lacks the failed deploy's sentence:\n%s", got)
	}
	// The headline counts the apps on the list.
	if got, want := h1Text(t, page), "2 apps need you: blog, whoami."; got != want {
		t.Errorf("headline = %q, want %q", got, want)
	}
	// Newest first: the out-of-memory restart (1 minute ago) before the other restart (2 minutes ago).
	if i, j := strings.Index(page, "ran out of memory."), strings.Index(page, "keeps restarting."); i < 0 || j < 0 || i > j {
		t.Errorf("items aren't newest first (memory at %d, restarts at %d)", i, j)
	}

	t.Run("the part is the region the page draws", func(t *testing.T) {
		resp, part := e.get(t, "/parts/attention")
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("GET /parts/attention = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if strings.Contains(part, "<html") || strings.Contains(part, "<h1") || strings.Contains(part, `role="table"`) {
			t.Errorf("the part is more than the region:\n%s", part)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		if part = strings.TrimSpace(part); part == "" || !strings.Contains(page, part) {
			t.Errorf("the page doesn't contain what /parts/attention answers:\n%s", part)
		}
		// And the apps part carries it too, so a refresh of the apps region can't lose it.
		_, apps := e.get(t, "/parts/apps")
		if !strings.Contains(apps, part) {
			t.Errorf("/parts/apps lacks the needs-you region")
		}
	})
}

func TestAppsNeedsYouWhenNothingDoes(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
	e.copies("whoami", "web", 7, 2, 0)

	_, page := e.get(t, "/apps")
	if !strings.Contains(page, "Nothing needs you.") {
		t.Errorf("the page doesn't say nothing needs the owner:\n%s", page)
	}
	if strings.Contains(page, "cv-need ") {
		t.Error("the page lists an item")
	}
	if _, part := e.get(t, "/parts/attention"); !strings.Contains(part, "Nothing needs you.") {
		t.Errorf("the part doesn't say it:\n%s", part)
	}

	t.Run("an item that is resolved is gone on the next read", func(t *testing.T) {
		e.Nomad.SetCopies("whoami", api.AllocationListStub{TaskGroup: "web", JobVersion: 7, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {Restarts: 5, LastRestart: time.Now()}}})
		if _, part := e.get(t, "/parts/attention"); !strings.Contains(part, "keeps restarting") {
			t.Fatalf("the restarting copy isn't listed:\n%s", part)
		}
		e.copies("whoami", "web", 7, 2, 0)
		if _, part := e.get(t, "/parts/attention"); !strings.Contains(part, "Nothing needs you.") {
			t.Errorf("the item stayed after its condition went:\n%s", part)
		}
	})
}

// Without Nomad the copy items may be missing, so the list says that instead of
// "Nothing needs you.", and the stored items stay.
func TestAppsNeedsYouWhenNomadIsDown(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: "no such image"})
	e.Nomad.Down()

	for _, path := range []string{"/apps", "/parts/attention"} {
		resp, body := e.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		if !strings.Contains(body, "Copies couldn&#39;t be checked: Nomad can&#39;t be reached.") &&
			!strings.Contains(body, "Copies couldn't be checked: Nomad can't be reached.") {
			t.Errorf("GET %s doesn't say copies couldn't be checked:\n%s", path, body)
		}
		if got := text(body); !strings.Contains(got, "whoami: This deploy didn't start.") || strings.Contains(got, "No copies are running.") {
			t.Errorf("GET %s lost the stored item:\n%s", path, body)
		}
	}

	t.Run("nothing stored to list: no claim that nothing needs the owner", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
		e.Nomad.Down()
		_, part := e.get(t, "/parts/attention")
		if strings.Contains(part, "Nothing needs you.") || !strings.Contains(part, "Nomad can") {
			t.Errorf("part = %s", part)
		}
	})
}

// A copy that was killed for memory says so on the Running tab.
func TestRunningTabSaysOutOfMemory(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 7})
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "oom00001", TaskGroup: "web", JobVersion: 7, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {Events: []*api.TaskEvent{{Type: api.TaskTerminated, Details: map[string]string{"oom_killed": "true"}}}}}},
		api.AllocationListStub{ID: "fine0001", TaskGroup: "web", JobVersion: 7, ClientStatus: "running"})

	_, part := e.get(t, "/parts/apps/whoami/copies")
	if got := strings.Count(part, "ran out of memory"); got != 1 {
		t.Errorf(`"ran out of memory" appears %d times, want once (only the killed copy):\n%s`, got, part)
	}
}

// The Apps stream carries the attention notice, which refreshes the list.
func TestLiveAppsSendsAttention(t *testing.T) {
	e := newConsoleEnv(t)
	ls := openStream(t, e.URL, "/live", e.Cookie.Value)
	ls.expect("sync")
	if _, err := e.Svc.Deploy(context.Background(), whoamiApp(), app.TriggerManual); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	for {
		if ls.next() == "attention" {
			return
		}
	}
}

// The no-destination item's action leads to the Backup storage page, which has the form.
func TestSettingsHasTheBackupDestinationPage(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/settings/backup-storage")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `action="/settings/backups"`) {
		t.Errorf("GET /settings/backup-storage = %d, want the destination form:\n%s", resp.StatusCode, body)
	}
}

// The Apps headline words for the domain kinds follow the app's name.
func TestHeadlineWordsDomains(t *testing.T) {
	for kind, want := range map[service.AttentionKind]string{
		service.AttentionDomainDNSElsewhere: "has a domain pointing elsewhere",
		service.AttentionDomainCertExpiring: "has a certificate expiring",
	} {
		if got := headlineWords(service.AttentionItem{Kind: kind}); got != want {
			t.Errorf("headlineWords(%s) = %q, want %q", kind, got, want)
		}
	}
}
