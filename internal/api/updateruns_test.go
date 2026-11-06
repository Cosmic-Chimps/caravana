package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 030: requestUpdate, listUpdateRuns and the additions to getUpdates, putUpdateSettings and
// listAttention, checked against the contract.

// updaterEnv is the API on a service that runs 0.16.0, has seen 0.17.0 and has an updater installed.
type updaterEnv struct {
	*testEnv
	dir     string
	request string
}

func newUpdaterEnv(t *testing.T, version string, installed bool) *updaterEnv {
	t.Helper()
	u := &updaterEnv{dir: t.TempDir()}
	u.request = filepath.Join(t.TempDir(), "update-request")
	u.testEnv = newTestEnvWith(t, "", func(s *service.Service) {
		s.SetMetricsSampleGap(time.Hour)
		s.SetVersion(version)
		s.SetUpdaterDir(u.dir)
		s.SetUpdateRequestPath(u.request)
		s.SetUpdateWatchGap(time.Hour)
		s.SetReleaseCheckGap(24 * time.Hour)
	})
	if installed {
		if err := os.WriteFile(filepath.Join(u.dir, "updater"), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := u.Store.ReleaseCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc.LatestTag, rc.URL = "v0.17.0", "https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.17.0"
	if err := u.Store.SaveReleaseResult(context.Background(), rc, nil); err != nil {
		t.Fatal(err)
	}
	return u
}

func (u *updaterEnv) post(t *testing.T, body string) (int, []byte) {
	t.Helper()
	status, _, raw := call(t, http.MethodPost, u.URL+"/v1/updates/runs", "application/json", body)
	return status, raw
}

func TestRequestUpdateAccepted(t *testing.T) {
	u := newUpdaterEnv(t, "0.16.0", true)
	status, raw := u.post(t, `{"version":"v0.17.0"}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST = %d %s, want 202", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "UpdateRun")
	run := decodeAs[map[string]any](t, raw)
	if run["state"] != "requested" || run["version"] != "0.17.0" || run["fromVersion"] != "0.16.0" || run["trigger"] != "api" ||
		run["tokenName"] != "tests" || run["reason"] != "" || run["log"] != "" || run["startedAt"] != nil || run["finishedAt"] != nil ||
		len(run["id"].(string)) != 32 {
		t.Errorf("run = %v", run)
	}
	if data, err := os.ReadFile(u.request); err != nil || string(data) != "ID="+run["id"].(string)+"\nVERSION=0.17.0\n" {
		t.Errorf("request file = %q, %v", data, err)
	}

	// The run is in the list and in getUpdates.
	list := decodeAs[struct {
		Runs []map[string]any `json:"runs"`
	}](t, mustGet(t, u.testEnv, "/v1/updates/runs"))
	if len(list.Runs) != 1 || list.Runs[0]["id"] != run["id"] {
		t.Errorf("list = %v", list)
	}
	up := getJSON(t, u.testEnv, "/v1/updates", "Updates")
	c := obj(t, up["caravana"], "caravana")
	got := obj(t, c["run"], "run")
	if got["id"] != run["id"] || got["state"] != "requested" {
		t.Errorf("caravana.run = %v", got)
	}
	if upd := obj(t, c["updater"], "updater"); upd["installed"] != true || upd["unavailable"] != "" {
		t.Errorf("caravana.updater = %v", upd)
	}
	if a := obj(t, c["autoUpdate"], "autoUpdate"); a["enabled"] != false || a["hour"] != float64(3) || a["next"] != nil {
		t.Errorf("caravana.autoUpdate = %v", a)
	}

	// A second request while it is open.
	status, raw = u.post(t, `{"version":"0.17.0"}`)
	eb := decodeAs[errorBody](t, raw)
	if status != http.StatusConflict || eb.Error.Code != "update_running" || eb.Error.Message != "An update to 0.17.0 is already under way." {
		t.Errorf("second POST = %d %s, want 409 update_running", status, raw)
	}
}

func TestRequestUpdateWithASession(t *testing.T) {
	u := newUpdaterEnv(t, "0.16.0", true)
	c := sessionCookie(t, u.testEnv)
	status, raw := cookieCall(t, c, http.MethodPost, u.RawURL+"/v1/updates/runs", `{"version":"0.17.0"}`, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if status != http.StatusAccepted {
		t.Fatalf("POST with a session = %d %s", status, raw)
	}
	run := decodeAs[map[string]any](t, raw)
	if run["trigger"] != "console" || run["tokenName"] != "" {
		t.Errorf("run = %v, want trigger console and no token name", run)
	}
	// A cross-site post is refused like any other.
	if status, _ := cookieCall(t, c, http.MethodPost, u.RawURL+"/v1/updates/runs", `{"version":"0.17.0"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}); status != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", status)
	}
}

func mustGet(t *testing.T, e *testEnv, path string) []byte {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, status, raw)
	}
	return raw
}

func TestRequestUpdateRefusals(t *testing.T) {
	tests := []struct {
		name      string
		version   string // the service's version
		installed bool
		body      string
		status    int
		code      string
		field     string
		text      string
	}{
		{"missing", "0.16.0", true, `{}`, 422, "invalid_field", "version", "required"},
		{"empty", "0.16.0", true, `{"version":""}`, 422, "invalid_field", "version", ""},
		{"not a version", "0.16.0", true, `{"version":"latest"}`, 422, "invalid_field", "version", "release version"},
		{"two numbers", "0.16.0", true, `{"version":"0.17"}`, 422, "invalid_field", "version", ""},
		{"a command", "0.16.0", true, `{"version":"0.17.0;id"}`, 422, "invalid_field", "version", ""},
		{"a path", "0.16.0", true, `{"version":"../0.17.0"}`, 422, "invalid_field", "version", ""},
		{"not a string", "0.16.0", true, `{"version":17}`, 400, "invalid_field", "version", ""},
		{"an unknown field", "0.16.0", true, `{"version":"0.17.0","url":"https://evil.example"}`, 400, "unknown_field", "url", ""},
		{"a development build", "dev", true, `{"version":"0.17.0"}`, 409, "update_unavailable", "version", "development build"},
		{"no updater", "0.16.0", false, `{"version":"0.17.0"}`, 409, "update_unavailable", "version", "Run the installer of 0.17.0 once on the server"},
		{"already running here", "0.17.0", true, `{"version":"0.17.0"}`, 409, "update_not_offered", "version", "caravana 0.17.0 already runs here."},
		{"not the newest seen", "0.16.0", true, `{"version":"0.16.1"}`, 409, "update_not_offered", "version", "0.16.1 isn't the newest release caravana has seen (0.17.0). Check for releases first."},
		{"newer than seen", "0.16.0", true, `{"version":"0.18.0"}`, 409, "update_not_offered", "version", "isn't the newest release caravana has seen (0.17.0)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpdaterEnv(t, tc.version, tc.installed)
			status, raw := u.post(t, tc.body)
			eb := decodeAs[errorBody](t, raw)
			if status != tc.status || (tc.code != "" && eb.Error.Code != tc.code) || eb.Error.Field != tc.field ||
				!strings.Contains(eb.Error.Message, tc.text) {
				t.Errorf("POST %s = %d %s, want %d %s (field %q, %q)", tc.body, status, raw, tc.status, tc.code, tc.field, tc.text)
			}
			if _, err := os.Lstat(u.request); err == nil {
				t.Error("a refused request left a file")
			}
			if runs, _ := u.Store.UpdateRuns(context.Background(), 5); len(runs) != 0 {
				t.Errorf("a refused request left rows: %v", runs)
			}
		})
	}
}

func TestRequestUpdateWriteFailureIs500AndRemovesTheRow(t *testing.T) {
	u := newUpdaterEnv(t, "0.16.0", true)
	// The request path's directory is gone: the file can't be written.
	u.Service.SetUpdateRequestPath(filepath.Join(t.TempDir(), "missing", "update-request"))
	status, raw := u.post(t, `{"version":"0.17.0"}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("POST = %d %s, want 500", status, raw)
	}
	if strings.Contains(string(raw), u.dir) || strings.Contains(string(raw), "missing") {
		t.Errorf("the 500 shows a path: %s", raw)
	}
	if runs, _ := u.Store.UpdateRuns(context.Background(), 5); len(runs) != 0 {
		t.Errorf("the row stayed: %v", runs)
	}
}

func TestListUpdateRunsAndTheAdditionsToGetUpdates(t *testing.T) {
	u := newUpdaterEnv(t, "0.16.0", true)
	ctx := context.Background()
	end := time.Now().UTC().Add(-time.Hour)
	for i, st := range []string{store.UpdateUpdated, store.UpdateRolledBack} {
		id := strings.Repeat(string(rune('a'+i)), 32)
		at := end.Add(time.Duration(i) * time.Minute)
		if _, err := u.Store.InsertUpdateRun(ctx, store.UpdateRun{ID: id, Version: "0.16.5", FromVersion: "0.16.0", Trigger: store.UpdateByAutomatic, RequestedAt: at.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := u.Store.SetUpdateRunState(ctx, id, st, at.Add(-50*time.Second), at, map[string]string{"updated": "", "rolled_back": "didn't answer"}[st],
			filepath.Join(u.dir, "logs", id+".log")); err != nil {
			t.Fatal(err)
		}
	}
	raw := mustGet(t, u.testEnv, "/v1/updates/runs")
	list := decodeAs[struct {
		Runs []map[string]any `json:"runs"`
	}](t, raw)
	if len(list.Runs) != 2 || list.Runs[0]["state"] != "rolled_back" || list.Runs[1]["state"] != "updated" {
		t.Fatalf("list = %v, want rolled_back then updated", list)
	}
	for _, r := range list.Runs {
		checkSchema(t, loadContract(t), mustMarshal(t, r), "UpdateRun")
	}
	if list.Runs[0]["trigger"] != "automatic" || list.Runs[0]["reason"] != "didn't answer" || !strings.HasSuffix(list.Runs[0]["log"].(string), ".log") ||
		list.Runs[0]["startedAt"] == nil || list.Runs[0]["finishedAt"] == nil {
		t.Errorf("run = %v", list.Runs[0])
	}

	// getUpdates: the newest finished run within 24 hours, and the history with both rows.
	up := getJSON(t, u.testEnv, "/v1/updates", "Updates")
	c := obj(t, up["caravana"], "caravana")
	if run := obj(t, c["run"], "run"); run["state"] != "rolled_back" {
		t.Errorf("caravana.run = %v", run)
	}
	var kinds []string
	for _, h := range up["history"].([]any) {
		row := h.(map[string]any)
		if row["kind"] == "update" {
			kinds = append(kinds, row["state"].(string)+"|"+row["text"].(string))
		}
	}
	if len(kinds) != 2 || kinds[0] != "rolled_back|caravana 0.16.0 → 0.16.5" || kinds[1] != "updated|caravana 0.16.0 → 0.16.5" {
		t.Errorf("history update rows = %v", kinds)
	}
}

func TestPutUpdateSettingsAutomatic(t *testing.T) {
	u := newUpdaterEnv(t, "0.16.0", true)
	// A patch release is out, so an automatic request is due at the set hour.
	ctx := context.Background()
	rc, _ := u.Store.ReleaseCheck(ctx)
	rc.LatestTag = "v0.16.4"
	if err := u.Store.SaveReleaseResult(ctx, rc, nil); err != nil {
		t.Fatal(err)
	}
	put := func(body string) (int, []byte) {
		status, _, raw := call(t, http.MethodPut, u.URL+"/v1/updates/settings", "application/json", body)
		return status, raw
	}
	status, raw := put(`{"autoUpdate":true,"autoUpdateHour":5}`)
	if status != http.StatusOK {
		t.Fatalf("put = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "UpdatesCaravana")
	a := obj(t, decodeAs[map[string]any](t, raw)["autoUpdate"], "autoUpdate")
	if a["enabled"] != true || a["hour"] != float64(5) || a["next"] == nil {
		t.Errorf("autoUpdate = %v, want on at 5 with a next time", a)
	}
	// A partial body keeps the rest.
	if status, raw = put(`{"autoUpdate":false}`); status != http.StatusOK {
		t.Fatalf("put = %d %s", status, raw)
	}
	a = obj(t, decodeAs[map[string]any](t, raw)["autoUpdate"], "autoUpdate")
	if a["enabled"] != false || a["hour"] != float64(5) || a["next"] != nil {
		t.Errorf("autoUpdate = %v, want off, still at 5, no next", a)
	}
	if c := decodeAs[map[string]any](t, raw); c["checkEnabled"] != true {
		t.Errorf("the check setting changed: %v", c["checkEnabled"])
	}
	// The hour that was refused changed nothing.
	if status, _ = put(`{"autoUpdate":true,"autoUpdateHour":24}`); status != http.StatusUnprocessableEntity {
		t.Errorf("hour 24 = %d, want 422", status)
	}
	if rc, _ := u.Store.ReleaseCheck(ctx); rc.AutoUpdate || rc.AutoUpdateHour != 5 {
		t.Errorf("stored = auto %v at %d after a refused put", rc.AutoUpdate, rc.AutoUpdateHour)
	}
}

func TestAttentionHasTheFailedUpdate(t *testing.T) {
	u := newUpdaterEnv(t, "0.16.0", true)
	ctx := context.Background()
	end := time.Now().UTC().Add(-time.Minute)
	if _, err := u.Store.InsertUpdateRun(ctx, store.UpdateRun{ID: strings.Repeat("c", 32), Version: "0.17.0", FromVersion: "0.16.0",
		Trigger: store.UpdateByConsole, RequestedAt: end.Add(-3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.SetUpdateRunState(ctx, strings.Repeat("c", 32), store.UpdateFailed, end.Add(-time.Minute), end, "The installer stopped with exit code 4.", ""); err != nil {
		t.Fatal(err)
	}
	att := getJSON(t, u.testEnv, "/v1/attention", "Attention")
	var item map[string]any
	for _, it := range att["items"].([]any) {
		if m := it.(map[string]any); m["kind"] == "caravana_update_failed" {
			item = m
		}
	}
	if item == nil {
		t.Fatalf("no caravana_update_failed in %v", att)
	}
	if item["appId"] != nil || item["level"] != "failed" || item["detail"] != "The update to caravana 0.17.0 failed: The installer stopped with exit code 4." {
		t.Errorf("item = %v", item)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
