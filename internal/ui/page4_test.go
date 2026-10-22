package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 021, page 4: the Apps home's Deploying now card, filter and kind words, the
// Activity rail's "by <who>", and the deployment entry's stages and rollback wording.

// ---- pure rules ----------------------------------------------------------------------

func TestKindOf(t *testing.T) {
	img := func(image string) app.App {
		return app.App{ID: "x", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{Name: "web", Image: image}}}
	}
	withVolume := img("ghcr.io/acme/web:1")
	withVolume.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	compose := img("ghcr.io/acme/web:1")
	compose.Source.Kind = app.SourceCompose
	composeDB := img("postgres:16")
	composeDB.Source.Kind = app.SourceCompose
	backedUp := img("ghcr.io/acme/db:1")
	backedUp.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres}
	volumeBackup := withVolume
	volumeBackup.Services = []app.Service{withVolume.Services[0]}
	volumeBackup.Services[0].Backup = &app.Backup{Engine: app.BackupVolume}
	dbWithVolume := img("postgres:16")
	dbWithVolume.Services[0].Volumes = withVolume.Services[0].Volumes

	tests := []struct {
		name string
		a    app.App
		kept bool
		want string
	}{
		{"a plain image app has no word", img("traefik/whoami:latest"), false, ""},
		{"compose by source", compose, false, "compose"},
		{"compose by a kept file", img("ghcr.io/acme/web:1"), true, "compose"},
		{"compose wins over database", composeDB, false, "compose"},
		{"database by image", img("postgres:16"), false, "database"},
		{"database by an image with a registry and a digest", img("ghcr.io/acme/MariaDB@sha256:abc"), false, "database"},
		{"database by the last path segment only", img("docker.io/library/redis"), false, "database"},
		{"valkey and mysql too", img("valkey/valkey:8"), false, "database"},
		{"database by a postgres backup", backedUp, false, "database"},
		{"a volume app", withVolume, false, "volume"},
		{"a volume backup is not a database", volumeBackup, false, "volume"},
		{"database wins over volume", dbWithVolume, false, "database"},
		{"an image that only contains a database's name", img("ghcr.io/acme/postgres-exporter:1"), false, ""},
		{"a registry port is not a tag", img("localhost:5000/acme/web"), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kindOf(tt.a, tt.kept); got != tt.want {
				t.Errorf("kindOf = %q, want %q", got, tt.want)
			}
		})
	}
}

// The one set of who-words: the rail and the history entry's meta line say them in
// lowercase with the commit, the lede without the commit, and the card starts a
// sentence with them ("Pushed ...", "Deployed from the console").
func TestWhoWords(t *testing.T) {
	origin := &app.Origin{Commit: "4e0a77aabbcc", Ref: "refs/heads/main", Actor: "ana"}
	tests := []struct {
		name         string
		rec          store.DeploymentRecord
		wantWho      string // the rail and the meta line
		wantLede     string // without the commit
		wantChange   string // the card
		againVersion string
	}{
		{"an origin", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerManual, Origin: origin}},
			"pushed to main by ana · 4e0a77a", "pushed to main by ana", "Pushed to main by ana · 4e0a77a", ""},
		{"an origin with a tag and no actor", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerManual,
			Origin: &app.Origin{Ref: "refs/tags/v1.2.0", Commit: "abcdef0"}}},
			"pushed to v1.2.0 · abcdef0", "pushed to v1.2.0", "Pushed to v1.2.0 · abcdef0", ""},
		{"an origin with only a run says nothing of its own", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerManual,
			Origin: &app.Origin{Run: "https://github.com/a/b/actions/runs/1"}}},
			"through the API", "through the API", "Deployed through the API", ""},
		{"the console", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerConsole}},
			"from the console", "from the console", "Deployed from the console", ""},
		{"the API", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerManual}},
			"through the API", "through the API", "Deployed through the API", ""},
		{"the API with a token's name", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerManual, TokenName: "ci-github"}},
			"through the API by ci-github", "through the API by ci-github", "Deployed through the API by ci-github", ""},
		{"the webhook", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerWebhook}},
			"by the webhook", "by the webhook", "Deployed by the webhook", ""},
		{"a promotion", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerManual, TokenName: "ci", Origin: origin},
			SourceApp: "shop--staging--web", SourceEnvironment: "staging", Source: "d9"},
			"promoted from staging", "promoted from staging", "Promoted from staging", ""},
		{"a redeploy of a past version", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerConsole}, Source: "d3"},
			"from the console", "from the console", "v3 deployed again", "v3"},
		{"a redeploy whose source can't be named", store.DeploymentRecord{Deployment: app.Deployment{Trigger: app.TriggerConsole}, Source: "d3"},
			"from the console", "from the console", "A past version deployed again", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deployWho(tt.rec, true).String(); got != tt.wantWho {
				t.Errorf("deployWho = %q, want %q", got, tt.wantWho)
			}
			if got := deployWho(tt.rec, false).String(); got != tt.wantLede {
				t.Errorf("deployWho without the commit = %q, want %q", got, tt.wantLede)
			}
			if got := changeLine(tt.rec, tt.againVersion).String(); got != tt.wantChange {
				t.Errorf("changeLine = %q, want %q", got, tt.wantChange)
			}
		})
	}
}

// The three places that say who deployed use the same words: the lede after "Last deployed",
// the Activity rail, and the history entry's meta line, which also carries the run link.
func TestWhoWordsAreTheSameEverywhere(t *testing.T) {
	origin := &app.Origin{Commit: "4e0a77aabb", Ref: "refs/heads/main", Actor: "ana", Run: "https://github.com/a/b/actions/runs/7"}
	staging := whoamiApp()
	staging.ID = "stg"
	staging.Services[0].Domains[0].Host = "stg.example.com"
	tests := []struct {
		name     string
		seed     seedDeploy
		lede     string // after "Last deployed 0 minutes ago" ("just now")
		railMeta string
		entry    string // the entry's meta line, up to its time
	}{
		{"console", seedDeploy{Trigger: app.TriggerConsole}, "just now, from the console.", "from the console · ", "from the console · "},
		{"webhook", seedDeploy{Trigger: app.TriggerWebhook}, "just now, by the webhook.", "by the webhook · ", "by the webhook · "},
		{"API with a token", seedDeploy{Trigger: app.TriggerManual, TokenName: "ci-github"}, "just now, through the API by ci-github.",
			"through the API by ci-github · ", "through the API by ci-github · "},
		{"CI", seedDeploy{Trigger: app.TriggerManual, Origin: origin}, "just now, pushed to main by ana.",
			"pushed to main by ana · 4e0a77a · ", "pushed to main by ana · 4e0a77a · run · "},
		{"promotion", seedDeploy{Trigger: app.TriggerManual, Source: "s1"}, "just now, promoted from production.",
			"promoted from production · ", "promoted from production · "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newConsoleEnv(t)
			e.seed(t, staging, seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
			tt.seed.ID, tt.seed.Status, tt.seed.Version = "w1", store.StatusHealthy, 1
			e.seed(t, whoamiApp(), tt.seed)
			e.copies("whoami", "web", 1, 2, 0)

			_, part := e.get(t, "/parts/apps/whoami/header")
			if got := text(part); !strings.Contains(got, "Last deployed "+tt.lede) {
				t.Errorf("the lede lacks %q: %s", tt.lede, got)
			}
			_, rail := e.get(t, "/parts/activity")
			if got := text(rail); !strings.Contains(got, tt.railMeta) {
				t.Errorf("the rail lacks %q: %s", tt.railMeta, got)
			}
			_, body := e.get(t, "/apps/whoami/deployments")
			entry := entryOf(t, body, "w1")
			meta := entry[strings.Index(entry, `class="cv-deploy-meta`):]
			meta = text(meta[strings.Index(meta, ">")+1 : strings.Index(meta, "</span>\n")+7])
			if !strings.HasPrefix(meta, tt.entry) {
				t.Errorf("the entry's meta line = %q, want it to start with %q", meta, tt.entry)
			}
			// The run is the one https link, opened safely; the old separate origin line is gone.
			hasRun := strings.Contains(entry, `<a href="https://github.com/a/b/actions/runs/7" rel="noopener noreferrer" target="_blank">run</a>`)
			if hasRun != (tt.seed.Origin != nil) {
				t.Errorf("run link present = %v, want %v:\n%s", hasRun, tt.seed.Origin != nil, entry)
			}
			if strings.Contains(entry, "Pushed to") || strings.Contains(entry, "From CI") {
				t.Errorf("the entry still has an origin line of its own:\n%s", entry)
			}
		})
	}
}

func TestStagesOf(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	reg := t0.Add(2 * time.Second)
	rec := func(status store.Status, version *uint64, registered *time.Time) store.DeploymentRecord {
		return store.DeploymentRecord{Status: status, JobVersion: version, RegisteredAt: registered,
			CreatedAt: t0, UpdatedAt: t0.Add(45 * time.Second)}
	}
	v := new(uint64(3))
	cell := func(mark, value string, active bool) stageView {
		return stageView{Mark: mark, Value: value, Active: active}
	}
	tests := []struct {
		name string
		rec  store.DeploymentRecord
		p    *service.Progress
		want [3]stageView // marks, values and the active flag; names are checked apart
	}{
		{"waiting to register", rec(store.StatusPending, nil, nil), nil,
			[3]stageView{cell("rolling", "waiting", true), cell("idle", "—", false), cell("idle", "—", false)}},
		{"rolling out with Nomad's progress", rec(store.StatusRunning, v, &reg), &service.Progress{Wanted: 2, Healthy: 1},
			[3]stageView{cell("healthy", "2 s", false), cell("rolling", "1 of 2 new copies healthy", true), cell("idle", "—", false)}},
		{"rolling out, one copy wanted", rec(store.StatusRunning, v, &reg), &service.Progress{Wanted: 1},
			[3]stageView{cell("healthy", "2 s", false), cell("rolling", "0 of 1 new copy healthy", true), cell("idle", "—", false)}},
		{"rolling out with no progress yet", rec(store.StatusRunning, v, &reg), nil,
			[3]stageView{cell("healthy", "2 s", false), cell("rolling", "waiting", true), cell("idle", "—", false)}},
		{"healthy", rec(store.StatusHealthy, v, &reg), nil,
			[3]stageView{cell("healthy", "2 s", false), cell("healthy", "43 s", false), cell("healthy", "14:00:45 UTC", false)}},
		{"healthy, recorded before the job's time was kept", rec(store.StatusHealthy, v, nil), nil,
			[3]stageView{cell("healthy", "—", false), cell("healthy", "—", false), cell("healthy", "14:00:45 UTC", false)}},
		{"reverted", rec(store.StatusReverted, v, &reg), nil,
			[3]stageView{cell("healthy", "2 s", false), cell("failed", "failed", false), cell("idle", "—", false)}},
		{"failed during the rollout", rec(store.StatusFailed, v, &reg), nil,
			[3]stageView{cell("healthy", "2 s", false), cell("failed", "failed", false), cell("idle", "—", false)}},
		{"failed before register", rec(store.StatusFailed, nil, nil), nil,
			[3]stageView{cell("failed", "didn't start", false), cell("idle", "—", false), cell("idle", "—", false)}},
		{"superseded before it registered", rec(store.StatusSuperseded, nil, nil), nil,
			[3]stageView{cell("idle", "superseded", false), cell("idle", "—", false), cell("idle", "—", false)}},
		{"superseded during its rollout", rec(store.StatusSuperseded, v, &reg), nil,
			[3]stageView{cell("healthy", "2 s", false), cell("idle", "superseded", false), cell("idle", "—", false)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stagesOf(tt.rec, tt.p)
			for i, name := range []string{"Queued", "Roll out", "Live"} {
				w := tt.want[i]
				if got[i].Name != name || got[i].Mark != w.Mark || got[i].Value != w.Value || got[i].Active != w.Active {
					t.Errorf("stage %d = %+v, want %s %+v", i, got[i], name, w)
				}
			}
		})
	}
	if w := (stageView{Mark: "failed"}).Word(); w != "failed" {
		t.Errorf("Word = %q", w)
	}
}

// The strip and the card say the same thing about a rollout: "N of M new copies healthy".
func TestNewCopiesHealthy(t *testing.T) {
	for _, tt := range []struct {
		p    service.Progress
		want string
	}{
		{service.Progress{Wanted: 2, Healthy: 1}, "1 of 2 new copies healthy"},
		{service.Progress{Wanted: 1, Healthy: 0}, "0 of 1 new copy healthy"},
		{service.Progress{Wanted: 3, Healthy: 3}, "3 of 3 new copies healthy"},
	} {
		if got := newCopiesHealthy(tt.p); got != tt.want {
			t.Errorf("newCopiesHealthy(%+v) = %q, want %q", tt.p, got, tt.want)
		}
	}
}

// A full strip is for what is going on, the newest entry and what failed; the rest get one line.
func TestFullStrip(t *testing.T) {
	for _, tt := range []struct {
		status  store.Status
		current bool
		want    bool
	}{
		{store.StatusPending, false, true},
		{store.StatusRunning, false, true},
		{store.StatusFailed, false, true},
		{store.StatusReverted, false, true},
		{store.StatusHealthy, true, true},
		{store.StatusSuperseded, true, true},
		{store.StatusHealthy, false, false},
		{store.StatusSuperseded, false, false},
	} {
		name := fmt.Sprintf("%s newest=%v", tt.status, tt.current)
		if got := fullStrip(store.DeploymentRecord{Status: tt.status}, tt.current); got != tt.want {
			t.Errorf("%s: fullStrip = %v, want %v", name, got, tt.want)
		}
	}
}

func TestChipsOf(t *testing.T) {
	v1, v2 := new(uint64(1)), new(uint64(2))
	mk := func(id, svc string, version uint64, status deploy.CopyStatus, stopping bool) deploy.Copy {
		return deploy.Copy{ID: id, Service: svc, Version: version, Status: status, Stopping: stopping}
	}
	tests := []struct {
		name     string
		services int
		copies   []deploy.Copy
		newV     *uint64
		want     []string // each chip as "name version short role"
	}{
		{"new copies first, then the old ones that still serve", 1, []deploy.Copy{
			mk("aaaaaaaa-1", "web", 1, deploy.CopyHealthy, false),
			mk("bbbbbbbb-2", "web", 2, deploy.CopyStarting, false),
			mk("cccccccc-3", "web", 2, deploy.CopyHealthy, false),
			mk("dddddddd-4", "web", 1, deploy.CopyHealthy, false),
		}, v2, []string{
			"app v2 bbbbbbbb new, starting", "app v2 cccccccc new, healthy",
			"app v1 aaaaaaaa serving, v1", "app v1 dddddddd serving, v1",
		}},
		{"draining is only for a copy Nomad is stopping, and comes last", 1, []deploy.Copy{
			mk("aaaaaaaa-1", "web", 1, deploy.CopyHealthy, true),
			mk("bbbbbbbb-2", "web", 1, deploy.CopyHealthy, false),
			mk("cccccccc-3", "web", 2, deploy.CopyHealthy, false),
		}, v2, []string{"app v2 cccccccc new, healthy", "app v1 bbbbbbbb serving, v1", "app v1 aaaaaaaa draining, v1"}},
		{"an unhealthy new copy", 1, []deploy.Copy{mk("aaaaaaaa-1", "web", 2, deploy.CopyUnhealthy, false)}, v2,
			[]string{"app v2 aaaaaaaa new, unhealthy"}},
		{"ended copies are not chips", 1, []deploy.Copy{
			{ID: "aaaaaaaa-1", Service: "web", Version: 1, Status: deploy.CopyStopped, Terminal: true},
			mk("bbbbbbbb-2", "web", 2, deploy.CopyHealthy, false),
		}, v2, []string{"app v2 bbbbbbbb new, healthy"}},
		{"several services name each chip's service, the new ones first", 2, []deploy.Copy{
			mk("aaaaaaaa-1", "web", 1, deploy.CopyHealthy, false),
			mk("bbbbbbbb-2", "worker", 2, deploy.CopyStarting, false),
		}, v2, []string{"worker v2 bbbbbbbb new, starting", "web v1 aaaaaaaa serving, v1"}},
		{"no version to compare: nothing is new", 1, []deploy.Copy{mk("aaaaaaaa-1", "web", 1, deploy.CopyHealthy, false)}, nil,
			[]string{"app v1 aaaaaaaa serving, v1"}},
		{"the first deploy: all new", 1, []deploy.Copy{mk("aaaaaaaa-1", "web", 1, deploy.CopyStarting, false)}, v1,
			[]string{"app v1 aaaaaaaa new, starting"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, c := range chipsOf("app", tt.services, tt.copies, tt.newV) {
				got = append(got, c.Name+" "+c.Version+" "+c.Short+" "+c.Role)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("chips = %q\nwant    %q", got, tt.want)
			}
		})
	}
}

// history is a history of whoami's deployments, newest first, for the rollback rules.
func TestRepeatedVersion(t *testing.T) {
	def := &app.App{ID: "whoami"}
	digests := map[string]string{"web": "traefik/whoami@sha256:aa"}
	healthy := func(v uint64) store.DeploymentRecord {
		return store.DeploymentRecord{Status: store.StatusHealthy, JobVersion: new(v), Definition: def, Deployment: app.Deployment{Digests: digests}}
	}
	reverted := func(v uint64, msg string) store.DeploymentRecord {
		return store.DeploymentRecord{Status: store.StatusReverted, JobVersion: new(v), Error: msg, Deployment: app.Deployment{Digests: digests}}
	}
	noDef := healthy(4)
	noDef.Definition = nil
	noDigests := healthy(4)
	noDigests.Deployment.Digests = nil
	failed := store.DeploymentRecord{Status: store.StatusFailed, JobVersion: new(uint64(3))}

	tests := []struct {
		name   string
		failed store.DeploymentRecord
		older  []store.DeploymentRecord
		want   string
	}{
		{"the newest healthy one before it", reverted(5, "Failed - rolling back to job version 9"), []store.DeploymentRecord{failed, healthy(2), healthy(0)}, "v2"},
		{"the version Nomad's message names, when a healthy deployment has it", reverted(5, "Failed - rolling back to job version 0"), []store.DeploymentRecord{healthy(2), healthy(0)}, "v0"},
		{"a message without a version", reverted(5, "health checks failed"), []store.DeploymentRecord{healthy(2), healthy(0)}, "v2"},
		{"one with no recorded definition can't be matched", reverted(5, ""), []store.DeploymentRecord{noDef, healthy(0)}, "v0"},
		{"one with no pinned images can't be matched", reverted(5, ""), []store.DeploymentRecord{noDigests}, ""},
		{"no healthy deployment before it", reverted(5, ""), []store.DeploymentRecord{failed}, ""},
		{"no history at all", reverted(5, ""), nil, ""},
		{"a deployment that wasn't reverted", failed, []store.DeploymentRecord{healthy(2)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ""
			if v := repeatedVersion(tt.failed, tt.older); v != nil {
				got = versionLabel(v)
			}
			if got != tt.want {
				t.Errorf("repeatedVersion = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRollbackWording(t *testing.T) {
	failed := store.DeploymentRecord{Status: store.StatusReverted, JobVersion: new(uint64(1)),
		Error: "Failed due to unhealthy allocations - rolling back to job version 0", AppID: "whoami"}
	v0, v2 := new(uint64(0)), new(uint64(2))
	tests := []struct {
		name  string
		facts deployFacts
		want  string
	}{
		{"today's wording when nothing matches", deployFacts{Current: true, Previous: v0},
			"v1 failed its health checks. Nomad rolled back to v0, which is serving."},
		{"the version Nomad registered, and the deployment it repeats", deployFacts{Current: true, Previous: v0, Live: v2, Again: v0},
			"v1 failed its health checks. Nomad rolled back to v2 (v0 again), which is serving."},
		{"an older entry says the version in Nomad's message, and no 'again' when it is the match", deployFacts{Previous: v0, Again: v0},
			"v1 failed its health checks. Nomad rolled back to v0."},
		{"an older entry whose message names a copy", deployFacts{Previous: v0, Again: new(uint64(5))},
			"v1 failed its health checks. Nomad rolled back to v0 (v5 again)."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deploymentSentence(failed, tt.facts).String(); got != tt.want {
				t.Errorf("sentence = %q\nwant       %q", got, tt.want)
			}
		})
	}

	t.Run("the lede and the needs-you sentence", func(t *testing.T) {
		now := runningNow{Version: v2, Known: true}
		if got := deployFailedSentence(failed, now).String(); got != "v1 failed its health checks. v2 keeps running." {
			t.Errorf("without a match: %q", got)
		}
		if got := deployFailedSentence(failed, now.repeating(v0)).String(); got != "v1 failed its health checks. v2 (v0 again) keeps running." {
			t.Errorf("with a match: %q", got)
		}
		// A running version that is the deployment itself isn't "again".
		if got := deployFailedSentence(failed, runningNow{Version: v0, Known: true}.repeating(v0)).String(); got != "v1 failed its health checks. v0 keeps running." {
			t.Errorf("version equal to its match: %q", got)
		}
	})
}

// ---- the Apps home -------------------------------------------------------------------

// rolloutEnv has whoami with a healthy v1 and a v2 rolling out, pushed by ana: three copies
// in Nomad, the old one draining, one new one healthy and one new one starting.
func rolloutEnv(t *testing.T, origin *app.Origin) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(),
		seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1, Definition: new(whoamiApp())},
		seedDeploy{ID: "d2", Trigger: app.TriggerManual, Status: store.StatusRunning, Version: 2, Origin: origin})
	for range 3 {
		e.Nomad.AddForeignJob(&api.Job{ID: new("whoami")})
	}
	e.Nomad.Script("whoami", 2, deploytest.Rolling("Deployment is running", map[string]api.DeploymentState{
		"web": {DesiredTotal: 2, PlacedAllocs: 2, HealthyAllocs: 1},
	}))
	yes := true
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "77b00000-0000-4000-8000-000000000001", TaskGroup: "web", JobVersion: 1, ClientStatus: "running", DesiredStatus: "stop", NodeName: "n1"},
		api.AllocationListStub{ID: "4b2e0000-0000-4000-8000-000000000002", TaskGroup: "web", JobVersion: 2, ClientStatus: "running", NodeName: "n1",
			DeploymentStatus: &api.AllocDeploymentStatus{Healthy: &yes}},
		api.AllocationListStub{ID: "c81e0000-0000-4000-8000-000000000003", TaskGroup: "web", JobVersion: 2, ClientStatus: "pending", NodeName: "n1"})
	return e
}

// cardOf is the page's Deploying now region, from its section to the filter or the groups.
func cardOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `class="cv-deploying"`)
	if i < 0 {
		t.Fatalf("no Deploying now section:\n%s", body)
	}
	rest := body[i:]
	return rest[:strings.Index(rest, "</section>")]
}

func TestDeployingNowCard(t *testing.T) {
	origin := &app.Origin{Commit: "4e0a77aabb", Ref: "refs/heads/main", Actor: "ana"}
	e := rolloutEnv(t, origin)
	_, body := e.get(t, "/apps")
	card := cardOf(t, body)
	got := text(card)

	t.Run("the card, its head row and its facts", func(t *testing.T) {
		for _, want := range []string{
			"Deploying now", "whoami", "v2 rolling out", "View deployment",
			"Change Pushed to main by ana · 4e0a77a",
			"Copies whoami v2 4b2e0000 (new, healthy) whoami v2 c81e0000 (new, starting) whoami v1 77b00000 (draining, v1) 1 of 2 new copies healthy",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the card lacks %q:\n%s", want, got)
			}
		}
		for _, want := range []string{
			`class="cv-panel cv-dcard"`, `href="/apps/whoami" class="cv-dcard-name"`, `cv-pill cv-pill-rolling`,
			`href="/apps/whoami/deployments#deployment-d2" class="cv-btn cv-btn-secondary cv-btn-sm"`,
		} {
			if !strings.Contains(card, want) {
				t.Errorf("the card lacks the markup %q:\n%s", want, card)
			}
		}
		if strings.Contains(got, "Roll back") || strings.Contains(strings.ToLower(got), "ship") {
			t.Errorf("the card offers a roll back, or says ship: %s", got)
		}
	})

	t.Run("a starting copy and a draining one have their own chips", func(t *testing.T) {
		if !strings.Contains(card, `class="cv-copychip cv-copychip-starting"`) || !strings.Contains(card, `class="cv-copychip cv-copychip-draining"`) {
			t.Errorf("the chips lack their states:\n%s", card)
		}
		if strings.Count(card, `title="copy `) != 3 {
			t.Errorf("want 3 chips:\n%s", card)
		}
		// The draining one shows the idle ring, the starting one the half mark, the healthy one the disc.
		for _, want := range []string{"cv-mark-idle", "cv-mark-rolling", "cv-mark-healthy"} {
			if !strings.Contains(card, want) {
				t.Errorf("the chips lack the mark %s", want)
			}
		}
		if !strings.Contains(got, "(new, starting)") || !strings.Contains(got, "(draining, v1)") || !strings.Contains(got, "(new, healthy)") {
			t.Errorf("the states aren't said in words for a reader that doesn't see the style: %s", got)
		}
		// The versions are drawn in mono of their own, and "N of M new copies healthy" is under the chips, not among them.
		if strings.Count(card, `class="cv-copychip-ver">v2<`) != 2 || strings.Count(card, `class="cv-copychip-ver">v1<`) != 1 {
			t.Errorf("the chips don't carry their versions:\n%s", card)
		}
		if !strings.Contains(card, `</div><span class="cv-dcard-sum">1 of 2 new copies healthy</span>`) &&
			!strings.Contains(card, "</div>\n      <span class=\"cv-dcard-sum\">1 of 2 new copies healthy</span>") {
			t.Errorf("the summary isn't on its own line under the chips:\n%s", card)
		}
	})

	t.Run("it is a live region of its own, on deployments and copies", func(t *testing.T) {
		for _, want := range []string{`hx-get="/parts/deploying"`, `hx-trigger="sse:deployments, sse:copies"`} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q", want)
			}
		}
		resp, part := e.get(t, "/parts/deploying")
		if resp.StatusCode != http.StatusOK || !strings.Contains(part, "cv-dcard") || strings.Contains(part, "<html") {
			t.Errorf("GET /parts/deploying = %d:\n%s", resp.StatusCode, part)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
		}
		if !strings.Contains(body, strings.TrimSpace(part)) {
			t.Errorf("the page doesn't contain what /parts/deploying answers:\n%s", part)
		}
		// The apps region carries it too, so a refresh of the region keeps the card.
		_, apps := e.get(t, "/parts/apps")
		if !strings.Contains(apps, "cv-dcard") {
			t.Errorf("/parts/apps lacks the card")
		}
	})

	t.Run("it draws nothing when no rollout runs", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seedStandard(t)
		_, body := e.get(t, "/apps")
		if !strings.Contains(body, `class="cv-deploying"`) || strings.Contains(body, "cv-dcard") || strings.Contains(body, `cv-label">Deploying now`) {
			t.Errorf("a card is drawn with no rollout, or the region is gone:\n%s", body)
		}
		if !strings.Contains(body, `hx-trigger="sse:deployments, sse:copies" hx-swap="innerHTML"></section>`) {
			t.Errorf("the empty region isn't empty (so it isn't hidden)")
		}
		if resp, part := e.get(t, "/parts/deploying"); resp.StatusCode != http.StatusOK || strings.TrimSpace(part) != "" {
			t.Errorf("GET /parts/deploying with no rollout = %d %q, want 200 and nothing", resp.StatusCode, part)
		}
	})
}

func TestDeployingNowChangeLines(t *testing.T) {
	tests := []struct {
		name   string
		origin *app.Origin
		seed   seedDeploy
		prior  []seedDeploy
		want   string
	}{
		{"the console", nil, seedDeploy{Trigger: app.TriggerConsole}, nil, "Change Deployed from the console"},
		{"the API", nil, seedDeploy{Trigger: app.TriggerManual}, nil, "Change Deployed through the API"},
		{"the webhook", nil, seedDeploy{Trigger: app.TriggerWebhook}, nil, "Change Deployed by the webhook"},
		{"a redeploy of a past version", nil, seedDeploy{Trigger: app.TriggerConsole, Source: "d1"}, nil, "Change v1 deployed again"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newConsoleEnv(t)
			tt.seed.ID, tt.seed.Status, tt.seed.Version = "d2", store.StatusRunning, 2
			e.seed(t, whoamiApp(), seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1}, tt.seed)
			_, body := e.get(t, "/apps")
			if got := text(cardOf(t, body)); !strings.Contains(got, tt.want) {
				t.Errorf("the card lacks %q: %s", tt.want, got)
			}
		})
	}

	t.Run("a promotion", func(t *testing.T) {
		e := newConsoleEnv(t)
		staging := whoamiApp()
		staging.ID = "stg"
		e.seed(t, staging, seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
		e.seed(t, whoamiApp(), seedDeploy{ID: "d2", Trigger: app.TriggerManual, Status: store.StatusRunning, Version: 2, Source: "s1"})
		_, body := e.get(t, "/apps")
		if got := text(cardOf(t, body)); !strings.Contains(got, "Change Promoted from production") {
			t.Errorf("the card lacks the promotion: %s", got)
		}
	})
}

func TestDeployingNowWithoutCopies(t *testing.T) {
	t.Run("a rollout Nomad hasn't placed a copy for", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusRunning, Version: 1})
		e.Nomad.AddForeignJob(&api.Job{ID: new("whoami")})
		e.Nomad.AddForeignJob(&api.Job{ID: new("whoami")})
		_, body := e.get(t, "/apps")
		card := cardOf(t, body)
		if got := text(card); !strings.Contains(got, "Waiting for Nomad to place the first copy.") || strings.Contains(card, "cv-copychip") {
			t.Errorf("the card is wrong with no copies: %s", got)
		}
	})
	t.Run("a deployment that isn't registered yet", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusPending})
		_, body := e.get(t, "/apps")
		card := cardOf(t, body)
		if got := text(card); !strings.Contains(got, "rolling out") || strings.Contains(got, "v0") || !strings.Contains(got, "Waiting for Nomad") {
			t.Errorf("a pending deployment's card is wrong: %s", got)
		}
	})
	t.Run("a failed deploy and a healthy one have no card", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: "image not found"})
		_, body := e.get(t, "/apps")
		if strings.Contains(body, "cv-dcard") {
			t.Errorf("a failed-before-register deploy has a card")
		}
	})
	t.Run("a stopped app has no card", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusRunning, Version: 1})
		at := time.Now()
		if err := e.Store.SetAppStopped(context.Background(), "whoami", &at); err != nil {
			t.Fatal(err)
		}
		_, body := e.get(t, "/apps")
		if strings.Contains(body, "cv-dcard") {
			t.Errorf("a stopped app has a card")
		}
	})
}

func TestDeployingNowSeveralAppsNewestFirst(t *testing.T) {
	e := newConsoleEnv(t)
	other := whoamiApp()
	other.ID = "second"
	other.Services[0].Domains[0].Host = "second.example.com"
	e.seed(t, whoamiApp(), seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusRunning, Version: 1})
	time.Sleep(10 * time.Millisecond)
	e.seed(t, other, seedDeploy{ID: "d2", Trigger: app.TriggerConsole, Status: store.StatusRunning, Version: 1})
	_, body := e.get(t, "/apps")
	card := cardOf(t, body)
	if a, b := strings.Index(card, `class="cv-dcard-name">second<`), strings.Index(card, `class="cv-dcard-name">whoami<`); a < 0 || b < 0 || a > b {
		t.Errorf("cards aren't newest first (second %d, whoami %d):\n%s", a, b, card)
	}
	if strings.Count(card, `cv-label">Deploying now`) != 1 {
		t.Errorf("the label is drawn more than once")
	}
}

// filterEnv has six apps: whoami healthy, blog (two services, its newest deploy reverted), a Compose
// app, a Postgres app, an app with a volume, and one never deployed.
func filterEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.seedStandard(t) // whoami, blog, api
	web := func(id, image, host string) app.App {
		a := whoamiApp()
		a.ID = id
		a.Services[0].Image = image
		a.Services[0].Domains[0].Host = host
		return a
	}
	shop := web("shop", "ghcr.io/acme/shop:1", "shop.example.com")
	shop.Source.Kind = app.SourceCompose
	e.seed(t, shop, seedDeploy{ID: "sh1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.copies("shop", "web", 1, 2, 0)
	pg := web("pgdata", "postgres:16", "pg.example.com")
	e.seed(t, pg, seedDeploy{ID: "pg1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.copies("pgdata", "web", 1, 2, 0)
	vol := web("files", "ghcr.io/acme/files:2", "files.example.com")
	vol.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	e.seed(t, vol, seedDeploy{ID: "f1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.copies("files", "web", 1, 2, 0)
	e.seed(t, web("idle", "ghcr.io/acme/idle:1", "idle.example.com"))
	return e
}

// rowsOf lists the app names the apps table shows, in order.
func rowsOf(body string) []string {
	var out []string
	for _, part := range strings.Split(body, `class="cv-app-name">`)[1:] {
		name := part[:strings.Index(part, "<")]
		if !strings.Contains(part[:strings.Index(part, "</a>")+4], "cv-feed") {
			out = append(out, name)
		}
	}
	return out
}

// appsTable is the main column's part of the page: the Activity rail repeats app names.
func appsTable(body string) string {
	i := strings.Index(body, `class="cv-main"`)
	j := strings.Index(body, `<aside class="cv-rail"`)
	if i < 0 || j < i {
		return body
	}
	return body[i:j]
}

func TestAppsFilter(t *testing.T) {
	e := filterEnv(t)
	names := func(t *testing.T, query string) []string {
		t.Helper()
		_, body := e.get(t, "/apps"+query)
		return rowsOf(appsTable(body))
	}
	sameSet := func(t *testing.T, query string, want ...string) {
		t.Helper()
		got := names(t, query)
		if len(got) != len(want) {
			t.Errorf("GET /apps%s lists %v, want %v", query, got, want)
			return
		}
		for _, w := range want {
			found := false
			for _, g := range got {
				found = found || g == w
			}
			if !found {
				t.Errorf("GET /apps%s lists %v, want %v", query, got, want)
			}
		}
	}

	t.Run("no filter lists every app and draws the row", func(t *testing.T) {
		_, body := e.get(t, "/apps")
		if got := names(t, ""); len(got) != 7 {
			t.Errorf("rows = %v, want the seven apps", got)
		}
		for _, want := range []string{`<form id="apps-filter" class="cv-filter" method="get" action="/apps" role="search" hx-preserve="true">`,
			`name="q" value="" placeholder="Filter apps" aria-label="Filter apps by name, image or address"`,
			`<select class="cv-field cv-select" name="state" aria-label="State">`, `<select class="cv-field cv-select" name="kind" aria-label="Kind">`,
			`>Apply</button>`, `hx-get="/parts/apps" hx-trigger="sse:apps, sse:sync"`} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q", want)
			}
		}
		if strings.Contains(body, ">Clear<") {
			t.Error("Clear is offered with no filter set")
		}
		for _, opt := range []string{"All states", "Needs you", "Rolling out", "Healthy", "Stopped", "Not deployed", "All kinds", "Image", "Compose", "Database", "Volume"} {
			if !strings.Contains(body, ">"+opt+"</option>") {
				t.Errorf("no option %q", opt)
			}
		}
	})

	t.Run("by name, case-insensitively", func(t *testing.T) { sameSet(t, "?q=BLO", "blog") })
	t.Run("by image", func(t *testing.T) { sameSet(t, "?q=acme/files", "files") })
	t.Run("by domain", func(t *testing.T) { sameSet(t, "?q=pg.example", "pgdata") })
	t.Run("by project and environment", func(t *testing.T) {
		sameSet(t, "?q=production", "whoami", "blog", "api", "shop", "pgdata", "files", "idle")
	})
	// "Needs you" is the apps the headline names: blog's newest deploy was reverted, and api has
	// 1 of 2 copies healthy.
	t.Run("by state: needs you", func(t *testing.T) { sameSet(t, "?state=needs", "blog", "api") })
	t.Run("by state: healthy", func(t *testing.T) { sameSet(t, "?state=healthy", "whoami", "shop", "pgdata", "files") })
	t.Run("by state: not deployed", func(t *testing.T) { sameSet(t, "?state=notdeployed", "idle") })
	t.Run("by kind: compose", func(t *testing.T) { sameSet(t, "?kind=compose", "shop") })
	t.Run("by kind: database", func(t *testing.T) { sameSet(t, "?kind=database", "pgdata") })
	t.Run("by kind: volume", func(t *testing.T) { sameSet(t, "?kind=volume", "files") })
	t.Run("by kind: image", func(t *testing.T) { sameSet(t, "?kind=image", "whoami", "blog", "api", "idle") })
	t.Run("combined", func(t *testing.T) {
		sameSet(t, "?q=example&state=healthy&kind=database", "pgdata")
		sameSet(t, "?q=shop&state=needs")
	})
	t.Run("a value the selects don't offer is none", func(t *testing.T) {
		sameSet(t, "?state=bogus&kind=bogus&q=", "whoami", "blog", "api", "shop", "pgdata", "files", "idle")
	})

	t.Run("nothing matching says so, with Clear, and the headline and needs-you stay whole", func(t *testing.T) {
		_, body := e.get(t, "/apps?q=nothing-called-this")
		got := text(appsTable(body))
		if !strings.Contains(got, "No app matches. Clear") || !strings.Contains(body, `<a href="/apps" class="cv-btn cv-btn-secondary cv-btn-sm">Clear</a>`) {
			t.Errorf("no 'No app matches.' with Clear: %s", got)
		}
		if strings.Contains(body, `role="table"`) {
			t.Error("a table is drawn with nothing matching")
		}
		_, all := e.get(t, "/apps")
		if h1Text(t, body) != h1Text(t, all) {
			t.Errorf("the headline changed with the filter: %q vs %q", h1Text(t, body), h1Text(t, all))
		}
		if !strings.Contains(text(body), "blog") || !strings.Contains(body, `aria-label="Needs you"`) {
			t.Error("the needs-you list was filtered")
		}
	})

	t.Run("the kind word follows the name", func(t *testing.T) {
		_, body := e.get(t, "/apps")
		for name, word := range map[string]string{"shop": "compose", "pgdata": "database", "files": "volume"} {
			want := `class="cv-app-name">` + name + `</a><span class="cv-kind">` + word + `</span>`
			if !strings.Contains(body, want) {
				t.Errorf("no kind word %q after %s", word, name)
			}
		}
		for _, name := range []string{"whoami", "blog", "api", "idle"} {
			if strings.Contains(body, `class="cv-app-name">`+name+`</a><span class="cv-kind">`) {
				t.Errorf("the image app %s has a kind word", name)
			}
		}
	})

	t.Run("the live refresh keeps the query, and so does the part", func(t *testing.T) {
		_, body := e.get(t, "/apps?q=example&state=healthy&kind=database")
		want := `hx-get="/parts/apps?kind=database&amp;q=example&amp;state=healthy"`
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
		if !strings.Contains(body, ">Clear</a>") || !strings.Contains(body, `value="example"`) ||
			!strings.Contains(body, `<option value="healthy" selected>`) || !strings.Contains(body, `<option value="database" selected>`) {
			t.Error("the form doesn't show what was asked for")
		}
		_, part := e.get(t, "/parts/apps?"+url.Values{"q": {"pg.example"}}.Encode())
		if got := rowsOf(part); len(got) != 1 || got[0] != "pgdata" {
			t.Errorf("/parts/apps?q= lists %v, want only pgdata", got)
		}
	})
}

func TestAppsFilterIsDrawnOnlyWithMoreThanFiveApps(t *testing.T) {
	e := newConsoleEnv(t)
	e.seedStandard(t) // three apps
	_, body := e.get(t, "/apps")
	if strings.Contains(body, `id="apps-filter"`) {
		t.Error("the filter is drawn with three apps")
	}
	// Under the limit the query is ignored too: nothing could be changed in a form that isn't there.
	if got := rowsOf(appsTable(mustGet(t, e, "/apps?q=zzz"))); len(got) != 3 {
		t.Errorf("a query with three apps lists %v, want all three", got)
	}
	for i := range 2 { // five apps
		a := whoamiApp()
		a.ID = fmt.Sprintf("extra%d", i)
		a.Services[0].Domains[0].Host = a.ID + ".example.com"
		e.seed(t, a)
	}
	if _, body := e.get(t, "/apps"); strings.Contains(body, `id="apps-filter"`) {
		t.Error("the filter is drawn with five apps")
	}
	a := whoamiApp()
	a.ID = "extra9"
	a.Services[0].Domains[0].Host = "extra9.example.com"
	e.seed(t, a)
	if _, body := e.get(t, "/apps"); !strings.Contains(body, `id="apps-filter"`) {
		t.Error("the filter isn't drawn with six apps")
	}
}

func mustGet(t *testing.T, e *consoleEnv, path string) string {
	t.Helper()
	_, body := e.get(t, path)
	return body
}

func TestAppsFilterInGroups(t *testing.T) {
	e := filterEnv(t)
	ctx := context.Background()
	if _, err := e.Svc.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	web := whoamiApp()
	web.ID = "shop--staging--web"
	web.Services[0].Domains[0].Host = "staging.shop.example.com"
	e.seed(t, web, seedDeploy{ID: "sw1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.copies("shop--staging--web", "web", 1, 2, 0)

	_, all := e.get(t, "/apps")
	if !strings.Contains(all, "cv-env-title") || !strings.Contains(all, "No apps in") {
		t.Fatalf("the groups aren't drawn:\n%s", all)
	}
	_, body := e.get(t, "/apps?q=staging.shop")
	table := appsTable(body)
	if got := rowsOf(table); len(got) != 1 || got[0] != "web" {
		t.Errorf("rows = %v, want only web", got)
	}
	if strings.Contains(table, "No apps in") || strings.Contains(text(table), "Default") || strings.Count(table, `cv-env-title`) != 1 {
		t.Errorf("groups without a match are drawn:\n%s", text(table))
	}
	_, none := e.get(t, "/apps?q=zzzzz")
	if strings.Contains(none, "cv-project") || !strings.Contains(none, "No app matches.") {
		t.Errorf("nothing matching in groups is wrong:\n%s", text(appsTable(none)))
	}
}

func TestActivityRailSaysWho(t *testing.T) {
	e := newConsoleEnv(t)
	staging := whoamiApp()
	staging.ID = "stg"
	staging.Services[0].Domains[0].Host = "stg.example.com"
	e.seed(t, staging, seedDeploy{ID: "s1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	origin := &app.Origin{Commit: "4e0a77aabb", Ref: "refs/heads/main", Actor: "ana"}
	e.seed(t, whoamiApp(),
		seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1},
		seedDeploy{ID: "w2", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 2, Origin: origin},
		seedDeploy{ID: "w3", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 3, TokenName: "ci-github"},
		seedDeploy{ID: "w4", Trigger: app.TriggerWebhook, Status: store.StatusHealthy, Version: 4},
		seedDeploy{ID: "w5", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 5, Source: "s1"})

	_, part := e.get(t, "/parts/activity")
	got := text(part)
	for _, want := range []string{
		"from the console · ",
		"pushed to main by ana · 4e0a77a · ",
		"through the API by ci-github · ",
		"by the webhook · ",
		"promoted from production · ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the rail lacks %q:\n%s", want, got)
		}
	}
	// The branch and the commit are in mono, the token's name too.
	for _, want := range []string{`<span class="cv-inline-mono text-fg-1">main</span>`, `<span class="cv-inline-mono text-fg-1">4e0a77a</span>`,
		`<span class="cv-inline-mono text-fg-1">ci-github</span>`} {
		if !strings.Contains(part, want) {
			t.Errorf("the rail lacks the mono part %q", want)
		}
	}
	// A deploy made without the token's name says only "through the API".
	if strings.Count(got, "through the API by") != 1 {
		t.Errorf("only the token deploy says by whom:\n%s", got)
	}
}

// ---- the deployment entry ------------------------------------------------------------

// entryOf is one history entry of a page.
func entryOf(t *testing.T, body, id string) string {
	t.Helper()
	i := strings.Index(body, `id="deployment-`+id+`"`)
	if i < 0 {
		t.Fatalf("no entry %s in:\n%s", id, body)
	}
	rest := body[i:]
	if j := strings.Index(rest[1:], `id="deployment-`); j >= 0 {
		rest = rest[:j+1]
	}
	return rest
}

func TestDeploymentEntriesHaveStages(t *testing.T) {
	e := historyEnv(t) // d1, d2 healthy; d3 reverted; d4 failed before register; d5 superseded; d6 running 1 of 2
	_, body := e.get(t, "/apps/blog/deployments")
	strip := func(id string) string {
		t.Helper()
		entry := entryOf(t, body, id)
		i := strings.Index(entry, `class="cv-stageswrap"`)
		if i < 0 {
			t.Fatalf("entry %s has no stages strip:\n%s", id, entry)
		}
		// The strip is under the head and above the sentence.
		if sentence := strings.Index(entry, `class="cv-deploy-sentence"`); sentence < i {
			t.Errorf("entry %s: the strip is after the sentence", id)
		}
		return text(entry[i:strings.Index(entry, `class="cv-deploy-sentence"`)])
	}
	// The entries that get the panel: the running one, the newest, and what failed or was reverted.
	for id, want := range map[string][]string{
		"d3": {"Queued done:", "Roll out failed: failed", "Live not reached: —"},
		"d4": {"Queued failed: didn't start", "Roll out not reached: —", "Live not reached: —"},
		"d6": {"Queued done:", "Roll out in progress: 1 of 2 new copies healthy", "Live not reached: —"},
	} {
		got := strip(id)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("entry %s: the strip lacks %q: %s", id, w, got)
			}
		}
	}
	// The marks and the active cell.
	if e6 := entryOf(t, body, "d6"); !strings.Contains(e6, `class="cv-stage cv-stage-active" aria-current="step"`) ||
		strings.Count(e6, "cv-stage-active") != 1 {
		t.Errorf("the running entry doesn't underline its Roll out cell:\n%s", e6)
	}
	if strings.Contains(entryOf(t, body, "d2"), "cv-stage-active") {
		t.Error("a healthy entry has an active cell")
	}
	if strings.Contains(entryOf(t, body, "d5"), "cv-stage-active") {
		t.Error("a superseded entry has an active cell")
	}
	if e4 := entryOf(t, body, "d4"); !strings.Contains(e4, `cv-mark cv-mark-failed`) || !strings.Contains(e4, "cv-stage-todo") {
		t.Errorf("the failed-before-register entry lacks its failed mark or quiet names:\n%s", e4)
	}
	// The entry's own sentence and its error stay where they were.
	if got := text(entryOf(t, body, "d4")); !strings.Contains(got, "This deploy didn't start: the image wasn't found.") {
		t.Errorf("d4's sentence changed: %s", got)
	}
	// Narrow: the strip is a container the cells stack in; the rule is in the stylesheet.
	css := mustGet(t, e, "/static/app.css")
	for _, want := range []string{".cv-stageswrap", "659.98px", ".cv-stageline"} {
		if !strings.Contains(css, want) {
			t.Errorf("the stylesheet lacks %q", want)
		}
	}
}

// revertEnv has whoami with v1 healthy and v2 reverted, and the copies Nomad's rollback made as v3.
func revertEnv(t *testing.T, withDefinition bool) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	var def *app.App
	if withDefinition {
		def = new(whoamiApp())
	}
	e.seed(t, whoamiApp(),
		seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1, Definition: def},
		seedDeploy{ID: "d2", Trigger: app.TriggerManual, Status: store.StatusReverted, Version: 2,
			Error: "Failed due to unhealthy allocations - rolling back to job version 1"})
	e.copies("whoami", "web", 3, 2, 0)
	return e
}

func TestRollbackNamesWhatWasDeployedAgain(t *testing.T) {
	e := revertEnv(t, true)

	t.Run("the entry, the lede and the needs-you list agree", func(t *testing.T) {
		_, entries := e.get(t, "/apps/whoami/deployments")
		if got := text(entryOf(t, entries, "d2")); !strings.Contains(got, "v2 failed its health checks. Nomad rolled back to v3 (v1 again), which is serving.") {
			t.Errorf("the entry says: %s", got)
		}
		_, lede := e.get(t, "/apps/whoami")
		if got := text(lede); !strings.Contains(got, "v2 failed its health checks. v3 (v1 again) keeps running.") {
			t.Errorf("the lede says: %s", got)
		}
		_, header := e.get(t, "/parts/apps/whoami/header")
		if got := text(header); !strings.Contains(got, "v3 (v1 again) keeps running.") {
			t.Errorf("the header part says: %s", got)
		}
		_, home := e.get(t, "/apps")
		if got := text(home); !strings.Contains(got, "v3 (v1 again) keeps running.") {
			t.Errorf("the needs-you list says: %s", got)
		}
	})

	t.Run("nothing to match: the wording stays as it was", func(t *testing.T) {
		e := revertEnv(t, false) // v0's definition wasn't recorded
		_, entries := e.get(t, "/apps/whoami/deployments")
		if got := text(entryOf(t, entries, "d2")); strings.Contains(got, " again)") || !strings.Contains(got, "Nomad rolled back to v3, which is serving.") {
			t.Errorf("the entry says: %s", got)
		}
		_, lede := e.get(t, "/apps/whoami")
		if got := text(lede); strings.Contains(got, " again)") || !strings.Contains(got, "v3 keeps running.") {
			t.Errorf("the lede says: %s", got)
		}
	})

	t.Run("without Nomad the entry keeps the version in Nomad's message", func(t *testing.T) {
		e := revertEnv(t, true)
		e.Nomad.Down()
		_, entries := e.get(t, "/apps/whoami/deployments")
		got := text(entryOf(t, entries, "d2"))
		if strings.Contains(got, "v3") || !strings.Contains(got, "Nomad rolled back to v1, which is serving.") {
			t.Errorf("the entry says: %s", got)
		}
	})
}

// A page that has the strip, the card and the filter must pass the same checks as the others.
func TestPage4PartsAreNotWholePages(t *testing.T) {
	e := rolloutEnv(t, nil)
	for _, path := range []string{"/parts/deploying", "/parts/apps", "/parts/apps/whoami/deployments"} {
		resp, part := e.get(t, path)
		if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") {
			t.Errorf("GET %s = %d:\n%s", path, resp.StatusCode, part)
		}
	}
	// The parts need a session like every other part.
	resp, _ := e.do(t, request{path: "/parts/deploying", headers: map[string]string{"HX-Current-URL": e.URL + "/apps"}})
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("HX-Redirect") == "" {
		t.Errorf("GET /parts/deploying without a session = %d", resp.StatusCode)
	}
}

// Older healthy and superseded entries get their stages as one line in place of the panel, and
// drop "took ..." from the meta, which the line carries; the newest entry keeps the panel.
func TestOlderEntriesGetAOneLineStrip(t *testing.T) {
	e := historyEnv(t) // d1, d2 healthy; d3 reverted; d4 failed; d5 superseded; d6 running
	_, body := e.get(t, "/apps/blog/deployments")
	for _, id := range []string{"d1", "d2", "d5"} {
		entry := entryOf(t, body, id)
		if strings.Contains(entry, "cv-stageswrap") || !strings.Contains(entry, `<ol class="cv-stageline cv-data-muted" aria-label="Stages">`) {
			t.Errorf("entry %s isn't a one-line strip:\n%s", id, entry)
		}
		meta := entry[strings.Index(entry, `class="cv-deploy-meta`):]
		if meta = meta[:strings.Index(meta, "</span>\n")]; strings.Contains(meta, "took") {
			t.Errorf("entry %s's meta still says how long it took: %s", id, meta)
		}
	}
	// The marks and their words are the panel's, in one line.
	d2 := text(entryOf(t, body, "d2"))
	for _, want := range []string{"done: Queued", "done: Roll out", "done: Live"} {
		if !strings.Contains(d2, want) {
			t.Errorf("d2's line lacks %q: %s", want, d2)
		}
	}
	d5 := text(entryOf(t, body, "d5"))
	for _, want := range []string{"done: Queued", "not reached: Roll out superseded", "not reached: Live"} {
		if !strings.Contains(d5, want) {
			t.Errorf("d5's line lacks %q: %s", want, d5)
		}
	}
	if !strings.Contains(entryOf(t, body, "d2"), `<span class="cv-mark cv-mark-healthy" aria-hidden="true"></span><span class="sr-only">done: </span>Queued`) {
		t.Errorf("the line's marks lack their screen-reader words:\n%s", entryOf(t, body, "d2"))
	}
	// The ones that explain themselves, and the running one, keep the panel and their "took" or "running for".
	for _, id := range []string{"d3", "d4", "d6"} {
		if entry := entryOf(t, body, id); !strings.Contains(entry, "cv-stageswrap") || strings.Contains(entry, "cv-stageline") {
			t.Errorf("entry %s lacks the full strip:\n%s", id, entry)
		}
	}
	for id, want := range map[string]string{"d3": "took ", "d6": "running for "} {
		if !strings.Contains(text(entryOf(t, body, id)), want) {
			t.Errorf("entry %s's meta lacks %q", id, want)
		}
	}

	t.Run("the newest entry, healthy, keeps the panel and its time", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(),
			seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1},
			seedDeploy{ID: "d2", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 2})
		e.copies("whoami", "web", 2, 2, 0)
		_, body := e.get(t, "/apps/whoami/deployments")
		d2, d1 := entryOf(t, body, "d2"), entryOf(t, body, "d1")
		if !strings.Contains(d2, "cv-stageswrap") || !strings.Contains(d2, `UTC"><span class="sr-only">done: </span>`) || !strings.Contains(text(d2), "took ") {
			t.Errorf("the newest healthy entry lacks its panel, the Live time or 'took':\n%s", d2)
		}
		if strings.Contains(d1, "cv-stageswrap") || !strings.Contains(d1, "cv-stageline") || strings.Contains(text(d1), "took ") {
			t.Errorf("the older healthy entry isn't one line:\n%s", d1)
		}
		// The line keeps the Live time, with the date on hover, too.
		if !strings.Contains(d1, `<span title="`) || !strings.Contains(text(d1), "UTC") {
			t.Errorf("the line's Live time is missing:\n%s", d1)
		}
	})
}

// The strip, the card and the running entry's progress line agree on how many new copies are healthy.
func TestProgressWordingAgrees(t *testing.T) {
	e := rolloutEnv(t, nil) // v2 rolling out: 2 copies wanted, 2 placed, 1 healthy
	_, body := e.get(t, "/apps/whoami/deployments")
	d2 := text(entryOf(t, body, "d2"))
	for _, want := range []string{
		"Roll out in progress: 1 of 2 new copies healthy",
		"2 of 2 new copies started, 1 healthy. Old copies keep serving until the new ones pass their checks.",
	} {
		if !strings.Contains(d2, want) {
			t.Errorf("the running entry lacks %q: %s", want, d2)
		}
	}
	if card := text(cardOf(t, mustGet(t, e, "/apps"))); !strings.Contains(card, "1 of 2 new copies healthy") {
		t.Errorf("the card doesn't use the shared wording: %s", card)
	}
}

// A reverted entry's raw line says whose words it is; the other entries' raw lines don't.
func TestRevertedEntryRawLineSaysNomadSaid(t *testing.T) {
	e := historyEnv(t)
	_, body := e.get(t, "/apps/blog/deployments")
	d3 := entryOf(t, body, "d3")
	if want := `<span class="cv-detail-lead">Nomad said: </span>Failed due to unhealthy allocations - rolling back to job version 2</p>`; !strings.Contains(d3, want) {
		t.Errorf("the reverted entry's raw line lacks %q:\n%s", want, d3)
	}
	for _, id := range []string{"d4", "d5"} {
		if entry := entryOf(t, body, id); strings.Contains(entry, "Nomad said") {
			t.Errorf("entry %s says 'Nomad said':\n%s", id, entry)
		}
	}
}

// While an app rolls out its Version cell reads "v1 → v2", the arrow and the new version quiet.
func TestVersionCellShowsTheRollout(t *testing.T) {
	cell := func(body string) string {
		i := strings.Index(body, `class="cv-c-version" data-label="Version">`)
		if i < 0 {
			t.Fatalf("no Version cell:\n%s", body)
		}
		rest := body[i:]
		return rest[:strings.Index(rest, "</div>")]
	}
	t.Run("a rollout over a live version", func(t *testing.T) {
		// v1 still serves (healthy, asked to run) while v2 starts: blue/green keeps the old copies until promotion.
		e := rolloutEnv(t, nil)
		yes := true
		e.Nomad.SetCopies("whoami",
			api.AllocationListStub{ID: "77b00000-0000-4000-8000-000000000001", TaskGroup: "web", JobVersion: 1, ClientStatus: "running", NodeName: "n1",
				DeploymentStatus: &api.AllocDeploymentStatus{Healthy: &yes}},
			api.AllocationListStub{ID: "c81e0000-0000-4000-8000-000000000003", TaskGroup: "web", JobVersion: 2, ClientStatus: "pending", NodeName: "n1"})
		got := cell(appsTable(mustGet(t, e, "/apps")))
		want := `<span class="cv-data text-fg-1">v1</span> <span class="cv-data text-fg-3"><span aria-hidden="true">→ </span><span class="sr-only">rolling out </span>v2</span>`
		if !strings.Contains(got, want) {
			t.Errorf("the Version cell = %s, want it to contain %s", got, want)
		}
		if txt := text(got); !strings.Contains(txt, "v1") || !strings.Contains(txt, "→ rolling out v2") {
			t.Errorf("text = %q", txt)
		}
	})
	t.Run("a settled app shows its version alone", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1})
		e.copies("whoami", "web", 1, 2, 0)
		got := cell(appsTable(mustGet(t, e, "/apps")))
		if strings.Contains(got, "→") || !strings.Contains(got, "v1") {
			t.Errorf("the Version cell = %s", got)
		}
	})
}

// The state filter's "Needs you" is the apps the headline names as needing the user, whatever the
// reason: a failed backup counts as a failed deploy does, and an app that is stopped is only stopped.
func TestNeedsYouFilterFollowsTheNeedsYouList(t *testing.T) {
	e := filterEnv(t) // whoami, blog (reverted), api, shop, pgdata, files, idle
	db := whoamiApp()
	db.ID = "dbapp"
	db.Services[0].Domains[0].Host = "db.example.com"
	db.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres, Schedule: "30 14 * * *", Keep: 2}
	e.seed(t, db, seedDeploy{ID: "db1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.copies("dbapp", "web", 1, 2, 0)
	// shop has a failed backup too, but it is stopped on purpose.
	shop := e.storedApp(t, "shop")
	shop.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres, Schedule: "30 14 * * *", Keep: 2}
	if _, err := e.Store.SaveApp(context.Background(), shop, nil); err != nil {
		t.Fatal(err)
	}
	at := e.Clock.Now()
	if err := e.Store.SetAppStopped(context.Background(), "shop", &at); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"dbapp", "shop"} {
		fin := at.Add(-time.Hour)
		if err := e.Store.SaveBackup(context.Background(), store.BackupRecord{AppID: id, ID: id + "-1", Service: "web", Engine: "postgres",
			Status: store.BackupFailed, Message: "pg_dump: connection refused", StartedAt: at.Add(-time.Hour - time.Minute), FinishedAt: &fin}); err != nil {
			t.Fatal(err)
		}
	}

	body := mustGet(t, e, "/apps?state=needs")
	got := rowsOf(appsTable(body))
	slices.Sort(got)
	if want := []string{"api", "blog", "dbapp"}; !slices.Equal(got, want) {
		t.Errorf("state=needs lists %v, want %v", got, want)
	}
	// The headline names the same apps.
	if head := text(body[:strings.Index(body, `aria-label="Needs you"`)]); !strings.Contains(head, "3 apps need you") {
		t.Errorf("the headline doesn't name the filter's three apps: %s", head)
	}
	// Each is in the needs-you list above the filter, which says why.
	list := text(body[strings.Index(body, `aria-label="Needs you"`):strings.Index(body, `class="cv-deploying"`)])
	for _, want := range []string{"The latest backup of dbapp/web failed.", "blog"} {
		if !strings.Contains(list, want) {
			t.Errorf("the needs-you list lacks %q: %s", want, list)
		}
	}
	if got := rowsOf(appsTable(mustGet(t, e, "/apps?state=stopped"))); !slices.Equal(got, []string{"shop"}) {
		t.Errorf("state=stopped lists %v, want only shop", got)
	}
}

func TestStateKey(t *testing.T) {
	now := time.Now()
	deployed := &store.DeploymentRecord{Status: store.StatusHealthy}
	tests := []struct {
		name     string
		sum      service.AppSummary
		st       statusView
		needsYou bool
		want     string
	}{
		{"an item in the needs-you list", service.AppSummary{LastDeployment: deployed}, statusView{"healthy", "healthy"}, true, stateNeeds},
		{"an item beats a rollout", service.AppSummary{LastDeployment: deployed}, statusView{"rolling", "rolling out"}, true, stateNeeds},
		{"stopped beats an item", service.AppSummary{StoppedAt: &now, LastDeployment: deployed}, statusView{"idle", "stopped"}, true, stateStopped},
		{"a warning status with no item is not on the list", service.AppSummary{LastDeployment: deployed}, statusView{"warning", "degraded"}, false, ""},
		{"rolling out", service.AppSummary{LastDeployment: deployed}, statusView{"rolling", "rolling out"}, false, stateRolling},
		{"healthy", service.AppSummary{LastDeployment: deployed}, statusView{"healthy", "healthy"}, false, stateHealthy},
		{"never deployed", service.AppSummary{Status: service.AppStatus{State: service.StateNotRunning}}, statusView{"idle", "not running"}, false, stateNotDeployed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stateKey(tt.sum, tt.st, tt.needsYou); got != tt.want {
				t.Errorf("stateKey = %q, want %q", got, tt.want)
			}
		})
	}
}
