package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// scaledWhoami is whoami scaled 1 to 3 on requests a second per copy (target
// 5), with the one-minute cooldown of the shortest setting, starting at 1.
func scaledWhoami() app.App {
	a := whoami()
	a.Services[0].Replicas = nil
	a.Services[0].Scale = &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{RequestsPerCopy: 5}, CooldownSeconds: 60}
	return a
}

// scaleClock is a time after every deploy of the test, on a minute boundary, so
// the rows written before it are after the deployment's end.
func scaleClock() time.Time {
	return time.Now().UTC().Truncate(time.Minute).Add(2 * time.Hour)
}

// writeLoad writes n minute rows ending one minute before at, for service svc
// of appID: perMin requests a minute (so perMin/60 a second, in total).
func writeLoad(t *testing.T, e *env, appID, svc string, at time.Time, n int, perMin int64) {
	t.Helper()
	ctx := context.Background()
	series, err := e.store.EnsureSeries(ctx, appID, svc)
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.MetricsSample
	for i := 1; i <= n; i++ {
		rows = append(rows, store.MetricsSample{Series: series, TS: at.Add(-time.Duration(i) * time.Minute), Minutes: 1,
			HTTPSecs: new(int64(60)), Requests: new(perMin), Errors: new(int64(0))})
	}
	if err := e.store.WriteMetricsTick(ctx, store.MetricsTick{Minute: store.MetricsRows{Samples: rows}}); err != nil {
		t.Fatal(err)
	}
}

func scaleEnv(t *testing.T, a app.App) *env {
	t.Helper()
	e := newEnv(t)
	e.svc.metricsGap = time.Minute
	deployHealthy(t, e, a)
	return e
}

func scalesOf(t *testing.T, e *env, appID string) []store.ActionRecord {
	t.Helper()
	acts, err := e.svc.ListActions(context.Background(), appID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.ActionRecord
	for _, a := range acts {
		if a.Action == store.ActionScale {
			out = append(out, a)
		}
	}
	return out
}

func countOf(t *testing.T, e *env, appID, svc string) int {
	t.Helper()
	job := e.nomad.Job(appID)
	for _, g := range job.TaskGroups {
		if *g.Name == svc {
			return *g.Count
		}
	}
	t.Fatalf("no group %s", svc)
	return 0
}

func TestAutoscaleStepsUpWithItsReason(t *testing.T) {
	ctx := context.Background()
	e := scaleEnv(t, scaledWhoami())
	now := scaleClock()
	writeLoad(t, e, "whoami", "web", now, 4, 360) // 6 a second on one copy, target 5

	e.svc.autoscale(ctx, now)

	got := scalesOf(t, e, "whoami")
	if len(got) != 1 {
		t.Fatalf("scale actions = %+v, want one", got)
	}
	a := got[0]
	if a.Status != store.ActionSucceeded || a.Trigger != "autoscale" || a.Service != "web" ||
		*a.FromCount != 1 || *a.ToCount != 2 || a.JobVersion == nil || a.BaseVersion == nil {
		t.Errorf("action = %+v", a)
	}
	if want := "6 requests a second per copy over the last 3 minutes, target 5"; a.Reason != want {
		t.Errorf("reason = %q, want %q", a.Reason, want)
	}
	if n := countOf(t, e, "whoami", "web"); n != 2 {
		t.Errorf("Nomad's count = %d, want 2", n)
	}
	reqs := e.nomad.Scales()
	if len(reqs) != 1 || reqs[0].Message != a.Reason {
		t.Errorf("scale requests = %+v", reqs)
	}
	counts, _ := e.store.ServiceCounts(ctx)
	if counts["whoami"]["web"] != 2 {
		t.Errorf("stored counts = %v, want web 2", counts)
	}
	// The status counts the copies the service has now.
	sum, err := e.svc.AppSummary(ctx, "whoami")
	if err != nil || sum.Status.WantedCopies != 2 || sum.Status.Services[0].Wanted != 2 {
		t.Errorf("status = %+v, %v; want 2 wanted", sum.Status, err)
	}
}

func TestAutoscaleWaitsOutItsCooldownThenStepsAgain(t *testing.T) {
	ctx := context.Background()
	e := scaleEnv(t, scaledWhoami())
	now := scaleClock()
	writeLoad(t, e, "whoami", "web", now, 4, 360)
	e.svc.autoscale(ctx, now)

	// A minute later the load is still high per copy (two copies, 12 a second in
	// all), but the cooldown is a minute counted from the step: still inside it.
	writeLoad(t, e, "whoami", "web", now.Add(30*time.Second), 4, 720)
	e.svc.autoscale(ctx, now.Add(30*time.Second))
	if n := len(scalesOf(t, e, "whoami")); n != 1 {
		t.Fatalf("%d scale actions inside the cooldown, want 1", n)
	}

	// After it, with rows from after the step, it goes on to 3; then it is at max.
	later := now.Add(10 * time.Minute)
	writeLoad(t, e, "whoami", "web", later, 4, 720)
	e.svc.autoscale(ctx, later)
	acts := scalesOf(t, e, "whoami")
	if len(acts) != 2 || *acts[0].ToCount != 3 {
		t.Fatalf("scale actions = %+v, want a second step to 3", acts)
	}
	if acts[0].BaseVersion == nil || acts[1].BaseVersion == nil || *acts[0].BaseVersion != *acts[1].BaseVersion {
		t.Errorf("a chain of scales maps to one deployment: %v and %v", acts[0].BaseVersion, acts[1].BaseVersion)
	}
	end := later.Add(10 * time.Minute)
	writeLoad(t, e, "whoami", "web", end, 4, 1800)
	e.svc.autoscale(ctx, end)
	if n := len(scalesOf(t, e, "whoami")); n != 2 {
		t.Errorf("%d scale actions at max, want 2", n)
	}
}

func TestAutoscaleStepsDown(t *testing.T) {
	ctx := context.Background()
	e := scaleEnv(t, scaledWhoami())
	if _, _, err := e.svc.deployer.Scale(ctx, "whoami", "web", 3, "setup"); err != nil {
		t.Fatal(err)
	}
	now := scaleClock()
	writeLoad(t, e, "whoami", "web", now, 6, 120) // 2 a second in all
	e.svc.autoscale(ctx, now)
	acts := scalesOf(t, e, "whoami")
	if len(acts) != 1 || *acts[0].FromCount != 3 || *acts[0].ToCount != 2 {
		t.Fatalf("scale actions = %+v, want 3 -> 2", acts)
	}
	if want := "about 1 requests a second per copy on one copy fewer over the last 5 minutes, target 5"; acts[0].Reason != want {
		t.Errorf("reason = %q, want %q", acts[0].Reason, want)
	}
}

func TestAutoscaleBringsABadCountBack(t *testing.T) {
	ctx := context.Background()
	e := scaleEnv(t, scaledWhoami())
	if _, _, err := e.svc.deployer.Scale(ctx, "whoami", "web", 5, "outside"); err != nil {
		t.Fatal(err)
	}
	// No rows at all: the bound needs no metrics and no cooldown.
	e.svc.autoscale(ctx, scaleClock())
	acts := scalesOf(t, e, "whoami")
	if len(acts) != 1 || *acts[0].ToCount != 3 || !strings.Contains(acts[0].Reason, "outside the scale's 1–3 copies") {
		t.Fatalf("scale actions = %+v, want the count brought back to 3", acts)
	}
	if n := countOf(t, e, "whoami", "web"); n != 3 {
		t.Errorf("Nomad's count = %d, want 3", n)
	}
}

func TestAutoscaleRecordsAFailureOnceAndWaits(t *testing.T) {
	ctx := context.Background()
	e := scaleEnv(t, scaledWhoami())
	now := scaleClock()
	writeLoad(t, e, "whoami", "web", now, 4, 360)
	e.nomad.RejectScale(500, "boom")

	e.svc.autoscale(ctx, now)
	acts := scalesOf(t, e, "whoami")
	if len(acts) != 1 || acts[0].Status != store.ActionFailed || acts[0].Error == "" || acts[0].JobVersion != nil {
		t.Fatalf("scale actions = %+v, want one failed step with an error and no version", acts)
	}
	if n := countOf(t, e, "whoami", "web"); n != 1 {
		t.Errorf("Nomad's count = %d after a refused step, want 1", n)
	}
	// The refused step counts for the cooldown: the next tick doesn't try again.
	e.svc.autoscale(ctx, now.Add(30*time.Second))
	if n := len(scalesOf(t, e, "whoami")); n != 1 {
		t.Errorf("%d scale actions, want the failure alone", n)
	}
	if n := len(e.nomad.Scales()); n != 0 {
		t.Errorf("Nomad accepted %d scales, want none", n)
	}
}

func TestAutoscaleLeavesAlone(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		hold func(t *testing.T, e *env) (release func())
	}{
		{"an app whose lock is held", func(t *testing.T, e *env) func() {
			state := e.svc.stateOf("whoami")
			state.mu.Lock()
			return state.mu.Unlock
		}},
		{"a Nomad deployment still going", func(t *testing.T, e *env) func() {
			e.nomad.SetDeployment("whoami", 0, api.DeploymentStatusRunning)
			return func() {}
		}},
		{"a stopped app", func(t *testing.T, e *env) func() {
			if _, err := e.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
				t.Fatal(err)
			}
			return func() {}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := scaleEnv(t, scaledWhoami())
			now := scaleClock()
			writeLoad(t, e, "whoami", "web", now, 4, 360)
			release := tc.hold(t, e)
			e.svc.autoscale(ctx, now)
			release()
			if n := len(e.nomad.Scales()); n != 0 {
				t.Errorf("%d scales while it should wait", n)
			}
			if n := len(scalesOf(t, e, "whoami")); n != 0 {
				t.Errorf("%d scale actions, want none", n)
			}
		})
	}
}

func TestAutoscaleOneStepPerApp(t *testing.T) {
	ctx := context.Background()
	a := scaledWhoami()
	api2 := a.Services[0]
	api2.Name = "api"
	api2.Ports, api2.Domains, api2.Health = nil, nil, nil
	api2.Scale = &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{CPU: 50}, CooldownSeconds: 60}
	a.Services = append(a.Services, api2)
	e := scaleEnv(t, a)
	now := scaleClock()
	writeLoad(t, e, "whoami", "web", now, 4, 360)
	series, _ := e.store.EnsureSeries(ctx, "whoami", "api")
	var rows []store.MetricsSample
	for i := 1; i <= 4; i++ {
		rows = append(rows, store.MetricsSample{Series: series, TS: now.Add(-time.Duration(i) * time.Minute), Minutes: 1,
			CPUMHz: new(int64(90)), CPUReservedMHz: new(int64(100))})
	}
	if err := e.store.WriteMetricsTick(ctx, store.MetricsTick{Minute: store.MetricsRows{Samples: rows}}); err != nil {
		t.Fatal(err)
	}

	e.svc.autoscale(ctx, now)
	acts := scalesOf(t, e, "whoami")
	if len(acts) != 1 || acts[0].Service != "web" {
		t.Fatalf("scale actions = %+v, want one step, on the first service", acts)
	}
	// The next tick, past the cooldown, takes the other one.
	later := now.Add(10 * time.Minute)
	writeLoad(t, e, "whoami", "web", later, 4, 0)
	rows = nil
	for i := 1; i <= 4; i++ {
		rows = append(rows, store.MetricsSample{Series: series, TS: later.Add(-time.Duration(i) * time.Minute), Minutes: 1,
			CPUMHz: new(int64(180)), CPUReservedMHz: new(int64(200))})
	}
	if err := e.store.WriteMetricsTick(ctx, store.MetricsTick{Minute: store.MetricsRows{Samples: rows}}); err != nil {
		t.Fatal(err)
	}
	e.svc.autoscale(ctx, later)
	if got := scalesOf(t, e, "whoami"); len(got) != 2 || got[0].Service != "api" {
		t.Errorf("scale actions = %+v, want the api's step now", got)
	}
}

// A deploy at a scaled count registers that count and keeps the stored count.
func TestDeployKeepsTheScaledCount(t *testing.T) {
	ctx := context.Background()
	a := scaledWhoami()
	e := scaleEnv(t, a)
	if _, _, err := e.svc.deployer.Scale(ctx, "whoami", "web", 3, "setup"); err != nil {
		t.Fatal(err)
	}
	deployHealthy(t, e, a)
	g := e.nomad.Job("whoami").TaskGroups[0]
	if *g.Count != 3 || *g.Update.Canary != 3 {
		t.Errorf("after a deploy count %d canary %d, want 3 and 3", *g.Count, *g.Update.Canary)
	}
	counts, _ := e.store.ServiceCounts(ctx)
	if counts["whoami"]["web"] != 3 {
		t.Errorf("stored counts = %v, want web 3", counts)
	}
	rec, _ := e.svc.GetApp(ctx, "whoami")
	if rec.App.Services[0].Replicas != nil {
		t.Errorf("the stored app's replicas = %d, want it left unset", *rec.App.Services[0].Replicas)
	}

	// Without scale the count is the replicas again, and nothing is left stored.
	a.Services[0].Scale = nil
	deployHealthy(t, e, a)
	if n := countOf(t, e, "whoami", "web"); n != 1 {
		t.Errorf("unscaled count = %d, want 1", n)
	}
	if counts, _ := e.store.ServiceCounts(ctx); len(counts) != 0 {
		t.Errorf("stored counts = %v, want none", counts)
	}
}

// The copies of a scale's job version show the deployment's version, so the
// live version doesn't move because of a scale.
func TestScaleVersionsReadAsTheDeployments(t *testing.T) {
	ctx := context.Background()
	e := scaleEnv(t, scaledWhoami())
	now := scaleClock()
	writeLoad(t, e, "whoami", "web", now, 4, 360)
	before := *e.nomad.Job("whoami").Version
	e.svc.autoscale(ctx, now)
	scaled := *e.nomad.Job("whoami").Version
	if scaled == before {
		t.Fatal("a scale made no new version")
	}
	e.nomad.SetCopies("whoami", api.AllocationListStub{TaskGroup: "web", ClientStatus: "running", JobVersion: scaled})
	copies, err := e.svc.deployer.Copies(ctx, "whoami")
	if err != nil || len(copies) != 1 || copies[0].Version != before {
		t.Errorf("copies = %+v, %v; want the version %d of the deployment", copies, err, before)
	}
}

func TestScaleReason(t *testing.T) {
	rule := func(kind string, target float64) metrics.ScaleRule {
		return metrics.ScaleRule{Min: 1, Max: 3, Kind: kind, Target: target}
	}
	tests := []struct {
		name  string
		d     metrics.ScaleDecision
		r     metrics.ScaleRule
		count int
		span  time.Duration
		want  string
	}{
		{"requests up", metrics.ScaleDecision{Move: metrics.MoveUp, Value: 62.4}, rule("requestsPerCopy", 50), 1, 3 * time.Minute,
			"62 requests a second per copy over the last 3 minutes, target 50"},
		{"cpu up", metrics.ScaleDecision{Move: metrics.MoveUp, Value: 91.2}, rule("cpu", 70), 2, 3 * time.Minute,
			"CPU at 91 % of its reservation over the last 3 minutes, target 70 %"},
		{"p95 up", metrics.ScaleDecision{Move: metrics.MoveUp, Value: 420}, rule("p95", 300), 1, 3 * time.Minute,
			"the slowest 5 % at 420 ms over the last 3 minutes, target 300 ms"},
		{"requests down projects one copy fewer", metrics.ScaleDecision{Move: metrics.MoveDown, Value: 12}, rule("requestsPerCopy", 50), 3, 5 * time.Minute,
			"about 18 requests a second per copy on one copy fewer over the last 5 minutes, target 50"},
		{"cpu down", metrics.ScaleDecision{Move: metrics.MoveDown, Value: 20}, rule("cpu", 70), 2, 5 * time.Minute,
			"about 40 % CPU on one copy fewer over the last 5 minutes, target 70 %"},
		{"p95 down", metrics.ScaleDecision{Move: metrics.MoveDown, Value: 80}, rule("p95", 300), 2, 5 * time.Minute,
			"the slowest 5 % at 80 ms over the last 5 minutes, under half of the 300 ms target"},
		{"a small value keeps a decimal", metrics.ScaleDecision{Move: metrics.MoveUp, Value: 5.04}, rule("requestsPerCopy", 5), 1, time.Minute,
			"5 requests a second per copy over the last minute, target 5"},
		{"a window in seconds", metrics.ScaleDecision{Move: metrics.MoveUp, Value: 7.25}, rule("requestsPerCopy", 5), 1, 30 * time.Second,
			"7.3 requests a second per copy over the last 30 seconds, target 5"},
		{"bound", metrics.ScaleDecision{Move: metrics.MoveBound, To: 3}, rule("cpu", 70), 5, 0,
			"the count was outside the scale's 1–3 copies"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scaleReason(tc.d, tc.r, tc.count, tc.span); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// The needs-you kind: at its maximum and still over the target over the last 10
// minutes; it goes when the load drops, and a stopped app is not judged.
func TestAttentionScaleAtMax(t *testing.T) {
	ctx := context.Background()
	e := scaleEnv(t, scaledWhoami())
	now := e.svc.clock.Now().UTC().Truncate(time.Minute)
	e.svc.clock.(*fakeClock).Set(now)
	if _, _, err := e.svc.deployer.Scale(ctx, "whoami", "web", 3, "setup"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetServiceCount(ctx, "whoami", "web", 3); err != nil {
		t.Fatal(err)
	}
	items := func() []AttentionItem {
		att, err := e.svc.Attention(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []AttentionItem
		for _, it := range att.Items {
			if it.Kind == AttentionScaleAtMax {
				out = append(out, it)
			}
		}
		return out
	}
	if got := items(); len(got) != 0 {
		t.Fatalf("items with no load = %+v", got)
	}

	writeLoad(t, e, "whoami", "web", now, 10, 1800) // 30 a second on 3 copies: 10 each, target 5
	got := items()
	if len(got) != 1 {
		t.Fatalf("items = %+v, want one scale_at_max", got)
	}
	it := got[0]
	if it.AppID != "whoami" || it.Service != "web" || it.Count != 3 || it.Level != metrics.DiskWarning {
		t.Errorf("item = %+v", it)
	}
	if want := "whoami/web: at its maximum of 3 copies and still over its target. Raise the maximum, give it more CPU, or add a server."; it.Detail != want {
		t.Errorf("detail = %q, want %q", it.Detail, want)
	}

	// Below the maximum it is the autoscaler's to fix, not the owner's.
	if err := e.store.SetServiceCount(ctx, "whoami", "web", 2); err != nil {
		t.Fatal(err)
	}
	if got := items(); len(got) != 0 {
		t.Errorf("items below the maximum = %+v", got)
	}
	if err := e.store.SetServiceCount(ctx, "whoami", "web", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if got := items(); len(got) != 0 {
		t.Errorf("items of a stopped app = %+v", got)
	}
}

// A scale step is worded in the feed, on its own.
func TestActivityWordsAScaleStep(t *testing.T) {
	ctx := context.Background()
	e := scaleEnv(t, scaledWhoami())
	now := scaleClock()
	writeLoad(t, e, "whoami", "web", now, 4, 360)
	e.svc.autoscale(ctx, now)
	e.nomad.RejectScale(500, "boom")
	writeLoad(t, e, "whoami", "web", now.Add(10*time.Minute), 4, 720)
	e.svc.autoscale(ctx, now.Add(10*time.Minute))

	entries, err := e.svc.Activity(ctx, ActivityQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, en := range entries {
		if en.Kind == ActivityScale {
			texts = append(texts, plainText(en.Parts))
		}
	}
	want := []string{
		"Couldn't scale web in whoami 2 → 3: ",
		"Scaled web in whoami 1 → 2 on its own: 6 requests a second per copy over the last 3 minutes, target 5.",
	}
	if len(texts) != 2 || !strings.HasPrefix(texts[0], want[0]) || texts[1] != want[1] {
		t.Errorf("scale entries = %q, want %q", texts, want)
	}
}

// Promotion keeps the target's scale like its replicas; a service the target
// lacks arrives with the source's, unless it measures at a proxy it has no
// domain for. Copy to keeps it, and the copy's first deploy asks for the domain.
func TestPromoteAndCopyKeepScale(t *testing.T) {
	ctx := context.Background()
	source, target := staging(), production()
	source.Services[0].Scale = &app.Scale{Min: 1, Max: 2, Target: app.ScaleTarget{CPU: 60}}
	target.Services[0].Scale = &app.Scale{Min: 2, Max: 5, Target: app.ScaleTarget{CPU: 80}}
	got, changes, _, err := promoted(target, source)
	if err != nil {
		t.Fatal(err)
	}
	if got.Services[0].Scale == nil || got.Services[0].Scale.Max != 5 {
		t.Errorf("promoted scale = %+v, want the target's own", got.Services[0].Scale)
	}
	if slices.Contains(changes[0].Changed, "scale") {
		t.Errorf("changed = %v: the scale stays the target's, so it isn't a change", changes[0].Changed)
	}

	// A service new to the target.
	source = staging()
	extra := source.Services[0]
	extra.Name = "api"
	extra.Scale = &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{RequestsPerCopy: 10}}
	cheap := source.Services[0]
	cheap.Name = "jobs"
	cheap.Scale = &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{CPU: 70}}
	source.Services = append(source.Services, extra, cheap)
	got, _, added, err := promoted(production(), source)
	if err != nil || len(added) != 2 {
		t.Fatalf("promoted() = %v, %v", added, err)
	}
	if got.Services[1].Scale != nil {
		t.Errorf("an added service that measures at the proxy kept its scale: %+v", got.Services[1].Scale)
	}
	if got.Services[2].Scale == nil || got.Services[2].Scale.Target.CPU != 70 {
		t.Errorf("an added cpu-scaled service lost its scale: %+v", got.Services[2].Scale)
	}

	// Copy to.
	e := shopEnv(t)
	setDestination(t, e)
	prod := production()
	prod.Services[0].Scale = &app.Scale{Min: 1, Max: 4, Target: app.ScaleTarget{RequestsPerCopy: 20}}
	prod.Services[0].Replicas = new(2)
	deployHealthy(t, e, prod)
	rec, _, err := e.svc.CopyApp(ctx, productionID, "staging")
	if err != nil {
		t.Fatalf("CopyApp() = %v", err)
	}
	if sc := rec.App.Services[0].Scale; sc == nil || sc.Max != 4 || len(rec.App.Services[0].Domains) != 0 {
		t.Errorf("the copy = scale %+v, domains %v; want the scale kept, no domains", sc, rec.App.Services[0].Domains)
	}
	_, err = e.svc.Deploy(ctx, rec.App, app.TriggerManual)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "services[0].scale.target" {
		t.Errorf("deploying the copy = %v, want a field error on services[0].scale.target", err)
	}
}
