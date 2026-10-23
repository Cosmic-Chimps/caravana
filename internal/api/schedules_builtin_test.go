package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// builtinEnv has two apps with a backup policy: pg's db service backs up
// with Postgres on the default time, kuma's kuma service its volume at 04:30.
func builtinEnv(t *testing.T) *testEnv {
	t.Helper()
	e := scheduleEnv(t)
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/pg/compose", "application/json", dbCompose); status != http.StatusAccepted {
		t.Fatalf("deploy pg = %d %s", status, raw)
	}
	kuma := composeBody(t, volumeCompose, map[string]any{"kuma": map[string]any{"backup": map[string]any{
		"engine": "volume", "schedule": "30 4 * * *", "keep": 2}}})
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/kuma/compose", "application/json", kuma); status != http.StatusAccepted {
		t.Fatalf("deploy kuma = %d %s", status, raw)
	}
	return e
}

// T017: a service's backup policy is a built-in schedule in listSchedules and
// getSchedule, with its runs from the backups table, read-only through every
// route.
func TestBuiltinSchedules(t *testing.T) {
	e := builtinEnv(t)
	doc := loadContract(t)
	ctx := context.Background()

	// Three backups of pg's db, newest last: ok, failed, running; one of kuma.
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	for i, b := range []store.BackupRecord{
		{AppID: "pg", ID: "pg-1", Service: "db", JobID: "pg_db_backup/periodic-1", Status: store.BackupSucceeded, Bytes: 10},
		{AppID: "pg", ID: "pg-2", Service: "db", JobID: "pg_db_backup/periodic-2", Status: store.BackupFailed,
			Message: "pg_dump: connection refused"},
		{AppID: "pg", ID: "pg-3", Service: "db", JobID: "pg_db_backup/periodic-3", Status: store.BackupRunning},
		{AppID: "kuma", ID: "kuma-1", Service: "kuma", Engine: "volume", Status: store.BackupSucceeded, Bytes: 20},
	} {
		b.StartedAt = base.Add(time.Duration(i) * time.Hour)
		if b.Status != store.BackupRunning {
			b.FinishedAt = new(b.StartedAt.Add(42 * time.Second))
		}
		if err := e.Store.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
	}

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules", "", "")
	if status != http.StatusOK {
		t.Fatalf("listSchedules = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "ScheduleList")
	byID := map[string]service.Schedule{}
	for _, sc := range decodeAs[struct{ Schedules []service.Schedule }](t, raw).Schedules {
		byID[sc.ID] = sc
	}
	if len(byID) != 2 {
		t.Fatalf("listSchedules has %d schedules, want the two backups: %s", len(byID), raw)
	}

	t.Run("the rows", func(t *testing.T) {
		tests := []struct {
			id, app, service, name, cron, words string
			lastStatus                          string
			running                             int
		}{
			// The newest run that isn't skipped is the running backup.
			{"backup:pg:db", "pg", "db", "db-backup", "0 3 * * *", "Every day at 03:00 UTC", "running", 1},
			{"backup:kuma:kuma", "kuma", "kuma", "kuma-backup", "30 4 * * *", "Every day at 04:30 UTC", "ok", 0},
		}
		for _, tc := range tests {
			sc, ok := byID[tc.id]
			if !ok {
				t.Errorf("no %s in the list", tc.id)
				continue
			}
			if !sc.Builtin || sc.App != tc.app || sc.Service != tc.service || sc.Name != tc.name || sc.Cron != tc.cron ||
				sc.Words != tc.words || sc.Paused || sc.RunningNow != tc.running || len(sc.Command) != 0 {
				t.Errorf("%s = %+v", tc.id, sc)
			}
			if sc.NextRunAt == nil || !sc.NextRunAt.After(time.Now()) || time.Until(*sc.NextRunAt) > 24*time.Hour {
				t.Errorf("%s next run %v, want within the day", tc.id, sc.NextRunAt)
			}
			if sc.LastRun == nil || sc.LastRun.Status != tc.lastStatus {
				t.Errorf("%s last run %+v, want %s", tc.id, sc.LastRun, tc.lastStatus)
			}
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+tc.id, "", "")
			if status != http.StatusOK || decodeAs[service.Schedule](t, raw).ID != tc.id {
				t.Errorf("getSchedule %s = %d %s", tc.id, status, raw)
			}
			checkSchema(t, doc, raw, "Schedule")
		}
		if got := byID["backup:kuma:kuma"].UsualSeconds; got == nil || *got != 42 {
			t.Errorf("kuma's usual seconds = %v, want 42", got)
		}
	})

	t.Run("the runs are the backups", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/backup:pg:db/runs", "", "")
		if status != http.StatusOK {
			t.Fatalf("listScheduleRuns = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "ScheduleRunList")
		runs := decodeAs[struct{ Runs []service.ScheduleRun }](t, raw).Runs
		want := []struct{ id, status, err string }{
			{"pg-3", "running", ""}, {"pg-2", "failed", "pg_dump: connection refused"}, {"pg-1", "ok", ""},
		}
		if len(runs) != len(want) {
			t.Fatalf("runs = %s, want %d", raw, len(want))
		}
		for i, w := range want {
			r := runs[i]
			if r.ID != w.id || r.Status != w.status || r.Error != w.err || r.ScheduleID != "backup:pg:db" || r.OutputKept {
				t.Errorf("run %d = %+v, want %s %s %q", i, r, w.id, w.status, w.err)
			}
		}
		if runs[0].DurationSeconds != nil || runs[2].DurationSeconds == nil || *runs[2].DurationSeconds != 42 {
			t.Errorf("durations %v and %v, want none while running and 42", runs[0].DurationSeconds, runs[2].DurationSeconds)
		}
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/schedules/backup:pg:db/runs?limit=1", "", "")
		if status != http.StatusOK || len(decodeAs[struct{ Runs []service.ScheduleRun }](t, raw).Runs) != 1 {
			t.Errorf("listScheduleRuns?limit=1 = %d %s", status, raw)
		}
		if r := getRun(t, e, "backup:pg:db", "pg-2"); r.Status != "failed" {
			t.Errorf("getScheduleRun pg-2 = %+v", r)
		}
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/schedules/backup:pg:db/runs/kuma-1", "", "")
		wantScheduleError(t, "another service's backup as a run", status, raw, 404, codeRunNotFound, "")
	})

	t.Run("no output is kept", func(t *testing.T) {
		for _, run := range []string{"pg-1", "pg-2"} {
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/backup:pg:db/runs/"+run+"/output", "", "")
			wantScheduleError(t, "output of backup "+run, status, raw, 404, codeOutputNotKept, "")
		}
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/backup:pg:db/runs/nope/output", "", "")
		wantScheduleError(t, "output of no backup", status, raw, 404, codeRunNotFound, "")
	})

	t.Run("every change is refused", func(t *testing.T) {
		update := `{"app": "pg", "service": "db", "name": "db-backup", "command": ["true"], "cron": "0 4 * * *"}`
		for _, id := range []string{"backup:pg:db", "backup:kuma:kuma"} {
			for _, c := range []struct{ method, path, body string }{
				{http.MethodPut, "", update},
				{http.MethodDelete, "", ""},
				{http.MethodPost, "/run", ""},
				{http.MethodPost, "/pause", ""},
				{http.MethodPost, "/resume", ""},
				{http.MethodPost, "/runs/pg-3/stop", ""},
			} {
				status, _, raw := call(t, c.method, e.URL+"/v1/schedules/"+id+c.path, "application/json", c.body)
				wantScheduleError(t, c.method+" "+id+c.path, status, raw, 409, codeBuiltinSchedule, "")
				checkSchema(t, doc, raw, "Error")
			}
		}
		// The rows are still there, unpaused.
		if sc := getScheduleOK(t, e, "backup:pg:db"); sc.Paused || sc.NextRunAt == nil {
			t.Errorf("after the refusals: %+v", sc)
		}
	})

	t.Run("a service without a policy has none", func(t *testing.T) {
		for _, id := range []string{"backup:pg:web", "backup:ghost:db", "backup:pg", "backup:pg:db:x"} {
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+id, "", "")
			wantScheduleError(t, "getSchedule "+id, status, raw, 404, codeScheduleNotFound, "")
			status, _, raw = call(t, http.MethodPost, e.URL+"/v1/schedules/"+id+"/pause", "", "")
			wantScheduleError(t, "pause "+id, status, raw, 404, codeScheduleNotFound, "")
		}
	})

	t.Run("filters", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules?app=kuma", "", "")
		if got := decodeAs[struct{ Schedules []service.Schedule }](t, raw).Schedules; status != http.StatusOK || len(got) != 1 ||
			got[0].ID != "backup:kuma:kuma" {
			t.Errorf("listSchedules?app=kuma = %d %s", status, raw)
		}
	})

	t.Run("a stopped app's backup has no next run", func(t *testing.T) {
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/kuma/stop", "", ""); status != http.StatusOK {
			t.Fatalf("stop kuma = %d %s", status, raw)
		}
		if sc := getScheduleOK(t, e, "backup:kuma:kuma"); sc.NextRunAt != nil {
			t.Errorf("a stopped app's backup runs next at %v, want none", sc.NextRunAt)
		}
	})
}

// getScheduleOK reads a schedule through the API.
func getScheduleOK(t *testing.T, e *testEnv, id string) service.Schedule {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/schedules/"+id, "", "")
	if status != http.StatusOK {
		t.Fatalf("getSchedule %s = %d %s", id, status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Schedule")
	return decodeAs[service.Schedule](t, raw)
}
