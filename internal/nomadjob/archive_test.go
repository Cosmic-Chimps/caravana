package nomadjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestVolumeArchiveJob compares the job with its golden file and checks what
// the plan says it must be: a batch job beside the app's, the volume mounted
// read-only, the destination only in the job's own variable.
func TestVolumeArchiveJob(t *testing.T) {
	job := VolumeArchiveJob("shop_uploads", "shop", "bucket/backups/shop/volumes", "1791386036", labSettings())
	got, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "volume-archive.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs; run with -update and read the diff", path)
	}

	if *job.ID != "shop_uploads_archive" || *job.Type != "batch" || job.Periodic != nil || len(job.TaskGroups) != 1 {
		t.Fatalf("job = %s %s %+v", *job.ID, *job.Type, job.Periodic)
	}
	if !strings.HasPrefix(*job.ID, AppJobPrefix("shop")) {
		t.Error("the archive job doesn't go with the app's other helper jobs")
	}
	g := job.TaskGroups[0]
	vr := g.Volumes["data"]
	if vr == nil || vr.Type != "host" || vr.Source != "shop_uploads" || vr.AccessMode != "single-node-single-writer" || vr.AttachmentMode != "file-system" {
		t.Errorf("volume request = %+v", vr)
	}
	if *g.RestartPolicy.Attempts != 0 || *g.ReschedulePolicy.Attempts != 0 || len(g.Tasks) != 1 {
		t.Errorf("group = %+v", g)
	}
	task := g.Tasks[0]
	if task.Name != ArchiveTaskName || task.Lifecycle != nil || len(task.VolumeMounts) != 1 ||
		*task.VolumeMounts[0].Destination != "/data" || !*task.VolumeMounts[0].ReadOnly {
		t.Errorf("task = %+v", task)
	}
	if !strings.Contains(*task.Templates[0].EmbeddedTmpl, `nomadVar "nomad/jobs/shop_uploads_archive"`) {
		t.Error("the task doesn't read the job's own variable")
	}
	if strings.Contains(task.Config["args"].([]string)[0], "${") {
		t.Error("the script has ${, which Nomad would interpolate")
	}
	if bytes.Contains(got, []byte("SECRET")) && bytes.Contains(got, []byte("sk-")) {
		t.Error("the job holds a destination key")
	}
	if job.Meta["caravana.app"] != "shop" || job.Meta["caravana.volume"] != "shop_uploads" {
		t.Errorf("meta = %v", job.Meta)
	}

	t.Run("a volume that doesn't read as an app's", func(t *testing.T) {
		j := VolumeArchiveJob("gone_data", "", "bucket/volumes", "1791386036", labSettings())
		if _, has := j.Meta["caravana.app"]; has || j.Meta["caravana.volume"] != "gone_data" {
			t.Errorf("meta = %v", j.Meta)
		}
	})
}

func TestParseArchiveOutput(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  ArchiveOutcome
	}{
		{"nothing printed", nil, ArchiveOutcome{}},
		{"unrelated lines", []string{"tar: removing leading /"}, ArchiveOutcome{}},
		{"ok", []string{"noise", "caravana-archive: ok object=b/shop/volumes/17/shop_uploads.tar.gz bytes=2048"},
			ArchiveOutcome{Found: true, OK: true, Object: "b/shop/volumes/17/shop_uploads.tar.gz", Bytes: 2048}},
		{"failed", []string{"caravana-archive: failed: the upload: Failed to rcat: boom"},
			ArchiveOutcome{Found: true, Message: "the upload: Failed to rcat: boom"}},
		{"the last marker decides", []string{"caravana-archive: ok object=a bytes=1", "caravana-archive: failed: late"},
			ArchiveOutcome{Found: true, Message: "late"}},
		{"ok without a size isn't trusted", []string{"caravana-archive: ok object=a"},
			ArchiveOutcome{Found: true, Message: "the archive task's answer has no object or size"}},
		{"ok with a bad size isn't trusted", []string{"caravana-archive: ok object=a bytes=-4"},
			ArchiveOutcome{Found: true, Message: "the archive task's answer has no object or size"}},
		{"a marker that says neither", []string{"caravana-archive: hello"},
			ArchiveOutcome{Found: true, Message: "the archive task said something Caravana can't read"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseArchiveOutput(tc.lines); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// archiveRun is one run of ArchiveScript under a real sh with the fake tools
// of the volume scripts (see newScriptRun).
type archiveRun struct {
	*scriptRun
	stdout string
	exit   int
}

// runArchive runs the script on the volume "data" of a new scriptRun, with the
// job's env and extra "KEY=value" entries laid over it. The secret key the fake
// rclone insists on is in the environment, as the template would put it.
func runArchive(t *testing.T, env ...string) *archiveRun {
	t.Helper()
	r := newScriptRun(t)
	if _, err := exec.LookPath("mktemp"); err != nil {
		t.Skipf("mktemp not found: %v", err)
	}
	cmd := exec.Command("sh", "-c", ArchiveScript)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ARCHIVE_ROOT="+filepath.Join(r.root, "data"), "BACKUP_PATH=b/shop/volumes", "TS=1791", "VOLUME=shop_uploads",
		"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY=sk-archive-secret", "FAKE_SECRET=sk-archive-secret",
		"FAKE_BUCKET="+r.bucket, "FAKE_LOG="+r.log)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.Output()
	exit := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run the script: %v", err)
	}
	return &archiveRun{scriptRun: r, stdout: string(out), exit: exit}
}

// lastLine is the script's last line of standard output.
func (r *archiveRun) lastLine() string {
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestArchiveScriptOK(t *testing.T) {
	r := runArchive(t)
	if r.exit != 0 {
		t.Fatalf("exit = %d, output %q", r.exit, r.stdout)
	}
	const object = "b/shop/volumes/1791/shop_uploads.tar.gz"
	info, err := os.Stat(filepath.Join(r.bucket, object))
	if err != nil {
		t.Fatalf("the archive isn't in the bucket: %v (bucket %v)", err, r.bucketFiles())
	}
	if want := "caravana-archive: ok object=" + object + " bytes=" + strconv.FormatInt(info.Size(), 10); r.lastLine() != want || info.Size() == 0 {
		t.Errorf("last line %q, want %q", r.lastLine(), want)
	}
	list, err := exec.Command("tar", "-tzf", filepath.Join(r.bucket, object)).Output()
	if err != nil || !strings.Contains(string(list), "file") {
		t.Errorf("the archive lists %q (%v), want the volume's file", list, err)
	}
	// Every call to tar and rclone ran under timeout (the fakes refuse
	// otherwise), none for longer than the script's limit.
	for _, prog := range []string{"tar", "rclone"} {
		secs := r.timeouts()[prog]
		if len(secs) == 0 {
			t.Errorf("no call to %s ran under timeout", prog)
		}
		for _, s := range secs {
			if s < 1 || s > ArchiveLimitSeconds {
				t.Errorf("timeout for %s = %d seconds, want 1 to %d", prog, s, ArchiveLimitSeconds)
			}
		}
	}
	if strings.Contains(r.stdout, "sk-archive-secret") {
		t.Error("the output holds the secret key")
	}
}

func TestArchiveScriptFailures(t *testing.T) {
	tests := []struct {
		name        string
		env         []string
		wantMessage string
		wantDeleted bool // the partial object is removed from the destination
	}{
		{"the upload fails", []string{"FAKE_FAIL_RCAT=.tar.gz"}, "the upload: Failed to rcat: boom", true},
		{"tar fails", []string{"FAKE_FAIL_TAR=cannot read the volume"}, "the archive: tar: cannot read the volume", true},
		{"the size can't be read", []string{"FAKE_FAIL_CMD=size"}, "reading the size of the archive: Failed to size: injected failure", true},
		{"the wrong secret key", []string{"FAKE_SECRET=another-key"}, "the upload: AccessDenied: the secret key is wrong", true},
		{"the upload runs out of time", []string{"FAKE_TIMEOUT_EXPIRE=rclone"}, "the upload timed out", true},
		{"no destination path", []string{"BACKUP_PATH="}, "the job has no BACKUP_PATH", false},
		{"no timestamp", []string{"TS="}, "the job has no TS", false},
		{"a timestamp that isn't a number", []string{"TS=17x"}, "the job's timestamp is not a number", false},
		{"no volume name", []string{"VOLUME="}, "the job has no VOLUME", false},
		{"the volume isn't mounted", []string{"ARCHIVE_ROOT=/nonexistent-for-the-test"}, "the volume is not mounted", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runArchive(t, tc.env...)
			if r.exit != 1 {
				t.Fatalf("exit = %d, output %q; a failure exits 1", r.exit, r.stdout)
			}
			if want := "caravana-archive: failed: " + tc.wantMessage; r.lastLine() != want {
				t.Errorf("last line %q, want %q", r.lastLine(), want)
			}
			if tc.wantDeleted && slices.ContainsFunc(r.bucketFiles(), func(f string) bool { return strings.HasSuffix(f, ".tar.gz") }) {
				t.Errorf("a partial archive is left in the bucket: %v", r.bucketFiles())
			}
			if strings.Contains(r.stdout, "sk-archive-secret") {
				t.Error("the output holds the secret key")
			}
		})
	}

	t.Run("a long message is cut to 200 characters", func(t *testing.T) {
		r := runArchive(t, "FAKE_FAIL_TAR="+strings.Repeat("x", 500))
		msg, ok := strings.CutPrefix(r.lastLine(), "caravana-archive: failed: ")
		if r.exit != 1 || !ok || len(msg) != 200 {
			t.Errorf("exit %d, message length %d (%q)", r.exit, len(msg), r.lastLine())
		}
	})
}

func TestArchiveLimitConstantsAgree(t *testing.T) {
	if archiveLimit != strconv.Itoa(ArchiveLimitSeconds) {
		t.Errorf("archiveLimit %q and ArchiveLimitSeconds %d differ", archiveLimit, ArchiveLimitSeconds)
	}
}
