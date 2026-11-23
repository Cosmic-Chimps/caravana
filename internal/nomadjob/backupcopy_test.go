package nomadjob

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/nomad/api"
)

// The copy and check jobs (feature 052): their shapes against goldens, their
// output parsers, and their scripts under a real sh with a fake rclone.

// copySecret is the secret key every copy and check test puts in the
// environment, where the template would. The fake rclone fails when it finds it
// in its arguments, which is where `ps` would show it.
const copySecret = "SENTINEL-sk-052"

// goldenJob compares job with testdata/<name>.golden.json (-update rewrites it).
func goldenJob(t *testing.T, name string, job *api.Job) []byte {
	t.Helper()
	got, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return got
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs; run with -update and read the diff", path)
	}
	return got
}

func TestBackupCopyJob(t *testing.T) {
	for _, tt := range []struct{ name, kind, ext string }{
		{"backup-copy-file", CopyKindFile, ".dump"},
		{"backup-copy-dir", CopyKindDir, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			job := BackupCopyJob("shop", "db", "0123456789ab", tt.kind, tt.ext, labSettings())
			got := goldenJob(t, tt.name, job)

			if *job.ID != "shop_db_copy-0123456789ab" || *job.Type != "batch" || job.Periodic != nil || len(job.TaskGroups) != 1 {
				t.Fatalf("job = %s %s %+v", *job.ID, *job.Type, job.Periodic)
			}
			if !strings.HasPrefix(*job.ID, AppJobPrefix("shop")) || !strings.Contains(*job.ID, BackupCopyJobMarker) {
				t.Error("the copy job doesn't go with the app's helper jobs, or isn't marked as a copy")
			}
			g := job.TaskGroups[0]
			if *g.RestartPolicy.Attempts != 0 || *g.ReschedulePolicy.Attempts != 0 || len(g.Tasks) != 1 {
				t.Errorf("group = %+v", g)
			}
			task := g.Tasks[0]
			if task.Name != BackupCopyTaskName || task.Lifecycle != nil || task.Env["KIND"] != tt.kind || task.Env["EXT"] != tt.ext {
				t.Errorf("task = %+v", task)
			}
			if *task.Resources.CPU != 200 || *task.Resources.MemoryMB != 256 {
				t.Errorf("resources = %+v, want 200 MHz and 256 MB", task.Resources)
			}
			tmpl := task.Templates[0]
			if !strings.Contains(*tmpl.EmbeddedTmpl, `nomadVar "nomad/jobs/shop_db_copy-0123456789ab"`) || *tmpl.DestPath != "secrets/backup.env" {
				t.Errorf("template = %q into %q: the task must read its own variable into secrets/", *tmpl.EmbeddedTmpl, *tmpl.DestPath)
			}
			if strings.Contains(task.Config["args"].([]string)[0], "${") {
				t.Error("the script has ${, which Nomad would interpolate")
			}
			if bytes.Contains(got, []byte("RCLONE_CONFIG_")) && bytes.Contains(got, []byte("SECRET_ACCESS_KEY")) {
				t.Error("the job names a destination key")
			}
			if job.Meta["caravana.app"] != "shop" || job.Meta["caravana.service"] != "db" {
				t.Errorf("meta = %v", job.Meta)
			}
		})
	}
}

func TestBackupCheckJob(t *testing.T) {
	for _, mode := range []string{CheckModeTest, CheckModeLocate} {
		t.Run("backup-check-"+mode, func(t *testing.T) {
			job := BackupCheckJob("0123456789ab", mode, labSettings())
			got := goldenJob(t, "backup-check-"+mode, job)

			if *job.ID != "_backup-check-0123456789ab" || *job.Type != "batch" || len(job.TaskGroups) != 1 {
				t.Fatalf("job = %s %s", *job.ID, *job.Type)
			}
			if _, has := job.Meta["caravana.app"]; has || len(job.Meta) != 0 {
				t.Errorf("meta = %v: a check job belongs to no app", job.Meta)
			}
			task := job.TaskGroups[0].Tasks[0]
			if task.Name != BackupCheckTaskName || task.Env["MODE"] != mode || task.Env["CHECK_ID"] != "0123456789ab" {
				t.Errorf("task = %+v", task)
			}
			if !strings.Contains(*task.Templates[0].EmbeddedTmpl, `nomadVar "nomad/jobs/_backup-check-0123456789ab"`) {
				t.Error("the task doesn't read the job's own variable")
			}
			if strings.Contains(task.Config["args"].([]string)[0], "${") {
				t.Error("the script has ${, which Nomad would interpolate")
			}
			if bytes.Contains(got, []byte("SECRET_ACCESS_KEY")) {
				t.Error("the job names a destination key")
			}
		})
	}
}

func TestParseBackupCopyOutput(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  BackupCopyOutput
	}{
		{"nothing", nil, BackupCopyOutput{}},
		{"unrelated lines", []string{"Transferred: 1 / 1"}, BackupCopyOutput{}},
		{"done", []string{"noise", "caravana-copy: done key=b/shop/db/1791.dump bytes=1499"},
			BackupCopyOutput{Done: true, Object: "b/shop/db/1791.dump", Bytes: 1499}},
		{"done with retention", []string{"caravana-copy: done key=b/shop/web/1791/ bytes=2048",
			"caravana-copy: expired 1700", "caravana-copy: expired 1701"},
			BackupCopyOutput{Done: true, Object: "b/shop/web/1791/", Bytes: 2048, Expired: []string{"1700", "1701"}}},
		{"a warning", []string{"caravana-copy: done key=a bytes=1", "caravana-copy: warning the copy is kept, but old copies were not removed: boom"},
			BackupCopyOutput{Done: true, Object: "a", Bytes: 1, Warning: "the copy is kept, but old copies were not removed: boom"}},
		{"done without an object isn't done", []string{"caravana-copy: done bytes=1"}, BackupCopyOutput{Bytes: 1}},
		{"a bad size is left out", []string{"caravana-copy: done key=a bytes=-4"}, BackupCopyOutput{Done: true, Object: "a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseBackupCopyOutput(tc.lines)
			if got.Done != tc.want.Done || got.Object != tc.want.Object || got.Bytes != tc.want.Bytes ||
				got.Warning != tc.want.Warning || !slices.Equal(got.Expired, tc.want.Expired) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseBackupCheckOutput(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  BackupCheckOutput
	}{
		{"nothing", nil, BackupCheckOutput{}},
		{"ok", []string{"caravana-check: ok"}, BackupCheckOutput{OK: true}},
		{"failed", []string{"caravana-check: failed write"}, BackupCheckOutput{Failed: "write"}},
		{"found after a missing", []string{"caravana-check: missing 1", "caravana-check: found 2"},
			BackupCheckOutput{Found: 2, Missing: []int{1}}},
		{"unreachable and missing", []string{"caravana-check: unreachable 1", "caravana-check: missing 2", "caravana-check: missing 3"},
			BackupCheckOutput{Unreachable: []int{1}, Missing: []int{2, 3}}},
		{"a place out of range is ignored", []string{"caravana-check: found 4", "caravana-check: found 0", "caravana-check: found x"}, BackupCheckOutput{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseBackupCheckOutput(tc.lines)
			if got.OK != tc.want.OK || got.Failed != tc.want.Failed || got.Found != tc.want.Found ||
				!slices.Equal(got.Missing, tc.want.Missing) || !slices.Equal(got.Unreachable, tc.want.Unreachable) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// copyFakeRclone is rclone for the copy and check scripts. A remote's name is a
// folder of $FAKE_BUCKET ("src:a/b" is $FAKE_BUCKET/src/a/b). FAKE_FAIL_CMD makes
// the named subcommand fail, and FAKE_UNREACHABLE makes every call to the named
// remote fail, as a bucket that can't be reached does. size, like rclone's, says
// a missing directory is exit status 3.
const copyFakeRclone = `#!/bin/sh
echo "rclone $*" >> "$FAKE_LOG"
case "$*" in *` + copySecret + `*) echo "rclone: a secret key is in the arguments" >&2; exit 2 ;; esac
cmd=$1
shift
path_of() { echo "$FAKE_BUCKET/${1%%:*}/${1#*:}"; }
remote_of() { echo "${1%%:*}"; }
count_files() {
  n=0
  for f in "$1"/* "$1"/.[!.]*; do
    [ -e "$f" ] || continue
    if [ -d "$f" ]; then m=$(count_files "$f"); n=$((n + m)); else n=$((n + 1)); fi
  done
  echo $n
}
sum_bytes() {
  n=0
  for f in "$1"/* "$1"/.[!.]*; do
    [ -e "$f" ] || continue
    if [ -d "$f" ]; then m=$(sum_bytes "$f"); else m=$(wc -c < "$f" | tr -d ' '); fi
    n=$((n + m))
  done
  echo $n
}
for a in "$@"; do
  case "$a" in
    *:*) if [ -n "$FAKE_UNREACHABLE" ] && [ "$(remote_of "$a")" = "$FAKE_UNREACHABLE" ]; then
           [ "$cmd" != rcat ] || cat > /dev/null
           echo "dial tcp: connection refused" >&2; exit 1
         fi ;;
  esac
done
if [ "$cmd" = "$FAKE_FAIL_CMD" ]; then
  [ "$cmd" != rcat ] || cat > /dev/null
  echo "Failed to $cmd: injected failure" >&2
  exit 1
fi
case "$cmd" in
  copyto)
    from=$(path_of "$1"); to=$(path_of "$2")
    [ -f "$from" ] || { echo "object not found" >&2; exit 3; }
    mkdir -p "$(dirname "$to")" && cp "$from" "$to" ;;
  copy)
    from=$(path_of "$1"); to=$(path_of "$2")
    [ -d "$from" ] || { echo "directory not found" >&2; exit 3; }
    mkdir -p "$to" && cp -R "$from"/. "$to"/ ;;
  size)
    shift # --json
    p=$(path_of "$1")
    if [ -f "$p" ]; then
      echo "{\"count\":1,\"bytes\":$(wc -c < "$p" | tr -d ' '),\"sizeless\":0}"
    elif [ -d "$p" ]; then
      echo "{\"count\":$(count_files "$p"),\"bytes\":$(sum_bytes "$p"),\"sizeless\":0}"
    else
      echo "directory not found" >&2; exit 3
    fi ;;
  lsf)
    mode=$1
    case "$mode" in --files-only|--dirs-only|--max-depth) ;; *) echo "fake rclone: lsf $mode" >&2; exit 2 ;; esac
    [ "$mode" != --max-depth ] || shift # the depth
    shift
    p=$(path_of "$1")
    [ -d "$p" ] || { echo "directory not found" >&2; exit 3; }
    for f in "$p"/*; do
      [ -e "$f" ] || continue
      case "$mode" in
        --files-only) [ -f "$f" ] && basename "$f" ;;
        --dirs-only) [ -d "$f" ] && echo "$(basename "$f")/" ;;
        *) if [ -d "$f" ]; then echo "$(basename "$f")/"; else basename "$f"; fi ;;
      esac
    done
    exit 0 ;;
  rcat)
    p=$(path_of "$1"); mkdir -p "$(dirname "$p")" && cat > "$p" ;;
  deletefile)
    p=$(path_of "$1"); [ -f "$p" ] || { echo "object not found" >&2; exit 3; }; rm -f "$p" ;;
  purge)
    p=$(path_of "$1"); [ -d "$p" ] || { echo "directory not found" >&2; exit 3; }; rm -rf "$p" ;;
  *) echo "fake rclone: unknown command $cmd" >&2; exit 2 ;;
esac
`

var (
	copyTools     sync.Once
	copyToolsPath string
	copyToolsErr  error
)

// copyToolsDir writes the fake rclone once for the whole test run, as the other
// fakes are (macOS scans every new executable). TestMain removes it.
func copyToolsDir(t *testing.T) string {
	t.Helper()
	copyTools.Do(func() {
		copyToolsPath, copyToolsErr = os.MkdirTemp("", "caravana-copy-tools-")
		if copyToolsErr != nil {
			return
		}
		copyToolsErr = os.WriteFile(filepath.Join(copyToolsPath, "rclone"), []byte(copyFakeRclone), 0o755)
	})
	if copyToolsErr != nil {
		t.Fatalf("fake tools: %v", copyToolsErr)
	}
	return copyToolsPath
}

// newCopyRun is a dumpRun whose PATH has the fake rclone and the real utilities
// the copy and check scripts use.
func newCopyRun(t *testing.T) *dumpRun {
	t.Helper()
	r := newDumpRun(t)
	for _, tool := range []string{"tail"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(r.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(copyToolsDir(t), "rclone"), filepath.Join(r.bin, "rclone")); err != nil {
		t.Fatal(err)
	}
	return r
}

// put writes a file of the fake destination "remote" at path.
func (r *dumpRun) put(remote, path, content string) {
	r.t.Helper()
	r.putObject(filepath.Join(remote, path), content)
}

// exists reports whether the fake destination has a file or folder at path.
func (r *dumpRun) exists(remote, path string) bool {
	_, err := os.Stat(filepath.Join(r.bucket, remote, path))
	return err == nil
}

// names lists the files and folders of the fake destination's folder, sorted.
func (r *dumpRun) names(remote, path string) []string {
	r.t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.bucket, remote, path))
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// copyEnv is the environment of a copy run: what the variable and the job's env give.
func copyEnv(kind, ext, src, dest, destPath string, extra ...string) []string {
	return append([]string{"KIND=" + kind, "EXT=" + ext, "SRC_OBJECT=" + src, "DEST_OBJECT=" + dest, "DEST_PATH=" + destPath,
		"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY=" + copySecret, "RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY=" + copySecret}, extra...)
}

// expiredNames are the names the run said retention deleted.
func expiredNames(stdout string) []string {
	return ParseBackupCopyOutput(strings.Split(stdout, "\n")).Expired
}

func TestBackupCopyScriptFile(t *testing.T) {
	t.Run("copies the object and says where and how big", func(t *testing.T) {
		r := newCopyRun(t)
		r.put("src", "main-bucket/notes/db/1791.dump", "pg dump\n")
		stdout, stderr, code := r.run(BackupCopyScript, copyEnv(CopyKindFile, ".dump",
			"main-bucket/notes/db/1791.dump", "off-bucket/notes/db/1791.dump", "off-bucket/notes/db")...)
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if want := BackupCopyMarker + " done key=off-bucket/notes/db/1791.dump bytes=8\n"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		if got := r.read(filepath.Join(r.bucket, "dest", "off-bucket/notes/db/1791.dump")); got != "pg dump\n" {
			t.Errorf("the copy holds %q", got)
		}
		if !r.exists("src", "main-bucket/notes/db/1791.dump") {
			t.Error("the source object is gone")
		}
		if strings.Contains(strings.Join(r.calls(), "\n"), copySecret) {
			t.Error("a secret key reached an argument")
		}
	})

	tests := []struct {
		name         string
		existing     []string // stamps already in the target
		keep         string
		pinned       *string
		wantLeft     []string
		wantExpired  []string
		wantWarning  bool
		failListCall bool
	}{
		{name: "no KEEP deletes nothing", existing: []string{"1700", "1701"}, wantLeft: []string{"1700", "1701", "1791"}},
		{name: "KEEP counts the new copy", existing: []string{"1700", "1701", "1702"}, keep: "2",
			wantLeft: []string{"1702", "1791"}, wantExpired: []string{"1700.dump", "1701.dump"}},
		{name: "within KEEP deletes nothing", existing: []string{"1700"}, keep: "5", wantLeft: []string{"1700", "1791"}},
		{name: "a pinned backup is kept and not counted", existing: []string{"1700", "1701", "1702", "1703"}, keep: "2",
			pinned: new("1700"), wantLeft: []string{"1700", "1703", "1791"}, wantExpired: []string{"1701.dump", "1702.dump"}},
		{name: "everything pinned deletes nothing", existing: []string{"1700", "1701"}, keep: "1", pinned: new("1700 1701"),
			wantLeft: []string{"1700", "1701", "1791"}},
		{name: "the new copy is never deleted", existing: []string{"1800", "1801"}, keep: "1", wantLeft: []string{"1791", "1801"},
			wantExpired: []string{"1800.dump"}},
		{name: "a pin list that is not digits deletes nothing and warns", existing: []string{"1700", "1701", "1702"}, keep: "1",
			pinned: new("1700 $(touch PWNED)"), wantLeft: []string{"1700", "1701", "1702", "1791"}, wantWarning: true},
		{name: "a failed listing keeps the copy and warns", existing: []string{"1700", "1701"}, keep: "1", failListCall: true,
			wantLeft: []string{"1700", "1701", "1791"}, wantWarning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCopyRun(t)
			r.put("src", "main-bucket/notes/db/1791.dump", "pg dump\n")
			for _, stamp := range tt.existing {
				r.put("dest", "off-bucket/notes/db/"+stamp+".dump", "old "+stamp)
			}
			r.put("dest", "off-bucket/notes/db/readme", "keep me")
			r.put("dest", "off-bucket/notes/db/1500.sqlite.gz", "keep me too")
			env := copyEnv(CopyKindFile, ".dump", "main-bucket/notes/db/1791.dump", "off-bucket/notes/db/1791.dump", "off-bucket/notes/db",
				"KEEP="+tt.keep)
			if tt.pinned != nil {
				env = append(env, "PINNED="+*tt.pinned)
			}
			if tt.failListCall {
				env = append(env, "FAKE_FAIL_CMD=lsf")
			}
			stdout, stderr, code := r.run(BackupCopyScript, env...)
			if _, err := os.Stat(filepath.Join(r.dir, "PWNED")); err == nil {
				t.Error("a pin was run as a shell command")
			}
			if code != 0 {
				t.Fatalf("exit %d, stderr %q: retention never fails the copy", code, stderr)
			}
			out := ParseBackupCopyOutput(strings.Split(stdout, "\n"))
			if !out.Done || out.Object != "off-bucket/notes/db/1791.dump" {
				t.Errorf("stdout = %q, want the done line first", stdout)
			}
			if (out.Warning != "") != tt.wantWarning {
				t.Errorf("warning = %q, want a warning: %v", out.Warning, tt.wantWarning)
			}
			if !slices.Equal(out.Expired, tt.wantExpired) {
				t.Errorf("expired = %v, want %v", out.Expired, tt.wantExpired)
			}
			var left []string
			for _, n := range r.names("dest", "off-bucket/notes/db") {
				if stamp, ok := strings.CutSuffix(n, ".dump"); ok {
					left = append(left, stamp)
				}
			}
			if !slices.Equal(left, tt.wantLeft) {
				t.Errorf("dumps left = %v, want %v", left, tt.wantLeft)
			}
			for _, other := range []string{"readme", "1500.sqlite.gz"} {
				if !r.exists("dest", "off-bucket/notes/db/"+other) {
					t.Errorf("retention deleted %s, which isn't a dump", other)
				}
			}
		})
	}

	t.Run("an empty extension is how a gzip dump is told from the rest", func(t *testing.T) {
		r := newCopyRun(t)
		r.put("src", "b/db/1791.sql.gz", "gz")
		r.put("dest", "o/db/1700.sql.gz", "old")
		r.put("dest", "o/db/1701.dump", "other engine")
		_, _, code := r.run(BackupCopyScript, copyEnv(CopyKindFile, ".sql.gz", "b/db/1791.sql.gz", "o/db/1791.sql.gz", "o/db", "KEEP=1")...)
		if code != 0 || r.exists("dest", "o/db/1700.sql.gz") || !r.exists("dest", "o/db/1701.dump") || !r.exists("dest", "o/db/1791.sql.gz") {
			t.Errorf("exit %d, files %v: want 1700.sql.gz gone and the .dump left", code, r.names("dest", "o/db"))
		}
	})
}

func TestBackupCopyScriptDir(t *testing.T) {
	t.Run("copies the folder with what is inside it", func(t *testing.T) {
		r := newCopyRun(t)
		r.put("src", "main-bucket/notes/web/1791/data.tar.gz", "12345")
		r.put("src", "main-bucket/notes/web/1791/uploads.tar.gz", "678")
		stdout, stderr, code := r.run(BackupCopyScript, copyEnv(CopyKindDir, "",
			"main-bucket/notes/web/1791/", "off-bucket/notes/web/1791/", "off-bucket/notes/web")...)
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if want := BackupCopyMarker + " done key=off-bucket/notes/web/1791/ bytes=8\n"; !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		if got := r.names("dest", "off-bucket/notes/web/1791"); !slices.Equal(got, []string{"data.tar.gz", "uploads.tar.gz"}) {
			t.Errorf("the copy holds %v", got)
		}
		calls := strings.Join(r.calls(), "\n")
		if !strings.Contains(calls, "rclone copy src:main-bucket/notes/web/1791/ dest:off-bucket/notes/web/1791/") {
			t.Errorf("calls = %s, want rclone copy of the folder", calls)
		}
	})

	t.Run("retention removes folders of digits and leaves the rest", func(t *testing.T) {
		r := newCopyRun(t)
		r.put("src", "b/web/1791/a.tar.gz", "a")
		for _, stamp := range []string{"1700", "1701", "1702", "1703"} {
			r.put("dest", "o/web/"+stamp+"/a.tar.gz", "old")
		}
		r.put("dest", "o/web/junk/a.tar.gz", "not a backup")
		r.put("dest", "o/web/notes.txt", "a file")
		stdout, _, code := r.run(BackupCopyScript, copyEnv(CopyKindDir, "", "b/web/1791/", "o/web/1791/", "o/web", "KEEP=2", "PINNED=1700")...)
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if got, want := expiredNames(stdout), []string{"1701", "1702"}; !slices.Equal(got, want) {
			t.Errorf("expired = %v, want %v", got, want)
		}
		want := []string{"1700", "1703", "1791", "junk", "notes.txt"}
		if got := r.names("dest", "o/web"); !slices.Equal(got, want) {
			t.Errorf("target = %v, want %v", got, want)
		}
	})

	t.Run("a bad pin list deletes nothing", func(t *testing.T) {
		r := newCopyRun(t)
		r.put("src", "b/web/1791/a.tar.gz", "a")
		r.put("dest", "o/web/1700/a.tar.gz", "old")
		r.put("dest", "o/web/1701/a.tar.gz", "old")
		stdout, _, code := r.run(BackupCopyScript, copyEnv(CopyKindDir, "", "b/web/1791/", "o/web/1791/", "o/web", "KEEP=1", "PINNED=../1700")...)
		out := ParseBackupCopyOutput(strings.Split(stdout, "\n"))
		if code != 0 || !out.Done || out.Warning == "" || len(out.Expired) != 0 || len(r.names("dest", "o/web")) != 3 {
			t.Errorf("exit %d, stdout %q, target %v: want the copy kept, a warning and nothing deleted", code, stdout, r.names("dest", "o/web"))
		}
	})
}

func TestBackupCopyScriptFailures(t *testing.T) {
	t.Run("an empty folder is not a copy", func(t *testing.T) {
		r := newCopyRun(t)
		if err := os.MkdirAll(filepath.Join(r.bucket, "src", "b/web/1791"), 0o755); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := r.run(BackupCopyScript, copyEnv(CopyKindDir, "", "b/web/1791/", "o/web/1791/", "o/web")...)
		if code != 1 || strings.Contains(stdout, BackupCopyMarker) || !strings.Contains(stderr, "the copy is empty") {
			t.Errorf("exit %d, stdout %q, stderr %q; want exit 1, no done line and the sentence", code, stdout, stderr)
		}
	})
	t.Run("a source that is gone fails with rclone's words", func(t *testing.T) {
		r := newCopyRun(t)
		stdout, stderr, code := r.run(BackupCopyScript, copyEnv(CopyKindFile, ".dump", "b/db/1791.dump", "o/db/1791.dump", "o/db")...)
		if code == 0 || strings.Contains(stdout, BackupCopyMarker) || !strings.Contains(stderr, "object not found") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
	t.Run("an unreachable target fails the run", func(t *testing.T) {
		r := newCopyRun(t)
		r.put("src", "b/db/1791.dump", "x")
		stdout, stderr, code := r.run(BackupCopyScript, copyEnv(CopyKindFile, ".dump", "b/db/1791.dump", "o/db/1791.dump", "o/db", "FAKE_UNREACHABLE=dest")...)
		if code == 0 || strings.Contains(stdout, BackupCopyMarker) || !strings.Contains(stderr, "connection refused") {
			t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
	t.Run("a missing input fails naming it", func(t *testing.T) {
		r := newCopyRun(t)
		_, stderr, code := r.run(BackupCopyScript, "KIND=file")
		if code != 1 || stderr != "the copy job has no SRC_OBJECT\n" {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
	})
	t.Run("a kind that is neither is refused before anything is copied", func(t *testing.T) {
		r := newCopyRun(t)
		r.put("src", "b/db/1791.dump", "x")
		_, stderr, code := r.run(BackupCopyScript, copyEnv("tree", ".dump", "b/db/1791.dump", "o/db/1791.dump", "o/db")...)
		if code != 1 || !strings.Contains(stderr, "neither file nor dir") || r.exists("dest", "o") {
			t.Errorf("exit %d, stderr %q, copied: %v", code, stderr, r.exists("dest", "o"))
		}
	})
}

// checkEnv is the environment of a check run.
func checkEnv(mode string, extra ...string) []string {
	return append([]string{"MODE=" + mode, "CHECK_ID=0123456789ab",
		"RCLONE_CONFIG_R1_SECRET_ACCESS_KEY=" + copySecret, "RCLONE_CONFIG_R2_SECRET_ACCESS_KEY=" + copySecret}, extra...)
}

func TestBackupCheckScriptTest(t *testing.T) {
	probe := "b/caravana/.caravana-check-0123456789ab"

	t.Run("lists, writes and removes a small object", func(t *testing.T) {
		r := newCopyRun(t)
		r.put("r1", "b/caravana/1700.dump", "an existing backup")
		stdout, stderr, code := r.run(BackupCheckScript, checkEnv(CheckModeTest, "R1_PATH=b/caravana")...)
		if code != 0 || stdout != BackupCheckMarker+" ok\n" {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if r.exists("r1", probe) {
			t.Error("the test object is still in the destination")
		}
		if !r.exists("r1", "b/caravana/1700.dump") {
			t.Error("the test touched a backup")
		}
		var verbs []string
		for _, c := range r.calls() {
			verbs = append(verbs, strings.Fields(c)[1])
		}
		if !slices.Equal(verbs, []string{"lsf", "rcat", "deletefile"}) {
			t.Errorf("calls = %v, want list, write, delete", r.calls())
		}
		if strings.Contains(strings.Join(r.calls(), "\n"), copySecret) {
			t.Error("a secret key reached an argument")
		}
	})
	t.Run("a folder that doesn't exist yet is still a reachable destination", func(t *testing.T) {
		r := newCopyRun(t)
		stdout, _, code := r.run(BackupCheckScript, checkEnv(CheckModeTest, "R1_PATH=b/caravana")...)
		if code != 0 || stdout != BackupCheckMarker+" ok\n" || r.exists("r1", probe) {
			t.Errorf("exit %d, stdout %q", code, stdout)
		}
	})
	for _, tt := range []struct {
		name, env, step string
	}{
		{"list fails", "FAKE_FAIL_CMD=lsf", "list"},
		{"write fails", "FAKE_FAIL_CMD=rcat", "write"},
		{"delete fails", "FAKE_FAIL_CMD=deletefile", "delete"},
		{"the destination is unreachable", "FAKE_UNREACHABLE=r1", "list"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newCopyRun(t)
			stdout, stderr, code := r.run(BackupCheckScript, checkEnv(CheckModeTest, "R1_PATH=b/caravana", tt.env)...)
			if code != 1 || stdout != BackupCheckMarker+" failed "+tt.step+"\n" {
				t.Errorf("exit %d, stdout %q; want exit 1 and failed %s", code, stdout, tt.step)
			}
			if strings.TrimSpace(stderr) == "" || strings.Contains(stderr, copySecret) {
				t.Errorf("stderr = %q, want rclone's own words and no key", stderr)
			}
		})
	}
	t.Run("a missing path or mode fails naming it", func(t *testing.T) {
		r := newCopyRun(t)
		if _, stderr, code := r.run(BackupCheckScript, checkEnv(CheckModeTest)...); code != 1 || stderr != "the check job has no R1_PATH\n" {
			t.Errorf("no path: exit %d, stderr %q", code, stderr)
		}
		if _, stderr, code := r.run(BackupCheckScript, checkEnv("peek", "R1_PATH=b")...); code != 1 || !strings.Contains(stderr, "neither test nor locate") {
			t.Errorf("bad mode: exit %d, stderr %q", code, stderr)
		}
		if _, stderr, code := r.run(BackupCheckScript, "MODE=test"); code != 1 || stderr != "the check job has no CHECK_ID\n" {
			t.Errorf("no id: exit %d, stderr %q", code, stderr)
		}
	})
}

func TestBackupCheckScriptLocate(t *testing.T) {
	const obj = "b/shop/db/1791.dump"
	tests := []struct {
		name  string
		setup func(r *dumpRun)
		env   []string
		want  string // stdout
	}{
		{"found in the first place stops there", func(r *dumpRun) { r.put("r1", obj, "x"); r.put("r2", obj, "x") },
			[]string{"R1_PATH=" + obj, "R2_PATH=" + obj}, "caravana-check: found 1\n"},
		{"missing in the first, found in the second", func(r *dumpRun) { r.put("r2", obj, "x") },
			[]string{"R1_PATH=" + obj, "R2_PATH=" + obj}, "caravana-check: missing 1\ncaravana-check: found 2\n"},
		{"found in the third", func(r *dumpRun) { r.put("r3", obj, "x") },
			[]string{"R1_PATH=" + obj, "R2_PATH=" + obj, "R3_PATH=" + obj},
			"caravana-check: missing 1\ncaravana-check: missing 2\ncaravana-check: found 3\n"},
		{"missing everywhere", func(r *dumpRun) {},
			[]string{"R1_PATH=" + obj, "R2_PATH=" + obj}, "caravana-check: missing 1\ncaravana-check: missing 2\n"},
		{"an empty folder counts as missing", func(r *dumpRun) {
			if err := os.MkdirAll(filepath.Join(r.bucket, "r1", "b/shop/web/1791"), 0o755); err != nil {
				r.t.Fatal(err)
			}
		}, []string{"R1_PATH=b/shop/web/1791/"}, "caravana-check: missing 1\n"},
		{"a folder with files is found", func(r *dumpRun) { r.put("r1", "b/shop/web/1791/a.tar.gz", "x") },
			[]string{"R1_PATH=b/shop/web/1791/"}, "caravana-check: found 1\n"},
		{"an unreachable place is said, and the next one is tried", func(r *dumpRun) { r.put("r2", obj, "x") },
			[]string{"R1_PATH=" + obj, "R2_PATH=" + obj, "FAKE_UNREACHABLE=r1"}, "caravana-check: unreachable 1\ncaravana-check: found 2\n"},
		{"unreachable everywhere", func(r *dumpRun) {},
			[]string{"R1_PATH=" + obj, "R2_PATH=" + obj, "FAKE_FAIL_CMD=size"}, "caravana-check: unreachable 1\ncaravana-check: unreachable 2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCopyRun(t)
			tt.setup(r)
			stdout, stderr, code := r.run(BackupCheckScript, checkEnv(CheckModeLocate, tt.env...)...)
			if code != 0 || stdout != tt.want {
				t.Errorf("exit %d, stdout %q, stderr %q; want %q", code, stdout, stderr, tt.want)
			}
			if strings.Contains(strings.Join(r.calls(), "\n"), copySecret) {
				t.Error("a secret key reached an argument")
			}
		})
	}
}

func TestCopyAndCheckScriptsHaveNoInterpolation(t *testing.T) {
	for name, script := range map[string]string{"copy": BackupCopyScript, "check": BackupCheckScript} {
		if strings.Contains(script, "${") {
			t.Errorf("the %s script has ${, which Nomad would interpolate", name)
		}
		if strings.Contains(script, "set -x") {
			t.Errorf("the %s script traces its commands", name)
		}
	}
}
