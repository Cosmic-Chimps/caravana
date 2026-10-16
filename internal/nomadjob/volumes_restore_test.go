package nomadjob

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The restore run of VolumeScript, under a real sh with a fake rclone, timeout
// and tar (see newScriptRun). The volumes are "data" and "logs".

// restoreRequest is a valid restore request of the backup at b/kuma/web/1791/.
func restoreRequest(kv ...string) *string {
	return request(append([]string{
		"REQUEST", "web-2", "ACTION", "restore", "OBJECT", "b/kuma/web/1791/", "NOT_AFTER", inAnHour(),
		"LIMIT", "600",
	}, kv...)...)
}

// tree is a set of files by path under a volume. A path that ends in "/" is
// an empty directory, a value that starts with "->" is a symbolic link to
// what follows, and anything else is a file with that text.
type tree map[string]string

// write makes the tree under dir, which it empties first.
func (tr tree) write(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, text := range tr {
		p := filepath.Join(dir, path)
		var err error
		switch {
		case strings.HasSuffix(path, "/"):
			err = os.MkdirAll(p, 0o755)
		case strings.HasPrefix(text, "->"):
			if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
				err = os.Symlink(strings.TrimPrefix(text, "->"), p)
			}
		default:
			if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
				err = os.WriteFile(p, []byte(text), 0o644)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot reads what is under dir back into a tree, hidden files included.
func snapshot(t *testing.T, dir string) tree {
	t.Helper()
	got := tree{}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return got
	}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			got[rel] = "->" + target
			return err
		case d.IsDir():
			// Only an empty directory is listed: a directory with files is
			// there because of them.
			entries, err := os.ReadDir(p)
			if err == nil && len(entries) == 0 {
				got[rel+"/"] = ""
			}
			return err
		default:
			b, err := os.ReadFile(p)
			got[rel] = string(b)
			return err
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// backupOf archives tr the way the backup run does and puts it in the fake
// bucket as the volume's archive of the backup at b/kuma/web/1791/.
func (r *scriptRun) backupOf(volume string, tr tree) {
	r.t.Helper()
	src := filepath.Join(r.dir, "archive-source-"+volume)
	tr.write(r.t, src)
	dest := filepath.Join(r.bucket, "b/kuma/web/1791", volume+".tar.gz")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if out, err := exec.Command("tar", "-czf", dest, "-C", src, ".").CombinedOutput(); err != nil {
		r.t.Fatalf("tar: %v\n%s", err, out)
	}
}

// volume returns the directory of the named volume.
func (r *scriptRun) volume(name string) string { return filepath.Join(r.root, name) }

// state is what the volumes hold before a run, for the checks that a
// failed restore left them as they were.
func (r *scriptRun) state() map[string]tree {
	r.t.Helper()
	return map[string]tree{"data": snapshot(r.t, r.volume("data")), "logs": snapshot(r.t, r.volume("logs"))}
}

// A tree with the awkward things: hidden files, a hidden directory, a name
// that starts with two dots, nesting, an empty directory and a symbolic link
// that points nowhere.
func awkwardTree(tag string) tree {
	return tree{
		"file":              tag + " file",
		".hidden":           tag + " hidden",
		"..twodots":         tag + " two dots",
		".config/app.ini":   tag + " ini",
		"sub/deep/leaf.txt": tag + " leaf",
		"empty/":            "",
		"dangling":          "->/nowhere/" + tag,
	}
}

func TestVolumeScriptRestore(t *testing.T) {
	tests := []struct {
		name    string
		object  string
		current map[string]tree // the volumes before; nil leaves the fixtures
		backup  map[string]tree // what the backup holds
	}{
		{
			name:    "files are replaced, hidden ones too, and files not in the archive are gone",
			object:  "b/kuma/web/1791/",
			current: map[string]tree{"data": awkwardTree("new"), "logs": {"extra": "x", ".extra": "y"}},
			backup: map[string]tree{
				"data": {"file": "A", ".dot": "dot", "sub/old.txt": "old"},
				"logs": {"app.log": "line"},
			},
		},
		{
			name:    "an empty volume is fine",
			object:  "b/kuma/web/1791/",
			current: map[string]tree{"data": {}, "logs": {"x": "x"}},
			backup:  map[string]tree{"data": {"file": "A"}, "logs": {"app.log": "line"}},
		},
		{
			name:    "an empty backup empties the volume",
			object:  "b/kuma/web/1791/",
			current: map[string]tree{"data": awkwardTree("new"), "logs": {"x": "x"}},
			backup:  map[string]tree{"data": {}, "logs": {"app.log": "line"}},
		},
		{
			name:    "the object without its trailing slash",
			object:  "b/kuma/web/1791",
			current: map[string]tree{"data": {"b": "B"}, "logs": {"b": "B"}},
			backup:  map[string]tree{"data": {"file": "A"}, "logs": {"file": "A"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newScriptRun(t)
			for name, tr := range tt.current {
				tr.write(t, r.volume(name))
			}
			for name, tr := range tt.backup {
				r.backupOf(name, tr)
			}
			r.run(restoreRequest("OBJECT", tt.object), r.withDestination()...)

			res := r.result("web-2")
			if !res.Succeeded || res.Message != "" || len(res.Volumes) != 0 || res.Object != "" {
				t.Fatalf("result = %+v, want a plain success", res)
			}
			// Each volume is exactly the archive's tree: nothing of what was
			// there before, and no staging directory.
			for name, want := range tt.backup {
				if got := snapshot(t, r.volume(name)); !maps.Equal(got, want) {
					t.Errorf("volume %s = %v, want %v", name, got, want)
				}
			}
			// The scratch files are gone: only the sizes and the result are left.
			if got := r.files(); !slices.Equal(got, []string{"sizes", "web-2.result"}) {
				t.Errorf("data dir = %v", got)
			}
			// Every call to tar and rclone ran under the run's time limit (the
			// fakes refuse otherwise).
			for _, prog := range []string{"tar", "rclone"} {
				secs := r.timeouts()[prog]
				if len(secs) == 0 {
					t.Errorf("no call to %s ran under timeout", prog)
				}
				for _, s := range secs {
					if s < 1 || s > 600 {
						t.Errorf("timeout for %s = %d seconds, want 1 to 600", prog, s)
					}
				}
			}
		})
	}
}

// A restore that fails leaves every volume as it was: the files, the hidden
// files, and no staging directory.
func TestVolumeScriptRestoreFailures(t *testing.T) {
	tests := []struct {
		name       string
		env        []string
		request    *string
		prep       func(t *testing.T, r *scriptRun)
		wantPrefix string
	}{
		{
			name: "a volume has no archive in the backup",
			prep: func(t *testing.T, r *scriptRun) {
				if err := os.Remove(filepath.Join(r.bucket, "b/kuma/web/1791/logs.tar.gz")); err != nil {
					t.Fatal(err)
				}
			},
			wantPrefix: "download of logs: object not found",
		},
		{
			name: "the first volume has no archive either",
			prep: func(t *testing.T, r *scriptRun) {
				if err := os.RemoveAll(filepath.Join(r.bucket, "b/kuma/web/1791")); err != nil {
					t.Fatal(err)
				}
			},
			wantPrefix: "download of data: object not found",
		},
		{
			name: "a corrupt archive",
			prep: func(t *testing.T, r *scriptRun) {
				r.put("b/kuma/web/1791/logs.tar.gz", "this is not a gzip file at all")
			},
			wantPrefix: "check of the archive of logs: ",
		},
		{
			name: "an archive cut short",
			prep: func(t *testing.T, r *scriptRun) {
				p := filepath.Join(r.bucket, "b/kuma/web/1791/data.tar.gz")
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, b[:len(b)/2], 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantPrefix: "check of the archive of data: ",
		},
		{
			name:       "the second volume's extract fails: both are as they were",
			env:        []string{"FAKE_FAIL_EXTRACT=/vols/logs"},
			wantPrefix: "extract of the archive of logs: tar: unexpected end of file",
		},
		{
			name:       "the first volume's extract fails",
			env:        []string{"FAKE_FAIL_EXTRACT=/vols/data"},
			wantPrefix: "extract of the archive of data: tar: unexpected end of file",
		},
		{
			name:       "a download runs out of time",
			env:        []string{"FAKE_TIMEOUT_EXPIRE=rclone"},
			wantPrefix: "download of data timed out (the limit is 600 seconds)",
		},
		{
			name:       "the wrong secret key",
			env:        []string{"FAKE_SECRET=another-key"},
			wantPrefix: "download of data: AccessDenied",
		},
		{
			name:       "no object in the request",
			request:    restoreRequest("OBJECT", ""),
			wantPrefix: "the request names no backup to restore",
		},
		{
			name: "files left by an earlier restore are not deleted",
			prep: func(t *testing.T, r *scriptRun) {
				tree{"precious": "someone's data"}.write(t, filepath.Join(r.volume("logs"), ".caravana-restore", "old"))
			},
			wantPrefix: "logs has files left in .caravana-restore/old by an earlier restore",
		},
		{
			name: "no volume mounted",
			prep: func(t *testing.T, r *scriptRun) {
				for _, v := range []string{"data", "logs"} {
					if err := os.RemoveAll(r.volume(v)); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantPrefix: "the service has no volume mounted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newScriptRun(t)
			awkwardTree("now").write(t, r.volume("data"))
			tree{"app.log": "now log", ".logrc": "now rc"}.write(t, r.volume("logs"))
			r.backupOf("data", tree{"file": "A"})
			r.backupOf("logs", tree{"app.log": "A"})
			if tt.prep != nil {
				tt.prep(t, r)
			}
			before := r.state()

			req := tt.request
			if req == nil {
				req = restoreRequest()
			}
			env := slices.Clone(tt.env)
			if !slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "FAKE_SECRET=") }) {
				env = append(env, r.withDestination()...)
			}
			r.run(req, env...)

			res := r.result("web-2")
			if res.Succeeded || !strings.HasPrefix(res.Message, tt.wantPrefix) {
				t.Errorf("result = %+v, want failed with a message starting %q", res, tt.wantPrefix)
			}
			after := r.state()
			for name, was := range before {
				if now := after[name]; !maps.Equal(now, was) {
					t.Errorf("volume %s changed:\nbefore %v\nafter  %v", name, was, now)
				}
			}
			if got := r.files(); !slices.Equal(got, []string{"sizes", "web-2.result"}) {
				t.Errorf("data dir = %v, want no scratch files", got)
			}
		})
	}
}

// When a volume can't be put back after a failed extract, the old files are
// kept where a person can find them, and the message says so.
func TestVolumeScriptRestoreRollbackFails(t *testing.T) {
	r := newScriptRun(t)
	tree{"app.log": "now"}.write(t, r.volume("data"))
	tree{"app.log": "now"}.write(t, r.volume("logs"))
	r.backupOf("data", tree{"file": "A"})
	r.backupOf("logs", tree{"file": "A"})

	// A fake mv that works until the extract has failed, then refuses to move
	// anything out of a staging directory's old/, as a full disk would.
	bin := filepath.Join(r.dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	realMv, err := exec.LookPath("mv")
	if err != nil {
		t.Skip(err)
	}
	fakeMv := "#!/bin/sh\n" +
		"case \"$1\" in */.caravana-restore/old/*) if [ -e \"$FLAG\" ]; then echo \"mv: no space left\" >&2; exit 1; fi ;; esac\n" +
		"exec " + realMv + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "mv"), []byte(fakeMv), 0o755); err != nil {
		t.Fatal(err)
	}
	// The fake tar's failing extract is the moment mv starts to fail: it
	// leaves the flag file beside the volume.
	flag := filepath.Join(r.dir, "mv-fails")
	failingTar := "#!/bin/sh\n[ \"$1\" != -xzf ] || { touch \"$FLAG\"; echo \"tar: boom\" >&2; exit 2; }\nexec " +
		filepath.Join(r.bin, "tar") + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tar"), []byte(failingTar), 0o755); err != nil {
		t.Fatal(err)
	}
	r.run(restoreRequest(), append(r.withDestination(), "FLAG="+flag, "PATH="+bin+string(os.PathListSeparator)+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"))...)

	res := r.result("web-2")
	if res.Succeeded || !strings.HasPrefix(res.Message, "extract of the archive of data: tar: boom; the old files could not all be put back") {
		t.Fatalf("result = %+v", res)
	}
	// Nothing was deleted: the old files are still in the staging directory.
	if got := snapshot(t, filepath.Join(r.volume("data"), ".caravana-restore", "old")); got["app.log"] != "now" {
		t.Errorf("the old files are gone: %v", got)
	}
}

// A restore that was asked for twice never runs twice: a restore replaces newer
// data with an old backup, so a start of the copy with the request still
// rendered must leave the volumes alone.
func TestVolumeScriptRestoreNeverRepeats(t *testing.T) {
	r := newScriptRun(t)
	tree{"file": "B"}.write(t, r.volume("data"))
	tree{"file": "B"}.write(t, r.volume("logs"))
	r.backupOf("data", tree{"file": "A"})
	r.backupOf("logs", tree{"file": "A"})
	env := r.withDestination()
	r.run(restoreRequest(), env...)
	if !r.result("web-2").Succeeded {
		t.Fatalf("result = %+v", r.result("web-2"))
	}

	// The service writes new data, then the copy starts again with the same
	// request still rendered.
	tree{"file": "newer"}.write(t, r.volume("data"))
	r.run(nil, env...)
	if got := snapshot(t, r.volume("data")); !maps.Equal(got, tree{"file": "newer"}) {
		t.Errorf("a repeated start restored again: %v", got)
	}
}
