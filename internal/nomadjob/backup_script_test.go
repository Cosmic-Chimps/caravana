package nomadjob

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The upload task of the Postgres backup job, under a real sh with a fake
// rclone and stat. The script is the only one of the three engines that had no
// test of its own before feature 036.

// postgresFakeRclone knows the three calls uploadScript makes: copyto of the
// dump (the bucket gets a file named for the destination), lsf --files-only of
// the backup path, and deletefile.
const postgresFakeRclone = `#!/bin/sh
echo "rclone $*" >> "$FAKE_LOG"
cmd=$1
shift
local_path() { echo "$FAKE_BUCKET/${1#dest:}"; }
case "$cmd" in
  copyto)
    p=$(local_path "$2")
    mkdir -p "$(dirname "$p")" && echo dump > "$p" ;;
  lsf)
    shift # --files-only
    p=$(local_path "$1")
    [ -d "$p" ] || { echo "directory not found" >&2; exit 3; }
    for f in "$p"/*; do [ -f "$f" ] && basename "$f"; done
    exit 0 ;;
  deletefile)
    rm -f "$(local_path "$1")" ;;
  *) echo "fake rclone: unknown command $cmd" >&2; exit 2 ;;
esac
`

// postgresFakeStat answers "stat -c %s <file>", which is GNU's: BusyBox has it,
// macOS' stat doesn't. The dump isn't on this machine, so the size is fixed.
const postgresFakeStat = `#!/bin/sh
echo 1234
`

var (
	postgresTools     sync.Once
	postgresToolsPath string
	postgresToolsErr  error
)

// postgresToolsDir writes the fakes once for the whole test run, as
// fakeToolsDir does (macOS scans every new executable). TestMain removes
// the folder when the tests are done.
func postgresToolsDir(t *testing.T) string {
	t.Helper()
	postgresTools.Do(func() {
		postgresToolsPath, postgresToolsErr = os.MkdirTemp("", "caravana-postgres-tools-")
		if postgresToolsErr != nil {
			return
		}
		for name, text := range map[string]string{"rclone": postgresFakeRclone, "stat": postgresFakeStat} {
			if postgresToolsErr = os.WriteFile(filepath.Join(postgresToolsPath, name), []byte(text), 0o755); postgresToolsErr != nil {
				return
			}
		}
	})
	if postgresToolsErr != nil {
		t.Fatalf("fake tools: %v", postgresToolsErr)
	}
	return postgresToolsPath
}

// uploadRun runs uploadScript as the upload task does: the job's variable is its
// environment. pinned is nil for "BACKUP_PINNED is not an item at all".
func uploadRun(t *testing.T, existing []string, keep string, ts string, pinned *string) (stdout, stderr string, code int, left []string) {
	t.Helper()
	for _, tool := range []string{"sh", "sed", "grep", "sort", "tr", "head", "wc", "printenv", "basename", "mkdir", "rm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
	}
	dir := t.TempDir()
	bucket := filepath.Join(dir, "bucket")
	for _, stamp := range existing {
		p := filepath.Join(bucket, "b/notes/db", stamp+".dump")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("old "+stamp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Other things under the path are not dumps, and stay.
	for _, other := range []string{"b/notes/db/readme", "b/notes/db/1500.sqlite.gz"} {
		p := filepath.Join(bucket, other)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("sh", "-c", uploadScript)
	cmd.Dir = dir
	// A pinned item left in the test process's own environment must not leak in.
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "BACKUP_") && !strings.HasPrefix(e, "NOMAD_") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "PATH="+postgresToolsDir(t)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_BUCKET="+bucket, "FAKE_LOG="+filepath.Join(dir, "calls.log"),
		"NOMAD_JOB_ID=notes_db_backup/periodic-"+ts, "BACKUP_PATH=b/notes/db", "BACKUP_KEEP="+keep)
	if pinned != nil {
		cmd.Env = append(cmd.Env, "BACKUP_PINNED="+*pinned)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run: %v", err)
		}
		code = ee.ExitCode()
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Error("a value was run as a shell command")
	}
	files, err := os.ReadDir(filepath.Join(bucket, "b/notes/db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		left = append(left, f.Name())
	}
	slices.Sort(left)
	return out.String(), errOut.String(), code, left
}

func TestPostgresUploadScript(t *testing.T) {
	tests := []struct {
		name        string
		existing    []string
		keep        string
		ts          string
		pinned      *string
		wantLeft    []string // stamps of the dumps left
		wantExpired []string
		wantCode    int
		wantStderr  string
	}{
		{name: "the oldest beyond KEEP go", existing: []string{"1700", "1701", "1702", "1703"}, keep: "3", ts: "1791",
			wantLeft: []string{"1702", "1703", "1791"}, wantExpired: []string{"1700", "1701"}},
		{name: "within KEEP nothing goes", existing: []string{"1700", "1701"}, keep: "3", ts: "1791",
			wantLeft: []string{"1700", "1701", "1791"}},
		{name: "an empty pin list is no pin list", existing: []string{"1700", "1701", "1702"}, keep: "2", ts: "1791", pinned: new(""),
			wantLeft: []string{"1702", "1791"}, wantExpired: []string{"1700", "1701"}},
		{name: "a pinned dump is kept and doesn't count against KEEP", existing: []string{"1700", "1701", "1702", "1703"}, keep: "2", ts: "1791",
			pinned: new("1700"), wantLeft: []string{"1700", "1703", "1791"}, wantExpired: []string{"1701", "1702"}},
		{name: "several pinned dumps, KEEP counts only the others", existing: []string{"1700", "1701", "1702", "1703", "1704"}, keep: "1", ts: "1791",
			pinned: new("1700 1702"), wantLeft: []string{"1700", "1702", "1791"}, wantExpired: []string{"1701", "1703", "1704"}},
		{name: "a pin that is no longer in the destination is ignored", existing: []string{"1700", "1701", "1702"}, keep: "2", ts: "1791",
			pinned: new("1600"), wantLeft: []string{"1702", "1791"}, wantExpired: []string{"1700", "1701"}},
		{name: "with everything pinned nothing goes", existing: []string{"1700", "1701"}, keep: "1", ts: "1791",
			pinned: new("1700 1701"), wantLeft: []string{"1700", "1701", "1791"}},
		{name: "the new dump is never deleted, pins or not", existing: []string{"1800", "1801"}, keep: "1", ts: "1791",
			pinned: new("1801"), wantLeft: []string{"1791", "1800", "1801"}},
		{name: "a pin list that is not digits deletes nothing and says so", existing: []string{"1700", "1701", "1702"}, keep: "1", ts: "1791",
			pinned: new("1700 $(touch PWNED)"), wantLeft: []string{"1700", "1701", "1702", "1791"}, wantCode: 1,
			wantStderr: "old backups were not removed: the list of pinned backups is not valid\n"},
		{name: "a pin list with a path in it deletes nothing and says so", existing: []string{"1700", "1701"}, keep: "1", ts: "1791",
			pinned: new("../1700"), wantLeft: []string{"1700", "1701", "1791"}, wantCode: 1,
			wantStderr: "old backups were not removed: the list of pinned backups is not valid\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code, files := uploadRun(t, tt.existing, tt.keep, tt.ts, tt.pinned)
			if code != tt.wantCode || stderr != tt.wantStderr {
				t.Fatalf("exit %d, stderr %q; want %d and %q", code, stderr, tt.wantCode, tt.wantStderr)
			}
			// The upload happened before retention either way.
			if !strings.Contains(stdout, BackupDoneMarker+" key=b/notes/db/"+tt.ts+".dump bytes=1234\n") {
				t.Errorf("stdout = %q, want the done marker for the new dump", stdout)
			}
			var expired []string
			for line := range strings.Lines(stdout) {
				if f, ok := strings.CutPrefix(line, BackupExpiredMarker+" "); ok {
					expired = append(expired, strings.TrimSuffix(strings.TrimSpace(f), ".dump"))
				}
			}
			if !slices.Equal(expired, tt.wantExpired) {
				t.Errorf("expired = %v, want %v", expired, tt.wantExpired)
			}
			var left []string
			for _, f := range files {
				if stamp, ok := strings.CutSuffix(f, ".dump"); ok {
					left = append(left, stamp)
				}
			}
			if !slices.Equal(left, tt.wantLeft) {
				t.Errorf("dumps left = %v, want %v", left, tt.wantLeft)
			}
			for _, other := range []string{"readme", "1500.sqlite.gz"} {
				if !slices.Contains(files, other) {
					t.Errorf("retention deleted %s, which isn't a dump", other)
				}
			}
		})
	}
}
