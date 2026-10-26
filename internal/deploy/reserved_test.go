package deploy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

func TestFlattenReserved(t *testing.T) {
	main := func(cpu, mem int) TaskReservation { return TaskReservation{CPUMHz: cpu, MemoryMB: mem} }
	pre := func(cpu, mem int) TaskReservation {
		return TaskReservation{CPUMHz: cpu, MemoryMB: mem, Hook: api.TaskLifecycleHookPrestart}
	}
	sidecar := func(cpu, mem int) TaskReservation {
		return TaskReservation{CPUMHz: cpu, MemoryMB: mem, Hook: api.TaskLifecycleHookPrestart, Sidecar: true}
	}
	stop := func(cpu, mem int) TaskReservation {
		return TaskReservation{CPUMHz: cpu, MemoryMB: mem, Hook: api.TaskLifecycleHookPoststop}
	}
	tests := []struct {
		name         string
		tasks        []TaskReservation
		cpu, memory  int
		naiveCPU     int // the plain sum, for the cases where it differs
		naiveMemory  int
		wantNaiveGap bool
	}{
		{"a service alone", []TaskReservation{main(100, 64)}, 100, 64, 100, 64, false},
		{"the names sidecar runs beside the service", []TaskReservation{main(100, 64), sidecar(20, 16)}, 120, 80, 120, 80, false},
		{"a prestart task that ends before main isn't added (caravana-volumes)",
			[]TaskReservation{main(100, 96), sidecar(20, 16), pre(50, 32)}, 120, 112, 170, 144, true},
		{"a prestart task larger than main sets the reservation",
			[]TaskReservation{main(100, 64), pre(300, 200)}, 300, 200, 400, 264, true},
		{"each dimension takes its own largest sum",
			[]TaskReservation{main(100, 64), pre(300, 16)}, 300, 64, 400, 80, true},
		{"a poststop task runs alone at the end",
			[]TaskReservation{main(100, 64), stop(80, 128)}, 100, 128, 180, 192, true},
		{"a poststart task is part of main", []TaskReservation{main(100, 64), {CPUMHz: 10, MemoryMB: 8, Hook: api.TaskLifecycleHookPoststart}}, 110, 72, 110, 72, false},
		{"no tasks", nil, 0, 0, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cpu, mem := FlattenReserved(tc.tasks)
			if cpu != tc.cpu || mem != tc.memory {
				t.Errorf("flattened = %d MHz, %d MB; want %d, %d", cpu, mem, tc.cpu, tc.memory)
			}
			var sumCPU, sumMem int
			for _, task := range tc.tasks {
				sumCPU += task.CPUMHz
				sumMem += task.MemoryMB
			}
			if (sumCPU != cpu || sumMem != mem) != tc.wantNaiveGap || sumCPU != tc.naiveCPU || sumMem != tc.naiveMemory {
				t.Errorf("plain sum = %d, %d (test says %d, %d)", sumCPU, sumMem, tc.naiveCPU, tc.naiveMemory)
			}
		})
	}
}

// resources is an allocation's reserved resources by task.
func resources(tasks map[string][3]int) *api.AllocatedResources {
	out := &api.AllocatedResources{Tasks: map[string]*api.AllocatedTaskResources{}}
	for name, r := range tasks {
		out.Tasks[name] = &api.AllocatedTaskResources{
			Cpu:    api.AllocatedCpuResources{CpuShares: int64(r[0])},
			Memory: api.AllocatedMemoryResources{MemoryMB: int64(r[1]), MemoryMaxMB: int64(r[2])},
		}
	}
	return out
}

func task(name string, hook string, sidecar bool) *api.Task {
	t := &api.Task{Name: name}
	if hook != "" {
		t.Lifecycle = &api.TaskLifecycle{Hook: hook, Sidecar: sidecar}
	}
	return t
}

func jobOf(id string, group string, tasks ...*api.Task) *api.Job {
	return &api.Job{ID: &id, Name: &id, TaskGroups: []*api.TaskGroup{{Name: &group, Tasks: tasks}}}
}

func running(id, jobID, group string, version uint64, res *api.AllocatedResources) api.AllocationListStub {
	return api.AllocationListStub{
		ID: id, JobID: jobID, TaskGroup: group, JobVersion: version, NodeID: "node-1",
		ClientStatus: api.AllocClientStatusRunning, DesiredStatus: api.AllocDesiredStatusRun,
		AllocatedResources: res,
	}
}

// TestReservationsLabNumbers is the lab's node on 2026-10-25 (research R8.7):
// 18 copies whose reservations, by Nomad's lifecycle rule, summed to 1780 MHz
// and 1344 MB, which is what nomad.client.allocated.cpu and .memory said. A
// plain sum of every task is 1930 and 1440: three copies have a prestart
// caravana-volumes task (50 MHz, 32 MB) that has ended before main starts.
func TestReservationsLabNumbers(t *testing.T) {
	d, _, nomad := newTestDeployer(t)

	// three volume apps: service, names sidecar, caravana-volumes
	nomad.AddForeignJob(jobOf("vol", "web",
		task("web", "", false), task("names", api.TaskLifecycleHookPrestart, true), task("caravana-volumes", api.TaskLifecycleHookPrestart, false)))
	var vol []api.AllocationListStub
	for i := range 3 {
		vol = append(vol, running(fmt.Sprintf("vol-%d", i), "vol", "web", 0,
			resources(map[string][3]int{"web": {100, 96, 0}, "names": {20, 16, 0}, "caravana-volumes": {50, 32, 512}})))
	}
	nomad.SetCopies("vol", vol...)

	// eleven plain copies
	nomad.AddForeignJob(jobOf("plain", "web", task("web", "", false)))
	var plain []api.AllocationListStub
	for i := range 11 {
		plain = append(plain, running(fmt.Sprintf("plain-%d", i), "plain", "web", 0, resources(map[string][3]int{"web": {100, 64, 0}})))
	}
	// a crashed and a finished copy of the same job reserve nothing now
	failed := running("plain-failed", "plain", "web", 0, resources(map[string][3]int{"web": {100, 64, 0}}))
	failed.ClientStatus = api.AllocClientStatusFailed
	done := running("plain-done", "plain", "web", 0, resources(map[string][3]int{"web": {100, 64, 0}}))
	done.ClientStatus = api.AllocClientStatusComplete
	nomad.SetCopies("plain", append(plain, failed, done)...)

	// four copies of another service
	nomad.AddForeignJob(jobOf("other", "api", task("api", "", false)))
	var other []api.AllocationListStub
	for i := range 4 {
		other = append(other, running(fmt.Sprintf("other-%d", i), "other", "api", 0, resources(map[string][3]int{"api": {80, 76, 0}})))
	}
	nomad.SetCopies("other", other...)

	got, err := d.Reservations(context.Background())
	if err != nil {
		t.Fatalf("Reservations: %v", err)
	}
	if got.Total.CPUMHz != 1780 || got.Total.MemoryMB != 1344 || got.Total.Copies != 18 || len(got.Copies) != 18 {
		t.Errorf("total = %+v over %d copies, want 1780 MHz, 1344 MB over 18", got.Total, len(got.Copies))
	}
	if r := got.ByKey[ServiceKey{"vol", "web"}]; r != (Reserved{CPUMHz: 360, MemoryMB: 336, Copies: 3}) {
		t.Errorf("vol/web = %+v, want 360 MHz, 336 MB, 3 copies (not 510, 432)", r)
	}
	if r := got.ByKey[ServiceKey{"plain", "web"}]; r != (Reserved{CPUMHz: 1100, MemoryMB: 704, Copies: 11}) {
		t.Errorf("plain/web = %+v", r)
	}
	if r := got.ByKey[ServiceKey{"other", "api"}]; r != (Reserved{CPUMHz: 320, MemoryMB: 304, Copies: 4}) {
		t.Errorf("other/api = %+v", r)
	}
	for _, c := range got.Copies {
		if c.Status == CopyFailed || c.Status == CopyStopped {
			t.Errorf("copy %s is %s: only live copies are listed", c.CopyID, c.Status)
		}
	}

	// The lifecycles come from one job read per job, then from the cache.
	if n := nomad.JobInfoRequests(); n != 3 {
		t.Errorf("job reads = %d, want 3", n)
	}
	if _, err := d.Reservations(context.Background()); err != nil {
		t.Fatalf("Reservations again: %v", err)
	}
	if n := nomad.JobInfoRequests(); n != 3 {
		t.Errorf("job reads after a second call = %d, want 3 still", n)
	}
	if n := nomad.AllocationRequests(); n != 2 {
		t.Errorf("allocation lists = %d, want one per call", n)
	}
}

func TestReservationsPerCopy(t *testing.T) {
	d, _, nomad := newTestDeployer(t)
	nomad.AddForeignJob(jobOf("web", "web", task("web", "", false)))

	exit := time.Date(2026, 10, 25, 11, 30, 0, 0, time.UTC)
	c := running("c1", "web", "web", 0, resources(map[string][3]int{"web": {100, 64, 256}}))
	c.TaskStates = map[string]*api.TaskState{"web": {
		Restarts: 3,
		Events: []*api.TaskEvent{
			{Type: api.TaskTerminated, Time: exit.Add(-time.Hour).UnixNano(), Details: map[string]string{"exit_code": "1"}},
			{Type: api.TaskRestartSignal, DisplayMessage: "User requested all tasks to restart"},
			{Type: api.TaskTerminated, Time: exit.UnixNano(), Details: map[string]string{"exit_code": "137", "oom_killed": "true"}},
			{Type: api.TaskStarted, Time: exit.Add(time.Minute).UnixNano()},
		},
	}}
	c2 := running("c2", "web", "web", 0, resources(map[string][3]int{"web": {100, 64, 0}}))
	c2.ClientStatus = api.AllocClientStatusPending
	nomad.SetCopies("web", c, c2)

	got, err := d.Reservations(context.Background())
	if err != nil {
		t.Fatalf("Reservations: %v", err)
	}
	if len(got.Copies) != 2 {
		t.Fatalf("copies = %+v", got.Copies)
	}
	a, b := got.Copies[0], got.Copies[1]
	if a.CopyID != "c1" || a.JobID != "web" || a.Group != "web" || a.NodeID != "node-1" || a.Version != 0 {
		t.Errorf("c1 = %+v", a)
	}
	if a.ServiceMemoryMB != 64 || a.ServiceMemoryMaxMB != 256 || a.ServiceMemoryLimitMB() != 256 {
		t.Errorf("c1 memory = %d, max %d, limit %d; want 64, 256, 256", a.ServiceMemoryMB, a.ServiceMemoryMaxMB, a.ServiceMemoryLimitMB())
	}
	if a.Restarts != 3 || a.RequestedRestarts != 1 {
		t.Errorf("c1 restarts = %d, requested %d; want 3, 1", a.Restarts, a.RequestedRestarts)
	}
	if a.ExitCode == nil || *a.ExitCode != 137 || a.ExitedAt == nil || !a.ExitedAt.Equal(exit) {
		t.Errorf("c1 exit = %v at %v, want 137 at %v", a.ExitCode, a.ExitedAt, exit)
	}
	if a.Status != CopyHealthy {
		t.Errorf("c1 status = %q", a.Status)
	}
	// a copy with no ceiling is limited by its reservation; a pending one still reserves
	if b.CopyID != "c2" || b.Status != CopyStarting || b.ServiceMemoryLimitMB() != 64 || b.ExitCode != nil || b.ExitedAt != nil {
		t.Errorf("c2 = %+v", b)
	}
	if got.Total.CPUMHz != 200 || got.Total.MemoryMB != 128 {
		t.Errorf("total = %+v, want 200 MHz, 128 MB", got.Total)
	}
}

func TestReservationsLifecycleOfTheCopysVersion(t *testing.T) {
	d, _, nomad := newTestDeployer(t)
	// version 0 had a big prestart task; version 1 doesn't have it
	nomad.AddForeignJob(jobOf("app", "web", task("web", "", false), task("migrate", api.TaskLifecycleHookPrestart, false)))
	nomad.AddForeignJob(jobOf("app", "web", task("web", "", false)))
	nomad.SetCopies("app",
		running("old", "app", "web", 0, resources(map[string][3]int{"web": {100, 64, 0}, "migrate": {300, 200, 0}})),
		running("new", "app", "web", 1, resources(map[string][3]int{"web": {100, 64, 0}})))

	got, err := d.Reservations(context.Background())
	if err != nil {
		t.Fatalf("Reservations: %v", err)
	}
	byID := map[string]Reservation{}
	for _, c := range got.Copies {
		byID[c.CopyID] = c
	}
	// With version 0's own lifecycles the big prestart task replaces main;
	// read from the newest job it would have been added to it (400 MHz).
	if r := byID["old"]; r.CPUMHz != 300 || r.MemoryMB != 200 {
		t.Errorf("old copy = %d MHz, %d MB, want 300, 200", r.CPUMHz, r.MemoryMB)
	}
	if r := byID["new"]; r.CPUMHz != 100 || r.MemoryMB != 64 {
		t.Errorf("new copy = %d MHz, %d MB, want 100, 64", r.CPUMHz, r.MemoryMB)
	}
}

func TestReservationsEdges(t *testing.T) {
	ctx := context.Background()

	t.Run("a job Nomad no longer has counts every task as main", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies("gone", running("g1", "gone", "web", 0, resources(map[string][3]int{"web": {100, 64, 0}, "x": {50, 32, 0}})))
		got, err := d.Reservations(ctx)
		if err != nil || got.Total.CPUMHz != 150 || got.Total.MemoryMB != 96 {
			t.Errorf("total = %+v, err = %v", got.Total, err)
		}
	})

	t.Run("a copy with no resources reserves nothing and is still a copy", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddForeignJob(jobOf("bare", "web", task("web", "", false)))
		nomad.SetCopies("bare", running("b1", "bare", "web", 0, nil))
		got, err := d.Reservations(ctx)
		if err != nil || got.Total != (Reserved{Copies: 1}) {
			t.Errorf("total = %+v, err = %v", got.Total, err)
		}
	})

	t.Run("the lifecycle cache keeps only the versions of live copies", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddForeignJob(jobOf("a", "web", task("web", "", false)))
		nomad.SetCopies("a", running("a1", "a", "web", 0, resources(map[string][3]int{"web": {1, 1, 0}})))
		if _, err := d.Reservations(ctx); err != nil {
			t.Fatal(err)
		}
		nomad.SetCopies("a")
		if _, err := d.Reservations(ctx); err != nil {
			t.Fatal(err)
		}
		d.cache.mu.Lock()
		n := len(d.cache.tasks)
		d.cache.mu.Unlock()
		if n != 0 {
			t.Errorf("cached versions = %d, want 0", n)
		}
	})

	t.Run("Nomad down is ErrNomadUnavailable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		if _, err := d.Reservations(ctx); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("err = %v", err)
		}
	})
}
