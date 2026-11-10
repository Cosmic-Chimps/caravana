package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// hostStub is a copy of a service on the node: its status, desired status, and reservation by task
// (MHz, MB, MB ceiling). A running one has started its task.
func hostStub(id, group, status, desired string, tasks map[string][3]int) api.AllocationListStub {
	a := api.AllocationListStub{
		ID: id, TaskGroup: group, JobVersion: 1, NodeID: "node-1", NodeName: "lab",
		ClientStatus: status, DesiredStatus: desired,
		AllocatedResources: samplerResources(tasks),
	}
	if status == api.AllocClientStatusRunning {
		a.TaskStates = map[string]*api.TaskState{group: {State: "running", StartedAt: time.Now().Add(-time.Hour)}}
	}
	return a
}

func TestHostCopies(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	saveApp(t, e, appWith("blog", 1))
	saveApp(t, e, appWith("shop--staging--web", 1))
	if _, err := e.svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}

	run := api.AllocClientStatusRunning
	// blog: a running copy with a names sidecar (the sidecar's 20 MHz is not the service's), a
	// starting one, and one the scheduler has told to stop.
	e.nomad.SetCopies("blog",
		hostStub("c-run", "web", run, api.AllocDesiredStatusRun, map[string][3]int{"web": {100, 64, 256}, "names": {20, 16, 16}}),
		hostStub("c-start", "web", api.AllocClientStatusPending, api.AllocDesiredStatusRun, map[string][3]int{"web": {100, 64, 256}}),
		hostStub("c-stop", "web", run, api.AllocDesiredStatusStop, map[string][3]int{"web": {100, 64, 256}}),
	)
	e.nomad.SetCopies("shop--staging--web",
		hostStub("s-fail", "web", api.AllocClientStatusFailed, api.AllocDesiredStatusRun, map[string][3]int{"web": {50, 128, 512}}),
		hostStub("s-run", "web", run, api.AllocDesiredStatusRun, map[string][3]int{"web": {50, 128, 512}}),
	)
	// A job that isn't an app is left out.
	e.nomad.SetCopies("traefik", hostStub("t1", "traefik", run, api.AllocDesiredStatusRun, map[string][3]int{"traefik": {100, 128, 128}}))
	e.nomad.SetStats("c-run", statsFor("web", 12.4, 30))
	e.nomad.SetStats("c-stop", statsFor("web", 1, 5))
	e.nomad.SetStats("s-run", statsFor("web", 20, 500))

	got, err := e.svc.HostCopies(ctx, "")
	if err != nil {
		t.Fatalf("HostCopies: %v", err)
	}
	var order []string
	for _, c := range got {
		order = append(order, c.ID+":"+c.Phase)
	}
	// Problems first (failed, starting, stopping), then running by app: blog before shop--staging--web
	// only if its project sorts first, and "default" does.
	want := []string{"s-fail:failed", "c-start:starting", "c-stop:stopping", "c-run:running", "s-run:running"}
	if len(order) != len(want) {
		t.Fatalf("copies = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("copies = %v, want %v", order, want)
		}
	}

	byID := map[string]HostCopy{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if c := byID["c-run"]; c.AppID != "blog" || c.Service != "web" || c.Place.Name != "blog" || c.Place.Project != app.DefaultProject || c.StartedAt == nil {
		t.Errorf("c-run = %+v", c)
	}
	// The service task's own numbers: 100 MHz reserved (not the sidecar's 20 more), 64 MB reserved, 256 MB limit.
	if u := byID["c-run"].Usage; u == nil || *u != (CopyUsage{CPUMHz: 12, MemoryMB: 30, CPUReservedMHz: 100, MemoryReservedMB: 64, MemoryCeilingMB: 256}) {
		t.Errorf("c-run usage = %+v", u)
	}
	// 500 MB of a 512 MB limit is near it.
	if u := byID["s-run"].Usage; u == nil || u.MemoryMB != 500 || u.MemoryCeilingMB != 512 || !u.NearCeiling || u.CPUReservedMHz != 50 {
		t.Errorf("s-run usage = %+v", u)
	}
	if c := byID["s-run"]; c.Place.Project != "shop" || c.Place.Environment != "staging" || c.Place.Name != "web" {
		t.Errorf("s-run place = %+v", c.Place)
	}
	// A copy that hasn't started has its reservation and no value (not 0 MB); so does a failed copy: the
	// reservation is a fact of the copy in every phase.
	if u := byID["c-start"].Usage; u == nil || !u.None || u.MemoryReservedMB != 64 || u.MemoryMB != 0 {
		t.Errorf("c-start usage = %+v, want None with the reservation", u)
	}
	if u := byID["s-fail"].Usage; u == nil || !u.None || u.CPUReservedMHz != 50 || u.MemoryReservedMB != 128 || u.MemoryCeilingMB != 512 || u.MemoryMB != 0 {
		t.Errorf("a failed copy's usage = %+v, want None with the reservation it held", u)
	}
	if _, ok := byID["t1"]; ok {
		t.Error("a job that isn't an app is listed")
	}
}

// A copy Nomad can't give numbers for says so and doesn't touch the others; one with nothing measured
// has no value; and Nomad down is deploy's error.
func TestHostCopiesUsageErrorsAreOneCopys(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	saveApp(t, e, appWith("blog", 1))
	run := api.AllocClientStatusRunning
	e.nomad.SetCopies("blog",
		hostStub("c1", "web", run, api.AllocDesiredStatusRun, map[string][3]int{"web": {100, 64, 256}}),
		hostStub("c2", "web", run, api.AllocDesiredStatusRun, map[string][3]int{"web": {100, 64, 256}}),
		hostStub("c3", "web", run, api.AllocDesiredStatusRun, map[string][3]int{"web": {100, 64, 256}}),
	)
	e.nomad.SetStats("c1", statsFor("web", 5, 10))
	e.nomad.SetStats("c2", deploytest.DeadAllocUsage("web"))
	// c3 has no stats: the fake answers 404 for it.
	got, err := e.svc.HostCopies(ctx, "")
	if err != nil {
		t.Fatalf("HostCopies: %v", err)
	}
	byID := map[string]HostCopy{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if u := byID["c1"].Usage; u == nil || u.MemoryMB != 10 || u.Error != "" || u.None {
		t.Errorf("c1 = %+v", u)
	}
	if u := byID["c2"].Usage; u == nil || !u.None || u.Error != "" || u.MemoryReservedMB != 64 {
		t.Errorf("c2 = %+v, want no value and no error", u)
	}
	if u := byID["c3"].Usage; u == nil || u.Error == "" || u.MemoryReservedMB != 64 {
		t.Errorf("c3 = %+v, want an error and the reservation", u)
	}

	e.nomad.Down()
	if _, err := e.svc.HostCopies(ctx, ""); !errors.Is(err, deploy.ErrNomadUnavailable) {
		t.Errorf("HostCopies with Nomad down = %v, want ErrNomadUnavailable", err)
	}
}

func TestHostCopiesWithNoCopies(t *testing.T) {
	e := newEnv(t)
	saveApp(t, e, appWith("blog", 1))
	got, err := e.svc.HostCopies(context.Background(), "")
	if err != nil || len(got) != 0 {
		t.Errorf("HostCopies = %v, %v; want none", got, err)
	}
}
