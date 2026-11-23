package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// destPut is the body of PUT /v1/backups/destinations/{name}: bucket is the name of the bucket.
func destPut(bucket, secret string, extra ...string) string {
	pairs := []string{"endpoint", quote("http://10.0.0.5:9000"), "region", quote("us-east-1"), "bucket", quote(bucket),
		"accessKeyId", quote("AK-" + bucket)}
	if secret != "" {
		pairs = append(pairs, "secretAccessKey", quote(secret))
	}
	return jsonBody(append(pairs, extra...)...)
}

// dbFile is a postgres-like service the fake registry resolves; backupOverlay gives it a backup policy.
const dbFile = "services:\n  db:\n    image: traefik/whoami:latest\n    ports: [\"5432\"]\n    environment:\n      POSTGRES_PASSWORD: pw\n"

// deployShop deploys the app "shop" with a postgres backup policy (overlay's fields beside the engine) and
// waits until it is healthy. It returns the status and body of a refused deploy.
func deployShop(t *testing.T, e *testEnv, policy map[string]any) (int, []byte) {
	t.Helper()
	policy["engine"] = "postgres"
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json",
		composeBody(t, dbFile, map[string]any{"db": map[string]any{"backup": policy}}))
	if status == http.StatusAccepted {
		waitHealthy(t, e, "shop", decodeAs[deployment](t, raw).ID)
	}
	return status, raw
}

// putDest makes the destination name and fails the test unless it answers 201.
func putDest(t *testing.T, e *testEnv, name, bucket, secret string, extra ...string) {
	t.Helper()
	status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destinations/"+name, "application/json", destPut(bucket, secret, extra...))
	if status != http.StatusCreated {
		t.Fatalf("PUT destination %s = %d %s", name, status, raw)
	}
}

func TestBackupDestinationsEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	base := e.URL + "/v1/backups/destinations"

	// Nothing yet: an empty list, and the old read says there is none.
	status, _, raw := call(t, http.MethodGet, base, "", "")
	if status != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("GET list on a new installation = %d %s", status, raw)
	}
	if status, _, raw = call(t, http.MethodGet, e.URL+"/v1/backups/destination", "", ""); status != http.StatusNotFound {
		t.Errorf("GET default = %d %s, want 404", status, raw)
	}

	// The first destination is the default whatever it says; Location names it.
	status, header, raw := call(t, http.MethodPut, base+"/main", "application/json", destPut("backups", "SK-main", "default", "false"))
	if status != http.StatusCreated || header.Get("Location") != "/v1/backups/destinations/main" {
		t.Fatalf("PUT main = %d %v %s, want 201 with its Location", status, header, raw)
	}
	checkSchema(t, doc, raw, "BackupDestination")
	if got := decodeAs[backupDestinationJSON](t, raw); got.Name != "main" || !got.Default || got.Bucket != "backups" || got.LastTest != nil {
		t.Errorf("main = %+v", got)
	}
	// A second PUT of the same name changes it: 200, no Location, and the secret is kept.
	status, header, raw = call(t, http.MethodPut, base+"/main", "application/json", destPut("backups2", ""))
	if status != http.StatusOK || header.Get("Location") != "" || decodeAs[backupDestinationJSON](t, raw).Bucket != "backups2" {
		t.Fatalf("PUT main again = %d %v %s, want 200", status, header, raw)
	}
	checkSchema(t, doc, raw, "BackupDestination")
	if got := e.Nomad.Variable("caravana/backup-destinations/main")["secretAccessKey"]; got != "SK-main" {
		t.Errorf("the secret key = %q, want it kept", got)
	}

	putDest(t, e, "offsite", "offsite-bkt", "SK-off")
	if status, _, raw = call(t, http.MethodGet, e.URL+"/v1/backups/destination", "", ""); decodeAs[backupDestinationJSON](t, raw).Name != "main" || status != 200 {
		t.Errorf("GET default = %d %s, want main", status, raw)
	}

	// The refusals name their field.
	for _, tc := range []struct {
		name, path, body, field string
	}{
		{"a bad name", "/Off_Site", destPut("bkt-x", "s"), "name"},
		{"a new destination without a key", "/third", destPut("bkt-x", ""), "secretAccessKey"},
		{"a bad bucket", "/third", destPut("No_Such", "s"), "bucket"},
		{"the default made not the default", "/main", destPut("backups2", "", "default", "false"), "default"},
	} {
		status, _, raw := call(t, http.MethodPut, base+tc.path, "application/json", tc.body)
		if got := decodeAs[errorBody](t, raw).Error; status != http.StatusBadRequest || got.Code != codeInvalidField || got.Field != tc.field {
			t.Errorf("%s: PUT = %d %s, want 400 invalid_field on %q", tc.name, status, raw, tc.field)
		}
		checkSchema(t, doc, raw, "Error")
	}
	if status, _, raw = call(t, http.MethodPut, base+"/third", "application/json", strings.TrimSuffix(destPut("bkt-x", "s"), "}")+`,"colour":"red"}`); status != http.StatusBadRequest {
		t.Errorf("PUT with an unknown field = %d %s, want 400", status, raw)
	}

	// default:true moves the default, and the old routes follow it.
	status, _, raw = call(t, http.MethodPut, base+"/offsite", "application/json", destPut("offsite-bkt", "", "default", "true"))
	if got := decodeAs[backupDestinationJSON](t, raw); status != http.StatusOK || !got.Default {
		t.Fatalf("PUT offsite default = %d %s", status, raw)
	}
	if _, _, raw = call(t, http.MethodGet, e.URL+"/v1/backups/destination", "", ""); decodeAs[backupDestinationJSON](t, raw).Name != "offsite" {
		t.Errorf("GET default = %s, want offsite", raw)
	}
	// PUT /v1/backups/destination writes the default (offsite now) and answers its name.
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destPut("offsite-two", ""))
	if got := decodeAs[backupDestinationJSON](t, raw); status != http.StatusOK || got.Name != "offsite" || got.Bucket != "offsite-two" || !got.Default {
		t.Errorf("PUT default = %d %s, want it to change offsite", status, raw)
	}

	// The list, by name, with what uses each: a policy that copies to main and a project that defaults to offsite... first main.
	status, raw = deployShop(t, e, map[string]any{"keep": 2, "destination": "offsite", "copyTo": []string{"main"}})
	if status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	if status, _, raw = call(t, http.MethodPost, e.URL+"/v1/projects", "application/json", `{"name": "acme"}`); status != http.StatusCreated {
		t.Fatalf("POST project = %d %s", status, raw)
	}
	if status, _, raw = call(t, http.MethodPatch, e.URL+"/v1/projects/acme", "application/json", `{"backupDestination": "offsite"}`); status != http.StatusOK {
		t.Fatalf("PATCH project = %d %s", status, raw)
	}
	status, _, raw = call(t, http.MethodGet, base, "", "")
	var list []backupDestinationJSON
	if err := json.Unmarshal(raw, &list); err != nil || status != http.StatusOK || len(list) != 2 || list[0].Name != "main" || list[1].Name != "offsite" {
		t.Fatalf("GET list = %d %s", status, raw)
	}
	var items []json.RawMessage
	_ = json.Unmarshal(raw, &items)
	for _, item := range items {
		checkSchema(t, doc, item, "BackupDestination")
	}
	if u := list[0].UsedBy; len(u) != 1 || u[0] != (destinationUseJSON{App: "shop", Service: "db", Role: "copy"}) {
		t.Errorf("main is used by %+v, want shop/db as a copy", u)
	}
	if u := list[1].UsedBy; len(u) != 2 || u[0] != (destinationUseJSON{App: "shop", Service: "db", Role: "destination"}) || u[1] != (destinationUseJSON{Project: "acme"}) {
		t.Errorf("offsite is used by %+v, want shop/db and the project acme", u)
	}

	// Removing: unknown, the default, in use, and one nothing uses.
	for _, tc := range []struct {
		name   string
		status int
		code   string
		msg    string
	}{
		{"ghost", http.StatusNotFound, codeDestinationNotFound, ""},
		{"offsite", http.StatusConflict, codeDestinationIsDefault, ""},
	} {
		status, _, raw := call(t, http.MethodDelete, base+"/"+tc.name, "", "")
		if got := decodeAs[errorBody](t, raw).Error; status != tc.status || got.Code != tc.code {
			t.Errorf("DELETE %s = %d %s, want %d %s", tc.name, status, raw, tc.status, tc.code)
		}
		checkSchema(t, doc, raw, "Error")
	}
	putDest(t, e, "cold", "cold-bkt", "SK-cold")
	if status, _, raw = call(t, http.MethodPut, e.URL+"/v1/backups/destinations/offsite", "application/json", destPut("offsite-two", "", "default", "true")); status != http.StatusOK {
		t.Fatal(status, string(raw))
	}
	status, _, raw = call(t, http.MethodDelete, base+"/main", "", "")
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusConflict || got.Code != codeDestinationInUse || !strings.Contains(got.Message, "shop/db (copy)") {
		t.Errorf("DELETE a destination in use = %d %s, want 409 destination_in_use naming shop/db", status, raw)
	}
	if status, _, raw = call(t, http.MethodDelete, base+"/cold", "", ""); status != http.StatusNoContent || len(raw) != 0 {
		t.Errorf("DELETE a free destination = %d %s, want 204", status, raw)
	}
	if e.Nomad.Variable("caravana/backup-destinations/cold") != nil {
		t.Error("the removed destination's secret key is still stored")
	}
}

// A policy's destination, copies and keep reach the stored app, and a name that isn't a destination, or a
// copy where the backups already go, is refused naming its field.
func TestBackupPolicyDestinationFields(t *testing.T) {
	e := newTestEnv(t, "")
	putDest(t, e, "main", "backups", "SK-main")
	putDest(t, e, "offsite", "offsite-bkt", "SK-off")

	for _, tc := range []struct {
		name   string
		policy map[string]any
		field  string
	}{
		{"an unknown destination", map[string]any{"destination": "ghost"}, "services.db.backup.destination"},
		{"an unknown copy", map[string]any{"copyTo": []string{"ghost"}}, "services.db.backup.copyTo[0]"},
		{"a copy where the backups go", map[string]any{"copyTo": []string{"main"}}, "services.db.backup.copyTo[0]"},
		{"copyKeep without copies", map[string]any{"copyKeep": 3}, "services.db.backup.copyKeep"},
		{"three copies", map[string]any{"copyTo": []string{"a", "b", "c"}}, "services.db.backup.copyTo"},
	} {
		status, raw := deployShop(t, e, tc.policy)
		if got := decodeAs[errorBody](t, raw).Error; status != http.StatusBadRequest || got.Field != tc.field {
			t.Errorf("%s: deploy = %d %s, want 400 on %q", tc.name, status, raw, tc.field)
		}
	}
	if status, raw := deployShop(t, e, map[string]any{"keep": 2, "copyTo": []string{"offsite"}, "copyKeep": 3}); status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop", "", "")
	checkSchema(t, loadContract(t), raw, "AppRecord")
	var rec struct {
		App struct {
			Services []struct {
				Backup struct {
					CopyTo   []string `json:"copyTo"`
					CopyKeep int      `json:"copyKeep"`
				} `json:"backup"`
			} `json:"services"`
		} `json:"app"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || len(rec.App.Services) != 1 ||
		len(rec.App.Services[0].Backup.CopyTo) != 1 || rec.App.Services[0].Backup.CopyTo[0] != "offsite" || rec.App.Services[0].Backup.CopyKeep != 3 {
		t.Errorf("stored app = %s (%v)", raw, err)
	}
}

func TestTestBackupDestinationEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	putDest(t, e, "main", "backups", "SK-main")
	var verdict atomic.Value
	verdict.Store("caravana-check: ok\n")
	var fails atomic.Bool
	e.Nomad.FinishOnRegisterPrefix("_backup-check-", true, "", func(*api.Job) (string, string) {
		if fails.Load() {
			return "caravana-check: failed write\n", "ERROR: AccessDenied: not allowed\n"
		}
		return verdict.Load().(string), ""
	})

	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/backups/destinations/main/test", "", "")
	got := decodeAs[destinationTestJSON](t, raw)
	if status != http.StatusOK || !got.OK || got.Message != "listed, wrote and removed a test object" || got.TestedAt.IsZero() {
		t.Fatalf("test = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "BackupDestinationTest")
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/backups/destinations", "", "")
	var list []backupDestinationJSON
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 || list[0].LastTest == nil || !list[0].LastTest.OK {
		t.Errorf("list after the test = %s", raw)
	}

	// A bucket that said no is an outcome, 200 with ok false.
	fails.Store(true)
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/backups/destinations/main/test", "", "")
	got = decodeAs[destinationTestJSON](t, raw)
	if status != http.StatusOK || got.OK || !strings.HasPrefix(got.Message, "could not write: ") || !strings.Contains(got.Message, "AccessDenied") {
		t.Errorf("failed test = %d %s, want 200 with ok false", status, raw)
	}
	checkSchema(t, doc, raw, "BackupDestinationTest")

	// An unknown name is 404, and a Nomad that won't run the check is a 502.
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/backups/destinations/ghost/test", "", "")
	if decodeAs[errorBody](t, raw).Error.Code != codeDestinationNotFound || status != http.StatusNotFound {
		t.Errorf("test of an unknown destination = %d %s", status, raw)
	}
	e.Nomad.RejectNext(400, "no")
	if status, _, raw = call(t, http.MethodPost, e.URL+"/v1/backups/destinations/main/test", "", ""); status != http.StatusBadGateway {
		t.Errorf("test with a Nomad that refuses = %d %s, want 502", status, raw)
	}
}

// seedBackup records a succeeded backup of shop/db in main, with a succeeded copy to offsite when
// copyObject isn't empty. Both destinations must exist: main's bucket is "backups", offsite's "offsite-bkt".
func seedShopBackup(t *testing.T, e *testEnv, n int, copyObject string) store.BackupRecord {
	t.Helper()
	stamp := 1700000000 + n
	done := time.Date(2026, 11, 23, 3, 0, n, 0, time.UTC)
	rec := store.BackupRecord{AppID: "shop", ID: "db-" + itoa(stamp), Service: "db", Engine: "postgres", Status: store.BackupSucceeded,
		Object: "backups/shop/db/" + itoa(stamp) + ".dump", Bytes: 100, StartedAt: done, FinishedAt: &done, Destination: "main"}
	ctx := t.Context()
	if err := e.Store.SaveBackup(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if copyObject != "" {
		if err := e.Store.QueueBackupCopies(ctx, "shop", rec.ID, []string{"offsite"}); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.SaveBackupCopy(ctx, store.BackupCopy{AppID: "shop", BackupID: rec.ID, Destination: "offsite",
			Status: store.CopySucceeded, Object: copyObject, Bytes: 100, StartedAt: &done, FinishedAt: &done}); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

func TestBackupsListTheirPlaces(t *testing.T) {
	e := newTestEnv(t, "")
	putDest(t, e, "main", "backups", "SK-main")
	putDest(t, e, "offsite", "offsite-bkt", "SK-off")
	if status, raw := deployShop(t, e, map[string]any{"keep": 2, "copyTo": []string{"offsite"}}); status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	b := seedShopBackup(t, e, 1, "offsite-bkt/shop/db/1700000001.dump")
	// A backup main lost, kept by its copy; and one nobody has any more.
	lost := seedShopBackup(t, e, 2, "offsite-bkt/shop/db/1700000002.dump")
	if err := e.Store.ExpireBackups(t.Context(), "shop", []string{lost.ID}); err != nil {
		t.Fatal(err)
	}
	gone := seedShopBackup(t, e, 3, "offsite-bkt/shop/db/1700000003.dump")
	if err := e.Store.ExpireBackups(t.Context(), "shop", []string{gone.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.ExpireBackupCopies(t.Context(), "shop", "offsite", []string{gone.ID}); err != nil {
		t.Fatal(err)
	}

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop/backups", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET backups = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "BackupList")
	var got struct {
		Backups []backupJSON `json:"backups"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	byID := map[string]backupJSON{}
	for _, x := range got.Backups {
		byID[x.ID] = x
	}
	if len(got.Backups) != 2 || byID[gone.ID].ID != "" {
		t.Fatalf("backups = %s, want the one nobody has any more left out", raw)
	}
	if x := byID[b.ID]; x.Destination != "main" || x.Expired || len(x.Copies) != 1 || x.Copies[0].Destination != "offsite" ||
		x.Copies[0].Status != "succeeded" || x.Copies[0].Object != "offsite-bkt/shop/db/1700000001.dump" || x.Copies[0].Expired || x.Copies[0].FinishedAt == nil {
		t.Errorf("backup = %+v", x)
	}
	if x := byID[lost.ID]; !x.Expired || len(x.Copies) != 1 || x.Copies[0].Expired {
		t.Errorf("a backup main lost = %+v, want expired with a live copy", x)
	}
}

func TestRestoreFromAPlaceEndpoint(t *testing.T) {
	// newRestoreEnv has main and offsite, the shop app and a backup in both.
	newRestoreEnv := func(t *testing.T) (*testEnv, store.BackupRecord) {
		t.Helper()
		e := newTestEnv(t, "")
		putDest(t, e, "main", "backups", "SK-main")
		putDest(t, e, "offsite", "offsite-bkt", "SK-off")
		if status, raw := deployShop(t, e, map[string]any{"keep": 2, "copyTo": []string{"offsite"}}); status != http.StatusAccepted {
			t.Fatalf("deploy = %d %s", status, raw)
		}
		return e, seedShopBackup(t, e, 1, "offsite-bkt/shop/db/1700000001.dump")
	}
	restore := func(e *testEnv, id, body string) (int, []byte) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/backups/"+id+"/restore", "application/json", body)
		return status, raw
	}
	doc := loadContract(t)

	t.Run("no body reads the backup's own destination", func(t *testing.T) {
		e, b := newRestoreEnv(t)
		e.Nomad.FinishOnRegisterPrefix("_backup-check-", true, "", func(*api.Job) (string, string) { return "caravana-check: found 1\n", "" })
		status, raw := restore(e, b.ID, "")
		if status != http.StatusAccepted || decodeAs[restoreJSON](t, raw).Destination != "main" {
			t.Fatalf("restore = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Restore")
	})
	t.Run("from names a copy", func(t *testing.T) {
		e, b := newRestoreEnv(t)
		status, raw := restore(e, b.ID, `{"from": "offsite"}`)
		if got := decodeAs[restoreJSON](t, raw); status != http.StatusAccepted || got.Destination != "offsite" {
			t.Fatalf("restore = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Restore")
		if job := e.Nomad.Job("shop_db_restore"); job == nil || job.Meta["backup_key"] != "offsite-bkt/shop/db/1700000001.dump" {
			t.Errorf("restore job = %+v, want it to read the copy", job)
		}
	})
	t.Run("a place the backup isn't in is 400 on from", func(t *testing.T) {
		e, b := newRestoreEnv(t)
		for _, body := range []string{`{"from": "cold"}`, `{"from": "Not A Name"}`} {
			status, raw := restore(e, b.ID, body)
			if got := decodeAs[errorBody](t, raw).Error; status != http.StatusBadRequest || got.Code != codeInvalidField || got.Field != "from" {
				t.Errorf("restore %s = %d %s, want 400 on from", body, status, raw)
			}
			checkSchema(t, doc, raw, "Error")
		}
		if status, raw := restore(e, b.ID, `{"place": "offsite"}`); status != http.StatusBadRequest {
			t.Errorf("restore with an unknown field = %d %s, want 400", status, raw)
		}
	})
	t.Run("in none of its places is 409 backup_missing", func(t *testing.T) {
		e, b := newRestoreEnv(t)
		e.Nomad.FinishOnRegisterPrefix("_backup-check-", true, "", func(*api.Job) (string, string) {
			return "caravana-check: missing 1\ncaravana-check: missing 2\n", ""
		})
		status, raw := restore(e, b.ID, "")
		got := decodeAs[errorBody](t, raw).Error
		if status != http.StatusConflict || got.Code != codeBackupMissing || !strings.Contains(got.Message, "main or offsite") {
			t.Errorf("restore = %d %s, want 409 backup_missing naming both places", status, raw)
		}
		checkSchema(t, doc, raw, "Error")
	})
	t.Run("a place that couldn't be reached is 502", func(t *testing.T) {
		e, b := newRestoreEnv(t)
		e.Nomad.FinishOnRegisterPrefix("_backup-check-", true, "", func(*api.Job) (string, string) {
			return "caravana-check: unreachable 1\ncaravana-check: missing 2\n", ""
		})
		status, raw := restore(e, b.ID, "")
		if got := decodeAs[errorBody](t, raw).Error; status != http.StatusBadGateway || got.Code != codeNomadUnavailable || !strings.Contains(got.Message, "couldn't be reached") {
			t.Errorf("restore = %d %s, want 502", status, raw)
		}
	})
}

func TestProjectBackupDestinationEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	putDest(t, e, "main", "backups", "SK-main")
	putDest(t, e, "client-a", "client-bkt", "SK-a")
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects", "application/json", `{"name": "acme", "displayName": "Acme"}`); status != http.StatusCreated {
		t.Fatalf("POST project = %d %s", status, raw)
	}
	patch := func(body string) (int, []byte) {
		status, _, raw := call(t, http.MethodPatch, e.URL+"/v1/projects/acme", "application/json", body)
		return status, raw
	}
	get := func() projectJSON {
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/projects/acme", "", "")
		checkSchema(t, doc, raw, "Project")
		return decodeAs[projectJSON](t, raw)
	}
	if got := get(); got.BackupDestination != "" {
		t.Fatalf("a new project has %q", got.BackupDestination)
	}

	status, raw := patch(`{"backupDestination": "client-a"}`)
	if got := decodeAs[projectJSON](t, raw); status != http.StatusOK || got.BackupDestination != "client-a" || got.DisplayName != "Acme" {
		t.Fatalf("PATCH destination = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Project")
	// Another field leaves it; "" clears it, and then it is not in the answer at all.
	if status, raw = patch(`{"displayName": "Acme Inc"}`); status != http.StatusOK || decodeAs[projectJSON](t, raw).BackupDestination != "client-a" {
		t.Errorf("PATCH displayName = %d %s, want the destination left", status, raw)
	}
	status, raw = patch(`{"backupDestination": ""}`)
	if status != http.StatusOK || strings.Contains(string(raw), "backupDestination") {
		t.Errorf("PATCH \"\" = %d %s, want it cleared and absent", status, raw)
	}
	if got := get(); got.BackupDestination != "" || got.DisplayName != "Acme Inc" {
		t.Errorf("project = %+v", got)
	}

	// A name that isn't a destination is 400 on its field; a bad display name is the place's 422, and
	// either leaves the other change unmade.
	status, raw = patch(`{"backupDestination": "ghost"}`)
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusBadRequest || got.Code != codeInvalidField || got.Field != "backupDestination" {
		t.Errorf("PATCH an unknown destination = %d %s", status, raw)
	}
	status, raw = patch(`{"displayName": "", "backupDestination": "client-a"}`)
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusUnprocessableEntity || got.Field != "displayName" {
		t.Errorf("PATCH a bad display name = %d %s", status, raw)
	}
	if got := get(); got.BackupDestination != "" {
		t.Errorf("a refused PATCH set the destination to %q", got.BackupDestination)
	}
	if status, raw = patch(`{}`); status != http.StatusUnprocessableEntity {
		t.Errorf("PATCH {} = %d %s, want 422", status, raw)
	}
	if status, _, raw = call(t, http.MethodPatch, e.URL+"/v1/projects/ghost", "application/json", `{"backupDestination": "client-a"}`); status != http.StatusNotFound {
		t.Errorf("PATCH an unknown project = %d %s, want 404", status, raw)
	}
}

func TestAttentionHasTheBackupCopyItem(t *testing.T) {
	e := newTestEnv(t, "")
	putDest(t, e, "main", "backups", "SK-main")
	putDest(t, e, "offsite", "offsite-bkt", "SK-off")
	if status, raw := deployShop(t, e, map[string]any{"keep": 2, "copyTo": []string{"offsite"}}); status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	b := seedShopBackup(t, e, 1, "")
	done := b.StartedAt.Add(time.Minute)
	if err := e.Store.QueueBackupCopies(t.Context(), "shop", b.ID, []string{"offsite"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SaveBackupCopy(t.Context(), store.BackupCopy{AppID: "shop", BackupID: b.ID, Destination: "offsite", Status: store.CopyFailed,
		Message: "copy: AccessDenied", FinishedAt: &done}); err != nil {
		t.Fatal(err)
	}
	failed := b
	failed.ID, failed.Status, failed.Message = "db-1700000009", store.BackupFailed, "pg_dump: boom"
	failed.StartedAt = b.StartedAt.Add(time.Hour)
	failed.FinishedAt = &done
	if err := e.Store.SaveBackup(t.Context(), failed); err != nil {
		t.Fatal(err)
	}

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/attention", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET attention = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Attention")
	var got struct {
		Items []attentionItemJSON `json:"items"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	seen := map[string]attentionItemJSON{}
	for _, it := range got.Items {
		seen[it.Kind] = it
	}
	if it := seen["backup_copy_failed"]; it.AppID != "shop" || it.Service != "db" || it.Destination != "offsite" || it.Detail != "copy: AccessDenied" {
		t.Errorf("backup_copy_failed = %+v in %s", it, raw)
	}
	if it := seen["backup_failed"]; it.Destination != "main" || it.Detail != "pg_dump: boom" {
		t.Errorf("backup_failed = %+v in %s", it, raw)
	}
}
