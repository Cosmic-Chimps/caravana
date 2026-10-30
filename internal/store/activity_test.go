package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

var actT0 = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

func at(min int) time.Time { return actT0.Add(time.Duration(min) * time.Minute) }

// activityFixture makes one row of every source, at known minutes after actT0:
//
//	 0 deployment created      1 deployment healthy (the "ended" entry)
//	 2 restart action          3 run action (with a sentinel in its arguments' place)
//	 4 backup finished         5 restore finished
//	 6 schedule run failed     (an ok run at 7 is not in the feed)
//	 8 problem opened          9 host boot        10 version change
//	11 stop action             12 problem restarting resolved
func activityFixture(t *testing.T) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	s := openTestStore(t)
	clock := at(0)
	s.now = func() time.Time { return clock }

	a := apptest.Whoami().App
	if _, err := s.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeployment(ctx, a.ID, newDeployment("dep1"), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	clock = at(1)
	if _, err := s.UpdateDeployment(ctx, "dep1", DeploymentUpdate{Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateDeployment(ctx, "dep1", DeploymentUpdate{Status: StatusHealthy}); err != nil {
		t.Fatal(err)
	}

	three := 3
	for _, act := range []ActionRecord{
		{ID: "act_restart", AppID: a.ID, Action: ActionRestart, Trigger: "console", Status: ActionSucceeded, StartedAt: at(2)},
		{ID: "act_run", AppID: a.ID, Action: ActionRun, Trigger: "console", Status: ActionFailed, CopyID: "c1", Service: "web",
			Program: "cat", ExitCode: &three, Error: "failed to start", StartedAt: at(3)},
		{ID: "act_stop", AppID: a.ID, Action: ActionStop, Trigger: "manual", Status: ActionSucceeded, StartedAt: at(11)},
	} {
		if err := s.InsertAction(ctx, act); err != nil {
			t.Fatal(err)
		}
	}

	fin := at(4)
	if err := s.SaveBackup(ctx, BackupRecord{AppID: a.ID, ID: "db-1", Service: "db", JobID: "j", Status: "succeeded",
		StartedAt: at(3).Add(30 * time.Second), FinishedAt: &fin}); err != nil {
		t.Fatal(err)
	}
	fin = at(5)
	if err := s.SaveRestore(ctx, RestoreRecord{ID: "rst1", AppID: a.ID, Service: "db", BackupID: "db-1", Status: "failed",
		Message: "pg_restore failed", CreatedAt: at(4).Add(30 * time.Second), FinishedAt: &fin}); err != nil {
		t.Fatal(err)
	}

	sch, err := s.InsertSchedule(ctx, testSchedule(a.ID, "aaaaaaaaaaaa", "nightly"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		id, status string
		finished   int
	}{{"run000000001", RunFailed, 6}, {"run000000002", RunOK, 7}, {"run000000003", RunSkipped, 7}} {
		rec := ScheduleRunRecord{ID: r.id, ScheduleID: sch.ID, AppID: a.ID, Service: "web", Trigger: "timer",
			Command: []string{"sh", "-c", "echo s3cret-sentinel"}, Status: RunRunning, StartedAt: at(r.finished).Add(-time.Minute)}
		if r.status == RunSkipped {
			rec.Status = RunSkipped
			rec.FinishedAt = new(at(r.finished))
		}
		if err := s.InsertRun(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if r.status != RunSkipped {
			st := RunSettlement{Status: r.status, FinishedAt: at(r.finished)}
			if r.status == RunFailed {
				st.ExitCode, st.Error = &three, "exited with 3"
			}
			if err := s.SettleRun(ctx, r.id, st); err != nil {
				t.Fatal(err)
			}
		}
	}

	clock = at(8)
	if err := s.ApplyProblemCheck(ctx, ProblemCheck{SeenAt: at(8), Opened: []SentProblem{
		{Key: "restarting|whoami|web|", Kind: "restarting", AppID: a.ID, Service: "web", OpenedAt: at(8), LastSeenAt: at(8), Summary: "whoami keeps restarting"},
		{Key: "backup_failed|whoami|db|", Kind: "backup_failed", AppID: a.ID, Service: "db", OpenedAt: at(8), LastSeenAt: at(8), Summary: "whoami's backup failed"},
	}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AppendHostEvent(ctx, HostEvent{Kind: HostEventBoot, At: at(9), Detail: `{}`, Key: "boot|1"}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.AppendHostEvent(ctx, HostEvent{Kind: HostEventVersion, At: at(10), Detail: `{"from":"0.1.0","to":"0.2.0"}`, Key: "version|0.2.0"}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.ApplyProblemCheck(ctx, ProblemCheck{SeenAt: at(12), Removed: []string{"restarting|whoami|web|"}}); err != nil {
		t.Fatal(err)
	}
	return s, a.ID
}

func entryOf(r ActivityRow) string {
	return fmt.Sprintf("%s/%s", r.Source, r.Phase)
}

func TestListActivity(t *testing.T) {
	ctx := context.Background()
	s, appID := activityFixture(t)

	t.Run("every source, newest first", func(t *testing.T) {
		got, err := s.ListActivity(ctx, 50, time.Time{}, false)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			"problem_events/",     // 12 resolved
			"app_actions/",        // 11 stop
			"host_events/",        // 10 version
			"host_events/",        // 9 boot
			"problem_events/",     // 8 opened (two rows at the same time)
			"problem_events/",     // 8
			"schedule_runs/",      // 6 failed
			"restores/",           // 5
			"backups/",            // 4
			"app_actions/",        // 3 run
			"app_actions/",        // 2 restart
			"deployments/ended",   // 1
			"deployments/started", // 0
		}
		var names []string
		for _, r := range got {
			names = append(names, entryOf(r))
		}
		if strings.Join(names, " ") != strings.Join(want, " ") {
			t.Fatalf("feed =\n%s\nwant\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
		}
		for i := 1; i < len(got); i++ {
			if got[i].At.After(got[i-1].At) {
				t.Errorf("row %d (%v) is newer than row %d (%v)", i, got[i].At, i-1, got[i-1].At)
			}
		}
	})

	t.Run("rows carry the app, its place and what the words need", func(t *testing.T) {
		got, _ := s.ListActivity(ctx, 50, time.Time{}, false)
		byKey := map[string]ActivityRow{}
		for _, r := range got {
			byKey[r.Source+"/"+r.Phase+"/"+r.RowID] = r
		}
		dep := byKey["deployments/ended/dep1"]
		if dep.AppID != appID || dep.Place.Project != "default" || dep.Place.Environment != "production" || dep.Place.Name != appID ||
			dep.Status != "healthy" || !dep.At.Equal(at(1)) || dep.Trigger == "" {
			t.Errorf("deployment ended = %+v", dep)
		}
		if st := byKey["deployments/started/dep1"]; st.Status != "healthy" || !st.At.Equal(at(0)) {
			t.Errorf("deployment started = %+v, want the deployment's time of creation", st)
		}
		run := byKey["app_actions//act_run"]
		if run.Label != ActionRun || run.Program != "cat" || run.ExitCode == nil || *run.ExitCode != 3 || run.Service != "web" || run.Error != "failed to start" {
			t.Errorf("run action = %+v", run)
		}
		if b := byKey["backups//"+appID+"/db-1"]; b.Status != "succeeded" || b.Service != "db" || b.Label != "postgres" || !b.At.Equal(at(4)) {
			t.Errorf("backup = %+v", b)
		}
		if r := byKey["restores//rst1"]; r.Status != "failed" || r.Service != "db" || !r.At.Equal(at(5)) {
			t.Errorf("restore = %+v", r)
		}
		sr := byKey["schedule_runs//run000000001"]
		if sr.Status != RunFailed || sr.Label != "nightly" || sr.ExitCode == nil || *sr.ExitCode != 3 || sr.Error != "exited with 3" {
			t.Errorf("schedule run = %+v", sr)
		}
		boot := byKey["host_events//1"]
		if boot.AppID != "" || boot.Place != (app.Place{}) || boot.Label != HostEventBoot || boot.Detail != `{}` {
			t.Errorf("boot = %+v, want no app", boot)
		}
		var resolved ActivityRow
		for _, r := range got {
			if r.Source == ActivityProblem && r.Status == ProblemResolved {
				resolved = r
			}
		}
		if resolved.Label != "restarting" || resolved.Detail != "whoami keeps restarting" || resolved.AppID != appID || resolved.Place.Name != appID {
			t.Errorf("problem resolved = %+v", resolved)
		}
	})

	t.Run("an ok or skipped schedule run is not in the feed", func(t *testing.T) {
		got, _ := s.ListActivity(ctx, 50, time.Time{}, false)
		for _, r := range got {
			if r.Source == ActivitySchedule && r.RowID != "run000000001" {
				t.Errorf("schedule run %s (%s) is in the feed", r.RowID, r.Status)
			}
		}
	})

	t.Run("a schedule run stopped at its limit is", func(t *testing.T) {
		sch, _ := s.ListSchedules(ctx, appID)
		rec := ScheduleRunRecord{ID: "run000000009", ScheduleID: sch[0].ID, AppID: appID, Service: "web", Trigger: "timer",
			Command: []string{"x"}, Status: RunRunning, StartedAt: at(13)}
		if err := s.InsertRun(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if err := s.SettleRun(ctx, rec.ID, RunSettlement{Status: RunStoppedAtLimit, FinishedAt: at(14)}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.ListActivity(ctx, 1, time.Time{}, false)
		if len(got) != 1 || got[0].RowID != rec.ID || got[0].Status != RunStoppedAtLimit {
			t.Errorf("newest = %+v, want the stopped run", got)
		}
	})

	t.Run("before pages without a repeat or a gap", func(t *testing.T) {
		all, _ := s.ListActivity(ctx, 100, time.Time{}, false)
		var paged []ActivityRow
		var before time.Time
		for range 30 {
			page, err := s.ListActivity(ctx, 4, before, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			paged = append(paged, page...)
			before = page[len(page)-1].At
		}
		// Rows at the same instant (the two problems opened together) may be cut by a
		// page boundary; the fixture's pages of 4 land the pair inside one page.
		if len(paged) != len(all) {
			t.Fatalf("paging read %d rows, one read %d", len(paged), len(all))
		}
		for i := range all {
			if all[i].Source != paged[i].Source || all[i].RowID != paged[i].RowID || all[i].Phase != paged[i].Phase {
				t.Errorf("row %d: paged %s %s, whole %s %s", i, paged[i].Source, paged[i].RowID, all[i].Source, all[i].RowID)
			}
		}
	})

	t.Run("limit must be positive", func(t *testing.T) {
		if _, err := s.ListActivity(ctx, 0, time.Time{}, false); err == nil {
			t.Error("limit 0 was accepted")
		}
	})
}

func TestListActivityServerOnly(t *testing.T) {
	ctx := context.Background()
	s, _ := activityFixture(t)
	if err := s.ApplyProblemCheck(ctx, ProblemCheck{SeenAt: at(15), Opened: []SentProblem{
		{Key: "disk_nearly_full|||/", Kind: "disk_nearly_full", OpenedAt: at(15), LastSeenAt: at(15), Summary: "disk / is 86% full"},
		{Key: "unhealthy|whoami|web|", Kind: "unhealthy", AppID: "whoami", OpenedAt: at(15), LastSeenAt: at(15), Summary: "unhealthy"},
	}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListActivity(ctx, 50, time.Time{}, true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		switch r.Source {
		case ActivityProblem:
			names = append(names, "problem:"+r.Label+":"+r.Status)
		case ActivityAction:
			names = append(names, "action:"+r.Label)
		default:
			names = append(names, r.Source+":"+r.Label)
		}
	}
	want := []string{
		"problem:disk_nearly_full:opened",
		"problem:restarting:resolved",
		"host_events:version",
		"host_events:boot",
		"problem:restarting:opened",
		"action:restart",
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("server feed =\n%s\nwant\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
}

// An app action's row holds its program and exit code, never arguments or
// output: the table has no column for them, and the feed reads none.
func TestListActivityNeverHoldsCommandText(t *testing.T) {
	ctx := context.Background()
	s, _ := activityFixture(t)
	got, err := s.ListActivity(ctx, 100, time.Time{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if strings.Contains(fmt.Sprintf("%+v", r), "s3cret-sentinel") {
			t.Errorf("row %s %s holds command text: %+v", r.Source, r.RowID, r)
		}
	}
}
