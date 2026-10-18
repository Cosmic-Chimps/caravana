package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// sentinel is what a command prints in the tests that look for output where it
// must not be: in a row, a log line, a record, an error.
const (
	sentinel    = "s3cret-sentinel-0205"
	argSentinel = "s3cret-arg-0205"
)

func TestCheckCommand(t *testing.T) {
	long := strings.Repeat("a", maxCommandArgBytes+1)
	many := make([]string, maxCommandArgs+1)
	for i := range many {
		many[i] = "x"
	}
	tests := []struct {
		name    string
		command []string
		ok      bool
	}{
		{"a program", []string{"echo"}, true},
		{"program and arguments", []string{"/app/Umbral", "reset-credential"}, true},
		{"the largest item", []string{strings.Repeat("a", maxCommandArgBytes)}, true},
		{"the most items", many[:maxCommandArgs], true},
		{"none", nil, false},
		{"empty", []string{}, false},
		{"too many items", many, false},
		{"an item too long", []string{"echo", long}, false},
		{"a NUL byte", []string{"echo", "a\x00b"}, false},
		{"an empty program", []string{"", "x"}, false},
		{"a blank program", []string{"  "}, false},
		{"an empty argument is fine", []string{"echo", ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCommand(tt.command)
			var fe *app.FieldError
			switch {
			case tt.ok && err != nil:
				t.Errorf("checkCommand = %v, want ok", err)
			case !tt.ok && (!errors.As(err, &fe) || fe.Field != "command"):
				t.Errorf("checkCommand = %v, want a field error on command", err)
			}
		})
	}
}

func TestProgramOf(t *testing.T) {
	tests := []struct {
		command []string
		want    string
	}{
		{[]string{"echo", argSentinel}, "echo"},
		{[]string{"/app/Umbral", "reset-credential"}, "/app/Umbral"},
		{[]string{"mysql -p" + argSentinel + " -e x"}, "mysql"}, // a whole line in one item
		{[]string{strings.Repeat("a", 500)}, strings.Repeat("a", maxProgramBytes)},
		{[]string{"  spaced  out"}, "spaced"},
	}
	for _, tt := range tests {
		if got := programOf(tt.command); got != tt.want {
			t.Errorf("programOf(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

func TestRestartTargets(t *testing.T) {
	c := func(id string, v uint64, st deploy.CopyStatus) Copy {
		return Copy{ID: id, Version: v, Status: st, Terminal: st == deploy.CopyStopped || st == deploy.CopyFailed}
	}
	ids := func(cs []Copy) string {
		var out []string
		for _, x := range cs {
			out = append(out, x.ID)
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name   string
		copies []Copy
		want   string
	}{
		{"none", nil, ""},
		{"two healthy", []Copy{c("a", 3, deploy.CopyHealthy), c("b", 3, deploy.CopyHealthy)}, "a,b"},
		{"the newest healthy version, not the failed newer one", []Copy{
			c("new", 4, deploy.CopyFailed), c("a", 3, deploy.CopyHealthy), c("old", 2, deploy.CopyHealthy)}, "a"},
		{"an unhealthy copy of the live version goes too", []Copy{c("a", 3, deploy.CopyHealthy), c("b", 3, deploy.CopyUnhealthy)}, "a,b"},
		{"ended copies are skipped", []Copy{c("a", 3, deploy.CopyHealthy), c("b", 3, deploy.CopyStopped)}, "a"},
		{"nothing healthy, nothing live", []Copy{c("a", 3, deploy.CopyStarting), c("b", 3, deploy.CopyUnhealthy)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ids(restartTargets(tt.copies)); got != tt.want {
				t.Errorf("restartTargets = %q, want %q", got, tt.want)
			}
		})
	}
}

// actionLab is an env with whoami deployed (two copies, "c1" and "c2") and what
// a test of the actions needs from it.
type actionLab struct {
	*env
	t   *testing.T
	dir string
}

func (l *actionLab) copies() {
	l.nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 0},
		api.AllocationListStub{ID: "c2", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
}

// newActionLab deploys whoami healthy and gives it two running copies.
func newActionLab(t *testing.T) *actionLab {
	t.Helper()
	l := &actionLab{env: newEnv(t), t: t}
	deployHealthy(t, l.env, whoami())
	l.copies()
	l.nomad.SetRestartStop(0)
	return l
}

// syncBuffer is a log destination that can be read while the service writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newLoggedActionLab is newActionLab with the store's directory known and the
// service logging at debug level into the returned buffer.
func newLoggedActionLab(t *testing.T) (*actionLab, *syncBuffer) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	e.nomad.Succeed()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets, log)

	l := &actionLab{env: e, t: t, dir: dir}
	deployHealthy(t, e, whoami())
	l.copies()
	l.nomad.SetRestartStop(0)
	return l, logs
}

// rawStore is everything the SQLite file holds, as text: every value of every
// table, and the bytes of the database and its journal files, which is where a
// value that was written and deleted would still be.
func rawStore(t *testing.T, dir string) string {
	t.Helper()
	var out strings.Builder
	db, err := sql.Open("sqlite", filepath.Join(dir, "caravana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	names, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	names.Close()
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			for i, v := range vals {
				fmt.Fprintf(&out, "%s.%s=%s\n", table, cols[i], v.String)
			}
		}
		rows.Close()
	}
	files, _ := filepath.Glob(filepath.Join(dir, "caravana.db*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(b)
	}
	return out.String()
}

func TestRunCommand(t *testing.T) {
	ctx := context.Background()

	t.Run("output, exit code and what is recorded", func(t *testing.T) {
		l := newActionLab(t)
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "out", Stderr: "err", ExitCode: 3}
		})
		changes, stop := l.svc.Subscribe("whoami")
		defer stop()

		res, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"sh", "-c", "exit 3"}, app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode == nil || *res.ExitCode != 3 || res.Stdout != "out" || res.Stderr != "err" || res.TimedOut || res.FailedToStart {
			t.Errorf("result = %+v", res)
		}
		if got := l.nomad.Execs(); len(got) != 1 || got[0].Task != "web" || got[0].Copy != "c1" {
			t.Errorf("exec requests = %+v, want one in task web of copy c1", got)
		}

		acts, err := l.svc.ListActions(ctx, "whoami", 10)
		if err != nil || len(acts) != 1 {
			t.Fatalf("actions = %+v, %v", acts, err)
		}
		a := acts[0]
		if a.Action != store.ActionRun || a.Status != store.ActionSucceeded || a.Program != "sh" || a.ExitCode == nil || *a.ExitCode != 3 ||
			a.CopyID != "c1" || a.Service != "web" || a.Trigger != "manual" || a.FinishedAt == nil || a.Error != "" {
			t.Errorf("action = %+v", a)
		}

		sawActions := false
		for len(changes) > 0 {
			if c := <-changes; c.What == ChangeActions && c.AppID == "whoami" {
				sawActions = true
			}
		}
		if !sawActions {
			t.Error("no actions notice was published")
		}
	})

	t.Run("a timeout is a result, recorded as failed", func(t *testing.T) {
		l := newActionLab(t)
		l.svc.commandTimeout = 100 * time.Millisecond
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "partial", Hold: true}
		})
		res, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"sleep", "120"}, app.TriggerConsole)
		if err != nil || !res.TimedOut || res.ExitCode != nil {
			t.Fatalf("result = %+v, %v; want a timeout", res, err)
		}
		acts, _ := l.svc.ListActions(ctx, "whoami", 1)
		if len(acts) != 1 || acts[0].Status != store.ActionFailed || acts[0].ExitCode != nil ||
			!strings.Contains(acts[0].Error, "timed out") || acts[0].Trigger != "console" {
			t.Errorf("action = %+v", acts)
		}
	})

	t.Run("a command that doesn't exist is a result, not an error", func(t *testing.T) {
		l := newActionLab(t)
		l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Fail: `exec: "nope": executable file not found in $PATH`}
		})
		res, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"nope", argSentinel}, app.TriggerManual)
		if err != nil || !res.FailedToStart || res.ExitCode != nil || !strings.Contains(res.Stderr, "executable file not found") {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if strings.HasPrefix(res.Stderr, deploy.ErrExecFailed.Error()) {
			t.Errorf("stderr = %q: the sentinel error's own words leaked in", res.Stderr)
		}
		acts, _ := l.svc.ListActions(ctx, "whoami", 1)
		if len(acts) != 1 || acts[0].Status != store.ActionFailed || acts[0].Error != "failed to start" || acts[0].Program != "nope" {
			t.Errorf("action = %+v; want failed to start, with Nomad's text left out", acts)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		l := newActionLab(t)
		var fe *app.FieldError
		if _, err := l.svc.RunCommand(ctx, "whoami", "c1", nil, app.TriggerManual); !errors.As(err, &fe) || fe.Field != "command" {
			t.Errorf("no command = %v, want a field error on command", err)
		}
		if _, err := l.svc.RunCommand(ctx, "ghost", "c1", []string{"true"}, app.TriggerManual); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown app = %v, want ErrNotFound", err)
		}
		if _, err := l.svc.RunCommand(ctx, "whoami", "other-copy", []string{"true"}, app.TriggerManual); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another app's copy = %v, want ErrNotFound", err)
		}
		l.nomad.SetCopies("whoami",
			api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "complete", JobVersion: 0})
		if _, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"true"}, app.TriggerManual); !errors.Is(err, ErrCopyNotRunning) {
			t.Errorf("an ended copy = %v, want ErrCopyNotRunning", err)
		}
		if acts, _ := l.svc.ListActions(ctx, "whoami", 10); len(acts) != 0 {
			t.Errorf("refused commands left records: %+v", acts)
		}
		if got := l.nomad.Execs(); len(got) != 0 {
			t.Errorf("a refused command reached Nomad: %+v", got)
		}
	})

	t.Run("Nomad refusing the token is an error and a failed record", func(t *testing.T) {
		l := newActionLab(t)
		l.nomad.FailExec(403)
		if _, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"true"}, app.TriggerManual); !errors.Is(err, deploy.ErrNomadUnavailable) {
			t.Fatalf("RunCommand = %v, want ErrNomadUnavailable", err)
		}
		acts, _ := l.svc.ListActions(ctx, "whoami", 1)
		if len(acts) != 1 || acts[0].Status != store.ActionFailed || strings.Contains(acts[0].Error, "token") {
			t.Errorf("action = %+v, want failed, in words of ours", acts)
		}
	})

	t.Run("a command is no deploy and takes no lock", func(t *testing.T) {
		l := newActionLab(t)
		state := l.svc.stateOf("whoami")
		state.mu.Lock() // a deploy or a backup is running
		defer state.mu.Unlock()
		if _, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"true"}, app.TriggerManual); err != nil {
			t.Errorf("RunCommand while the app is locked = %v", err)
		}
		if n := deployCount(l.env, "whoami"); n != 1 {
			t.Errorf("deployments = %d, want the one", n)
		}
	})
}

// A command's output (and its arguments) is shown in its answer and nowhere
// else: not in a database row or file, a log line, a record or an error
// (feature 020.5, FR-002).
func TestRunCommandKeepsNoOutput(t *testing.T) {
	ctx := context.Background()
	l, logs := newLoggedActionLab(t)
	changes, stop := l.svc.Subscribe("")
	defer stop()

	l.nomad.OnExec(func(r deploytest.ExecRequest) deploytest.ExecReply {
		switch r.Command[0] {
		case "nope":
			return deploytest.ExecReply{Fail: "exec: nope: not found"}
		case "hang":
			return deploytest.ExecReply{Stdout: sentinel, Stderr: sentinel, Hold: true}
		}
		return deploytest.ExecReply{Stdout: sentinel + "\n", Stderr: sentinel + "-err\n", ExitCode: 7}
	})
	l.svc.commandTimeout = 150 * time.Millisecond

	res, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"echo", "--password=" + argSentinel}, app.TriggerConsole)
	if err != nil || !strings.Contains(res.Stdout, sentinel) || !strings.Contains(res.Stderr, sentinel) {
		t.Fatalf("the answer = %+v, %v; want the output, this once", res, err)
	}
	// A whole line in one item, a timeout, a missing program, a copy that
	// isn't there: every way a run ends.
	_, _ = l.svc.RunCommand(ctx, "whoami", "c2", []string{"echo -n " + argSentinel}, app.TriggerManual)
	if r, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"hang", argSentinel}, app.TriggerManual); err != nil || !r.TimedOut {
		t.Fatalf("hang = %+v, %v", r, err)
	}
	_, _ = l.svc.RunCommand(ctx, "whoami", "c1", []string{"nope", argSentinel}, app.TriggerManual)
	_, _ = l.svc.RunCommand(ctx, "whoami", "gone", []string{"echo", argSentinel}, app.TriggerManual)
	l.nomad.FailExec(403)
	_, _ = l.svc.RunCommand(ctx, "whoami", "c1", []string{"echo", argSentinel}, app.TriggerManual)
	l.nomad.FailExec(0)

	// The rest of what the service does about the app, with the logs open.
	if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.StartApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}

	acts, err := l.svc.ListActions(ctx, "whoami", 50)
	if err != nil {
		t.Fatal(err)
	}
	var runs int
	for _, a := range acts {
		if a.Action == store.ActionRun {
			runs++
			if a.Program != "echo" && a.Program != "hang" && a.Program != "nope" {
				t.Errorf("a record's program = %q: more than the first word", a.Program)
			}
		}
	}
	if runs != 5 {
		t.Errorf("run records = %d, want 5 (the unknown copy leaves none)", runs)
	}
	for what, text := range map[string]string{
		"the actions as the service lists them": fmt.Sprintf("%+v", acts),
		"the SQLite file and every row in it":   rawStore(t, l.dir),
		"the log":                               logs.String(),
		"the live notices":                      drainNotices(changes),
	} {
		for _, s := range []string{sentinel, argSentinel} {
			if strings.Contains(text, s) {
				t.Errorf("%s holds %q", what, s)
			}
		}
	}
}

// drainNotices reads every notice waiting on the channel, as text.
func drainNotices(ch <-chan Change) string {
	var out strings.Builder
	for {
		select {
		case c := <-ch:
			fmt.Fprintf(&out, "%+v\n", c)
		default:
			return out.String()
		}
	}
}

func TestStopAndStart(t *testing.T) {
	ctx := context.Background()
	l := newActionLab(t)
	before := l.nomad.Job("whoami")
	regs := l.nomad.Registrations()

	act, err := l.svc.StopApp(ctx, "whoami", app.TriggerConsole)
	if err != nil {
		t.Fatalf("StopApp = %v", err)
	}
	if act.Action != store.ActionStop || act.Status != store.ActionSucceeded || act.Trigger != "console" || act.FinishedAt == nil {
		t.Errorf("stop action = %+v", act)
	}
	rec, err := l.svc.GetApp(ctx, "whoami")
	if err != nil || rec.StoppedAt == nil {
		t.Fatalf("app after stop = %+v, %v; want StoppedAt set", rec, err)
	}
	if j := l.nomad.Job("whoami"); j == nil || j.Stop == nil || !*j.Stop || l.nomad.Purged("whoami") {
		t.Errorf("Nomad's job after stop = %+v, purged %v; want it kept, stopped", j, l.nomad.Purged("whoami"))
	}
	sum, err := l.svc.AppSummary(ctx, "whoami")
	if err != nil || sum.Status.State != StateStopped || sum.StoppedAt == nil {
		t.Errorf("summary = %+v, %v; want state stopped", sum.Status, err)
	}
	if list, err := l.svc.ListApps(ctx); err != nil || len(list) != 1 || list[0].Status.State != StateStopped {
		t.Errorf("ListApps = %+v, %v; want the app stopped", list, err)
	}
	deps := deployCount(l.env, "whoami")

	if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrAppStopped) {
		t.Errorf("a second StopApp = %v, want ErrAppStopped", err)
	}

	act, err = l.svc.StartApp(ctx, "whoami", app.TriggerManual)
	if err != nil {
		t.Fatalf("StartApp = %v", err)
	}
	if act.Action != store.ActionStart || act.Status != store.ActionSucceeded {
		t.Errorf("start action = %+v", act)
	}
	if rec, _ := l.svc.GetApp(ctx, "whoami"); rec.StoppedAt != nil {
		t.Errorf("app after start has StoppedAt %v", rec.StoppedAt)
	}
	after := l.nomad.Job("whoami")
	if after.Stop == nil || *after.Stop || after.Meta["caravana.deployment"] != before.Meta["caravana.deployment"] {
		t.Errorf("Nomad's job after start = stop %v, meta %v; want the same job running", after.Stop, after.Meta)
	}
	if got := l.nomad.Registrations() - regs; got != 1 {
		t.Errorf("registrations by start = %d, want 1", got)
	}
	if n := deployCount(l.env, "whoami"); n != deps {
		t.Errorf("deployments = %d after start, want %d: start is no deploy", n, deps)
	}
	if _, err := l.svc.StartApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrAppNotStopped) {
		t.Errorf("StartApp of a running app = %v, want ErrAppNotStopped", err)
	}

	acts, _ := l.svc.ListActions(ctx, "whoami", 10)
	if len(acts) != 2 || acts[0].Action != store.ActionStart || acts[1].Action != store.ActionStop {
		t.Errorf("actions = %+v, want start then stop", acts)
	}
}

func TestStopTakesBackupJobsToo(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	a := whoami()
	a.Services[0].Env = []app.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pg"}}
	a.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres, Keep: 2}
	deployHealthy(t, e, a)
	job := e.nomad.Job("whoami_web_backup")
	if job == nil {
		t.Fatal("no backup job was made")
	}
	if _, err := e.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if got := e.nomad.Job("whoami_web_backup"); got == nil || !*got.Stop {
		t.Errorf("backup job after stop = %+v, want it stopped", got)
	}
	if _, err := e.svc.StartApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if got := e.nomad.Job("whoami_web_backup"); got == nil || *got.Stop {
		t.Errorf("backup job after start = %+v, want it running", got)
	}
}

func TestActionsNeedADeployedApp(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "shop", Name: "Shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateAppOnly(ctx, whoami()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"shop", "whoami"} {
		want := ErrAppNotDefined
		if id == "whoami" {
			want = ErrNoLiveDeployment
		}
		if _, err := e.svc.StopApp(ctx, id, app.TriggerManual); !errors.Is(err, want) {
			t.Errorf("StopApp(%s) = %v, want %v", id, err, want)
		}
		if _, err := e.svc.StartApp(ctx, id, app.TriggerManual); !errors.Is(err, want) {
			t.Errorf("StartApp(%s) = %v, want %v", id, err, want)
		}
		if _, err := e.svc.RestartApp(ctx, id, app.TriggerManual); !errors.Is(err, want) {
			t.Errorf("RestartApp(%s) = %v, want %v", id, err, want)
		}
	}
	if _, err := e.svc.StopApp(ctx, "ghost", app.TriggerManual); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("StopApp(ghost) = %v, want ErrNotFound", err)
	}
	if e.nomad.Registrations() != 0 {
		t.Error("Nomad was asked to register something")
	}
	// A job Nomad lost is "no live deployment" too, not a Nomad error.
	l := newActionLab(t)
	if err := l.svc.deployer.Purge(ctx, "whoami", func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrNoLiveDeployment) {
		t.Errorf("StopApp of an app Nomad has no job for = %v, want ErrNoLiveDeployment", err)
	}
	if acts, _ := l.svc.ListActions(ctx, "whoami", 5); len(acts) != 1 || acts[0].Status != store.ActionFailed {
		t.Errorf("actions = %+v, want the one failed stop", acts)
	}
}

// Every way to deploy a stopped app is refused with ErrAppStopped, and records
// nothing (feature 020.5, FR-004).
func TestStoppedAppRefusesDeploys(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	setDestination(t, e)
	prod := deployHealthy(t, e, production())
	e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
	deployHealthy(t, e, staging())
	if _, err := e.svc.CreateWebhookToken(ctx, productionID); err != nil {
		t.Fatal(err)
	}
	deps := func() int { return deployCount(e, productionID) + deployCount(e, stagingID) }
	regs, n := e.nomad.Registrations(), deps()

	if _, err := e.svc.StopApp(ctx, productionID, app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.svc.GetApp(ctx, productionID)

	refusals := map[string]func() error{
		"Deploy": func() error { _, err := e.svc.Deploy(ctx, production(), app.TriggerManual); return err },
		"DeployIf": func() error {
			_, err := e.svc.DeployIf(ctx, production(), Condition{IfRevision: rec.Revision}, app.TriggerManual)
			return err
		},
		"Redeploy": func() error { _, err := e.svc.Redeploy(ctx, productionID, app.TriggerManual); return err },
		"RedeployVersion": func() error {
			_, err := e.svc.RedeployVersion(ctx, productionID, prod.Deployment.ID, app.TriggerManual)
			return err
		},
		"Promote into it": func() error {
			_, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
			return err
		},
		"PreviewPromotion": func() error { _, err := e.svc.PreviewPromotion(ctx, stagingID, "production"); return err },
		"webhook":          func() error { _, err := e.svc.WebhookRedeploy(ctx, productionID); return err },
		"BackUpNow":        func() error { _, err := e.svc.BackUpNow(ctx, productionID, "web"); return err },
		"Restore": func() error {
			if err := e.store.SaveBackup(ctx, store.BackupRecord{AppID: productionID, ID: "web-1", Service: "web",
				Engine: app.BackupPostgres, Status: store.BackupSucceeded, Object: "b/x.dump", StartedAt: time.Now()}); err != nil {
				return err
			}
			_, err := e.svc.Restore(ctx, productionID, "web-1")
			return err
		},
	}
	for name, call := range refusals {
		if err := call(); !errors.Is(err, ErrAppStopped) {
			t.Errorf("%s of a stopped app = %v, want ErrAppStopped", name, err)
		}
	}
	if got := e.nomad.Registrations(); got != regs {
		t.Errorf("registrations = %d, want %d: a refused deploy reached Nomad", got, regs)
	}
	if got := deps(); got != n {
		t.Errorf("deployments = %d, want %d: a refused deploy was recorded", got, n)
	}
	if after, _ := e.svc.GetApp(ctx, productionID); after.Revision != rec.Revision {
		t.Error("a refused deploy changed the stored app")
	}

	// Copying and promoting from a stopped app read records, so they work.
	if _, err := e.svc.PreviewPromotion(ctx, productionID, "staging"); err != nil {
		t.Errorf("promoting from a stopped app = %v, want it allowed", err)
	}

	// After Start, the same calls work.
	if _, err := e.svc.StartApp(ctx, productionID, app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Redeploy(ctx, productionID, app.TriggerManual); err != nil {
		t.Errorf("Redeploy after start = %v", err)
	}
}

// A deploy that was waiting for the app's lock when it was stopped doesn't
// register: it ends failed, saying why.
func TestDeployWaitingForTheLockWhenStopped(t *testing.T) {
	ctx := context.Background()
	l := newActionLab(t)
	state := l.svc.stateOf("whoami")
	state.mu.Lock() // held as a stop would hold it
	regs := l.nomad.Registrations()

	type result struct {
		rec store.DeploymentRecord
		err error
	}
	done := make(chan result, 1)
	go func() {
		rec, err := l.svc.Redeploy(ctx, "whoami", app.TriggerManual)
		done <- result{rec, err}
	}()
	waitFor(t, "the deploy's record", func() bool { return deployCount(l.env, "whoami") == 2 })
	if err := l.store.SetAppStopped(ctx, "whoami", new(time.Now())); err != nil {
		t.Fatal(err)
	}
	state.mu.Unlock()

	res := <-done
	var de *DeployError
	if !errors.As(res.err, &de) || !errors.Is(res.err, ErrAppStopped) {
		t.Fatalf("Redeploy = %v, want a DeployError wrapping ErrAppStopped", res.err)
	}
	if res.rec.Status != store.StatusFailed {
		t.Errorf("record = %+v, want failed", res.rec)
	}
	if l.nomad.Registrations() != regs {
		t.Error("the deploy registered a job for a stopped app")
	}
}

func TestSchedulerSkipsStoppedApps(t *testing.T) {
	ctx := context.Background()
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC) }
	l := newVolumeLab(t)
	last := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupVolume,
		Status: store.BackupSucceeded, StartedAt: day(14, 3, 0).Add(5 * time.Second)}
	if err := l.store.SaveBackup(ctx, last); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	l.clock.Set(day(15, 3, 20))
	noted := map[string]string{}
	l.svc.runDueVolumeBackups(ctx, noted)
	time.Sleep(30 * time.Millisecond)
	if rs := l.nomad.Restarts(); len(rs) != 0 {
		t.Errorf("restarts = %+v, want none for a stopped app", rs)
	}
	if backups, _ := l.store.ListBackups(ctx, "whoami", 0); len(backups) != 1 {
		t.Errorf("backups = %d, want only the old one: nothing recorded, nothing failed", len(backups))
	}
	if len(noted) != 0 {
		t.Errorf("noted = %v, want nothing said", noted)
	}
}

func TestStatusAndAttentionOfAStoppedApp(t *testing.T) {
	a := whoami()
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	rec := store.AppRecord{App: a, StoppedAt: &at}
	ended := []Copy{{ID: "c1", Service: "web", Version: 0, Status: deploy.CopyFailed, Terminal: true, FinishedAt: &at},
		{ID: "c2", Service: "web", Version: 0, Status: deploy.CopyUnhealthy}}

	for _, nomadErr := range []error{nil, errors.New("nomad down")} {
		if got := statusOfRecord(rec, nil, ended, nomadErr).State; got != StateStopped {
			t.Errorf("state with nomadErr %v = %s, want stopped", nomadErr, got)
		}
	}
	rec.StoppedAt = nil
	if got := statusOfRecord(rec, nil, ended, nil).State; got == StateStopped {
		t.Error("a running app is stopped")
	}

	apps := []store.AppRecord{{App: a, StoppedAt: &at}}
	got := attentionOf(at.Add(time.Minute), apps, nil, map[string][]Copy{"whoami": ended}, nil, nil, nil, true)
	if len(got.Items) != 0 {
		t.Errorf("a stopped app's copies gave %+v, want nothing", got.Items)
	}
	apps[0].StoppedAt = nil
	got = attentionOf(at.Add(time.Minute), apps, nil, map[string][]Copy{"whoami": ended}, nil, nil, nil, true)
	if len(got.Items) == 0 {
		t.Error("the same copies of a running app gave nothing: the test proves nothing")
	}
}

func TestRestartApp(t *testing.T) {
	ctx := context.Background()

	t.Run("one copy at a time, recorded, lock held until the end", func(t *testing.T) {
		l := newActionLab(t)
		release := make(chan struct{})
		var mu sync.Mutex
		var seen []string
		l.nomad.OnRestart(func(copyID string) {
			mu.Lock()
			seen = append(seen, copyID)
			mu.Unlock()
			if copyID == "c1" {
				<-release // c1's restart is open: c2 must not be touched yet
			}
		})

		act, err := l.svc.RestartApp(ctx, "whoami", app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		if act.Action != store.ActionRestart || act.Status != store.ActionRunning || act.Trigger != "console" {
			t.Errorf("action = %+v, want running", act)
		}
		waitFor(t, "the first restart", func() bool { return len(l.nomad.Restarts()) == 1 })
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		if len(seen) != 1 {
			t.Errorf("restarted %v while the first was open, want one at a time", seen)
		}
		mu.Unlock()

		// The lock is held for the whole run.
		if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrAppBusy) {
			t.Errorf("StopApp during a restart = %v, want ErrAppBusy", err)
		}
		if _, err := l.svc.RestartApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrAppBusy) {
			t.Errorf("RestartApp during a restart = %v, want ErrAppBusy", err)
		}

		close(release)
		var final store.ActionRecord
		waitFor(t, "the restart to end", func() bool {
			acts, _ := l.store.ListActions(ctx, "whoami", 5)
			for _, a := range acts {
				if a.ID == act.ID && a.Status != store.ActionRunning {
					final = a
					return true
				}
			}
			return false
		})
		if final.Status != store.ActionSucceeded || final.Error != "" || final.FinishedAt == nil {
			t.Errorf("final = %+v", final)
		}
		rs := l.nomad.Restarts()
		if len(rs) != 2 || rs[0].Copy != "c1" || rs[1].Copy != "c2" || !rs[0].AllTasks {
			t.Errorf("restarts = %+v, want c1 then c2, all tasks", rs)
		}
		if deployCount(l.env, "whoami") != 1 {
			t.Error("a restart recorded a deployment")
		}
		// The lock is free again.
		waitFor(t, "the lock", func() bool {
			if !l.svc.stateOf("whoami").mu.TryLock() {
				return false
			}
			l.svc.stateOf("whoami").mu.Unlock()
			return true
		})
	})

	t.Run("a copy that doesn't come back fails the restart, naming it", func(t *testing.T) {
		l := newActionLab(t)
		l.svc.restartWait = 100 * time.Millisecond
		// The copy's task never shows a new start: the fake only adds events
		// for the restart, so end c1's task state to look stuck.
		l.nomad.OnRestart(func(copyID string) {
			stuck := api.AllocationListStub{ID: copyID, TaskGroup: "web", ClientStatus: "running", JobVersion: 0,
				TaskStates: map[string]*api.TaskState{"web": {State: "pending"}}}
			l.nomad.SetCopies("whoami", stuck)
		})
		act, err := l.svc.RestartApp(ctx, "whoami", app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		var final store.ActionRecord
		waitFor(t, "the restart to fail", func() bool {
			acts, _ := l.store.ListActions(ctx, "whoami", 5)
			for _, a := range acts {
				if a.ID == act.ID && a.Status != store.ActionRunning {
					final = a
					return true
				}
			}
			return false
		})
		if final.Status != store.ActionFailed || !strings.Contains(final.Error, "c1") || !strings.Contains(final.Error, "web") {
			t.Errorf("final = %+v, want failed naming copy c1", final)
		}
		if rs := l.nomad.Restarts(); len(rs) != 1 {
			t.Errorf("restarts = %+v, want the restart to stop at the first copy", rs)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		l := newActionLab(t)
		l.nomad.SetCopies("whoami")
		if _, err := l.svc.RestartApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrNoRunningCopy) {
			t.Errorf("no copies = %v, want ErrNoRunningCopy", err)
		}
		l.copies()
		if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.RestartApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrAppStopped) {
			t.Errorf("a stopped app = %v, want ErrAppStopped", err)
		}
		if len(l.nomad.Restarts()) != 0 {
			t.Error("a refused restart reached Nomad")
		}
	})
}

func TestStopStartRefuseWhenBusy(t *testing.T) {
	ctx := context.Background()
	l := newActionLab(t)
	state := l.svc.stateOf("whoami")
	state.mu.Lock() // a deploy or a volume run holds it
	stops := l.nomad.Stops("whoami")
	for name, call := range map[string]func() error{
		"StopApp":    func() error { _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); return err },
		"StartApp":   func() error { _, err := l.svc.StartApp(ctx, "whoami", app.TriggerManual); return err },
		"RestartApp": func() error { _, err := l.svc.RestartApp(ctx, "whoami", app.TriggerManual); return err },
	} {
		if err := call(); !errors.Is(err, ErrAppBusy) {
			t.Errorf("%s with the lock held = %v, want ErrAppBusy", name, err)
		}
	}
	state.mu.Unlock()
	if l.nomad.Stops("whoami") != stops {
		t.Error("a refused stop reached Nomad")
	}
	if acts, _ := l.svc.ListActions(ctx, "whoami", 5); len(acts) != 0 {
		t.Errorf("busy refusals left records: %+v", acts)
	}
	// A volume run holds the lock the same way (the claim and the lock are its).
	l2 := newVolumeLab(t)
	release, err := l2.svc.claimVolumeRun("whoami")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := l2.svc.StopApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrAppBusy) {
		t.Errorf("StopApp during a volume run = %v, want ErrAppBusy", err)
	}
}

func TestInterruptedActionsAreSettledAtStart(t *testing.T) {
	ctx := context.Background()
	l := newActionLab(t)
	if err := l.store.InsertAction(ctx, store.ActionRecord{ID: "act_old", AppID: "whoami", Action: store.ActionRestart,
		Trigger: "console", Status: store.ActionRunning, StartedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	// A new service on the same store is the next process.
	startService(t, l.store, l.registry, l.nomad, l.secrets)
	acts, err := l.store.ListActions(ctx, "whoami", 5)
	if err != nil || len(acts) != 1 {
		t.Fatalf("actions = %+v, %v", acts, err)
	}
	if a := acts[0]; a.Status != store.ActionFailed || !strings.Contains(a.Error, "restarted") || a.FinishedAt == nil {
		t.Errorf("action = %+v, want failed as interrupted", a)
	}
}

func TestDeleteAppDeletesItsActions(t *testing.T) {
	ctx := context.Background()
	l := newActionLab(t)
	if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if err := l.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatalf("DeleteApp of a stopped app = %v", err)
	}
	if acts, err := l.store.ListActions(ctx, "whoami", 5); err != nil || len(acts) != 0 {
		t.Errorf("actions after delete = %+v, %v", acts, err)
	}
}
