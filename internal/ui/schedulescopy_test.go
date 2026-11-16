package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// copyRunSentinel is what a command in a running copy would print. Nothing of
// it is kept, so no page may hold it.
const copyRunSentinel = "s3cret-sentinel-040-output"

// A copy schedule through the form: "Run in" and "In every copy" are on the
// page, a new copy is the default, and Stop after is one select with every
// choice.
func TestScheduleFormTarget(t *testing.T) {
	e := schedEnv(t)
	body := e.pageOf(t, "/schedules/new?app=whoami")
	for _, want := range []string{
		`<input type="radio" name="target" value="fresh" checked>`, `<input type="radio" name="target" value="copy">`,
		`<input type="checkbox" name="allCopies" value="1">`,
		`<option value="60">1 min</option>`, `<option value="120">2 min</option>`, `<option value="300">5 min</option>`,
		`<option value="0" selected>Usual (30 min; 1 min in the running copy)</option>`, `<option value="1800">30 min</option>`, `<option value="7200">2 h</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %s", want)
		}
	}
	for _, want := range []string{"Run in", "A new copy", "The running copy", "In every copy",
		"Memory is for a new copy only: the running copy has its own.", "Its output isn't kept."} {
		if !strings.Contains(text(body), want) {
			t.Errorf("the form lacks %q", want)
		}
	}
}

func TestScheduleFormCopyCreateAndEdit(t *testing.T) {
	e := schedEnv(t)
	e.seed(t, kumaApp(), seedDeploy{ID: "k1", Status: store.StatusHealthy, Version: 1})

	// The running copy of a service with a volume, in every copy, for 2 minutes. The memory and the
	// skip box are not sent for it: the running copy has its own memory and always skips.
	resp, body := e.postForm(t, "/schedules", scheduleForm(url.Values{"app": {"kuma"}, "name": {"purge"}, "command": {"sh -c 'rm -f /app/data/tmp/*'"},
		"target": {"copy"}, "allCopies": {"1"}, "stopAfter": {"120"}, "memory": {"512"}, "skip": nil}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create = %d:\n%s", resp.StatusCode, text(body))
	}
	list, err := e.Svc.ListSchedules(context.Background(), service.ScheduleFilter{App: "kuma"})
	if err != nil {
		t.Fatal(err)
	}
	var sc service.Schedule
	for _, s := range list {
		if s.Name == "purge" {
			sc = s
		}
	}
	if sc.Target != service.TargetCopy || !sc.AllCopies || sc.StopAfterSeconds != 120 || sc.MemoryMB != 0 || !sc.SkipIfRunning {
		t.Fatalf("created %+v", sc)
	}

	// "Usual" is each target's own default: 1 minute for the running copy, 30 for a new one.
	if resp, body := e.postForm(t, "/schedules", scheduleForm(url.Values{"app": {"kuma"}, "name": {"usual-copy"}, "command": {"true"},
		"target": {"copy"}, "stopAfter": {"0"}})); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create with the usual limit = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, body := e.postForm(t, "/schedules", scheduleForm(url.Values{"name": {"usual-fresh"}, "command": {"true"}, "stopAfter": {"0"}})); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create with the usual limit in a new copy = %d:\n%s", resp.StatusCode, text(body))
	}
	all, _ := e.Svc.ListSchedules(context.Background(), schedFilterAll)
	for _, s := range all {
		switch s.Name {
		case "usual-copy":
			if s.StopAfterSeconds != 60 {
				t.Errorf("usual in the running copy = %d s, want 60", s.StopAfterSeconds)
			}
		case "usual-fresh":
			if s.StopAfterSeconds != 1800 {
				t.Errorf("usual in a new copy = %d s, want 1800", s.StopAfterSeconds)
			}
		}
	}

	// The list and the detail say where it runs.
	page := e.pageOf(t, "/schedules")
	row := rowOf(page, sc.ID)
	for _, want := range []string{"in every running copy", "runs inevery running copy · uptime-kuma of kuma", "memorythe copy's own", "stops after2 min"} {
		if !strings.Contains(text(row), want) {
			t.Errorf("the row lacks %q:\n%s", want, text(row))
		}
	}

	// Edit shows the choices as saved, and moving it to a new copy is refused at the service.
	edit := e.pageOf(t, "/schedules/"+sc.ID+"/edit")
	for _, want := range []string{`<input type="radio" name="target" value="copy" checked>`, `<input type="checkbox" name="allCopies" value="1" checked>`,
		`<option value="120" selected>2 min</option>`} {
		if !strings.Contains(edit, want) {
			t.Errorf("the edit form lacks %s", want)
		}
	}
	resp, body = e.postForm(t, "/schedules/"+sc.ID, scheduleForm(url.Values{"app": {"kuma"}, "name": {"purge"}, "command": {"true"},
		"target": {"fresh"}, "stopAfter": {"300"}, "action": {"save"}}))
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "The service mounts a volume; a schedule needs a service without volumes.") {
		t.Errorf("a volume service moved to a new copy = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestScheduleFormTargetRefusals(t *testing.T) {
	e := schedEnv(t)
	for _, tt := range []struct {
		name string
		set  url.Values
		want string
		id   string // the element the error is drawn under
	}{
		{"every copy of a new copy", url.Values{"allCopies": {"1"}}, "Applies only to a schedule that runs in the running copy.", "schedule-target"},
		{"a new copy's limit for the running copy", url.Values{"target": {"copy"}, "stopAfter": {"1800"}}, "Must be 60, 120 or 300 for a schedule that runs in the running copy.", "schedule-stop"},
		{"the running copy's limit for a new copy", url.Values{"stopAfter": {"60"}}, "Must be 300, 1800, 3600 or 7200.", "schedule-stop"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.postForm(t, "/schedules", scheduleForm(tt.set))
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d:\n%s", resp.StatusCode, text(body))
			}
			if !strings.Contains(text(body), tt.want) || !strings.Contains(body, `id="`+tt.id+`-error"`) {
				t.Errorf("no %q under %s:\n%s", tt.want, tt.id, text(body))
			}
			// What was chosen is kept.
			if tt.set.Get("target") == "copy" && !strings.Contains(body, `<input type="radio" name="target" value="copy" checked>`) {
				t.Error("the choice of the running copy was lost")
			}
		})
	}
	if list, _ := e.Svc.ListSchedules(context.Background(), schedFilterAll); len(list) != 0 {
		t.Errorf("a refusal saved something: %+v", list)
	}
}

// The run page of a run in the running copy: the exit code, the reason and the copies, and a
// sentence that says its output isn't kept. A skipped slot says why.
func TestCopyRunPage(t *testing.T) {
	e := schedEnv(t)
	sc, err := e.Svc.CreateSchedule(context.Background(), service.NewSchedule{App: "whoami", Name: "purge", Command: []string{"sh", "-c", "echo hi"},
		Cron: "0 3 * * *", Target: service.TargetCopy, AllCopies: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	started := time.Now().UTC().Add(-time.Hour)
	for _, r := range []store.ScheduleRunRecord{
		{ID: "r1", Status: store.RunStarting, Copies: []string{"c1aaaaaa-0000", "c2bbbbbb-1111"}},
		{ID: "r2", Status: store.RunSkipped, Error: "the app is stopped"},
	} {
		r.ScheduleID, r.AppID, r.Service, r.Trigger = sc.ID, "whoami", "web", service.RunTriggerTimer
		r.Command, r.Target, r.StartedAt = sc.Command, store.TargetCopy, started
		if r.Status == store.RunSkipped {
			r.FinishedAt = &started
		}
		if err := e.Store.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Store.SettleRun(ctx, "r1", store.RunSettlement{Status: store.RunFailed, ExitCode: new(3), Error: "exit 3 in copy c1aaaaaa",
		FinishedAt: started.Add(5 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	got := text(e.pageOf(t, "/schedules/"+sc.ID+"/runs/r1"))
	for _, want := range []string{"Exited with code 3 in copy c1aaaaaa.", "in copies c1aaaaaa, c2bbbbbb", "exit code 3",
		"This run was in the running copy: its output isn't kept."} {
		if !strings.Contains(got, want) {
			t.Errorf("the run page lacks %q:\n%s", want, got)
		}
	}
	if got := text(e.pageOf(t, "/schedules/"+sc.ID+"/runs/r2")); !strings.Contains(got, "Skipped: the app is stopped.") ||
		!strings.Contains(got, "No output: the command didn't run.") {
		t.Errorf("a skipped slot:\n%s", got)
	}

	// The list says where it runs, and no page holds the command's output.
	if got := text(rowOf(e.pageOf(t, "/schedules"), sc.ID)); !strings.Contains(got, "in every running copy") {
		t.Errorf("the row doesn't say where it runs:\n%s", got)
	}
	for _, path := range []string{"/schedules", "/parts/schedules", "/schedules/" + sc.ID + "/runs/r1", "/apps/whoami/configure", "/apps", "/parts/attention"} {
		_, body := e.get(t, path)
		if strings.Contains(body, copyRunSentinel) {
			t.Errorf("%s holds the command's output", path)
		}
	}
}

// An action a schedule started says so in the app's Recent actions.
func TestRecentActionsSayOnASchedule(t *testing.T) {
	e := schedEnv(t)
	if got := triggerWords(app.TriggerSchedule); got != "on a schedule" {
		t.Errorf("triggerWords = %q", got)
	}
	ended := time.Now().UTC()
	if err := e.Store.InsertAction(context.Background(), store.ActionRecord{ID: "act_1", AppID: "whoami", Action: store.ActionRun,
		Trigger: string(app.TriggerSchedule), Status: store.ActionSucceeded, CopyID: "c1", Service: "web", Program: "sh", ExitCode: new(0),
		StartedAt: ended, FinishedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	_, body := e.get(t, "/parts/apps/whoami/actions")
	if got := text(body); !strings.Contains(got, "on a schedule") {
		t.Errorf("Recent actions lack \"on a schedule\":\n%s", got)
	}
}
