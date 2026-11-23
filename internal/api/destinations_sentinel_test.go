package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"
)

// destinationSentinel is the secret key of a destination. It goes in with a create and an update, and a
// failing bucket would repeat it in its own words; no answer of any route, no log line, no error, no
// stored row or file and no job may hold it. Only the Nomad Variables that hold keys do.
const destinationSentinel = "s3cret-sentinel-052-api-key"

func TestDestinationSecretKeyIsNowhere(t *testing.T) {
	e := newTestEnv(t, "")
	var seen []string
	do := func(method, path, body string) int {
		t.Helper()
		ctype := ""
		if body != "" {
			ctype = "application/json"
		}
		status, _, raw := call(t, method, e.URL+path, ctype, body)
		seen = append(seen, method+" "+path+" -> "+string(raw))
		return status
	}

	// Create, update (the key in the body again), and with the key in a refused or unreadable body.
	if status := do(http.MethodPut, "/v1/backups/destinations/main", destPut("backups", "SK-main")); status != http.StatusCreated {
		t.Fatalf("PUT main = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destinations/offsite", destPut("offsite-bkt", destinationSentinel)); status != http.StatusCreated {
		t.Fatalf("PUT offsite = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destinations/offsite", destPut("offsite-two", destinationSentinel)); status != http.StatusOK {
		t.Fatalf("PUT offsite again = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destinations/offsite", destPut("No_Such", destinationSentinel)); status != http.StatusBadRequest {
		t.Errorf("PUT a bad bucket = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destinations/Bad_Name", destPut("bkt-x", destinationSentinel)); status != http.StatusBadRequest {
		t.Errorf("PUT a bad name = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destinations/offsite", strings.TrimSuffix(destPut("bkt-x", destinationSentinel), "}")+`,"colour":"red"}`); status != http.StatusBadRequest {
		t.Errorf("PUT an unknown field = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destinations/offsite", `{"secretAccessKey": "`+destinationSentinel+`", "endpoint": }`); status != http.StatusBadRequest {
		t.Errorf("PUT unreadable JSON = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destinations/offsite", `{"secretAccessKey": 7, "endpoint": "`+destinationSentinel+`"}`); status != http.StatusBadRequest {
		t.Errorf("PUT the wrong type = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destination", destPut("backups", destinationSentinel)); status != http.StatusOK {
		t.Errorf("PUT the default = %d", status)
	}
	if status := do(http.MethodPut, "/v1/backups/destination", destPut("backups", destinationSentinel)); status != http.StatusOK {
		t.Errorf("PUT the default again = %d", status)
	}

	// A test of a bucket that says no, in words that carry the key, and of one whose job Nomad refuses.
	e.Nomad.FinishOnRegisterPrefix("_backup-check-", false, "check", func(*api.Job) (string, string) {
		return "caravana-check: failed write\n", "ERROR: AccessDenied: the key " + destinationSentinel + " can't write\n"
	})
	if status := do(http.MethodPost, "/v1/backups/destinations/offsite/test", ""); status != http.StatusOK {
		t.Errorf("test of a failing bucket = %d", status)
	}
	e.Nomad.RejectNext(400, "refused with "+destinationSentinel)
	do(http.MethodPost, "/v1/backups/destinations/offsite/test", "")

	// The policy and the app, a refused delete, and every read that could carry it.
	if status, raw := deployShop(t, e, map[string]any{"keep": 2, "destination": "offsite", "copyTo": []string{"main"}}); status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	do(http.MethodDelete, "/v1/backups/destinations/offsite", "")
	do(http.MethodDelete, "/v1/backups/destinations/ghost", "")
	for _, path := range []string{"/v1/backups/destinations", "/v1/backups/destination", "/v1/apps/shop", "/v1/apps/shop/backups",
		"/v1/apps/shop/deployments", "/v1/apps/shop/jobspec", "/v1/attention", "/v1/projects", "/v1/apps/shop/backups/db-1/restore"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/restore") {
			method = http.MethodPost
		}
		do(method, path, "")
	}
	do(http.MethodDelete, "/v1/backups/destinations/main", "")

	for _, out := range seen {
		if strings.Contains(out, destinationSentinel) {
			t.Errorf("the secret key is in an answer: %.300s", out)
		}
	}
	if strings.Contains(e.Logs.String(), destinationSentinel) {
		t.Error("the log holds the secret key")
	}
	for _, id := range e.Nomad.JobIDs("") {
		if strings.Contains(string(mustJSON(t, e.Nomad.Job(id))), destinationSentinel) {
			t.Errorf("job %s holds the secret key", id)
		}
	}
	files, err := filepath.Glob(filepath.Join(e.Dir, "caravana.db*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("store files = %v, %v", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), destinationSentinel) {
			t.Errorf("%s holds the secret key", filepath.Base(f))
		}
	}
	// It is where it belongs: in its own variable, and in the backup job's.
	if got := e.Nomad.Variable("caravana/backup-destinations/offsite")["secretAccessKey"]; got != destinationSentinel {
		t.Errorf("offsite's key = %q, want it in its variable", got)
	}
}
