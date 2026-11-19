package main

// The image-scan gate step (feature 044, specs/044-image-scanning/tasks.md T012): the image
// scanner as an add-on, from install to remove, through the API with the gate's token. It is in
// its own file because gate_test.go is long; runGate lists it after autoscale.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy"
)

const (
	scanApp        = "lab044"  // alpine 3.14.0 serving a page, then 3.14.10
	scanGateApp    = "lab044b" // the app the deploy check refuses
	scanPrivateApp = "lab044p" // an image from the gate's password-protected registry
	scanAdvisory   = "CVE-2022-37434"
	scanOldImage   = "alpine:3.14.0"  // zlib 1.2.11-r3
	scanFixedImage = "alpine:3.14.10" // zlib 1.2.12-r3, which has the fix
	scanVolume     = "caravana-scanner"
)

// scanPrivateImage is where gateScanSkopeoJob puts alpine 3.14.0 in the gate's registry.
const scanPrivateImage = gateRegistryHost + "/scan/alpine:3.14"

// The part of each API answer the step reads.
type scanFindingView struct {
	ID          string    `json:"id"`
	Advisory    string    `json:"advisory"`
	CVE         string    `json:"cve"`
	Severity    string    `json:"severity"`
	Package     string    `json:"package"`
	Version     string    `json:"version"`
	Type        string    `json:"type"`
	FixState    string    `json:"fixState"`
	FixedIn     []string  `json:"fixedIn"`
	Fixable     bool      `json:"fixable"`
	URL         string    `json:"url"`
	Remediation string    `json:"remediation"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
}

type scanImageView struct {
	Image     string     `json:"image"`
	Status    string     `json:"status"`
	Reason    string     `json:"reason"`
	ScannedAt *time.Time `json:"scannedAt"`
	DBBuilt   *time.Time `json:"dbBuilt"`
	Scanner   string     `json:"scanner"`
	SBOMKept  bool       `json:"sbomKept"`
	Counts    struct {
		Critical        int `json:"critical"`
		High            int `json:"high"`
		FixableCritical int `json:"fixableCritical"`
	} `json:"counts"`
	Findings []scanFindingView `json:"findings"`
}

type scanServiceView struct {
	Service string         `json:"service"`
	Image   string         `json:"image"`
	Servers []string       `json:"servers"`
	Scan    *scanImageView `json:"scan"`
	Run     *scanRunView   `json:"run"`
}

type scanAppView struct {
	App       string            `json:"app"`
	Installed bool              `json:"installed"`
	Services  []scanServiceView `json:"services"`
}

type scanRunView struct {
	ID      string `json:"id"`
	Image   string `json:"image"`
	Trigger string `json:"trigger"`
	Source  string `json:"source"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Scanner string `json:"scanner"`
}

type scanAddonView struct {
	Name          string     `json:"name"`
	State         string     `json:"state"`
	Version       string     `json:"version"`
	Reason        string     `json:"reason"`
	Server        string     `json:"server"`
	DatabaseBuilt *time.Time `json:"databaseBuilt"`
}

// scanMessage is the part of a notification the step reads.
type scanMessage struct {
	Type           string   `json:"type"`
	Kind           string   `json:"kind"`
	App            string   `json:"app"`
	Service        string   `json:"service"`
	Title          string   `json:"title"`
	Package        string   `json:"package"`
	PackageVersion string   `json:"packageVersion"`
	FixedIn        []string `json:"fixedIn"`
	Advisory       string   `json:"advisory"`
	Severity       string   `json:"severity"`
	Image          string   `json:"image"`
}

func scanMessages(rx *notifyReceiver, match func(scanMessage) bool) []scanMessage {
	var out []scanMessage
	for _, r := range rx.all() {
		var m scanMessage
		if json.Unmarshal(r.Body, &m) == nil && m.Kind == "image_vulnerable" && match(m) {
			out = append(out, m)
		}
	}
	return out
}

// gateScanSkopeoJob copies alpine 3.14.0 into the gate's registry as scan/alpine:3.14, with the
// per-run credentials from its env (not the command line).
func gateScanSkopeoJob(user, pass string) *api.Job {
	job := gateSkopeoJob(user, pass)
	job.ID, job.Name = new("gate-skopeo"), new("gate-skopeo")
	task := job.TaskGroups[0].Tasks[0]
	task.Config["args"] = []string{`set -e; skopeo copy --dest-creds "$U:$P" --dest-tls-verify=false ` +
		`docker://` + scanOldImage + ` docker://` + scanPrivateImage}
	return job
}

// scanWeb is the service of the scan apps: busybox's nc answering every request with 200 on 8080.
// Alpine's busybox has no httpd (that applet is in busybox-extras), and an old alpine is the point.
func scanWeb(image string) map[string]any {
	return map[string]any{
		"name": "web", "image": image, "command": "sh",
		"args":      []string{"-c", `printf 'HTTP/1.0 200 OK\r\nContent-Length: 3\r\n\r\nok\n' > /tmp/r; exec nc -ll -p 8080 -e cat /tmp/r`},
		"ports":     []map[string]any{{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": map[string]int{"memoryMB": 64, "memoryMaxMB": 128},
	}
}

func (g *gate) scanAddon(t *testing.T) scanAddonView {
	t.Helper()
	var list []scanAddonView
	g.getJSON(t, "/v1/addons", &list)
	for _, a := range list {
		if a.Name == "image-scanner" {
			return a
		}
	}
	t.Fatalf("GET /v1/addons has no image-scanner: %+v", list)
	return scanAddonView{}
}

// scanAddonQuiet is scanAddon for a wait loop and a cleanup: a failed read is a zero add-on.
func (g *gate) scanAddonQuiet() scanAddonView {
	resp, err := g.apiRequest(http.MethodGet, "/v1/addons", "", "")
	if err != nil {
		return scanAddonView{}
	}
	defer resp.Body.Close()
	var list []scanAddonView
	_ = json.NewDecoder(resp.Body).Decode(&list)
	for _, a := range list {
		if a.Name == "image-scanner" {
			return a
		}
	}
	return scanAddonView{}
}

func (g *gate) appScan(t *testing.T, id string) scanAppView {
	t.Helper()
	var v scanAppView
	g.getJSON(t, "/v1/apps/"+id+"/scan", &v)
	return v
}

func (g *gate) scanRuns(t *testing.T) []scanRunView {
	t.Helper()
	var v struct {
		Runs []scanRunView `json:"runs"`
	}
	g.getJSON(t, "/v1/scans/runs", &v)
	return v.Runs
}

// scanVolumeExists says whether Nomad has the scanner's host volume.
func (g *gate) scanVolumeExists(t *testing.T) bool {
	t.Helper()
	stubs, _, err := g.target.Nomad.HostVolumes().List(&api.HostVolumeListRequest{}, nil)
	if err != nil {
		t.Fatalf("list the host volumes: %v", err)
	}
	for _, s := range stubs {
		if s.Name == scanVolume {
			return true
		}
	}
	return false
}

// waitScanned waits for a scan with a result of the image app's live version runs. notImage, when
// not empty, is an image the live version must have moved off.
func (g *gate) waitScanned(t *testing.T, id, notImage string, within time.Duration) scanServiceView {
	t.Helper()
	var got scanServiceView
	if !waitUntil(within, func() bool {
		v := g.appScan(t, id)
		if len(v.Services) != 1 || v.Services[0].Image == notImage || v.Services[0].Scan == nil {
			return false
		}
		sc := v.Services[0].Scan
		got = v.Services[0]
		if (sc.Status == "failed" || sc.Status == "skipped") && got.Run == nil {
			t.Fatalf("%s: the scan of %s ended %s: %s", id, got.Image, sc.Status, sc.Reason)
		}
		return sc.Status == "ok" && sc.ScannedAt != nil
	}) {
		t.Fatalf("%s: no finished scan of the live image after %v (last: %+v, scan %+v)", id, within, got, got.Scan)
	}
	return got
}

// deployScanApp deploys an app with alpine image (busybox httpd on 8080) and waits for healthy. It
// returns the deployment.
func (g *gate) deployScanApp(t *testing.T, id, image string) gateDeployment {
	t.Helper()
	web := scanWeb(image)
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(web))
	var d gateDeployment
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, d.ID, 5*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy of %s ended %s: %s", id, final.Status, final.Error)
	}
	return d
}

// imageScanStep (feature 044): the image scanner, from install to remove.
//
//	a. install it (about 2 minutes: the database is 204 MB);
//	b. alpine 3.14.0 is scanned after its deploy and CVE-2022-37434 is found in zlib, with its
//	   fields; the image is listed with its app and server;
//	c. the needs-you item and the webhook's problem.opened name zlib;
//	d. a daily rescan of the unchanged digest finds a dropped finding again (a lab hook deletes the
//	   row), from the SBOM the first scan kept: no pull, no credentials;
//	e. alpine 3.14.10 (zlib fixed) resolves the item, and problem.resolved is sent;
//	f. an image from the gate's password-protected registry is scanned with the credentials in the
//	   scan job's own variable, and the password is in no answer, log, job or data file;
//	g. with the deploy check on, a deploy of alpine 3.14.0 is 422 image_vulnerable;
//	h. removing it leaves nothing: no volume, no scans, no items, no "resolved" for them.
func (g *gate) imageScanStep(t *testing.T) {
	nomad := g.target.Nomad
	sentinel := "s3cret-sentinel-044-" + g.run
	user := "scan" + g.run
	registryPath := "/v1/registries/" + gateRegistryHost
	channelID := ""

	removeAddon := func() {
		waitUntil(10*time.Minute, func() bool {
			switch g.scanAddonQuiet().State {
			case "available":
				return true
			case "installed", "failed":
				if r, err := g.apiRequest(http.MethodDelete, "/v1/addons/image-scanner", "", ""); err == nil {
					r.Body.Close()
				}
			}
			return false
		})
	}
	clean := func() {
		for _, p := range []string{"/v1/apps/" + scanApp, "/v1/apps/" + scanGateApp, "/v1/apps/" + scanPrivateApp, registryPath} {
			if resp, err := g.apiRequest(http.MethodDelete, p, "", ""); err == nil {
				resp.Body.Close()
			}
		}
		if resp, err := g.apiRequest(http.MethodPut, "/v1/scans/settings", "application/json", `{"refuseFixableCritical":false}`); err == nil {
			resp.Body.Close()
		}
		if channelID != "" {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/notifications/channels/"+channelID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
		for _, id := range []string{"gate-skopeo", "gate-registry"} {
			_, _, _ = nomad.Jobs().Deregister(id, true, nil)
		}
		_, _ = nomad.Variables().Delete(deploy.RegistryVariablePath(gateRegistryHost), nil)
		for _, id := range []string{scanApp, scanGateApp, scanPrivateApp} {
			if metas, _, err := nomad.Variables().PrefixList("nomad/jobs/"+id+"/", nil); err == nil {
				for _, m := range metas {
					_, _ = nomad.Variables().Delete(m.Path, nil)
				}
			}
		}
		removeAddon()
	}
	clean() // leftovers of an aborted run, the scanner included
	t.Cleanup(clean)
	t.Cleanup(func() { logTail(t, g.target) })

	// The receiver: every problem the notifier opens or resolves.
	rx := &notifyReceiver{}
	srv := httptest.NewServer(rx)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		if t.Failed() {
			for _, r := range rx.all() {
				t.Logf("the receiver got: %s", r.Body)
			}
		}
	})
	raw := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/notifications/channels",
		`{"kind":"webhook","name":"scan receiver","url":"`+srv.URL+`","events":["problems"]}`)
	var channel struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &channel); err != nil || channel.ID == "" {
		t.Fatalf("create the channel: %s", raw)
	}
	channelID = channel.ID

	// a. Install. Nothing is scanned or offered before it.
	if a := g.scanAddon(t); a.State != "available" {
		t.Fatalf("the image scanner is %q before the step installs it, want available", a.State)
	}
	g.refusal(t, http.StatusConflict, "scanner_not_installed", "", http.MethodPost, "/v1/scans", `{}`)
	installStart := time.Now()
	g.sendWant(t, http.StatusAccepted, http.MethodPut, "/v1/addons/image-scanner", "")
	var addon scanAddonView
	waitFor(t, "the image scanner to be installed", 10*time.Minute, func() bool {
		addon = g.scanAddon(t)
		if addon.State == "failed" {
			t.Fatalf("the install failed: %s", addon.Reason)
		}
		return addon.State == "installed"
	})
	t.Logf("install took %v", time.Since(installStart).Round(time.Second))
	// The database date is known from the first scan (the install's "db update" run doesn't report one).
	if addon.Version == "" || addon.Server == "" {
		t.Errorf("the installed add-on = %+v, want a version and a server", addon)
	}
	if !g.scanVolumeExists(t) {
		t.Errorf("Nomad has no host volume %s after the install", scanVolume)
	}

	// b. A deploy is scanned.
	deployStart := time.Now()
	g.deployScanApp(t, scanApp, scanOldImage)
	firstScan := g.waitScanned(t, scanApp, "", 10*time.Minute)
	t.Logf("deploy to first scan result took %v", time.Since(deployStart).Round(time.Second))
	oldImage := firstScan.Image
	if a := g.scanAddon(t); a.DatabaseBuilt == nil {
		t.Errorf("the add-on after its first scan = %+v, want the database date", a)
	}
	var zlib *scanFindingView
	for i, f := range firstScan.Scan.Findings {
		if f.Advisory == scanAdvisory && f.Package == "zlib" {
			zlib = &firstScan.Scan.Findings[i]
		}
	}
	if zlib == nil {
		t.Fatalf("the scan of %s has no %s in zlib (%d findings)", scanOldImage, scanAdvisory, len(firstScan.Scan.Findings))
	}
	if zlib.Version != "1.2.11-r3" || zlib.Severity != "critical" || !zlib.Fixable || zlib.FixState != "fixed" ||
		!strings.Contains(strings.Join(zlib.FixedIn, ","), "1.2.12-r2") || !strings.HasPrefix(zlib.URL, "https://") ||
		len(zlib.ID) != 16 || zlib.FirstSeen.IsZero() || zlib.LastSeen.IsZero() || zlib.Remediation == "" {
		t.Errorf("the zlib finding = %+v", *zlib)
	}
	if sc := firstScan.Scan; sc.Scanner != "0.117.0" || sc.DBBuilt == nil || !sc.SBOMKept || sc.Counts.FixableCritical == 0 {
		t.Errorf("the scan = %+v, want scanner 0.117.0, a database date, a kept SBOM and a fixable critical", *sc)
	}
	var list []struct {
		Image  string `json:"image"`
		UsedBy []struct {
			App     string   `json:"app"`
			Service string   `json:"service"`
			Servers []string `json:"servers"`
		} `json:"usedBy"`
	}
	g.getJSON(t, "/v1/scans", &list)
	listed := false
	for _, e := range list {
		for _, u := range e.UsedBy {
			if e.Image == oldImage && u.App == scanApp && u.Service == "web" && len(u.Servers) > 0 {
				listed = true
			}
		}
	}
	if !listed {
		t.Errorf("GET /v1/scans doesn't list %s used by %s/web on a server: %+v", oldImage, scanApp, list)
	}

	// c. The item and the notification. The notifier checks every 30 s.
	isZlibOpened := func(m scanMessage) bool {
		return m.Type == "problem.opened" && m.App == scanApp && m.Package == "zlib"
	}
	waitFor(t, "problem.opened for zlib to reach the receiver", 4*time.Minute, func() bool {
		return len(scanMessages(rx, isZlibOpened)) > 0
	})
	if m := scanMessages(rx, isZlibOpened)[0]; m.Service != "web" || m.Severity != "critical" || m.Advisory == "" ||
		m.PackageVersion != "1.2.11-r3" || !strings.Contains(strings.Join(m.FixedIn, ","), "1.2.12-r2") || m.Image != oldImage {
		t.Errorf("problem.opened = %+v, want zlib 1.2.11-r3 critical, fixed in 1.2.12-r2, on %s", m, oldImage)
	}
	zlibItem := func() (detail string, found bool) {
		var att struct {
			Items []struct {
				Kind    string `json:"kind"`
				AppID   string `json:"appId"`
				Service string `json:"service"`
				Detail  string `json:"detail"`
				Level   string `json:"level"`
			} `json:"items"`
		}
		g.getJSON(t, "/v1/attention", &att)
		for _, it := range att.Items {
			if it.Kind == "image_vulnerable" && it.AppID == scanApp && it.Service == "web" && strings.Contains(it.Detail, "zlib") {
				return it.Detail, true
			}
		}
		return "", false
	}
	if detail, ok := zlibItem(); !ok || !strings.Contains(detail, "1.2.12-r2") {
		t.Errorf("/v1/attention has no image_vulnerable item for %s/web naming zlib and 1.2.12-r2 (got %q)", scanApp, detail)
	}

	// d. The daily rescan of an unchanged digest finds a finding again, from the kept SBOM.
	if g.target.RunDailyScans == nil || g.target.DropFinding == nil {
		t.Log("this target has no lab hooks for the daily pass: the rescan check is skipped")
	} else {
		ctx := context.Background()
		// The first scan's own daily run (none) is the baseline: count the runs before.
		runsBefore := len(g.scanRuns(t))
		if n, err := g.target.DropFinding(ctx, oldImage, scanAdvisory); err != nil || n < 1 {
			t.Fatalf("drop %s of %s = %d, %v, want at least 1", scanAdvisory, oldImage, n, err)
		}
		for _, f := range g.appScan(t, scanApp).Services[0].Scan.Findings {
			if f.Advisory == scanAdvisory && f.Package == "zlib" {
				t.Fatalf("%s is still on the app's scan after it was dropped", scanAdvisory)
			}
		}
		rescanStart := time.Now()
		if err := g.target.RunDailyScans(ctx); err != nil {
			t.Fatalf("the daily pass: %v", err)
		}
		var daily scanRunView
		waitFor(t, "the daily rescan of "+scanApp+"'s image to end", 5*time.Minute, func() bool {
			for _, r := range g.scanRuns(t) {
				if r.Trigger == "daily" && r.Image == oldImage && (r.Status == "ok" || r.Status == "failed") {
					daily = r
					return true
				}
			}
			return false
		})
		t.Logf("the daily rescan took %v (%d runs before it)", time.Since(rescanStart).Round(time.Second), runsBefore)
		if daily.Status != "ok" || daily.Source != "sbom" {
			t.Errorf("the daily run = %+v, want ok, read from the SBOM", daily)
		}
		var back *scanFindingView
		after := g.appScan(t, scanApp).Services[0].Scan
		for i, f := range after.Findings {
			if f.Advisory == scanAdvisory && f.Package == "zlib" {
				back = &after.Findings[i]
			}
		}
		if back == nil {
			t.Fatalf("the rescan didn't find %s again", scanAdvisory)
		}
		if back.ID != zlib.ID || !back.FirstSeen.After(zlib.FirstSeen) {
			t.Errorf("the finding came back as %+v, want id %s and a first_seen after %v", *back, zlib.ID, zlib.FirstSeen)
		}
		if len(after.Findings) != len(firstScan.Scan.Findings) {
			t.Errorf("the SBOM rescan has %d findings, the registry scan %d: it lost or gained some", len(after.Findings), len(firstScan.Scan.Findings))
		}
	}

	// e. The fixed image resolves the item.
	fixedStart := time.Now()
	g.deployScanApp(t, scanApp, scanFixedImage)
	fixed := g.waitScanned(t, scanApp, oldImage, 10*time.Minute)
	t.Logf("fixed deploy to its scan result took %v", time.Since(fixedStart).Round(time.Second))
	for _, f := range fixed.Scan.Findings {
		if f.Advisory == scanAdvisory {
			t.Errorf("%s still has %s: %+v", scanFixedImage, scanAdvisory, f)
		}
	}
	for _, f := range fixed.Scan.Findings {
		if f.Package == "zlib" && f.Fixable && (f.Severity == "critical") {
			t.Errorf("%s has a fixable critical in zlib: %+v", scanFixedImage, f)
		}
	}
	waitFor(t, "the zlib item to go from the needs-you list", time.Minute, func() bool {
		_, ok := zlibItem()
		return !ok
	})
	resolved := func(m scanMessage) bool { return m.Type == "problem.resolved" && m.App == scanApp }
	waitFor(t, "problem.resolved for the image to reach the receiver", 4*time.Minute, func() bool {
		return len(scanMessages(rx, resolved)) > 0
	})
	zlibResolved := false
	for _, m := range scanMessages(rx, resolved) {
		zlibResolved = zlibResolved || m.Package == "zlib" || strings.Contains(m.Title, "zlib")
	}
	if !zlibResolved {
		t.Errorf("no problem.resolved names zlib: %+v", scanMessages(rx, resolved))
	}

	// f. A private image, scanned with the credentials in the job's own variable.
	pass := sentinel
	user2, pass2 := "scanb"+g.run, "pw2-"+g.run
	if _, _, err := nomad.Jobs().Register(gateRegistryJob(user, pass, user2, pass2), nil); err != nil {
		t.Fatalf("register the registry job: %v", err)
	}
	waitFor(t, "the registry to answer 401 on "+gateRegistryHost, 3*time.Minute, g.registryAnswers401)
	if _, _, err := nomad.Jobs().Register(gateScanSkopeoJob(user, pass), nil); err != nil {
		t.Fatalf("register the skopeo job: %v", err)
	}
	var copyFailure string
	waitFor(t, "skopeo to copy alpine into the registry", 10*time.Minute, func() bool {
		stubs, _, err := nomad.Jobs().Allocations("gate-skopeo", false, nil)
		if err != nil {
			return false
		}
		for _, s := range stubs {
			switch s.ClientStatus {
			case "complete":
				return true
			case "failed":
				copyFailure = "the allocation failed"
				if state := s.TaskStates["skopeo"]; state != nil {
					for _, ev := range state.Events {
						copyFailure += "; " + ev.DisplayMessage
					}
				}
				return true
			}
		}
		return false
	})
	if copyFailure != "" {
		t.Fatalf("skopeo could not copy the image: %s", copyFailure)
	}
	payload, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	answers := map[string]string{} // every answer read from here on, searched for the password at the end
	answers["PUT registry"] = string(g.sendWant(t, http.StatusOK, http.MethodPut, registryPath, string(payload)))

	// Watch the scan jobs Nomad holds while the private scan runs: they are purged when it ends.
	var (
		mu       sync.Mutex
		jobDumps = map[string]string{}
		sawVar   bool
	)
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			stubs, _, err := nomad.Jobs().List(&api.QueryOptions{Prefix: "_scan-"})
			if err == nil {
				for _, s := range stubs {
					mu.Lock()
					_, seen := jobDumps[s.ID]
					mu.Unlock()
					if !seen {
						if job, _, err := nomad.Jobs().Info(s.ID, nil); err == nil {
							dump, _ := json.Marshal(job)
							mu.Lock()
							jobDumps[s.ID] = string(dump)
							mu.Unlock()
						}
					}
					if v, _, err := nomad.Variables().Peek("nomad/jobs/"+s.ID, nil); err == nil && v != nil && v.Items["GRYPE_REGISTRY_AUTH_PASSWORD"] != "" {
						mu.Lock()
						sawVar = true
						mu.Unlock()
					}
				}
			}
			select {
			case <-stopWatch:
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
	}()
	stopWatcher := func() {
		select {
		case <-stopWatch:
		default:
			close(stopWatch)
		}
		<-watchDone
	}
	t.Cleanup(stopWatcher)

	privateStart := time.Now()
	g.deployScanApp(t, scanPrivateApp, scanPrivateImage)
	priv := g.waitScanned(t, scanPrivateApp, "", 5*time.Minute)
	t.Logf("private deploy to its scan result took %v", time.Since(privateStart).Round(time.Second))
	stopWatcher()
	if !strings.HasPrefix(priv.Image, gateRegistryHost+"/scan/alpine@sha256:") || len(priv.Scan.Findings) == 0 {
		t.Errorf("the private image's scan = %s with %d findings, want an alpine on %s with findings", priv.Image, len(priv.Scan.Findings), gateRegistryHost)
	}
	mu.Lock()
	sawPrivateJob := false
	for id, dump := range jobDumps {
		if strings.Contains(dump, "registry:"+gateRegistryHost+"/scan/alpine@sha256:") {
			sawPrivateJob = true
		}
		answers["scan job "+id] = dump
	}
	if !sawPrivateJob {
		t.Errorf("the step never saw the private image's scan job (saw %d jobs)", len(jobDumps))
	}
	if !sawVar {
		t.Error("the step never saw the scan job's variable holding the credentials while the job ran")
	}
	mu.Unlock()
	waitFor(t, "the scan jobs and their variables to be purged", time.Minute, func() bool {
		jobs, _, _ := nomad.Jobs().List(&api.QueryOptions{Prefix: "_scan-"})
		vars, _, _ := nomad.Variables().PrefixList("nomad/jobs/_scan-", nil)
		return len(jobs) == 0 && len(vars) == 0
	})
	for _, p := range []string{"/v1/registries", "/v1/scans", "/v1/scans/runs", "/v1/scans/settings", "/v1/addons", "/v1/attention",
		"/v1/apps/" + scanPrivateApp, "/v1/apps/" + scanPrivateApp + "/scan", "/v1/apps/" + scanPrivateApp + "/deployments"} {
		answers["GET "+p] = string(g.sendWant(t, http.StatusOK, http.MethodGet, p, ""))
	}
	if g.target.ServerLog != nil {
		answers["the server log"] = g.target.ServerLog()
	}
	if g.target.DataDir != "" {
		files, _ := filepath.Glob(filepath.Join(g.target.DataDir, "*"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil {
				answers["data file "+filepath.Base(f)] = string(b)
			}
		}
	}
	for what, text := range answers {
		if strings.Contains(text, sentinel) {
			t.Errorf("the registry password is in %s", what)
		}
	}

	// g. The deploy check.
	g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/scans/settings", `{"refuseFixableCritical":true}`)
	web := scanWeb(scanOldImage)
	message := g.refusal(t, http.StatusUnprocessableEntity, "image_vulnerable", "services[0].image",
		http.MethodPost, "/v1/apps/"+scanGateApp+"/deployments", labApp(web))
	// The message names the worst fixable critical (any package of alpine 3.14.0), its fix, and how many more.
	if !regexp.MustCompile(`^web's image has CVE-[0-9]+-[0-9]+ \(critical\) in \S+ \S+, fixed in \S+( and [0-9]+ more)?$`).MatchString(message) {
		t.Errorf("the refusal's message = %q, want it to name a critical advisory, its package and its fix", message)
	}
	g.sendWant(t, http.StatusOK, http.MethodPut, "/v1/scans/settings", `{"refuseFixableCritical":false}`)

	// h. Remove it: nothing is left, and the items go without a "resolved".
	var open int
	var att struct {
		Items []struct {
			Kind string `json:"kind"`
		} `json:"items"`
	}
	g.getJSON(t, "/v1/attention", &att)
	for _, it := range att.Items {
		if it.Kind == "image_vulnerable" {
			open++
		}
	}
	resolvedBefore := len(scanMessages(rx, func(m scanMessage) bool { return m.Type == "problem.resolved" }))
	removeStart := time.Now()
	g.sendWant(t, http.StatusAccepted, http.MethodDelete, "/v1/addons/image-scanner", "")
	waitFor(t, "the image scanner to be removed", 10*time.Minute, func() bool {
		a := g.scanAddon(t)
		if a.State == "failed" {
			t.Fatalf("the removal failed: %s", a.Reason)
		}
		return a.State == "available"
	})
	t.Logf("remove took %v", time.Since(removeStart).Round(time.Second))
	waitFor(t, "the scanner's host volume to be gone", 2*time.Minute, func() bool { return !g.scanVolumeExists(t) })
	var left []json.RawMessage
	g.getJSON(t, "/v1/scans", &left)
	if len(left) != 0 {
		t.Errorf("GET /v1/scans after the removal = %s, want []", left)
	}
	if runs := g.scanRuns(t); len(runs) != 0 {
		t.Errorf("GET /v1/scans/runs after the removal has %d runs", len(runs))
	}
	if g.hasAttention(t, "image_vulnerable") {
		t.Error("an image_vulnerable item is still on the needs-you list after the removal")
	}
	if v := g.appScan(t, scanApp); v.Installed || len(v.Services) != 0 {
		t.Errorf("the app scan after the removal = %+v, want not installed", v)
	}
	g.refusal(t, http.StatusConflict, "scanner_not_installed", "", http.MethodPost, "/v1/scans", `{}`)
	if open > 0 {
		// The notifier checks every 30 s; a "resolved" for an item that only went because the add-on
		// went would arrive within that and the resolve delay (10 s in the lab).
		time.Sleep(50 * time.Second)
		if after := len(scanMessages(rx, func(m scanMessage) bool { return m.Type == "problem.resolved" })); after != resolvedBefore {
			t.Errorf("%d problem.resolved arrived for image_vulnerable items after the removal, want none", after-resolvedBefore)
		}
	}
}
