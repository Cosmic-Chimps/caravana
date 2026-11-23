package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// The keys of the two destinations of a copy. Each test searches for them where
// they must not be.
const (
	srcKey  = "SRC-sentinel-052-secret"
	destKey = "DEST-sentinel-052-secret"
)

var (
	copySource = BackupDestination{Name: "main", Endpoint: "http://10.0.0.5:9000", Region: "us-east-1", Bucket: "backups", Prefix: "caravana",
		AccessKeyID: "AK1", SecretAccessKey: srcKey}
	copyTarget = BackupDestination{Name: "offsite", Endpoint: "https://s3.example.com", Region: "eu-west-1", Bucket: "offsite", Prefix: "",
		AccessKeyID: "AK2", SecretAccessKey: destKey}
)

func testCopySpec() BackupCopySpec {
	return BackupCopySpec{AppID: "shop", Service: "db", ID: "0123456789ab", Source: copySource, Target: copyTarget,
		SrcObject: "backups/caravana/shop/db/1791.dump", DestObject: "offsite/shop/db/1791.dump", DestPath: "offsite/shop/db",
		Kind: nomadjob.CopyKindFile, Ext: ".dump", Keep: 14, Pinned: []string{"1700", "1750"}}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStartBackupCopy(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	spec := testCopySpec()

	jobID, err := d.StartBackupCopy(ctx, spec)
	if err != nil || jobID != "shop_db_copy-0123456789ab" {
		t.Fatalf("StartBackupCopy = %q, %v", jobID, err)
	}
	v := nomad.Variable("nomad/jobs/" + jobID)
	for k, want := range map[string]string{
		"RCLONE_CONFIG_SRC_TYPE": "s3", "RCLONE_CONFIG_SRC_ENDPOINT": "http://10.0.0.5:9000", "RCLONE_CONFIG_SRC_ACCESS_KEY_ID": "AK1",
		"RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY": srcKey,
		"RCLONE_CONFIG_DEST_TYPE":             "s3", "RCLONE_CONFIG_DEST_ENDPOINT": "https://s3.example.com", "RCLONE_CONFIG_DEST_REGION": "eu-west-1",
		"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY": destKey, "RCLONE_CONFIG_DEST_NO_CHECK_BUCKET": "true",
		"SRC_OBJECT": spec.SrcObject, "DEST_OBJECT": spec.DestObject, "DEST_PATH": spec.DestPath, "KEEP": "14", "PINNED": "1700 1750",
	} {
		if v[k] != want {
			t.Errorf("variable[%s] = %q, want %q", k, v[k], want)
		}
	}
	// The job holds neither key, and the variable is the only place that does.
	job := nomad.Job(jobID)
	if job == nil || *job.Type != "batch" {
		t.Fatalf("job = %+v", job)
	}
	if raw := marshal(t, job); strings.Contains(raw, srcKey) || strings.Contains(raw, destKey) || strings.Contains(raw, "AK1") {
		t.Errorf("the job holds a key: %s", raw)
	}
	if got := job.TaskGroups[0].Tasks[0].Env; got["KIND"] != "file" || got["EXT"] != ".dump" {
		t.Errorf("env = %v", got)
	}
	for _, path := range nomad.VariablePaths() {
		if path != "nomad/jobs/"+jobID {
			t.Errorf("a variable at %s: the keys belong only in the job's own", path)
		}
	}

	// Without pins there is no PINNED, and a copy with no KEEP says 0.
	spec.ID, spec.Pinned, spec.Keep = "ba9876543210", nil, 0
	id2, err := d.StartBackupCopy(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable("nomad/jobs/" + id2); v["KEEP"] != "0" || v["PINNED"] != "" {
		t.Errorf("variable = %v", v)
	} else if _, has := v["PINNED"]; has {
		t.Errorf("a copy with no pins has PINNED: %v", v)
	}

	// Purge takes the job and its variable, and nothing else.
	if err := d.PurgeBackupCopy(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	if nomad.Job(jobID) != nil || nomad.Variable("nomad/jobs/"+jobID) != nil || nomad.Variable("nomad/jobs/"+id2) == nil {
		t.Error("purge left the job or its variable, or took another copy's")
	}
	if err := d.PurgeBackupCopy(ctx, jobID); err != nil {
		t.Errorf("purge of a copy that is gone = %v, want nil", err)
	}
}

func TestStartBackupCopyRefusedByNomadLeavesNoKeys(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	nomad.RejectNext(400, "job rejected by the test")
	_, err := d.StartBackupCopy(ctx, testCopySpec())
	if err == nil {
		t.Fatal("StartBackupCopy = nil, want Nomad's refusal")
	}
	if strings.Contains(err.Error(), srcKey) || strings.Contains(err.Error(), destKey) {
		t.Errorf("the error holds a key: %v", err)
	}
	if got := nomad.VariablePaths(); len(got) != 0 {
		t.Errorf("variables = %v, want none: a job that isn't there doesn't leave the keys", got)
	}
}

func TestBackupCopyOutcome(t *testing.T) {
	ctx := context.Background()
	start := func(t *testing.T) (*Deployer, *deploytest.Nomad, string) {
		t.Helper()
		d, _, nomad := newTestDeployer(t)
		id, err := d.StartBackupCopy(ctx, testCopySpec())
		if err != nil {
			t.Fatal(err)
		}
		return d, nomad, id
	}

	t.Run("running until the job is dead", func(t *testing.T) {
		d, _, id := start(t)
		got, err := d.BackupCopyOutcome(ctx, id)
		if err != nil || got.Status != RunRunning {
			t.Errorf("outcome = %+v, %v; want running", got, err)
		}
	})
	t.Run("succeeded with its object, size and what retention removed", func(t *testing.T) {
		d, nomad, id := start(t)
		nomad.FinishRun(id, true, "", "caravana-copy: done key=offsite/shop/db/1791.dump bytes=1499\ncaravana-copy: expired 1700.dump\ncaravana-copy: expired 1701.dump\n", "")
		got, err := d.BackupCopyOutcome(ctx, id)
		if err != nil || got.Status != RunSucceeded || got.Object != "offsite/shop/db/1791.dump" || got.Bytes != 1499 ||
			len(got.Expired) != 2 || got.Expired[0] != "1700.dump" || got.Message != "" {
			t.Errorf("outcome = %+v, %v", got, err)
		}
	})
	t.Run("a warning rides on a good copy", func(t *testing.T) {
		d, nomad, id := start(t)
		nomad.FinishRun(id, true, "", "caravana-copy: done key=o bytes=1\ncaravana-copy: warning the copy is kept, but old copies were not removed: boom\n", "")
		got, err := d.BackupCopyOutcome(ctx, id)
		if err != nil || got.Status != RunSucceeded || !strings.Contains(got.Message, "old copies were not removed") {
			t.Errorf("outcome = %+v, %v", got, err)
		}
	})
	t.Run("failed with the task's last line, without a key", func(t *testing.T) {
		d, nomad, id := start(t)
		nomad.FinishRun(id, false, "copy", "", "Failed to copyto: AccessDenied for "+destKey+" at "+srcKey+"\n")
		got, err := d.BackupCopyOutcome(ctx, id, copySource, copyTarget)
		if err != nil || got.Status != RunFailed || !strings.HasPrefix(got.Message, "copy: Failed to copyto: AccessDenied") {
			t.Fatalf("outcome = %+v, %v", got, err)
		}
		if strings.Contains(got.Message, srcKey) || strings.Contains(got.Message, destKey) {
			t.Errorf("the message holds a key: %q", got.Message)
		}
	})
	t.Run("a copy that ends without the done line is a failure", func(t *testing.T) {
		d, nomad, id := start(t)
		nomad.FinishRun(id, true, "", "something else\n", "")
		got, err := d.BackupCopyOutcome(ctx, id)
		if err != nil || got.Status != RunFailed || got.Message == "" {
			t.Errorf("outcome = %+v, %v", got, err)
		}
	})
	t.Run("a job Nomad doesn't have is a failure", func(t *testing.T) {
		d, _, id := start(t)
		if err := d.PurgeBackupCopy(ctx, id); err != nil {
			t.Fatal(err)
		}
		got, err := d.BackupCopyOutcome(ctx, id)
		if err != nil || got.Status != RunFailed || got.Message != "Nomad has no record of this copy" {
			t.Errorf("outcome = %+v, %v", got, err)
		}
	})
}

func TestCheckBackupPlaces(t *testing.T) {
	ctx := context.Background()
	testPlace := []CheckPlace{{Dest: copyTarget, Path: "offsite"}}

	// hook finishes every check job as asked and keeps what the job and its
	// variable looked like while it ran.
	type seen struct {
		variable map[string]string
		job      string
	}
	hook := func(nomad *deploytest.Nomad, ok bool, failedTask, stdout, stderr string) *seen {
		got := &seen{}
		nomad.FinishOnRegisterPrefix("_backup-check-", ok, failedTask, func(job *api.Job) (string, string) {
			got.variable = nomad.Variable("nomad/jobs/" + *job.ID)
			got.job = marshal(t, job)
			return stdout, stderr
		})
		return got
	}
	gone := func(t *testing.T, nomad *deploytest.Nomad) {
		t.Helper()
		if ids := nomad.JobIDs("_backup-check-"); len(ids) != 0 {
			t.Errorf("check jobs left: %v", ids)
		}
		if paths := nomad.VariablePaths(); len(paths) != 0 {
			t.Errorf("variables left: %v", paths)
		}
	}

	t.Run("a test that passes", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		got := hook(nomad, true, "", "caravana-check: ok\n", "")
		res, err := d.CheckBackupPlaces(ctx, nomadjob.CheckModeTest, testPlace)
		if err != nil || !res.OK || res.Failed != "" {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if got.variable["RCLONE_CONFIG_R1_SECRET_ACCESS_KEY"] != destKey || got.variable["R1_PATH"] != "offsite" ||
			got.variable["RCLONE_CONFIG_R1_ENDPOINT"] != "https://s3.example.com" {
			t.Errorf("variable = %v", got.variable)
		}
		if strings.Contains(got.job, destKey) || strings.Contains(got.job, "AK2") {
			t.Errorf("the job holds the key: %s", got.job)
		}
		gone(t, nomad)
	})
	t.Run("a test that fails names the step, with rclone's words and no key", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		hook(nomad, false, "check", "caravana-check: failed write\n", "2026/11/23 ERROR: Failed to rcat: AccessDenied "+destKey+"\n")
		res, err := d.CheckBackupPlaces(ctx, nomadjob.CheckModeTest, testPlace)
		if err != nil || res.OK || res.Failed != "write" || !strings.Contains(res.Message, "Failed to rcat: AccessDenied") {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if strings.Contains(res.Message, destKey) {
			t.Errorf("the message holds the key: %q", res.Message)
		}
		gone(t, nomad)
	})
	t.Run("a job that couldn't start says what Nomad said", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		hook(nomad, false, "check", "", "")
		res, err := d.CheckBackupPlaces(ctx, nomadjob.CheckModeTest, testPlace)
		if err != nil || res.OK || res.Failed != "" || res.Message == "" {
			t.Errorf("result = %+v, %v; want a failure with Nomad's words", res, err)
		}
		gone(t, nomad)
	})
	t.Run("locate found in the second place", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		got := hook(nomad, true, "", "caravana-check: missing 1\ncaravana-check: found 2\n", "")
		places := []CheckPlace{{Dest: copySource, Path: "backups/caravana/shop/db/1791.dump"}, {Dest: copyTarget, Path: "offsite/shop/db/1791.dump"}}
		res, err := d.CheckBackupPlaces(ctx, nomadjob.CheckModeLocate, places)
		if err != nil || res.Found != 2 || len(res.Missing) != 1 || res.Missing[0] != 1 || len(res.Unreachable) != 0 {
			t.Fatalf("result = %+v, %v", res, err)
		}
		v := got.variable
		if v["RCLONE_CONFIG_R1_SECRET_ACCESS_KEY"] != srcKey || v["RCLONE_CONFIG_R2_SECRET_ACCESS_KEY"] != destKey ||
			v["R1_PATH"] != "backups/caravana/shop/db/1791.dump" || v["R2_PATH"] != "offsite/shop/db/1791.dump" {
			t.Errorf("variable = %v", v)
		}
		if strings.Contains(got.job, srcKey) || strings.Contains(got.job, destKey) {
			t.Errorf("the job holds a key: %s", got.job)
		}
		gone(t, nomad)
	})
	t.Run("locate: missing and unreachable, and a failed job leaves every place unreachable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		hook(nomad, true, "", "caravana-check: unreachable 1\ncaravana-check: missing 2\n", "")
		places := []CheckPlace{{Dest: copySource, Path: "a"}, {Dest: copyTarget, Path: "b"}}
		res, err := d.CheckBackupPlaces(ctx, nomadjob.CheckModeLocate, places)
		if err != nil || res.Found != 0 || len(res.Unreachable) != 1 || res.Unreachable[0] != 1 || len(res.Missing) != 1 || res.Missing[0] != 2 {
			t.Errorf("result = %+v, %v", res, err)
		}
		gone(t, nomad)

		d, _, nomad = newTestDeployer(t)
		hook(nomad, false, "check", "", "docker: pull access denied\n")
		res, err = d.CheckBackupPlaces(ctx, nomadjob.CheckModeLocate, places)
		if err != nil || res.Found != 0 || len(res.Unreachable) != 2 || res.Message == "" {
			t.Errorf("result = %+v, %v; want both places unreachable and a message", res, err)
		}
		gone(t, nomad)
	})
	t.Run("a check that never ends is Nomad's problem, and still cleans up", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		d.Timing.CheckWait = 150 * time.Millisecond
		_, err := d.CheckBackupPlaces(ctx, nomadjob.CheckModeTest, testPlace)
		if !errors.Is(err, ErrNomadUnavailable) || strings.Contains(err.Error(), destKey) {
			t.Errorf("error = %v, want ErrNomadUnavailable", err)
		}
		gone(t, nomad)
	})
	t.Run("a cancelled caller still gets its clean-up", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		cctx, cancel := context.WithCancel(ctx)
		go func() { time.Sleep(100 * time.Millisecond); cancel() }()
		if _, err := d.CheckBackupPlaces(cctx, nomadjob.CheckModeTest, testPlace); err == nil {
			t.Error("want the caller's cancellation")
		}
		gone(t, nomad)
	})
	t.Run("a Nomad that refuses the job leaves no keys", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.RejectNext(400, "no")
		if _, err := d.CheckBackupPlaces(ctx, nomadjob.CheckModeTest, testPlace); err == nil {
			t.Error("want Nomad's refusal")
		}
		gone(t, nomad)
	})
	t.Run("arguments are checked first", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		for name, tc := range map[string]struct {
			mode   string
			places []CheckPlace
		}{
			"unknown mode":  {"peek", testPlace},
			"no places":     {nomadjob.CheckModeLocate, nil},
			"four places":   {nomadjob.CheckModeLocate, make([]CheckPlace, 4)},
			"a test of two": {nomadjob.CheckModeTest, make([]CheckPlace, 2)},
		} {
			if _, err := d.CheckBackupPlaces(ctx, tc.mode, tc.places); err == nil {
				t.Errorf("%s: want an error", name)
			}
		}
		if len(nomad.VariablePaths()) != 0 || len(nomad.JobIDs("_")) != 0 {
			t.Error("a refused check wrote something")
		}
	})
}

func TestCopyPathsHelpers(t *testing.T) {
	if got := DestinationBase(copySource); got != "backups/caravana" {
		t.Errorf("DestinationBase = %q", got)
	}
	if got := DestinationBase(copyTarget); got != "offsite" {
		t.Errorf("DestinationBase without a prefix = %q", got)
	}
	if got := BackupPath("shop", "db", copySource); got != "backups/caravana/shop/db" {
		t.Errorf("BackupPath = %q", got)
	}
}
