package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// Image scans (feature 044). What the answers hold is what the scanner's
// database says about an image (advisory IDs, package names, versions, fix
// versions, the advisory's https link) and Caravana's own sentences. The scanner's
// output text, a registry credential and anything an app wrote are not in any
// type below (constitution VII).

// scanRunsListed is how many runs listScanRuns answers.
const scanRunsListed = 50

// scanCountsJSON is an image's findings by severity (schema ScanCounts).
type scanCountsJSON struct {
	Critical        int `json:"critical"`
	High            int `json:"high"`
	Medium          int `json:"medium"`
	Low             int `json:"low"`
	Info            int `json:"info"`
	FixableCritical int `json:"fixableCritical"`
	FixableHigh     int `json:"fixableHigh"`
	KEV             int `json:"kev"`
}

func toScanCountsJSON(c vulns.Counts) scanCountsJSON {
	return scanCountsJSON{Critical: c.Critical, High: c.High, Medium: c.Medium, Low: c.Low, Info: c.Info,
		FixableCritical: c.FixableCritical, FixableHigh: c.FixableHigh, KEV: c.KEV}
}

// scanEPSSJSON is the advisory's exploit-prediction score (schema ScanEPSS).
type scanEPSSJSON struct {
	Score      float64 `json:"score"`
	Percentile float64 `json:"percentile"`
}

// scanFindingJSON is one advisory that hits one package of an image (schema ScanFinding).
type scanFindingJSON struct {
	ID          string        `json:"id"`
	Advisory    string        `json:"advisory"`
	CVE         string        `json:"cve"`
	Severity    string        `json:"severity"`
	Package     string        `json:"package"`
	Version     string        `json:"version"`
	Type        string        `json:"type"`
	FixState    string        `json:"fixState"`
	FixedIn     []string      `json:"fixedIn"`
	Fixable     bool          `json:"fixable"`
	URL         string        `json:"url"`
	EPSS        *scanEPSSJSON `json:"epss"`
	KEV         bool          `json:"kev"`
	Remediation string        `json:"remediation"`
	FirstSeen   time.Time     `json:"firstSeen"`
	LastSeen    time.Time     `json:"lastSeen"`
}

func toScanFindingJSON(f service.ScanFinding) scanFindingJSON {
	out := scanFindingJSON{
		ID: f.ID, Advisory: f.Advisory, CVE: f.CVE, Severity: f.Severity.String(), Package: f.Package,
		Version: f.Version, Type: f.Type, FixState: f.FixState, FixedIn: f.FixedIn, Fixable: f.Fixable,
		URL: f.URL, KEV: f.KEV, Remediation: f.Remediation,
		FirstSeen: f.FirstSeen.UTC(), LastSeen: f.LastSeen.UTC(),
	}
	if out.FixedIn == nil {
		out.FixedIn = []string{}
	}
	if f.EPSS != nil {
		out.EPSS = &scanEPSSJSON{Score: f.EPSS.Score, Percentile: f.EPSS.Percentile}
	}
	return out
}

// imageScanJSON is an image's stored scan (schema ImageScan). ScannedAt, DBBuilt
// and the findings are empty until a scan of the image succeeded; the status and
// reason are the newest try's.
type imageScanJSON struct {
	Image     string            `json:"image"`
	Status    string            `json:"status"`
	Reason    string            `json:"reason"`
	TriedAt   time.Time         `json:"triedAt"`
	ScannedAt *time.Time        `json:"scannedAt"`
	DBBuilt   *time.Time        `json:"dbBuilt"`
	DBSchema  string            `json:"dbSchema"`
	Scanner   string            `json:"scanner"`
	Cut       bool              `json:"cut"`
	SBOMKept  bool              `json:"sbomKept"`
	Counts    scanCountsJSON    `json:"counts"`
	Findings  []scanFindingJSON `json:"findings"`
}

func toImageScanJSON(v service.ImageScanView) imageScanJSON {
	out := imageScanJSON{
		Image: v.Image, Status: v.Status, Reason: v.Reason, TriedAt: v.TriedAt.UTC(),
		ScannedAt: timePtr(v.ScannedAt), DBBuilt: timePtr(v.DBBuilt), DBSchema: v.DBSchema, Scanner: v.Scanner,
		Cut: v.Cut, SBOMKept: v.SBOMKept, Counts: toScanCountsJSON(v.Counts),
		Findings: make([]scanFindingJSON, 0, len(v.Findings)),
	}
	for _, f := range v.Findings {
		out.Findings = append(out.Findings, toScanFindingJSON(f))
	}
	return out
}

// scanRunJSON is one scan: a row of the queue and of the history (schema ScanRun).
// Image is empty for the install's database run.
type scanRunJSON struct {
	ID        string     `json:"id"`
	Image     string     `json:"image"`
	Trigger   string     `json:"trigger"`
	Source    string     `json:"source"`
	Status    string     `json:"status"`
	Reason    string     `json:"reason"`
	Scanner   string     `json:"scanner"`
	DBBuilt   *time.Time `json:"dbBuilt"`
	CreatedAt time.Time  `json:"createdAt"`
	StartedAt *time.Time `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt"`
}

func toScanRunJSON(r store.ScanRun) scanRunJSON {
	return scanRunJSON{ID: r.ID, Image: r.Image, Trigger: r.Trigger, Source: r.Source, Status: r.Status,
		Reason: r.Reason, Scanner: r.Scanner, DBBuilt: timePtr(r.DBBuilt), CreatedAt: r.CreatedAt.UTC(),
		StartedAt: timePtr(r.StartedAt), EndedAt: timePtr(r.EndedAt)}
}

// scanRunPtr is the run queued or running for an image, null when none is.
func scanRunPtr(r *store.ScanRun) *scanRunJSON {
	if r == nil {
		return nil
	}
	out := toScanRunJSON(*r)
	return &out
}

// serviceScanJSON is one service's image and what is known of its scan (schema
// ServiceScan). Servers are the servers running a copy now. Scan is null before the
// image's first scan, and Run is the scan queued or running for the image.
type serviceScanJSON struct {
	Service string         `json:"service"`
	Image   string         `json:"image"`
	Servers []string       `json:"servers"`
	Scan    *imageScanJSON `json:"scan"`
	Run     *scanRunJSON   `json:"run"`
}

// appScanJSON is an app's scans (schema AppScan).
type appScanJSON struct {
	App       string            `json:"app"`
	Installed bool              `json:"installed"`
	Services  []serviceScanJSON `json:"services"`
}

func toAppScanJSON(v service.AppScanView) appScanJSON {
	out := appScanJSON{App: v.App, Installed: v.Installed, Services: make([]serviceScanJSON, 0, len(v.Services))}
	for _, sv := range v.Services {
		e := serviceScanJSON{Service: sv.Service, Image: sv.Image, Servers: sv.Servers, Run: scanRunPtr(sv.Open)}
		if e.Servers == nil {
			e.Servers = []string{}
		}
		if sv.Scan != nil {
			scan := toImageScanJSON(*sv.Scan)
			e.Scan = &scan
		}
		out.Services = append(out.Services, e)
	}
	return out
}

// getAppScan answers GET /v1/apps/{id}/scan: the scan of each image the app's live
// deployment (its newest healthy one) runs, with the findings and the servers that
// run it. While the image scanner isn't installed it answers 200 with
// `installed: false` and no services; an app with no healthy deployment has no
// services. 404 for an app that doesn't exist.
func (s *server) getAppScan(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.AppScan(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAppScanJSON(v))
	return nil
}

// getDeploymentScan answers GET /v1/apps/{id}/deployments/{deploymentId}/scan: the
// same for the images one deployment ran. The servers are given only for the live
// deployment. 404 for an app or a deployment that doesn't exist.
func (s *server) getDeploymentScan(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.DeploymentScan(r.Context(), r.PathValue("id"), r.PathValue("deploymentId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAppScanJSON(v))
	return nil
}

// scanUseJSON is one service that runs an image (schema ScanUse).
type scanUseJSON struct {
	App     string   `json:"app"`
	Service string   `json:"service"`
	Servers []string `json:"servers"`
}

// imageScanSummaryJSON is an image the installation runs, with who runs it and the
// counts of its scan, without the findings (schema ImageScanSummary). An image that
// was never tried has status `none` and null times.
type imageScanSummaryJSON struct {
	Image     string         `json:"image"`
	UsedBy    []scanUseJSON  `json:"usedBy"`
	Counts    scanCountsJSON `json:"counts"`
	ScannedAt *time.Time     `json:"scannedAt"`
	DBBuilt   *time.Time     `json:"dbBuilt"`
	Scanner   string         `json:"scanner"`
	Status    string         `json:"status"`
	Reason    string         `json:"reason"`
	TriedAt   *time.Time     `json:"triedAt"`
	Run       *scanRunJSON   `json:"run"`
}

// listImageScans answers GET /v1/scans: every image an app's live deployment runs,
// in order of first use. It is empty while the image scanner isn't installed.
func (s *server) listImageScans(w http.ResponseWriter, r *http.Request) error {
	list, err := s.svc.ImageScans(r.Context())
	if err != nil {
		return err
	}
	out := make([]imageScanSummaryJSON, 0, len(list))
	for _, sum := range list {
		e := imageScanSummaryJSON{Image: sum.Image, UsedBy: make([]scanUseJSON, 0, len(sum.UsedBy)),
			Status: "none", Run: scanRunPtr(sum.Open)}
		for _, u := range sum.UsedBy {
			use := scanUseJSON{App: u.App, Service: u.Service, Servers: u.Servers}
			if use.Servers == nil {
				use.Servers = []string{}
			}
			e.UsedBy = append(e.UsedBy, use)
		}
		if sc := sum.Scan; sc != nil {
			e.Counts, e.ScannedAt, e.DBBuilt = toScanCountsJSON(sc.Counts), timePtr(sc.ScannedAt), timePtr(sc.DBBuilt)
			e.Scanner, e.Status, e.Reason, e.TriedAt = sc.Scanner, sc.Status, sc.Reason, timePtr(sc.TriedAt)
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// scanRunsJSON is a list of runs (schema ScanRunList).
type scanRunsJSON struct {
	Runs []scanRunJSON `json:"runs"`
}

func toScanRunsJSON(runs []store.ScanRun) scanRunsJSON {
	out := scanRunsJSON{Runs: make([]scanRunJSON, 0, len(runs))}
	for _, r := range runs {
		out.Runs = append(out.Runs, toScanRunJSON(r))
	}
	return out
}

// requestScanRequest is schema RequestScanRequest. Leaving App out scans every app.
type requestScanRequest struct {
	App string `json:"app"`
}

// requestScan answers POST /v1/scans with 202 and the runs it queued (a run already
// queued or running for an image is returned as it is). The body is optional: with
// `{app}` it scans the images that app runs, without it every app's. It reads the
// registry again; it doesn't reuse a kept SBOM. 409 scanner_not_installed while the
// add-on isn't installed, 409 no_live_deployment for an app with no healthy
// deployment, 404 for an app that doesn't exist.
func (s *server) requestScan(w http.ResponseWriter, r *http.Request) error {
	var in requestScanRequest
	if _, err := decodeOptionalJSON(r, &in); err != nil {
		return err
	}
	runs, err := s.svc.RequestScan(r.Context(), in.App)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toScanRunsJSON(runs))
	return nil
}

// listScanRuns answers GET /v1/scans/runs: the newest scan runs, newest first, at
// most 50.
func (s *server) listScanRuns(w http.ResponseWriter, r *http.Request) error {
	runs, err := s.svc.ScanRuns(r.Context(), scanRunsListed)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toScanRunsJSON(runs))
	return nil
}

// scanSettingsJSON is the owner's settings for scanning (schema ScanSettings).
type scanSettingsJSON struct {
	NotifyLevel           string `json:"notifyLevel"`
	RefuseFixableCritical bool   `json:"refuseFixableCritical"`
	DailyHour             int    `json:"dailyHour"`
	DatabaseURL           string `json:"databaseUrl"`
}

func toScanSettingsJSON(s service.ScanSettings) scanSettingsJSON {
	return scanSettingsJSON{NotifyLevel: s.NotifyLevel, RefuseFixableCritical: s.RefuseFixableCritical,
		DailyHour: s.DailyHour, DatabaseURL: s.DatabaseURL}
}

// getScanSettings answers GET /v1/scans/settings.
func (s *server) getScanSettings(w http.ResponseWriter, r *http.Request) error {
	set, err := s.svc.ScanSettings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toScanSettingsJSON(set))
	return nil
}

// scanSettingsRequest is schema ScanSettingsRequest: every field is optional and a
// missing one keeps its value.
type scanSettingsRequest struct {
	NotifyLevel           *string `json:"notifyLevel"`
	RefuseFixableCritical *bool   `json:"refuseFixableCritical"`
	DailyHour             *int    `json:"dailyHour"`
	DatabaseURL           *string `json:"databaseUrl"`
}

// putScanSettings answers PUT /v1/scans/settings with the settings as saved. A bad
// value is 400 invalid_field naming it (`notifyLevel` is critical or high,
// `dailyHour` 0 to 23, `databaseUrl` https:// or empty); turning the gate on
// (`refuseFixableCritical`) while the image scanner isn't installed is 409
// scanner_not_installed.
func (s *server) putScanSettings(w http.ResponseWriter, r *http.Request) error {
	var in scanSettingsRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	set, err := s.svc.ScanSettings(r.Context())
	if err != nil {
		return err
	}
	if in.NotifyLevel != nil {
		set.NotifyLevel = *in.NotifyLevel
	}
	if in.RefuseFixableCritical != nil {
		set.RefuseFixableCritical = *in.RefuseFixableCritical
	}
	if in.DailyHour != nil {
		set.DailyHour = *in.DailyHour
	}
	if in.DatabaseURL != nil {
		set.DatabaseURL = *in.DatabaseURL
	}
	saved, err := s.svc.SetScanSettings(r.Context(), set)
	if e := scansError(err); e != nil {
		e.Field = "refuseFixableCritical"
		return e
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toScanSettingsJSON(saved))
	return nil
}
