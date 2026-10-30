package service

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// HostView.Recent is the mean of the host's minute samples of the 10 minutes up to the newest;
// older samples don't count, and a host never sampled has none.
func TestHostRecentIsTheMeanOfTenMinutes(t *testing.T) {
	ctx := context.Background()
	l := newSamplerLab(t, discardLogger())
	if v, err := l.svc.Host(ctx); err != nil || v.Recent != nil {
		t.Fatalf("Host before a sample: Recent = %+v, %v; want nil", v.Recent, err)
	}
	row := func(ts time.Time, memUsed, cpuUsed int64) store.HostSample {
		return store.HostSample{Node: "n1", TS: ts, MemoryUsedMB: new(memUsed), MemoryTotalMB: new(int64(1000)),
			CPUUsedMHz: new(cpuUsed), CPUTotalMHz: new(int64(2000))}
	}
	base := time.Now().UTC().Truncate(time.Minute)
	rows := store.MetricsRows{Hosts: []store.HostSample{
		row(base.Add(-30*time.Minute), 900, 1800), // too old: not in the mean
		row(base.Add(-9*time.Minute), 400, 200),
		row(base.Add(-1*time.Minute), 600, 400),
		row(base, 800, 600),
	}}
	if err := l.store.WriteMetricsTick(ctx, store.MetricsTick{Minute: rows}); err != nil {
		t.Fatal(err)
	}
	v, err := l.svc.Host(ctx)
	if err != nil || v.Recent == nil {
		t.Fatalf("Host: Recent = %+v, %v", v.Recent, err)
	}
	// Memory: (40 + 60 + 80) / 3 = 60 %; CPU: (10 + 20 + 30) / 3 = 20 %.
	if m, c := *v.Recent.MemoryPercent, *v.Recent.CPUPercent; m < 59.9 || m > 60.1 || c < 19.9 || c > 20.1 {
		t.Errorf("recent = mem %.1f%%, cpu %.1f%%; want 60 and 20", m, c)
	}
}

// AllCopies lists the copies of the apps only, by app ID: the jobs that aren't apps are left out, and
// an app Nomad knows nothing of has an empty list.
func TestAllCopiesAreTheAppsOnly(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := apptest.Whoami().App
	saveApp(t, e, a)
	e.nomad.SetCopies(a.ID, api.AllocationListStub{TaskGroup: "web", ClientStatus: "running"})
	e.nomad.SetCopies("traefik", api.AllocationListStub{TaskGroup: "traefik", ClientStatus: "running"})
	got, err := e.svc.AllCopies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[a.ID]) != 1 {
		t.Errorf("AllCopies = %v, want one app with one copy", got)
	}
	e.nomad.Down()
	if _, err := e.svc.AllCopies(ctx); err == nil {
		t.Error("AllCopies with Nomad down answered")
	}
}
