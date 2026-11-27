package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// Image scanning in the console (feature 044, D13): Operator › Security (/ops/security), the Security
// panel on an app's Deployments tab, and the one-line counts in the history. Everything here reads and
// changes through internal/service. What a page draws is the parsed fields of a finding (advisory ID,
// severity, package, versions, the advisory's https address, EPSS) and Caravana's own sentences: the
// scanner's output text and a registry credential never reach a page (constitution VII).

const (
	// secAllShown is how many findings an "All N findings" table draws; the API has them all.
	secAllShown = 200
	// secFixableShown is how many fixable findings the list above the table draws.
	secFixableShown = 10
	// secRunsShown is how many scan runs the page lists.
	secRunsShown = 20
	// secRefreshSeconds is how often a page without script reloads itself while the add-on installs or is removed.
	secRefreshSeconds = 15
)

// ---- what the pages draw ----------------------------------------------------------------------

// secChip is one count of a scan: "3 critical". Tone is critical (the failed tone), high (the warning
// tone) or plain; the word is always there, so the colour is never the only signal.
type secChip struct {
	N     int
	Label string
	Tone  string
}

// secImage is an image as the pages show it: its repository, then a short digest.
type secImage struct {
	Repo, Short, Full string
}

func secImageOf(ref string) secImage {
	repo, _, _ := strings.Cut(ref, "@")
	return secImage{Repo: repo, Short: shortDigest(ref), Full: ref}
}

// secUse is one service that runs an image, linked to its app: the app by its place label (never its
// ID outside Default › production), and the service only when the app has more than one.
type secUse struct {
	App           sentence
	Service, Href string
}

// secUses names the services that run an image, reading each app once per page.
type secUses struct {
	s    *server
	pl   places
	apps map[string]*store.AppRecord // nil: the app can't be read (deleted meanwhile)
}

func (u secUses) of(ctx context.Context, use service.ScanUse) secUse {
	rec, ok := u.apps[use.App]
	if !ok {
		if r, err := u.s.svc.GetApp(ctx, use.App); err == nil {
			rec = &r
		}
		u.apps[use.App] = rec
	}
	out := secUse{App: sentence{mono(use.App)}, Href: "/apps/" + url.PathEscape(use.App) + "/deployments#security"}
	if rec == nil {
		out.Service = use.Service // nothing says it has one service: name it
		return out
	}
	out.App = placeLabel(use.App, rec.Place, u.pl)
	if len(rec.App.Services) > 1 {
		out.Service = use.Service
	}
	return out
}

// secImageRow is a row of the images table.
type secImageRow struct {
	Image     secImage
	UsedBy    []secUse
	Chips     []secChip
	Scanned   *clockTime
	Database  string
	Status    statusView
	Reason    string
	HasResult bool
	Clean     bool
}

// secRun is a row of the recent runs.
type secRun struct {
	Trigger, Source string
	Image           secImage
	HasImage        bool
	Status          statusView
	Reason          string
	Scanner         string
	Database        string
	Created         clockTime
	Took            string
}

// secFinding is one finding as the Security panel draws it: parsed fields only.
type secFinding struct {
	Advisory    string
	URL         string
	Severity    string
	Tone        string
	Package     string
	Version     string
	Type        string
	FixedIn     string
	Fixable     bool
	KEV         bool
	Remediation string
	EPSS        string
}

// secService is one service of the live version in the Security panel.
type secService struct {
	Name     string
	Image    secImage
	Chips    []secChip
	Scanned  *clockTime
	Database string
	// Status is the scan's state: a scan going or queued, or the newest try's failure.
	Status    *statusView
	Reason    string
	HasResult bool
	Clean     bool
	Cut       bool
	Fixable   []secFinding
	FixableN  int
	All       []secFinding
	AllN      int
}

// secPanel is the Security panel of the Deployments tab.
type secPanel struct {
	ID        string
	Installed bool
	// Waiting says the add-on is being installed or removed, which the panel words differently from "not installed".
	Waiting  bool
	Services []secService
	// Action is the Scan now post; "" when there is nothing to scan.
	Action string
}

// secCost is what the scanner costs, in the words the offer says it.
type secCost struct {
	Disk, Download, Memory string
}

// secAddon is the installed add-on's line.
type secAddon struct {
	Version  string
	Server   string
	Database string
	Since    *clockTime
}

// secData is what one read gives the page and its part.
type secData struct {
	State  string
	Reason string
	Cost   secCost
	Addon  secAddon
	Images []secImageRow
	Runs   []secRun
	// Queued is how many scans are queued or running, for the line that says so.
	Queued int
	// NoImages and NoRuns say what an empty list is.
	NoImages, NoRuns emptyView
}

// secForm is the settings form with what was typed.
type secForm struct {
	Level  formField
	Refuse formField
	Hour   formField
	Hours  []upHour
	URL    formField
	Levels []secLevel
}

type secLevel struct {
	Value, Label string
	Selected     bool
}

// securityPage is the whole page. Refusal is the sentence of an action that was turned down, drawn outside
// the live region so that a refresh never takes it away.
type securityPage struct {
	pageData
	Saved   bool
	Refusal sentence
	Data    secData
	Form    secForm
}

// securityRemovePage is the confirmation page of Remove.
type securityRemovePage struct {
	pageData
	Refusal sentence
	Version string
}

// ---- counts, statuses and words ---------------------------------------------------------------

// secChipsOf are the counts of a scan that are not zero, the worst first.
func secChipsOf(c vulns.Counts) []secChip {
	var out []secChip
	add := func(n int, label, tone string) {
		if n > 0 {
			out = append(out, secChip{N: n, Label: label, Tone: tone})
		}
	}
	add(c.Critical, "critical", "critical")
	add(c.High, "high", "high")
	add(c.Medium, "medium", "plain")
	add(c.Low, "low", "plain")
	add(c.FixableCritical+c.FixableHigh, "fixable", "plain")
	add(c.KEV, "known exploited", "critical")
	return out
}

// secTotal is how many findings a scan has of the five severities.
func secTotal(c vulns.Counts) int { return c.Critical + c.High + c.Medium + c.Low + c.Info }

// secDate is a database's build date: "Nov 19", with the year when it isn't this one.
func secDate(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.UTC()
	if t.Year() != now.UTC().Year() {
		return t.Format("Jan 2, 2006")
	}
	return t.Format("Jan 2")
}

// secSize is a size of the offer in the words it uses: "3.5 GB", "204 MB".
func secSize(b int64) string {
	mb := b >> 20
	if mb >= 1000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(mb)/1000), ".0") + " GB"
	}
	return fmt.Sprintf("%d MB", mb)
}

// secStatusOf says where an image's scan is: a run queued or going first, then the newest try. A scan
// that succeeded is "Scanned"; a failed or skipped try says why in Caravana's words.
func secStatusOf(scan *store.ImageScan, open *store.ScanRun) (st statusView, reason string) {
	switch {
	case open != nil && open.Status == store.ScanRunning:
		return statusView{Kind: "rolling", Word: "Scanning now"}, ""
	case open != nil:
		return statusView{Kind: "idle", Word: "Queued to scan"}, ""
	case scan == nil:
		return statusView{Kind: "idle", Word: "Not scanned yet"}, ""
	case scan.Status == store.ScanFailed:
		return statusView{Kind: "failed", Word: "Scan failed"}, scan.Reason
	case scan.Status == store.ScanSkipped:
		return statusView{Kind: "warning", Word: "Scan skipped"}, scan.Reason
	}
	return statusView{Kind: "healthy", Word: "Scanned"}, ""
}

// secTriggerWords says what started a run.
func secTriggerWords(trigger string) string {
	switch trigger {
	case store.ScanTriggerInstall:
		return "install"
	case store.ScanTriggerDeploy:
		return "after a deploy"
	case store.ScanTriggerDaily:
		return "daily"
	case store.ScanTriggerOwner:
		return "you asked"
	case store.ScanTriggerGate:
		return "deploy check"
	}
	return trigger
}

// secSourceWords says what a run read.
func secSourceWords(source string) string {
	switch source {
	case store.ScanSourceRegistry:
		return "the registry"
	case store.ScanSourceSBOM:
		return "a kept SBOM"
	case store.ScanSourceDatabase:
		return "database download"
	}
	return ""
}

// secRunStatus is a run's state as a mark and a word.
func secRunStatus(status string) statusView {
	switch status {
	case store.ScanOK:
		return statusView{Kind: "healthy", Word: "ok"}
	case store.ScanFailed:
		return statusView{Kind: "failed", Word: "failed"}
	case store.ScanSkipped:
		return statusView{Kind: "warning", Word: "skipped"}
	case store.ScanRunning:
		return statusView{Kind: "rolling", Word: "running"}
	}
	return statusView{Kind: "idle", Word: "queued"}
}

// secFindingOf is one stored finding with its words. Only the parsed fields are read; the advisory's
// address is drawn as a link only when it is https (the store keeps no other).
func secFindingOf(f service.ScanFinding) secFinding {
	out := secFinding{Advisory: f.Advisory, Severity: f.Severity.String(), Tone: "plain", Package: f.Package,
		Version: f.Version, Type: f.Type, Fixable: f.Fixable, KEV: f.KEV, Remediation: f.Remediation,
		FixedIn: strings.Join(f.FixedIn, " or ")}
	switch f.Severity {
	case vulns.Critical:
		out.Tone = "critical"
	case vulns.High:
		out.Tone = "high"
	}
	if strings.HasPrefix(f.URL, "https://") {
		out.URL = f.URL
	}
	if f.EPSS != nil {
		out.EPSS = percentile(f.EPSS.Percentile)
	}
	return out
}

// percentile is an EPSS percentile (0 to 1) in words: "97th percentile".
func percentile(p float64) string {
	n := int(p*100 + 0.5)
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix + " percentile"
}

// ---- Operator › Security ----------------------------------------------------------------------

// showOpsSecurity answers GET /ops/security.
func (s *server) showOpsSecurity(w http.ResponseWriter, r *http.Request, session string) {
	s.drawOpsSecurity(w, r, http.StatusOK, nil, nil)
}

// partOpsSecurity answers GET /parts/ops/security: the main region, and by id the lists under it. The page
// says the state it was drawn for (?state=); when the add-on has moved on, the settings form and the rest of
// the page are out of date, so the part asks the browser to load the whole page again.
func (s *server) partOpsSecurity(w http.ResponseWriter, r *http.Request, session string) {
	d, err := s.loadSecurity(r.Context())
	if err != nil {
		s.log.Error("draw the security page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if was := r.URL.Query().Get("state"); was != "" && was != d.State {
		w.Header().Set("HX-Refresh", "true")
	}
	s.renderPart(w, "opssecurity", "sec-part", d)
}

// drawOpsSecurity draws the page with the given status. refusal is the sentence of an action that was
// turned down; form, when set, is the settings form as it was posted.
func (s *server) drawOpsSecurity(w http.ResponseWriter, r *http.Request, status int, refusal sentence, form *secForm) {
	ctx := r.Context()
	d, err := s.loadSecurity(ctx)
	if err != nil {
		s.serverError(w, r, "draw the security page", err)
		return
	}
	var f secForm
	if form != nil {
		f = *form
	} else if d.State == string(store.AddonInstalled) {
		set, err := s.svc.ScanSettings(ctx)
		if err != nil {
			s.serverError(w, r, "read the scan settings", err)
			return
		}
		f = secFormOf(set)
	}
	page, err := s.shellPage(r, "Security", navOpsSecurity, crumb{Label: "Security"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page.LiveURL = "/live?watch=ops"
	if d.State == string(store.AddonInstalling) || d.State == string(store.AddonRemoving) {
		page.Refresh = secRefreshSeconds
	}
	s.render(w, status, "opssecurity", securityPage{pageData: page, Saved: r.URL.Query().Get("saved") == "1",
		Refusal: refusal, Data: d, Form: f})
}

// loadSecurity reads the add-on and, once it is installed, its images and runs.
func (s *server) loadSecurity(ctx context.Context) (secData, error) {
	addon, err := s.svc.Addon(ctx, store.AddonImageScanner)
	if err != nil {
		return secData{}, err
	}
	now := s.now().UTC()
	d := secData{State: addon.State, Reason: addon.Reason,
		NoImages: emptyView{Title: "No images to scan yet.", Text: "The scanner reads the images of each app's live version. Deploy an app and it appears here."},
		NoRuns:   emptyView{Title: "No scans yet.", Text: "Scans appear here as they are queued and finish."},
		Cost:     secCost{Disk: secSize(addon.DiskBytes), Download: secSize(addon.DownloadBytesPerDay), Memory: strconv.Itoa(nomadjob.ScanMemoryMB) + " MB"},
		Addon:    secAddon{Version: addon.Version, Server: addon.Server, Database: secDate(now, addon.DatabaseBuilt)}}
	if !addon.InstalledAt.IsZero() {
		c := clockOf(now, addon.InstalledAt)
		d.Addon.Since = &c
	}
	if addon.State != string(store.AddonInstalled) {
		return d, nil
	}
	images, err := s.svc.ImageScans(ctx)
	if err != nil {
		return secData{}, err
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return secData{}, err
	}
	use := secUses{s: s, pl: pl, apps: map[string]*store.AppRecord{}}
	for _, sum := range images {
		row := secImageRow{Image: secImageOf(sum.Image)}
		for _, u := range sum.UsedBy {
			row.UsedBy = append(row.UsedBy, use.of(ctx, u))
		}
		row.Status, row.Reason = secStatusOf(sum.Scan, sum.Open)
		if sum.Open != nil {
			d.Queued++
		}
		if sc := sum.Scan; sc != nil && sc.HasResult() {
			row.HasResult = true
			row.Chips = secChipsOf(sc.Counts)
			row.Clean = secTotal(sc.Counts) == 0
			c := clockOf(now, sc.ScannedAt)
			row.Scanned, row.Database = &c, secDate(now, sc.DBBuilt)
		}
		d.Images = append(d.Images, row)
	}
	runs, err := s.svc.ScanRuns(ctx, secRunsShown)
	if err != nil {
		return secData{}, err
	}
	for _, run := range runs {
		r := secRun{Trigger: secTriggerWords(run.Trigger), Source: secSourceWords(run.Source), Status: secRunStatus(run.Status),
			Reason: run.Reason, Scanner: run.Scanner, Database: secDate(now, run.DBBuilt), Created: clockOf(now, run.CreatedAt)}
		if run.Image != "" {
			r.Image, r.HasImage = secImageOf(run.Image), true
		}
		if !run.StartedAt.IsZero() && !run.EndedAt.IsZero() {
			r.Took = "took " + run.EndedAt.Sub(run.StartedAt).Round(time.Second).String()
		}
		d.Runs = append(d.Runs, r)
	}
	return d, nil
}

// secFormOf is the settings form filled from the stored settings.
func secFormOf(set service.ScanSettings) secForm {
	f := secForm{Level: formField{ID: "sec-level", Value: set.NotifyLevel}, Refuse: formField{ID: "sec-refuse"},
		Hour: formField{ID: "sec-hour", Value: strconv.Itoa(set.DailyHour)}, URL: formField{ID: "sec-url", Value: set.DatabaseURL}}
	if set.RefuseFixableCritical {
		f.Refuse.Value = "1"
	}
	f.fill()
	return f
}

// fill lists the options of the two selects for the values the form holds.
func (f *secForm) fill() {
	f.Levels = []secLevel{
		{Value: "critical", Label: "Critical only", Selected: f.Level.Value == "critical"},
		{Value: "high", Label: "High and critical", Selected: f.Level.Value == "high"},
	}
	hour, _ := strconv.Atoi(f.Hour.Value)
	f.Hours = nil
	for h := range 24 {
		f.Hours = append(f.Hours, upHour{Value: h, Label: fmt.Sprintf("%02d:00 UTC", h), Selected: h == hour})
	}
}

// secBadHour is the refusal of an hour the select can't have sent.
const secBadHour = "Choose an hour from 00:00 UTC to 23:00 UTC."

// saveSecuritySettings answers POST /ops/security/settings: the notify level, the gate, the daily hour and
// the database address. A bad value draws the page again at its input with what was typed (422); the gate
// while the scanner isn't installed is 409 with the service's sentence; success is 303 to ?saved=1.
func (s *server) saveSecuritySettings(w http.ResponseWriter, r *http.Request, session string) {
	form := secForm{
		Level:  formField{ID: "sec-level", Value: strings.TrimSpace(r.PostFormValue("notifyLevel"))},
		Refuse: formField{ID: "sec-refuse"},
		Hour:   formField{ID: "sec-hour", Value: strings.TrimSpace(r.PostFormValue("dailyHour"))},
		URL:    formField{ID: "sec-url", Value: strings.TrimSpace(r.PostFormValue("databaseUrl"))},
	}
	if r.PostFormValue("refuseFixableCritical") != "" {
		form.Refuse.Value = "1"
	}
	form.fill()
	hour, err := strconv.Atoi(form.Hour.Value)
	if err != nil {
		form.Hour.Error, form.Hour.Alert = plainS(secBadHour), true
		s.drawOpsSecurity(w, r, http.StatusUnprocessableEntity, nil, &form)
		return
	}
	_, err = s.svc.SetScanSettings(r.Context(), service.ScanSettings{NotifyLevel: form.Level.Value,
		RefuseFixableCritical: form.Refuse.Value != "", DailyHour: hour, DatabaseURL: form.URL.Value})
	var fe *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/ops/security?saved=1#settings", http.StatusSeeOther)
	case errors.As(err, &fe):
		msg := plainS(refusalWords(errors.New(fe.Message)))
		switch fe.Field {
		case "notifyLevel":
			form.Level.Error = msg
		case "dailyHour":
			form.Hour.Error = msg
		default:
			form.URL.Error = msg
			// An address that holds a user name or password isn't drawn back: the form never echoes one.
			if u, perr := url.Parse(form.URL.Value); perr != nil || u.User != nil {
				form.URL.Value = ""
			}
		}
		for _, f := range []*formField{&form.Level, &form.Hour, &form.URL} {
			if len(f.Error) > 0 {
				f.Alert = true
				break
			}
		}
		s.drawOpsSecurity(w, r, http.StatusUnprocessableEntity, nil, &form)
	case errors.Is(err, service.ErrScannerNotInstalled):
		s.drawOpsSecurity(w, r, http.StatusConflict, plainS(refusalWords(err)), nil)
	default:
		s.serverError(w, r, "save the scan settings", err)
	}
}

// installScanner answers POST /ops/security/install: the install starts in the background and the page
// shows it; the live notice moves it on. An add-on that is busy draws the page again with the reason (409).
func (s *server) installScanner(w http.ResponseWriter, r *http.Request, session string) {
	_, err := s.svc.InstallAddon(r.Context(), store.AddonImageScanner)
	switch {
	case err == nil:
		http.Redirect(w, r, "/ops/security", http.StatusSeeOther)
	case errors.Is(err, service.ErrAddonBusy):
		s.drawOpsSecurity(w, r, http.StatusConflict, plainS(refusalWords(err)), nil)
	default:
		s.serverError(w, r, "install the image scanner", err)
	}
}

// showSecurityRemove answers GET /ops/security/remove: what goes with the scanner, and the button.
func (s *server) showSecurityRemove(w http.ResponseWriter, r *http.Request, session string) {
	s.drawSecurityRemove(w, r, nil)
}

// removeScanner answers POST /ops/security/remove: the removal starts in the background (303 to the
// page). A busy add-on draws the confirmation again with the reason (409).
func (s *server) removeScanner(w http.ResponseWriter, r *http.Request, session string) {
	_, err := s.svc.RemoveAddon(r.Context(), store.AddonImageScanner)
	if err == nil {
		http.Redirect(w, r, "/ops/security", http.StatusSeeOther)
		return
	}
	s.drawSecurityRemove(w, r, err)
}

// drawSecurityRemove draws the confirmation. refused is why a post was turned down; with none, the page
// refuses itself when there is nothing to remove or the add-on is busy.
func (s *server) drawSecurityRemove(w http.ResponseWriter, r *http.Request, refused error) {
	addon, err := s.svc.Addon(r.Context(), store.AddonImageScanner)
	if err != nil {
		s.serverError(w, r, "read the image scanner", err)
		return
	}
	page := securityRemovePage{Version: addon.Version}
	status := http.StatusOK
	switch {
	case refused != nil && !errors.Is(refused, service.ErrAddonBusy):
		s.serverError(w, r, "remove the image scanner", refused)
		return
	case refused != nil || addon.State == string(store.AddonInstalling) || addon.State == string(store.AddonRemoving):
		status, page.Refusal = http.StatusConflict, plainS(refusalWords(service.ErrAddonBusy))
	case addon.State == service.AddonStateAvailable:
		status, page.Refusal = http.StatusConflict, plainS("The image scanner isn't installed, so there is nothing to remove.")
	}
	head, err := s.shellPage(r, "Remove the image scanner", navOpsSecurity, crumb{Label: "Security", Href: "/ops/security"}, crumb{Label: "Remove"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page.pageData = head
	s.render(w, status, "opssecurityremove", page)
}

// scanAllNow answers POST /ops/security/scan: every image an app runs is queued again from the registry
// (303 to the page). The scanner not installed is 409.
func (s *server) scanAllNow(w http.ResponseWriter, r *http.Request, session string) {
	_, err := s.svc.RequestScan(r.Context(), "")
	switch {
	case err == nil:
		http.Redirect(w, r, "/ops/security", http.StatusSeeOther)
	case errors.Is(err, service.ErrScannerNotInstalled):
		s.drawOpsSecurity(w, r, http.StatusConflict, plainS(refusalWords(err)), nil)
	default:
		s.serverError(w, r, "queue the scans", err)
	}
}

// ---- an app's Security panel ------------------------------------------------------------------

// loadSecurityPanel reads the scans of the images the app's live version runs.
func (s *server) loadSecurityPanel(ctx context.Context, id string) (secPanel, error) {
	view, err := s.svc.AppScan(ctx, id)
	if err != nil {
		return secPanel{}, err
	}
	panel := secPanel{ID: id, Installed: view.Installed}
	if !view.Installed {
		st, err := s.svc.Addon(ctx, store.AddonImageScanner)
		if err != nil {
			return secPanel{}, err
		}
		panel.Waiting = st.State == string(store.AddonInstalling) || st.State == string(store.AddonRemoving)
		return panel, nil
	}
	now := s.now().UTC()
	for _, sv := range view.Services {
		svc := secService{Name: sv.Service, Image: secImageOf(sv.Image)}
		var scan *store.ImageScan
		if sv.Scan != nil {
			scan = &sv.Scan.ImageScan
		}
		st, reason := secStatusOf(scan, sv.Open)
		// Only the states worth a line are drawn: a scan going or queued, an image never scanned, and a try
		// that failed or was skipped. A scan that succeeded says so with its time.
		if sv.Open != nil || scan == nil || scan.Status != store.ScanOK {
			svc.Status, svc.Reason = &st, reason
		}
		if scan != nil && scan.HasResult() {
			svc.HasResult = true
			svc.Chips = secChipsOf(scan.Counts)
			svc.Clean = secTotal(scan.Counts) == 0
			svc.Cut = scan.Cut
			c := clockOf(now, scan.ScannedAt)
			svc.Scanned, svc.Database = &c, secDate(now, scan.DBBuilt)
			svc.AllN = len(sv.Scan.Findings)
			for _, f := range sv.Scan.Findings {
				row := secFindingOf(f)
				if f.Fixable {
					svc.FixableN++
					if len(svc.Fixable) < secFixableShown {
						svc.Fixable = append(svc.Fixable, row)
					}
				}
				if len(svc.All) < secAllShown {
					svc.All = append(svc.All, row)
				}
			}
		}
		panel.Services = append(panel.Services, svc)
	}
	if len(panel.Services) > 0 && s.capsFor(ctx, id).Admin { // the scanner is the installation's (feature 053)
		panel.Action = "/apps/" + id + "/security/scan"
	}
	return panel, nil
}

// partSecurity answers GET /parts/apps/{id}/security: the panel's region alone. An app that isn't there any
// more says so.
func (s *server) partSecurity(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	panel, err := s.loadSecurityPanel(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		s.log.Error("read the security panel", "app", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "app", "security-region", panel)
}

// scanApp answers POST /apps/{id}/security/scan: the images the app's live version runs are queued again
// (303 to the Deployments tab). The scanner not installed and an app with nothing live are 409, drawn on the
// tab above the panel.
func (s *server) scanApp(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	_, err := s.svc.RequestScan(ctx, id)
	switch {
	case err == nil:
		http.Redirect(w, r, "/apps/"+id+"/deployments#security", http.StatusSeeOther)
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case errors.Is(err, service.ErrScannerNotInstalled), errors.Is(err, service.ErrNoLiveDeployment):
		sum, serr := s.svc.AppSummary(ctx, id)
		if serr != nil {
			s.serverError(w, r, "read app", serr)
			return
		}
		s.deploymentsPageNotice(w, r, http.StatusConflict, sum, plainS(refusalWords(err)))
	default:
		s.serverError(w, r, "queue the scans of an app", err)
	}
}

// ---- the history's counts ---------------------------------------------------------------------

// vulnLine is the one line of a history entry: "1 critical · 9 high", "no critical or high
// vulnerabilities" when only the lesser ones are there, "no known vulnerabilities" for a clean scan, and
// "" when none of the entry's images has a result. It adds the counts of every image the entry ran.
func vulnLine(digests map[string]string, scans map[string]store.ImageScan) string {
	var c vulns.Counts
	found := false
	for _, ref := range digests {
		sc, ok := scans[ref]
		if !ok || !sc.HasResult() {
			continue
		}
		found = true
		c.Critical += sc.Counts.Critical
		c.High += sc.Counts.High
		c.Medium += sc.Counts.Medium
		c.Low += sc.Counts.Low
		c.Info += sc.Counts.Info
	}
	switch {
	case !found:
		return ""
	case c.Critical+c.High > 0:
		var parts []string
		if c.Critical > 0 {
			parts = append(parts, fmt.Sprintf("%d critical", c.Critical))
		}
		if c.High > 0 {
			parts = append(parts, fmt.Sprintf("%d high", c.High))
		}
		return strings.Join(parts, " · ")
	case secTotal(c) > 0:
		return "no critical or high vulnerabilities"
	}
	return "no known vulnerabilities"
}
