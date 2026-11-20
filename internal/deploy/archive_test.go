package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

var archiveDest = BackupDestination{Endpoint: "https://s3.example.com", Region: "auto", Bucket: "bkt", Prefix: "/caravana/",
	AccessKeyID: "AKIA-archive", SecretAccessKey: "sk-archive-sentinel"}

// archiveEnd answers as the task would for the job it was given: the object at the path and time the job's
// env says, with the size.
func archiveEnd(bytes string) func(job *api.Job) (string, string) {
	return func(job *api.Job) (string, string) {
		env := job.TaskGroups[0].Tasks[0].Env
		return "caravana-archive: ok object=" + env["BACKUP_PATH"] + "/" + env["TS"] + "/" + env["VOLUME"] + ".tar.gz bytes=" + bytes + "\n", ""
	}
}

func TestArchiveVolume(t *testing.T) {
	ctx := context.Background()
	const jobID = "shop_uploads_archive"
	varPath := "nomad/jobs/" + jobID

	t.Run("an app's volume is archived", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FinishOnRegister(jobID, true, "", archiveEnd("4096"))
		object, bytes, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(object, "bkt/caravana/shop/volumes/") || !strings.HasSuffix(object, "/shop_uploads.tar.gz") || bytes != 4096 {
			t.Errorf("object %q, bytes %d", object, bytes)
		}
		if nomad.Registrations() != 1 || !nomad.Purged(jobID) {
			t.Errorf("registrations %d, purged %v: the job isn't registered once and purged", nomad.Registrations(), nomad.Purged(jobID))
		}
		if nomad.Variable(varPath) != nil {
			t.Error("the job's variable is left behind")
		}
	})

	t.Run("a volume that doesn't read as an app's goes under volumes", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FinishOnRegister("gone_data_archive", true, "", archiveEnd("7"))
		object, _, err := d.ArchiveVolume(ctx, "gone_data", "", archiveDest)
		if err != nil || !strings.HasPrefix(object, "bkt/caravana/volumes/") {
			t.Errorf("object %q, err %v", object, err)
		}
	})

	t.Run("the keys are in the job's variable and nowhere else", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		var items map[string]string
		nomad.FinishOnRegister(jobID, true, "", func(job *api.Job) (string, string) {
			items = nomad.Variable(varPath) // read while the job exists
			if strings.Contains(job.TaskGroups[0].Tasks[0].Env["BACKUP_PATH"], "sk-") {
				t.Error("a key in the job's env")
			}
			return archiveEnd("1")(job)
		})
		if _, _, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest); err != nil {
			t.Fatal(err)
		}
		if items["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "sk-archive-sentinel" || items["RCLONE_CONFIG_DEST_ACCESS_KEY_ID"] != "AKIA-archive" {
			t.Errorf("variable = %v", items)
		}
		if _, has := items["BACKUP_PATH"]; has {
			t.Error("the variable has a BACKUP_PATH: the job's env carries it")
		}
		job := nomad.Job(jobID)
		if job != nil {
			t.Error("the job is still registered")
		}
	})

	t.Run("the script says it failed", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		long := strings.Repeat("y", 400)
		nomad.FinishOnRegister(jobID, false, nomadjob.ArchiveTaskName, func(*api.Job) (string, string) {
			return "caravana-archive: failed: the upload: AccessDenied " + long + "\n", ""
		})
		_, _, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest)
		if !errors.Is(err, ErrArchiveFailed) || !strings.Contains(err.Error(), "the upload: AccessDenied") || len(err.Error()) > 300 {
			t.Errorf("err = %v (length %d)", err, len(err.Error()))
		}
		if strings.Contains(err.Error(), "sk-archive-sentinel") {
			t.Error("the error holds a key")
		}
		if !nomad.Purged(jobID) || nomad.Variable(varPath) != nil {
			t.Error("a failed archive leaves its job or variable")
		}
	})

	t.Run("the task never ran to its end", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FinishOnRegister(jobID, false, nomadjob.ArchiveTaskName, func(*api.Job) (string, string) {
			return "", "Failed to pull the image\n"
		})
		_, _, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest)
		if !errors.Is(err, ErrArchiveFailed) || !strings.Contains(err.Error(), "Failed to pull the image") {
			t.Errorf("err = %v, want Nomad's own account of it", err)
		}
	})

	t.Run("an ok with the wrong object isn't trusted", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FinishOnRegister(jobID, true, "", func(*api.Job) (string, string) {
			return "caravana-archive: ok object=elsewhere/x.tar.gz bytes=5\n", ""
		})
		if _, _, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest); !errors.Is(err, ErrArchiveFailed) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a complete job that said nothing", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FinishOnRegister(jobID, true, "", func(*api.Job) (string, string) { return "", "" })
		if _, _, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest); !errors.Is(err, ErrArchiveFailed) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a server that isn't placing work", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		d.Timing.ArchivePlaceWait = 30 * time.Millisecond // no FinishOnRegister: the job never gets a copy
		_, _, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest)
		if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "the server that holds the volume isn't placing work") {
			t.Errorf("err = %v", err)
		}
		if !nomad.Purged(jobID) || nomad.Variable(varPath) != nil {
			t.Error("the job or its variable is left")
		}
	})

	t.Run("it never ends", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies(jobID, api.AllocationListStub{ID: "a1", JobID: jobID, ClientStatus: api.AllocClientStatusRunning})
		d.Timing.ArchiveWait = 40 * time.Millisecond
		_, _, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest)
		if !errors.Is(err, ErrArchiveFailed) || !strings.Contains(err.Error(), "didn't finish") {
			t.Errorf("err = %v", err)
		}
		if !nomad.Purged(jobID) {
			t.Error("the job keeps running")
		}
	})

	t.Run("the caller gives up", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetCopies(jobID, api.AllocationListStub{ID: "a1", JobID: jobID, ClientStatus: api.AllocClientStatusRunning})
		cctx, cancel := context.WithCancel(ctx)
		go func() { time.Sleep(30 * time.Millisecond); cancel() }()
		_, _, err := d.ArchiveVolume(cctx, "shop_uploads", "shop", archiveDest)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
		if !nomad.Purged(jobID) || nomad.Variable(varPath) != nil {
			t.Error("the clean-up didn't run with the caller gone")
		}
	})

	t.Run("a Nomad that refuses the variable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FailVariables(403)
		_, _, err := d.ArchiveVolume(ctx, "shop_uploads", "shop", archiveDest)
		if !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("err = %v", err)
		}
		if nomad.Registrations() != 0 {
			t.Error("a job was registered without its variable")
		}
	})
}

func TestShorten(t *testing.T) {
	if got := shorten("  short  ", 200); got != "short" {
		t.Errorf("got %q", got)
	}
	got := shorten(strings.Repeat("é", 150), 200) // 300 bytes
	if !strings.HasSuffix(got, "…") || len(got) > 203 || !strings.HasPrefix(got, "é") {
		t.Errorf("got %q (%d bytes)", got, len(got))
	}
	if strings.ContainsRune(got, '�') {
		t.Error("cut inside a character")
	}
}

func TestRemoveArchive(t *testing.T) {
	ctx := context.Background()
	const jobID = "shop_uploads_archive"
	d, _, nomad := newTestDeployer(t)
	nomad.AddForeignJob(&api.Job{ID: new(jobID), Name: new(jobID), Type: new("batch")})
	nomad.SetVariable("nomad/jobs/"+jobID, map[string]string{"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY": "sk-left"})
	if err := d.RemoveArchive(ctx, "shop_uploads"); err != nil {
		t.Fatal(err)
	}
	if !nomad.Purged(jobID) || nomad.Variable("nomad/jobs/"+jobID) != nil {
		t.Error("the leftover job or its variable is still there")
	}
	if err := d.RemoveArchive(ctx, "shop_uploads"); err != nil {
		t.Errorf("removing what isn't there = %v, want no error", err)
	}
}
