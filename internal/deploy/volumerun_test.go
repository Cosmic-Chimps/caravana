package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// volumeApp is whoami's web service with one volume and, when engine isn't
// empty, a backup of that engine.
func volumeApp(engine string) app.App {
	a := withVolume("data")
	if engine != "" {
		a.Services[0].Backup = &app.Backup{Engine: engine, Keep: 4}
	}
	return a
}

const volumeVar = "nomad/jobs/whoami/web/caravana-volumes"

// webCopy gives the app's web service one running copy, "c1", and returns
// its ID.
func webCopy(nomad *deploytest.Nomad) string {
	nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
	return "c1"
}

func fastRender(d *Deployer) {
	d.Timing.RenderGap = time.Millisecond
	d.Timing.RenderWait = 80 * time.Millisecond
}

func TestWriteVolumeVariable(t *testing.T) {
	ctx := context.Background()
	notAfter := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		dest *BackupDestination
		keep int
		req  *VolumeRequest
		want map[string]string // the items that must be there, exactly
	}{
		{
			name: "no destination, no request: an empty request and the limit",
			want: map[string]string{"REQUEST": "", "LIMIT": "1800"},
		},
		{
			name: "a destination and KEEP",
			dest: &testDest, keep: 4,
			want: map[string]string{
				"REQUEST": "", "LIMIT": "1800", "KEEP": "4", "BACKUP_PATH": "backups/caravana/whoami/web",
				"RCLONE_CONFIG_DEST_TYPE": "s3", "RCLONE_CONFIG_DEST_PROVIDER": "Other",
				"RCLONE_CONFIG_DEST_ENDPOINT": "http://10.0.0.5:9000", "RCLONE_CONFIG_DEST_REGION": "us-east-1",
				"RCLONE_CONFIG_DEST_ACCESS_KEY_ID": "AK", "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY": "SK",
				"RCLONE_CONFIG_DEST_FORCE_PATH_STYLE": "true", "RCLONE_CONFIG_DEST_NO_CHECK_BUCKET": "true",
			},
		},
		{
			name: "a backup request",
			keep: 4,
			req:  &VolumeRequest{ID: "web-17", Action: VolumeBackup, TS: "17", NotAfter: notAfter, Limit: 10 * time.Minute},
			want: map[string]string{"REQUEST": "web-17", "ACTION": "backup", "TS": "17", "NOT_AFTER": itoa(notAfter.Unix()), "LIMIT": "600", "KEEP": "4"},
		},
		{
			name: "a restore request",
			req:  &VolumeRequest{ID: "web-18", Action: VolumeRestore, Object: "backups/whoami/web/17/", NotAfter: notAfter},
			want: map[string]string{"REQUEST": "web-18", "ACTION": "restore", "OBJECT": "backups/whoami/web/17/", "NOT_AFTER": itoa(notAfter.Unix()), "LIMIT": "1800"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, nomad := newTestDeployer(t)
			a := volumeApp(app.BackupVolume)
			if err := d.WriteVolumeVariable(ctx, a, a.Services[0], tt.dest, tt.keep, tt.req); err != nil {
				t.Fatal(err)
			}
			got := nomad.Variable(volumeVar)
			if !maps.Equal(got, tt.want) {
				t.Errorf("items = %v\nwant    %v", got, tt.want)
			}
		})
	}
}

// TestVolumeJobHoldsNoSecret: the destination's key goes in the variable; the
// job Nomad gets names the variable and holds no value.
func TestVolumeJobHoldsNoSecret(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := volumeApp(app.BackupVolume)
	dest := testDest
	dest.SecretAccessKey = "s3cr3t-key-marker"
	if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs()); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteVolumeVariable(ctx, a, a.Services[0], &dest, 4, nil); err != nil {
		t.Fatal(err)
	}
	job, err := json.Marshal(nomad.Job("whoami"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(job), "s3cr3t-key-marker") {
		t.Error("the job holds the destination's secret key")
	}
	if !strings.Contains(string(job), volumeVar) {
		t.Error("the job doesn't name the variable the task reads")
	}
	if v := nomad.Variable(volumeVar); v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "s3cr3t-key-marker" {
		t.Errorf("the variable lacks the key: %v", v)
	}
}

func TestClearVolumeRequest(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := volumeApp(app.BackupVolume)
	req := &VolumeRequest{ID: "web-17", Action: VolumeRestore, Object: "x/", TS: "17", NotAfter: time.Now().Add(time.Hour), Limit: time.Minute}
	if err := d.WriteVolumeVariable(ctx, a, a.Services[0], &testDest, 4, req); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearVolumeRequest(ctx, "whoami", "web"); err != nil {
		t.Fatal(err)
	}
	got := nomad.Variable(volumeVar)
	if got["REQUEST"] != "" || got["ACTION"] != "" || got["OBJECT"] != "" || got["TS"] != "" || got["NOT_AFTER"] != "" {
		t.Errorf("request left: %v", got)
	}
	if got["KEEP"] != "4" || got["LIMIT"] != "60" || got["BACKUP_PATH"] == "" || got["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK" {
		t.Errorf("the rest wasn't kept: %v", got)
	}
	if _, ok := got["REQUEST"]; !ok {
		t.Error("REQUEST is gone; the variable must keep an item to stay valid")
	}

	t.Run("a variable that isn't there stays that way", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.ClearVolumeRequest(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		if nomad.Variable(volumeVar) != nil {
			t.Error("a variable was made")
		}
	})
}

// TestRegisterWritesTheVolumeVariable: before the job, so the task never waits
// for a variable that isn't there (lab finding L6).
func TestRegisterWritesTheVolumeVariable(t *testing.T) {
	ctx := context.Background()
	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()

	tests := []struct {
		name   string
		app    app.App
		before map[string]string // the variable already there; nil for none
		want   map[string]string // nil: no variable at all
	}{
		{
			name: "a service with a volume gets one",
			app:  volumeApp(""),
			want: map[string]string{"REQUEST": "", "LIMIT": "1800"},
		},
		{
			name: "a service without volumes gets none",
			app:  whoami(),
		},
		{
			name: "a volume backup's KEEP follows its policy",
			app:  volumeApp(app.BackupVolume),
			want: map[string]string{"REQUEST": "", "LIMIT": "1800", "KEEP": "4"},
		},
		{
			name:   "a request that hasn't expired survives, with the destination",
			app:    volumeApp(app.BackupVolume),
			before: map[string]string{"REQUEST": "web-1", "ACTION": "backup", "NOT_AFTER": itoa(future), "LIMIT": "60", "BACKUP_PATH": "b/whoami/web", "KEEP": "9"},
			want:   map[string]string{"REQUEST": "web-1", "ACTION": "backup", "NOT_AFTER": itoa(future), "LIMIT": "60", "BACKUP_PATH": "b/whoami/web", "KEEP": "4"},
		},
		{
			name:   "an expired request is cleared, the rest kept",
			app:    volumeApp(app.BackupVolume),
			before: map[string]string{"REQUEST": "web-1", "ACTION": "restore", "OBJECT": "o/", "NOT_AFTER": itoa(past), "LIMIT": "60", "BACKUP_PATH": "b/whoami/web"},
			want:   map[string]string{"REQUEST": "", "LIMIT": "60", "BACKUP_PATH": "b/whoami/web", "KEEP": "4"},
		},
		{
			name:   "a request with no usable NOT_AFTER is cleared",
			app:    volumeApp(app.BackupVolume),
			before: map[string]string{"REQUEST": "web-1", "ACTION": "backup", "LIMIT": "60"},
			want:   map[string]string{"REQUEST": "", "LIMIT": "60", "KEEP": "4"},
		},
		{
			name:   "the destination goes when the service is no longer backed up",
			app:    volumeApp(""),
			before: map[string]string{"REQUEST": "", "LIMIT": "60", "KEEP": "4", "BACKUP_PATH": "b/x", "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY": "SK"},
			want:   map[string]string{"REQUEST": "", "LIMIT": "60"},
		},
		{
			name:   "a variable with no limit gets the default",
			app:    volumeApp(""),
			before: map[string]string{"REQUEST": ""},
			want:   map[string]string{"REQUEST": "", "LIMIT": "1800"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, nomad := newTestDeployer(t)
			if tt.before != nil {
				nomad.SetVariable(volumeVar, tt.before)
			}
			if _, err := d.Register(ctx, tt.app, deployment("d1"), ownsIDs()); err != nil {
				t.Fatalf("Register: %v", err)
			}
			got := nomad.Variable(volumeVar)
			if !maps.Equal(got, tt.want) || (tt.want == nil) != (got == nil) {
				t.Errorf("variable = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("Nomad refusing the variable stops the register", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FailVariables(403)
		if _, err := d.Register(ctx, volumeApp(""), deployment("d1"), ownsIDs()); err == nil {
			t.Fatal("Register = nil")
		}
		if nomad.Job("whoami") != nil {
			t.Error("the job was registered without its variable")
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestRunningCopy(t *testing.T) {
	ctx := context.Background()
	yes, no := true, false
	copyOfWeb := func(id, status string, version uint64, healthy *bool) api.AllocationListStub {
		s := api.AllocationListStub{ID: id, TaskGroup: "web", ClientStatus: status, JobVersion: version}
		if healthy != nil {
			s.DeploymentStatus = &api.AllocDeploymentStatus{Healthy: healthy}
		}
		return s
	}
	tests := []struct {
		name   string
		copies []api.AllocationListStub
		want   string // "" means none
	}{
		{"no copies", nil, ""},
		{"a healthy copy", []api.AllocationListStub{copyOfWeb("a", "running", 1, &yes)}, "a"},
		{"a running copy outside a rollout", []api.AllocationListStub{copyOfWeb("a", "running", 1, nil)}, "a"},
		{"an unhealthy copy still runs", []api.AllocationListStub{copyOfWeb("a", "running", 1, &no)}, "a"},
		{"a copy still starting", []api.AllocationListStub{copyOfWeb("a", "pending", 1, nil)}, ""},
		{"a running copy whose checks haven't answered", []api.AllocationListStub{
			{ID: "a", TaskGroup: "web", ClientStatus: "running", JobVersion: 1, DeploymentStatus: &api.AllocDeploymentStatus{}}}, ""},
		{"a stopped copy", []api.AllocationListStub{copyOfWeb("a", "complete", 1, nil)}, ""},
		{"the newest version's copy", []api.AllocationListStub{copyOfWeb("old", "running", 1, &yes), copyOfWeb("new", "running", 2, &yes)}, "new"},
		{"another service's copy", []api.AllocationListStub{{ID: "db", TaskGroup: "db", ClientStatus: "running"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, nomad := newTestDeployer(t)
			nomad.SetCopies("whoami", tt.copies...)
			got, ok, err := d.RunningCopy(ctx, "whoami", "web")
			if err != nil || got != tt.want || ok != (tt.want != "") {
				t.Errorf("RunningCopy = %q, %v, %v; want %q", got, ok, err, tt.want)
			}
		})
	}
	t.Run("Nomad down", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		if _, _, err := d.RunningCopy(ctx, "whoami", "web"); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("RunningCopy = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestWaitRequestRendered(t *testing.T) {
	ctx := context.Background()
	rendered := func(id string) string {
		return "KEEP='4'\nREQUEST='" + id + "'\nACTION='backup'\n"
	}

	t.Run("the file shows the request", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		fastRender(d)
		copyID := webCopy(nomad)
		nomad.WriteAllocFile(copyID, nomadjob.VolumeRequestFile, rendered("web-1"))
		if err := d.WaitRequestRendered(ctx, copyID, "web-1"); err != nil {
			t.Errorf("WaitRequestRendered = %v", err)
		}
	})
	t.Run("the file shows it a little later", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		fastRender(d)
		d.Timing.RenderWait = 5 * time.Second
		copyID := webCopy(nomad)
		nomad.WriteAllocFile(copyID, nomadjob.VolumeRequestFile, rendered("web-0")) // the old request
		go func() {
			time.Sleep(30 * time.Millisecond)
			nomad.WriteAllocFile(copyID, nomadjob.VolumeRequestFile, rendered("web-1"))
		}()
		if err := d.WaitRequestRendered(ctx, copyID, "web-1"); err != nil {
			t.Errorf("WaitRequestRendered = %v", err)
		}
	})
	t.Run("it never does", func(t *testing.T) {
		for name, setup := range map[string]func(n *deploytest.Nomad, copyID string){
			"no file": func(n *deploytest.Nomad, copyID string) {},
			"another request": func(n *deploytest.Nomad, copyID string) {
				n.WriteAllocFile(copyID, nomadjob.VolumeRequestFile, rendered("web-0"))
			},
			"a longer ID": func(n *deploytest.Nomad, copyID string) {
				n.WriteAllocFile(copyID, nomadjob.VolumeRequestFile, rendered("web-10"))
			},
			"the ID elsewhere": func(n *deploytest.Nomad, copyID string) {
				n.WriteAllocFile(copyID, nomadjob.VolumeRequestFile, "OBJECT='REQUEST=web-1'\n")
			},
		} {
			t.Run(name, func(t *testing.T) {
				d, _, nomad := newTestDeployer(t)
				fastRender(d)
				copyID := webCopy(nomad)
				setup(nomad, copyID)
				if err := d.WaitRequestRendered(ctx, copyID, "web-1"); !errors.Is(err, ErrRequestNotRendered) {
					t.Errorf("WaitRequestRendered = %v, want ErrRequestNotRendered", err)
				}
			})
		}
	})
	t.Run("a copy that is gone", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		fastRender(d)
		if err := d.WaitRequestRendered(ctx, "nope", "web-1"); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("WaitRequestRendered = %v, want ErrCopyNotFound", err)
		}
	})
	t.Run("a token without read-fs says so", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		fastRender(d)
		copyID := webCopy(nomad)
		nomad.FailFS(403)
		err := d.WaitRequestRendered(ctx, copyID, "web-1")
		if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "read-fs") {
			t.Errorf("WaitRequestRendered = %v, want ErrNomadUnavailable naming read-fs", err)
		}
	})
	t.Run("the caller giving up ends the wait", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		fastRender(d)
		d.Timing.RenderWait = time.Minute
		copyID := webCopy(nomad)
		cctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		if err := d.WaitRequestRendered(cctx, copyID, "web-1"); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("WaitRequestRendered = %v, want the caller's deadline", err)
		}
	})
}

func TestRestartCopy(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	copyID := webCopy(nomad)

	if err := d.RestartCopy(ctx, copyID); err != nil {
		t.Fatalf("RestartCopy = %v", err)
	}
	if got := nomad.Restarts(); len(got) != 1 || got[0] != (deploytest.RestartRequest{Copy: copyID, AllTasks: true}) {
		t.Errorf("restarts = %+v, want one of all tasks", got)
	}
	// The same copy and job version: restarting is not a deploy.
	if nomad.Registrations() != 0 {
		t.Error("restarting registered a job")
	}

	if err := d.RestartCopy(ctx, "nope"); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("RestartCopy of an unknown copy = %v, want ErrCopyNotFound", err)
	}
	nomad.FailRestart(403)
	err := d.RestartCopy(ctx, copyID)
	if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "alloc-lifecycle") {
		t.Errorf("RestartCopy = %v, want ErrNomadUnavailable naming alloc-lifecycle", err)
	}
}

func TestVolumeResultAndSizes(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	copyID := webCopy(nomad)

	t.Run("no result yet", func(t *testing.T) {
		if _, ok, err := d.VolumeResult(ctx, copyID, "web-1"); ok || err != nil {
			t.Errorf("VolumeResult = _, %v, %v; want not ok, no error", ok, err)
		}
	})
	t.Run("a result", func(t *testing.T) {
		nomad.WriteAllocFile(copyID, nomadjob.VolumeResultFile("web-1"),
			"status=succeeded\nmessage=\nobject=b/whoami/web/1/\nvolume.data.bytes=20\nvolume.data.size=50\nexpired=0\n")
		res, ok, err := d.VolumeResult(ctx, copyID, "web-1")
		if err != nil || !ok || !res.Succeeded || res.Object != "b/whoami/web/1/" || len(res.Volumes) != 1 ||
			res.Volumes[0] != (nomadjob.VolumeOutcome{Name: "data", Bytes: 20, SizeKB: 50}) || len(res.Expired) != 1 {
			t.Errorf("VolumeResult = %+v, %v, %v", res, ok, err)
		}
	})
	t.Run("another request's result isn't this one's", func(t *testing.T) {
		if _, ok, _ := d.VolumeResult(ctx, copyID, "web-2"); ok {
			t.Error("web-2 has a result")
		}
	})
	t.Run("a file that isn't a result", func(t *testing.T) {
		nomad.WriteAllocFile(copyID, nomadjob.VolumeResultFile("web-3"), "garbage\n")
		if _, ok, err := d.VolumeResult(ctx, copyID, "web-3"); ok || err == nil {
			t.Errorf("VolumeResult = %v, %v; want an error", ok, err)
		}
	})
	t.Run("a copy that is gone", func(t *testing.T) {
		if _, _, err := d.VolumeResult(ctx, "nope", "web-1"); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("VolumeResult = %v, want ErrCopyNotFound", err)
		}
	})
	t.Run("sizes", func(t *testing.T) {
		got, err := d.VolumeSizes(ctx, copyID)
		if err != nil || len(got) != 0 {
			t.Fatalf("VolumeSizes before the task measured = %v, %v; want none", got, err)
		}
		nomad.WriteAllocFile(copyID, nomadjob.VolumeSizesFile, "data 5000\nlogs 12\n")
		got, err = d.VolumeSizes(ctx, copyID)
		if err != nil || got["data"] != 5000 || got["logs"] != 12 || len(got) != 2 {
			t.Errorf("VolumeSizes = %v, %v", got, err)
		}
		if _, err := d.VolumeSizes(ctx, "nope"); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("VolumeSizes of a gone copy = %v, want ErrCopyNotFound", err)
		}
	})
}

// TestRestartPlaysTheTask: the fake's restart hook is how a test plays the
// caravana-volumes task, and the restart's events feed the stop duration and
// the requested-restart count.
func TestRestartPlaysTheTask(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	copyID := webCopy(nomad)
	nomad.SetRestartStop(3 * time.Second)
	nomad.OnRestart(func(id string) {
		nomad.WriteAllocFile(id, nomadjob.VolumeResultFile("web-1"), "status=succeeded\nobject=b/1/\n")
	})

	before := time.Now().Add(-time.Second)
	if _, ok, _ := d.StopDuration(ctx, copyID, "web", before); ok {
		t.Error("a copy that wasn't restarted has a stop duration")
	}
	if _, ok, _ := d.VolumeResult(ctx, copyID, "web-1"); ok {
		t.Fatal("the result is there before the restart")
	}
	if err := d.RestartCopy(ctx, copyID); err != nil {
		t.Fatal(err)
	}
	if res, ok, err := d.VolumeResult(ctx, copyID, "web-1"); err != nil || !ok || !res.Succeeded {
		t.Errorf("VolumeResult after the restart = %+v, %v, %v", res, ok, err)
	}
	stopped, ok, err := d.StopDuration(ctx, copyID, "web", before)
	if err != nil || !ok || stopped != 3*time.Second {
		t.Errorf("StopDuration = %v, %v, %v; want 3s", stopped, ok, err)
	}
	if _, ok, _ := d.StopDuration(ctx, copyID, "web", time.Now().Add(time.Minute)); ok {
		t.Error("a restart before 'since' counts")
	}
	if _, _, err := d.StopDuration(ctx, "nope", "web", before); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("StopDuration of a gone copy = %v, want ErrCopyNotFound", err)
	}

	copies, err := d.Copies(ctx, "whoami")
	if err != nil || len(copies) != 1 {
		t.Fatalf("Copies = %+v, %v", copies, err)
	}
	// One restart signals each task: here only the service's own exists.
	if copies[0].RequestedRestarts != 1 {
		t.Errorf("RequestedRestarts = %d, want 1", copies[0].RequestedRestarts)
	}
}

func TestStopDuration(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	ev := func(typ string, at time.Duration) *api.TaskEvent {
		return &api.TaskEvent{Type: typ, Time: t0.Add(at).UnixNano()}
	}
	tests := []struct {
		name   string
		events []*api.TaskEvent
		since  time.Time
		want   time.Duration
		wantOK bool
	}{
		{"terminated, then started", []*api.TaskEvent{ev("Terminated", 10*time.Second), ev("Started", 14*time.Second)}, t0, 4 * time.Second, true},
		{"still stopped", []*api.TaskEvent{ev("Terminated", 10*time.Second)}, t0, 0, false},
		{"no events", nil, t0, 0, false},
		{"the first start is before the stop", []*api.TaskEvent{ev("Started", time.Second), ev("Terminated", 10*time.Second), ev("Started", 12*time.Second)}, t0, 2 * time.Second, true},
		{"an older restart is ignored", []*api.TaskEvent{ev("Terminated", time.Second), ev("Started", 2*time.Second), ev("Terminated", 20*time.Second), ev("Started", 25*time.Second)}, t0.Add(10 * time.Second), 5 * time.Second, true},
		{"only older events", []*api.TaskEvent{ev("Terminated", time.Second), ev("Started", 2*time.Second)}, t0.Add(10 * time.Second), 0, false},
		{"other events between", []*api.TaskEvent{ev("Restart Signaled", 9*time.Second), ev("Terminated", 10*time.Second), ev("Restarting", 10*time.Second), ev("Started", 11*time.Second)}, t0, time.Second, true},
		{"a nil event", []*api.TaskEvent{nil, ev("Terminated", time.Second), ev("Started", 3*time.Second)}, t0, 2 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stopDuration(tt.events, tt.since)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("stopDuration = %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestSyncBackupsLeavesVolumeBackupsAlone: only Postgres backups are Nomad
// jobs, and a service that switched engine loses its old job.
func TestSyncBackupsLeavesVolumeBackupsAlone(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	digests := map[string]string{"db": "postgres@sha256:" + whoamiDigest[7:]}
	creds := map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}

	a := backedUp()
	if err := d.SyncBackups(ctx, a, digests, creds, testDest); err != nil {
		t.Fatal(err)
	}
	if nomad.Job("shop_db_backup") == nil {
		t.Fatal("no postgres backup job to begin with")
	}

	a.Services[0].Backup = &app.Backup{Engine: app.BackupVolume}
	a.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	if err := d.SyncBackups(ctx, a, digests, creds, testDest); err != nil {
		t.Fatal(err)
	}
	if nomad.Job("shop_db_backup") != nil || nomad.Variable("nomad/jobs/shop_db_backup") != nil {
		t.Error("the old postgres job or its variable is still there")
	}
	if got := nomad.VariablePaths(); len(got) != 0 {
		t.Errorf("variables = %v, want none: a volume backup is no Nomad job", got)
	}
}

// TestSetBackupDestinationReachesVolumeBackups: the new destination goes into
// the variable of every backed-up volume service, keeping its request, and
// into none that has no destination yet.
func TestSetBackupDestinationReachesVolumeBackups(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := volumeApp(app.BackupVolume)
	req := &VolumeRequest{ID: "web-1", Action: VolumeBackup, TS: "1", NotAfter: time.Now().Add(time.Hour)}
	if err := d.WriteVolumeVariable(ctx, a, a.Services[0], &testDest, 4, req); err != nil {
		t.Fatal(err)
	}
	// A service with a volume and no backup: no destination in its variable.
	nomad.SetVariable("nomad/jobs/other/web/caravana-volumes", map[string]string{"REQUEST": "", "LIMIT": "1800"})

	moved := testDest
	moved.Bucket, moved.Prefix, moved.SecretAccessKey = "other", "", "SK2"
	if err := d.SetBackupDestination(ctx, moved); err != nil {
		t.Fatal(err)
	}
	got := nomad.Variable(volumeVar)
	if got["BACKUP_PATH"] != "other/whoami/web" || got["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK2" {
		t.Errorf("destination = %v", got)
	}
	if got["REQUEST"] != "web-1" || got["ACTION"] != "backup" || got["TS"] != "1" || got["KEEP"] != "4" || got["LIMIT"] != "1800" {
		t.Errorf("the request or the rest was lost: %v", got)
	}
	if other := nomad.Variable("nomad/jobs/other/web/caravana-volumes"); len(other) != 2 || other["BACKUP_PATH"] != "" {
		t.Errorf("a service without a backup got the destination: %v", other)
	}
}

func TestVolumeVariablePathMatchesTheJob(t *testing.T) {
	a := volumeApp(app.BackupVolume)
	res, err := nomadjob.Translate(a, apptest.Whoami().Deployment, labSettings, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, task := range res.Job.TaskGroups[0].Tasks {
		if task.Name == nomadjob.VolumeTaskName {
			found = strings.Contains(*task.Templates[0].EmbeddedTmpl, `"`+nomadjob.VolumeVariablePath("whoami", "web")+`"`)
		}
	}
	if !found || nomadjob.VolumeVariablePath("whoami", "web") != volumeVar {
		t.Error("the task reads another variable than the one Caravana writes")
	}
}
