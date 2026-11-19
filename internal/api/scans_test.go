package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// scanSentinel is text no scan answer may contain: a registry password stored for the
// registry the app's image comes from.
const scanSentinel = "s3cr3t-SENTINEL-scan-registry-pw"

// whoamiImage is the digest-pinned image the test registry gives whoami.
const whoamiImage = "traefik/whoami@" + whoamiDigest

// installScannerRecord marks the image scanner installed without running an install
// (the install itself is the service's test): the routes read the record.
func installScannerRecord(t *testing.T, e *testEnv) {
	t.Helper()
	err := e.Store.SaveAddon(context.Background(), store.Addon{Name: store.AddonImageScanner, State: store.AddonInstalled,
		Version: "test", InstalledAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatalf("save the add-on: %v", err)
	}
}

// deployHealthy deploys whoami and waits until it is healthy, so it has a live
// deployment.
func deployHealthy(t *testing.T, e *testEnv) deployment {
	t.Helper()
	d := deployOK(t, e, "whoami", whoamiBody)
	waitUntil(t, func() bool {
		rec, err := e.Store.GetDeployment(context.Background(), d.ID)
		return err == nil && rec.Status == "healthy"
	})
	return d
}

// saveScan stores a successful scan of whoami's image with a fixable critical
// finding (with an EPSS score) and an unfixable low one (with none).
func saveScan(t *testing.T, e *testEnv, at time.Time) {
	t.Helper()
	res := vulns.Result{
		DBBuilt: at.Add(-time.Hour), DBSchema: "6", Scanner: "0.117.0",
		Findings: []vulns.Finding{
			{ID: "aaaaaaaaaaaaaaaa", Advisory: "CVE-2022-37434", CVE: "CVE-2022-37434", Package: "zlib", Version: "1.2.11-r3",
				Type: "apk", FixState: "fixed", FixedIn: []string{"1.2.12-r2"}, URL: "https://nvd.nist.gov/vuln/detail/CVE-2022-37434",
				Severity: vulns.Critical, EPSS: &vulns.EPSS{Score: 0.5, Percentile: 0.9}, KEV: true},
			{ID: "bbbbbbbbbbbbbbbb", Advisory: "CVE-2023-0001", CVE: "CVE-2023-0001", Package: "busybox", Version: "1.36",
				Type: "apk", FixState: "not-fixed", Severity: vulns.Low},
		},
	}
	if err := e.Store.SaveScanResult(context.Background(), whoamiImage, res, true, at); err != nil {
		t.Fatalf("save the scan: %v", err)
	}
}

// firstOf is the first element of a JSON array, to check against its item schema.
func firstOf(t *testing.T, raw []byte) []byte {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		t.Fatalf("want a JSON array with an item, got %s (%v)", raw, err)
	}
	return items[0]
}

func scanErr(t *testing.T, raw []byte) (code, field string) {
	t.Helper()
	got := decodeAs[errorBody](t, raw).Error
	return got.Code, got.Field
}

// While the image scanner isn't installed, the reads answer empty, the actions refuse
// with scanner_not_installed and the gate can't be turned on.
func TestScanRoutesWithoutTheScanner(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	deployHealthy(t, e)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/addons", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/addons = %d %s", status, raw)
	}
	addons := decodeAs[[]addonJSON](t, raw)
	if len(addons) != 1 || addons[0].Name != "image-scanner" || addons[0].State != "available" || addons[0].InstalledAt != nil ||
		addons[0].Cost.DiskBytes <= 0 || addons[0].Cost.DownloadBytesPerDay <= 0 || addons[0].Version == "" {
		t.Errorf("addons = %+v", addons)
	}
	checkSchema(t, doc, firstOf(t, raw), "Addon")

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/scan", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET scan = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "AppScan")
	if v := decodeAs[appScanJSON](t, raw); v.App != "whoami" || v.Installed || len(v.Services) != 0 {
		t.Errorf("app scan = %+v", v)
	}

	if status, _, raw = call(t, http.MethodGet, e.URL+"/v1/scans", "", ""); status != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("GET /v1/scans = %d %s, want 200 []", status, raw)
	}
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/scans/runs", "", "")
	if status != http.StatusOK || strings.TrimSpace(string(raw)) != `{"runs":[]}` {
		t.Errorf("GET /v1/scans/runs = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "ScanRunList")

	for _, body := range []string{"", `{}`, `{"app": "whoami"}`} {
		ct := ""
		if body != "" {
			ct = "application/json"
		}
		status, _, raw = call(t, http.MethodPost, e.URL+"/v1/scans", ct, body)
		if code, _ := scanErr(t, raw); status != http.StatusConflict || code != "scanner_not_installed" {
			t.Errorf("POST /v1/scans %q = %d %s, want 409 scanner_not_installed", body, status, raw)
		}
		checkSchema(t, doc, raw, "Error")
	}

	// Settings: the defaults, a save that leaves the gate off, and the refusals.
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/scans/settings", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "ScanSettings")
	if s := decodeAs[scanSettingsJSON](t, raw); s.NotifyLevel != "critical" || s.RefuseFixableCritical || s.DatabaseURL != "" {
		t.Errorf("default settings = %+v", s)
	}
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/scans/settings", "application/json; charset=UTF-8",
		`{"notifyLevel": "high", "dailyHour": 7, "databaseUrl": "https://mirror.example.com/latest.json"}`)
	if s := decodeAs[scanSettingsJSON](t, raw); status != http.StatusOK || s.NotifyLevel != "high" || s.DailyHour != 7 ||
		s.DatabaseURL != "https://mirror.example.com/latest.json" {
		t.Errorf("PUT settings = %d %s", status, raw)
	}
	// A missing field keeps its value.
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/scans/settings", "application/json", `{"dailyHour": 9}`)
	if s := decodeAs[scanSettingsJSON](t, raw); status != http.StatusOK || s.NotifyLevel != "high" || s.DailyHour != 9 {
		t.Errorf("PUT dailyHour only = %d %s", status, raw)
	}
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/scans/settings", "application/json", `{"refuseFixableCritical": true}`)
	if code, field := scanErr(t, raw); status != http.StatusConflict || code != "scanner_not_installed" || field != "refuseFixableCritical" {
		t.Errorf("PUT gate on = %d %s, want 409 scanner_not_installed on refuseFixableCritical", status, raw)
	}
	checkSchema(t, doc, raw, "Error")
	for _, tc := range []struct{ body, field string }{
		{`{"notifyLevel": "low"}`, "notifyLevel"},
		{`{"dailyHour": 24}`, "dailyHour"},
		{`{"databaseUrl": "http://mirror.example.com/latest.json"}`, "databaseUrl"},
		{`{"databaseUrl": "https://user:pw@mirror.example.com/latest.json"}`, "databaseUrl"},
		{`{"dailyHour": "noon"}`, "dailyHour"},
	} {
		status, _, raw = call(t, http.MethodPut, e.URL+"/v1/scans/settings", "application/json", tc.body)
		if code, field := scanErr(t, raw); status != http.StatusBadRequest || code != "invalid_field" || field != tc.field {
			t.Errorf("PUT %s = %d %s, want 400 invalid_field on %s", tc.body, status, raw, tc.field)
		}
	}
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/scans/settings", "application/json", `{"extra": 1}`)
	if code, _ := scanErr(t, raw); status != http.StatusBadRequest || code != "unknown_field" {
		t.Errorf("PUT an unknown field = %d %s", status, raw)
	}
	if status, _, raw = call(t, http.MethodPut, e.URL+"/v1/scans/settings", "text/plain", `x`); status != http.StatusUnsupportedMediaType {
		t.Errorf("PUT text/plain = %d %s", status, raw)
	}

	// An app or a deployment that doesn't exist is 404.
	for _, path := range []string{"/v1/apps/nope/scan", "/v1/apps/whoami/deployments/nope/scan"} {
		status, _, raw = call(t, http.MethodGet, e.URL+path, "", "")
		if code, _ := scanErr(t, raw); status != http.StatusNotFound || code != "not_found" {
			t.Errorf("GET %s = %d %s, want 404 not_found", path, status, raw)
		}
	}
}

// The add-on routes: the refusals and the 202 of an install and a removal.
func TestAddonRoutes(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		status, _, raw := call(t, method, e.URL+"/v1/addons/nope", "", "")
		if code, _ := scanErr(t, raw); status != http.StatusNotFound || code != "addon_not_found" {
			t.Errorf("%s /v1/addons/nope = %d %s, want 404 addon_not_found", method, status, raw)
		}
		checkSchema(t, doc, raw, "Error")
	}

	// While it installs or is removed, both calls are 409 addon_busy.
	for _, state := range []store.AddonState{store.AddonInstalling, store.AddonRemoving} {
		if err := e.Store.SaveAddon(context.Background(), store.Addon{Name: store.AddonImageScanner, State: state, Version: "test"}); err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			status, _, raw := call(t, method, e.URL+"/v1/addons/image-scanner", "", "")
			if code, _ := scanErr(t, raw); status != http.StatusConflict || code != "addon_busy" {
				t.Errorf("%s while %s = %d %s, want 409 addon_busy", method, state, status, raw)
			}
			checkSchema(t, doc, raw, "Error")
		}
	}
	if err := e.Store.DeleteAddon(context.Background(), store.AddonImageScanner); err != nil {
		t.Fatal(err)
	}

	// A removal of what isn't installed is answered as available.
	status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/addons/image-scanner", "", "")
	if got := decodeAs[addonJSON](t, raw); status != http.StatusAccepted || got.State != "available" {
		t.Errorf("DELETE of an add-on not installed = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "Addon")

	// An install answers 202 with the add-on installing, and it lists as such or later.
	status, _, raw = call(t, http.MethodPut, e.URL+"/v1/addons/image-scanner", "", "")
	if got := decodeAs[addonJSON](t, raw); status != http.StatusAccepted || got.State != "installing" || got.Name != "image-scanner" {
		t.Fatalf("PUT image-scanner = %d %s, want 202 installing", status, raw)
	}
	checkSchema(t, doc, raw, "Addon")
}

// With the scanner installed, the reads give the findings in the contract's shape.
func TestScanReads(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	d := deployHealthy(t, e)
	installScannerRecord(t, e)
	scannedAt := time.Now().UTC().Truncate(time.Second)
	saveScan(t, e, scannedAt)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/scan", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET scan = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "AppScan")
	v := decodeAs[appScanJSON](t, raw)
	if !v.Installed || len(v.Services) != 1 {
		t.Fatalf("app scan = %+v", v)
	}
	sv := v.Services[0]
	if sv.Service != "web" || sv.Image != whoamiImage || sv.Servers == nil || sv.Scan == nil {
		t.Fatalf("service scan = %+v", sv)
	}
	scan := *sv.Scan
	if scan.Status != "ok" || scan.Image != whoamiImage || scan.ScannedAt == nil || !scan.ScannedAt.Equal(scannedAt) ||
		scan.DBBuilt == nil || scan.Scanner != "0.117.0" || !scan.SBOMKept || scan.Cut {
		t.Errorf("scan = %+v", scan)
	}
	if c := scan.Counts; c.Critical != 1 || c.Low != 1 || c.FixableCritical != 1 || c.KEV != 1 {
		t.Errorf("counts = %+v", c)
	}
	if len(scan.Findings) != 2 {
		t.Fatalf("findings = %+v", scan.Findings)
	}
	crit, low := scan.Findings[0], scan.Findings[1]
	if crit.Severity != "critical" || !crit.Fixable || !crit.KEV || crit.EPSS == nil || crit.EPSS.Score != 0.5 || crit.EPSS.Percentile != 0.9 ||
		len(crit.FixedIn) != 1 || crit.FixedIn[0] != "1.2.12-r2" || crit.Remediation == "" || crit.URL == "" || crit.FirstSeen.IsZero() {
		t.Errorf("critical finding = %+v", crit)
	}
	if low.Severity != "low" || low.Fixable || low.EPSS != nil || low.FixedIn == nil || len(low.FixedIn) != 0 {
		t.Errorf("low finding = %+v", low)
	}
	// epss is null, not left out, for a finding with none.
	if !strings.Contains(string(raw), `"epss":null`) {
		t.Errorf("a finding without EPSS should have epss null: %s", raw)
	}

	// The deployment's scan is the same for the live deployment.
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+d.ID+"/scan", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET deployment scan = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "AppScan")
	if got := decodeAs[appScanJSON](t, raw); len(got.Services) != 1 || got.Services[0].Scan == nil || len(got.Services[0].Scan.Findings) != 2 {
		t.Errorf("deployment scan = %+v", got)
	}

	// The images, without findings.
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/scans", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/scans = %d %s", status, raw)
	}
	list := decodeAs[[]imageScanSummaryJSON](t, raw)
	if len(list) != 1 || list[0].Image != whoamiImage || list[0].Status != "ok" || list[0].Counts.Critical != 1 || list[0].ScannedAt == nil ||
		len(list[0].UsedBy) != 1 || list[0].UsedBy[0].App != "whoami" || list[0].UsedBy[0].Service != "web" {
		t.Errorf("image scans = %+v", list)
	}
	checkSchema(t, doc, firstOf(t, raw), "ImageScanSummary")
	if strings.Contains(string(raw), "findings") {
		t.Errorf("the image list has findings: %s", raw)
	}
}

// requestScan queues the app's images, with or without a body.
func TestRequestScan(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	deployHealthy(t, e)
	installScannerRecord(t, e)

	for _, tc := range []struct{ ct, body string }{{"", ""}, {"application/json", `{}`}, {"application/json; charset=UTF-8", `{"app": "whoami"}`}} {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/scans", tc.ct, tc.body)
		if status != http.StatusAccepted {
			t.Fatalf("POST /v1/scans %q = %d %s", tc.body, status, raw)
		}
		checkSchema(t, doc, raw, "ScanRunList")
		runs := decodeAs[scanRunsJSON](t, raw).Runs
		if len(runs) != 1 || runs[0].Image != whoamiImage || runs[0].ID == "" || runs[0].CreatedAt.IsZero() {
			t.Errorf("POST %q runs = %+v", tc.body, runs)
		}
	}
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/scans/runs", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET runs = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "ScanRunList")
	if runs := decodeAs[scanRunsJSON](t, raw).Runs; len(runs) == 0 || runs[0].Trigger == "" {
		t.Errorf("runs = %+v", runs)
	}

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/scans", "application/json", `{"app": "nope"}`)
	if code, _ := scanErr(t, raw); status != http.StatusNotFound || code != "not_found" {
		t.Errorf("POST for an unknown app = %d %s, want 404", status, raw)
	}
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/scans", "application/json", `{"application": "x"}`)
	if code, _ := scanErr(t, raw); status != http.StatusBadRequest || code != "unknown_field" {
		t.Errorf("POST with an unknown field = %d %s", status, raw)
	}
}

// With the gate on, every deploy path that reaches the service answers 422
// image_vulnerable on the service's image field, and leaves a failed record.
func TestGateAnswers422(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	d := deployHealthy(t, e)
	installScannerRecord(t, e)
	saveScan(t, e, time.Now()) // fresh: the gate reads it, it doesn't scan

	status, _, raw := call(t, http.MethodPut, e.URL+"/v1/scans/settings", "application/json", `{"refuseFixableCritical": true}`)
	if status != http.StatusOK || !decodeAs[scanSettingsJSON](t, raw).RefuseFixableCritical {
		t.Fatalf("turn the gate on = %d %s", status, raw)
	}

	for name, req := range map[string]struct{ path, ct, body string }{
		"deployApp with a body":  {"/v1/apps/whoami/deployments", "application/json", whoamiBody},
		"deployApp with no body": {"/v1/apps/whoami/deployments", "", ""},
		"redeployDeployment":     {"/v1/apps/whoami/deployments/" + d.ID + "/redeploy", "", ""},
		"deployCompose": {"/v1/apps/whoami/compose", "application/json", composeBody(t, whoamiCompose, map[string]any{
			"web": map[string]any{"domains": []any{map[string]any{"host": "whoami.localhost"}}, "health": map[string]any{"path": "/"}}})},
	} {
		t.Run(name, func(t *testing.T) {
			status, header, raw := call(t, http.MethodPost, e.URL+req.path, req.ct, req.body)
			code, field := scanErr(t, raw)
			if status != http.StatusUnprocessableEntity || code != "image_vulnerable" || field != "services[0].image" {
				t.Fatalf("POST %s = %d %s, want 422 image_vulnerable on services[0].image", req.path, status, raw)
			}
			msg := decodeAs[errorBody](t, raw).Error.Message
			if !strings.Contains(msg, "web's image has CVE-2022-37434 (critical) in zlib 1.2.11-r3, fixed in 1.2.12-r2") {
				t.Errorf("message = %q", msg)
			}
			checkSchema(t, doc, raw, "Error")
			if loc := header.Get("Location"); !strings.HasPrefix(loc, "/v1/apps/whoami/deployments/") {
				t.Errorf("Location = %q, want the failed deployment record", loc)
			}
		})
	}

	// Turned off, the same deploy goes through.
	if status, _, raw = call(t, http.MethodPut, e.URL+"/v1/scans/settings", "application/json", `{"refuseFixableCritical": false}`); status != http.StatusOK {
		t.Fatalf("turn the gate off = %d %s", status, raw)
	}
	deployOK(t, e, "whoami", whoamiBody)
}

// No scan answer, run or log line holds a registry password stored for the
// image's registry, or anything the scanner would print.
func TestScanAnswersHoldNoSecret(t *testing.T) {
	e := newTestEnv(t, "")
	status, _, raw := call(t, http.MethodPut, e.URL+"/v1/registries/docker.io", "application/json", registryBody("deploy", scanSentinel))
	if status != http.StatusOK {
		t.Fatalf("PUT registry = %d %s", status, raw)
	}
	deployHealthy(t, e)
	installScannerRecord(t, e)
	saveScan(t, e, time.Now())
	call(t, http.MethodPost, e.URL+"/v1/scans", "", "")
	call(t, http.MethodPut, e.URL+"/v1/scans/settings", "application/json", `{"refuseFixableCritical": true}`)
	_, _, gateRaw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", whoamiBody)

	seen := []string{string(gateRaw)}
	for _, path := range []string{"/v1/addons", "/v1/apps/whoami/scan", "/v1/scans", "/v1/scans/runs", "/v1/scans/settings"} {
		_, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		seen = append(seen, string(raw))
	}
	for _, body := range seen {
		if strings.Contains(body, scanSentinel) {
			t.Errorf("an answer holds the registry password: %.200s", body)
		}
	}
	if strings.Contains(e.Logs.String(), scanSentinel) {
		t.Error("the request log holds the registry password")
	}
}
