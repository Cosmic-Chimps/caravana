package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// deployRowsEnv has app "blog" in Default › production, "docs" beside it and
// "shop--staging--web" elsewhere. Deployments get the store's own times (it
// reads the real clock), so the tests take their ranges from the records.
type deployRowsEnv struct {
	*env
	ctx context.Context
}

func deployRowsFixture(t *testing.T) *deployRowsEnv {
	t.Helper()
	e := newEnv(t)
	for _, id := range []string{"blog", "docs", "shop--staging--web"} {
		if _, err := e.store.SaveApp(context.Background(), appWith(id, 1), nil); err != nil {
			t.Fatalf("SaveApp %s: %v", id, err)
		}
	}
	return &deployRowsEnv{env: e, ctx: context.Background()}
}

// deployment records a deployment of appID with d's trigger, token and origin
// and brings it to final (pending when final is StatusPending, running when it
// is StatusRunning), with job version v and the error text.
func (e *deployRowsEnv) deployment(t *testing.T, appID, id string, d app.Deployment, final store.Status, v uint64, errText string) store.DeploymentRecord {
	t.Helper()
	base := apptest.Whoami().Deployment
	d.ID, d.Digests = id, base.Digests
	time.Sleep(2 * time.Millisecond)
	if _, err := e.store.CreateDeployment(e.ctx, appID, d, nil, nil, ""); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	var steps []store.Status
	if final != store.StatusPending {
		steps = append(steps, store.StatusRunning)
		if final != store.StatusRunning {
			steps = append(steps, final)
		}
	}
	for _, st := range steps {
		time.Sleep(2 * time.Millisecond)
		u := store.DeploymentUpdate{Status: st}
		if st == store.StatusRunning {
			u.JobVersion = &v
		}
		if st == final {
			u.Error = errText
		}
		if _, err := e.store.UpdateDeployment(e.ctx, id, u); err != nil {
			t.Fatalf("%s -> %s: %v", id, st, err)
		}
	}
	rec, err := e.store.GetDeployment(e.ctx, id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return rec
}

func (e *deployRowsEnv) rows(t *testing.T, from, to time.Time, onlyApp string) []DeployRow {
	t.Helper()
	rows, err := e.svc.LogDeploys(e.ctx, "", "", from, to, onlyApp)
	if err != nil {
		t.Fatalf("LogDeploys: %v", err)
	}
	return rows
}

func rowKinds(rows []DeployRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.DeploymentID + ":" + string(r.Kind)
	}
	return out
}

func TestLogDeploysKindsAndWords(t *testing.T) {
	e := deployRowsFixture(t)
	manual := app.Deployment{Trigger: app.TriggerManual, TokenName: "ci-token"}
	ci := app.Deployment{Trigger: app.TriggerManual, Origin: &app.Origin{Commit: "4e0a77c", Ref: "refs/heads/main", Actor: "ana"}}
	hook := app.Deployment{Trigger: app.TriggerWebhook}
	console := app.Deployment{Trigger: app.TriggerConsole}

	d1 := e.deployment(t, "blog", "d1", ci, store.StatusHealthy, 5, "")
	d2 := e.deployment(t, "blog", "d2", hook, store.StatusFailed, 6, "boom")
	d3 := e.deployment(t, "blog", "d3", manual, store.StatusReverted, 7, "Failed due to unhealthy allocations - rolling back to job version 2")
	d4 := e.deployment(t, "blog", "d4", console, store.StatusReverted, 8, "health check failed")
	d5 := e.deployment(t, "blog", "d5", console, store.StatusSuperseded, 9, "")
	d6 := e.deployment(t, "blog", "d6", console, store.StatusRunning, 10, "")
	d7 := e.deployment(t, "blog", "d7", console, store.StatusPending, 0, "")
	e.deployment(t, "docs", "docs1", console, store.StatusHealthy, 1, "")
	e.deployment(t, "shop--staging--web", "shop1", console, store.StatusHealthy, 1, "")

	all := e.rows(t, d1.CreatedAt.Add(-time.Hour), time.Now().Add(time.Hour), "")
	// Default › production only: the shop's deployment is in another environment.
	for _, r := range all {
		if r.DeploymentID == "shop1" {
			t.Fatalf("a row of another environment: %+v", r)
		}
	}
	blog := e.rows(t, d1.CreatedAt.Add(-time.Hour), time.Now().Add(time.Hour), "blog")
	if len(all) != len(blog)+2 {
		t.Errorf("%d rows for the environment, %d for blog: docs should add its two", len(all), len(blog))
	}
	type want struct {
		id, kind, who      string
		version, revertsTo int
	}
	wants := []want{
		{"d1", "started", "pushed to main by ana", 5, 0},
		{"d1", "live", "pushed to main by ana", 5, 0},
		{"d2", "started", "by the webhook", 6, 0},
		{"d2", "failed", "by the webhook", 6, 0},
		{"d3", "started", "through the API by ci-token", 7, 0},
		{"d3", "reverted", "through the API by ci-token", 7, 2}, // from Nomad's message
		{"d4", "started", "from the console", 8, 0},
		{"d4", "reverted", "from the console", 8, 5}, // no message: the last healthy deployment's version
		{"d5", "started", "from the console", 9, 0},
		{"d5", "superseded", "from the console", 9, 0},
		{"d6", "started", "from the console", 10, 0},
		{"d7", "started", "from the console", 0, 0},
	}
	if len(blog) != len(wants) {
		t.Fatalf("%d rows %q, want %d", len(blog), rowKinds(blog), len(wants))
	}
	// Rows come oldest first: starts and ends interleave as they happened.
	for i := 1; i < len(blog); i++ {
		if blog[i].At.Before(blog[i-1].At) {
			t.Errorf("row %d (%s) is before row %d", i, blog[i].At, i-1)
		}
	}
	got := map[string]DeployRow{}
	for _, r := range blog {
		got[r.DeploymentID+":"+string(r.Kind)] = r
	}
	times := map[string]store.DeploymentRecord{"d1": d1, "d2": d2, "d3": d3, "d4": d4, "d5": d5, "d6": d6, "d7": d7}
	for _, w := range wants {
		r, ok := got[w.id+":"+w.kind]
		if !ok {
			t.Errorf("no %s row for %s", w.kind, w.id)
			continue
		}
		if r.App != "blog" || r.Service != "" || r.Who != w.who || r.JobVersion != w.version || r.RevertedTo != w.revertsTo {
			t.Errorf("%s %s = %+v, want who %q version %d reverted to %d", w.id, w.kind, r, w.who, w.version, w.revertsTo)
		}
		wantAt := times[w.id].CreatedAt
		if w.kind != "started" {
			wantAt = times[w.id].UpdatedAt
		}
		if !r.At.Equal(wantAt) {
			t.Errorf("%s %s at %s, want %s", w.id, w.kind, r.At, wantAt)
		}
	}
}

func TestLogDeploysRangeEdges(t *testing.T) {
	e := deployRowsFixture(t)
	console := app.Deployment{Trigger: app.TriggerConsole}
	a := e.deployment(t, "blog", "a", console, store.StatusHealthy, 1, "")
	b := e.deployment(t, "blog", "b", console, store.StatusFailed, 2, "x")

	tests := []struct {
		name     string
		from, to time.Time
		want     []string
	}{
		{"exactly a start", a.CreatedAt, a.CreatedAt, []string{"a:started"}},
		{"exactly an end", a.UpdatedAt, a.UpdatedAt, []string{"a:live"}},
		{"just before a start", time.Time{}, a.CreatedAt.Add(-time.Nanosecond), nil},
		{"just after the last end", b.UpdatedAt.Add(time.Nanosecond), b.UpdatedAt.Add(time.Hour), nil},
		{"the end of one and the start of the next", a.UpdatedAt, b.CreatedAt, []string{"a:live", "b:started"}},
		{"only the second's end", b.CreatedAt.Add(time.Nanosecond), b.UpdatedAt, []string{"b:failed"}},
	}
	for _, tc := range tests {
		from := tc.from
		if from.IsZero() {
			from = a.CreatedAt.Add(-time.Hour)
		}
		if got := rowKinds(e.rows(t, from, tc.to, "")); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLogDeploysOnlyApp(t *testing.T) {
	e := deployRowsFixture(t)
	console := app.Deployment{Trigger: app.TriggerConsole}
	a := e.deployment(t, "blog", "b1", console, store.StatusHealthy, 1, "")
	e.deployment(t, "docs", "d1", console, store.StatusHealthy, 1, "")
	from, to := a.CreatedAt.Add(-time.Hour), time.Now().Add(time.Hour)

	if got := rowKinds(e.rows(t, from, to, "docs")); !slices.Equal(got, []string{"d1:started", "d1:live"}) {
		t.Errorf("docs = %q", got)
	}
	if got := rowKinds(e.rows(t, from, to, "nothing")); len(got) != 0 {
		t.Errorf("an app with no deploys = %q", got)
	}
	if got := rowKinds(e.rows(t, from, to, "")); len(got) != 4 {
		t.Errorf("no filter = %q, want both apps' four rows", got)
	}
	// Another place: the shop's staging has nothing here, and asking for it by name works.
	rows, err := e.svc.LogDeploys(e.ctx, "nope", "none", from, to, "")
	if err != nil || len(rows) != 0 {
		t.Errorf("unknown place = %v, %v, want none", rows, err)
	}
}

func TestDeployWhoWords(t *testing.T) {
	rec := func(d app.Deployment, srcApp, srcEnv string) store.DeploymentRecord {
		return store.DeploymentRecord{Deployment: d, SourceApp: srcApp, SourceEnvironment: srcEnv}
	}
	tests := []struct {
		name string
		rec  store.DeploymentRecord
		want string
	}{
		{"promotion beats the origin", rec(app.Deployment{Origin: &app.Origin{Ref: "main"}}, "shop--staging--web", "staging"), "promoted from staging"},
		{"branch and actor", rec(app.Deployment{Origin: &app.Origin{Ref: "refs/tags/v1.2", Actor: "ana", Commit: "4e0a77c"}}, "", ""), "pushed to v1.2 by ana"},
		{"actor only", rec(app.Deployment{Origin: &app.Origin{Actor: "ana"}}, "", ""), "pushed by ana"},
		{"commit only", rec(app.Deployment{Origin: &app.Origin{Commit: "4e0a77c"}}, "", ""), "pushed"},
		{"a run alone says nothing of who", rec(app.Deployment{Trigger: app.TriggerWebhook, Origin: &app.Origin{Run: "https://example.com/run/1"}}, "", ""), "by the webhook"},
		{"api", rec(app.Deployment{Trigger: app.TriggerManual}, "", ""), "through the API"},
		{"api with a token", rec(app.Deployment{Trigger: app.TriggerManual, TokenName: "ci"}, "", ""), "through the API by ci"},
		{"console", rec(app.Deployment{Trigger: app.TriggerConsole}, "", ""), "from the console"},
		{"unknown", rec(app.Deployment{Trigger: "schedule"}, "", ""), "by schedule"},
	}
	for _, tc := range tests {
		if got := deployWhoWords(tc.rec); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
