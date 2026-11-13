package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// sqliteSentinel is a secret key no test may find anywhere but on a standard
// input.
const sqliteSentinel = "sk-sentinel-029-do-not-leak"

func sqliteDest() BackupDestination {
	d := testDest
	d.SecretAccessKey = sqliteSentinel
	return d
}

// sqliteSvc is whoami's web service with a volume and a sqlite backup of
// /data/db/app.db.
func sqliteSvc() app.Service {
	a := withVolume("data")
	a.Services[0].Backup = &app.Backup{Engine: app.BackupSQLite, File: "/data/db/app.db", Keep: 4}
	return a.Services[0]
}

// onlyExec returns the one command the fake was asked to run.
func onlyExec(t *testing.T, nomad *deploytest.Nomad) deploytest.ExecRequest {
	t.Helper()
	execs := nomad.Execs()
	if len(execs) != 1 {
		t.Fatalf("execs = %d, want 1", len(execs))
	}
	return execs[0]
}

func TestSQLiteSteps(t *testing.T) {
	ctx := context.Background()
	svc := sqliteSvc()

	t.Run("copy", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-sqlite-copy bytes=8192\n"}
		})
		out, err := d.SQLiteCopy(ctx, id, svc, "web-1791")
		if err != nil || out.Bytes != 8192 {
			t.Fatalf("SQLiteCopy = %+v, %v; want 8192 bytes", out, err)
		}
		got := onlyExec(t, nomad)
		want := nomadjob.SQLiteCommand(nomadjob.SQLiteCopyScript,
			"/caravana-volumes/data/db/app.db", "/data/db/app.db", "/alloc/data/caravana/sqlite/web-1791.db", got.Command[len(got.Command)-1])
		if got.Task != "caravana-sqlite" || !slices.Equal(got.Command, want) || got.Stdin != "" || got.TTY {
			t.Errorf("exec = task %q, command %q, stdin %q; want caravana-sqlite and %q", got.Task, got.Command, got.Stdin, want)
		}
		// The last argument is the run's seconds: no deadline, so the default.
		if last := got.Command[len(got.Command)-1]; last != "1800" {
			t.Errorf("seconds = %q, want 1800", last)
		}
	})

	t.Run("the seconds are what the context has left", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-sqlite-copy bytes=1\n"}
		})
		tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if _, err := d.SQLiteCopy(tctx, id, svc, "web-1"); err != nil {
			t.Fatal(err)
		}
		cmd := onlyExec(t, nomad).Command
		var secs int
		if _, err := fmt.Sscan(cmd[len(cmd)-1], &secs); err != nil || secs < 85 || secs > 90 {
			t.Errorf("seconds = %q, want about 90", cmd[len(cmd)-1])
		}
	})

	t.Run("upload: the credentials are on the standard input only", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-backup key=backups/caravana/whoami/web/1791.sqlite.gz bytes=900\ncaravana-expired 1700.sqlite.gz\ncaravana-warning slow\n"}
		})
		out, err := d.SQLiteUpload(ctx, id, "web-1791", "whoami", svc, sqliteDest(), "1791", 4, []string{"1700", "1750"})
		if err != nil {
			t.Fatal(err)
		}
		if out.Object != "backups/caravana/whoami/web/1791.sqlite.gz" || out.Bytes != 900 || !slices.Equal(out.Expired, []string{"1700"}) || out.Warning != "slow" {
			t.Errorf("output = %+v", out)
		}
		got := onlyExec(t, nomad)
		if got.Task != "caravana-upload" {
			t.Errorf("task = %q, want caravana-upload", got.Task)
		}
		wantArgs := []string{"/alloc/data/caravana/sqlite/web-1791.db", "backups/caravana/whoami/web", "1791", "4"}
		if script := got.Command[2]; script != nomadjob.SQLiteUploadScript || !slices.Equal(got.Command[4:8], wantArgs) {
			t.Errorf("command = %q, want the upload script and %q", got.Command[:8], wantArgs)
		}
		// The last argument is the pin list, after the seconds the run may take.
		if n := len(got.Command); n != 10 || got.Command[9] != "1700 1750" {
			t.Errorf("command = %q, want the pinned timestamps as the sixth argument", got.Command)
		}
		wantStdin := "RCLONE_CONFIG_DEST_ACCESS_KEY_ID=AK\n" +
			"RCLONE_CONFIG_DEST_ENDPOINT=http://10.0.0.5:9000\n" +
			"RCLONE_CONFIG_DEST_FORCE_PATH_STYLE=true\n" +
			"RCLONE_CONFIG_DEST_NO_CHECK_BUCKET=true\n" +
			"RCLONE_CONFIG_DEST_PROVIDER=Other\n" +
			"RCLONE_CONFIG_DEST_REGION=us-east-1\n" +
			"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY=" + sqliteSentinel + "\n" +
			"RCLONE_CONFIG_DEST_TYPE=s3\n"
		if got.Stdin != wantStdin {
			t.Errorf("stdin =\n%s\nwant\n%s", got.Stdin, wantStdin)
		}
		// The secret is on the standard input and in no argument.
		for _, arg := range got.Command {
			if strings.Contains(arg, sqliteSentinel) {
				t.Errorf("the secret key is in an argument: %q", arg)
			}
		}
		if strings.Contains(got.Task, sqliteSentinel) {
			t.Error("the secret key is in the task name")
		}
	})

	t.Run("upload without a keep passes none", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-backup key=b/x.sqlite.gz bytes=1\n"}
		})
		if _, err := d.SQLiteUpload(ctx, id, "web-1", "whoami", svc, sqliteDest(), "1", 0, nil); err != nil {
			t.Fatal(err)
		}
		if got := onlyExec(t, nomad).Command[7]; got != "" {
			t.Errorf("keep = %q, want empty", got)
		}
	})

	t.Run("download", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: "caravana-sqlite-download bytes=12288\n"}
		})
		out, err := d.SQLiteDownload(ctx, id, "web-2", "backups/caravana/whoami/web/1791.sqlite.gz", sqliteDest())
		if err != nil || out.Bytes != 12288 {
			t.Fatalf("SQLiteDownload = %+v, %v", out, err)
		}
		got := onlyExec(t, nomad)
		if got.Task != "caravana-upload" || got.Command[2] != nomadjob.SQLiteDownloadScript ||
			!slices.Equal(got.Command[4:6], []string{"backups/caravana/whoami/web/1791.sqlite.gz", "/alloc/data/caravana/sqlite/web-2.db"}) {
			t.Errorf("exec = task %q, command %q", got.Task, got.Command[:6])
		}
		if !strings.Contains(got.Stdin, "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY="+sqliteSentinel+"\n") {
			t.Errorf("stdin lacks the credentials: %q", got.Stdin)
		}
		for _, arg := range got.Command {
			if strings.Contains(arg, sqliteSentinel) {
				t.Errorf("the secret key is in an argument")
			}
		}
	})

	t.Run("stage", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		if err := d.SQLiteStage(ctx, id, svc, "web-2"); err != nil {
			t.Fatal(err)
		}
		got := onlyExec(t, nomad)
		if got.Task != "caravana-sqlite" || got.Command[2] != nomadjob.SQLiteStageScript ||
			!slices.Equal(got.Command[4:7], []string{"/alloc/data/caravana/sqlite/web-2.db", "/caravana-volumes/data", "web-2"}) {
			t.Errorf("exec = task %q, command %q", got.Task, got.Command[:7])
		}
		if got.Stdin != "" {
			t.Errorf("stage got a standard input: %q", got.Stdin)
		}
	})
}

func TestSQLiteStepErrors(t *testing.T) {
	ctx := context.Background()
	svc := sqliteSvc()

	// A script that exits non-zero: its last line of stderr is the reason.
	failing := func(stderr string, code int) func(deploytest.ExecRequest) deploytest.ExecReply {
		return func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stderr: stderr, ExitCode: code}
		}
	}
	tests := []struct {
		name    string
		reply   func(deploytest.ExecRequest) deploytest.ExecReply
		call    func(d *Deployer, id string) error
		wantMsg string
	}{
		{"copy: the script's reason", failing("noise\ncopy: there is no database file at /data/db/app.db\n", 1),
			func(d *Deployer, id string) error { _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); return err },
			"copy: there is no database file at /data/db/app.db"},
		{"check: a damaged copy", failing("check: the copy is damaged: row 7 missing\n", 1),
			func(d *Deployer, id string) error { _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); return err },
			"check: the copy is damaged: row 7 missing"},
		{"a script that said nothing", failing("", 3),
			func(d *Deployer, id string) error { _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); return err },
			"copy: failed (exit status 3)"},
		{"a line without the step gets it", failing("Segmentation fault\n", 139),
			func(d *Deployer, id string) error { _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); return err },
			"copy: Segmentation fault"},
		{"control characters are dropped", failing("copy: bad \x1b[31mred\x07 text\r\n", 1),
			func(d *Deployer, id string) error { _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); return err },
			"copy: bad [31mred text"},
		{"a very long line is cut", failing("copy: "+strings.Repeat("x", 1000)+"\n", 1),
			func(d *Deployer, id string) error { _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); return err },
			"copy: " + strings.Repeat("x", 294) + "…"},
		{"upload: the secret key in the reason is removed", failing("upload: the key "+sqliteSentinel+" was refused\n", 1),
			func(d *Deployer, id string) error {
				_, err := d.SQLiteUpload(ctx, id, "web-1", "whoami", svc, sqliteDest(), "1", 4, nil)
				return err
			},
			"upload: the key *** was refused"},
		{"download: the reason", failing("download: the backup is not a SQLite database\n", 1),
			func(d *Deployer, id string) error {
				_, err := d.SQLiteDownload(ctx, id, "web-2", "b/x.sqlite.gz", sqliteDest())
				return err
			},
			"download: the backup is not a SQLite database"},
		{"stage: the reason", failing("stage: data has files left in .caravana-restore/old by an earlier restore\n", 1),
			func(d *Deployer, id string) error { return d.SQLiteStage(ctx, id, svc, "web-2") },
			"stage: data has files left in .caravana-restore/old by an earlier restore"},
		{"a successful copy with no size", func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{Stdout: "hello\n"} },
			func(d *Deployer, id string) error { _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); return err },
			"copy: the size of the copy is unreadable"},
		{"a successful upload with no object", func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{} },
			func(d *Deployer, id string) error {
				_, err := d.SQLiteUpload(ctx, id, "web-1", "whoami", svc, sqliteDest(), "1", 4, nil)
				return err
			},
			"upload: the object of the backup is unreadable"},
		{"a successful download with no size", func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{} },
			func(d *Deployer, id string) error {
				_, err := d.SQLiteDownload(ctx, id, "web-2", "b/x.sqlite.gz", sqliteDest())
				return err
			},
			"download: the size of the file is unreadable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, nomad := newTestDeployer(t)
			id := execCopy(nomad, "running")
			nomad.OnExec(tt.reply)
			err := tt.call(d, id)
			var se *SQLiteError
			if !errors.As(err, &se) || err.Error() != tt.wantMsg {
				t.Fatalf("err = %v (%T), want a *SQLiteError with %q", err, err, tt.wantMsg)
			}
			if strings.Contains(err.Error(), sqliteSentinel) {
				t.Error("the error holds the secret key")
			}
		})
	}

	t.Run("a script that runs out of the context's time", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply { return deploytest.ExecReply{Hold: true} })
		tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		_, err := d.SQLiteCopy(tctx, id, svc, "web-1")
		var se *SQLiteError
		if !errors.As(err, &se) || err.Error() != "copy: timed out" {
			t.Errorf("err = %v, want copy: timed out", err)
		}
	})

	t.Run("a copy that isn't running or isn't there is Exec's error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "pending")
		if _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); !errors.Is(err, ErrCopyNotRunning) {
			t.Errorf("err = %v, want ErrCopyNotRunning", err)
		}
		if _, err := d.SQLiteCopy(ctx, "nope", svc, "web-1"); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("err = %v, want ErrCopyNotFound", err)
		}
		if _, err := d.SQLiteUpload(ctx, "nope", "web-1", "whoami", svc, sqliteDest(), "1", 4, nil); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("err = %v, want ErrCopyNotFound", err)
		}
		if err := d.SQLiteStage(ctx, "nope", svc, "web-1"); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("err = %v, want ErrCopyNotFound", err)
		}
	})

	t.Run("Nomad refusing the exec says which capability", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.FailExec(403)
		_, err := d.SQLiteUpload(ctx, id, "web-1", "whoami", svc, sqliteDest(), "1", 4, nil)
		if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "alloc-exec") {
			t.Errorf("err = %v, want a refusal naming alloc-exec", err)
		}
	})

	t.Run("a task Nomad can't start the command in", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Fail: "task caravana-sqlite not running"}
		})
		if _, err := d.SQLiteCopy(ctx, id, svc, "web-1"); !errors.Is(err, ErrExecFailed) {
			t.Errorf("err = %v, want ErrExecFailed", err)
		}
	})

	t.Run("a service with no sqlite file", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		id := execCopy(nomad, "running")
		plain := withVolume("data").Services[0]
		if _, err := d.SQLiteCopy(ctx, id, plain, "web-1"); err == nil {
			t.Error("SQLiteCopy of a service with no sqlite backup = nil")
		}
		if err := d.SQLiteStage(ctx, id, plain, "web-1"); err == nil {
			t.Error("SQLiteStage of a service with no sqlite backup = nil")
		}
		if len(nomad.Execs()) != 0 {
			t.Error("a command ran")
		}
	})
}

func TestRedactError(t *testing.T) {
	dest := sqliteDest()
	plain := errors.New("copy not found")
	if got := redactError(plain, &dest); got != plain {
		t.Errorf("an error without the key was changed: %v", got)
	}
	if got := redactError(fmt.Errorf("x: %w", errors.New("key "+sqliteSentinel)), &dest); strings.Contains(got.Error(), sqliteSentinel) || !strings.Contains(got.Error(), "***") {
		t.Errorf("redacted = %v", got)
	}
	if got := redactError(plain, nil); got != plain {
		t.Errorf("with no destination: %v", got)
	}
}

func TestDestinationLines(t *testing.T) {
	got, err := destinationLines(sqliteDest())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 8 || !slices.IsSorted(lines) {
		t.Errorf("lines = %q, want the eight RCLONE_CONFIG_DEST settings in order", lines)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "RCLONE_CONFIG_DEST_") {
			t.Errorf("line %q isn't a destination setting", l)
		}
	}
	if strings.Contains(got, "BACKUP_PATH") || strings.Contains(got, "KEEP") {
		t.Errorf("the lines hold more than the destination's credentials: %s", got)
	}

	// A value with a line break would start a line of its own: refused, and
	// the error never says the value.
	for field, mut := range map[string]func(*BackupDestination){
		"endpoint":          func(d *BackupDestination) { d.Endpoint = "http://x\nRCLONE_CONFIG_DEST_TYPE=evil" },
		"secret_access_key": func(d *BackupDestination) { d.SecretAccessKey = sqliteSentinel + "\r\nX=1" },
		"access_key_id":     func(d *BackupDestination) { d.AccessKeyID = "AK\n" },
	} {
		d := sqliteDest()
		mut(&d)
		_, err := destinationLines(d)
		if err == nil || !strings.Contains(err.Error(), field) || strings.Contains(err.Error(), sqliteSentinel) || strings.Contains(err.Error(), "evil") {
			t.Errorf("%s with a line break: err = %v, want a refusal naming the setting and not its value", field, err)
		}
	}
}

func TestSQLiteRequestIsCarriedAsFILE(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := withVolume("data")
	a.Services[0].Backup = &app.Backup{Engine: app.BackupSQLite, File: "/data/db/app.db", Keep: 4}
	req := &VolumeRequest{ID: "web-18", Action: VolumeRestoreSQLite, File: "data/db/app.db", NotAfter: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	if err := d.WriteVolumeVariable(ctx, a, a.Services[0], nil, 0, req); err != nil {
		t.Fatal(err)
	}
	got := nomad.Variable(volumeVar)
	if got["ACTION"] != "restore-sqlite" || got["FILE"] != "data/db/app.db" || got["REQUEST"] != "web-18" {
		t.Errorf("variable = %v", got)
	}
	for k := range got {
		if isDestinationKey(k) {
			t.Errorf("a sqlite restore's variable holds %s: the destination never goes there", k)
		}
	}
	if err := d.ClearVolumeRequest(ctx, "whoami", "web"); err != nil {
		t.Fatal(err)
	}
	cleared := nomad.Variable(volumeVar)
	if _, ok := cleared["FILE"]; ok || cleared["REQUEST"] != "" || cleared["ACTION"] != "" {
		t.Errorf("request left after clearing: %v", cleared)
	}
}

// TestRegisterKeepsTheDestinationOutOfASQLiteVariable: a sqlite service's
// caravana-volumes variable never holds the destination, even one an earlier
// engine left there, and no KEEP (the upload script has its own).
func TestRegisterKeepsTheDestinationOutOfASQLiteVariable(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := withVolume("data")
	a.Services[0].Backup = &app.Backup{Engine: app.BackupSQLite, File: "/data/app.db", Keep: 4}
	nomad.SetVariable(volumeVar, map[string]string{"REQUEST": "", "LIMIT": "60", "KEEP": "9", "BACKUP_PATH": "b/x", "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY": "SK"})
	if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := nomad.Variable(volumeVar); len(got) != 2 || got["REQUEST"] != "" || got["LIMIT"] != "60" {
		t.Errorf("variable = %v, want only REQUEST and LIMIT", got)
	}
	// The job names the two sidecars.
	job := nomad.Job("whoami")
	var names []string
	for _, tk := range job.TaskGroups[0].Tasks {
		names = append(names, tk.Name)
	}
	if !slices.Contains(names, "caravana-sqlite") || !slices.Contains(names, "caravana-upload") {
		t.Errorf("tasks = %v, want the two sidecars", names)
	}
}
