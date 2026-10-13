package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var attT0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func at(m int) time.Time { return attT0.Add(time.Duration(m) * time.Minute) }

// appRec is a stored app with the given services; a service named "db" has a
// backup policy.
func appRec(id string, updated time.Time, services ...string) store.AppRecord {
	a := app.App{ID: id}
	for _, name := range services {
		svc := app.Service{Name: name, Image: "example/" + name}
		if name == "db" {
			svc.Backup = &app.Backup{Engine: app.BackupPostgres}
		}
		a.Services = append(a.Services, svc)
	}
	return store.AppRecord{App: a, UpdatedAt: updated}
}

func dep(appID, id string, status store.Status, version *uint64, updated time.Time, errText string) store.DeploymentRecord {
	return store.DeploymentRecord{Deployment: app.Deployment{ID: id}, AppID: appID, Status: status,
		JobVersion: version, UpdatedAt: updated, Error: errText}
}

func ver(v uint64) *uint64 { return &v }

func tp(t time.Time) *time.Time { return &t }

// run is a running copy of a service at a version.
func run(id, service string, version uint64) Copy {
	return Copy{ID: id, Service: service, Version: version, Status: deploy.CopyHealthy, StartedAt: tp(at(0))}
}

func TestAttentionOf(t *testing.T) {
	errNomad := errors.New("nomad is down")
	healthy := func(appID string) store.DeploymentRecord {
		return dep(appID, appID+"-ok", store.StatusHealthy, ver(1), at(1), "")
	}

	tests := []struct {
		name     string
		apps     []store.AppRecord
		latest   map[string]store.DeploymentRecord
		copies   map[string][]Copy
		copyErr  error
		backups  []store.BackupRecord
		restores []store.RestoreRecord
		noDest   bool // no backup destination
		want     []AttentionItem
		unknown  bool
	}{
		{
			name: "nothing wrong is an empty list",
			apps: []store.AppRecord{appRec("blog", at(0), "web", "db")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies:  map[string][]Copy{"blog": {run("b1", "web", 1), run("b2", "db", 1)}},
			backups: []store.BackupRecord{{AppID: "blog", Service: "db", Status: store.BackupSucceeded, StartedAt: at(2)}},
		},
		{
			name:   "a failed deployment",
			apps:   []store.AppRecord{appRec("blog", at(0), "web")},
			latest: map[string]store.DeploymentRecord{"blog": dep("blog", "d7", store.StatusFailed, ver(7), at(5), "no image")},
			want:   []AttentionItem{{Kind: AttentionDeployFailed, AppID: "blog", Deployment: "d7", Version: ver(7), When: at(5), Detail: "no image"}},
		},
		{
			name:   "a reverted deployment that never got a job version",
			apps:   []store.AppRecord{appRec("blog", at(0), "web")},
			latest: map[string]store.DeploymentRecord{"blog": dep("blog", "d8", store.StatusReverted, nil, at(5), "unhealthy")},
			want:   []AttentionItem{{Kind: AttentionDeployFailed, AppID: "blog", Deployment: "d8", When: at(5), Detail: "unhealthy"}},
		},
		{
			name: "pending, running, healthy and superseded deployments are not failures",
			apps: []store.AppRecord{appRec("a", at(0), "web"), appRec("b", at(0), "web"), appRec("c", at(0), "web"), appRec("d", at(0), "web")},
			latest: map[string]store.DeploymentRecord{
				"a": dep("a", "a1", store.StatusPending, nil, at(1), ""),
				"b": dep("b", "b1", store.StatusRunning, ver(1), at(1), ""),
				"c": healthy("c"),
				"d": dep("d", "d1", store.StatusSuperseded, ver(1), at(1), ""),
			},
		},
		{
			name: "a later healthy deployment resolves the failure",
			apps: []store.AppRecord{appRec("blog", at(0), "web")},
			// LatestDeployments returns only the newest, so a healthy one hides the failed one before it.
			latest: map[string]store.DeploymentRecord{"blog": dep("blog", "d9", store.StatusHealthy, ver(9), at(9), "")},
		},
		{
			name: "a copy killed for memory",
			apps: []store.AppRecord{appRec("blog", at(0), "web")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies: map[string][]Copy{"blog": {{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyHealthy, OutOfMemory: true,
				LastRestart: tp(at(4)), StartedAt: tp(at(0)), Description: "Tasks are running"}}},
			want: []AttentionItem{{Kind: AttentionOutOfMemory, AppID: "blog", Service: "web", Copies: []string{"b1"}, When: at(4), Detail: "Tasks are running"}},
		},
		{
			name: "restarting from three restarts, not from two",
			apps: []store.AppRecord{appRec("blog", at(0), "web", "worker")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies: map[string][]Copy{"blog": {
				{ID: "w1", Service: "worker", Version: 1, Status: deploy.CopyHealthy, Restarts: 2, LastRestart: tp(at(6))},
				{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(5))},
			}},
			want: []AttentionItem{{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"b1"}, When: at(5)}},
		},
		{
			name: "a copy that restarts in a loop is restarting even while it is only starting",
			apps: []store.AppRecord{appRec("blog", at(0), "web")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies: map[string][]Copy{"blog": {{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyStarting, Restarts: 9, LastRestart: tp(at(5))}}},
			want:   []AttentionItem{{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"b1"}, When: at(5)}},
		},
		{
			name: "an out of memory copy that also restarts is one item, out of memory",
			apps: []store.AppRecord{appRec("blog", at(0), "web")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies: map[string][]Copy{"blog": {{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyUnhealthy, Restarts: 5,
				OutOfMemory: true, LastRestart: tp(at(5))}}},
			want: []AttentionItem{{Kind: AttentionOutOfMemory, AppID: "blog", Service: "web", Copies: []string{"b1"}, When: at(5)}},
		},
		{
			name: "an unhealthy copy, dated by its start",
			apps: []store.AppRecord{appRec("blog", at(0), "web")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies: map[string][]Copy{"blog": {{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyUnhealthy,
				StartedAt: tp(at(3)), LastRestart: tp(at(8)), Description: "Tasks are running"}}},
			want: []AttentionItem{{Kind: AttentionUnhealthy, AppID: "blog", Service: "web", Copies: []string{"b1"}, When: at(3), Detail: "Tasks are running"}},
		},
		{
			name:   "an unhealthy copy during a rollout is the rollout's business",
			apps:   []store.AppRecord{appRec("blog", at(0), "web")},
			latest: map[string]store.DeploymentRecord{"blog": dep("blog", "d2", store.StatusRunning, ver(2), at(1), "")},
			copies: map[string][]Copy{"blog": {{ID: "b1", Service: "web", Version: 2, Status: deploy.CopyUnhealthy, StartedAt: tp(at(3))}}},
		},
		{
			name: "stopped and failed copies, and a starting one that hasn't restarted, give no copy item",
			apps: []store.AppRecord{appRec("blog", at(0), "web")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies: map[string][]Copy{"blog": {
				{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyStarting, Restarts: 2},
				{ID: "b2", Service: "web", Version: 1, Status: deploy.CopyFailed, Terminal: true, Restarts: 9, OutOfMemory: true},
				{ID: "b3", Service: "web", Version: 1, Status: deploy.CopyStopped, Terminal: true},
			}},
		},
		{
			name: "copies of one service and kind are one item, the newest time, every copy listed",
			apps: []store.AppRecord{appRec("blog", at(0), "web")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies: map[string][]Copy{"blog": {
				{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyHealthy, Restarts: 4, LastRestart: tp(at(5)), Description: "older"},
				{ID: "b2", Service: "web", Version: 1, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(9)), Description: "newer"},
			}},
			want: []AttentionItem{{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"b1", "b2"}, When: at(9), Detail: "newer"}},
		},
		{
			name: "two services, or two kinds, are separate items",
			apps: []store.AppRecord{appRec("blog", at(0), "web", "worker")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			copies: map[string][]Copy{"blog": {
				{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(5))},
				{ID: "b2", Service: "web", Version: 1, Status: deploy.CopyHealthy, OutOfMemory: true, LastRestart: tp(at(6))},
				{ID: "w1", Service: "worker", Version: 1, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(7))},
			}},
			want: []AttentionItem{
				{Kind: AttentionRestarting, AppID: "blog", Service: "worker", Copies: []string{"w1"}, When: at(7)},
				{Kind: AttentionOutOfMemory, AppID: "blog", Service: "web", Copies: []string{"b2"}, When: at(6)},
				{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"b1"}, When: at(5)},
			},
		},
		{
			name:   "a failed deployment hides the copy items of its own job version only",
			apps:   []store.AppRecord{appRec("blog", at(0), "web")},
			latest: map[string]store.DeploymentRecord{"blog": dep("blog", "d7", store.StatusReverted, ver(7), at(10), "unhealthy")},
			copies: map[string][]Copy{"blog": {
				{ID: "new", Service: "web", Version: 7, Status: deploy.CopyUnhealthy, Restarts: 6, StartedAt: tp(at(8)), OutOfMemory: true},
				{ID: "old", Service: "web", Version: 6, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(2))},
			}},
			want: []AttentionItem{
				{Kind: AttentionDeployFailed, AppID: "blog", Deployment: "d7", Version: ver(7), When: at(10), Detail: "unhealthy"},
				{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"old"}, When: at(2)},
			},
		},
		{
			name:   "a failed deployment without a job version hides no copies",
			apps:   []store.AppRecord{appRec("blog", at(0), "web")},
			latest: map[string]store.DeploymentRecord{"blog": dep("blog", "d7", store.StatusFailed, nil, at(10), "no image")},
			copies: map[string][]Copy{"blog": {{ID: "b1", Service: "web", Version: 6, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(2))}}},
			want: []AttentionItem{
				{Kind: AttentionDeployFailed, AppID: "blog", Deployment: "d7", When: at(10), Detail: "no image"},
				{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"b1"}, When: at(2)},
			},
		},
		{
			name:    "Nomad unreadable: no copy items, the rest stays, and the list says so",
			apps:    []store.AppRecord{appRec("blog", at(0), "web")},
			latest:  map[string]store.DeploymentRecord{"blog": dep("blog", "d7", store.StatusFailed, ver(7), at(10), "x")},
			copies:  map[string][]Copy{"blog": {{ID: "b1", Service: "web", Version: 6, Status: deploy.CopyHealthy, Restarts: 9}}},
			copyErr: errNomad, unknown: true,
			want: []AttentionItem{{Kind: AttentionDeployFailed, AppID: "blog", Deployment: "d7", Version: ver(7), When: at(10), Detail: "x"}},
		},
		{
			name: "a failed backup and a failed restore, dated by their finish",
			apps: []store.AppRecord{appRec("blog", at(0), "web", "db")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			backups:  []store.BackupRecord{{AppID: "blog", Service: "db", Status: store.BackupFailed, Message: "pg_dump: boom", StartedAt: at(1), FinishedAt: tp(at(2))}},
			restores: []store.RestoreRecord{{AppID: "blog", Service: "db", Status: store.BackupFailed, Message: "pg_restore: boom", CreatedAt: at(3), FinishedAt: tp(at(4))}},
			want: []AttentionItem{
				{Kind: AttentionRestoreFailed, AppID: "blog", Service: "db", When: at(4), Detail: "pg_restore: boom"},
				{Kind: AttentionBackupFailed, AppID: "blog", Service: "db", When: at(2), Detail: "pg_dump: boom"},
			},
		},
		{
			name: "a backup that never finished is dated by its start",
			apps: []store.AppRecord{appRec("blog", at(0), "db")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			backups: []store.BackupRecord{{AppID: "blog", Service: "db", Status: store.BackupFailed, StartedAt: at(7)}},
			want:    []AttentionItem{{Kind: AttentionBackupFailed, AppID: "blog", Service: "db", When: at(7)}},
		},
		{
			name: "running and succeeded backups and restores are fine (a later good one resolves a failure)",
			apps: []store.AppRecord{appRec("blog", at(0), "db")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			backups:  []store.BackupRecord{{AppID: "blog", Service: "db", Status: store.BackupRunning, StartedAt: at(5)}},
			restores: []store.RestoreRecord{{AppID: "blog", Service: "db", Status: store.BackupSucceeded, CreatedAt: at(5), FinishedAt: tp(at(6))}},
		},
		{
			name: "a failed backup of a service that no longer has a policy, or of an app that is gone, is dropped",
			apps: []store.AppRecord{appRec("blog", at(0), "web")}, latest: map[string]store.DeploymentRecord{"blog": healthy("blog")},
			backups: []store.BackupRecord{
				{AppID: "blog", Service: "db", Status: store.BackupFailed, StartedAt: at(1)},
				{AppID: "blog", Service: "web", Status: store.BackupFailed, StartedAt: at(1)},
				{AppID: "gone", Service: "db", Status: store.BackupFailed, StartedAt: at(1)},
			},
		},
		{
			name:   "no destination is one item for the installation, dated by the newest app that wants backups",
			apps:   []store.AppRecord{appRec("a", at(3), "db"), appRec("b", at(9), "db"), appRec("c", at(20), "web")},
			latest: map[string]store.DeploymentRecord{"a": healthy("a"), "b": healthy("b"), "c": healthy("c")},
			noDest: true,
			want:   []AttentionItem{{Kind: AttentionNoBackupDestination, When: at(9)}},
		},
		{
			name:   "a destination, or no service that wants backups, is no item",
			apps:   []store.AppRecord{appRec("a", at(3), "db"), appRec("c", at(20), "web")},
			latest: map[string]store.DeploymentRecord{"a": healthy("a"), "c": healthy("c")},
		},
		{
			name: "no destination and no backups wanted is fine",
			apps: []store.AppRecord{appRec("c", at(20), "web")}, latest: map[string]store.DeploymentRecord{"c": healthy("c")},
			noDest: true,
		},
		{
			name: "newest first, ties by app then service",
			apps: []store.AppRecord{appRec("b", at(0), "web"), appRec("a", at(0), "web", "worker")},
			latest: map[string]store.DeploymentRecord{
				"b": dep("b", "bd", store.StatusFailed, nil, at(5), "x"),
				"a": dep("a", "ad", store.StatusFailed, nil, at(5), "y"),
			},
			copies: map[string][]Copy{
				"a": {
					{ID: "a1", Service: "worker", Version: 1, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(5))},
					{ID: "a2", Service: "web", Version: 1, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(5))},
				},
				"b": {{ID: "b1", Service: "web", Version: 1, Status: deploy.CopyHealthy, Restarts: 3, LastRestart: tp(at(6))}},
			},
			want: []AttentionItem{
				{Kind: AttentionRestarting, AppID: "b", Service: "web", Copies: []string{"b1"}, When: at(6)},
				{Kind: AttentionDeployFailed, AppID: "a", Deployment: "ad", When: at(5), Detail: "y"},
				{Kind: AttentionRestarting, AppID: "a", Service: "web", Copies: []string{"a2"}, When: at(5)},
				{Kind: AttentionRestarting, AppID: "a", Service: "worker", Copies: []string{"a1"}, When: at(5)},
				{Kind: AttentionDeployFailed, AppID: "b", Deployment: "bd", When: at(5), Detail: "x"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := attentionOf(tc.apps, tc.latest, tc.copies, tc.copyErr, tc.backups, tc.restores, !tc.noDest)
			if got.CopiesUnknown != tc.unknown {
				t.Errorf("CopiesUnknown = %v, want %v", got.CopiesUnknown, tc.unknown)
			}
			if len(got.Items) != len(tc.want) {
				t.Fatalf("items = %+v, want %+v", got.Items, tc.want)
			}
			for i, w := range tc.want {
				g := got.Items[i]
				if g.Kind != w.Kind || g.AppID != w.AppID || g.Service != w.Service || g.Deployment != w.Deployment ||
					!g.When.Equal(w.When) || g.Detail != w.Detail || !slices.Equal(g.Copies, w.Copies) ||
					(g.Version == nil) != (w.Version == nil) || (g.Version != nil && *g.Version != *w.Version) {
					t.Errorf("item %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

// An item goes when its condition does: the same inputs with the problem
// fixed give an empty list.
func TestAttentionOfResolution(t *testing.T) {
	apps := []store.AppRecord{appRec("blog", at(0), "web", "db")}
	broken := attentionOf(apps,
		map[string]store.DeploymentRecord{"blog": dep("blog", "d1", store.StatusFailed, nil, at(5), "x")},
		nil, nil,
		[]store.BackupRecord{{AppID: "blog", Service: "db", Status: store.BackupFailed, StartedAt: at(1)}}, nil, true)
	if len(broken.Items) != 2 {
		t.Fatalf("broken = %+v, want the failed deploy and the failed backup", broken.Items)
	}
	fixed := attentionOf(apps,
		map[string]store.DeploymentRecord{"blog": dep("blog", "d2", store.StatusHealthy, ver(2), at(8), "")},
		nil, nil,
		[]store.BackupRecord{{AppID: "blog", Service: "db", Status: store.BackupSucceeded, StartedAt: at(9)}}, nil, true)
	if len(fixed.Items) != 0 {
		t.Errorf("fixed = %+v, want none", fixed.Items)
	}
}

// The service reads the store and Nomad and hands them to attentionOf.
func TestAttention(t *testing.T) {
	ctx := context.Background()

	t.Run("a reverted deploy and a restarting copy, then a healthy deploy clears the first", func(t *testing.T) {
		e := newEnv(t)
		first, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, first.Deployment.ID)
		if got, err := e.svc.Attention(ctx); err != nil || len(got.Items) != 0 || got.CopiesUnknown {
			t.Fatalf("Attention after a healthy deploy = %+v, %v; want nothing", got, err)
		}

		desc := "Failed due to unhealthy allocations - rolling back to job version 0"
		e.nomad.Script("whoami", 1, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: desc})
		second, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, second.Deployment.ID)
		e.nomad.SetCopies("whoami", api.AllocationListStub{ID: "w1", TaskGroup: "web", JobVersion: 0, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {Restarts: 4, LastRestart: attT0}}})

		got, err := e.svc.Attention(ctx)
		if err != nil || len(got.Items) != 2 {
			t.Fatalf("Attention = %+v, %v; want two items", got, err)
		}
		kinds := map[AttentionKind]AttentionItem{}
		for _, it := range got.Items {
			kinds[it.Kind] = it
		}
		if f := kinds[AttentionDeployFailed]; f.Deployment != second.Deployment.ID || f.Detail != desc || f.AppID != "whoami" {
			t.Errorf("deploy_failed = %+v", f)
		}
		if r := kinds[AttentionRestarting]; r.Service != "web" || !slices.Equal(r.Copies, []string{"w1"}) {
			t.Errorf("restarting = %+v", r)
		}

		e.nomad.SetCopies("whoami")
		third, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, third.Deployment.ID)
		if got, _ := e.svc.Attention(ctx); len(got.Items) != 0 {
			t.Errorf("Attention after a healthy deploy = %+v, want none", got.Items)
		}
	})

	t.Run("Nomad down: the list says its copies are unknown, no error", func(t *testing.T) {
		e := newEnv(t)
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		e.nomad.Down()
		got, err := e.svc.Attention(ctx)
		if err != nil || !got.CopiesUnknown || len(got.Items) != 0 {
			t.Errorf("Attention = %+v, %v; want copiesUnknown and no items", got, err)
		}
	})

	t.Run("backups without a destination, until one is set", func(t *testing.T) {
		e := newEnv(t)
		// Saved without deploying: Deploy refuses a backup with no destination.
		if _, err := e.store.SaveApp(ctx, db()); err != nil {
			t.Fatal(err)
		}
		got, err := e.svc.Attention(ctx)
		if err != nil || len(got.Items) != 1 || got.Items[0].Kind != AttentionNoBackupDestination {
			t.Fatalf("Attention = %+v, %v; want no_backup_destination", got, err)
		}
		setDestination(t, e)
		if got, _ := e.svc.Attention(ctx); len(got.Items) != 0 {
			t.Errorf("Attention with a destination = %+v, want none", got.Items)
		}
	})

	t.Run("a failed backup run is an item", func(t *testing.T) {
		e := newEnv(t)
		setDestination(t, e)
		deployHealthy(t, e, db())
		if err := e.store.SaveBackup(ctx, store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web",
			Status: store.BackupFailed, Message: "pg_dump: boom", StartedAt: at(1), FinishedAt: tp(at(2))}); err != nil {
			t.Fatal(err)
		}
		got, err := e.svc.Attention(ctx)
		if err != nil || len(got.Items) != 1 || got.Items[0].Kind != AttentionBackupFailed || got.Items[0].Detail != "pg_dump: boom" {
			t.Errorf("Attention = %+v, %v", got, err)
		}
	})
}

// The needs-you list's notice goes out whenever something it is built from
// changes: deployments, copies, backups and a deleted app.
func TestAttentionNotices(t *testing.T) {
	ctx := context.Background()
	has := func(got []Change) bool { return slices.Contains(got, Change{What: ChangeAttention}) }

	t.Run("a deployment", func(t *testing.T) {
		e := newEnv(t)
		ch, unsub := e.svc.Subscribe("")
		defer unsub()
		e.svc.deploymentChanged("blog")
		if !has(drain(ch)) {
			t.Error("deploymentChanged sent no attention notice")
		}
	})
	t.Run("copies", func(t *testing.T) {
		e := newEnv(t)
		ch, unsub := e.svc.Subscribe("")
		defer unsub()
		e.svc.copiesChanged([]string{"blog"})
		if !has(drain(ch)) {
			t.Error("copiesChanged sent no attention notice")
		}
	})
	t.Run("backups", func(t *testing.T) {
		e := newEnv(t)
		ch, unsub := e.svc.Subscribe("")
		defer unsub()
		e.svc.backupsChanged("blog")
		if !has(drain(ch)) {
			t.Error("backupsChanged sent no attention notice")
		}
	})
	t.Run("a backup destination", func(t *testing.T) {
		e := newEnv(t)
		ch, unsub := e.svc.Subscribe("")
		defer unsub()
		setDestination(t, e)
		if !has(drain(ch)) {
			t.Error("SetBackupDestination sent no attention notice")
		}
	})
	t.Run("a deleted app", func(t *testing.T) {
		e := newEnv(t)
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		ch, unsub := e.svc.Subscribe("")
		defer unsub()
		drain(ch)
		if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
			t.Fatal(err)
		}
		if !has(drain(ch)) {
			t.Error("DeleteApp sent no attention notice")
		}
	})
}
