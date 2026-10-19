package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// attentionItemBody is an AttentionItem, decoded.
type attentionItemBody struct {
	Kind         string   `json:"kind"`
	AppID        *string  `json:"appId"`
	Service      *string  `json:"service"`
	Copies       []string `json:"copies"`
	DeploymentID *string  `json:"deploymentId"`
	DeployStatus *string  `json:"deploymentStatus"`
	JobVersion   *uint64  `json:"jobVersion"`
	At           string   `json:"at"`
	Detail       *string  `json:"detail"`
}

type attentionBody struct {
	Items         []attentionItemBody `json:"items"`
	CopiesUnknown bool                `json:"copiesUnknown"`
}

func getAttention(t *testing.T, e *testEnv) attentionBody {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/attention", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/attention = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Attention")
	return decodeAs[attentionBody](t, raw)
}

// seedDeployment records a deployment of appID that ended in status at version.
func seedDeployment(t *testing.T, e *testEnv, appID, id string, status store.Status, version uint64, errText string) {
	t.Helper()
	ctx := context.Background()
	d := app.Deployment{ID: id, Trigger: app.TriggerManual, Digests: map[string]string{}}
	if _, err := e.Store.CreateDeployment(ctx, appID, d, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: &version}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: status, Error: errText}); err != nil {
		t.Fatal(err)
	}
}

func seedApp(t *testing.T, e *testEnv, id string, services ...app.Service) {
	t.Helper()
	if _, err := e.Store.SaveApp(context.Background(), app.App{ID: id, Source: app.Source{Kind: app.SourceImage}, Services: services}, nil); err != nil {
		t.Fatal(err)
	}
}

// US5: the home's needs-you list, as the API tells it.
func TestListAttention(t *testing.T) {
	ctx := context.Background()

	t.Run("nothing needs the owner: an empty list, not null", func(t *testing.T) {
		e := newTestEnv(t, "")
		seedHealthy(t, e, "whoami", 1, 1)
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/attention", "", "")
		if status != http.StatusOK {
			t.Fatalf("status = %d %s", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Attention")
		if got := decodeAs[map[string]any](t, raw); got["items"] == nil || len(got["items"].([]any)) != 0 || got["copiesUnknown"] != false {
			t.Errorf("body = %s, want items [] and copiesUnknown false", raw)
		}
	})

	t.Run("every kind, with its facts, newest first", func(t *testing.T) {
		e := newTestEnv(t, "")
		web := app.Service{Name: "web", Image: "example/web"}
		worker := app.Service{Name: "worker", Image: "example/worker"}
		db := app.Service{Name: "db", Image: "example/postgres", Backup: &app.Backup{Engine: app.BackupPostgres}}
		seedApp(t, e, "blog", web, worker, db)
		seedDeployment(t, e, "blog", "blog-d1", store.StatusHealthy, 4, "")
		seedApp(t, e, "shop", web)
		seedDeployment(t, e, "shop", "shop-d3", store.StatusReverted, 3, "the new copies failed their health check")

		restart := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
		// Restarts count as a loop only within the last hour, so the restarting
		// copies restarted minutes ago (on the real clock the service reads).
		recent := time.Now().UTC().Truncate(time.Second).Add(-5 * time.Minute)
		no := false
		e.Nomad.SetCopies("blog",
			api.AllocationListStub{ID: "w1", TaskGroup: "web", JobVersion: 4, ClientStatus: "running", ClientDescription: "Tasks are running",
				TaskStates: map[string]*api.TaskState{"web": {Restarts: 1, LastRestart: restart, Events: []*api.TaskEvent{
					{Type: api.TaskTerminated, Details: map[string]string{"oom_killed": "true"}}}}}},
			api.AllocationListStub{ID: "k1", TaskGroup: "worker", JobVersion: 4, ClientStatus: "running",
				TaskStates: map[string]*api.TaskState{"worker": {Restarts: 3, LastRestart: recent}}},
			api.AllocationListStub{ID: "k2", TaskGroup: "worker", JobVersion: 4, ClientStatus: "running",
				TaskStates: map[string]*api.TaskState{"worker": {Restarts: 5, LastRestart: recent.Add(time.Minute)}}},
			api.AllocationListStub{ID: "d1", TaskGroup: "db", JobVersion: 4, ClientStatus: "running", ClientDescription: "Tasks are running",
				DeploymentStatus: &api.AllocDeploymentStatus{Healthy: &no},
				TaskStates:       map[string]*api.TaskState{"db": {StartedAt: restart.Add(-time.Hour)}}})
		e.Nomad.SetCopies("shop", api.AllocationListStub{ID: "s1", TaskGroup: "web", JobVersion: 3, ClientStatus: "running",
			TaskStates: map[string]*api.TaskState{"web": {Restarts: 9, LastRestart: restart}}}) // the failed version's copy: no item of its own

		done := time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
		if err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "blog", ID: "db-1", Service: "db", Status: store.BackupFailed,
			Message: "pg_dump: boom", StartedAt: done.Add(-time.Minute), FinishedAt: &done}); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.SaveRestore(ctx, store.RestoreRecord{ID: "r1", AppID: "blog", Service: "db", BackupID: "db-1",
			Status: store.BackupFailed, Message: "pg_restore: boom", CreatedAt: done.Add(-time.Hour), FinishedAt: &done}); err != nil {
			t.Fatal(err)
		}

		got := getAttention(t, e)
		if got.CopiesUnknown {
			t.Error("copiesUnknown = true, want false")
		}
		by := map[string]attentionItemBody{}
		for _, it := range got.Items {
			if _, dup := by[it.Kind]; dup {
				t.Errorf("two items of kind %s", it.Kind)
			}
			by[it.Kind] = it
		}
		if len(got.Items) != 7 {
			t.Fatalf("items = %+v, want one of each of the 7 kinds", got.Items)
		}

		str := func(s *string) string {
			if s == nil {
				return "<absent>"
			}
			return *s
		}
		if it := by["deploy_failed"]; str(it.AppID) != "shop" || str(it.DeploymentID) != "shop-d3" || str(it.DeployStatus) != "reverted" || it.JobVersion == nil || *it.JobVersion != 3 ||
			str(it.Detail) != "the new copies failed their health check" || it.Service != nil || it.Copies != nil {
			t.Errorf("deploy_failed = %+v", it)
		}
		if by["out_of_memory"].DeployStatus != nil {
			t.Errorf("an out_of_memory item carries a deploymentStatus: %+v", by["out_of_memory"])
		}
		if it := by["out_of_memory"]; str(it.AppID) != "blog" || str(it.Service) != "web" || !slices.Equal(it.Copies, []string{"w1"}) ||
			it.At != "2026-10-12T08:00:00Z" || str(it.Detail) != "Tasks are running" {
			t.Errorf("out_of_memory = %+v", it)
		}
		if it := by["restarting"]; str(it.Service) != "worker" || !slices.Equal(it.Copies, []string{"k1", "k2"}) || it.At != recent.Add(time.Minute).Format(time.RFC3339) {
			t.Errorf("restarting = %+v, want the two worker copies as one item at the newer restart", it)
		}
		if it := by["unhealthy"]; str(it.Service) != "db" || !slices.Equal(it.Copies, []string{"d1"}) || it.At != "2026-10-12T07:00:00Z" {
			t.Errorf("unhealthy = %+v", it)
		}
		if it := by["backup_failed"]; str(it.AppID) != "blog" || str(it.Service) != "db" || str(it.Detail) != "pg_dump: boom" || it.At != "2026-10-12T02:00:00Z" {
			t.Errorf("backup_failed = %+v", it)
		}
		if it := by["restore_failed"]; str(it.Service) != "db" || str(it.Detail) != "pg_restore: boom" {
			t.Errorf("restore_failed = %+v", it)
		}
		if it := by["no_backup_destination"]; it.AppID != nil || it.Service != nil || it.At == "" {
			t.Errorf("no_backup_destination = %+v, want no app and no service", it)
		}
		// RFC 3339 times in one zone sort as text.
		ats := make([]string, len(got.Items))
		for i, it := range got.Items {
			ats[i] = it.At
		}
		if !slices.IsSortedFunc(ats, func(a, b string) int { return -strings.Compare(a, b) }) {
			t.Errorf("items aren't newest first: %v", ats)
		}
	})

	t.Run("Nomad down is 200 with copiesUnknown, and the stored kinds stay", func(t *testing.T) {
		e := newTestEnv(t, "")
		seedApp(t, e, "shop", app.Service{Name: "web", Image: "example/web"})
		seedDeployment(t, e, "shop", "shop-d3", store.StatusFailed, 3, "no image")
		e.Nomad.Down()
		got := getAttention(t, e)
		if !got.CopiesUnknown || len(got.Items) != 1 || got.Items[0].Kind != "deploy_failed" ||
			got.Items[0].DeployStatus == nil || *got.Items[0].DeployStatus != "failed" {
			t.Errorf("attention = %+v, want copiesUnknown and the deploy_failed item", got)
		}
	})

	t.Run("a token is needed", func(t *testing.T) {
		e := newTestEnv(t, "")
		status, _, raw := call(t, http.MethodGet, e.RawURL+"/v1/attention", "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("without a token: %d %s, want 401", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Error")
	})
}
