package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

func TestCappedWriter(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		writes    []string
		want      string
		truncated bool
	}{
		{"under the limit", 10, []string{"abc", "def"}, "abcdef", false},
		{"exactly the limit", 6, []string{"abc", "def"}, "abcdef", false},
		{"cut inside a write", 4, []string{"abc", "def"}, "abcd", true},
		{"cut at a write's start", 3, []string{"abc", "def"}, "abc", true},
		{"nothing kept at zero", 0, []string{"abc"}, "", true},
		{"empty writes", 3, []string{"", ""}, "", false},
		{"later writes keep being counted", 2, []string{"abc", "d", "e"}, "ab", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newCappedWriter(tt.limit)
			for _, s := range tt.writes {
				if n, err := w.Write([]byte(s)); err != nil || n != len(s) {
					t.Fatalf("Write(%q) = %d, %v; want all of it written", s, n, err)
				}
			}
			got, truncated := w.result()
			if string(got) != tt.want || truncated != tt.truncated {
				t.Errorf("result = %q, %v; want %q, %v", got, truncated, tt.want, tt.truncated)
			}
		})
	}
}

func execCopy(nomad *deploytest.Nomad, status string) string {
	nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: status, JobVersion: 1})
	return "c1"
}

func TestExec(t *testing.T) {
	ctx := context.Background()

	t.Run("output and exit code", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "out\n", Stderr: "err\n", ExitCode: 3}
		})
		res, err := d.Exec(ctx, id, "web", []string{"sh", "-c", "exit 3"}, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode == nil || *res.ExitCode != 3 || string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" ||
			res.StdoutTruncated || res.StderrTruncated || res.TimedOut {
			t.Errorf("result = %+v", res)
		}
		got := nomad.Execs()
		if len(got) != 1 || got[0].Copy != id || got[0].Task != "web" || got[0].TTY ||
			strings.Join(got[0].Command, "|") != "sh|-c|exit 3" {
			t.Errorf("exec requests = %+v; want one, in the service's task, without a terminal", got)
		}
	})

	t.Run("each stream is cut at the limit", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		big := strings.Repeat("x", 3*8192+5) // several frames
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: big, Stderr: "short"}
		})
		res, err := d.Exec(ctx, id, "web", []string{"gen"}, 10000)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Stdout) != 10000 || !res.StdoutTruncated {
			t.Errorf("stdout = %d bytes, truncated %v; want 10000, true", len(res.Stdout), res.StdoutTruncated)
		}
		if string(res.Stderr) != "short" || res.StderrTruncated {
			t.Errorf("stderr = %q, truncated %v", res.Stderr, res.StderrTruncated)
		}
	})

	t.Run("the caller's deadline is a timeout, not an error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "partial", Hold: true}
		})
		tctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		res, err := d.Exec(tctx, id, "web", []string{"sleep", "120"}, 1024)
		if err != nil {
			t.Fatalf("Exec = %v, want the timeout in the result", err)
		}
		if !res.TimedOut || res.ExitCode != nil {
			t.Errorf("result = %+v, want TimedOut with no exit code", res)
		}
	})

	t.Run("a cancelled context is its error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{Hold: true} })
		cctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(100*time.Millisecond, cancel)
		if _, err := d.Exec(cctx, id, "web", []string{"sleep", "120"}, 1024); !errors.Is(err, context.Canceled) {
			t.Errorf("Exec = %v, want context.Canceled", err)
		}
	})

	t.Run("a copy that isn't running", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "complete")
		if _, err := d.Exec(ctx, id, "web", []string{"true"}, 1024); !errors.Is(err, ErrCopyNotRunning) {
			t.Errorf("Exec = %v, want ErrCopyNotRunning", err)
		}
		if len(nomad.Execs()) != 0 {
			t.Error("a command was sent to a copy that isn't running")
		}
	})

	t.Run("an unknown copy", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if _, err := d.Exec(ctx, "nope", "web", []string{"true"}, 1024); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("Exec = %v, want ErrCopyNotFound", err)
		}
	})

	t.Run("a command Nomad can't start", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Fail: `exec: "nope": executable file not found in $PATH`}
		})
		_, err := d.Exec(ctx, id, "web", []string{"nope"}, 1024)
		if !errors.Is(err, ErrExecFailed) || !strings.Contains(err.Error(), "executable file not found") {
			t.Errorf("Exec = %v, want ErrExecFailed with Nomad's text", err)
		}
	})

	t.Run("a token without alloc-exec", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.FailExec(403)
		_, err := d.Exec(ctx, id, "web", []string{"true"}, 1024)
		if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "alloc-exec") {
			t.Errorf("Exec = %v, want ErrNomadUnavailable naming alloc-exec", err)
		}
	})
}

func TestStopAndStartApp(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := whoami()
	if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil); err != nil {
		t.Fatal(err)
	}
	registeredJob := *nomad.Job("whoami")
	backup := "whoami_db_backup"
	nomad.AddForeignJob(&api.Job{ID: &backup, Name: &backup, Type: new("batch"), Stop: new(false)})
	nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running"})

	if err := d.StopApp(ctx, "whoami", []string{backup, "whoami_gone_backup"}, nil); err != nil {
		t.Fatalf("StopApp = %v (a backup job Nomad doesn't have is skipped)", err)
	}
	if j := nomad.Job("whoami"); j == nil || j.Stop == nil || !*j.Stop {
		t.Fatalf("job after stop = %+v, want it kept with Stop set", j)
	}
	if j := nomad.Job(backup); j == nil || !*j.Stop {
		t.Errorf("backup job after stop = %+v, want it kept with Stop set", j)
	}
	if nomad.Purged("whoami") {
		t.Error("Stop purged the job")
	}
	if copies, err := d.Copies(ctx, "whoami"); err != nil || len(copies) != 1 || !copies[0].Terminal {
		t.Errorf("Copies after stop = %+v, %v; want the copy ended", copies, err)
	}
	// Stopping twice is fine.
	if err := d.StopApp(ctx, "whoami", nil, nil); err != nil {
		t.Errorf("second StopApp = %v", err)
	}

	before := nomad.Registrations()
	if err := d.StartApp(ctx, "whoami", []string{backup, "whoami_gone_backup"}); err != nil {
		t.Fatalf("StartApp = %v", err)
	}
	started := nomad.Job("whoami")
	if started.Stop == nil || *started.Stop {
		t.Errorf("job after start has Stop = %v", started.Stop)
	}
	if got := nomad.Registrations() - before; got != 2 {
		t.Errorf("registrations by start = %d, want 2 (the app and its backup job)", got)
	}
	// Same definition and images as before the stop, nothing resolved again.
	if started.Meta[metaDeployment] != registeredJob.Meta[metaDeployment] ||
		started.TaskGroups[0].Tasks[0].Config["image"] != registeredJob.TaskGroups[0].Tasks[0].Config["image"] {
		t.Errorf("start changed what runs: meta %v vs %v", started.Meta, registeredJob.Meta)
	}

	// Starting a job that isn't stopped changes nothing.
	before = nomad.Registrations()
	if err := d.StartApp(ctx, "whoami", nil); err != nil {
		t.Fatal(err)
	}
	if nomad.Registrations() != before {
		t.Error("starting a job that isn't stopped registered it again")
	}

	if err := d.StopApp(ctx, "ghost", nil, nil); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("StopApp of a missing job = %v, want ErrJobNotFound", err)
	}
	if err := d.StartApp(ctx, "ghost", nil); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("StartApp of a missing job = %v, want ErrJobNotFound", err)
	}
}

func TestWaitCopyRestarted(t *testing.T) {
	d, _, nomad := newTestDeployer(t)
	nomad.SetRestartStop(0)
	id := execCopy(nomad, "running")
	since := time.Now()

	t.Run("not restarted yet runs out the deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		if err := d.WaitCopyRestarted(ctx, id, "web", since, false); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("WaitCopyRestarted = %v, want DeadlineExceeded", err)
		}
	})

	if err := d.RestartCopy(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// The fake's restart events are on the stub; make the task read as running.
	stub := api.AllocationListStub{ID: id, TaskGroup: "web", ClientStatus: "running", JobVersion: 1}
	stub.TaskStates = map[string]*api.TaskState{"web": {State: "running", Events: []*api.TaskEvent{
		{Type: api.TaskStarted, Time: time.Now().Add(time.Second).UnixNano()},
	}}}
	nomad.SetCopies("whoami", stub)

	t.Run("started again", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := d.WaitCopyRestarted(ctx, id, "web", since, false); err != nil {
			t.Errorf("WaitCopyRestarted = %v", err)
		}
	})

	t.Run("a started event from before the restart doesn't count", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		if err := d.WaitCopyRestarted(ctx, id, "web", since.Add(time.Hour), false); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("WaitCopyRestarted = %v, want DeadlineExceeded", err)
		}
	})

	t.Run("waits for the checks", func(t *testing.T) {
		nomad.SetChecks(id, api.AllocCheckStatuses{"c": {ID: "c", Status: "pending"}})
		short, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		if err := d.WaitCopyRestarted(short, id, "web", since, true); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("with a pending check = %v, want DeadlineExceeded", err)
		}
		nomad.SetChecks(id, api.AllocCheckStatuses{"c": {ID: "c", Status: "success"}})
		ctx, cancel2 := context.WithTimeout(context.Background(), time.Second)
		defer cancel2()
		if err := d.WaitCopyRestarted(ctx, id, "web", since, true); err != nil {
			t.Errorf("with a passing check = %v", err)
		}
	})

	t.Run("an ended copy", func(t *testing.T) {
		nomad.SetCopies("whoami", api.AllocationListStub{ID: id, TaskGroup: "web", ClientStatus: "failed"})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := d.WaitCopyRestarted(ctx, id, "web", since, false); !errors.Is(err, ErrCopyNotRunning) {
			t.Errorf("WaitCopyRestarted = %v, want ErrCopyNotRunning", err)
		}
	})

	t.Run("a copy Nomad lost", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := d.WaitCopyRestarted(ctx, "nope", "web", since, false); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("WaitCopyRestarted = %v, want ErrCopyNotFound", err)
		}
	})
}
