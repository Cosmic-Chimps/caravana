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
			want: `{"id":"db-1","service":"db","engine":"postgres","status":"succeeded","trigger":"timer","bytes":10,"startedAt":"0001-01-01T00:00:00Z"}`,
		},
		{
			name: "volume, running: no stop time yet",
			in:   store.BackupRecord{ID: "web-2", Service: "web", Engine: "volume", Status: "running", Trigger: "owner"},
			want: `{"id":"web-2","service":"web","engine":"volume","status":"running","trigger":"owner","bytes":0,"startedAt":"0001-01-01T00:00:00Z"}`,
		},
		{
			name: "volume, finished: stop time and archives",
			in: store.BackupRecord{ID: "web-3", Service: "web", Engine: "volume", Status: "succeeded", Bytes: 2300, Stopped: &stopped,
				Volumes: []store.VolumeArchive{{Name: "data", Bytes: 2000, SizeKB: 5}, {Name: "uploads", Bytes: 300, SizeKB: 1}}},
			want: `{"id":"web-3","service":"web","engine":"volume","status":"succeeded","trigger":"timer","bytes":2300,"startedAt":"0001-01-01T00:00:00Z",` +
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
	want := `{"id":"r1","service":"web","backupId":"web-3","engine":"volume","status":"succeeded","createdAt":"0001-01-01T00:00:00Z","stoppedSeconds":4.2,"trigger":"owner"}`
	if got != want {
		t.Errorf("restore JSON =\n%s\nwant\n%s", got, want)
	}

	// A pinned backup and a scheduled restore (feature 036).
	pinnedAt := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)
	got = mustJSONString(t, toBackupJSON(store.BackupRecord{ID: "web-3", Service: "web", Status: "succeeded", Pin: "golden", PinnedAt: &pinnedAt}))
	want = `{"id":"web-3","service":"web","engine":"postgres","status":"succeeded","trigger":"timer","bytes":0,"startedAt":"0001-01-01T00:00:00Z",` +
		`"pin":"golden","pinnedAt":"2026-11-13T04:00:00Z"}`
	if got != want {
		t.Errorf("pinned backup JSON =\n%s\nwant\n%s", got, want)
	}
	got = mustJSONString(t, toRestoreJSON(store.RestoreRecord{ID: "r2", Service: "web", Engine: "volume", Status: "skipped", Trigger: "schedule", Pin: "golden"}))
	want = `{"id":"r2","service":"web","backupId":"","engine":"volume","status":"skipped","createdAt":"0001-01-01T00:00:00Z","trigger":"schedule","pin":"golden"}`
	if got != want {
		t.Errorf("scheduled restore JSON =\n%s\nwant\n%s", got, want)
	}
}

// Feature 036: pinBackup and unpinBackup, and the fields they add to a backup
// and a restore.
func TestPinBackupEndpoints(t *testing.T) {
	ctx := t.Context()
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", volumeBackupBody(t))
	if status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	waitHealthy(t, e, "shop", decodeAs[deployment](t, raw).ID) // the app is held while it deploys
	started := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	done := started.Add(time.Second)
	save := func(id, status string, expired bool) {
		t.Helper()
		err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "shop", ID: id, Service: "kuma", Engine: "volume", Status: status,
			Object: "b/shop/kuma/" + id + "/", StartedAt: started, FinishedAt: &done, Expired: expired})
		if err != nil {
			t.Fatal(err)
		}
	}
	save("kuma-1791990000", store.BackupSucceeded, false)
	save("kuma-1791990100", store.BackupSucceeded, false)
	save("kuma-1791990200", store.BackupFailed, false)
	save("kuma-1791990300", store.BackupSucceeded, true)
	pin := func(method, id, body string) (int, []byte) {
		t.Helper()
		status, _, raw := call(t, method, e.URL+"/v1/apps/shop/backups/"+id+"/pin", "application/json", body)
		return status, raw
	}
	codeOf := func(raw []byte) string { return decodeAs[errorBody](t, raw).Error.Code }

	// Pin: 200 with the pin on the Backup, in the contract's shape; the label moves.
	status, raw = pin(http.MethodPut, "kuma-1791990000", `{"label": "golden"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT pin = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Backup")
	if b := decodeAs[backupJSON](t, raw); b.Pin != "golden" || b.PinnedAt == nil || b.ID != "kuma-1791990000" {
		t.Errorf("pinned backup = %+v", b)
	}
	// With a parameter on the media type, as every JSON body is accepted.
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/apps/shop/backups/kuma-1791990100/pin", "application/json; charset=UTF-8", `{"label": "golden"}`)
	if status != http.StatusOK || decodeAs[backupJSON](t, raw).Pin != "golden" {
		t.Fatalf("PUT pin with a charset = %d %s", status, raw)
	}
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop/backups", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET backups = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "BackupList")
	pinned := map[string]string{}
	for _, b := range decodeAs[struct{ Backups []backupJSON }](t, raw).Backups {
		pinned[b.ID] = b.Pin
	}
	if pinned["kuma-1791990000"] != "" || pinned["kuma-1791990100"] != "golden" {
		t.Errorf("pins after the label moved = %v", pinned)
	}

	// Refusals.
	for _, tt := range []struct {
		name         string
		method, id   string
		body         string
		wantStatus   int
		wantCode     string
		wantFieldNot string
	}{
		{"a bad label", http.MethodPut, "kuma-1791990000", `{"label": "Golden"}`, http.StatusBadRequest, "invalid_field", "label"},
		{"no label", http.MethodPut, "kuma-1791990000", `{}`, http.StatusBadRequest, "invalid_field", "label"},
		{"a backup that failed", http.MethodPut, "kuma-1791990200", `{"label": "x"}`, http.StatusNotFound, "not_found", ""},
		{"a backup retention deleted", http.MethodPut, "kuma-1791990300", `{"label": "x"}`, http.StatusNotFound, "not_found", ""},
		{"an unknown backup", http.MethodPut, "kuma-1", `{"label": "x"}`, http.StatusNotFound, "not_found", ""},
		{"unpinning an unknown backup", http.MethodDelete, "kuma-1", "", http.StatusNotFound, "not_found", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, raw := pin(tt.method, tt.id, tt.body)
			got := decodeAs[errorBody](t, raw).Error
			if status != tt.wantStatus || got.Code != tt.wantCode || got.Field != tt.wantFieldNot {
				t.Errorf("= %d %s, want %d %s field %q", status, raw, tt.wantStatus, tt.wantCode, tt.wantFieldNot)
			}
		})
	}
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/apps/nope/backups/kuma-1/pin", "application/json", `{"label": "x"}`); status != http.StatusNotFound {
		t.Errorf("PUT pin in an unknown app = %d %s", status, raw)
	}

	// A running backup of the service: 409 backup_running, for both verbs.
	err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "shop", ID: "kuma-1792000000", Service: "kuma", Engine: "volume",
		Status: store.BackupRunning, StartedAt: started})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ method, body string }{{http.MethodPut, `{"label": "other"}`}, {http.MethodDelete, ""}} {
		id := "kuma-1791990000"
		if tt.method == http.MethodDelete {
			id = "kuma-1791990100"
		}
		if status, raw := pin(tt.method, id, tt.body); status != http.StatusConflict || codeOf(raw) != codeBackupRunning {
			t.Errorf("%s pin while a backup runs = %d %s, want 409 backup_running", tt.method, status, raw)
		}
	}
	finished := started.Add(time.Minute)
	if err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "shop", ID: "kuma-1792000000", Service: "kuma", Engine: "volume",
		Status: store.BackupFailed, StartedAt: started, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}

	// 409 app_busy, for an app a deploy or a volume run holds, is the service's
	// ErrAppBusy through actionError (TestActionErrors); the service tests hold the lock.

	// Unpin: 200 without the pin; a second time too.
	for range 2 {
		status, raw = pin(http.MethodDelete, "kuma-1791990100", "")
		if status != http.StatusOK {
			t.Fatalf("DELETE pin = %d %s", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Backup")
		if b := decodeAs[backupJSON](t, raw); b.Pin != "" || b.PinnedAt != nil {
			t.Errorf("unpinned backup = %+v", b)
		}
	}
}

// Nomad refusing the Postgres job's pin list is a 502, and nothing changes.
func TestPinBackupNomadRefuses(t *testing.T) {
	ctx := t.Context()
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", dbCompose)
	if status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	waitHealthy(t, e, "shop", decodeAs[deployment](t, raw).ID)
	started := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	done := started.Add(time.Second)
	if err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "shop", ID: "db-1791990000", Service: "db", Status: store.BackupSucceeded,
		Object: "b/shop/db/1791990000.dump", StartedAt: started, FinishedAt: &done}); err != nil {
		t.Fatal(err)
	}
	e.Nomad.FailVariables(http.StatusForbidden)
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/apps/shop/backups/db-1791990000/pin", "application/json", `{"label": "golden"}`)
	e.Nomad.FailVariables(0)
	if status != http.StatusBadGateway {
		t.Fatalf("PUT pin with Nomad refusing = %d %s, want 502", status, raw)
	}
	if b, err := e.Store.GetBackup(ctx, "shop", "db-1791990000"); err != nil || b.Pin != "" {
		t.Errorf("backup after the refusal = %+v, %v; want it unpinned", b, err)
	}
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/apps/shop/backups/db-1791990000/pin", "application/json", `{"label": "golden"}`); status != http.StatusOK {
		t.Fatalf("PUT pin = %d %s", status, raw)
	}
	if got := e.Nomad.Variable("nomad/jobs/shop_db_backup")["BACKUP_PINNED"]; got != "1791990000" {
		t.Errorf("BACKUP_PINNED = %q, want 1791990000", got)
	}
}

// A restore says who started it, and a scheduled one the label it asked for.
func TestRestoreJSONTrigger(t *testing.T) {
	ctx := t.Context()
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", volumeBackupBody(t)); status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	at := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)
	done := at.Add(time.Second)
	for _, r := range []store.RestoreRecord{
		{ID: "r-owner", AppID: "shop", Service: "kuma", BackupID: "kuma-1", Engine: "volume", Status: store.BackupSucceeded, CreatedAt: at, FinishedAt: &done},
		{ID: "r-skip", AppID: "shop", Service: "kuma", Engine: "volume", Status: store.RestoreSkipped, Message: "a backup or restore of kuma was running",
			CreatedAt: at.Add(time.Hour), FinishedAt: &done, Trigger: store.RestoreTriggerSchedule, Pin: "golden"},
	} {
		if err := e.Store.SaveRestore(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop/backups", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET backups = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "BackupList")
	got := map[string]restoreJSON{}
	for _, r := range decodeAs[struct{ Restores []restoreJSON }](t, raw).Restores {
		got[r.ID] = r
	}
	if r := got["r-owner"]; r.Trigger != "owner" || r.Pin != "" {
		t.Errorf("owner's restore = %+v", r)
	}
	if r := got["r-skip"]; r.Trigger != "schedule" || r.Pin != "golden" || r.Status != "skipped" || r.BackupID != "" {
		t.Errorf("skipped restore = %+v", r)
	}
}

// backup.restore travels with the app through deployApp and getApp, and a bad
// one is a field error on its own path.
func TestBackupPolicyRestoreField(t *testing.T) {
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	body := func(restore string) string {
		return `{"id": "shop", "source": {"kind": "image"}, "services": [{"name": "kuma", "image": "traefik/whoami:latest",
		  "volumes": [{"name": "data", "path": "/data"}], "backup": {"engine": "volume", "restore": ` + restore + `}}]}`
	}
	for _, tt := range []struct{ name, restore, field string }{
		{"a bad schedule", `{"schedule": "nightly", "from": "golden"}`, "services[0].backup.restore.schedule"},
		{"no schedule", `{"from": "golden"}`, "services[0].backup.restore.schedule"},
		{"a bad label", `{"schedule": "0 4 * * *", "from": "Golden"}`, "services[0].backup.restore.from"},
		{"no label", `{"schedule": "0 4 * * *"}`, "services[0].backup.restore.from"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/deployments", "application/json", body(tt.restore))
			if got := decodeAs[errorBody](t, raw).Error; status != http.StatusBadRequest || got.Code != "invalid_field" || got.Field != tt.field {
				t.Errorf("= %d %s, want 400 invalid_field on %s", status, raw, tt.field)
			}
		})
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/deployments", "application/json", body(`{"schedule": "0 4 * * *", "from": "golden"}`))
	if status != http.StatusAccepted {
		t.Fatalf("deploy with a restore schedule = %d %s", status, raw)
	}
	if !strings.Contains(string(raw), "services[0].backup.restore.from") {
		t.Errorf("deploy = %s, want the warning that nothing is pinned as golden", raw)
	}
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET app = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")
	if !strings.Contains(string(raw), `"restore":{"schedule":"0 4 * * *","from":"golden"}`) {
		t.Errorf("GET app = %s, want the restore schedule", raw)
	}
}
