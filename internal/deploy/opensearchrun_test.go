package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// osPassword is a cluster password no test may find anywhere but on a standard input.
const osPassword = "pw-sentinel-061-do-not-leak"

// openSearchSvc is whoami's web service with a snapshot volume and an opensearch
// backup into /snapshots/os (a folder inside the volume).
func openSearchSvc() app.Service {
	a := withVolume("snapshots")
	a.Services[0].Backup = &app.Backup{Engine: app.BackupOpenSearch, Repository: "/snapshots/os", Keep: 4}
	return a.Services[0]
}

func TestOpenSearchSteps(t *testing.T) {
	ctx := context.Background()
	svc := openSearchSvc()
	login := OpenSearchLogin{User: "admin", Password: osPassword}
	port := "80" // whoami's container port

	t.Run("prepare: in the service's own task, the owner read from its marker", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-opensearch-owner uid=1000 gid=0\n"}
		})
		owner, err := d.OpenSearchPrepare(ctx, id, svc)
		if err != nil || owner != "1000:0" {
			t.Fatalf("OpenSearchPrepare = %q, %v; want 1000:0", owner, err)
		}
		got := onlyExec(t, nomad)
		want := nomadjob.OpenSearchCommand(nomadjob.OpenSearchPrepareScript, "/snapshots/os")
		if got.Task != "web" || !slices.Equal(got.Command, want) || got.Stdin != "" || got.TTY {
			t.Errorf("exec = task %q, command %q, stdin %q; want the service's task and %q", got.Task, got.Command, got.Stdin, want)
		}
	})

	t.Run("prepare asks once more when the exit came before the output", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		calls := 0
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			calls++
			if calls == 1 {
				return deploytest.ExecReply{}
			}
			return deploytest.ExecReply{Stdout: "caravana-opensearch-owner uid=1000 gid=1000\n"}
		})
		owner, err := d.OpenSearchPrepare(ctx, id, svc)
		if err != nil || owner != "1000:1000" || calls != 2 {
			t.Errorf("OpenSearchPrepare = %q, %v after %d calls; want 1000:1000 after 2", owner, err, calls)
		}
	})

	t.Run("prepare that never names the user fails, after two retries", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		calls := 0
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			calls++
			return deploytest.ExecReply{Stdout: "caravana-opensearch-owner uid=root gid=0\n"}
		})
		_, err := d.OpenSearchPrepare(ctx, id, svc)
		var se *SQLiteError
		if !errors.As(err, &se) || se.Message != "prepare: the service's user could not be read" || calls != 3 {
			t.Errorf("err = %v after %d calls", err, calls)
		}
	})

	t.Run("prepare's failure is the script's last line", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{ExitCode: 1, Stderr: "prepare: could not make the repository folder /snapshots/os\n"}
		})
		_, err := d.OpenSearchPrepare(ctx, id, svc)
		var se *SQLiteError
		if !errors.As(err, &se) || se.Message != "prepare: could not make the repository folder /snapshots/os" {
			t.Errorf("err = %v", err)
		}
		if got := len(nomad.Execs()); got != 1 {
			t.Errorf("execs = %d, want 1: a failed script isn't asked again", got)
		}
	})

	t.Run("an owner that isn't two numbers never reaches a command", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		if err := d.OpenSearchClean(ctx, id, svc, "1000:$(id)", true); err == nil {
			t.Error("clean took a bad owner")
		}
		if _, err := d.OpenSearchDownload(ctx, id, "web-2", "b/x.opensearch.tar.gz", svc, sqliteDest(), ""); err == nil {
			t.Error("download took no owner")
		}
		if len(nomad.Execs()) != 0 {
			t.Errorf("execs = %d, want none", len(nomad.Execs()))
		}
	})

	t.Run("clean", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		if err := d.OpenSearchClean(ctx, id, svc, "1000:0", true); err != nil {
			t.Fatal(err)
		}
		got := onlyExec(t, nomad)
		want := nomadjob.OpenSearchCommand(nomadjob.OpenSearchCleanScript, "/caravana-volumes/snapshots/os", "/alloc/data/caravana/opensearch", "fresh", "1000:0")
		if got.Task != "caravana-upload" || !slices.Equal(got.Command, want) || got.Stdin != "" {
			t.Errorf("exec = task %q, command %q, stdin %q; want caravana-upload and %q", got.Task, got.Command, got.Stdin, want)
		}
		d, _, nomad = newTestDeployer(t)
		id = execCopy(nomad, "running")
		if err := d.OpenSearchClean(ctx, id, svc, "1000:0", false); err != nil {
			t.Fatal(err)
		}
		if cmd := onlyExec(t, nomad).Command; cmd[len(cmd)-2] != "keep" || cmd[len(cmd)-1] != "1000:0" {
			t.Errorf("mode and owner = %q, want keep 1000:0", cmd[len(cmd)-2:])
		}
	})

	t.Run("snapshot: in the service's own task, the login on standard input only", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-opensearch-snapshot indices=3\n"}
		})
		out, err := d.OpenSearchSnapshot(ctx, id, svc, "web-1791", "1791", login)
		if err != nil || out.Indices != 3 {
			t.Fatalf("OpenSearchSnapshot = %+v, %v; want 3 indices", out, err)
		}
		got := onlyExec(t, nomad)
		want := nomadjob.OpenSearchCommand(nomadjob.OpenSearchSnapshotScript, "/snapshots/os", "web-1791", "1791", port, "1800",
			"/alloc/data/caravana/opensearch/web-1791.snapshot.result")
		if got.Task != "web" || !slices.Equal(got.Command, want) || got.TTY {
			t.Errorf("exec = task %q, command %q; want the service's task and %q", got.Task, got.Command, want)
		}
		if got.Stdin != "OPENSEARCH_USER=admin\nOPENSEARCH_PASSWORD="+osPassword+"\n" {
			t.Errorf("stdin = %q", got.Stdin)
		}
		for _, arg := range got.Command {
			if strings.Contains(arg, osPassword) {
				t.Errorf("the password is in an argument: %q", arg)
			}
		}
	})

	t.Run("snapshot and restore without a login send no standard input", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		if _, err := d.OpenSearchSnapshot(ctx, id, svc, "web-1", "1", OpenSearchLogin{}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.OpenSearchRestore(ctx, id, svc, "web-2", OpenSearchLogin{}); err != nil {
			t.Fatal(err)
		}
		for _, e := range nomad.Execs() {
			if e.Stdin != "" {
				t.Errorf("stdin = %q, want none", e.Stdin)
			}
		}
	})

	t.Run("a login with a line break is refused before anything runs", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		_, err := d.OpenSearchSnapshot(ctx, id, svc, "web-1", "1", OpenSearchLogin{User: "admin", Password: "a\nb"})
		if err == nil || strings.Contains(err.Error(), "a\nb") || len(nomad.Execs()) != 0 {
			t.Errorf("err = %v, execs = %d; want a refusal that doesn't quote the value", err, len(nomad.Execs()))
		}
	})

	t.Run("upload: the destination on standard input, the folder inside the volume", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-backup key=b/whoami/web/1791.opensearch.tar.gz bytes=900\ncaravana-expired 1700.opensearch.tar.gz\n"}
		})
		out, err := d.OpenSearchUpload(ctx, id, "web-1791", "whoami", svc, sqliteDest(), "1791", 4, []string{"1700", "1750"})
		if err != nil || out.Object != "b/whoami/web/1791.opensearch.tar.gz" || out.Bytes != 900 || !slices.Equal(out.Expired, []string{"1700"}) {
			t.Fatalf("OpenSearchUpload = %+v, %v", out, err)
		}
		got := onlyExec(t, nomad)
		wantArgs := []string{"/caravana-volumes/snapshots/os/caravana-web-1791", "backups/caravana/whoami/web", "1791", "4", "1800", "1700 1750",
			"/alloc/data/caravana/opensearch/web-1791.upload.result"}
		if got.Task != "caravana-upload" || got.Command[2] != nomadjob.OpenSearchUploadScript || !slices.Equal(got.Command[4:], wantArgs) {
			t.Errorf("exec = task %q, command %q, want %q", got.Task, got.Command[4:], wantArgs)
		}
		if !strings.Contains(got.Stdin, "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY="+sqliteSentinel+"\n") {
			t.Errorf("stdin lacks the credentials: %q", got.Stdin)
		}
		for _, arg := range got.Command {
			if strings.Contains(arg, sqliteSentinel) {
				t.Error("the secret key is in an argument")
			}
		}
	})

	t.Run("download", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-opensearch-download files=17\n"}
		})
		out, err := d.OpenSearchDownload(ctx, id, "web-2", "b/whoami/web/1791.opensearch.tar.gz", svc, sqliteDest(), "1000:0")
		if err != nil || out.Files != 17 {
			t.Fatalf("OpenSearchDownload = %+v, %v", out, err)
		}
		got := onlyExec(t, nomad)
		wantArgs := []string{"b/whoami/web/1791.opensearch.tar.gz", "/caravana-volumes/snapshots/os/caravana-web-2", "1800",
			"/alloc/data/caravana/opensearch/web-2.download.result", "1000:0"}
		if got.Task != "caravana-upload" || got.Command[2] != nomadjob.OpenSearchDownloadScript || !slices.Equal(got.Command[4:], wantArgs) {
			t.Errorf("exec = task %q, command %q", got.Task, got.Command[4:])
		}
		if !strings.Contains(got.Stdin, "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY="+sqliteSentinel+"\n") {
			t.Errorf("stdin lacks the credentials: %q", got.Stdin)
		}
	})

	t.Run("restore", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-opensearch-restore indices=2\n"}
		})
		out, err := d.OpenSearchRestore(ctx, id, svc, "web-2", login)
		if err != nil || out.Indices != 2 {
			t.Fatalf("OpenSearchRestore = %+v, %v", out, err)
		}
		got := onlyExec(t, nomad)
		want := nomadjob.OpenSearchCommand(nomadjob.OpenSearchRestoreScript, "/snapshots/os", "web-2", port, "1800",
			"/alloc/data/caravana/opensearch/web-2.restore.result")
		if got.Task != "web" || !slices.Equal(got.Command, want) || !strings.Contains(got.Stdin, "OPENSEARCH_PASSWORD="+osPassword) {
			t.Errorf("exec = task %q, command %q, stdin %q", got.Task, got.Command, got.Stdin)
		}
	})

	t.Run("a service without the backup is refused", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		plain := withVolume("snapshots").Services[0]
		if _, err := d.OpenSearchSnapshot(ctx, id, plain, "web-1", "1", OpenSearchLogin{}); err == nil {
			t.Error("a service with no opensearch backup was snapshotted")
		}
		if err := d.OpenSearchClean(ctx, id, plain, "1000:0", true); err == nil {
			t.Error("a service with no opensearch backup was cleaned")
		}
	})
}

func TestOpenSearchStepErrors(t *testing.T) {
	ctx := context.Background()
	svc := openSearchSvc()
	reply := func(stderr string) func(deploytest.ExecRequest) deploytest.ExecReply {
		return func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stderr: stderr, ExitCode: 1}
		}
	}

	t.Run("the script's last line is the reason", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(reply("warming up\nsnapshot: [caravana] location [/x] doesn't match any of the locations specified by path.repo\n"))
		_, err := d.OpenSearchSnapshot(ctx, id, svc, "web-1", "1", OpenSearchLogin{})
		var se *SQLiteError
		if !errors.As(err, &se) || se.Message != "snapshot: [caravana] location [/x] doesn't match any of the locations specified by path.repo" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a password a tool echoed is removed from the reason", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(reply("restore: User [name=admin, password=" + osPassword + "] has no permission\n"))
		_, err := d.OpenSearchRestore(ctx, id, svc, "web-1", OpenSearchLogin{User: "admin", Password: osPassword})
		if err == nil || strings.Contains(err.Error(), osPassword) || !strings.Contains(err.Error(), "***") {
			t.Errorf("err = %v, want the password replaced", err)
		}
	})

	t.Run("a destination's key is removed from the reason", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(reply("upload: bad key " + sqliteSentinel + "\n"))
		_, err := d.OpenSearchUpload(ctx, id, "web-1", "whoami", svc, sqliteDest(), "1", 4, nil)
		if err == nil || strings.Contains(err.Error(), sqliteSentinel) {
			t.Errorf("err = %v, want the key removed", err)
		}
	})

	t.Run("a copy that went away", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		execCopy(nomad, "running")
		if _, err := d.OpenSearchSnapshot(ctx, "gone", svc, "web-1", "1", OpenSearchLogin{}); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("err = %v, want ErrCopyNotFound", err)
		}
	})

	t.Run("a script that ran out of time", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{Hold: true} })
		tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		_, err := d.OpenSearchRestore(tctx, id, svc, "web-1", OpenSearchLogin{})
		var se *SQLiteError
		if !errors.As(err, &se) || se.Message != "restore: timed out" {
			t.Errorf("err = %v, want restore: timed out", err)
		}
	})
}

func TestOpenSearchStepsReadTheResultFile(t *testing.T) {
	ctx := context.Background()
	svc := openSearchSvc()
	exitedFirst := func(stdout string) func(deploytest.ExecRequest) deploytest.ExecReply {
		return func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: stdout, ExitFirst: true}
		}
	}

	t.Run("upload: the exit came before the output", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(exitedFirst("caravana-backup key=b/x/1791.opensearch.tar.gz bytes=900\n"))
		nomad.WriteAllocFile(id, nomadjob.OpenSearchResultFile("web-1", "upload"),
			"caravana-backup key=b/x/1791.opensearch.tar.gz bytes=900\ncaravana-expired 1700.opensearch.tar.gz\n")
		out, err := d.OpenSearchUpload(ctx, id, "web-1", "whoami", svc, sqliteDest(), "1791", 4, nil)
		if err != nil || out.Object != "b/x/1791.opensearch.tar.gz" || !slices.Equal(out.Expired, []string{"1700"}) {
			t.Fatalf("OpenSearchUpload = %+v, %v; want everything the result file says", out, err)
		}
	})

	t.Run("upload: no output and no file is an error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		_, err := d.OpenSearchUpload(ctx, id, "web-1", "whoami", svc, sqliteDest(), "1791", 4, nil)
		var se *SQLiteError
		if !errors.As(err, &se) || se.Message != "upload: the object of the backup is unreadable" {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("download: exit 0 and no output", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.WriteAllocFile(id, nomadjob.OpenSearchResultFile("web-2", "download"), "caravana-opensearch-download files=9\n")
		out, err := d.OpenSearchDownload(ctx, id, "web-2", "b/x/1791.opensearch.tar.gz", svc, sqliteDest(), "1000:0")
		if err != nil || out.Files != 9 {
			t.Fatalf("OpenSearchDownload = %+v, %v; want 9 files from the result file", out, err)
		}
	})

	t.Run("snapshot and restore don't need their result file", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		if _, err := d.OpenSearchSnapshot(ctx, id, svc, "web-1", "1", OpenSearchLogin{}); err != nil {
			t.Errorf("snapshot with no output and no file: %v", err)
		}
		if _, err := d.OpenSearchRestore(ctx, id, svc, "web-1", OpenSearchLogin{}); err != nil {
			t.Errorf("restore with no output and no file: %v", err)
		}
	})
}
