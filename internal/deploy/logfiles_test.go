package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/logline"
)

func TestParseLogFileName(t *testing.T) {
	tests := []struct {
		name       string
		task, file string
		stream     logline.Stream
		index      int
		ok         bool
	}{
		{"stdout", "web", "web.stdout.0", logline.Stdout, 0, true},
		{"stderr, a high index", "web", "web.stderr.12", logline.Stderr, 12, true},
		{"fifo", "web", ".web.stdout.fifo", "", 0, false},
		{"another task", "web", "names.stdout.0", "", 0, false},
		{"a longer task name", "web", "web2.stdout.0", "", 0, false},
		{"a task with a dash", "my-web", "my-web.stdout.3", logline.Stdout, 3, true},
		{"no index", "web", "web.stdout", "", 0, false},
		{"empty index", "web", "web.stdout.", "", 0, false},
		{"a signed index", "web", "web.stdout.-1", "", 0, false},
		{"a word for an index", "web", "web.stdout.fifo", "", 0, false},
		{"another stream", "web", "web.stdin.0", "", 0, false},
		{"a suffix", "web", "web.stdout.0.gz", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream, index, ok := parseLogFileName(tt.task, tt.file)
			if stream != tt.stream || index != tt.index || ok != tt.ok {
				t.Errorf("parseLogFileName(%q, %q) = %q, %d, %v; want %q, %d, %v",
					tt.task, tt.file, stream, index, ok, tt.stream, tt.index, tt.ok)
			}
		})
	}
}

func TestValidLogPath(t *testing.T) {
	for path, want := range map[string]bool{
		"alloc/logs/web.stdout.0":            true,
		"alloc/logs/":                        false,
		"alloc/logs/../../etc/passwd":        false,
		"alloc/logs/..":                      false,
		"alloc/logs/sub/web.stdout.0":        false,
		"alloc/data/caravana/x.result":       false,
		"/alloc/logs/web.stdout.0":           false,
		"web/secrets/destination.env":        false,
		"alloc/logs/web.stdout.0\x00":        false,
		`alloc/logs/..\..\web.stdout.0`:      false,
		"alloc/logsx/web.stdout.0":           false,
		"alloc/logs/web.stdout.0/../../..":   false,
		"caravana-volumes/local/request.env": false,
	} {
		if got := validLogPath(path); got != want {
			t.Errorf("validLogPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// logCopy sets one running copy c1 of service web and gives it files.
func logCopy(t *testing.T) (*Deployer, *deploytest.Nomad) {
	t.Helper()
	d, _, nomad := newTestDeployer(t)
	nomad.SetCopies("blog", api.AllocationListStub{ID: "c1", ClientStatus: "running", TaskGroup: "web", JobVersion: 1})
	return d, nomad
}

func TestLogFiles(t *testing.T) {
	d, nomad := logCopy(t)
	t0 := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	nomad.AddLogFile("c1", "stderr", 0, []string{"e0"}, t0)
	nomad.AddLogFile("c1", "stdout", 4, []string{"newest"}, t0.Add(time.Hour))
	nomad.AddLogFile("c1", "stdout", 3, []string{"older", "line two"}, t0)

	got, err := d.LogFiles(context.Background(), "c1", "web")
	if err != nil {
		t.Fatalf("LogFiles: %v", err)
	}
	// The fake's directory also holds .web.<stream>.fifo and names.<stream>.0,
	// which must not show: stdout 3 and 4, then stderr 0.
	want := []LogFile{
		{Stream: logline.Stdout, Index: 3, Size: int64(len("older\nline two\n")), ModTime: t0, Path: "alloc/logs/web.stdout.3"},
		{Stream: logline.Stdout, Index: 4, Size: int64(len("newest\n")), ModTime: t0.Add(time.Hour), Path: "alloc/logs/web.stdout.4"},
		{Stream: logline.Stderr, Index: 0, Size: int64(len("e0\n")), ModTime: t0, Path: "alloc/logs/web.stderr.0"},
	}
	if len(got) != len(want) {
		t.Fatalf("LogFiles = %+v, want %+v", got, want)
	}
	for i := range want {
		g := got[i]
		if g.Stream != want[i].Stream || g.Index != want[i].Index || g.Size != want[i].Size ||
			!g.ModTime.Equal(want[i].ModTime) || g.Path != want[i].Path {
			t.Errorf("LogFiles[%d] = %+v, want %+v", i, g, want[i])
		}
	}

	// The sidecar's task is its own: asking for it lists only its files.
	side, err := d.LogFiles(context.Background(), "c1", "names")
	if err != nil || len(side) != 2 {
		t.Errorf("LogFiles(names) = %+v, %v; want its two empty files", side, err)
	}
}

func TestLogFilesNoOutputYet(t *testing.T) {
	d, _ := logCopy(t)
	got, err := d.LogFiles(context.Background(), "c1", "web")
	if err != nil || len(got) != 0 {
		t.Errorf("LogFiles of a copy with no output = %+v, %v; want none and nil", got, err)
	}
}

func TestLogFilesErrors(t *testing.T) {
	d, nomad := logCopy(t)
	nomad.AddLogFile("c1", "stdout", 0, []string{"x"}, time.Now())

	if _, err := d.LogFiles(context.Background(), "nope", "web"); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("LogFiles of an unknown copy: %v, want ErrCopyNotFound", err)
	}

	nomad.FailFS(403)
	_, err := d.LogFiles(context.Background(), "c1", "web")
	if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "read-fs") ||
		!strings.Contains(err.Error(), "installer") {
		t.Errorf("LogFiles without read-fs: %v, want ErrNomadUnavailable naming read-fs and the installer", err)
	}
	nomad.FailFS(0)

	nomad.Down()
	if _, err := d.LogFiles(context.Background(), "c1", "web"); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("LogFiles with Nomad down: %v, want ErrNomadUnavailable", err)
	}
}

func TestReadLogAt(t *testing.T) {
	d, nomad := logCopy(t)
	nomad.AddLogFile("c1", "stdout", 0, []string{"alpha", "bravo", "charlie"}, time.Now()) // 6+6+8 = 20 bytes
	const path = "alloc/logs/web.stdout.0"
	const whole = "alpha\nbravo\ncharlie\n"

	tests := []struct {
		name          string
		offset, limit int64
		want          string
	}{
		{"all of it", 0, 100, whole},
		{"exactly its size", 0, 20, whole},
		{"a window inside", 6, 6, "bravo\n"},
		{"a window starting mid line", 8, 5, "avo\nc"},
		{"one byte", 19, 1, "\n"},
		{"a limit across the end", 14, 1000, "arlie\n"},
		{"at the end", 20, 10, ""},
		{"past the end", 99999, 10, ""},
		{"no limit", 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.ReadLogAt(context.Background(), "c1", path, tt.offset, tt.limit)
			if err != nil || string(got) != tt.want {
				t.Errorf("ReadLogAt(%d, %d) = %q, %v; want %q", tt.offset, tt.limit, got, err, tt.want)
			}
		})
	}

	// A file that grows is read from where the caller left off.
	nomad.AppendLogLine("c1", "stdout", "delta")
	got, err := d.ReadLogAt(context.Background(), "c1", path, 20, 100)
	if err != nil || string(got) != "delta\n" {
		t.Errorf("ReadLogAt after an append = %q, %v; want %q", got, err, "delta\n")
	}
}

func TestReadLogAtErrors(t *testing.T) {
	d, nomad := logCopy(t)
	nomad.AddLogFile("c1", "stdout", 0, []string{"alpha"}, time.Now())
	nomad.AddLogFile("c1", "stdout", 1, []string{"bravo"}, time.Now())
	ctx := context.Background()

	// A purged file: Nomad answers readat with its message and a 200, so the
	// real answer comes from the stat that runs first.
	nomad.PurgeLogFile("c1", "stdout", 0)
	if _, err := d.ReadLogAt(ctx, "c1", "alloc/logs/web.stdout.0", 0, 10); !errors.Is(err, ErrLogRotated) {
		t.Errorf("ReadLogAt of a purged file: %v, want ErrLogRotated", err)
	}
	// A file that never was is the same.
	if _, err := d.ReadLogAt(ctx, "c1", "alloc/logs/web.stdout.7", 0, 10); !errors.Is(err, ErrLogRotated) {
		t.Errorf("ReadLogAt of a file that isn't there: %v, want ErrLogRotated", err)
	}
	// The one that remains still reads.
	if got, err := d.ReadLogAt(ctx, "c1", "alloc/logs/web.stdout.1", 0, 10); err != nil || string(got) != "bravo\n" {
		t.Errorf("ReadLogAt of the newer file = %q, %v", got, err)
	}

	if _, err := d.ReadLogAt(ctx, "nope", "alloc/logs/web.stdout.1", 0, 10); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("ReadLogAt of an unknown copy: %v, want ErrCopyNotFound", err)
	}

	for _, path := range []string{"alloc/data/x", "alloc/logs/../data/x", "/alloc/logs/web.stdout.1", ""} {
		_, err := d.ReadLogAt(ctx, "c1", path, 0, 10)
		if err == nil || errors.Is(err, ErrLogRotated) || errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("ReadLogAt(%q) = %v, want a refusal that is not a Nomad error", path, err)
		}
	}
	if _, err := d.ReadLogAt(ctx, "c1", "alloc/logs/web.stdout.1", -1, 10); err == nil {
		t.Error("ReadLogAt with a negative offset = nil, want an error")
	}

	nomad.FailFS(403)
	_, err := d.ReadLogAt(ctx, "c1", "alloc/logs/web.stdout.1", 0, 10)
	if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "read-fs") {
		t.Errorf("ReadLogAt without read-fs: %v, want ErrNomadUnavailable naming read-fs", err)
	}
	nomad.FailFS(0)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.ReadLogAt(cancelled, "c1", "alloc/logs/web.stdout.1", 0, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadLogAt with a cancelled context: %v, want context.Canceled", err)
	}
}

// A body that isn't the bytes asked for is Nomad's message, whatever its
// status: this is what a file purged between the stat and the read looks like.
func TestLogMessageError(t *testing.T) {
	d, _, _ := newTestDeployer(t)
	tests := []struct {
		msg  string
		want error
	}{
		{"stat /opt/nomad/data/alloc/c1/alloc/logs/web.stdout.0: no such file or directory", ErrLogRotated},
		{`Unknown allocation "c1"`, ErrCopyNotFound},
		{"Permission denied", ErrNomadUnavailable},
		{"something else broke", ErrNomadUnavailable},
	}
	for _, tt := range tests {
		err := d.logMessageError("read output of copy c1", "c1", tt.msg)
		if !errors.Is(err, tt.want) {
			t.Errorf("logMessageError(%q) = %v, want %v", tt.msg, err, tt.want)
		}
	}
	if err := d.logMessageError("x", "c1", "Permission denied"); !strings.Contains(err.Error(), "read-fs") {
		t.Errorf("permission message = %v, want it to name read-fs", err)
	}
	long := d.logMessageError("x", "c1", strings.Repeat("e", 5000))
	if len(long.Error()) > 400 {
		t.Errorf("a long message made an error of %d bytes, want it cut", len(long.Error()))
	}
}

func TestLogCopies(t *testing.T) {
	d, _, nomad := newTestDeployer(t)
	t0 := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	started := func(m int) map[string]*api.TaskState {
		return map[string]*api.TaskState{"web": {StartedAt: t0.Add(time.Duration(m) * time.Minute)}}
	}
	nomad.SetCopies("blog",
		api.AllocationListStub{ID: "old", ClientStatus: "complete", TaskGroup: "web", JobVersion: 1, TaskStates: started(0)},
		api.AllocationListStub{ID: "crashed", ClientStatus: "failed", TaskGroup: "web", JobVersion: 0, TaskStates: started(-30)},
		api.AllocationListStub{ID: "now", ClientStatus: "running", TaskGroup: "web", JobVersion: 2, TaskStates: started(10)},
		api.AllocationListStub{ID: "worker", ClientStatus: "running", TaskGroup: "worker", JobVersion: 2, TaskStates: started(10)},
	)
	nomad.SetCopies("other", api.AllocationListStub{ID: "elsewhere", ClientStatus: "running", TaskGroup: "web", JobVersion: 0})
	nomad.SetCopies("third", api.AllocationListStub{ID: "third-1", ClientStatus: "complete", TaskGroup: "web", JobVersion: 0})

	got, err := d.LogCopies(context.Background(), []string{"blog", "third"})
	if err != nil {
		t.Fatalf("LogCopies: %v", err)
	}
	var ids []string
	gone := map[string]bool{}
	for _, c := range got {
		ids = append(ids, c.ID)
		gone[c.ID] = c.Gone
	}
	// By service, newest version first: web v2, web v1, web v0 (blog), the
	// third job's, then worker. "elsewhere" belongs to a job not asked for.
	if want := []string{"now", "old", "crashed", "third-1", "worker"}; !slices.Equal(ids, want) {
		t.Errorf("LogCopies = %v, want %v", ids, want)
	}
	for id, want := range map[string]bool{"now": false, "worker": false, "old": true, "crashed": true, "third-1": true} {
		if gone[id] != want {
			t.Errorf("copy %s Gone = %v, want %v", id, gone[id], want)
		}
	}

	// Copies keeps hiding what LogCopies shows.
	shown, err := d.Copies(context.Background(), "blog")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range shown {
		if c.Gone {
			t.Errorf("Copies set Gone on %s", c.ID)
		}
		if c.ID == "crashed" {
			t.Errorf("Copies shows the old version's terminal copy %s", c.ID)
		}
	}

	before := nomad.AllocationRequests()
	if got, err := d.LogCopies(context.Background(), nil); err != nil || got != nil {
		t.Errorf("LogCopies(no jobs) = %v, %v", got, err)
	}
	if nomad.AllocationRequests() != before {
		t.Error("LogCopies with no jobs called Nomad")
	}

	nomad.Down()
	if _, err := d.LogCopies(context.Background(), []string{"blog"}); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("LogCopies with Nomad down: %v, want ErrNomadUnavailable", err)
	}
}

// The fake file system the service's tests will use: appends grow the newest
// file, requests are recorded, a purge makes a file go.
func TestFakeLogFileSystem(t *testing.T) {
	d, nomad := logCopy(t)
	fake := nomad
	nomad.AppendLogLine("c1", "stdout", "first") // makes index 0
	nomad.AddLogFile("c1", "stdout", 1, []string{"second"}, time.Now())
	nomad.AppendLogLine("c1", "stdout", "third") // goes to the newest: index 1

	if size, ok := fake.LogFileSize("c1", "stdout", 0); !ok || size != 6 {
		t.Errorf("file 0 = %d, %v; want 6 bytes", size, ok)
	}
	if size, ok := fake.LogFileSize("c1", "stdout", 1); !ok || size != int64(len("second\nthird\n")) {
		t.Errorf("file 1 = %d, %v; want %d bytes", size, ok, len("second\nthird\n"))
	}
	files, err := d.LogFiles(context.Background(), "c1", "web")
	if err != nil || len(files) != 2 || files[1].Size != int64(len("second\nthird\n")) {
		t.Errorf("LogFiles = %+v, %v", files, err)
	}

	nomad.PurgeLogFile("c1", "stdout", 0)
	if _, ok := fake.LogFileSize("c1", "stdout", 0); ok {
		t.Error("file 0 is still there after PurgeLogFile")
	}
	files, _ = d.LogFiles(context.Background(), "c1", "web")
	if len(files) != 1 || files[0].Index != 1 {
		t.Errorf("LogFiles after the purge = %+v, want only index 1", files)
	}
}
