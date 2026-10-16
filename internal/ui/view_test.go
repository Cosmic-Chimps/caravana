package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// summary is an app summary for the table tests.
func summary(id string, state service.AppState, healthy, wanted int, live *uint64, last *store.DeploymentRecord) service.AppSummary {
	return service.AppSummary{
		AppRecord:      store.AppRecord{App: app.App{ID: id}},
		Status:         service.AppStatus{State: state, HealthyCopies: healthy, WantedCopies: wanted, LiveVersion: live},
		LastDeployment: last,
	}
}

func TestHeadlineOf(t *testing.T) {
	v7 := new(uint64(7))
	ok := func(id string) service.AppSummary { return summary(id, service.StateHealthy, 1, 1, v7, nil) }
	rolling := func(id string) service.AppSummary { return summary(id, service.StateRollingOut, 1, 1, v7, nil) }
	degraded := func(id string) service.AppSummary { return summary(id, service.StateDegraded, 1, 2, v7, nil) }
	down := func(id string) service.AppSummary { return summary(id, service.StateNotRunning, 0, 1, nil, nil) }

	// failedDeploy is a deploy that never started: the newest one of the app, whatever the copies say.
	failedDeploy := func(sum service.AppSummary) service.AppSummary {
		sum.LastDeployment = &store.DeploymentRecord{Status: store.StatusFailed}
		return sum
	}
	revertedDeploy := func(sum service.AppSummary) service.AppSummary {
		sum.LastDeployment = &store.DeploymentRecord{Status: store.StatusReverted, JobVersion: v7}
		return sum
	}

	tests := []struct {
		name string
		in   []service.AppSummary
		want string
	}{
		{"no apps", nil, "No apps yet."},
		{"healthy copies don't hide a failed deploy", []service.AppSummary{failedDeploy(ok("whoami")), ok("blog")},
			"1 app needs you: whoami has a failed deploy. 1 app healthy."},
		{"a rollout beside a failed deploy is counted as the deploy's trouble", []service.AppSummary{failedDeploy(rolling("whoami"))},
			"1 app needs you: whoami has a failed deploy."},
		{"degraded and its last deploy failed", []service.AppSummary{failedDeploy(degraded("api"))},
			"1 app needs you: api has 1 of 2 copies healthy, and its last deploy failed."},
		{"not running and its last deploy failed", []service.AppSummary{failedDeploy(down("api"))},
			"1 app needs you: api isn't running, and its last deploy failed."},
		{"a failed deploy is listed with the other trouble", []service.AppSummary{failedDeploy(ok("whoami")), degraded("api"), ok("blog")},
			"2 apps need you: whoami, api. 1 app healthy."},
		{"a reverted deploy is not a failure: Nomad put the old version back", []service.AppSummary{revertedDeploy(ok("blog"))}, "1 app healthy."},
		{"one healthy", []service.AppSummary{ok("whoami")}, "1 app healthy."},
		{"all healthy", []service.AppSummary{ok("whoami"), ok("blog"), ok("jobs")}, "3 apps healthy."},
		{"healthy and one rolling out", []service.AppSummary{ok("whoami"), ok("blog"), rolling("jobs")}, "2 apps healthy, 1 rollout in progress."},
		{"only rolling out", []service.AppSummary{rolling("whoami"), rolling("blog")}, "2 rollouts in progress."},
		{"one degraded", []service.AppSummary{degraded("api")}, "1 app needs you: api has 1 of 2 copies healthy."},
		{"one not running", []service.AppSummary{down("api")}, "1 app needs you: api isn't running."},
		{"trouble first, then the rest counted", []service.AppSummary{ok("whoami"), degraded("api"), rolling("jobs")},
			"1 app needs you: api has 1 of 2 copies healthy. 1 app healthy, 1 rollout in progress."},
		{"several in trouble are listed", []service.AppSummary{degraded("api"), down("blog"), ok("whoami")}, "2 apps need you: api, blog. 1 app healthy."},
		{"a long list is cut", []service.AppSummary{down("a"), down("b"), down("c"), down("d"), down("e")}, "5 apps need you: a, b, c and 2 more."},
		{"Nomad down", []service.AppSummary{summary("whoami", service.StateUnknown, 0, 1, nil, nil), ok("blog")},
			"Nomad can't be reached; showing what caravana last recorded."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := headlineOf(tc.in, nil).String(); got != tc.want {
				t.Errorf("headline = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("the needs-you list counts too", func(t *testing.T) {
		item := func(kind service.AttentionKind, appID string) service.AttentionItem {
			return service.AttentionItem{Kind: kind, AppID: appID}
		}
		tests := []struct {
			name  string
			in    []service.AppSummary
			items []service.AttentionItem
			want  string
		}{
			{"a reverted deploy on a healthy app", []service.AppSummary{revertedDeploy(ok("blog")), ok("whoami")},
				[]service.AttentionItem{item(service.AttentionDeployFailed, "blog")},
				"1 app needs you: blog has a failed deploy. 1 app healthy."},
			{"a copy that keeps restarting", []service.AppSummary{ok("blog")},
				[]service.AttentionItem{item(service.AttentionRestarting, "blog")},
				"1 app needs you: blog has a service that keeps restarting."},
			{"a failed backup, newest item words", []service.AppSummary{ok("blog")},
				[]service.AttentionItem{item(service.AttentionBackupFailed, "blog"), item(service.AttentionRestoreFailed, "blog")},
				"1 app needs you: blog has a failed backup."},
			{"an app with several items is one app", []service.AppSummary{ok("blog"), ok("whoami"), rolling("jobs")},
				[]service.AttentionItem{item(service.AttentionOutOfMemory, "blog"), item(service.AttentionUnhealthy, "blog"), item(service.AttentionUnhealthy, "jobs")},
				"2 apps need you: blog, jobs. 1 app healthy."},
			{"status trouble keeps its own words", []service.AppSummary{degraded("api")},
				[]service.AttentionItem{item(service.AttentionRestarting, "api")},
				"1 app needs you: api has 1 of 2 copies healthy."},
			{"an item without an app is nobody's trouble", []service.AppSummary{ok("blog")},
				[]service.AttentionItem{item(service.AttentionNoBackupDestination, "")},
				"1 app healthy."},
		}
		for _, tc := range tests {
			if got := headlineOf(tc.in, tc.items).String(); got != tc.want {
				t.Errorf("%s: headline = %q, want %q", tc.name, got, tc.want)
			}
		}
	})

	t.Run("names are set apart as mono", func(t *testing.T) {
		got := headlineOf([]service.AppSummary{degraded("api")}, nil)
		if len(got) != 3 || got[0].Mono || !got[1].Mono || got[1].Text != "api" || got[2].Mono {
			t.Errorf("parts = %+v, want plain, mono api, plain", got)
		}
	})
}

func TestSummaryOf(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *store.DeploymentRecord {
		return &store.DeploymentRecord{CreatedAt: now.Add(-d), Deployment: app.Deployment{Trigger: app.TriggerConsole}}
	}
	running := func(version *uint64, d time.Duration) *store.DeploymentRecord {
		rec := at(d)
		rec.Status, rec.JobVersion = store.StatusRunning, version
		return rec
	}
	v7, v8 := new(uint64(7)), new(uint64(8))
	webhook := at(2 * time.Hour)
	webhook.Deployment.Trigger = app.TriggerWebhook
	manual := at(30 * time.Second)
	manual.Deployment.Trigger = app.TriggerManual

	tests := []struct {
		name string
		in   service.AppSummary
		want string
	}{
		{"healthy", summary("a", service.StateHealthy, 2, 2, v7, at(3*time.Minute)), "2 copies running v7. Last deployed 3 minutes ago from the console."},
		{"healthy, one copy", summary("a", service.StateHealthy, 1, 1, v7, webhook), "1 copy running v7. Last deployed 2 hours ago by webhook."},
		{"healthy, through the API, just now", summary("a", service.StateHealthy, 1, 1, v7, manual), "1 copy running v7. Last deployed just now through the API."},
		{"healthy, never deployed through caravana", summary("a", service.StateHealthy, 1, 1, v7, nil), "1 copy running v7. Never deployed."},
		{"degraded", summary("a", service.StateDegraded, 1, 2, v7, at(time.Hour)), "1 of 2 copies healthy, running v7. Last deployed 1 hour ago from the console."},
		{"rolling out, old copies serve", summary("a", service.StateRollingOut, 2, 2, v7, running(v8, time.Minute)),
			"Rolling out v8. 2 copies still run v7. Last deployed 1 minute ago from the console."},
		{"rolling out, nothing serving, not registered yet", summary("a", service.StateRollingOut, 0, 1, nil, running(nil, 5*time.Second)),
			"Rolling out a new version. Last deployed just now from the console."},
		{"not running", summary("a", service.StateNotRunning, 0, 2, nil, at(48*time.Hour)), "No copies are running. Last deployed 2 days ago from the console."},
		{"unknown", summary("a", service.StateUnknown, 0, 1, nil, at(time.Hour)),
			"Nomad can't be reached right now, so caravana can't say what's running. Last deployed 1 hour ago from the console."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := summaryOf(tc.in, now).String(); got != tc.want {
				t.Errorf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusViews(t *testing.T) {
	deployed := &store.DeploymentRecord{Status: store.StatusHealthy}
	apps := []struct {
		name string
		in   service.AppSummary
		want statusView
	}{
		{"healthy", summary("a", service.StateHealthy, 2, 2, nil, deployed), statusView{"healthy", "healthy"}},
		{"rolling out", summary("a", service.StateRollingOut, 0, 2, nil, deployed), statusView{"rolling", "rolling out"}},
		{"degraded says how many", summary("a", service.StateDegraded, 1, 3, nil, deployed), statusView{"warning", "1 of 3 healthy"}},
		{"not running after a deploy is a failure", summary("a", service.StateNotRunning, 0, 1, nil, deployed), statusView{"failed", "not running"}},
		{"not running without a deploy is idle", summary("a", service.StateNotRunning, 0, 1, nil, nil), statusView{"idle", "not running"}},
		{"unknown", summary("a", service.StateUnknown, 0, 1, nil, deployed), statusView{"idle", "unknown"}},
	}
	for _, tc := range apps {
		t.Run("app "+tc.name, func(t *testing.T) {
			if got := appStatusView(tc.in); got != tc.want {
				t.Errorf("appStatusView = %+v, want %+v", got, tc.want)
			}
		})
	}

	deployments := map[store.Status]statusView{
		store.StatusPending:    {"rolling", "rolling out"},
		store.StatusRunning:    {"rolling", "rolling out"},
		store.StatusHealthy:    {"healthy", "healthy"},
		store.StatusFailed:     {"failed", "failed"},
		store.StatusReverted:   {"failed", "reverted"},
		store.StatusSuperseded: {"idle", "superseded"},
	}
	for status, want := range deployments {
		if got := deploymentStatusView(status); got != want {
			t.Errorf("deploymentStatusView(%s) = %+v, want %+v", status, got, want)
		}
	}
}

func TestDeployOutcome(t *testing.T) {
	v8 := new(uint64(8))
	tests := []struct {
		name string
		in   *store.DeploymentRecord
		want string
	}{
		{"none", nil, ""},
		{"healthy needs no word", &store.DeploymentRecord{Status: store.StatusHealthy, JobVersion: v8}, ""},
		{"rolling out needs no word", &store.DeploymentRecord{Status: store.StatusRunning, JobVersion: v8}, ""},
		{"pending needs no word", &store.DeploymentRecord{Status: store.StatusPending}, ""},
		{"reverted", &store.DeploymentRecord{Status: store.StatusReverted, JobVersion: v8}, "v8 reverted"},
		{"failed in a rollout", &store.DeploymentRecord{Status: store.StatusFailed, JobVersion: v8}, "v8 failed"},
		{"failed before registration", &store.DeploymentRecord{Status: store.StatusFailed}, "deploy failed"},
		{"superseded", &store.DeploymentRecord{Status: store.StatusSuperseded, JobVersion: v8}, "v8 superseded"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deployOutcome(tc.in); got != tc.want {
				t.Errorf("deployOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSmallHelpers(t *testing.T) {
	for trigger, want := range map[app.Trigger]string{
		app.TriggerConsole: "from the console",
		app.TriggerManual:  "through the API",
		app.TriggerWebhook: "by webhook",
		"cron":             "by cron",
	} {
		if got := triggerWords(trigger); got != want {
			t.Errorf("triggerWords(%q) = %q, want %q", trigger, got, want)
		}
	}

	if got := versionLabel(new(uint64(7))); got != "v7" {
		t.Errorf("versionLabel(7) = %q", got)
	}
	if got := versionLabel(nil); got != "" {
		t.Errorf("versionLabel(nil) = %q, want none", got)
	}

	for in, want := range map[string]string{
		"d3adbeef-0000-1111-2222-333344445555": "d3adbeef",
		"abc":                                  "abc",
		"":                                     "",
	} {
		if got := shortID(in); got != want {
			t.Errorf("shortID(%q) = %q, want %q", in, got, want)
		}
	}

	for in, want := range map[string]string{
		"traefik/whoami@" + sampleDigest: "sha256:c4717a8d1f01",
		"sha256:abc":                     "sha256:abc",
		"traefik/whoami:latest":          "traefik/whoami:latest",
	} {
		if got := shortDigest(in); got != want {
			t.Errorf("shortDigest(%q) = %q, want %q", in, got, want)
		}
	}

	if got, want := copyCount(1), "1 copy"; got != want {
		t.Errorf("copyCount(1) = %q", got)
	}
	if got, want := copyCount(0), "0 copies"; got != want {
		t.Errorf("copyCount(0) = %q", got)
	}
	if got, want := appCount(2), "2 apps"; got != want {
		t.Errorf("appCount(2) = %q", got)
	}
}

func TestSortProblemsFirst(t *testing.T) {
	type row struct{ id, kind string }
	rows := []row{{"a", "healthy"}, {"b", "idle"}, {"c", "failed"}, {"d", "healthy"}, {"e", "rolling"}, {"f", "warning"}, {"g", "failed"}}
	sortProblemsFirst(rows, func(r row) string { return r.kind })
	got := ""
	for _, r := range rows {
		got += r.id
	}
	// failed (c, g), warning, rolling, healthy (a, d), idle; each kind keeps its order.
	if want := "cgfeadb"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestDurationText(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "under 1 s"},
		{900 * time.Millisecond, "under 1 s"},
		{time.Second, "1 s"},
		{42 * time.Second, "42 s"},
		{42*time.Second + 900*time.Millisecond, "42 s"},
		{time.Minute, "1 min"},
		{90 * time.Second, "1 min 30 s"},
		{59*time.Minute + 59*time.Second, "59 min 59 s"},
		{time.Hour, "1 h"},
		{time.Hour + 2*time.Minute + 5*time.Second, "1 h 2 min"},
		{26 * time.Hour, "26 h"},
		{-5 * time.Second, "under 1 s"}, // a clock that stepped back is not a negative duration
	}
	for _, tc := range tests {
		if got := durationText(tc.in); got != tc.want {
			t.Errorf("durationText(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDeploymentTiming(t *testing.T) {
	created := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	rec := func(status store.Status, took time.Duration) store.DeploymentRecord {
		return store.DeploymentRecord{Status: status, CreatedAt: created, UpdatedAt: created.Add(took)}
	}
	now := created.Add(40 * time.Second)
	tests := []struct {
		name string
		rec  store.DeploymentRecord
		want string
	}{
		{"healthy took", rec(store.StatusHealthy, 42*time.Second), "took 42 s"},
		{"failed took", rec(store.StatusFailed, 3*time.Second), "took 3 s"},
		{"reverted took", rec(store.StatusReverted, 2*time.Minute), "took 2 min"},
		{"superseded took", rec(store.StatusSuperseded, time.Second), "took 1 s"},
		{"running counts from the start to now", rec(store.StatusRunning, time.Second), "running for 40 s"},
		{"pending counts from the start to now", rec(store.StatusPending, 0), "running for 40 s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deploymentTiming(tc.rec, now); got != tc.want {
				t.Errorf("deploymentTiming = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProgressLine(t *testing.T) {
	tests := []struct {
		name string
		in   service.Progress
		want string
	}{
		{"the spec's example", service.Progress{Wanted: 2, Placed: 2, Healthy: 1},
			"2 of 2 new copies started, 1 healthy. Old copies keep serving until the new ones pass their checks."},
		{"nothing placed yet", service.Progress{Wanted: 2},
			"0 of 2 new copies started, 0 healthy. Old copies keep serving until the new ones pass their checks."},
		{"one copy is a copy, not copies", service.Progress{Wanted: 1, Placed: 1},
			"1 of 1 new copy started, 0 healthy. Old copies keep serving until the new ones pass their checks."},
		{"unhealthy copies are named", service.Progress{Wanted: 3, Placed: 3, Healthy: 1, Unhealthy: 2},
			"3 of 3 new copies started, 1 healthy, 2 unhealthy. Old copies keep serving until the new ones pass their checks."},
		{"canaries not yet promoted", service.Progress{Wanted: 2, Placed: 2, Healthy: 2, CanariesWanted: 2},
			"2 of 2 new copies started, 2 healthy. Old copies keep serving until the new ones pass their checks."},
		{"promoted", service.Progress{Wanted: 2, Placed: 2, Healthy: 2, CanariesWanted: 2, Promoted: true},
			"2 of 2 new copies started, 2 healthy. The new copies are serving; the old ones are being stopped."},
		{"no counts yet", service.Progress{}, "Nomad hasn't reported any copies for this rollout yet."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := progressLine(tc.in); got != tc.want {
				t.Errorf("progressLine =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

// TestDeploymentSentence has one row per row of data-model.md (Deployment), and the
// variations that keep a sentence true: it says "keeps serving" only for the app's
// newest deployment, since for an older one the claim could be stale.
func TestDeploymentSentence(t *testing.T) {
	v6, v8 := new(uint64(6)), new(uint64(8))
	rec := func(status store.Status, version *uint64, errText string) store.DeploymentRecord {
		return store.DeploymentRecord{AppID: "whoami", Status: status, JobVersion: version, Error: errText}
	}
	const imageGone = "image not found, or private: add credentials for ghcr.io in Settings › Registries"
	tests := []struct {
		name  string
		rec   store.DeploymentRecord
		facts deployFacts
		want  string
	}{
		{"healthy and live", rec(store.StatusHealthy, new(uint64(7)), ""), deployFacts{Current: true},
			"v7 is live."},
		{"healthy, but not the newest any more", rec(store.StatusHealthy, new(uint64(7)), ""), deployFacts{},
			"v7 rolled out healthy."},

		{"reverted, the previous version serves", rec(store.StatusReverted, new(uint64(7)), "Failed due to unhealthy allocations"), deployFacts{Current: true, Previous: v6},
			"v7 failed its health checks. Nomad rolled back to v6, which is serving."},
		{"reverted: Nomad's message names the version it went back to", rec(store.StatusReverted, new(uint64(7)), "Failed due to unhealthy allocations - rolling back to job version 5"), deployFacts{Current: true, Previous: v6},
			"v7 failed its health checks. Nomad rolled back to v5, which is serving."},
		{"reverted, no earlier deployment known", rec(store.StatusReverted, new(uint64(7)), "Failed due to progress deadline"), deployFacts{Current: true},
			"v7 failed its health checks. Nomad rolled back to the previous version, which is serving."},
		{"reverted, an older entry", rec(store.StatusReverted, new(uint64(7)), "Failed due to unhealthy allocations"), deployFacts{Previous: v6},
			"v7 failed its health checks. Nomad rolled back to v6."},

		{"failed before registration: the image", rec(store.StatusFailed, nil, imageGone), deployFacts{Current: true, Previous: v6},
			"This deploy didn't start: the image wasn't found. v6 keeps serving."},
		{"failed before registration: the image, as the registry words it", rec(store.StatusFailed, nil, "ghcr.io: ghcr.io/example/nope:1: "+imageGone), deployFacts{Current: true, Previous: v6},
			"This deploy didn't start: the image wasn't found. v6 keeps serving."},
		{"failed before registration: the registry", rec(store.StatusFailed, nil, "registry unavailable: it answered 503"), deployFacts{Current: true},
			"This deploy didn't start: the registry couldn't be reached."},
		{"failed before registration: the registry refused the credentials", rec(store.StatusFailed, nil, "ghcr.io: ghcr.io/acme/shop:v1: the registry refused the stored credentials (ghcr.io answered 401)"), deployFacts{Current: true, Previous: v6},
			"This deploy didn't start: the registry refused the stored credentials. v6 keeps serving."},
		{"failed before registration: the credentials are fine, the image isn't there", rec(store.StatusFailed, nil, "ghcr.io: ghcr.io/acme/shop:v1: image not found, or the stored credentials for ghcr.io can't read it"), deployFacts{Current: true},
			"This deploy didn't start: the image wasn't found."},
		{"failed before registration: Nomad down", rec(store.StatusFailed, nil, "nomad unavailable: Nomad is unreachable at http://x (register job whoami)"), deployFacts{Current: true},
			"This deploy didn't start: Nomad couldn't be reached."},
		{"failed before registration: Nomad refused the job", rec(store.StatusFailed, nil, "nomad rejected the job: Nomad answered 400: bad"), deployFacts{Current: true},
			"This deploy didn't start: Nomad rejected the job."},
		{"failed before registration: another change", rec(store.StatusFailed, nil, "job conflict: the Nomad job \"whoami\" changed while deploying; try again"), deployFacts{Current: true},
			"This deploy didn't start: the Nomad job changed while deploying."},
		// Nomad already runs a job of that name that caravana didn't make: the sentence says so, in
		// caravana's lowercase, and doesn't call it a change made while deploying. The raw error
		// (from the engine, with its own capital) goes under it, untouched.
		{"failed before registration: a job caravana didn't create", rec(store.StatusFailed, nil, "job conflict: Nomad already runs a job \"whoami\" that Caravana didn't create; stop it, or deploy under another app ID"), deployFacts{Current: true, Previous: v6},
			"This deploy didn't start: a job named whoami already exists in Nomad and caravana didn't create it. The running version, if any, was left alone. Stop that job in Nomad, then deploy again."},
		{"failed before registration: a job caravana didn't create, an older entry", rec(store.StatusFailed, nil, "job conflict: Nomad already runs a job \"whoami\" that Caravana didn't create; stop it, or deploy under another app ID"), deployFacts{},
			"This deploy didn't start: a job named whoami already exists in Nomad and caravana didn't create it. The running version, if any, was left alone. Stop that job in Nomad, then deploy again."},
		{"failed before registration: no cause known", rec(store.StatusFailed, nil, "something odd"), deployFacts{Current: true},
			"This deploy didn't start."},
		{"failed before registration, an older entry", rec(store.StatusFailed, nil, imageGone), deployFacts{Previous: v6},
			"This deploy didn't start: the image wasn't found."},

		{"failed during its rollout", rec(store.StatusFailed, new(uint64(7)), "Failed due to progress deadline"), deployFacts{Current: true, Previous: v6},
			"v7 failed during its rollout. v6 keeps serving."},
		{"failed during its rollout, nothing earlier", rec(store.StatusFailed, new(uint64(7)), "Failed due to progress deadline"), deployFacts{Current: true},
			"v7 failed during its rollout."},
		{"failed during its rollout, an older entry", rec(store.StatusFailed, new(uint64(7)), "x"), deployFacts{Previous: v6},
			"v7 failed during its rollout."},

		{"superseded by a known version", rec(store.StatusSuperseded, new(uint64(7)), "Cancelled due to newer version of job"), deployFacts{ReplacedBy: v8},
			"v7 was replaced by v8 before it finished."},
		{"superseded by an unknown one", rec(store.StatusSuperseded, new(uint64(7)), ""), deployFacts{},
			"v7 was replaced by a newer deploy before it finished."},
		{"superseded before it was registered", rec(store.StatusSuperseded, nil, "replaced by deployment x before it was registered"), deployFacts{ReplacedBy: v8},
			"This deploy was replaced by a newer one before it started."},

		{"running", rec(store.StatusRunning, new(uint64(7)), ""), deployFacts{Current: true}, "Rolling out v7."},
		{"pending", rec(store.StatusPending, nil, ""), deployFacts{Current: true}, "Rolling out a new version."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deploymentSentence(tc.rec, tc.facts).String(); got != tc.want {
				t.Errorf("deploymentSentence =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}

	t.Run("a job caravana didn't create is never called a change made while deploying", func(t *testing.T) {
		raw := "job conflict: Nomad already runs a job \"whoami\" that Caravana didn't create; stop it, or deploy under another app ID"
		got := deploymentSentence(rec(store.StatusFailed, nil, raw), deployFacts{Current: true}).String()
		if strings.Contains(got, "changed while deploying") || strings.Contains(got, "Caravana") {
			t.Errorf("sentence = %q", got)
		}
		if !foreignJob(raw) || foreignJob("job conflict: the Nomad job \"whoami\" changed while deploying; try again") || foreignJob("image not found") {
			t.Error("foreignJob doesn't tell the two job conflicts, or another error, apart")
		}
	})

	t.Run("versions are set in mono", func(t *testing.T) {
		s := deploymentSentence(rec(store.StatusReverted, new(uint64(7)), ""), deployFacts{Current: true, Previous: v6})
		var monos []string
		for _, p := range s {
			if p.Mono {
				monos = append(monos, p.Text)
			}
		}
		if len(monos) != 2 || monos[0] != "v7" || monos[1] != "v6" {
			t.Errorf("mono parts = %v, want [v7 v6]", monos)
		}
	})
}

func TestFailedAction(t *testing.T) {
	rec := func(status store.Status, errText string) store.DeploymentRecord {
		return store.DeploymentRecord{Status: status, Error: errText}
	}
	current, older := deployFacts{Current: true}, deployFacts{}
	tests := []struct {
		name  string
		rec   store.DeploymentRecord
		facts deployFacts
		want  string // the label; "" for no action
		href  string // where it goes; "" means the deploy form, #deploy
	}{
		{"failed, the newest", rec(store.StatusFailed, "job conflict: x"), current, "Deploy again", ""},
		{"reverted, the newest", rec(store.StatusReverted, "unhealthy"), current, "Deploy again", ""},
		{"the image wasn't found: the fix is the image", rec(store.StatusFailed, "ghcr.io: x:1: image not found, or private"), current, "Change the image", ""},
		{"the credentials were refused: the fix is in Settings", rec(store.StatusFailed, "ghcr.io: ghcr.io/acme/x:1: the registry refused the stored credentials (ghcr.io answered 401)"), current,
			"Check the registry's credentials", "/settings#registries"},
		{"the image isn't there with credentials: still the image", rec(store.StatusFailed, "ghcr.io: x:1: image not found, or the stored credentials for ghcr.io can't read it"), current, "Change the image", ""},
		{"failed, but a newer deploy came after", rec(store.StatusFailed, "x"), older, "", ""},
		{"healthy", rec(store.StatusHealthy, ""), current, "", ""},
		{"rolling out", rec(store.StatusRunning, ""), current, "", ""},
		{"superseded", rec(store.StatusSuperseded, ""), current, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := failedAction(tc.rec, tc.facts)
			href := tc.href
			if href == "" {
				href = "#deploy"
			}
			switch {
			case tc.want == "" && got != nil:
				t.Errorf("action = %+v, want none", *got)
			case tc.want != "" && (got == nil || got.Label != tc.want || got.Href != href):
				t.Errorf("action = %+v, want %q to %s", got, tc.want, href)
			}
		})
	}
}

func TestDeployFactsOf(t *testing.T) {
	// A page is newest first. Each entry's facts come from its neighbours.
	rec := func(id string, status store.Status, version uint64) store.DeploymentRecord {
		r := store.DeploymentRecord{Status: status}
		r.Deployment.ID = id
		if version != 0 {
			r.JobVersion = new(version)
		}
		return r
	}
	page := []store.DeploymentRecord{
		rec("d5", store.StatusRunning, 5),
		rec("d4", store.StatusFailed, 0),
		rec("d3", store.StatusSuperseded, 4),
		rec("d2", store.StatusHealthy, 3),
		rec("d1", store.StatusHealthy, 2),
	}
	facts := deployFactsOf(page, true)
	version := func(v *uint64) string { return versionLabel(v) }
	for i, want := range []struct {
		current              bool
		previous, replacedBy string
	}{
		{true, "v3", ""},    // d5: the newest; the newest healthy before it is v3
		{false, "v3", "v5"}, // d4: failed before registration; the next one with a version is v5
		{false, "v3", "v5"}, // d3
		{false, "v2", "v4"}, // d2: the nearest newer entry with a version is d3's v4
		{false, "", "v3"},   // d1: nothing healthy before it on this page
	} {
		got := facts[i]
		if got.Current != want.current || version(got.Previous) != want.previous || version(got.ReplacedBy) != want.replacedBy {
			t.Errorf("entry %d (%s): current %v previous %q replacedBy %q; want %v %q %q", i, page[i].Deployment.ID,
				got.Current, version(got.Previous), version(got.ReplacedBy), want.current, want.previous, want.replacedBy)
		}
	}

	// Only the first page's first entry is the newest deployment.
	if later := deployFactsOf(page, false); later[0].Current {
		t.Error("an entry on a later page is marked as the app's newest")
	}
	if len(deployFactsOf(nil, true)) != 0 {
		t.Error("no entries, no facts")
	}
}
