package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const activitySentinel = "s3cret-sentinel-027act"

// activityLab fills every source of the feed. Times are minutes before
// `base` (an hour ago): the deployment is recorded at the real time, so it is
// the newest; the node's events and the rest are older, in the order listed.
func activityLab(t *testing.T) (*samplerLab, time.Time) {
	t.Helper()
	ctx := context.Background()
	l := newSamplerLab(t, discardLogger())
	base := time.Now().UTC().Add(-time.Hour)
	ago := func(min int) time.Time { return base.Add(-time.Duration(min) * time.Minute) }

	l.nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", []*api.NodeEvent{
		{Subsystem: "Cluster", Message: "Node heartbeat missed", Timestamp: ago(1)},
		{Subsystem: "Cluster", Message: "Node registered", Timestamp: ago(2)},
	})
	l.tick(t, 1)

	a := apptest.Whoami().App
	if _, err := l.store.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	origin := &app.Origin{Commit: "abcdef1234", Ref: "refs/heads/main", Actor: "ana"}
	d := apptest.Whoami().Deployment
	d.ID, d.Origin = "dep1", origin
	if _, err := l.store.CreateDeployment(ctx, a.ID, d, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	v := uint64(24)
	for _, st := range []store.Status{store.StatusRunning, store.StatusHealthy} {
		if _, err := l.store.UpdateDeployment(ctx, "dep1", store.DeploymentUpdate{Status: st, JobVersion: &v}); err != nil {
			t.Fatal(err)
		}
	}

	three, zero := 3, 0
	for _, act := range []store.ActionRecord{
		{ID: "act_restart", AppID: a.ID, Action: store.ActionRestart, Trigger: "console", Status: store.ActionSucceeded, StartedAt: ago(3)},
		{ID: "act_stop", AppID: a.ID, Action: store.ActionStop, Trigger: "manual", Status: store.ActionSucceeded, StartedAt: ago(4)},
		{ID: "act_start", AppID: a.ID, Action: store.ActionStart, Trigger: "console", Status: store.ActionSucceeded, StartedAt: ago(5)},
		{ID: "act_run", AppID: a.ID, Action: store.ActionRun, Trigger: "console", Status: store.ActionFailed, CopyID: "c1",
			Service: "web", Program: "cat", ExitCode: &three, StartedAt: ago(6)},
		{ID: "act_ok", AppID: a.ID, Action: store.ActionRun, Trigger: "console", Status: store.ActionSucceeded, CopyID: "c1",
			Service: "web", Program: "ls", ExitCode: &zero, StartedAt: ago(7)},
	} {
		if err := l.store.InsertAction(ctx, act); err != nil {
			t.Fatal(err)
		}
	}
	f8, f9 := ago(8), ago(9)
	if err := l.store.SaveBackup(ctx, store.BackupRecord{AppID: a.ID, ID: "db-1", Service: "db", JobID: "j", Status: "succeeded",
		StartedAt: ago(8).Add(-time.Minute), FinishedAt: &f8}); err != nil {
		t.Fatal(err)
	}
	if err := l.store.SaveRestore(ctx, store.RestoreRecord{ID: "rst1", AppID: a.ID, Service: "db", BackupID: "db-1", Status: "failed",
		CreatedAt: ago(9).Add(-time.Minute), FinishedAt: &f9}); err != nil {
		t.Fatal(err)
	}
	sch, err := l.store.InsertSchedule(ctx, store.ScheduleRecord{ID: "aaaaaaaaaaaa", AppID: a.ID, Service: "web", Name: "nightly",
		Command: []string{"sh", "-c", "echo " + activitySentinel}, Cron: "0 3 * * *", StopAfter: time.Hour, MemoryMB: 64,
		ArmedAt: ago(100)})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.store.InsertRun(ctx, store.ScheduleRunRecord{ID: "run000000001", ScheduleID: sch.ID, AppID: a.ID, Service: "web",
		Trigger: "timer", Command: []string{"sh", "-c", "echo " + activitySentinel}, Status: store.RunRunning, StartedAt: ago(11)}); err != nil {
		t.Fatal(err)
	}
	if err := l.store.SettleRun(ctx, "run000000001", store.RunSettlement{Status: store.RunFailed, ExitCode: &three,
		Error: "exited with 3", FinishedAt: ago(10)}); err != nil {
		t.Fatal(err)
	}
	if err := l.store.ApplyProblemCheck(ctx, store.ProblemCheck{SeenAt: ago(12), Opened: []store.SentProblem{
		{Key: "restarting|whoami|web|", Kind: "restarting", AppID: a.ID, Service: "web", OpenedAt: ago(12), LastSeenAt: ago(12),
			Summary: "whoami keeps restarting: out of memory."},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []store.HostEvent{
		{Kind: store.HostEventBoot, At: ago(13), Detail: `{}`, Key: "boot|x"},
		{Kind: store.HostEventVersion, At: ago(14), Detail: `{"from":"0.1.0","to":"0.2.0"}`, Key: "version|0.2.0"},
		{Kind: store.HostEventOSPackages, At: ago(15), Detail: `{"packages":14,"by":"unattended","reboot":true}`, Key: "os|1"},
		// The first version ever recorded is a baseline, not an event: the feed doesn't list it.
		{Kind: store.HostEventVersion, At: ago(16), Detail: `{"from":"","to":"0.1.0"}`, Key: "version|0.1.0|first"},
	} {
		if _, err := l.store.AppendHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.store.ApplyProblemCheck(ctx, store.ProblemCheck{SeenAt: ago(11), Removed: []string{"restarting|whoami|web|"}}); err != nil {
		t.Fatal(err)
	}
	return l, base
}

func TestActivityWordsForEveryKind(t *testing.T) {
	l, base := activityLab(t)
	got, err := l.svc.Activity(context.Background(), ActivityQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ActivityEntry{}
	for _, e := range got {
		by[e.ID] = e
	}
	want := []struct {
		id, kind, status, sentence string
	}{
		{"deployments.dep1.started", "deploy", "rolling", "whoami v24 is rolling out, pushed to main by ana."},
		{"deployments.dep1.ended", "deploy", "healthy", "whoami v24 is live, pushed to main by ana."},
		{"app_actions.act_restart.", "restart", "healthy", "whoami was restarted from the console."},
		{"app_actions.act_stop.", "stop", "healthy", "whoami was stopped through the API."},
		{"app_actions.act_start.", "start", "healthy", "whoami was started from the console."},
		{"app_actions.act_run.", "command", "warning", "cat ran in whoami from the console, exit 3."},
		{"app_actions.act_ok.", "command", "healthy", "ls ran in whoami from the console, exit 0."},
		{"backups.whoami/db-1.", "backup", "healthy", "whoami's backup of db finished."},
		{"restores.rst1.", "restore", "failed", "whoami's restore of db failed."},
		{"schedule_runs.run000000001.", "schedule", "failed", "Schedule nightly failed in whoami, exit 3."},
	}
	for _, w := range want {
		e, ok := by[w.id]
		if !ok {
			t.Errorf("no entry %q in %v", w.id, ids(got))
			continue
		}
		if e.Kind != w.kind || e.Status != w.status || e.Sentence != w.sentence {
			t.Errorf("%s = {%s %s %q}, want {%s %s %q}", w.id, e.Kind, e.Status, e.Sentence, w.kind, w.status, w.sentence)
		}
		if e.AppID != "whoami" || e.AppName != "whoami" {
			t.Errorf("%s names app %q/%q", w.id, e.AppID, e.AppName)
		}
	}

	var problems, server []string
	for _, e := range got {
		switch e.Kind {
		case "needs you":
			problems = append(problems, e.Status+": "+e.Sentence)
		case "server":
			server = append(server, e.Sentence)
		}
	}
	if len(problems) != 2 ||
		problems[0] != "healthy: Resolved: whoami keeps restarting: out of memory." ||
		problems[1] != "warning: whoami keeps restarting: out of memory." {
		t.Errorf("needs-you entries = %q", problems)
	}
	wantServer := []string{
		"The server missed its heartbeat: Nomad lost contact with it.",
		"The server registered with Nomad.",
		"The server started (up since " + base.Add(-13*time.Minute).UTC().Format("15:04") + " UTC).",
		"Caravana was upgraded from 0.1.0 to 0.2.0.",
		"OS packages updated by Ubuntu's automatic updates: 14 packages, a reboot is needed.",
	}
	if len(server) != len(wantServer) {
		t.Fatalf("server entries = %q, want %q", server, wantServer)
	}
	for i := range wantServer {
		if server[i] != wantServer[i] {
			t.Errorf("server entry %d = %q, want %q", i, server[i], wantServer[i])
		}
	}
}

func ids(es []ActivityEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func TestActivityOrderLimitAndBefore(t *testing.T) {
	l, _ := activityLab(t)
	ctx := context.Background()
	all, err := l.svc.Activity(ctx, ActivityQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Time.After(all[i-1].Time) {
			t.Fatalf("entry %d (%s) is newer than entry %d (%s)", i, all[i].ID, i-1, all[i-1].ID)
		}
	}
	// The newest is the deployment's end; the node's events sit between the
	// restart (ago 3) and the registration of the node (ago 2, ago 1).
	if all[0].ID != "deployments.dep1.ended" {
		t.Errorf("newest = %s", all[0].ID)
	}
	pos := func(id string) int {
		for i, e := range all {
			if e.ID == id {
				return i
			}
		}
		t.Fatalf("no %s in %v", id, ids(all))
		return -1
	}
	if !(pos("app_actions.act_restart.") > pos(all[2].ID) && all[2].Kind == "server") {
		t.Errorf("node events don't sit between the deploy and the restart: %v", ids(all))
	}

	// The default limit is 8, a zero limit too; Before pages on.
	def, err := l.svc.Activity(ctx, ActivityQuery{})
	if err != nil || len(def) != DefaultActivityLimit {
		t.Fatalf("default = %d entries, %v", len(def), err)
	}
	next, err := l.svc.Activity(ctx, ActivityQuery{Limit: 3, Before: def[len(def)-1].Time})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range next {
		if !e.Time.Before(def[len(def)-1].Time) {
			t.Errorf("%s is not before the page's last entry", e.ID)
		}
	}
	if len(next) != 3 {
		t.Errorf("next page = %d entries", len(next))
	}
}

func TestActivityServerOnly(t *testing.T) {
	l, _ := activityLab(t)
	got, err := l.svc.Activity(context.Background(), ActivityQuery{Limit: 50, Server: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		switch e.Kind {
		case "server", "restart", "needs you":
		default:
			t.Errorf("server-only feed has a %s entry: %s", e.Kind, e.Sentence)
		}
	}
	if len(got) < 6 {
		t.Errorf("server-only feed = %v", ids(got))
	}
}

// What a command printed, and what it was given, are never in the feed: a
// schedule's command and a run's arguments are in the store, the feed only has
// the program and the exit code.
func TestActivityNeverHoldsCommandText(t *testing.T) {
	l, _ := activityLab(t)
	got, err := l.svc.Activity(context.Background(), ActivityQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		for _, field := range []string{e.ID, e.Sentence, e.AppID, e.AppName, e.Kind, e.Status} {
			if strings.Contains(field, activitySentinel) || strings.Contains(field, "echo") {
				t.Errorf("entry %s holds command text: %q", e.ID, field)
			}
		}
	}
}

func TestActivityDeployOutcomes(t *testing.T) {
	l := newSamplerLab(t, discardLogger())
	ctx := context.Background()
	a := apptest.Whoami().App
	if _, err := l.store.SaveApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	mk := func(id string, st store.Status, version *uint64, errText string) {
		d := apptest.Whoami().Deployment
		d.ID, d.Trigger = id, app.TriggerWebhook
		if _, err := l.store.CreateDeployment(ctx, a.ID, d, nil, nil, ""); err != nil {
			t.Fatal(err)
		}
		if version != nil {
			if _, err := l.store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: version}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := l.store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: st, Error: errText}); err != nil {
			t.Fatal(err)
		}
	}
	v57 := uint64(57)
	mk("failed-early", store.StatusFailed, nil, "image not found")
	mk("failed-rollout", store.StatusFailed, &v57, "deadline exceeded")
	mk("reverted", store.StatusReverted, &v57, "rolling back to job version 56")
	mk("replaced", store.StatusSuperseded, nil, "")
	got, err := l.svc.Activity(ctx, ActivityQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ActivityEntry{}
	for _, e := range got {
		by[e.ID] = e
	}
	for id, want := range map[string]string{
		"deployments.failed-early.ended":   "whoami's deploy failed before it started.",
		"deployments.failed-rollout.ended": "whoami v57 failed its health checks.",
		"deployments.reverted.ended":       "whoami v57 failed its health checks and was rolled back to v56.",
		"deployments.replaced.ended":       "whoami was replaced by a newer deploy.",
	} {
		if by[id].Sentence != want {
			t.Errorf("%s = %q, want %q", id, by[id].Sentence, want)
		}
	}
	if by["deployments.failed-early.ended"].Status != "failed" || by["deployments.replaced.ended"].Status != "idle" {
		t.Errorf("marks: %+v %+v", by["deployments.failed-early.ended"], by["deployments.replaced.ended"])
	}
	for _, e := range got {
		if strings.Contains(e.Sentence, "image not found") || strings.Contains(e.Sentence, "deadline") {
			t.Errorf("a deployment's error text is in the feed: %q", e.Sentence)
		}
	}
}

// The words come as parts, and every name in them is mono wherever it stands: the program, the
// app, the service, the schedule and the version.
func TestActivityPartsSetNamesInMono(t *testing.T) {
	l, _ := activityLab(t)
	got, err := l.svc.Activity(context.Background(), ActivityQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ActivityEntry{}
	for _, e := range got {
		by[e.ID] = e
		if plainText(e.Parts) != e.Sentence {
			t.Errorf("%s: the parts say %q and the sentence %q", e.ID, plainText(e.Parts), e.Sentence)
		}
	}
	monos := func(id string) string {
		var names []string
		for _, p := range by[id].Parts {
			if p.Mono {
				names = append(names, p.Text)
			}
		}
		return strings.Join(names, ",")
	}
	for id, want := range map[string]string{
		"deployments.dep1.ended":      "whoami,v24",
		"app_actions.act_run.":        "cat,whoami",
		"backups.whoami/db-1.":        "whoami,db",
		"restores.rst1.":              "whoami,db",
		"schedule_runs.run000000001.": "nightly,whoami",
		"app_actions.act_restart.":    "whoami",
		"deployments.dep1.started":    "whoami,v24",
	} {
		if got := monos(id); got != want {
			t.Errorf("%s: mono parts = %q, want %q", id, got, want)
		}
	}
	for _, e := range got {
		if e.Kind == "server" && strings.Contains(e.Sentence, "was upgraded") {
			var names []string
			for _, p := range e.Parts {
				if p.Mono {
					names = append(names, p.Text)
				}
			}
			if strings.Join(names, ",") != "0.1.0,0.2.0" {
				t.Errorf("the version change has mono parts %q", names)
			}
		}
	}
}
