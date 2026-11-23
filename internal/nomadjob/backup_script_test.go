package nomadjob

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The upload task of the backup jobs under a real sh with fake tools (see
// dumps_script_test.go). It was the only one of the engines' scripts without a
// test of its own before feature 036.

// uploadCase is one run of uploadScript.
type uploadCase struct {
	ext      string   // the engine's object extension
	gzip     bool     // BACKUP_GZIP
	dump     string   // what the dump task left in the dump file
	existing []string // stamps of the backups already in the destination, each as <stamp><ext>
	others   []string // other files under the path, which retention must leave
	keep     string
	ts       string
	pinned   *string // nil for "BACKUP_PINNED is not an item at all"
	dest     *string // nil for "BACKUP_DESTINATION is not an item at all" (a job from before feature 052)
}

// uploadResult is what uploadScript did.
type uploadResult struct {
	stdout, stderr string
	code           int
	files          map[string][]byte // the destination's files under the path, by name
}

// runUpload runs uploadScript as the upload task does: the job's variable and
// the task's env are its environment.
func runUpload(t *testing.T, c uploadCase) uploadResult {
	t.Helper()
	r := newDumpRun(t, "rclone", "stat")
	for _, stamp := range c.existing {
		r.putObject("b/notes/db/"+stamp+c.ext, "old "+stamp)
	}
	for _, other := range c.others {
		r.putObject("b/notes/db/"+other, "keep me")
	}
	if err := os.WriteFile(r.dump, []byte(c.dump), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"NOMAD_JOB_ID=notes_db_backup/periodic-" + c.ts, "BACKUP_PATH=b/notes/db", "BACKUP_KEEP=" + c.keep,
		"BACKUP_EXT=" + c.ext, "BACKUP_GZIP=0"}
	if c.gzip {
		env[len(env)-1] = "BACKUP_GZIP=1"
	}
	if c.pinned != nil {
		env = append(env, "BACKUP_PINNED="+*c.pinned)
	}
	if c.dest != nil {
		env = append(env, "BACKUP_DESTINATION="+*c.dest)
	}
	stdout, stderr, code := r.run(uploadScript, env...)
	if _, err := os.Stat(filepath.Join(r.dir, "PWNED")); err == nil {
		t.Error("a value was run as a shell command")
	}
	res := uploadResult{stdout: stdout, stderr: stderr, code: code, files: map[string][]byte{}}
	entries, err := os.ReadDir(filepath.Join(r.bucket, "b/notes/db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range entries {
		b, err := os.ReadFile(filepath.Join(r.bucket, "b/notes/db", f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		res.files[f.Name()] = b
	}
	return res
}

// names are the file names, sorted.
func (u uploadResult) names() []string {
	var out []string
	for n := range u.files {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// expired are the files the run said retention deleted.
func (u uploadResult) expired() []string {
	var out []string
	for line := range strings.Lines(u.stdout) {
		if f, ok := strings.CutPrefix(line, BackupExpiredMarker+" "); ok {
			out = append(out, strings.TrimSpace(f))
		}
	}
	return out
}

func gunzipString(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// exitCode is the exit code of a finished command, whatever its error.
func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run: %v", err)
	}
	return ee.ExitCode()
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
			res := runUpload(t, uploadCase{ext: ".dump", dump: "pg dump\n", existing: tt.existing,
				others: []string{"readme", "1500.sqlite.gz"}, keep: tt.keep, ts: tt.ts, pinned: tt.pinned})
			if res.code != tt.wantCode || res.stderr != tt.wantStderr {
				t.Fatalf("exit %d, stderr %q; want %d and %q", res.code, res.stderr, tt.wantCode, tt.wantStderr)
			}
			// The upload happened before retention either way, as the plain file.
			if !strings.Contains(res.stdout, BackupDoneMarker+" key=b/notes/db/"+tt.ts+".dump bytes=8 dest=\n") {
				t.Errorf("stdout = %q, want the done marker for the new dump", res.stdout)
			}
			if got := string(res.files[tt.ts+".dump"]); got != "pg dump\n" {
				t.Errorf("uploaded %q, want the dump as it was", got)
			}
			var expired []string
			for _, f := range res.expired() {
				expired = append(expired, strings.TrimSuffix(f, ".dump"))
			}
			if !slices.Equal(expired, tt.wantExpired) {
				t.Errorf("expired = %v, want %v", expired, tt.wantExpired)
			}
			var left []string
			for _, f := range res.names() {
				if stamp, ok := strings.CutSuffix(f, ".dump"); ok {
					left = append(left, stamp)
				}
			}
			if !slices.Equal(left, tt.wantLeft) {
				t.Errorf("dumps left = %v, want %v", left, tt.wantLeft)
			}
			for _, other := range []string{"readme", "1500.sqlite.gz"} {
				if _, ok := res.files[other]; !ok {
					t.Errorf("retention deleted %s, which isn't a dump", other)
				}
			}
		})
	}
}

// The done line names the destination the job's variable named (feature 052), so
// the run records where it went; a job from before that has none, and the line
// ends with an empty dest.
func TestUploadScriptNamesTheDestination(t *testing.T) {
	for _, tt := range []struct {
		name string
		dest *string
		want string
	}{
		{"named", new("offsite"), " bytes=8 dest=offsite\n"},
		{"empty", new(""), " bytes=8 dest=\n"},
		{"not an item", nil, " bytes=8 dest=\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := runUpload(t, uploadCase{ext: ".dump", dump: "pg dump\n", keep: "3", ts: "1791", dest: tt.dest})
			if res.code != 0 || res.stderr != "" {
				t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
			}
			if want := BackupDoneMarker + " key=b/notes/db/1791.dump" + tt.want; !strings.Contains(res.stdout, want) {
				t.Errorf("stdout = %q, want %q in it", res.stdout, want)
			}
		})
	}
}

// The MySQL and MongoDB objects (feature 046) go through the same script: it
// lists only the files that end in the engine's extension and a pin matches
// <stamp><ext>, so the engines' backups in one folder never touch each other's.
func TestUploadScriptExtensions(t *testing.T) {
	t.Run("a mysql dump is compressed and keeps its pins", func(t *testing.T) {
		res := runUpload(t, uploadCase{ext: ".sql.gz", gzip: true, dump: "CREATE TABLE t (id int);\n",
			existing: []string{"1700", "1701", "1702", "1703"}, others: []string{"1500.dump", "1600.archive.gz", "readme", "1650.sql"},
			keep: "2", ts: "1791", pinned: new("1700")})
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
		}
		obj, ok := res.files["1791.sql.gz"]
		if !ok {
			t.Fatalf("no object, files %v", res.names())
		}
		if got := gunzipString(t, obj); got != "CREATE TABLE t (id int);\n" {
			t.Errorf("the object holds %q", got)
		}
		if want := BackupDoneMarker + " key=b/notes/db/1791.sql.gz bytes=" + strconv.Itoa(len(obj)) + " dest=\n"; !strings.Contains(res.stdout, want) {
			t.Errorf("stdout = %q, want the compressed size in %q", res.stdout, want)
		}
		wantNames := []string{"1500.dump", "1600.archive.gz", "1650.sql", "1700.sql.gz", "1703.sql.gz", "1791.sql.gz", "readme"}
		if got := res.names(); !slices.Equal(got, wantNames) {
			t.Errorf("files = %v, want %v", got, wantNames)
		}
		if want := []string{"1701.sql.gz", "1702.sql.gz"}; !slices.Equal(res.expired(), want) {
			t.Errorf("expired = %v, want %v", res.expired(), want)
		}
	})
	t.Run("a mongodb archive is uploaded as it is", func(t *testing.T) {
		res := runUpload(t, uploadCase{ext: ".archive.gz", dump: "already compressed",
			existing: []string{"1700", "1701"}, others: []string{"1500.sql.gz", "1600.dump"}, keep: "1", ts: "1791"})
		if res.code != 0 || res.stderr != "" {
			t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
		}
		if got := string(res.files["1791.archive.gz"]); got != "already compressed" {
			t.Errorf("the object holds %q", got)
		}
		// KEEP counts the new archive: of three, one stays.
		wantNames := []string{"1500.sql.gz", "1600.dump", "1791.archive.gz"}
		if got := res.names(); !slices.Equal(got, wantNames) {
			t.Errorf("files = %v, want %v", got, wantNames)
		}
	})
	t.Run("a failed gzip fails the run before anything is uploaded", func(t *testing.T) {
		r := newDumpRun(t, "rclone", "stat")
		// No dump file: gzip can't read it.
		stdout, _, code := r.run(uploadScript, "NOMAD_JOB_ID=notes_db_backup/periodic-1791", "BACKUP_PATH=b/notes/db",
			"BACKUP_KEEP=3", "BACKUP_EXT=.sql.gz", "BACKUP_GZIP=1")
		if code == 0 || strings.Contains(stdout, BackupDoneMarker) {
			t.Errorf("exit %d, stdout %q: a missing dump must fail the run", code, stdout)
		}
		if _, err := os.Stat(filepath.Join(r.bucket, "b/notes/db/1791.sql.gz")); err == nil {
			t.Error("something was uploaded")
		}
	})
}
