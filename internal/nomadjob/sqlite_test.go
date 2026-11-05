package nomadjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// sqliteBackupCases is a service with a sqlite backup (feature 029): the
// database on one volume, a read-only config volume beside it. It lives here,
// not in apptest, like the volume backup cases.
func sqliteBackupCases() []goldenCase {
	fhodor := app.App{
		ID:     "fhodor",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{
			Name:    "web",
			Image:   "ghcr.io/acme/fhodor:1",
			Ports:   []app.Port{{Name: "http", ContainerPort: 3000}},
			Domains: []app.Domain{{Host: "fhodor.example.com", Port: "http"}},
			Health:  &app.HealthCheck{Path: "/"},
			Volumes: []app.Volume{
				{Name: "data", Path: "/data"},
				{Name: "config", Path: "/etc/fhodor", ReadOnly: true},
			},
			Backup: &app.Backup{Engine: app.BackupSQLite, File: "/data/db/app.sqlite3", Schedule: "0 4 * * *", Keep: 5},
		}},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"web": "ghcr.io/acme/fhodor@sha256:" + strings.Repeat("a", 64),
	}}
	return []goldenCase{{name: "sqlite-backup", app: fhodor, deployment: dep, settings: labSettings()}}
}

func TestSQLiteGoldenCaseExists(t *testing.T) {
	for _, tc := range goldenCases() {
		if tc.name == "sqlite-backup" {
			return
		}
	}
	t.Error("golden cases lack sqlite-backup")
}

// TestSQLiteTasks checks the two sidecars: only a service with a sqlite backup
// gets them, they come after caravana-volumes, and each has what it needs and
// nothing else.
func TestSQLiteTasks(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			job := translated(t, tc)
			for i, svc := range tc.app.Services {
				tg := job.TaskGroups[i]
				var names []string
				for _, task := range tg.Tasks {
					names = append(names, task.Name)
				}
				has := slices.Contains(names, app.SQLiteTaskName)
				if has != hasSQLiteBackup(svc) || slices.Contains(names, app.UploadTaskName) != hasSQLiteBackup(svc) {
					t.Errorf("service %s: tasks %v, sqlite backup = %v", svc.Name, names, hasSQLiteBackup(svc))
				}
			}
		})
	}

	tc := sqliteBackupCases()[0]
	tg := translated(t, tc).TaskGroups[0]
	if got := []string{tg.Tasks[0].Name, tg.Tasks[1].Name, tg.Tasks[2].Name, tg.Tasks[3].Name}; !slices.Equal(got, []string{"web", VolumeTaskName, app.SQLiteTaskName, app.UploadTaskName}) {
		t.Fatalf("tasks = %v, want the service, caravana-volumes, caravana-sqlite, caravana-upload", got)
	}
	sq, up := tg.Tasks[2], tg.Tasks[3]
	for _, task := range []*api.Task{sq, up} {
		if task.Lifecycle == nil || task.Lifecycle.Hook != "poststart" || !task.Lifecycle.Sidecar {
			t.Errorf("%s lifecycle = %+v, want a poststart sidecar", task.Name, task.Lifecycle)
		}
		if !reflect.DeepEqual(task.Config["entrypoint"], []string{"/bin/sh", "-c"}) || !reflect.DeepEqual(task.Config["args"], []string{IdleScript}) {
			t.Errorf("%s command = %v %v, want /bin/sh -c IdleScript", task.Name, task.Config["entrypoint"], task.Config["args"])
		}
		if task.KillTimeout == nil || task.KillTimeout.Seconds() != 1 {
			t.Errorf("%s kill timeout = %v, want 1s", task.Name, task.KillTimeout)
		}
		if len(task.Templates) != 0 || len(task.Env) != 0 {
			t.Errorf("%s has templates or env: the destination never reaches the job (research R3)", task.Name)
		}
	}
	if sq.Config["image"] != DefaultSQLiteImage || up.Config["image"] != DefaultBackupImage {
		t.Errorf("images = %v and %v, want the sqlite and the backup image", sq.Config["image"], up.Config["image"])
	}
	if r := sq.Resources; *r.CPU != 20 || *r.MemoryMB != 16 || *r.MemoryMaxMB != 256 {
		t.Errorf("caravana-sqlite resources = %d, %d, %d; want 20, 16, 256", *r.CPU, *r.MemoryMB, *r.MemoryMaxMB)
	}
	if r := up.Resources; *r.CPU != 20 || *r.MemoryMB != 32 || *r.MemoryMaxMB != 512 {
		t.Errorf("caravana-upload resources = %d, %d, %d; want 20, 32, 512", *r.CPU, *r.MemoryMB, *r.MemoryMaxMB)
	}

	// caravana-sqlite mounts the database's volume, writable, and only that
	// one; caravana-upload mounts none.
	if len(sq.VolumeMounts) != 1 {
		t.Fatalf("caravana-sqlite mounts = %d, want only the database's volume", len(sq.VolumeMounts))
	}
	if m := sq.VolumeMounts[0]; *m.Volume != "data" || *m.Destination != "/caravana-volumes/data" || *m.ReadOnly {
		t.Errorf("caravana-sqlite mount = %s at %s read-only %v, want data at /caravana-volumes/data, writable", *m.Volume, *m.Destination, *m.ReadOnly)
	}
	if len(up.VolumeMounts) != 0 {
		t.Errorf("caravana-upload mounts %d volumes, want none", len(up.VolumeMounts))
	}
	// The group asks for the database's volume writable, and keeps the
	// config volume read-only (nothing writes there).
	if tg.Volumes["data"].ReadOnly || !tg.Volumes["config"].ReadOnly {
		t.Errorf("group volumes: data read-only %v, config read-only %v; want false and true", tg.Volumes["data"].ReadOnly, tg.Volumes["config"].ReadOnly)
	}

	t.Run("the images come from the settings", func(t *testing.T) {
		s := labSettings()
		s.SQLiteImage = "ghcr.io/acme/sqlite@sha256:" + strings.Repeat("b", 64)
		s.BackupImage = "ghcr.io/acme/rclone@sha256:" + strings.Repeat("c", 64)
		res, err := Translate(tc.app, tc.deployment, s, nil)
		if err != nil {
			t.Fatal(err)
		}
		tasks := res.Job.TaskGroups[0].Tasks
		if tasks[2].Config["image"] != s.SQLiteImage || tasks[3].Config["image"] != s.BackupImage {
			t.Errorf("images = %v and %v", tasks[2].Config["image"], tasks[3].Config["image"])
		}
	})
	t.Run("an image with a tag is refused", func(t *testing.T) {
		s := labSettings()
		s.SQLiteImage = "alpine/sqlite:latest"
		_, err := Translate(tc.app, tc.deployment, s, nil)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "settings.sqliteImage" {
			t.Errorf("Translate = %v, want a field error on settings.sqliteImage", err)
		}
	})
	t.Run("the sidecars hold no destination or secret", func(t *testing.T) {
		// caravana-volumes' templates name the destination's keys (they are the
		// volume engine's); the sidecars have nothing of the kind.
		for _, task := range []*api.Task{sq, up} {
			b, err := json.Marshal(task)
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{"RCLONE_CONFIG_DEST", "SECRET_ACCESS_KEY", "nomadVar", "secrets/"} {
				if strings.Contains(string(b), bad) {
					t.Errorf("%s mentions %s", task.Name, bad)
				}
			}
		}
	})
}

// TestSQLiteScriptsAvoidNomadInterpolation: Nomad interpolates "${...}" in task
// config. The idle script is in the job; the others follow the same style.
func TestSQLiteScriptsAvoidNomadInterpolation(t *testing.T) {
	for name, text := range map[string]string{
		"IdleScript":           IdleScript,
		"SQLiteCopyScript":     SQLiteCopyScript,
		"SQLiteUploadScript":   SQLiteUploadScript,
		"SQLiteDownloadScript": SQLiteDownloadScript,
		"SQLiteStageScript":    SQLiteStageScript,
		"VolumeScript":         VolumeScript,
	} {
		if strings.Contains(text, "${") {
			t.Errorf("%s contains \"${\", which Nomad interpolates", name)
		}
	}
}

func TestSQLitePaths(t *testing.T) {
	tests := map[string]struct{ got, want string }{
		"copy":    {SQLiteCopyPath("web-3"), "/alloc/data/caravana/sqlite/web-3.db"},
		"db":      {SQLiteDatabasePath("data", "db/app.sqlite3"), "/caravana-volumes/data/db/app.sqlite3"},
		"stage":   {SQLiteStagePath("data", "web-3"), "/caravana-volumes/data/.caravana-restore/web-3.db"},
		"request": {SQLiteRestoreFile("data", "db/app.sqlite3"), "data/db/app.sqlite3"},
		"object":  {SQLiteObject("b/fhodor/web", "1791"), "b/fhodor/web/1791.sqlite.gz"},
	}
	for name, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", name, tt.got, tt.want)
		}
	}
	if got, want := SQLiteCommand("echo $1", "a b", "c"), []string{"/bin/sh", "-c", "echo $1", "sh", "a b", "c"}; !slices.Equal(got, want) {
		t.Errorf("SQLiteCommand = %q, want %q", got, want)
	}
}

func TestParseSQLiteOutput(t *testing.T) {
	tests := []struct {
		name, text string
		want       SQLiteOutput
	}{
		{"a copy", "caravana-sqlite-copy bytes=4096\n", SQLiteOutput{Bytes: 4096}},
		{"a download", "caravana-sqlite-download bytes=12288\n", SQLiteOutput{Bytes: 12288}},
		{"an upload with retention", "caravana-backup key=b/fhodor/web/1791.sqlite.gz bytes=900\n" +
			"caravana-expired 1700.sqlite.gz\ncaravana-expired 1701.sqlite.gz\n",
			SQLiteOutput{Bytes: 900, Object: "b/fhodor/web/1791.sqlite.gz", Expired: []string{"1700", "1701"}}},
		{"an upload whose retention failed", "caravana-backup key=b/x/1.sqlite.gz bytes=5\n" +
			"caravana-warning the backup is kept, but old backups were not removed: listing: boom\n",
			SQLiteOutput{Bytes: 5, Object: "b/x/1.sqlite.gz", Warning: "the backup is kept, but old backups were not removed: listing: boom"}},
		{"an object with a space", "caravana-backup key=my bucket/app db/3.sqlite.gz bytes=7\n",
			SQLiteOutput{Bytes: 7, Object: "my bucket/app db/3.sqlite.gz"}},
		{"CRLF and other lines are tolerated", "noise\r\ncaravana-sqlite-copy bytes=10\r\nmore noise\r\n", SQLiteOutput{Bytes: 10}},
		{"a size that is no number is skipped", "caravana-sqlite-copy bytes=lots\ncaravana-backup key=b/1.sqlite.gz bytes=-4\n", SQLiteOutput{}},
		{"an expired line that is not a backup is skipped", "caravana-expired 1700.dump\ncaravana-expired .sqlite.gz\ncaravana-expired\n", SQLiteOutput{}},
		{"a backup line without a key", "caravana-backup bytes=5\n", SQLiteOutput{}},
		{"empty", "", SQLiteOutput{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseSQLiteOutput(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// --- The scripts, under a real sh with fake tools.

// The fake tools of the sqlite scripts. Every wrapped tool refuses to run
// outside the fake timeout (the scripts must wrap every call), and the fake
// timeout, as in the volume tests, logs the seconds and can be told to run out.
// The real sqlite3 and gzip do the work; the fake rclone keeps a bucket in a
// directory. stat, chown, chmod and mv are thin wrappers: stat speaks BSD's
// syntax on a Mac (the scripts run on BusyBox, whose stat takes -c) and can
// answer a made-up owner, chown records the owner it was given instead of
// changing it (the test user can't give a file to someone else), and chmod and
// mv can be told to fail.
const (
	fakeSQLite3 = `#!/bin/sh
[ -n "$UNDER_TIMEOUT" ] || { echo "sqlite3 ran outside timeout" >&2; exit 99; }
echo "sqlite3 $*" >> "$FAKE_LOG"
case "$*" in
  *VACUUM*) if [ -n "$FAKE_SQLITE_FAIL" ]; then echo "Error: $FAKE_SQLITE_FAIL" >&2; exit 1; fi ;;
  *quick_check*) if [ -n "$FAKE_QUICK_CHECK" ]; then printf '%s\n' "$FAKE_QUICK_CHECK"; exit 0; fi ;;
esac
exec REALSQLITE3 "$@"
`
	fakeGzip = `#!/bin/sh
[ -n "$UNDER_TIMEOUT" ] || { echo "gzip ran outside timeout" >&2; exit 99; }
if [ -n "$FAKE_FAIL_GZIP" ] && [ "$1" = -c ]; then
  echo "gzip: $FAKE_FAIL_GZIP" >&2
  echo partial
  exit 2
fi
exec REALGZIP "$@"
`
	fakeSQLiteRclone = `#!/bin/sh
[ -n "$UNDER_TIMEOUT" ] || { echo "rclone ran outside timeout" >&2; exit 99; }
echo "rclone $*" >> "$FAKE_LOG"
[ -z "$EVIL" ] || echo "EVIL reached rclone" >> "$FAKE_LOG"
cmd=$1
shift
if [ "$cmd" = "$FAKE_FAIL_CMD" ]; then
  [ "$cmd" != rcat ] || cat > /dev/null
  echo "Failed to $cmd: injected failure" >&2
  exit 1
fi
if [ -n "$FAKE_SECRET" ] && [ "$RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY" != "$FAKE_SECRET" ]; then
  [ "$cmd" != rcat ] || cat > /dev/null
  echo "AccessDenied: the secret key is wrong" >&2
  exit 1
fi
local_path() { echo "$FAKE_BUCKET/${1#dest:}"; }
case "$cmd" in
  rcat)
    p=$(local_path "$1")
    mkdir -p "$(dirname "$p")"
    if [ -n "$FAKE_PARTIAL_RCAT" ]; then
      head -c 10 > "$p"
      cat > /dev/null
      echo "Failed to rcat: the connection broke" >&2
      exit 1
    fi
    case "$p" in *"$FAKE_FAIL_RCAT"*) if [ -n "$FAKE_FAIL_RCAT" ]; then cat > /dev/null; echo "Failed to rcat: boom" >&2; exit 1; fi ;; esac
    cat > "$p" ;;
  cat)
    p=$(local_path "$1")
    [ -f "$p" ] || { echo "object not found" >&2; exit 3; }
    cat "$p" ;;
  size)
    shift # --json
    p=$(local_path "$1")
    [ -f "$p" ] || { echo "object not found" >&2; exit 3; }
    n=$(wc -c < "$p" | tr -d ' ')
    echo "{\"count\":1,\"bytes\":$n,\"sizeless\":0}" ;;
  lsf)
    shift # --files-only
    p=$(local_path "$1")
    [ -d "$p" ] || { echo "directory not found" >&2; exit 3; }
    for f in "$p"/*; do [ -f "$f" ] && basename "$f"; done
    exit 0 ;;
  deletefile)
    p=$(local_path "$1")
    [ -f "$p" ] || { echo "object not found" >&2; exit 3; }
    if [ -n "$FAKE_FAIL_DELETE" ]; then
      case "$p" in *"$FAKE_FAIL_DELETE"*) echo "Failed to deletefile: injected failure" >&2; exit 1 ;; esac
    fi
    rm -f "$p" ;;
  *) echo "fake rclone: unknown command $cmd" >&2; exit 2 ;;
esac
`
	fakeStat = `#!/bin/sh
# Only "stat -c <format> <file>", the BusyBox form the scripts use.
[ "$1" = -c ] || { echo "stat: unexpected arguments" >&2; exit 2; }
case "$2" in
  %u:%g)
    case "$3" in
      *.db) [ -z "$FAKE_STAT_OWNER_DB" ] || { echo "$FAKE_STAT_OWNER_DB"; exit 0; } ;;
      *) [ -z "$FAKE_STAT_OWNER_DIR" ] || { echo "$FAKE_STAT_OWNER_DIR"; exit 0; } ;;
    esac ;;
esac
if [ -n "$BSD_STAT" ]; then
  case "$2" in
    %u:%g) exec REALSTAT -f %u:%g "$3" ;;
    %a) exec REALSTAT -f %Lp "$3" ;;
    %s) exec REALSTAT -f %z "$3" ;;
  esac
  exit 2
fi
exec REALSTAT -c "$2" "$3"
`
	fakeChown = `#!/bin/sh
echo "chown $*" >> "$FAKE_LOG"
exit 0
`
	fakeChmod = `#!/bin/sh
echo "chmod $*" >> "$FAKE_LOG"
[ -z "$FAKE_CHMOD_FAIL" ] || { echo "chmod: injected failure" >&2; exit 1; }
exec REALCHMOD "$@"
`
	fakeMv = `#!/bin/sh
if [ -n "$FAKE_MV_FAIL_FROM" ]; then
  case "$1" in *"$FAKE_MV_FAIL_FROM"*) echo "mv: injected failure" >&2; exit 1 ;; esac
fi
exec REALMV "$@"
`
)

var (
	sqliteTools     sync.Once
	sqliteToolsPath string
	sqliteToolsErr  error
)

// sqliteToolsDir writes the fake tools once for the whole test run (see
// fakeToolsDir for why). It reports a missing sqlite3 or gzip as a skip.
func sqliteToolsDir(t *testing.T) string {
	t.Helper()
	real := map[string]string{}
	for _, tool := range []string{"sqlite3", "gzip", "stat", "chmod", "mv", "sh", "cut", "tr", "date", "basename", "dirname", "cat", "rm",
		"tail", "head", "sort", "grep", "sed", "wc", "cp", "mkdir", "touch"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
		real[tool] = p
	}
	sqliteTools.Do(func() {
		sqliteToolsPath, sqliteToolsErr = os.MkdirTemp("", "caravana-sqlite-tools-")
		if sqliteToolsErr != nil {
			return
		}
		bsd := ""
		if err := exec.Command(real["stat"], "-c", "%s", "/").Run(); err != nil {
			bsd = "1"
		}
		stat := strings.NewReplacer("REALSTAT", real["stat"], `[ -n "$BSD_STAT" ]`, `[ -n "`+bsd+`" ]`).Replace(fakeStat)
		for name, text := range map[string]string{
			"timeout": fakeTimeout,
			"sqlite3": strings.ReplaceAll(fakeSQLite3, "REALSQLITE3", real["sqlite3"]),
			"gzip":    strings.ReplaceAll(fakeGzip, "REALGZIP", real["gzip"]),
			"rclone":  fakeSQLiteRclone,
			"stat":    stat,
			"chown":   fakeChown,
			"chmod":   strings.ReplaceAll(fakeChmod, "REALCHMOD", real["chmod"]),
			"mv":      strings.ReplaceAll(fakeMv, "REALMV", real["mv"]),
		} {
			if sqliteToolsErr = os.WriteFile(filepath.Join(sqliteToolsPath, name), []byte(text), 0o755); sqliteToolsErr != nil {
				return
			}
		}
	})
	if sqliteToolsErr != nil {
		t.Fatalf("fake tools: %v", sqliteToolsErr)
	}
	return sqliteToolsPath
}

// sqliteRun is a directory to run the scripts in: a volume "data" under vols,
// the allocation's data directory, and the fake bucket.
type sqliteRun struct {
	t      *testing.T
	dir    string
	vols   string // ROOT of caravana-volumes' view: vols/data is the volume
	work   string // SQLiteWorkDir
	bucket string
	log    string
	bin    string
}

func newSQLiteRun(t *testing.T) *sqliteRun {
	t.Helper()
	r := &sqliteRun{t: t, dir: t.TempDir(), bin: sqliteToolsDir(t)}
	r.vols = filepath.Join(r.dir, "vols")
	r.work = filepath.Join(r.dir, "alloc", "data", "caravana", "sqlite")
	r.bucket = filepath.Join(r.dir, "bucket")
	r.log = filepath.Join(r.dir, "calls.log")
	for _, d := range []string{filepath.Join(r.vols, "data"), r.bucket} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// execResult is what a script printed and how it ended.
type execResult struct {
	stdout, stderr string
	code           int
}

// lastLine is the last line of stderr: the reason of a failed script.
func (e execResult) lastLine() string {
	lines := strings.Split(strings.TrimRight(e.stderr, "\n"), "\n")
	return lines[len(lines)-1]
}

// exec runs script the way Caravana's exec does: sh -c <script> sh <args...>,
// with stdin on its standard input and extra environment ("KEY=value").
func (r *sqliteRun) exec(script, stdin string, env []string, args ...string) execResult {
	r.t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_BUCKET="+r.bucket, "FAKE_LOG="+r.log)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := execResult{stdout: stdout.String(), stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		res.code = ee.ExitCode()
	case err != nil:
		r.t.Fatalf("run: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(r.dir, "PWNED")); statErr == nil {
		r.t.Error("a value was run as a shell command")
	}
	return res
}

// database makes a WAL-mode SQLite file at path with n rows, as an app's live
// database is.
func (r *sqliteRun) database(path string, n int) { r.makeDatabase(path, n, "WAL") }

// plainDatabase makes a rollback-journal file, as a copy made with VACUUM INTO
// is: one file, no log beside it.
func (r *sqliteRun) plainDatabase(path string, n int) { r.makeDatabase(path, n, "DELETE") }

func (r *sqliteRun) makeDatabase(path string, n int, journal string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	sql := "PRAGMA journal_mode=" + journal + "; CREATE TABLE t(v INTEGER);"
	for i := range n {
		sql += fmt.Sprintf(" INSERT INTO t VALUES (%d);", i)
	}
	if out, err := exec.Command("sqlite3", path, sql).CombinedOutput(); err != nil {
		r.t.Fatalf("sqlite3: %v\n%s", err, out)
	}
}

// rows counts the rows of a database made by database.
func (r *sqliteRun) rows(path string) string {
	r.t.Helper()
	out, err := exec.Command("sqlite3", path, "SELECT count(*) FROM t").Output()
	if err != nil {
		r.t.Fatalf("sqlite3 count: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func (r *sqliteRun) read(path string) string {
	r.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *sqliteRun) write(path, text string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// callLog is every call the fake tools answered.
func (r *sqliteRun) callLog() string {
	b, _ := os.ReadFile(r.log)
	return string(b)
}

// bucketFiles lists the files in the fake bucket.
func (r *sqliteRun) bucketFiles() []string {
	r.t.Helper()
	var out []string
	err := filepath.WalkDir(r.bucket, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(r.bucket, p)
			out = append(out, rel)
		}
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// workFiles lists what is in the work directory.
func (r *sqliteRun) workFiles() []string {
	r.t.Helper()
	entries, err := os.ReadDir(r.work)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		r.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// timeouts counts the calls to prog that ran under timeout, with their seconds.
func (r *sqliteRun) timeouts(prog string) []int {
	var out []int
	for line := range strings.Lines(r.callLog()) {
		var secs int
		var p string
		if n, _ := fmt.Sscanf(line, "timeout %d %s", &secs, &p); n == 2 && p == prog {
			out = append(out, secs)
		}
	}
	return out
}

const sentinelKey = "sk-sentinel-029 $HOME `id` 'q' \"d\" a=b  "

// destination is the standard input Caravana sends: the destination's lines,
// the secret key a hard one (spaces at the end, quotes, a $, a backtick, an
// equals sign), each line ending in a newline.
func destination(extra ...string) string {
	lines := []string{
		"RCLONE_CONFIG_DEST_TYPE=s3",
		"RCLONE_CONFIG_DEST_ENDPOINT=http://10.0.0.5:9000",
		"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY=" + sentinelKey,
	}
	return strings.Join(append(lines, extra...), "\n") + "\n"
}

func TestSQLiteCopyScript(t *testing.T) {
	run := func(r *sqliteRun, env []string, db, out string) execResult {
		return r.exec(SQLiteCopyScript, "", env, db, "/data/app.db", out, "600")
	}

	t.Run("a copy of a live WAL database, consistent and checked", func(t *testing.T) {
		r := newSQLiteRun(t)
		db := filepath.Join(r.vols, "data", "app.db")
		r.database(db, 50)
		// A leftover of an earlier run is cleared first.
		r.write(filepath.Join(r.work, "old.db"), "leftover")
		out := filepath.Join(r.work, "web-1.db")
		res := run(r, nil, db, out)
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		info, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		if got := ParseSQLiteOutput(res.stdout); got.Bytes != info.Size() || got.Bytes == 0 {
			t.Errorf("output = %q, want the copy's size %d", res.stdout, info.Size())
		}
		if got := r.rows(out); got != "50" {
			t.Errorf("the copy has %s rows, want 50", got)
		}
		if got := r.workFiles(); !slices.Equal(got, []string{"web-1.db"}) {
			t.Errorf("work dir = %v, want only the copy (the earlier leftover is gone, no scratch file stays)", got)
		}
		// Both sqlite3 calls ran under timeout, with at most the run's limit.
		secs := r.timeouts("sqlite3")
		if len(secs) != 2 {
			t.Fatalf("sqlite3 ran under timeout %d times, want 2 (copy, check)", len(secs))
		}
		for _, s := range secs {
			if s < 1 || s > 600 {
				t.Errorf("timeout = %d seconds, want 1 to 600", s)
			}
		}
		if !strings.Contains(r.callLog(), "VACUUM INTO '"+out+"'") {
			t.Errorf("the copy isn't VACUUM INTO:\n%s", r.callLog())
		}
	})

	failures := []struct {
		name    string
		prep    func(r *sqliteRun, db string)
		env     []string
		out     string // overrides the default
		wantMsg string
	}{
		{name: "no database file", wantMsg: "copy: there is no database file at /data/app.db"},
		{name: "a file that isn't a database",
			prep: func(r *sqliteRun, db string) {
				r.write(db, "this is not sqlite, not at all, not even close to a database file")
			},
			wantMsg: "copy: Error: "},
		{name: "the copy fails", prep: func(r *sqliteRun, db string) { r.database(db, 3) },
			env: []string{"FAKE_SQLITE_FAIL=disk I/O error"}, wantMsg: "copy: Error: disk I/O error"},
		{name: "the copy runs out of time", prep: func(r *sqliteRun, db string) { r.database(db, 3) },
			env: []string{"FAKE_TIMEOUT_EXPIRE=sqlite3"}, wantMsg: "copy: timed out (the limit is 600 seconds)"},
		{name: "the check finds damage", prep: func(r *sqliteRun, db string) { r.database(db, 3) },
			env:     []string{"FAKE_QUICK_CHECK=*** in database main ***\nPage 4: never used"},
			wantMsg: "check: the copy is damaged: *** in database main ***"},
		{name: "a path with a quote", prep: func(r *sqliteRun, db string) { r.database(db, 3) },
			out: "/alloc/x'y.db", wantMsg: "copy: the copy's path is not usable"},
		{name: "a path with no directory", prep: func(r *sqliteRun, db string) { r.database(db, 3) },
			out: "x.db", wantMsg: "copy: the copy's path is not usable"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			r := newSQLiteRun(t)
			db := filepath.Join(r.vols, "data", "app.db")
			if tt.prep != nil {
				tt.prep(r, db)
			}
			out := tt.out
			if out == "" {
				out = filepath.Join(r.work, "web-1.db")
			}
			res := run(r, tt.env, db, out)
			if res.code != 1 {
				t.Fatalf("exit %d, want 1; stderr: %s", res.code, res.stderr)
			}
			if got := res.lastLine(); !strings.HasPrefix(got, tt.wantMsg) {
				t.Errorf("last line of stderr = %q, want it to start with %q", got, tt.wantMsg)
			}
			if exists(out) {
				t.Errorf("a failed copy left %s", out)
			}
			if got := r.workFiles(); len(got) != 0 {
				t.Errorf("work dir = %v, want empty", got)
			}
			// A missing database is never created by the copy (research D3).
			if tt.name == "no database file" && exists(db) {
				t.Error("the copy created an empty database file")
			}
		})
	}
}

func TestSQLiteUploadScript(t *testing.T) {
	// upload runs the script on a copy with n rows. keep and ts are the
	// request's.
	upload := func(r *sqliteRun, stdin string, env []string, ts, keep string) (execResult, string) {
		copyPath := filepath.Join(r.work, "web-1.db")
		r.plainDatabase(copyPath, 20)
		want := r.read(copyPath)
		env = append(slices.Clone(env), "FAKE_SECRET="+sentinelKey)
		return r.exec(SQLiteUploadScript, stdin, env, copyPath, "b/fhodor/web", ts, keep, "600"), want
	}

	t.Run("gzips the copy into the destination and removes it", func(t *testing.T) {
		r := newSQLiteRun(t)
		res, want := upload(r, destination(), nil, "1791", "3")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		obj := filepath.Join(r.bucket, "b/fhodor/web/1791.sqlite.gz")
		gz, err := os.ReadFile(obj)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := exec.Command("gzip", "-dc", obj).Output()
		if err != nil || string(plain) != want {
			t.Errorf("the object isn't the gzipped copy (err %v)", err)
		}
		out := ParseSQLiteOutput(res.stdout)
		if out.Object != "b/fhodor/web/1791.sqlite.gz" || out.Bytes != int64(len(gz)) || len(out.Expired) != 0 || out.Warning != "" {
			t.Errorf("output = %+v, want the object, its compressed size %d and nothing else", out, len(gz))
		}
		if got := r.workFiles(); len(got) != 0 {
			t.Errorf("work dir = %v, want the copy and the scratch files removed", got)
		}
		for _, prog := range []string{"gzip", "rclone"} {
			if len(r.timeouts(prog)) == 0 {
				t.Errorf("no call to %s ran under timeout", prog)
			}
		}
	})

	t.Run("the credentials come only from the standard input, and nowhere else", func(t *testing.T) {
		r := newSQLiteRun(t)
		res, _ := upload(r, destination(), nil, "1791", "3")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		// The fake rclone refused any other secret key: the hard value arrived
		// intact. It is in no output, no log and no file the run made.
		secret := "sk-sentinel-029"
		if strings.Contains(res.stdout, secret) || strings.Contains(res.stderr, secret) || strings.Contains(r.callLog(), secret) {
			t.Error("the secret key is in the script's output or in a call's arguments")
		}
		err := filepath.WalkDir(r.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.Contains(r.read(p), secret) {
				t.Errorf("the secret key is in %s", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("only RCLONE_CONFIG_DEST lines are read, and none is run", func(t *testing.T) {
		r := newSQLiteRun(t)
		stdin := destination("EVIL=1", "RCLONE_CONFIG_DEST_BAD NAME=y", "$(touch PWNED)", "`touch PWNED`", "touch PWNED", "RCLONE_CONFIG_DEST_X=$(touch PWNED)")
		res, _ := upload(r, stdin, nil, "1791", "3")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if strings.Contains(r.callLog(), "EVIL reached rclone") {
			t.Error("a line that isn't a destination variable was exported")
		}
	})

	t.Run("a destination whose key is wrong fails with the reason, and leaves nothing", func(t *testing.T) {
		r := newSQLiteRun(t)
		stdin := strings.Replace(destination(), "sk-sentinel-029", "another", 1)
		res, _ := upload(r, stdin, nil, "1791", "3")
		if res.code != 1 || res.lastLine() != "upload: AccessDenied: the secret key is wrong" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		if got := r.bucketFiles(); len(got) != 0 {
			t.Errorf("bucket = %v", got)
		}
	})

	failures := []struct {
		name    string
		env     []string
		ts      string
		wantMsg string
	}{
		{name: "the upload fails", env: []string{"FAKE_FAIL_RCAT=1791"}, ts: "1791", wantMsg: "upload: Failed to rcat: boom"},
		{name: "the upload breaks halfway, and the partial object is deleted", env: []string{"FAKE_PARTIAL_RCAT=1"}, ts: "1791",
			wantMsg: "upload: Failed to rcat: the connection broke"},
		{name: "gzip fails, and what rclone got is deleted", env: []string{"FAKE_FAIL_GZIP=out of memory"}, ts: "1791", wantMsg: "upload: gzip: out of memory"},
		{name: "the size can't be read", env: []string{"FAKE_FAIL_CMD=size"}, ts: "1791", wantMsg: "upload: Failed to size: injected failure"},
		{name: "the upload runs out of time", env: []string{"FAKE_TIMEOUT_EXPIRE=rclone"}, ts: "1791", wantMsg: "upload: timed out (the limit is 600 seconds)"},
		{name: "no timestamp", ts: "", wantMsg: "upload: the request has no timestamp"},
		{name: "a timestamp that is a path", ts: "../x", wantMsg: "upload: the request has no timestamp"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			r := newSQLiteRun(t)
			r.write(filepath.Join(r.bucket, "b/fhodor/web/1700.sqlite.gz"), "an older backup")
			res, _ := upload(r, destination(), tt.env, tt.ts, "3")
			if res.code != 1 || res.lastLine() != tt.wantMsg {
				t.Fatalf("exit %d, last line %q, want 1 and %q", res.code, res.lastLine(), tt.wantMsg)
			}
			if got := r.bucketFiles(); !slices.Equal(got, []string{"b/fhodor/web/1700.sqlite.gz"}) {
				t.Errorf("bucket = %v, want only the older backup: nothing partial stays", got)
			}
			if got := r.workFiles(); len(got) != 0 {
				t.Errorf("work dir = %v, want the copy removed even when the upload failed", got)
			}
		})
	}

	t.Run("no backup path, no copy", func(t *testing.T) {
		r := newSQLiteRun(t)
		res := r.exec(SQLiteUploadScript, destination(), nil, filepath.Join(r.work, "web-1.db"), "", "1791", "3", "600")
		if res.code != 1 || res.lastLine() != "upload: the request has no backup destination" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
		res = r.exec(SQLiteUploadScript, destination(), nil, filepath.Join(r.work, "missing.db"), "b/x", "1791", "3", "600")
		if res.code != 1 || !strings.HasPrefix(res.lastLine(), "upload: there is no copy to upload at ") {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})
}

func TestSQLiteUploadRetention(t *testing.T) {
	tests := []struct {
		name        string
		existing    []string // timestamps already in the bucket
		keep        string
		ts          string
		env         []string
		wantLeft    []string
		wantExpired []string
		wantWarning string
	}{
		{name: "the oldest beyond KEEP go", existing: []string{"1700", "1701", "1702", "1703"}, keep: "3", ts: "1791",
			wantLeft: []string{"1702", "1703", "1791"}, wantExpired: []string{"1700", "1701"}},
		{name: "numbers sort as numbers", existing: []string{"999", "1000", "9"}, keep: "2", ts: "1791",
			wantLeft: []string{"1000", "1791"}, wantExpired: []string{"9", "999"}},
		{name: "within KEEP nothing goes", existing: []string{"1700", "1701"}, keep: "3", ts: "1791",
			wantLeft: []string{"1700", "1701", "1791"}},
		{name: "no KEEP deletes nothing", existing: []string{"1700", "1701", "1702"}, keep: "",
			ts: "1791", wantLeft: []string{"1700", "1701", "1702", "1791"}},
		{name: "a KEEP that is no number deletes nothing", existing: []string{"1700", "1701"}, keep: "many",
			ts: "1791", wantLeft: []string{"1700", "1701", "1791"}},
		{name: "the new backup is never the one deleted", existing: []string{"1800", "1801"}, keep: "1", ts: "1791",
			wantLeft: []string{"1791", "1801"}, wantExpired: []string{"1800"}},
		{name: "listing fails: the backup stays and says so", existing: []string{"1700"}, keep: "1", ts: "1791",
			env:         []string{"FAKE_FAIL_CMD=lsf"},
			wantLeft:    []string{"1700", "1791"},
			wantWarning: "the backup is kept, but old backups were not removed: listing: Failed to lsf: injected failure"},
		{name: "a delete fails: what went before is listed, and the run still succeeds", existing: []string{"1700", "1701", "1702"}, keep: "1", ts: "1791",
			env:         []string{"FAKE_FAIL_DELETE=1701"},
			wantLeft:    []string{"1701", "1702", "1791"},
			wantExpired: []string{"1700"},
			wantWarning: "the backup is kept, but old backups were not removed: removing the backup 1701: Failed to deletefile: injected failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newSQLiteRun(t)
			for _, ts := range tt.existing {
				r.write(filepath.Join(r.bucket, "b/fhodor/web", ts+".sqlite.gz"), "old "+ts)
			}
			// Other things under the path are not sqlite backups, and stay.
			others := []string{"b/fhodor/web/notes/readme", "b/fhodor/web/1500.dump", "b/fhodor/web/1600.tar.gz", "b/fhodor/web/latest.sqlite.gz"}
			for _, o := range others {
				r.write(filepath.Join(r.bucket, o), "keep me")
			}
			copyPath := filepath.Join(r.work, "web-1.db")
			r.plainDatabase(copyPath, 2)
			env := append(slices.Clone(tt.env), "FAKE_SECRET="+sentinelKey)
			res := r.exec(SQLiteUploadScript, destination(), env, copyPath, "b/fhodor/web", tt.ts, tt.keep, "600")
			if res.code != 0 {
				t.Fatalf("exit %d: %s", res.code, res.stderr)
			}
			out := ParseSQLiteOutput(res.stdout)
			if !slices.Equal(out.Expired, tt.wantExpired) || out.Warning != tt.wantWarning {
				t.Errorf("expired = %v, warning = %q; want %v and %q", out.Expired, out.Warning, tt.wantExpired, tt.wantWarning)
			}
			var left []string
			for _, f := range r.bucketFiles() {
				if ts, ok := strings.CutPrefix(f, "b/fhodor/web/"); ok {
					if ts, ok := strings.CutSuffix(ts, ".sqlite.gz"); ok && ts != "latest" {
						left = append(left, ts)
					}
				}
			}
			slices.Sort(tt.wantLeft)
			slices.Sort(left)
			if !slices.Equal(left, tt.wantLeft) {
				t.Errorf("backups left = %v, want %v", left, tt.wantLeft)
			}
			for _, o := range others {
				if !slices.Contains(r.bucketFiles(), o) {
					t.Errorf("retention deleted %s, which isn't a sqlite backup", o)
				}
			}
		})
	}
}

func TestSQLiteDownloadScript(t *testing.T) {
	download := func(r *sqliteRun, stdin string, env []string, object string) (execResult, string) {
		out := filepath.Join(r.work, "web-2.db")
		env = append(slices.Clone(env), "FAKE_SECRET="+sentinelKey)
		return r.exec(SQLiteDownloadScript, stdin, env, object, out, "600"), out
	}
	// gzipped puts a gzipped copy of the file at path in the bucket.
	gzipped := func(r *sqliteRun, src, object string) {
		r.t.Helper()
		dest := filepath.Join(r.bucket, object)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			r.t.Fatal(err)
		}
		gz, err := exec.Command("gzip", "-c", src).Output()
		if err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(dest, gz, 0o644); err != nil {
			r.t.Fatal(err)
		}
	}

	t.Run("streams the backup, gunzipped, into the work directory", func(t *testing.T) {
		r := newSQLiteRun(t)
		src := filepath.Join(r.dir, "source.db")
		r.plainDatabase(src, 30)
		gzipped(r, src, "b/fhodor/web/1791.sqlite.gz")
		r.write(filepath.Join(r.work, "leftover.db"), "an earlier run")
		res, out := download(r, destination(), nil, "b/fhodor/web/1791.sqlite.gz")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if r.read(out) != r.read(src) {
			t.Error("the downloaded file isn't the backup's database")
		}
		if got := ParseSQLiteOutput(res.stdout); got.Bytes != int64(len(r.read(src))) {
			t.Errorf("output = %q, want the file's size %d", res.stdout, len(r.read(src)))
		}
		if got := r.workFiles(); !slices.Equal(got, []string{"web-2.db"}) {
			t.Errorf("work dir = %v, want only the download (the earlier leftover and the scratch files are gone)", got)
		}
		if strings.Contains(res.stdout+res.stderr+r.callLog(), "sk-sentinel-029") {
			t.Error("the secret key is in the output or in a call's arguments")
		}
		for _, prog := range []string{"gzip", "rclone"} {
			if len(r.timeouts(prog)) == 0 {
				t.Errorf("no call to %s ran under timeout", prog)
			}
		}
	})

	failures := []struct {
		name    string
		prep    func(r *sqliteRun)
		env     []string
		stdin   string
		wantMsg string // a prefix
	}{
		{name: "no such backup", wantMsg: "download: object not found"},
		{name: "the wrong secret key", prep: func(r *sqliteRun) { r.write(filepath.Join(r.bucket, "b/fhodor/web/1791.sqlite.gz"), "x") },
			stdin: strings.Replace(destination(), "sk-sentinel-029", "another", 1), wantMsg: "download: AccessDenied: the secret key is wrong"},
		{name: "the download runs out of time", prep: func(r *sqliteRun) { r.write(filepath.Join(r.bucket, "b/fhodor/web/1791.sqlite.gz"), "x") },
			env: []string{"FAKE_TIMEOUT_EXPIRE=rclone"}, wantMsg: "download: timed out (the limit is 600 seconds)"},
		{name: "not gzip", prep: func(r *sqliteRun) {
			r.write(filepath.Join(r.bucket, "b/fhodor/web/1791.sqlite.gz"), "plain text, not gzip")
		},
			wantMsg: "download: "},
		{name: "a gzip that is not a database", prep: func(r *sqliteRun) {
			src := filepath.Join(r.dir, "notes.txt")
			r.write(src, "just some text")
			gzipped(r, src, "b/fhodor/web/1791.sqlite.gz")
		}, wantMsg: "download: the backup is not a SQLite database"},
		{name: "a gzip cut short", prep: func(r *sqliteRun) {
			src := filepath.Join(r.dir, "source.db")
			r.plainDatabase(src, 200)
			gz, err := exec.Command("gzip", "-c", src).Output()
			if err != nil {
				t.Fatal(err)
			}
			r.write(filepath.Join(r.bucket, "b/fhodor/web/1791.sqlite.gz"), string(gz[:len(gz)/2]))
		}, wantMsg: "download: "},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			r := newSQLiteRun(t)
			if tt.prep != nil {
				tt.prep(r)
			}
			stdin := tt.stdin
			if stdin == "" {
				stdin = destination()
			}
			res, out := download(r, stdin, tt.env, "b/fhodor/web/1791.sqlite.gz")
			if res.code != 1 || !strings.HasPrefix(res.lastLine(), tt.wantMsg) {
				t.Fatalf("exit %d, last line %q, want 1 and a line starting %q", res.code, res.lastLine(), tt.wantMsg)
			}
			if exists(out) {
				t.Error("a failed download left the file")
			}
			if got := r.workFiles(); len(got) != 0 {
				t.Errorf("work dir = %v, want empty", got)
			}
		})
	}

	t.Run("no object", func(t *testing.T) {
		r := newSQLiteRun(t)
		res, _ := download(r, destination(), nil, "")
		if res.code != 1 || res.lastLine() != "download: the request names no backup to restore" {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})
}

func TestSQLiteStageScript(t *testing.T) {
	stage := func(r *sqliteRun, env []string, request string) (execResult, string) {
		dir := filepath.Join(r.vols, "data")
		return r.exec(SQLiteStageScript, "", env, filepath.Join(r.work, "web-2.db"), dir, request, "600"), filepath.Join(dir, ".caravana-restore", request+".db")
	}

	t.Run("checks the file and stages it on the database's disk, and removes the download", func(t *testing.T) {
		r := newSQLiteRun(t)
		downloaded := filepath.Join(r.work, "web-2.db")
		r.plainDatabase(downloaded, 12)
		want := r.read(downloaded)
		// A staging directory left by an earlier restore is replaced.
		r.write(filepath.Join(r.vols, "data", ".caravana-restore", "stale.db"), "stale")
		res, staged := stage(r, nil, "web-2")
		if res.code != 0 {
			t.Fatalf("exit %d: %s", res.code, res.stderr)
		}
		if r.read(staged) != want {
			t.Error("the staged file isn't the downloaded one")
		}
		if exists(downloaded) || len(r.workFiles()) != 0 {
			t.Errorf("work dir = %v, want the download removed", r.workFiles())
		}
		if exists(filepath.Join(r.vols, "data", ".caravana-restore", "stale.db")) {
			t.Error("an earlier staged file stayed")
		}
		if len(r.timeouts("sqlite3")) != 1 {
			t.Errorf("sqlite3 ran under timeout %d times, want 1", len(r.timeouts("sqlite3")))
		}
	})

	failures := []struct {
		name    string
		prep    func(r *sqliteRun)
		env     []string
		request string
		wantMsg string
	}{
		{name: "a damaged file", prep: func(r *sqliteRun) { r.plainDatabase(filepath.Join(r.work, "web-2.db"), 3) },
			env: []string{"FAKE_QUICK_CHECK=row 7 missing from index"}, wantMsg: "check: the backup is damaged: row 7 missing from index"},
		{name: "not a database", prep: func(r *sqliteRun) {
			r.write(filepath.Join(r.work, "web-2.db"), "not a database file, just text that is long enough to be read as a header by sqlite")
		}, wantMsg: "check: Error: "},
		{name: "the check runs out of time", prep: func(r *sqliteRun) { r.plainDatabase(filepath.Join(r.work, "web-2.db"), 3) },
			env: []string{"FAKE_TIMEOUT_EXPIRE=sqlite3"}, wantMsg: "check: timed out (the limit is 600 seconds)"},
		{name: "files left by an earlier restore", prep: func(r *sqliteRun) {
			r.plainDatabase(filepath.Join(r.work, "web-2.db"), 3)
			r.write(filepath.Join(r.vols, "data", ".caravana-restore", "old", "precious.db"), "the only copy")
		}, wantMsg: "stage: data has files left in .caravana-restore/old by an earlier restore; check them, then delete that directory"},
		{name: "no downloaded file", wantMsg: "stage: there is no downloaded file at "},
		{name: "a request ID that is a path", prep: func(r *sqliteRun) { r.plainDatabase(filepath.Join(r.work, "web-2.db"), 3) },
			request: "../x", wantMsg: "stage: the request ID is not usable"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			r := newSQLiteRun(t)
			if tt.prep != nil {
				tt.prep(r)
			}
			request := tt.request
			if request == "" {
				request = "web-2"
			}
			res, staged := stage(r, tt.env, request)
			if res.code != 1 || !strings.HasPrefix(res.lastLine(), tt.wantMsg) {
				t.Fatalf("exit %d, last line %q, want 1 and a line starting %q", res.code, res.lastLine(), tt.wantMsg)
			}
			if exists(staged) {
				t.Error("a failed stage left the staged file")
			}
			if got := r.workFiles(); len(got) != 0 {
				t.Errorf("work dir = %v, want the download removed", got)
			}
			// What an earlier restore left is never touched.
			if tt.name == "files left by an earlier restore" && r.read(filepath.Join(r.vols, "data", ".caravana-restore", "old", "precious.db")) != "the only copy" {
				t.Error("the old files of an earlier restore were touched")
			}
			if tt.name != "files left by an earlier restore" && exists(filepath.Join(r.vols, "data", ".caravana-restore")) {
				t.Error("a failed stage left .caravana-restore")
			}
		})
	}

	t.Run("the volume isn't mounted", func(t *testing.T) {
		r := newSQLiteRun(t)
		r.plainDatabase(filepath.Join(r.work, "web-2.db"), 3)
		res := r.exec(SQLiteStageScript, "", nil, filepath.Join(r.work, "web-2.db"), filepath.Join(r.vols, "nope"), "web-2", "600")
		if res.code != 1 || !strings.HasPrefix(res.lastLine(), "stage: the volume is not mounted at ") {
			t.Errorf("exit %d, last line %q", res.code, res.lastLine())
		}
	})
}

// --- VolumeScript's restore-sqlite action.

// sqliteRestoreRequest is a valid restore-sqlite request for the database at
// data/app.db, staged by request web-3.
func sqliteRestoreRequest(kv ...string) *string {
	return request(append([]string{
		"REQUEST", "web-3", "ACTION", "restore-sqlite", "FILE", "data/app.db", "NOT_AFTER", inAnHour(), "LIMIT", "600",
	}, kv...)...)
}

// swapRun is a scriptRun with the sqlite fake tools (stat, chown, chmod, mv)
// first on the PATH.
type swapRun struct {
	*scriptRun
	env []string
}

func newSwapRun(t *testing.T) *swapRun {
	t.Helper()
	bin := sqliteToolsDir(t)
	r := newScriptRun(t)
	return &swapRun{scriptRun: r, env: []string{"PATH=" + bin + string(os.PathListSeparator) + r.bin + string(os.PathListSeparator) + os.Getenv("PATH")}}
}

func (s *swapRun) run(req *string, env ...string) {
	s.t.Helper()
	s.scriptRun.run(req, append(slices.Clone(s.env), env...)...)
}

// put writes a file in a volume, with its mode.
func (s *swapRun) put(rel, text string, mode os.FileMode) {
	s.t.Helper()
	p := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), mode); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		s.t.Fatal(err)
	}
}

func (s *swapRun) text(rel string) string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.root, rel))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

func (s *swapRun) has(rel string) bool { return exists(filepath.Join(s.root, rel)) }

func (s *swapRun) mode(rel string) os.FileMode {
	s.t.Helper()
	info, err := os.Stat(filepath.Join(s.root, rel))
	if err != nil {
		s.t.Fatal(err)
	}
	return info.Mode().Perm()
}

// chowns returns the owner and the file of every chown the script ran.
func (s *swapRun) chowns() []string {
	var out []string
	for line := range strings.Lines(s.callLogText()) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "chown "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func (s *swapRun) callLogText() string {
	b, _ := os.ReadFile(s.log)
	return string(b)
}

func TestVolumeScriptRestoreSQLite(t *testing.T) {
	// withOldDatabase gives data/app.db its files: the database, its log and
	// its journal, each with its own text, and an unrelated file.
	withOldDatabase := func(s *swapRun) {
		s.put("data/app.db", "old database", 0o640)
		s.put("data/app.db-wal", "old wal", 0o600)
		s.put("data/app.db-shm", "old shm", 0o600)
		s.put("data/app.db-journal", "old journal", 0o600)
		s.put("data/other.txt", "other", 0o644)
		s.put("data/.caravana-restore/web-3.db", "restored database", 0o600)
	}
	before := func(s *swapRun) map[string]string {
		out := map[string]string{}
		for _, f := range []string{"data/app.db", "data/app.db-wal", "data/app.db-shm", "data/app.db-journal", "data/other.txt"} {
			if s.has(f) {
				out[f] = s.text(f)
			}
		}
		return out
	}

	t.Run("the file is swapped in with the old owner and mode, and the old log goes", func(t *testing.T) {
		s := newSwapRun(t)
		withOldDatabase(s)
		s.run(sqliteRestoreRequest(), "FAKE_STAT_OWNER_DB=1000:1001", "FAKE_STAT_OWNER_DIR=7:8")

		res := s.result("web-3")
		if !res.Succeeded || res.Message != "" {
			t.Fatalf("result = %+v, want a plain success", res)
		}
		if got := s.text("data/app.db"); got != "restored database" {
			t.Errorf("app.db = %q, want the restored database", got)
		}
		if got := s.mode("data/app.db"); got != 0o640 {
			t.Errorf("mode = %v, want the old file's 0640", got)
		}
		if got := s.chowns(); len(got) != 1 || got[0] != "1000:1001 "+filepath.Join(s.root, "data/.caravana-restore/web-3.db") {
			t.Errorf("chown calls = %v, want one giving the staged file the old file's owner 1000:1001", got)
		}
		for _, gone := range []string{"data/app.db-wal", "data/app.db-shm", "data/app.db-journal", "data/.caravana-restore"} {
			if s.has(gone) {
				t.Errorf("%s is still there", gone)
			}
		}
		if got := s.text("data/other.txt"); got != "other" || !s.has("logs/file") {
			t.Error("a file beside the database was touched")
		}
		if got := s.files(); !slices.Equal(got, []string{"sizes", "web-3.result"}) {
			t.Errorf("data dir = %v", got)
		}
	})

	t.Run("no current file: the directory's owner and mode 0644", func(t *testing.T) {
		s := newSwapRun(t)
		s.put("data/.caravana-restore/web-3.db", "restored database", 0o600)
		s.run(sqliteRestoreRequest(), "FAKE_STAT_OWNER_DB=1000:1001", "FAKE_STAT_OWNER_DIR=7:8")
		if res := s.result("web-3"); !res.Succeeded {
			t.Fatalf("result = %+v", res)
		}
		if got := s.text("data/app.db"); got != "restored database" || s.mode("data/app.db") != 0o644 {
			t.Errorf("app.db = %q, mode %v; want the restored database, 0644", got, s.mode("data/app.db"))
		}
		if got := s.chowns(); len(got) != 1 || !strings.HasPrefix(got[0], "7:8 ") {
			t.Errorf("chown calls = %v, want the directory's owner 7:8", got)
		}
		if s.has("data/.caravana-restore") {
			t.Error(".caravana-restore is still there")
		}
	})

	t.Run("a directory the volume lacks is made, owned like the nearest one that exists", func(t *testing.T) {
		s := newSwapRun(t)
		s.put("data/.caravana-restore/web-3.db", "restored database", 0o600)
		s.run(sqliteRestoreRequest("FILE", "data/db/sub/app.db"), "FAKE_STAT_OWNER_DIR=7:8")
		if res := s.result("web-3"); !res.Succeeded {
			t.Fatalf("result = %+v", res)
		}
		if got := s.text("data/db/sub/app.db"); got != "restored database" {
			t.Errorf("the database = %q", got)
		}
		owners := s.chowns()
		want := []string{
			"7:8 " + filepath.Join(s.root, "data/db/sub"),
			"7:8 " + filepath.Join(s.root, "data/db"),
			"7:8 " + filepath.Join(s.root, "data/.caravana-restore/web-3.db"),
		}
		if !slices.Equal(owners, want) {
			t.Errorf("chown calls = %v, want %v", owners, want)
		}
	})

	t.Run("a failed swap puts every old file back, and says so", func(t *testing.T) {
		s := newSwapRun(t)
		withOldDatabase(s)
		want := before(s)
		// The last step of the swap fails: the staged file can't be given its mode.
		s.run(sqliteRestoreRequest(), "FAKE_CHMOD_FAIL=1")
		res := s.result("web-3")
		if res.Succeeded || res.Message != "the restored database could not be put in place at data/app.db" {
			t.Fatalf("result = %+v, want failed with the swap message", res)
		}
		for f, text := range want {
			if got := s.text(f); got != text {
				t.Errorf("%s = %q, want %q (put back)", f, got, text)
			}
		}
		if s.has("data/.caravana-restore") {
			t.Error(".caravana-restore is still there after a clean rollback")
		}
	})

	t.Run("a file that can't be moved aside: everything is as it was", func(t *testing.T) {
		s := newSwapRun(t)
		withOldDatabase(s)
		want := before(s)
		s.run(sqliteRestoreRequest(), "FAKE_MV_FAIL_FROM=data/app.db-wal")
		res := s.result("web-3")
		if res.Succeeded || res.Message != "the files of data/app.db could not be moved aside" {
			t.Fatalf("result = %+v", res)
		}
		for f, text := range want {
			if got := s.text(f); got != text {
				t.Errorf("%s = %q, want %q", f, got, text)
			}
		}
	})

	t.Run("old files that can't be put back stay in .caravana-restore/old, and the result says where", func(t *testing.T) {
		s := newSwapRun(t)
		withOldDatabase(s)
		s.run(sqliteRestoreRequest(), "FAKE_CHMOD_FAIL=1", "FAKE_MV_FAIL_FROM=old/app.db-wal")
		res := s.result("web-3")
		if res.Succeeded || !strings.Contains(res.Message, "the old files could not all be put back, they are in the .caravana-restore/old directory of data") {
			t.Fatalf("result = %+v, want the rollback failure said", res)
		}
		if got := s.text("data/.caravana-restore/old/app.db-wal"); got != "old wal" {
			t.Errorf("the wal that couldn't be put back = %q, want it kept in old/", got)
		}
		if got := s.text("data/app.db"); got != "old database" {
			t.Errorf("the database = %q, want the old one back", got)
		}
	})

	refusals := []struct {
		name    string
		prep    func(s *swapRun)
		request *string
		wantMsg string
	}{
		{name: "files left by an earlier restore", prep: func(s *swapRun) {
			withOldDatabase(s)
			s.put("data/.caravana-restore/old/precious.db", "the only copy", 0o644)
		}, wantMsg: "data has files left in .caravana-restore/old by an earlier restore; check them, then delete that directory"},
		{name: "nothing staged", prep: func(s *swapRun) {
			s.put("data/app.db", "old database", 0o644)
		}, wantMsg: "the restored database is not staged at .caravana-restore/web-3.db"},
		{name: "a volume that isn't mounted", prep: func(s *swapRun) {
			s.put("data/app.db", "old database", 0o644)
		}, request: sqliteRestoreRequest("FILE", "nope/app.db"), wantMsg: "the volume nope is not mounted"},
	}
	for _, tt := range refusals {
		t.Run("refuses: "+tt.name, func(t *testing.T) {
			s := newSwapRun(t)
			tt.prep(s)
			snap := s.state()
			var old string
			if tt.name == "files left by an earlier restore" {
				old = s.text("data/.caravana-restore/old/precious.db")
			}
			req := tt.request
			if req == nil {
				req = sqliteRestoreRequest()
			}
			s.run(req)
			res := s.result("web-3")
			if res.Succeeded || res.Message != tt.wantMsg {
				t.Fatalf("result = %+v, want failed with %q", res, tt.wantMsg)
			}
			if got := s.state(); !reflect.DeepEqual(got, snap) {
				t.Errorf("a refused restore changed the volumes:\n%v\nbefore:\n%v", got, snap)
			}
			if old != "" && s.text("data/.caravana-restore/old/precious.db") != old {
				t.Error("the old files of an earlier restore were touched")
			}
		})
	}

	// FILE comes from a request file: anything that isn't a clean relative
	// path "<volume>/<path>" of plain characters is refused before anything
	// is touched.
	for _, file := range []string{
		"", "/etc/passwd", "../x", "data/../x", "data/./x", "data//x", "data/", "data", "data/a b.db", "data/a'b.db",
		"data/$(touch PWNED).db", "data/`touch PWNED`.db", "data/a;b", ".", "..", "./data/app.db", "data/..",
	} {
		t.Run("refuses FILE "+file, func(t *testing.T) {
			s := newSwapRun(t)
			withOldDatabase(s)
			snap := s.state()
			s.run(sqliteRestoreRequest("FILE", file))
			res := s.result("web-3")
			if res.Succeeded || res.Message != "the request names no valid database file" {
				t.Fatalf("result = %+v", res)
			}
			if got := s.state(); !reflect.DeepEqual(got, snap) {
				t.Error("a refused restore changed the volumes")
			}
			if _, err := os.Stat(filepath.Join(s.dir, "PWNED")); err == nil {
				t.Error("a value was run as a shell command")
			}
		})
	}
}
