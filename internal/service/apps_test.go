package service

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// appWith returns the whoami app with the given ID and replicas per service.
func appWith(id string, replicas ...int) app.App {
	a := whoami()
	a.ID = id
	a.Services = nil
	for i, n := range replicas {
		svc := whoami().Services[0]
		svc.Name = []string{"web", "worker", "cache"}[i]
		svc.Replicas = &n
		if i > 0 {
			svc.Ports, svc.Domains, svc.Health = nil, nil, nil
		}
		a.Services = append(a.Services, svc)
	}
	return a
}

func copyOfVersion(service string, version uint64, status deploy.CopyStatus) deploy.Copy {
	return deploy.Copy{ID: service + "-x", Service: service, Version: version, Status: status,
		Terminal: status == deploy.CopyStopped || status == deploy.CopyFailed}
}

func latest(status store.Status) *store.DeploymentRecord {
	return &store.DeploymentRecord{Status: status, JobVersion: new(uint64(7))}
}

func TestWantedCopies(t *testing.T) {
	two := 2
	tests := []struct {
		name string
		app  app.App
		want int
	}{
		{"nil replicas is one", app.App{Services: []app.Service{{Name: "web"}}}, 1},
		{"set replicas", app.App{Services: []app.Service{{Name: "web", Replicas: &two}}}, 2},
		{"summed over services", app.App{Services: []app.Service{{Name: "web", Replicas: &two}, {Name: "worker"}}}, 3},
		{"no services", app.App{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantedCopies(tc.app); got != tc.want {
				t.Errorf("wantedCopies = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestStatusOf has one row per case of research R7.
func TestStatusOf(t *testing.T) {
	down := errors.New("nomad is unreachable")
	healthy := deploy.CopyHealthy
	tests := []struct {
		name       string
		app        app.App
		latest     *store.DeploymentRecord
		copies     []deploy.Copy
		nomadErr   error
		wantState  AppState
		wantWanted int
		wantHealth int
		wantLive   *uint64
	}{
		{"rolling out: pending", appWith("a", 2), latest(store.StatusPending), nil,
			nil, StateRollingOut, 2, 0, nil},
		{"rolling out: running, old copies serve", appWith("a", 2), latest(store.StatusRunning),
			[]deploy.Copy{copyOfVersion("web", 6, healthy), copyOfVersion("web", 6, healthy)},
			nil, StateRollingOut, 2, 2, new(uint64(6))},
		{"rolling out beats unknown", appWith("a", 1), latest(store.StatusRunning), nil,
			down, StateRollingOut, 1, 0, nil},
		{"healthy with a healthy deployment", appWith("a", 2), latest(store.StatusHealthy),
			[]deploy.Copy{copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, healthy)},
			nil, StateHealthy, 2, 2, new(uint64(7))},
		{"a copy Nomad is stopping isn't healthy (right after a Start)", appWith("a", 1), latest(store.StatusHealthy),
			[]deploy.Copy{{Service: "web", Version: 9, Status: healthy, Stopping: true}, copyOfVersion("web", 10, deploy.CopyStarting)},
			nil, StateNotRunning, 1, 0, nil},
		{"healthy without a deployment status", appWith("a", 1), nil,
			[]deploy.Copy{copyOfVersion("web", 3, healthy)},
			nil, StateHealthy, 1, 1, new(uint64(3))},
		{"healthy after a revert", appWith("a", 1), latest(store.StatusReverted),
			[]deploy.Copy{copyOfVersion("web", 8, healthy), copyOfVersion("web", 7, deploy.CopyFailed)},
			nil, StateHealthy, 1, 1, new(uint64(8))},
		{"degraded: one of two", appWith("a", 2), latest(store.StatusHealthy),
			[]deploy.Copy{copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, deploy.CopyUnhealthy)},
			nil, StateDegraded, 2, 1, new(uint64(7))},
		{"degraded: starting copies don't count", appWith("a", 3), latest(store.StatusHealthy),
			[]deploy.Copy{copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, deploy.CopyStarting), copyOfVersion("web", 7, deploy.CopyFailed)},
			nil, StateDegraded, 3, 1, new(uint64(7))},
		{"not running: no copies", appWith("a", 2), latest(store.StatusHealthy), nil,
			nil, StateNotRunning, 2, 0, nil},
		{"not running: never deployed", appWith("a", 1), nil, nil,
			nil, StateNotRunning, 1, 0, nil},
		{"not running: only failed and stopped copies", appWith("a", 1), latest(store.StatusFailed),
			[]deploy.Copy{copyOfVersion("web", 7, deploy.CopyFailed), copyOfVersion("web", 7, deploy.CopyStopped)},
			nil, StateNotRunning, 1, 0, nil},
		{"unknown when Nomad can't be read", appWith("a", 2), latest(store.StatusHealthy), nil,
			down, StateUnknown, 2, 0, nil},
		{"unknown even without a deployment", appWith("a", 1), nil, nil,
			down, StateUnknown, 1, 0, nil},
		{"multi-service: all healthy", appWith("a", 2, 1), latest(store.StatusHealthy),
			[]deploy.Copy{copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, healthy), copyOfVersion("worker", 7, healthy)},
			nil, StateHealthy, 3, 3, new(uint64(7))},
		{"multi-service: the worker is down", appWith("a", 2, 1), latest(store.StatusHealthy),
			[]deploy.Copy{copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, healthy), copyOfVersion("worker", 7, deploy.CopyFailed)},
			nil, StateDegraded, 3, 2, new(uint64(7))},
		{"multi-service: a surplus in one service doesn't hide a missing copy in another", appWith("a", 2, 1), latest(store.StatusHealthy),
			[]deploy.Copy{copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, healthy)},
			nil, StateDegraded, 3, 3, new(uint64(7))},
		{"live version is the newest version with healthy copies", appWith("a", 2), latest(store.StatusHealthy),
			[]deploy.Copy{copyOfVersion("web", 6, healthy), copyOfVersion("web", 7, healthy)},
			nil, StateDegraded, 2, 1, new(uint64(7))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := statusOf(tc.app, tc.latest, tc.copies, tc.nomadErr)
			if got.State != tc.wantState || got.WantedCopies != tc.wantWanted || got.HealthyCopies != tc.wantHealth {
				t.Errorf("status = %s wanted %d healthy %d; want %s wanted %d healthy %d",
					got.State, got.WantedCopies, got.HealthyCopies, tc.wantState, tc.wantWanted, tc.wantHealth)
			}
			switch {
			case tc.wantLive == nil && got.LiveVersion != nil:
				t.Errorf("LiveVersion = %d, want nil", *got.LiveVersion)
			case tc.wantLive != nil && (got.LiveVersion == nil || *got.LiveVersion != *tc.wantLive):
				t.Errorf("LiveVersion = %v, want %d", got.LiveVersion, *tc.wantLive)
			}
			if (got.NomadError != "") != (tc.nomadErr != nil) {
				t.Errorf("NomadError = %q with Nomad error %v", got.NomadError, tc.nomadErr)
			}
		})
	}
}

// TestStatusOfServices checks the per-service counts: in the app's service
// order, healthy copies of the live version only, and a count of 0 for a
// service with no healthy copy or when no version is live.
func TestStatusOfServices(t *testing.T) {
	healthy := deploy.CopyHealthy
	tests := []struct {
		name     string
		app      app.App
		copies   []deploy.Copy
		nomadErr error
		want     []ServiceStatus
	}{
		{"in the app's order", appWith("a", 2, 1),
			[]deploy.Copy{copyOfVersion("worker", 7, healthy), copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, healthy)}, nil,
			[]ServiceStatus{{"web", 2, 2}, {"worker", 1, 1}}},
		{"a short service", appWith("a", 2, 1),
			[]deploy.Copy{copyOfVersion("web", 7, healthy), copyOfVersion("web", 7, deploy.CopyUnhealthy), copyOfVersion("worker", 7, healthy)}, nil,
			[]ServiceStatus{{"web", 2, 1}, {"worker", 1, 1}}},
		{"only the live version counts", appWith("a", 2),
			[]deploy.Copy{copyOfVersion("web", 6, healthy), copyOfVersion("web", 7, healthy)}, nil,
			[]ServiceStatus{{"web", 2, 1}}},
		{"a copy being stopped doesn't count", appWith("a", 1, 1),
			[]deploy.Copy{{Service: "web", Version: 7, Status: healthy, Stopping: true}, copyOfVersion("worker", 7, healthy)}, nil,
			[]ServiceStatus{{"web", 1, 0}, {"worker", 1, 1}}},
		{"nothing live", appWith("a", 1, 1), nil, nil,
			[]ServiceStatus{{"web", 1, 0}, {"worker", 1, 0}}},
		{"Nomad unreadable", appWith("a", 1), []deploy.Copy{copyOfVersion("web", 7, healthy)}, errors.New("down"),
			[]ServiceStatus{{"web", 1, 0}}},
		{"no services", app.App{}, nil, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := statusOf(tc.app, latest(store.StatusHealthy), tc.copies, tc.nomadErr).Services
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Services = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// ReportedCopies counts the copies Nomad reports that haven't ended, so a page can say "0 of 1
// copies healthy" for a copy that is starting or failing its checks, and "no copies" for none.
func TestStatusOfReportedCopies(t *testing.T) {
	copies := []deploy.Copy{
		copyOfVersion("web", 7, deploy.CopyStarting), copyOfVersion("web", 7, deploy.CopyUnhealthy),
		copyOfVersion("web", 6, deploy.CopyHealthy), copyOfVersion("web", 7, deploy.CopyFailed),
		copyOfVersion("web", 5, deploy.CopyStopped), {Service: "web", Version: 6, Status: deploy.CopyHealthy, Stopping: true},
	}
	if got := statusOf(appWith("a", 3), latest(store.StatusHealthy), copies, nil).ReportedCopies; got != 3 {
		t.Errorf("ReportedCopies = %d, want 3 (starting, unhealthy, healthy; not failed, stopped or stopping)", got)
	}
	if got := statusOf(appWith("a", 1), latest(store.StatusHealthy), nil, nil).ReportedCopies; got != 0 {
		t.Errorf("ReportedCopies without copies = %d, want 0", got)
	}
	if got := statusOf(appWith("a", 1), latest(store.StatusHealthy), copies, errors.New("down")).ReportedCopies; got != 0 {
		t.Errorf("ReportedCopies with Nomad unreadable = %d, want 0", got)
	}
}

// loggedEnv is newEnv with a service that logs into a buffer.
func loggedEnv(t *testing.T) (*env, *lockedBuffer) {
	t.Helper()
	e := newEnv(t)
	buf := &lockedBuffer{}
	svc := New(e.store, newDeployer(t, e.registry, e.nomad), e.secrets, slog.New(slog.NewTextHandler(buf, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { cancel(); svc.Wait() })
	e.svc = svc
	return e, buf
}

// finalDeployment records a deployment of appID that ended with status.
func finalDeployment(t *testing.T, st *store.Store, appID, id string, status store.Status, version uint64) {
	t.Helper()
	ctx := context.Background()
	d := app.Deployment{ID: id, Trigger: app.TriggerManual, Digests: map[string]string{}}
	if _, err := st.CreateDeployment(ctx, appID, d, nil, nil, ""); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if _, err := st.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: &version}); err != nil {
		t.Fatalf("running: %v", err)
	}
	if status != store.StatusRunning {
		if _, err := st.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: status}); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
}

func runningStub(group string, version uint64) api.AllocationListStub {
	return api.AllocationListStub{TaskGroup: group, JobVersion: version, ClientStatus: "running", NodeName: "node-1"}
}

func TestListApps(t *testing.T) {
	ctx := context.Background()

	t.Run("empty", func(t *testing.T) {
		e := newEnv(t)
		got, err := e.svc.ListApps(ctx)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("ListApps = %#v, %v; want an empty, non-nil slice", got, err)
		}
	})

	t.Run("by ID with status and last deployment, one Nomad request", func(t *testing.T) {
		e := newEnv(t)
		for _, a := range []app.App{appWith("whoami", 2), appWith("blog", 1), appWith("shop", 1)} {
			if _, err := e.store.SaveApp(ctx, a, nil); err != nil {
				t.Fatalf("SaveApp: %v", err)
			}
		}
		finalDeployment(t, e.store, "whoami", "w1", store.StatusHealthy, 4)
		finalDeployment(t, e.store, "blog", "b1", store.StatusHealthy, 2)
		finalDeployment(t, e.store, "blog", "b2", store.StatusRunning, 3)
		e.nomad.SetCopies("whoami", runningStub("web", 4), runningStub("web", 4))
		e.nomad.SetCopies("blog", runningStub("web", 2))

		before := e.nomad.AllocationRequests()
		got, err := e.svc.ListApps(ctx)
		if err != nil {
			t.Fatalf("ListApps: %v", err)
		}
		if n := e.nomad.AllocationRequests() - before; n != 1 {
			t.Errorf("ListApps made %d allocation requests, want 1 for all apps", n)
		}

		var ids []string
		for _, s := range got {
			ids = append(ids, s.App.ID)
		}
		if want := []string{"blog", "shop", "whoami"}; !slices.Equal(ids, want) {
			t.Fatalf("IDs = %v, want %v", ids, want)
		}
		blog, shop, who := got[0], got[1], got[2]
		if blog.Status.State != StateRollingOut || blog.LastDeployment == nil || blog.LastDeployment.Deployment.ID != "b2" {
			t.Errorf("blog = %s, last %+v; want rolling_out with b2", blog.Status.State, blog.LastDeployment)
		}
		if blog.Status.LiveVersion == nil || *blog.Status.LiveVersion != 2 {
			t.Errorf("blog live version = %v, want 2 (the old copies still serve)", blog.Status.LiveVersion)
		}
		if shop.Status.State != StateNotRunning || shop.LastDeployment != nil {
			t.Errorf("shop = %s, last %+v; want not_running and no deployment", shop.Status.State, shop.LastDeployment)
		}
		if who.Status.State != StateHealthy || who.Status.HealthyCopies != 2 || who.Status.WantedCopies != 2 ||
			who.Status.LiveVersion == nil || *who.Status.LiveVersion != 4 {
			t.Errorf("whoami status = %+v, want healthy 2 of 2 at v4", who.Status)
		}
		if who.CreatedAt.IsZero() || who.App.Services[0].Name != "web" {
			t.Errorf("whoami = %+v, want the stored record's fields", who.AppRecord)
		}
	})

	t.Run("Nomad down: unknown summaries, no error, one log line", func(t *testing.T) {
		e, logs := loggedEnv(t)
		for _, id := range []string{"whoami", "blog"} {
			if _, err := e.store.SaveApp(ctx, appWith(id, 1), nil); err != nil {
				t.Fatal(err)
			}
			finalDeployment(t, e.store, id, id+"-1", store.StatusHealthy, 1)
		}
		e.nomad.Down()

		got, err := e.svc.ListApps(ctx)
		if err != nil {
			t.Fatalf("ListApps with Nomad down = %v, want no error", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d summaries, want 2 (the stored data still shows)", len(got))
		}
		for _, s := range got {
			if s.Status.State != StateUnknown || s.Status.HealthyCopies != 0 || s.Status.NomadError == "" {
				t.Errorf("%s status = %+v, want unknown with the reason", s.App.ID, s.Status)
			}
			if s.LastDeployment == nil {
				t.Errorf("%s lost its last deployment", s.App.ID)
			}
		}
		if lines := strings.Split(strings.TrimSpace(logs.String()), "\n"); len(lines) != 1 || lines[0] == "" {
			t.Errorf("log has %d lines, want exactly 1 per call:\n%s", len(lines), logs.String())
		}
	})
}

func TestAppSummary(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.store.SaveApp(ctx, appWith("whoami", 1), nil); err != nil {
		t.Fatal(err)
	}
	finalDeployment(t, e.store, "whoami", "w1", store.StatusHealthy, 4)
	e.nomad.SetCopies("whoami", runningStub("web", 4))

	got, err := e.svc.AppSummary(ctx, "whoami")
	if err != nil {
		t.Fatalf("AppSummary: %v", err)
	}
	if got.Status.State != StateHealthy || got.LastDeployment == nil || got.LastDeployment.Deployment.ID != "w1" {
		t.Errorf("summary = %s, last %+v; want healthy with w1", got.Status.State, got.LastDeployment)
	}

	if _, err := e.svc.AppSummary(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("AppSummary(ghost) = %v, want store.ErrNotFound", err)
	}

	e.nomad.Down()
	got, err = e.svc.AppSummary(ctx, "whoami")
	if err != nil || got.Status.State != StateUnknown {
		t.Errorf("AppSummary with Nomad down = %s, %v; want unknown and no error", got.Status.State, err)
	}
}

func TestRecentDeployments(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for _, id := range []string{"whoami", "blog"} {
		if _, err := e.store.SaveApp(ctx, appWith(id, 1), nil); err != nil {
			t.Fatal(err)
		}
	}
	finalDeployment(t, e.store, "whoami", "d1", store.StatusHealthy, 1)
	time.Sleep(2 * time.Millisecond) // created_at has nanosecond text; keep the order unambiguous
	finalDeployment(t, e.store, "blog", "d2", store.StatusHealthy, 1)
	time.Sleep(2 * time.Millisecond)
	finalDeployment(t, e.store, "whoami", "d3", store.StatusFailed, 2)

	got, err := e.svc.RecentDeployments(ctx, 2)
	if err != nil {
		t.Fatalf("RecentDeployments: %v", err)
	}
	var ids []string
	for _, d := range got {
		ids = append(ids, d.Deployment.ID)
	}
	if want := []string{"d3", "d2"}; !slices.Equal(ids, want) {
		t.Errorf("RecentDeployments(2) = %v, want %v", ids, want)
	}
}

func TestProgress(t *testing.T) {
	ctx := context.Background()

	t.Run("set while the deployment runs", func(t *testing.T) {
		e := newEnv(t)
		e.nomad.Script("whoami", 0, deploytest.Rolling("Deployment is running", map[string]api.DeploymentState{
			"web": {DesiredTotal: 2, PlacedAllocs: 2, HealthyAllocs: 1},
		}))
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		if rec.Status != store.StatusRunning {
			t.Fatalf("status = %s, want running (the fake rollout never ends)", rec.Status)
		}
		p, err := e.svc.Progress(ctx, rec)
		if err != nil || p == nil {
			t.Fatalf("Progress = %v, %v; want the rollout's progress", p, err)
		}
		if *p != (Progress{Wanted: 2, Placed: 2, Healthy: 1, Description: "Deployment is running"}) {
			t.Errorf("Progress = %+v", *p)
		}
	})

	t.Run("nil for final deployments, even when Nomad still has the rollout", func(t *testing.T) {
		e := newEnv(t)
		e.nomad.Script("whoami", 0, deploytest.Rolling("done", map[string]api.DeploymentState{"web": {DesiredTotal: 1}}))
		if _, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		for _, status := range []store.Status{store.StatusHealthy, store.StatusFailed, store.StatusReverted, store.StatusSuperseded, store.StatusPending} {
			rec := store.DeploymentRecord{AppID: "whoami", Status: status, JobVersion: new(uint64(0))}
			if p, err := e.svc.Progress(ctx, rec); err != nil || p != nil {
				t.Errorf("Progress of a %s deployment = %v, %v; want nil", status, p, err)
			}
		}
	})

	t.Run("nil when Nomad has no rollout for the version yet", func(t *testing.T) {
		e := newEnv(t)
		rec := store.DeploymentRecord{AppID: "whoami", Status: store.StatusRunning, JobVersion: new(uint64(3))}
		if p, err := e.svc.Progress(ctx, rec); err != nil || p != nil {
			t.Errorf("Progress = %v, %v; want nil and no error", p, err)
		}
	})

	t.Run("nil for a running deployment without a job version", func(t *testing.T) {
		e := newEnv(t)
		rec := store.DeploymentRecord{AppID: "whoami", Status: store.StatusRunning}
		if p, err := e.svc.Progress(ctx, rec); err != nil || p != nil {
			t.Errorf("Progress = %v, %v; want nil and no error", p, err)
		}
	})

	t.Run("an error when Nomad can't be read", func(t *testing.T) {
		e := newEnv(t)
		e.nomad.Down()
		rec := store.DeploymentRecord{AppID: "whoami", Status: store.StatusRunning, JobVersion: new(uint64(0))}
		if p, err := e.svc.Progress(ctx, rec); err == nil || p != nil {
			t.Errorf("Progress = %v, %v; want an error", p, err)
		}
	})
}

func TestListDeploymentsBefore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for _, id := range []string{"whoami", "blog"} {
		if _, err := e.store.SaveApp(ctx, appWith(id, 1), nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []struct{ app, id string }{{"whoami", "d1"}, {"blog", "b1"}, {"whoami", "d2"}, {"whoami", "d3"}} {
		finalDeployment(t, e.store, d.app, d.id, store.StatusHealthy, 1)
		time.Sleep(2 * time.Millisecond) // created_at is the paging key; keep it distinct
	}
	all, err := e.svc.ListDeployments(ctx, "whoami", 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListDeployments = %d, %v; want 3", len(all), err)
	}

	ids := func(recs []store.DeploymentRecord) []string {
		var out []string
		for _, r := range recs {
			out = append(out, r.Deployment.ID)
		}
		return out
	}
	got, err := e.svc.ListDeploymentsBefore(ctx, "whoami", all[0].CreatedAt, 10)
	if err != nil || !slices.Equal(ids(got), []string{"d2", "d1"}) {
		t.Errorf("before d3 = %v, %v; want d2, d1 (this app only)", ids(got), err)
	}
	got, err = e.svc.ListDeploymentsBefore(ctx, "whoami", all[0].CreatedAt, 1)
	if err != nil || !slices.Equal(ids(got), []string{"d2"}) {
		t.Errorf("before d3, limit 1 = %v, %v; want d2", ids(got), err)
	}
	if _, err := e.svc.ListDeploymentsBefore(ctx, "ghost", time.Now(), 10); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app = %v, want store.ErrNotFound", err)
	}
}

func TestCreateApp(t *testing.T) {
	ctx := context.Background()

	t.Run("saves the app and deploys it with the given trigger", func(t *testing.T) {
		e := newEnv(t)
		rec, err := e.svc.CreateApp(ctx, whoami(), app.TriggerConsole)
		if err != nil {
			t.Fatalf("CreateApp() = %v", err)
		}
		if rec.Deployment.Trigger != app.TriggerConsole || rec.AppID != "whoami" || rec.JobVersion == nil {
			t.Errorf("record = %+v, want a console deployment of whoami with a job version", rec)
		}
		if stored, err := e.svc.GetApp(ctx, "whoami"); err != nil || !reflect.DeepEqual(stored.App, whoami()) {
			t.Errorf("stored app = %+v, %v; want the app as sent", stored.App, err)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Errorf("final = %s, want healthy", final.Status)
		}
	})

	t.Run("an existing ID is refused and nothing changes", func(t *testing.T) {
		e := newEnv(t)
		first, err := e.svc.CreateApp(ctx, whoami(), app.TriggerConsole)
		if err != nil {
			t.Fatalf("first CreateApp() = %v", err)
		}
		waitFinal(t, e.store, first.Deployment.ID)
		before, _ := e.svc.GetApp(ctx, "whoami")

		other := whoami()
		other.Services[0].Replicas = new(4)
		_, err = e.svc.CreateApp(ctx, other, app.TriggerConsole)
		if !errors.Is(err, ErrAppExists) {
			t.Fatalf("second CreateApp() = %v, want ErrAppExists", err)
		}
		after, _ := e.svc.GetApp(ctx, "whoami")
		if !reflect.DeepEqual(before, after) {
			t.Errorf("the stored app changed: %+v -> %+v", before, after)
		}
		if n := deployCount(e, "whoami"); n != 1 {
			t.Errorf("deployments = %d, want only the first", n)
		}
		if e.nomad.Registrations() != 1 {
			t.Errorf("registrations = %d, want 1", e.nomad.Registrations())
		}
	})

	t.Run("invalid input is a field error and nothing is saved", func(t *testing.T) {
		e := newEnv(t)
		bad := whoami()
		bad.Services[0].Name = "Web"
		var fieldErr *app.FieldError
		if _, err := e.svc.CreateApp(ctx, bad, app.TriggerConsole); !errors.As(err, &fieldErr) || fieldErr.Field != "services[0].name" {
			t.Fatalf("CreateApp() = %v, want a FieldError on services[0].name", err)
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want the app not saved", err)
		}
		if n := deployCount(e, "whoami"); n != 0 || e.nomad.Registrations() != 0 {
			t.Errorf("%d deployments, %d registrations; want none", n, e.nomad.Registrations())
		}
	})

	t.Run("a deploy that fails to start is a DeployError, and the app is kept", func(t *testing.T) {
		e := newEnv(t)
		a := whoami()
		a.Services[0].Image = "traefik/whoami:nope"
		var deployErr *DeployError
		_, err := e.svc.CreateApp(ctx, a, app.TriggerConsole)
		if !errors.As(err, &deployErr) || !errors.Is(err, deploy.ErrImageNotFound) {
			t.Fatalf("CreateApp() = %v, want a DeployError for a missing image", err)
		}
		if rec, err := e.store.GetDeployment(ctx, deployErr.DeploymentID); err != nil || rec.Status != store.StatusFailed {
			t.Errorf("deployment = %+v, %v; want a failed record", rec, err)
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); err != nil {
			t.Errorf("GetApp = %v, want the app kept", err)
		}
	})

	t.Run("two creates of one ID at once: exactly one wins", func(t *testing.T) {
		e := newEnv(t)
		const n = 8
		errs := make([]error, n)
		recs := make([]store.DeploymentRecord, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				recs[i], errs[i] = e.svc.CreateApp(ctx, whoami(), app.TriggerConsole)
			}()
		}
		wg.Wait()

		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
				waitFinal(t, e.store, recs[i].Deployment.ID)
			case !errors.Is(err, ErrAppExists):
				t.Errorf("CreateApp #%d = %v, want nil or ErrAppExists", i, err)
			}
		}
		if won != 1 {
			t.Errorf("%d creates won, want exactly 1", won)
		}
		if n := deployCount(e, "whoami"); n != 1 {
			t.Errorf("deployments = %d, want 1", n)
		}
	})

	t.Run("publishes app and apps notices", func(t *testing.T) {
		e := newEnv(t)
		ch, unsubscribe := e.svc.Subscribe("whoami")
		defer unsubscribe()
		all, unsubscribeAll := e.svc.Subscribe("")
		defer unsubscribeAll()
		rec, err := e.svc.CreateApp(ctx, whoami(), app.TriggerConsole)
		if err != nil {
			t.Fatalf("CreateApp() = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)

		seen := map[ChangeKind]bool{}
		timeout := time.After(5 * time.Second)
		for !(seen[ChangeApp] && seen[ChangeApps]) {
			select {
			case c := <-ch:
				seen[c.What] = true
			case c := <-all:
				seen[c.What] = true
			case <-timeout:
				t.Fatalf("notices seen: %v, want app and apps", seen)
			}
		}
	})
}

func TestCopies(t *testing.T) {
	ctx := context.Background()
	at := func(minutes int) *time.Time {
		t := time.Date(2026, 10, 4, 9, minutes, 0, 0, time.UTC)
		return &t
	}
	stub := func(id, group string, version uint64, status string, minutes int) api.AllocationListStub {
		a := api.AllocationListStub{ID: id, TaskGroup: group, JobVersion: version, ClientStatus: status, NodeName: "node-1"}
		if minutes > 0 {
			a.TaskStates = map[string]*api.TaskState{"main": {StartedAt: *at(minutes)}}
		}
		return a
	}

	t.Run("not-ended copies, plus the ended copies of the newest version, in order", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.store.SaveApp(ctx, appWith("blog", 1, 1), nil); err != nil {
			t.Fatalf("SaveApp: %v", err)
		}
		e.nomad.SetCopies("blog",
			stub("w-old-failed", "web", 6, "failed", 1),   // an ended copy of an older version: left out
			stub("w-old-running", "web", 6, "running", 2), // still running: listed, after the newer version
			stub("w-new-b", "web", 7, "running", 9),       // newer version, started later
			stub("w-new-a", "web", 7, "running", 5),       // newer version, started first
			stub("w-new-failed", "web", 7, "failed", 7),   // an ended copy of the newest version: listed
			stub("k-run", "worker", 7, "running", 3),      // another service: after web
			stub("w-new-pending", "web", 7, "pending", 0)) // not started: last of its version
		got, err := e.svc.Copies(ctx, "blog")
		if err != nil {
			t.Fatalf("Copies: %v", err)
		}
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		// Service name order first, then newest version first, then start time with unstarted last.
		want := []string{"w-new-a", "w-new-failed", "w-new-b", "w-new-pending", "w-old-running", "k-run"}
		if !slices.Equal(ids, want) {
			t.Errorf("copies = %v, want %v", ids, want)
		}
		if got[0].Status != deploy.CopyHealthy || got[1].Status != deploy.CopyFailed || !got[1].Terminal {
			t.Errorf("statuses = %s, %s (terminal %v), want healthy then failed", got[0].Status, got[1].Status, got[1].Terminal)
		}
	})

	t.Run("an app with no copies has an empty list, not nil", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.store.SaveApp(ctx, appWith("blog", 1), nil); err != nil {
			t.Fatalf("SaveApp: %v", err)
		}
		got, err := e.svc.Copies(ctx, "blog")
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("Copies = %#v, %v; want an empty, non-nil list", got, err)
		}
	})

	t.Run("another app's copies are not listed", func(t *testing.T) {
		e := newEnv(t)
		for _, id := range []string{"blog", "shop"} {
			if _, err := e.store.SaveApp(ctx, appWith(id, 1), nil); err != nil {
				t.Fatalf("SaveApp: %v", err)
			}
		}
		e.nomad.SetCopies("shop", stub("s1", "web", 1, "running", 1))
		if got, err := e.svc.Copies(ctx, "blog"); err != nil || len(got) != 0 {
			t.Errorf("Copies(blog) = %v, %v; want none", got, err)
		}
	})

	t.Run("an unknown app is store.ErrNotFound, without asking Nomad", func(t *testing.T) {
		e := newEnv(t)
		e.nomad.Down()
		if _, err := e.svc.Copies(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Copies = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("Nomad down is ErrNomadUnavailable", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.store.SaveApp(ctx, appWith("blog", 1), nil); err != nil {
			t.Fatalf("SaveApp: %v", err)
		}
		e.nomad.Down()
		if _, err := e.svc.Copies(ctx, "blog"); !errors.Is(err, deploy.ErrNomadUnavailable) {
			t.Errorf("Copies = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestDeleteApp(t *testing.T) {
	ctx := context.Background()

	// deployed is an env with whoami created and its rollout finished.
	deployed := func(t *testing.T) *env {
		t.Helper()
		e := newEnv(t)
		rec, err := e.svc.CreateApp(ctx, whoami(), app.TriggerConsole)
		if err != nil {
			t.Fatalf("CreateApp() = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		second, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("second deploy = %v", err)
		}
		waitFinal(t, e.store, second.Deployment.ID)
		return e
	}

	t.Run("purges the job in Nomad, then erases the app", func(t *testing.T) {
		e := deployed(t)
		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Fatalf("DeleteApp() = %v", err)
		}
		if !e.nomad.Purged("whoami") || e.nomad.Job("whoami") != nil {
			t.Error("the job is still in Nomad")
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want store.ErrNotFound", err)
		}
		if n := deployCount(e, "whoami"); n != 0 {
			t.Errorf("deployments left = %d, want 0", n)
		}
	})

	t.Run("a job Caravana didn't create is left alone, the app is still erased", func(t *testing.T) {
		e := newEnv(t)
		// Somebody else registered a job called whoami. Creating the app fails
		// on the conflict, but the app stays stored (R14), so it can be deleted.
		e.nomad.AddForeignJob(&api.Job{ID: new("whoami"), Name: new("whoami")})
		if _, err := e.svc.CreateApp(ctx, whoami(), app.TriggerConsole); !errors.Is(err, deploy.ErrJobConflict) {
			t.Fatalf("CreateApp() = %v, want deploy.ErrJobConflict", err)
		}
		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Fatalf("DeleteApp() = %v", err)
		}
		if e.nomad.Purged("whoami") || e.nomad.Job("whoami") == nil {
			t.Error("a job Caravana didn't create was purged")
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("an unknown app is store.ErrNotFound and Nomad is not asked", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.DeleteApp(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteApp(ghost) = %v, want store.ErrNotFound", err)
		}
		if e.nomad.Purged("ghost") {
			t.Error("Nomad was asked to purge an app Caravana doesn't have")
		}
	})

	t.Run("Nomad down: an error, and everything is still stored", func(t *testing.T) {
		e := deployed(t)
		token, err := e.svc.CreateWebhookToken(ctx, "whoami")
		if err != nil {
			t.Fatal(err)
		}
		e.nomad.Down()
		err = e.svc.DeleteApp(ctx, "whoami")
		if !errors.Is(err, deploy.ErrNomadUnavailable) {
			t.Fatalf("DeleteApp() = %v, want ErrNomadUnavailable", err)
		}
		e.nomad.Up()
		if _, err := e.svc.GetApp(ctx, "whoami"); err != nil {
			t.Errorf("GetApp = %v, want the app kept", err)
		}
		if n := deployCount(e, "whoami"); n != 2 {
			t.Errorf("deployments = %d, want both kept", n)
		}
		if err := e.svc.CheckWebhookToken(ctx, "whoami", token.Token); err != nil {
			t.Errorf("webhook token = %v, want it kept", err)
		}
		if e.nomad.Job("whoami") == nil {
			t.Error("the job is gone from Nomad")
		}

		// The same delete works once Nomad is back.
		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Errorf("DeleteApp after Nomad is back = %v", err)
		}
	})

	t.Run("a job already gone from Nomad doesn't stop the delete", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.store.SaveApp(ctx, whoami(), nil); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Errorf("DeleteApp() = %v", err)
		}
	})

	t.Run("the rollout watcher stops", func(t *testing.T) {
		e := newEnv(t)
		// A rollout that never ends: its watcher polls until it is told to stop.
		e.nomad.Default(deploytest.Rolling("Deployment is running", nil))
		rec, err := e.svc.CreateApp(ctx, whoami(), app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != store.StatusRunning {
			t.Fatalf("status = %s, want running", rec.Status)
		}

		stopped := make(chan struct{})
		go func() {
			e.svc.watchers.Wait()
			close(stopped)
		}()
		select {
		case <-stopped:
			t.Fatal("the watcher ended before the delete")
		case <-time.After(50 * time.Millisecond):
		}
		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Fatalf("DeleteApp() = %v", err)
		}
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatal("the rollout watcher still runs after the delete")
		}
	})

	t.Run("a later Redeploy is store.ErrNotFound", func(t *testing.T) {
		e := deployed(t)
		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Fatal(err)
		}
		before := e.nomad.Registrations()
		if _, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Redeploy = %v, want store.ErrNotFound", err)
		}
		if e.nomad.Registrations() != before || e.nomad.Job("whoami") != nil {
			t.Error("a redeploy of a deleted app reached Nomad")
		}
	})

	t.Run("a deploy waiting for the app's lock doesn't bring the job back", func(t *testing.T) {
		e := deployed(t)
		state := e.svc.stateOf("whoami")
		state.mu.Lock() // like a deploy in progress: everything else for this app waits

		// The delete queues first, so it gets the lock first; the redeploy has
		// already recorded its deployment by the time it waits.
		deleted, redeployed := make(chan error, 1), make(chan error, 1)
		go func() { deleted <- e.svc.DeleteApp(ctx, "whoami") }()
		time.Sleep(50 * time.Millisecond)
		go func() { _, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook); redeployed <- err }()
		waitFor(t, "the redeploy to queue", func() bool { return deployCount(e, "whoami") == 3 })
		time.Sleep(20 * time.Millisecond)
		state.mu.Unlock()

		for _, c := range []struct {
			name string
			ch   chan error
			want error
		}{{"DeleteApp", deleted, nil}, {"Redeploy", redeployed, store.ErrNotFound}} {
			select {
			case err := <-c.ch:
				if !errors.Is(err, c.want) {
					t.Errorf("%s = %v, want %v", c.name, err, c.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s timed out", c.name)
			}
		}
		if e.nomad.Job("whoami") != nil {
			t.Error("the job is back in Nomad after the delete")
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want the app gone", err)
		}
	})

	t.Run("publishes deleted, apps and activity", func(t *testing.T) {
		e := deployed(t)
		ch, unsubscribe := e.svc.Subscribe("whoami")
		defer unsubscribe()
		all, unsubscribeAll := e.svc.Subscribe("")
		defer unsubscribeAll()
		drainNow := func() { drain(ch); drain(all) }
		drainNow()
		time.Sleep(20 * time.Millisecond) // let the finished rollout's notices pass
		drainNow()

		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Fatal(err)
		}
		seen := map[ChangeKind]bool{}
		timeout := time.After(5 * time.Second)
		for !(seen[ChangeDeleted] && seen[ChangeApps] && seen[ChangeActivity]) {
			select {
			case c := <-ch:
				seen[c.What] = true
			case c := <-all:
				seen[c.What] = true
			case <-timeout:
				t.Fatalf("notices seen: %v, want deleted, apps and activity", seen)
			}
		}
	})

	t.Run("the ID can be created again", func(t *testing.T) {
		e := deployed(t)
		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Fatal(err)
		}
		rec, err := e.svc.CreateApp(ctx, whoami(), app.TriggerConsole)
		if err != nil {
			t.Fatalf("CreateApp after delete = %v", err)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Errorf("final = %s, want healthy", final.Status)
		}
		if e.nomad.Job("whoami") == nil {
			t.Error("the new app isn't in Nomad")
		}
	})
}
