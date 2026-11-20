package api

import (
	"net/http"
	"strings"
	"testing"
)

// Feature 046: the mysql and mongodb engines are policies like postgres's.
// A deploy without the login its image reads is 400 on services.<name>.backup
// naming the variables; with it the app deploys, and Back up now answers in the
// contract's Backup schema with the engine.
func TestDumpEngineBackupEndpoints(t *testing.T) {
	const password = "dump-pw-marker"
	tests := []struct {
		engine   string
		port     string
		env      []string // the service's environment in the file, a good one
		bad      []string // an environment that lacks the login
		wantName string   // a variable the refusal names
	}{
		{"mysql", "3306", []string{"MARIADB_ROOT_PASSWORD: " + password}, []string{"MARIADB_DATABASE: shop"}, "MARIADB_ROOT_PASSWORD"},
		{"mongodb", "27017", []string{"MONGO_INITDB_ROOT_USERNAME: root", "MONGO_INITDB_ROOT_PASSWORD: " + password},
			[]string{"MONGO_INITDB_ROOT_USERNAME: root"}, "MONGO_INITDB_ROOT_PASSWORD"},
	}
	for _, tc := range tests {
		t.Run(tc.engine, func(t *testing.T) {
			e := newTestEnv(t, "")
			if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
				t.Fatalf("PUT destination = %d %s", status, raw)
			}
			body := func(env []string) string {
				file := "services:\n  db:\n    image: traefik/whoami:latest\n    ports: [\"" + tc.port + "\"]\n    environment:\n      " +
					strings.Join(env, "\n      ") + "\n"
				return composeBody(t, file, map[string]any{"db": map[string]any{"backup": map[string]any{"engine": tc.engine, "keep": 2}}})
			}

			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", body(tc.bad))
			got := decodeAs[errorBody](t, raw).Error
			if status != http.StatusBadRequest || got.Code != "invalid_field" || got.Field != "services.db.backup" || !strings.Contains(got.Message, tc.wantName) {
				t.Fatalf("deploy without a login = %d %s, want 400 invalid_field on services.db.backup naming %s", status, raw, tc.wantName)
			}

			if status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", body(tc.env)); status != http.StatusAccepted {
				t.Fatalf("deploy = %d %s", status, raw)
			}
			status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "db"}`)
			if status != http.StatusAccepted {
				t.Fatalf("POST backups = %d %s", status, raw)
			}
			checkSchema(t, loadContract(t), raw, "Backup")
			if started := decodeAs[backupJSON](t, raw); started.Engine != tc.engine || started.Status != "running" {
				t.Errorf("the started backup = %+v, want a running %s run", started, tc.engine)
			}
			if status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups", "application/json", `{"service": "db"}`); status != http.StatusConflict ||
				decodeAs[errorBody](t, raw).Error.Code != codeBackupRunning {
				t.Errorf("second POST backups = %d %s, want 409 backup_running", status, raw)
			}

			status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop/backups", "", "")
			if status != http.StatusOK {
				t.Fatalf("GET backups = %d %s", status, raw)
			}
			checkSchema(t, loadContract(t), raw, "BackupList")
			if !strings.Contains(string(raw), `"engine":"`+tc.engine+`"`) {
				t.Errorf("GET backups = %s, want the %s engine", raw, tc.engine)
			}
			for _, out := range []string{string(raw), e.Logs.String(), mustJSONString(t, e.Nomad.Job("shop_db_backup"))} {
				if strings.Contains(out, password) {
					t.Errorf("the password leaked into: %.200s", out)
				}
			}
		})
	}
}
