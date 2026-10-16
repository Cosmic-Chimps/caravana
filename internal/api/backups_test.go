package api

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/store"
)

const destBody = `{"endpoint": "http://10.0.0.5:9000", "region": "us-east-1", "bucket": "backups",
  "accessKeyId": "AK", "secretAccessKey": "` + secretMarker + `"}`

// dbCompose is a backed-up postgres-like service the fake registry resolves.
const dbCompose = `{"compose": "services:\n  db:\n    image: traefik/whoami:latest\n    ports: [\"5432\"]\n    environment:\n      POSTGRES_PASSWORD: pw\n",
  "services": {"db": {"backup": {"engine": "postgres", "keep": 2}}}}`

func TestBackupDestinationEndpoints(t *testing.T) {
	e := newTestEnv(t, "")

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/backups/destination", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("GET destination before one is set = %d %s", status, raw)
	}
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", dbCompose)
	if status != http.StatusBadRequest || decodeAs[errorBody](t, raw).Error.Field != "services.db.backup" {
		t.Fatalf("deploy with backups and no destination = %d %s", status, raw)
	}

	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody)
	if status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "BackupDestination")
	if strings.Contains(string(raw), secretMarker) {
		t.Errorf("PUT destination answered the secret: %s", raw)
	}
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/backups/destination", "", "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"bucket":"backups"`) || strings.Contains(string(raw), secretMarker) {
		t.Errorf("GET destination = %d %s", status, raw)
	}
	bad := strings.Replace(destBody, `"bucket": "backups"`, `"bucket": "No_Such"`, 1)
	if status, _, raw = call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", bad); status != http.StatusBadRequest ||
		decodeAs[errorBody](t, raw).Error.Field != "bucket" {
		t.Errorf("PUT a bad bucket = %d %s", status, raw)
	}
}

func TestBackupEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", dbCompose)
	if status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "db"}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST backups = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Backup")
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "db"}`)
	if status != http.StatusConflict || decodeAs[errorBody](t, raw).Error.Code != codeBackupRunning {
		t.Errorf("second POST backups = %d %s, want 409 backup_running", status, raw)
	}
	if status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "nope"}`); status != http.StatusNotFound {
		t.Errorf("POST backups for an unknown service = %d %s", status, raw)
	}
	if status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups/db-1/restore", "application/json", ""); status != http.StatusNotFound {
		t.Errorf("restore of an unknown backup = %d %s", status, raw)
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop/backups", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET backups = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "BackupList")
	if !strings.Contains(string(raw), `"status":"running"`) {
		t.Errorf("GET backups = %s, want the running backup", raw)
	}

	for _, out := range []string{string(raw), e.Logs.String(), mustJSONString(t, e.Nomad.Job("shop_db_backup"))} {
		if strings.Contains(out, secretMarker) {
			t.Errorf("the secret key leaked into: %.200s", out)
		}
	}
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	return string(mustJSON(t, v))
}

// volumeCompose is a service with a volume the fake registry resolves, backed
// up with the volume engine (feature 016): no port and no credentials needed.
const volumeCompose = "services:\n  kuma:\n    image: traefik/whoami:latest\n    volumes: [\"kuma-data:/app/data\"]\nvolumes:\n  kuma-data: {}\n"

// volumeBackupBody deploys volumeCompose with a volume backup.
func volumeBackupBody(t *testing.T) string {
	t.Helper()
	return composeBody(t, volumeCompose, map[string]any{"kuma": map[string]any{"backup": map[string]any{"engine": "volume", "keep": 2}}})
}

// Feature 016: a volume backup answers 409 no_running_copy while the service
// has no copy to stop, and otherwise starts, runs and records its size and the
// time the service was stopped. The test plays the copy's caravana-volumes
// task: it renders the request the API wrote, and answers the restart with a
// result file.
func TestVolumeBackupEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", volumeBackupBody(t)); status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}

	const copyID = "c1"
	const variable = "nomad/jobs/shop/kuma/caravana-volumes"
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "kuma"}`)
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusConflict || got.Code != codeNoRunningCopy ||
		got.Message != "The service has no running copy. Deploy it first." {
		t.Fatalf("POST backups without a running copy = %d %s, want 409 no_running_copy", status, raw)
	}

	e.Nomad.SetCopies("shop", api.AllocationListStub{ID: copyID, TaskGroup: "kuma", ClientStatus: "running", JobVersion: 1})
	e.Nomad.SetRestartStop(1500 * time.Millisecond)
	// The restart is held open until the test lets the run finish, so that the
	// second request below finds the first one still running.
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish() // a failed assertion must not leave the run blocking the cleanup
	e.Nomad.OnRestart(func(copyID string) {
		<-release
		id := e.Nomad.Variable(variable)["REQUEST"]
		e.Nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\nmessage=\nobject=backups/gate/shop/kuma/1791990000/\n"+
			"volume.kuma-data.bytes=2000\nvolume.kuma-data.size=5\n")
	})
	// The task renders the request into its file as soon as it is written.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			if id := e.Nomad.Variable(variable)["REQUEST"]; id != "" {
				e.Nomad.WriteAllocFile(copyID, "caravana-volumes/local/request.env", "REQUEST='"+id+"'\n")
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "kuma"}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST backups = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Backup")
	started := decodeAs[backupJSON](t, raw)
	if started.Engine != "volume" || started.Status != "running" || started.StoppedSeconds != nil || started.Volumes != nil {
		t.Errorf("the started backup = %+v, want a running volume run with no stop time or volumes yet", started)
	}
	if status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "kuma"}`); status != http.StatusConflict ||
		decodeAs[errorBody](t, raw).Error.Code != codeBackupRunning {
		t.Errorf("a second POST backups = %d %s, want 409 backup_running", status, raw)
	}
	finish()

	var done backupJSON
	waitUntil(t, func() bool {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop/backups", "", "")
		if status != http.StatusOK {
			t.Fatalf("GET backups = %d %s", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "BackupList")
		for _, b := range decodeAs[struct{ Backups []backupJSON }](t, raw).Backups {
			if b.Status != "running" {
				done = b
				return true
			}
		}
		return false
	})
	if done.Status != "succeeded" || done.Engine != "volume" || done.Bytes != 2000 {
		t.Fatalf("the finished backup = %+v, want succeeded, volume, 2000 bytes", done)
	}
	if done.StoppedSeconds == nil || *done.StoppedSeconds < 1 {
		t.Errorf("stoppedSeconds = %v, want about 1.5", done.StoppedSeconds)
	}
	if len(done.Volumes) != 1 || done.Volumes[0] != (backupVolumeJSON{Name: "kuma-data", Bytes: 2000, SizeBytes: 5 * 1024}) {
		t.Errorf("volumes = %+v, want kuma-data with 2000 archive bytes and 5120 bytes of data", done.Volumes)
	}
}

// The Backup and Restore JSON: every record names its engine (a record from
// before feature 016 is postgres), and the stop time and archives appear only
// for a volume run.
func TestBackupJSONFields(t *testing.T) {
	stopped := 4200 * time.Millisecond
	tests := []struct {
		name string
		in   store.BackupRecord
		want string
	}{
		{
			name: "postgres, an old record",
			in:   store.BackupRecord{ID: "db-1", Service: "db", Status: "succeeded", Bytes: 10},
			want: `{"id":"db-1","service":"db","engine":"postgres","status":"succeeded","bytes":10,"startedAt":"0001-01-01T00:00:00Z"}`,
		},
		{
			name: "volume, running: no stop time yet",
			in:   store.BackupRecord{ID: "web-2", Service: "web", Engine: "volume", Status: "running"},
			want: `{"id":"web-2","service":"web","engine":"volume","status":"running","bytes":0,"startedAt":"0001-01-01T00:00:00Z"}`,
		},
		{
			name: "volume, finished: stop time and archives",
			in: store.BackupRecord{ID: "web-3", Service: "web", Engine: "volume", Status: "succeeded", Bytes: 2300, Stopped: &stopped,
				Volumes: []store.VolumeArchive{{Name: "data", Bytes: 2000, SizeKB: 5}, {Name: "uploads", Bytes: 300, SizeKB: 1}}},
			want: `{"id":"web-3","service":"web","engine":"volume","status":"succeeded","bytes":2300,"startedAt":"0001-01-01T00:00:00Z",` +
				`"stoppedSeconds":4.2,"volumes":[{"name":"data","bytes":2000,"sizeBytes":5120},{"name":"uploads","bytes":300,"sizeBytes":1024}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustJSONString(t, toBackupJSON(tt.in)); got != tt.want {
				t.Errorf("backup JSON =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}

	got := mustJSONString(t, toRestoreJSON(store.RestoreRecord{ID: "r1", Service: "web", BackupID: "web-3", Engine: "volume", Status: "succeeded", Stopped: &stopped}))
	want := `{"id":"r1","service":"web","backupId":"web-3","engine":"volume","status":"succeeded","createdAt":"0001-01-01T00:00:00Z","stoppedSeconds":4.2}`
	if got != want {
		t.Errorf("restore JSON =\n%s\nwant\n%s", got, want)
	}
}
