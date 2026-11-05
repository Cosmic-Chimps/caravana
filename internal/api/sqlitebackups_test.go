package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Feature 029: a sqlite backup is a policy with a file, refused on the
// backup's file field when the file is unusable, and its runs answer in the
// contract's Backup schema with the engine "sqlite".
func TestSQLiteBackupEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	overlay := func(backup map[string]any) string {
		return composeBody(t, volumeCompose, map[string]any{"kuma": map[string]any{"backup": backup}})
	}

	refusals := []struct {
		name   string
		backup map[string]any
	}{
		{"no file", map[string]any{"engine": "sqlite"}},
		{"a file outside every volume", map[string]any{"engine": "sqlite", "file": "/tmp/app.db"}},
		{"the WAL file", map[string]any{"engine": "sqlite", "file": "/app/data/app.db-wal"}},
		{"a file with another engine", map[string]any{"engine": "volume", "file": "/app/data/app.db"}},
	}
	for _, tt := range refusals {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", overlay(tt.backup))
		got := decodeAs[errorBody](t, raw).Error
		if status != http.StatusBadRequest || got.Code != "invalid_field" || got.Field != "services.kuma.backup.file" {
			t.Errorf("%s: deploy = %d %s, want 400 invalid_field on services.kuma.backup.file (Compose names the service)", tt.name, status, raw)
		}
	}

	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json",
		overlay(map[string]any{"engine": "sqlite", "file": "/app/data/app.db", "keep": 2})); status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop", "", "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"file":"/app/data/app.db"`) {
		t.Errorf("GET app = %d %s, want the policy's file", status, raw)
	}

	e.Nomad.SetCopies("shop", api.AllocationListStub{ID: "c1", TaskGroup: "kuma", ClientStatus: "running", JobVersion: 1})
	e.Nomad.OnExec(func(req deploytest.ExecRequest) deploytest.ExecReply {
		switch req.Command[2] {
		case nomadjob.SQLiteCopyScript:
			return deploytest.ExecReply{Stdout: "caravana-sqlite-copy bytes=4096\n"}
		case nomadjob.SQLiteUploadScript:
			return deploytest.ExecReply{Stdout: "caravana-backup key=backups/gate/shop/kuma/1.sqlite.gz bytes=900\n"}
		}
		return deploytest.ExecReply{ExitCode: 99}
	})
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "kuma"}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST backups = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Backup")
	if started := decodeAs[backupJSON](t, raw); started.Engine != "sqlite" || started.Status != "running" {
		t.Errorf("the started backup = %+v, want a running sqlite run", started)
	}

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
	if done.Status != "succeeded" || done.Engine != "sqlite" || done.Bytes != 900 || done.StoppedSeconds != nil {
		t.Errorf("the finished backup = %+v, want succeeded, sqlite, 900 bytes, no stop time", done)
	}
}
