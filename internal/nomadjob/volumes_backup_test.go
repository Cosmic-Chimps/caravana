package nomadjob

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The backup run of VolumeScript, under a real sh with a fake rclone and
// timeout (see newScriptRun).

// backupRequest is a valid backup request, with kv laid over it: the file is
// sourced top to bottom, so a later assignment wins.
func backupRequest(kv ...string) *string {
	return request(append([]string{
		"REQUEST", "web-1", "ACTION", "backup", "TS", "1791", "NOT_AFTER", inAnHour(),
		"LIMIT", "600", "KEEP", "3", "BACKUP_PATH", "b/kuma/web",
	}, kv...)...)
}

// withDestination writes the destination file the second template renders, with
// the secret key the fake rclone insists on, and returns the environment that
// makes the fake rclone insist on it. The key is in no other file.
func (r *scriptRun) withDestination() []string {
	r.t.Helper()
	text := "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY='sk-from-the-secrets-file'\nRCLONE_CONFIG_DEST_TYPE='s3'\n"
	if err := os.WriteFile(filepath.Join(r.dir, "destination.env"), []byte(text), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return []string{"FAKE_SECRET=sk-from-the-secrets-file"}
}

// put makes a file in the fake bucket.
func (r *scriptRun) put(path, text string) {
	r.t.Helper()
	p := filepath.Join(r.bucket, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// bucketFiles lists every file in the fake bucket, as paths under it.
func (r *scriptRun) bucketFiles() []string {
	r.t.Helper()
	var out []string
	err := filepath.WalkDir(r.bucket, func(p string, d os.DirEntry, err error) error {
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

// result reads and parses the run's result file.
func (r *scriptRun) result(id string) VolumeResult {
	r.t.Helper()
	res, err := ParseVolumeResult(r.read(id + ".result"))
	if err != nil {
		r.t.Fatalf("result: %v", err)
	}
	return res
}

// timeouts returns the seconds the script gave to each wrapped call, by
// program.
func (r *scriptRun) timeouts() map[string][]int {
	r.t.Helper()
	b, err := os.ReadFile(r.log)
	if err != nil {
		return nil
	}
	out := map[string][]int{}
	for line := range strings.Lines(string(b)) {
		var secs int
		var prog string
		if n, _ := fmt.Sscanf(line, "timeout %d %s", &secs, &prog); n == 2 {
			out[prog] = append(out[prog], secs)
		}
	}
	return out
}

func TestVolumeScriptBackup(t *testing.T) {
	r := newScriptRun(t)
	// What tar must leave out: a restore's staging directory.
	if err := os.MkdirAll(filepath.Join(r.root, "data", ".caravana-restore"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "data", ".caravana-restore", "old"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.run(backupRequest(), r.withDestination()...)

	res := r.result("web-1")
	if !res.Succeeded || res.Message != "" || res.Object != "b/kuma/web/1791/" {
		t.Fatalf("result = %+v, want a success with the object b/kuma/web/1791/", res)
	}
	if want := []string{"b/kuma/web/1791/data.tar.gz", "b/kuma/web/1791/logs.tar.gz"}; !slices.Equal(r.bucketFiles(), want) {
		t.Fatalf("bucket = %v, want %v", r.bucketFiles(), want)
	}
	if len(res.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want data and logs", res.Volumes)
	}
	sizes := ParseVolumeSizes(r.read("sizes"))
	for _, v := range res.Volumes {
		info, err := os.Stat(filepath.Join(r.bucket, "b/kuma/web/1791", v.Name+".tar.gz"))
		if err != nil {
			t.Fatal(err)
		}
		if v.Bytes != info.Size() || v.Bytes == 0 {
			t.Errorf("volume %s: bytes = %d, want the archive's %d", v.Name, v.Bytes, info.Size())
		}
		if v.SizeKB != sizes[v.Name] {
			t.Errorf("volume %s: size = %d KB, want %d (what the sizes file says)", v.Name, v.SizeKB, sizes[v.Name])
		}
	}

	// The archive is a gzipped tar of the volume's files, without the staging
	// directory.
	list, err := exec.Command("tar", "-tzf", filepath.Join(r.bucket, "b/kuma/web/1791/data.tar.gz")).Output()
	if err != nil {
		t.Fatalf("tar -tzf: %v", err)
	}
	if got := string(list); !strings.Contains(got, "file") || strings.Contains(got, "caravana-restore") {
		t.Errorf("archive lists:\n%s\nwant the volume's file and nothing of .caravana-restore", got)
	}

	// Only the sizes and the result are left behind: no scratch files.
	if got := r.files(); !slices.Equal(got, []string{"sizes", "web-1.result"}) {
		t.Errorf("data dir = %v", got)
	}
	// Every call to tar and rclone ran under timeout (the fakes refuse
	// otherwise), with what the run had left.
	for _, prog := range []string{"tar", "rclone"} {
		secs := r.timeouts()[prog]
		if len(secs) == 0 {
			t.Errorf("no call to %s ran under timeout", prog)
		}
		for _, s := range secs {
			if s < 1 || s > 600 {
				t.Errorf("timeout for %s = %d seconds, want 1 to 600 (the limit)", prog, s)
			}
		}
	}
}

func TestVolumeScriptBackupFailures(t *testing.T) {
	tests := []struct {
		name    string
		env     []string
		request *string
		prep    func(t *testing.T, r *scriptRun)
		wantMsg string
	}{
		{
			name: "an upload fails after the first volume went up", env: []string{"FAKE_FAIL_RCAT=logs.tar.gz"},
			wantMsg: "upload of logs: Failed to rcat: boom",
		},
		{
			name: "tar fails", env: []string{"FAKE_FAIL_TAR=cannot read the volume"},
			wantMsg: "archive of data: tar: cannot read the volume",
		},
		{
			name: "the size can't be read", env: []string{"FAKE_FAIL_CMD=size"},
			wantMsg: "size of the archive of data: Failed to size: injected failure",
		},
		{
			name: "the wrong secret key", env: []string{"FAKE_SECRET=another-key"},
			wantMsg: "upload of data: AccessDenied: the secret key is wrong",
		},
		{
			name: "an upload runs out of time", env: []string{"FAKE_TIMEOUT_EXPIRE=rclone"},
			wantMsg: "upload of data timed out (the limit is 600 seconds)",
		},
		{
			name: "no volume mounted",
			prep: func(t *testing.T, r *scriptRun) {
				for _, v := range []string{"data", "logs"} {
					if err := os.RemoveAll(filepath.Join(r.root, v)); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantMsg: "the service has no volume mounted",
		},
		{
			name:    "no TS",
			request: request("REQUEST", "web-1", "ACTION", "backup", "NOT_AFTER", inAnHour(), "BACKUP_PATH", "b"),
			wantMsg: "the request has no timestamp",
		},
		{name: "a TS that is a path", request: backupRequest("TS", "../x"), wantMsg: "the request has no timestamp"},
		{name: "no backup path", request: backupRequest("BACKUP_PATH", ""), wantMsg: "the request has no backup destination"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newScriptRun(t)
			r.put("b/kuma/web/1700/data.tar.gz", "an older backup")
			if tt.prep != nil {
				tt.prep(t, r)
			}
			req := tt.request
			if req == nil {
				req = backupRequest()
			}
			env := slices.Clone(tt.env)
			if !slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "FAKE_SECRET=") }) {
				env = append(env, r.withDestination()...)
			}
			r.run(req, env...)

			res := r.result("web-1")
			if res.Succeeded || res.Message != tt.wantMsg {
				t.Errorf("result = %+v, want failed with %q", res, tt.wantMsg)
			}
			if len(res.Volumes) != 0 || len(res.Expired) != 0 || res.Object != "" {
				t.Errorf("a failed run reports volumes, an object or expirations: %+v", res)
			}
			// No partial backup is left, and the older one is untouched.
			if got := r.bucketFiles(); !slices.Equal(got, []string{"b/kuma/web/1700/data.tar.gz"}) {
				t.Errorf("bucket = %v, want only the older backup", got)
			}
			if got := r.files(); !slices.Equal(got, []string{"sizes", "web-1.result"}) {
				t.Errorf("data dir = %v, want no scratch files", got)
			}
		})
	}
}

func TestVolumeScriptRetention(t *testing.T) {
	tests := []struct {
		name        string
		existing    []string // timestamps already in the bucket
		keep        string
		ts          string
		env         []string
		wantLeft    []string // timestamps in the bucket afterwards
		wantExpired []string
		wantMsg     string
	}{
		{name: "the oldest beyond KEEP go", existing: []string{"1700", "1701", "1702", "1703"}, keep: "3", ts: "1791",
			wantLeft: []string{"1702", "1703", "1791"}, wantExpired: []string{"1700", "1701"}},
		{name: "within KEEP nothing goes", existing: []string{"1700", "1701"}, keep: "3", ts: "1791",
			wantLeft: []string{"1700", "1701", "1791"}},
		{name: "no KEEP deletes nothing", existing: []string{"1700", "1701", "1702"}, keep: "", ts: "1791",
			wantLeft: []string{"1700", "1701", "1702", "1791"}},
		{name: "a KEEP that is no number deletes nothing", existing: []string{"1700", "1701", "1702"}, keep: "many", ts: "1791",
			wantLeft: []string{"1700", "1701", "1702", "1791"}},
		{name: "the new backup is never the one deleted", existing: []string{"1800", "1801"}, keep: "1", ts: "1791",
			wantLeft: []string{"1791", "1801"}, wantExpired: []string{"1800"}},
		{name: "listing fails: the backup stays and says so", existing: []string{"1700"}, keep: "1", ts: "1791",
			env:      []string{"FAKE_FAIL_CMD=lsf"},
			wantLeft: []string{"1700", "1791"},
			wantMsg:  "the backup is kept, but old backups were not removed: listing the old backups: Failed to lsf: injected failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newScriptRun(t)
			for _, ts := range tt.existing {
				r.put("b/kuma/web/"+ts+"/data.tar.gz", "old "+ts)
			}
			// Other things under the path are not backups, and stay.
			r.put("b/kuma/web/notes/readme", "keep me")
			r.put("b/kuma/web/1500.dump", "a postgres dump")
			env := append(slices.Clone(tt.env), r.withDestination()...)
			r.run(backupRequest("KEEP", tt.keep, "TS", tt.ts), env...)

			res := r.result("web-1")
			if !res.Succeeded || res.Message != tt.wantMsg {
				t.Fatalf("result = %+v, want a success with message %q", res, tt.wantMsg)
			}
			if !slices.Equal(res.Expired, tt.wantExpired) {
				t.Errorf("expired = %v, want %v", res.Expired, tt.wantExpired)
			}
			var left []string
			for _, f := range r.bucketFiles() {
				rest, ok := strings.CutPrefix(f, "b/kuma/web/")
				if !ok {
					continue
				}
				ts, _, ok := strings.Cut(rest, "/")
				if ok && ts != "notes" && !slices.Contains(left, ts) {
					left = append(left, ts)
				}
			}
			if !slices.Equal(left, tt.wantLeft) {
				t.Errorf("backups left = %v, want %v", left, tt.wantLeft)
			}
			for _, f := range []string{"b/kuma/web/notes/readme", "b/kuma/web/1500.dump"} {
				if !slices.Contains(r.bucketFiles(), f) {
					t.Errorf("retention deleted %s, which isn't a backup directory", f)
				}
			}
		})
	}
}

// A request that is too old, or whose run is done, uploads nothing.
func TestVolumeScriptBackupIgnored(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		r := newScriptRun(t)
		r.run(backupRequest("NOT_AFTER", anHourAgo()), r.withDestination()...)
		if got := r.bucketFiles(); len(got) != 0 {
			t.Errorf("an expired request uploaded %v", got)
		}
		if got := r.files(); !slices.Equal(got, []string{"sizes"}) {
			t.Errorf("data dir = %v", got)
		}
	})
	t.Run("the result is already there", func(t *testing.T) {
		r := newScriptRun(t)
		env := r.withDestination()
		r.run(backupRequest(), env...)
		if first := r.bucketFiles(); len(first) != 2 {
			t.Fatalf("first run uploaded %v", first)
		}
		// The copy starts again: the request is still rendered and the result
		// is still there. Nothing happens, though the bucket was emptied.
		if err := os.RemoveAll(r.bucket); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(r.bucket, 0o755); err != nil {
			t.Fatal(err)
		}
		r.run(nil, env...)
		if got := r.bucketFiles(); len(got) != 0 {
			t.Errorf("a repeated start uploaded %v", got)
		}
	})
}

// A LIMIT that is no number falls back to half an hour, and no call ever gets
// more than the limit, or less than a second.
func TestVolumeScriptTimeLimit(t *testing.T) {
	for name, tt := range map[string]struct {
		limit    string
		min, max int
	}{
		"a limit":              {"600", 1, 600},
		"a short limit":        {"1", 1, 1},
		"no number":            {"soon", 1, 1800},
		"zero means the limit": {"0", 1, 1800},
	} {
		t.Run(name, func(t *testing.T) {
			r := newScriptRun(t)
			r.run(backupRequest("LIMIT", tt.limit), r.withDestination()...)
			if !r.result("web-1").Succeeded {
				t.Fatalf("result = %+v", r.result("web-1"))
			}
			for prog, list := range r.timeouts() {
				for _, secs := range list {
					if secs < tt.min || secs > tt.max {
						t.Errorf("timeout for %s = %d, want %d to %d", prog, secs, tt.min, tt.max)
					}
				}
			}
		})
	}
}
