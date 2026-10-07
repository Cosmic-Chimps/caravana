package api

import (
	"net/http"
	"strings"
	"testing"
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
