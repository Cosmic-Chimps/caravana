package deploy

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// The status of a running copy when its health isn't decided in the stub: Nomad leaves
// DeploymentStatus nil until it decides, so only the job's deployment says whether that
// is "not in a rollout" or "not decided yet" (feature 027, the slowstart finding).
func TestCopyStatusInRollout(t *testing.T) {
	yes, no := true, false
	healthy := &api.AllocDeploymentStatus{Healthy: &yes}
	sick := &api.AllocDeploymentStatus{Healthy: &no}

	tests := []struct {
		name       string
		deployment string // status of the job's latest deployment, "" for none
		depVersion uint64
		copyStatus *api.AllocDeploymentStatus
		copyVer    uint64
		want       CopyStatus
	}{
		{"in a rollout, not decided", api.DeploymentStatusRunning, 3, nil, 3, CopyStarting},
		{"in a rollout, health not set", api.DeploymentStatusRunning, 3, &api.AllocDeploymentStatus{}, 3, CopyStarting},
		{"in a rollout, healthy", api.DeploymentStatusRunning, 3, healthy, 3, CopyHealthy},
		{"in a rollout, unhealthy", api.DeploymentStatusRunning, 3, sick, 3, CopyUnhealthy},
		{"no deployment and no status", "", 0, nil, 3, CopyHealthy},
		{"deployment over and no status", api.DeploymentStatusSuccessful, 3, nil, 3, CopyHealthy},
		{"deployment failed and no status", api.DeploymentStatusFailed, 3, nil, 3, CopyHealthy},
		{"an old version's copy while a new rollout runs", api.DeploymentStatusRunning, 4, nil, 3, CopyHealthy},
		{"an old version's decided copy while a new rollout runs", api.DeploymentStatusRunning, 4, healthy, 3, CopyHealthy},
		{"in a paused rollout, not decided", api.DeploymentStatusPaused, 3, nil, 3, CopyStarting},
		{"in a pending rollout, not decided", api.DeploymentStatusPending, 3, nil, 3, CopyStarting},
		{"in a blocked rollout, not decided", api.DeploymentStatusBlocked, 3, nil, 3, CopyStarting},
		{"in an unblocking rollout, not decided", api.DeploymentStatusUnblocking, 3, nil, 3, CopyStarting},
		{"in an initializing rollout, not decided", "initializing", 3, nil, 3, CopyStarting},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, _, nomad := newTestDeployer(t)
			if tc.deployment != "" {
				nomad.SetDeployment("blog", tc.depVersion, tc.deployment)
			}
			nomad.SetCopies("blog", api.AllocationListStub{
				ID: "c1", TaskGroup: "web", JobVersion: tc.copyVer, ClientStatus: "running",
				DeploymentStatus: tc.copyStatus,
			})

			// The one job's list.
			got, err := d.Copies(context.Background(), "blog")
			if err != nil || len(got) != 1 {
				t.Fatalf("Copies = %v, %v", got, err)
			}
			if got[0].Status != tc.want {
				t.Errorf("Copies: status = %s, want %s", got[0].Status, tc.want)
			}

			// Every job's list, with a second job whose rollout must not touch this one.
			nomad.SetDeployment("other", tc.copyVer, api.DeploymentStatusRunning)
			nomad.SetCopies("quiet", api.AllocationListStub{
				ID: "q1", TaskGroup: "web", JobVersion: tc.copyVer + 1, ClientStatus: "running"})
			all, err := d.AllCopies(context.Background())
			if err != nil || len(all["blog"]) != 1 {
				t.Fatalf("AllCopies = %v, %v", all, err)
			}
			if all["blog"][0].Status != tc.want {
				t.Errorf("AllCopies: status = %s, want %s", all["blog"][0].Status, tc.want)
			}
			if all["quiet"][0].Status != CopyHealthy {
				t.Errorf("AllCopies: another job's copy = %s, want healthy", all["quiet"][0].Status)
			}
		})
	}
}

// A pending copy is starting whatever the rollout says, and a finished one stays finished.
func TestCopyStatusIgnoresRolloutForOtherStates(t *testing.T) {
	active := rollouts{{"blog", 3}: true}
	for client, want := range map[string]CopyStatus{
		"pending": CopyStarting, "complete": CopyStopped, "failed": CopyFailed, "lost": CopyFailed, "unknown": CopyUnhealthy,
	} {
		got := copyOf(&api.AllocationListStub{JobID: "blog", JobVersion: 3, ClientStatus: client}, active)
		if got.Status != want {
			t.Errorf("client %s: status = %s, want %s", client, got.Status, want)
		}
	}
}

func TestStatusByChecks(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	check := func(status string) api.AllocCheckStatus { return api.AllocCheckStatus{Status: status} }

	tests := []struct {
		name   string
		copy   Copy
		checks api.AllocCheckStatuses
		want   CopyStatus
	}{
		{"all passing", Copy{Status: CopyHealthy, StartedAt: ago(time.Minute)}, api.AllocCheckStatuses{"a": check("success"), "b": check("success")}, CopyHealthy},
		{"one pending, started 30 s ago", Copy{Status: CopyHealthy, StartedAt: ago(30 * time.Second)}, api.AllocCheckStatuses{"a": check("success"), "b": check("pending")}, CopyStarting},
		{"failing, started 30 s ago", Copy{Status: CopyHealthy, StartedAt: ago(30 * time.Second)}, api.AllocCheckStatuses{"a": check("failure")}, CopyStarting},
		{"pending, started 5 min ago", Copy{Status: CopyHealthy, StartedAt: ago(5 * time.Minute)}, api.AllocCheckStatuses{"a": check("pending")}, CopyUnhealthy},
		{"failing, started long ago, restarted 30 s ago", Copy{Status: CopyHealthy, StartedAt: ago(48 * time.Hour), LastRestart: ago(30 * time.Second)}, api.AllocCheckStatuses{"a": check("failure")}, CopyStarting},
		{"failing, restarted 5 min ago", Copy{Status: CopyHealthy, StartedAt: ago(48 * time.Hour), LastRestart: ago(5 * time.Minute)}, api.AllocCheckStatuses{"a": check("failure")}, CopyUnhealthy},
		{"failing, no time known", Copy{Status: CopyHealthy}, api.AllocCheckStatuses{"a": check("failure")}, CopyStarting},
		{"no checks keeps the status", Copy{Status: CopyHealthy, StartedAt: ago(time.Minute)}, api.AllocCheckStatuses{}, CopyHealthy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusByChecks(tc.copy, tc.checks, now); got != tc.want {
				t.Errorf("status = %s, want %s", got, tc.want)
			}
		})
	}
}

// A copy that runs outside the rollout of its version (rescheduled, restarted by Caravana,
// back after its node returned) is judged by its service checks (the designer's addition).
func TestCopiesOutsideARolloutUseTheirChecks(t *testing.T) {
	yes := true
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	check := func(status string) api.AllocCheckStatuses {
		return api.AllocCheckStatuses{"c": {Status: status, Group: "web"}}
	}
	withCheck := func(job, group string) *api.Job {
		j := jobOf(job, group, task(group, "", false))
		j.TaskGroups[0].Services = []*api.Service{{Name: job + "-" + group, Checks: []api.ServiceCheck{{Type: "tcp"}}}}
		return j
	}
	copyStub := func(id, group string, started, restarted time.Duration, ds *api.AllocDeploymentStatus) api.AllocationListStub {
		ts := &api.TaskState{State: "running", StartedAt: now.Add(-started)}
		if restarted != 0 {
			ts.LastRestart = now.Add(-restarted)
		}
		return api.AllocationListStub{
			ID: id, TaskGroup: group, ClientStatus: "running", DesiredStatus: "run",
			DeploymentStatus: ds, TaskStates: map[string]*api.TaskState{group: ts},
		}
	}

	tests := []struct {
		name   string
		job    *api.Job
		stub   api.AllocationListStub
		checks api.AllocCheckStatuses
		later  time.Duration // how long after the setup the list is read
		want   CopyStatus
	}{
		{"rescheduled copy, check pending, started 30 s ago",
			withCheck("blog", "web"), copyStub("c1", "web", 30*time.Second, 0, nil), check("pending"), 0, CopyStarting},
		{"the same 5 min later",
			withCheck("blog", "web"), copyStub("c1", "web", 30*time.Second, 0, nil), check("pending"), 5 * time.Minute, CopyUnhealthy},
		{"rescheduled copy, every check passing",
			withCheck("blog", "web"), copyStub("c1", "web", 30*time.Second, 0, nil), check("success"), 0, CopyHealthy},
		{"restarted by Caravana: an old healthy status, a pending check",
			withCheck("blog", "web"), copyStub("c1", "web", 48*time.Hour, 20*time.Second, &api.AllocDeploymentStatus{Healthy: &yes}), check("pending"), 0, CopyStarting},
		{"long running and its check now fails",
			withCheck("blog", "web"), copyStub("c1", "web", 48*time.Hour, 0, &api.AllocDeploymentStatus{Healthy: &yes}), check("failure"), 0, CopyUnhealthy},
		{"a service without checks is healthy once running",
			jobOf("blog", "web", task("web", "", false)), copyStub("c1", "web", 5*time.Second, 0, nil), check("pending"), 0, CopyHealthy},
		{"a job Nomad no longer has keeps today's rule",
			nil, copyStub("c1", "web", 5*time.Second, 0, nil), check("pending"), 0, CopyHealthy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, _, nomad := newTestDeployer(t)
			d.cache.now = func() time.Time { return now.Add(tc.later) }
			if tc.job != nil {
				nomad.AddForeignJob(tc.job)
			}
			nomad.SetCopies("blog", tc.stub)
			nomad.SetChecks("c1", tc.checks)

			got, err := d.Copies(context.Background(), "blog")
			if err != nil || len(got) != 1 {
				t.Fatalf("Copies = %v, %v", got, err)
			}
			if got[0].Status != tc.want {
				t.Errorf("Copies: status = %s, want %s", got[0].Status, tc.want)
			}
			all, err := d.AllCopies(context.Background())
			if err != nil || len(all["blog"]) != 1 {
				t.Fatalf("AllCopies = %v, %v", all, err)
			}
			if all["blog"][0].Status != tc.want {
				t.Errorf("AllCopies: status = %s, want %s", all["blog"][0].Status, tc.want)
			}
		})
	}

	t.Run("a copy inside its rollout isn't read by its checks", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		d.cache.now = func() time.Time { return now }
		nomad.AddForeignJob(withCheck("blog", "web"))
		nomad.SetDeployment("blog", 0, api.DeploymentStatusRunning)
		nomad.SetCopies("blog", copyStub("c1", "web", time.Minute, 0, &api.AllocDeploymentStatus{Healthy: &yes}))
		nomad.SetChecks("c1", check("pending"))
		got, err := d.Copies(context.Background(), "blog")
		if err != nil || len(got) != 1 || got[0].Status != CopyHealthy {
			t.Errorf("Copies = %+v, %v; want healthy (the rollout decided it)", got, err)
		}
	})

	t.Run("a copy whose checks can't be read keeps its status and the list still answers", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		d.cache.now = func() time.Time { return now }
		nomad.AddForeignJob(withCheck("blog", "web"))
		nomad.SetCopies("blog", copyStub("c1", "web", time.Minute, 0, nil), copyStub("c2", "web", time.Minute, 0, nil))
		nomad.SetChecks("c2", check("pending"))
		nomad.FailChecks(500)
		got, err := d.Copies(context.Background(), "blog")
		if err != nil || len(got) != 2 {
			t.Fatalf("Copies = %v, %v", got, err)
		}
		for _, c := range got {
			if c.Status != CopyHealthy {
				t.Errorf("copy %s = %s, want healthy (today's rule)", c.ID, c.Status)
			}
		}
	})
}

func TestCopyOfLineage(t *testing.T) {
	got := copyOf(&api.AllocationListStub{ClientStatus: "failed", NextAllocation: "n1",
		RescheduleTracker: &api.RescheduleTracker{Events: []*api.RescheduleEvent{{PrevAllocID: "p0"}, {PrevAllocID: "p1"}}}}, nil)
	if !got.Replaced || got.PreviousID != "p1" {
		t.Errorf("copy = replaced %v, previous %q; want true, p1", got.Replaced, got.PreviousID)
	}
	first := copyOf(&api.AllocationListStub{ClientStatus: "failed"}, nil)
	if first.Replaced || first.PreviousID != "" {
		t.Errorf("first copy = replaced %v, previous %q; want false, none", first.Replaced, first.PreviousID)
	}
}
