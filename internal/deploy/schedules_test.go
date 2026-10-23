package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

const (
	testRunJob  = "blog_run-aaaaaaaaaaaa"
	runDigest   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	runSentinel = "s3cret-sentinel-022-value"
)

// testRunJobOf is a run's job as far as this package cares: a batch job with
// one task named after the service. private adds the docker auth block the
// translator adds for a credentialed registry.
func testRunJobOf(id, image string, private bool) *api.Job {
	config := map[string]any{"image": image, "entrypoint": []string{"sh"}, "args": []string{"-c", "true"}}
	if private {
		config["auth"] = []map[string]any{{"username": "${CARAVANA_REGISTRY_USERNAME}", "password": "${CARAVANA_REGISTRY_PASSWORD}"}}
	}
	return &api.Job{
		ID: &id, Name: &id, Type: new("batch"),
		TaskGroups: []*api.TaskGroup{{
			Name:  new("web"),
			Tasks: []*api.Task{{Name: "web", Driver: "docker", Config: config}},
		}},
	}
}

func TestStartScheduleRun(t *testing.T) {
	ctx := context.Background()

	t.Run("writes the variable, then registers the job", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		secrets := map[string]string{"DB_PASSWORD": runSentinel}
		if err := d.StartScheduleRun(ctx, testRunJobOf(testRunJob, "busybox@sha256:"+runDigest, false), secrets); err != nil {
			t.Fatal(err)
		}
		if got := nomad.Variable("nomad/jobs/" + testRunJob); len(got) != 1 || got["DB_PASSWORD"] != runSentinel {
			t.Errorf("variable = %v, want the secret set alone", got)
		}
		if nomad.Job(testRunJob) == nil || nomad.Registrations() != 1 {
			t.Errorf("job = %v, registrations = %d; want it registered once", nomad.Job(testRunJob), nomad.Registrations())
		}
		if len(secrets) != 1 {
			t.Errorf("the caller's secrets were changed: %v", secrets)
		}
	})

	t.Run("a run with nothing to hold gets a marker", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.StartScheduleRun(ctx, testRunJobOf(testRunJob, "busybox@sha256:"+runDigest, false), nil); err != nil {
			t.Fatal(err)
		}
		if got := nomad.Variable("nomad/jobs/" + testRunJob); got[runMarkerItem] != testRunJob || len(got) != 1 {
			t.Errorf("variable = %v, want only the marker (Nomad refuses an empty variable)", got)
		}
	})

	t.Run("a private image adds the credentials stored for its registry", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveRegistrySecret(ctx, "registry.example.com:5000", "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		job := testRunJobOf(testRunJob, "registry.example.com:5000/team/app@sha256:"+runDigest, true)
		if err := d.StartScheduleRun(ctx, job, map[string]string{"A": "1"}); err != nil {
			t.Fatal(err)
		}
		got := nomad.Variable("nomad/jobs/" + testRunJob)
		if got["A"] != "1" || got["CARAVANA_REGISTRY_USERNAME"] != "deploy" || got["CARAVANA_REGISTRY_PASSWORD"] != registrySecret {
			t.Errorf("variable items = %v, want the secret and the pull credentials", keysOf(got))
		}
		if strings.Contains(job.TaskGroups[0].Tasks[0].Config["auth"].([]map[string]any)[0]["password"].(string), registrySecret) {
			t.Error("the job holds the password")
		}
	})

	t.Run("a public image gets no credentials even when the registry has some", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.SaveRegistrySecret(ctx, "ghcr.io", "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		if err := d.StartScheduleRun(ctx, testRunJobOf(testRunJob, "ghcr.io/team/app@sha256:"+runDigest, false), map[string]string{"A": "1"}); err != nil {
			t.Fatal(err)
		}
		if got := nomad.Variable("nomad/jobs/" + testRunJob); len(got) != 1 {
			t.Errorf("variable items = %v, want only A", keysOf(got))
		}
	})

	t.Run("Nomad refusing the job is a RunStartError, and the variable is taken back", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.RejectNext(400, "invalid job: "+runSentinel)
		err := d.StartScheduleRun(ctx, testRunJobOf(testRunJob, "busybox@sha256:"+runDigest, false), map[string]string{"A": runSentinel})
		var start *RunStartError
		if !errors.As(err, &start) {
			t.Fatalf("error = %v, want a *RunStartError", err)
		}
		if start.Reason != "Nomad refused the job" || !errors.Is(err, ErrNomadRejected) {
			t.Errorf("reason = %q, is ErrNomadRejected = %v", start.Reason, errors.Is(err, ErrNomadRejected))
		}
		if nomad.Variable("nomad/jobs/"+testRunJob) != nil {
			t.Error("the variable with the secrets was left behind")
		}
		if nomad.Job(testRunJob) != nil {
			t.Error("a refused job is in Nomad")
		}
	})

	t.Run("a Nomad that can't be asked is not a RunStartError", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		err := d.StartScheduleRun(ctx, testRunJobOf(testRunJob, "busybox@sha256:"+runDigest, false), map[string]string{"A": "1"})
		var start *RunStartError
		if errors.As(err, &start) || !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("error = %v, want ErrNomadUnavailable and no RunStartError", err)
		}
	})

	t.Run("a job with no ID is refused before Nomad is asked", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.StartScheduleRun(ctx, &api.Job{}, nil); err == nil {
			t.Error("a job with no ID started")
		}
		if len(nomad.VariablePaths()) != 0 {
			t.Errorf("variables = %v, want none", nomad.VariablePaths())
		}
	})
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// registerRun registers a run's job in the fake and returns the deployer.
func registerRun(t *testing.T) (*Deployer, *deploytest.Nomad) {
	t.Helper()
	d, _, nomad := newTestDeployer(t)
	if err := d.StartScheduleRun(context.Background(), testRunJobOf(testRunJob, "busybox@sha256:"+runDigest, false), nil); err != nil {
		t.Fatal(err)
	}
	return d, nomad
}

func TestScheduleRunState(t *testing.T) {
	ctx := context.Background()

	t.Run("a job Nomad hasn't placed yet", func(t *testing.T) {
		d, _ := registerRun(t)
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if err != nil || !st.Found || st.Terminal || st.AllocID != "" || st.ExitCode != nil || st.TaskStartedAt != nil {
			t.Errorf("state = %+v, %v; want found, not terminal, nothing else", st, err)
		}
	})

	t.Run("pending", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.SetCopies(testRunJob, api.AllocationListStub{ID: "a1", TaskGroup: "web", ClientStatus: "pending", DesiredStatus: "run"})
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if err != nil || st.AllocID != "a1" || st.ClientStatus != "pending" || st.DesiredStatus != "run" || st.Terminal || st.TaskStartedAt != nil {
			t.Errorf("state = %+v, %v", st, err)
		}
	})

	t.Run("running", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.StartRun(testRunJob)
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if err != nil || st.ClientStatus != "running" || st.Terminal || st.TaskStartedAt == nil || st.ExitCode != nil || st.StartFailed {
			t.Errorf("state = %+v, %v; want running, started, no exit code", st, err)
		}
	})

	t.Run("exit 0", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.FinishRunExit(testRunJob, 0)
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if err != nil || st.ClientStatus != "complete" || !st.Terminal || st.ExitCode == nil || *st.ExitCode != 0 || st.OOMKilled || st.TaskStartedAt == nil {
			t.Errorf("state = %+v, %v; want complete with exit code 0", st, err)
		}
	})

	t.Run("exit 3", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.FinishRunExit(testRunJob, 3)
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if err != nil || st.ClientStatus != "failed" || !st.Terminal || st.ExitCode == nil || *st.ExitCode != 3 || st.OOMKilled {
			t.Errorf("state = %+v, %v; want failed with exit code 3", st, err)
		}
	})

	t.Run("out of memory", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.OOMRun(testRunJob)
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if err != nil || !st.Terminal || !st.OOMKilled || st.ExitCode == nil || *st.ExitCode != 137 {
			t.Errorf("state = %+v, %v; want killed for memory, exit 137", st, err)
		}
	})

	t.Run("a task that never started", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.FailRunStart(testRunJob)
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if err != nil || !st.Terminal || !st.StartFailed || st.TaskStartedAt != nil || st.ExitCode != nil {
			t.Errorf("state = %+v, %v; want a start failure with no start time or exit code", st, err)
		}
	})

	t.Run("the newest Terminated event counts", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.SetCopies(testRunJob, api.AllocationListStub{ID: "a1", ClientStatus: "complete", TaskStates: map[string]*api.TaskState{
			"web": {State: "dead", Events: []*api.TaskEvent{
				{Type: "Terminated", Details: map[string]string{"exit_code": "1"}},
				{Type: "Restarting"},
				{Type: "Terminated", Details: map[string]string{"exit_code": "0"}},
				{Type: "Killed"},
			}},
		}})
		if st, _ := d.ScheduleRunState(ctx, testRunJob, "web"); st.ExitCode == nil || *st.ExitCode != 0 {
			t.Errorf("exit code = %v, want the newest Terminated's 0", st.ExitCode)
		}
	})

	t.Run("the other tasks' events are not the service's", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.SetCopies(testRunJob, api.AllocationListStub{ID: "a1", ClientStatus: "failed", TaskStates: map[string]*api.TaskState{
			"web":   {State: "dead", Events: []*api.TaskEvent{{Type: "Terminated", Details: map[string]string{"exit_code": "2", "oom_killed": "false"}}}},
			"names": {State: "dead", Events: []*api.TaskEvent{{Type: "Terminated", Details: map[string]string{"exit_code": "9", "oom_killed": "true"}}}},
		}})
		st, _ := d.ScheduleRunState(ctx, testRunJob, "web")
		if st.ExitCode == nil || *st.ExitCode != 2 || st.OOMKilled {
			t.Errorf("state = %+v, want the service task's exit 2 and no OOM", st)
		}
	})

	t.Run("the newest allocation is the one read", func(t *testing.T) {
		d, nomad := registerRun(t)
		// SetCopies gives a copy it didn't have before a higher CreateIndex than
		// the ones it kept, so "newer" is the newest.
		nomad.SetCopies(testRunJob,
			api.AllocationListStub{ID: "old", ClientStatus: "failed"},
			api.AllocationListStub{ID: "new", ClientStatus: "running"})
		nomad.SetCopies(testRunJob,
			api.AllocationListStub{ID: "old", ClientStatus: "failed"},
			api.AllocationListStub{ID: "new", ClientStatus: "running"},
			api.AllocationListStub{ID: "newer", ClientStatus: "pending"})
		if st, _ := d.ScheduleRunState(ctx, testRunJob, "web"); st.AllocID != "newer" {
			t.Errorf("AllocID = %q, want newer", st.AllocID)
		}
	})

	t.Run("a purged job is ErrRunGone", func(t *testing.T) {
		d, _ := registerRun(t)
		if err := d.PurgeScheduleRun(ctx, testRunJob); err != nil {
			t.Fatal(err)
		}
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if !errors.Is(err, ErrRunGone) || st.Found {
			t.Errorf("state = %+v, %v; want ErrRunGone", st, err)
		}
	})

	t.Run("a job that was never registered is ErrRunGone", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if _, err := d.ScheduleRunState(ctx, "blog_run-bbbbbbbbbbbb", "web"); !errors.Is(err, ErrRunGone) {
			t.Errorf("error = %v, want ErrRunGone", err)
		}
	})

	t.Run("a Nomad that is down is ErrNomadUnavailable", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.Down()
		if _, err := d.ScheduleRunState(ctx, testRunJob, "web"); !errors.Is(err, ErrNomadUnavailable) || errors.Is(err, ErrRunGone) {
			t.Errorf("error = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestScheduleRunOutput(t *testing.T) {
	ctx := context.Background()

	t.Run("both streams, not cut", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.FinishRunExit(testRunJob, 0)
		nomad.SetRunLogs(testRunJob, []byte("one\ntwo\n"), []byte("warning\n"))
		st, _ := d.ScheduleRunState(ctx, testRunJob, "web")
		stdout, stderr, outCut, errCut, err := d.ScheduleRunOutput(ctx, st.AllocID, "web")
		if err != nil || string(stdout) != "one\ntwo\n" || string(stderr) != "warning\n" || outCut || errCut {
			t.Errorf("output = %q, %q, cut %v %v, %v", stdout, stderr, outCut, errCut, err)
		}
	})

	t.Run("a stream over 64 KiB keeps the end and says it was cut", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.FinishRunExit(testRunJob, 1)
		var big strings.Builder
		for i := 0; big.Len() < 100<<10; i++ {
			big.WriteString("line " + strings.Repeat("x", 90) + "\n")
		}
		nomad.SetRunLogs(testRunJob, []byte(big.String()), []byte("short\n"))
		st, _ := d.ScheduleRunState(ctx, testRunJob, "web")
		stdout, stderr, outCut, errCut, err := d.ScheduleRunOutput(ctx, st.AllocID, "web")
		if err != nil {
			t.Fatal(err)
		}
		if !outCut || errCut {
			t.Errorf("cut = %v %v, want stdout cut and stderr not", outCut, errCut)
		}
		if len(stdout) == 0 || len(stdout) > 64<<10+1 || !strings.HasSuffix(big.String(), string(stdout)) {
			t.Errorf("stdout is %d bytes; want the end of the output, at most 64 KiB", len(stdout))
		}
		if !strings.HasPrefix(string(stdout), "line ") {
			t.Errorf("stdout starts with half a line: %.20q", stdout)
		}
		if string(stderr) != "short\n" {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("a run that printed nothing, and one whose copy Nomad forgot, have no output", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.FinishRunExit(testRunJob, 0)
		st, _ := d.ScheduleRunState(ctx, testRunJob, "web")
		stdout, stderr, outCut, errCut, err := d.ScheduleRunOutput(ctx, st.AllocID, "web")
		if err != nil || len(stdout) != 0 || len(stderr) != 0 || outCut || errCut {
			t.Errorf("silent run: %q %q %v %v %v", stdout, stderr, outCut, errCut, err)
		}
		if stdout, stderr, _, _, err = d.ScheduleRunOutput(ctx, "no-such-copy", "web"); err != nil || len(stdout)+len(stderr) != 0 {
			t.Errorf("unknown copy: %q %q %v; want no output and no error", stdout, stderr, err)
		}
	})

	t.Run("a Nomad that is down is an error", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.FinishRunExit(testRunJob, 0)
		st, _ := d.ScheduleRunState(ctx, testRunJob, "web")
		nomad.Down()
		if _, _, _, _, err := d.ScheduleRunOutput(ctx, st.AllocID, "web"); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("error = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestStopAndPurgeScheduleRun(t *testing.T) {
	ctx := context.Background()

	t.Run("stop deregisters without purge and the copy is killed", func(t *testing.T) {
		d, nomad := registerRun(t)
		nomad.StartRun(testRunJob)
		if err := d.StopScheduleRun(ctx, testRunJob); err != nil {
			t.Fatal(err)
		}
		if nomad.Stops(testRunJob) != 1 || nomad.Purged(testRunJob) {
			t.Errorf("stops = %d, purged = %v; want one stop and no purge", nomad.Stops(testRunJob), nomad.Purged(testRunJob))
		}
		st, err := d.ScheduleRunState(ctx, testRunJob, "web")
		if err != nil || !st.Terminal || st.DesiredStatus != "stop" || st.ExitCode == nil || *st.ExitCode != 143 {
			t.Errorf("state after stop = %+v, %v; want ended, desired stop, exit 143", st, err)
		}
		if nomad.Variable("nomad/jobs/"+testRunJob) == nil {
			t.Error("a stop deleted the variable: only a purge does")
		}
	})

	t.Run("stopping a run Nomad doesn't have is ErrRunGone", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if err := d.StopScheduleRun(ctx, testRunJob); !errors.Is(err, ErrRunGone) {
			t.Errorf("error = %v, want ErrRunGone", err)
		}
	})

	t.Run("purge erases the job and deletes the variable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.StartScheduleRun(ctx, testRunJobOf(testRunJob, "busybox@sha256:"+runDigest, false), map[string]string{"A": runSentinel}); err != nil {
			t.Fatal(err)
		}
		nomad.FinishRunExit(testRunJob, 0)
		if err := d.PurgeScheduleRun(ctx, testRunJob); err != nil {
			t.Fatal(err)
		}
		if !nomad.Purged(testRunJob) || nomad.Job(testRunJob) != nil {
			t.Errorf("purged = %v, job = %v", nomad.Purged(testRunJob), nomad.Job(testRunJob))
		}
		if got := nomad.Variable("nomad/jobs/" + testRunJob); got != nil {
			t.Errorf("variable left behind: %v", keysOf(got))
		}
	})

	t.Run("purging twice, or a run that is gone, is done", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if err := d.PurgeScheduleRun(ctx, testRunJob); err != nil {
			t.Errorf("purge of a run Nomad doesn't have = %v", err)
		}
		if err := d.PurgeScheduleRun(ctx, testRunJob); err != nil {
			t.Errorf("second purge = %v", err)
		}
	})
}

func TestStopAppStopsItsRuns(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := whoami()
	if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil); err != nil {
		t.Fatal(err)
	}
	other := "other_run-cccccccccccc"
	if err := d.StartScheduleRun(ctx, testRunJobOf(testRunJob, "busybox@sha256:"+runDigest, false), nil); err != nil {
		t.Fatal(err)
	}
	if err := d.StartScheduleRun(ctx, testRunJobOf(other, "busybox@sha256:"+runDigest, false), nil); err != nil {
		t.Fatal(err)
	}
	nomad.StartRun(testRunJob)

	if err := d.StopApp(ctx, "whoami", nil, []string{testRunJob, "whoami_run-gone"}); err != nil {
		t.Fatalf("StopApp = %v (a run Nomad doesn't have is skipped)", err)
	}
	if nomad.Stops(testRunJob) != 1 || nomad.Purged(testRunJob) {
		t.Errorf("run job: stops = %d, purged = %v; want stopped, not purged", nomad.Stops(testRunJob), nomad.Purged(testRunJob))
	}
	if st, err := d.ScheduleRunState(ctx, testRunJob, "web"); err != nil || !st.Terminal {
		t.Errorf("the run after StopApp = %+v, %v; want ended and still readable", st, err)
	}
	if nomad.Stops(other) != 0 {
		t.Error("StopApp stopped a run job it wasn't given")
	}
	if j := nomad.Job("whoami"); j == nil || !*j.Stop {
		t.Error("the app's job wasn't stopped")
	}
}
