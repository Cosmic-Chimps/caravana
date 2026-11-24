package ui

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// placesSentinel is a destination's secret key no page, form, redirect or log line may hold.
const placesSentinel = "s3cret-sentinel-052-ui"

// twoDestinations is backupsEnv with a second destination, "offsite", whose secret key is the sentinel.
func twoDestinations(t *testing.T) *consoleEnv {
	t.Helper()
	e := backupsEnv(t, true)
	addDest(t, e, "offsite", "offsite-bkt", placesSentinel, "prefix", "caravana")
	return e
}

// placesForm is the "Where backups go" post of duo's web service on the page's revision.
func (e *consoleEnv) placesForm(t *testing.T, set url.Values) url.Values {
	t.Helper()
	v := url.Values{"revision": {e.revisionOf(t, "duo")}, "service": {"web"}}
	for k, vs := range set {
		v[k] = vs
	}
	return v
}

// saveBackupAt stores a succeeded backup of duo's web service, in main, with the given copies.
func (e *consoleEnv) saveBackupAt(t *testing.T, id string, started time.Time, expired bool, copies ...store.BackupCopy) {
	t.Helper()
	ctx := context.Background()
	done := started.Add(3 * time.Second)
	err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "duo", ID: id, Service: "web", Engine: app.BackupPostgres, Status: store.BackupSucceeded,
		Object: "caravana/duo/web/" + id + ".dump", Bytes: 2048, StartedAt: started, FinishedAt: &done, Destination: "main"})
	if err != nil {
		t.Fatalf("SaveBackup %s: %v", id, err)
	}
	for _, c := range copies {
		c.AppID, c.BackupID = "duo", id
		if err := e.Store.QueueBackupCopies(ctx, "duo", id, []string{c.Destination}); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.SaveBackupCopy(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if expired {
		if err := e.Store.ExpireBackups(ctx, "duo", []string{id}); err != nil {
			t.Fatal(err)
		}
	}
}

// With one destination the tab is as it was; with two it says where backups go, and its "Where backups
// go" form (plain HTML) sets the destination and the copies as a deploy on the page's revision.
func TestBackupsTabPlaces(t *testing.T) {
	t.Run("one destination says nothing new", func(t *testing.T) {
		e := backupsEnv(t, true)
		body := e.pageOf(t, "/apps/duo/backups")
		if strings.Contains(body, "Where backups go") || strings.Contains(text(body), "copied to") {
			t.Errorf("the tab offers places with one destination:\n%s", text(body))
		}
	})

	e := twoDestinations(t)
	body := e.pageOf(t, "/apps/duo/backups")
	got := text(body)
	for _, want := range []string{"1 backed up · 0 failing · 0 not · stored in main, offsite", "to main", "Where backups go…", "Backups go to", "Also copy to", "Keep in each copy",
		"The project's default, else the installation's"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	// main is where the backups go: only offsite is offered as a copy.
	if !strings.Contains(body, `name="copyTo" value="offsite"`) || strings.Contains(body, `name="copyTo" value="main"`) {
		t.Errorf("the copy checkboxes:\n%s", body)
	}
	if strings.Contains(body, "hx-post") {
		t.Error("the form needs script")
	}

	before := e.deployCount(t, "duo")
	resp, _ := e.postForm(t, "/apps/duo/backups/places", e.placesForm(t, url.Values{"copyTo": {"offsite"}, "copyKeep": {"14"}}))
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/apps/duo/deployments#deployment-") {
		t.Fatalf("save = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if e.deployCount(t, "duo") != before+1 {
		t.Error("saving where backups go didn't deploy")
	}
	b := e.storedApp(t, "duo").Services[0].Backup
	if b == nil || b.Destination != "" || len(b.CopyTo) != 1 || b.CopyTo[0] != "offsite" || b.CopyKeep != 14 || b.Engine != app.BackupPostgres {
		t.Errorf("the stored policy = %+v, want offsite as a copy kept 14 and the rest as it was", b)
	}
	got = text(e.pageOf(t, "/apps/duo/backups"))
	if !strings.Contains(got, "to main, copied to offsite (keeps 14)") {
		t.Errorf("the tab lacks the policy line:\n%s", got)
	}

	t.Run("refusals draw the form at its field", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			set    url.Values
			status int
			id     string
			want   string
		}{
			{"unknown destination", url.Values{"destination": {"nowhere"}}, 422, "pl-dest-web-error", "nowhere"},
			{"unknown copy", url.Values{"copyTo": {"nowhere"}}, 422, "pl-copy-web-error", "nowhere"},
			{"a copy where it goes", url.Values{"destination": {"offsite"}, "copyTo": {"offsite"}}, 422, "pl-copy-web-error", "already go"},
			{"keep not a number", url.Values{"copyTo": {"offsite"}, "copyKeep": {"many"}}, 422, "pl-keep-web-error", "whole number"},
			{"keep out of range", url.Values{"copyTo": {"offsite"}, "copyKeep": {"500"}}, 422, "pl-keep-web-error", "between 1 and 100"},
		} {
			n := e.deployCount(t, "duo")
			resp, body := e.postForm(t, "/apps/duo/backups/places", e.placesForm(t, tc.set))
			if resp.StatusCode != tc.status || !strings.Contains(body, `id="`+tc.id+`"`) || !strings.Contains(text(body), tc.want) {
				t.Errorf("%s = %d, want the form with %q under its field:\n%s", tc.name, resp.StatusCode, tc.id, text(body))
			}
			if !strings.Contains(body, `<details class="cv-confirm" open>`) {
				t.Errorf("%s: the form isn't open", tc.name)
			}
			if e.deployCount(t, "duo") != n {
				t.Errorf("%s deployed", tc.name)
			}
		}
		// What was posted is drawn again.
		_, body := e.postForm(t, "/apps/duo/backups/places", e.placesForm(t, url.Values{"copyTo": {"offsite"}, "copyKeep": {"many"}}))
		if !strings.Contains(body, `name="copyKeep" value="many"`) || !regexp.MustCompile(`name="copyTo" value="offsite" checked`).MatchString(body) {
			t.Error("the refused form lost what was typed")
		}
	})

	t.Run("a stale page and an unknown service", func(t *testing.T) {
		stale := e.placesForm(t, nil)
		stale.Set("revision", "0000000000000000")
		if resp, _ := e.postForm(t, "/apps/duo/backups/places", stale); resp.StatusCode != http.StatusConflict {
			t.Errorf("a stale revision = %d, want 409", resp.StatusCode)
		}
		if resp, _ := e.postForm(t, "/apps/duo/backups/places", e.placesForm(t, url.Values{"service": {"nope"}})); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("an unknown service = %d, want 422", resp.StatusCode)
		}
	})
}

// Each backup says where it is, and a restore may name the place to read from.
func TestBackupsTabWhereAndRestoreFrom(t *testing.T) {
	e := twoDestinations(t)
	now := time.Now().UTC()
	ok := func(dest string) store.BackupCopy {
		fin := now
		return store.BackupCopy{Destination: dest, Status: store.CopySucceeded, Object: "caravana/duo/web/x.dump", Bytes: 2048, FinishedAt: &fin}
	}
	e.saveBackupAt(t, "web-1000", now.Add(-5*time.Hour), false, ok("offsite"))
	e.saveBackupAt(t, "web-2000", now.Add(-4*time.Hour), false, store.BackupCopy{Destination: "offsite", Status: store.CopyRunning})
	e.saveBackupAt(t, "web-3000", now.Add(-3*time.Hour), false, store.BackupCopy{Destination: "offsite", Status: store.CopyFailed, Message: "access denied"})
	e.saveBackupAt(t, "web-4000", now.Add(-2*time.Hour), true, ok("offsite"))
	e.saveBackupAt(t, "web-5000", now.Add(-time.Hour), false)

	body := e.pageOf(t, "/apps/duo/backups")
	row := func(id string) string {
		m := regexp.MustCompile(`(?s)id="backup-` + id + `".*?</div>\s*(?:<div class="cv-brun|</div>)`).FindString(body)
		if m == "" {
			t.Fatalf("no row for %s", id)
		}
		return text(m)
	}
	for id, want := range map[string]string{
		"web-1000": "in main · copied to offsite",
		"web-2000": "in main · copying to offsite…",
		"web-3000": "in main · copy to offsite failed: access denied",
		"web-4000": "deleted from main, kept in offsite",
		"web-5000": "in main",
	} {
		if got := row(id); !strings.Contains(got, want) {
			t.Errorf("%s: %q lacks %q", id, got, want)
		}
	}
	if strings.Contains(row("web-5000"), "copied") {
		t.Error("a backup with no copies says it was copied")
	}

	// A backup in two places offers "Restore from"; one in a single place doesn't.
	sel := regexp.MustCompile(`(?s)<form method="post" action="/apps/duo/backups/web-1000/restore".*?</form>`).FindString(body)
	for _, want := range []string{`name="from"`, `Automatic: the first that has it`, `<option value="main">`, `<option value="offsite">`} {
		if !strings.Contains(sel, want) {
			t.Errorf("web-1000's restore lacks %q:\n%s", want, sel)
		}
	}
	if only := regexp.MustCompile(`(?s)<form method="post" action="/apps/duo/backups/web-5000/restore".*?</form>`).FindString(body); strings.Contains(only, `name="from"`) {
		t.Error("a backup in one place offers Restore from")
	}
	// A backup deleted from main offers only offsite, and its restore is still possible.
	if gone := regexp.MustCompile(`(?s)<form method="post" action="/apps/duo/backups/web-4000/restore".*?</form>`).FindString(body); gone == "" || strings.Contains(gone, `<option value="main">`) {
		t.Errorf("web-4000's restore form:\n%s", gone)
	}

	resp, body := e.postForm(t, "/apps/duo/backups/web-1000/restore", url.Values{"confirm": {"web"}, "from": {"nowhere"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "no copy in") {
		t.Errorf("restore from a place that hasn't it = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, _ = e.postForm(t, "/apps/duo/backups/web-1000/restore", url.Values{"confirm": {"web"}, "from": {"offsite"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("restore from offsite = %d", resp.StatusCode)
	}
	_, restores, err := e.Svc.ListBackups(context.Background(), "duo")
	if err != nil || len(restores) != 1 || restores[0].Destination != "offsite" {
		t.Errorf("restores = %+v (%v), want one read from offsite", restores, err)
	}
}

// The project's page chooses where its backups go when a policy names none.
func TestProjectBackupsGoTo(t *testing.T) {
	t.Run("one destination offers nothing", func(t *testing.T) {
		e := backupsEnv(t, true)
		if body := e.pageOf(t, "/projects/default/settings"); strings.Contains(body, "Backups go to") {
			t.Error("the project page offers a choice of one")
		}
	})

	e := twoDestinations(t)
	body := e.pageOf(t, "/projects/default/settings")
	got := text(body)
	for _, want := range []string{"Backups go to", "The installation's default (main)", "offsite", "the backup jobs use it from their next run"} {
		if !strings.Contains(got, want) {
			t.Errorf("the project page lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(body, `<option value="" selected>`) {
		t.Error("the installation's default isn't the selected choice")
	}

	resp, _ := e.postForm(t, "/projects/default/backups", url.Values{"backupDestination": {"offsite"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/projects/default/settings?saved=backups" {
		t.Fatalf("save = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	p, err := e.Svc.Project(context.Background(), "default")
	if err != nil || p.BackupDestination != "offsite" {
		t.Errorf("project = %+v (%v), want offsite", p, err)
	}
	body = e.pageOf(t, "/projects/default/settings?saved=backups")
	if !strings.Contains(body, `<option value="offsite" selected>`) || !strings.Contains(text(body), "Where this project's backups go is saved.") {
		t.Errorf("the page after saving:\n%s", text(body))
	}
	// The tab now says where duo's backups go: the project's default.
	if got := text(e.pageOf(t, "/apps/duo/backups")); !strings.Contains(got, "to offsite") {
		t.Errorf("the tab doesn't follow the project's default:\n%s", got)
	}

	resp, body = e.postForm(t, "/projects/default/backups", url.Values{"backupDestination": {"nowhere"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="backupDestination-error"`) || !strings.Contains(text(body), "nowhere") {
		t.Errorf("an unknown destination = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, _ = e.postForm(t, "/projects/nope/backups", url.Values{"backupDestination": {"offsite"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown project = %d, want 404", resp.StatusCode)
	}
	// Back to the installation's default.
	if resp, _ = e.postForm(t, "/projects/default/backups", url.Values{"backupDestination": {""}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("clearing = %d", resp.StatusCode)
	}
	if p, _ := e.Svc.Project(context.Background(), "default"); p.BackupDestination != "" {
		t.Errorf("project default = %q, want none", p.BackupDestination)
	}
}

// A destination's secret key is in no page, form, redirect or log line, through the destinations, the places
// form, the project's page and a restore from a place.
func TestDestinationKeyNeverShows(t *testing.T) {
	e := twoDestinations(t)
	e.Nomad.FinishOnRegisterPrefix("_backup-check-", true, "", func(*api.Job) (string, string) { return "caravana-check: ok\n", "" })
	now := time.Now().UTC()
	fin := now
	e.saveBackupAt(t, "web-1000", now.Add(-5*time.Hour), false, store.BackupCopy{Destination: "offsite", Status: store.CopySucceeded,
		Object: "caravana/duo/web/x.dump", FinishedAt: &fin})
	var seen []string
	note := func(label, body string, resp *http.Response) {
		seen = append(seen, label+": "+body)
		if resp != nil {
			seen = append(seen, label+" Location: "+resp.Header.Get("Location"))
		}
	}
	for _, path := range []string{"/settings/backup-storage", "/settings/backup-storage?edit=offsite", "/apps/duo/backups", "/apps/duo/configure",
		"/projects/default/settings", "/apps/duo/deployments", "/projects"} {
		note("GET "+path, e.pageOf(t, path), nil)
	}
	post := func(label, path string, form url.Values) {
		resp, body := e.postForm(t, path, form)
		note(label, body, resp)
	}
	post("places", "/apps/duo/backups/places", e.placesForm(t, url.Values{"copyTo": {"offsite"}}))
	post("places refused", "/apps/duo/backups/places", e.placesForm(t, url.Values{"destination": {"nowhere"}}))
	post("project", "/projects/default/backups", url.Values{"backupDestination": {"offsite"}})
	post("project refused", "/projects/default/backups", url.Values{"backupDestination": {"nowhere"}})
	post("restore", "/apps/duo/backups/web-1000/restore", url.Values{"confirm": {"web"}, "from": {"offsite"}})
	post("edit refused", "/settings/backups", destForm("offsite", "offsite-bkt", "edit", "1", "endpoint", "ftp://x", "secretAccessKey", placesSentinel))
	post("test", "/settings/backups/test", url.Values{"name": {"offsite"}})
	seen = append(seen, "log: "+e.Logs.String())
	for _, s := range seen {
		if strings.Contains(s, placesSentinel) {
			i := strings.Index(s, placesSentinel)
			t.Errorf("a secret key shows in %.60q…", s[max(0, i-40):])
		}
	}
}

// The Compose form keeps where a service's backups go and are copied (feature 052): it round-trips through
// the draft, from a kept request and a stored policy, and is refused with a bad number or no engine.
func TestComposeFormBackupPlaces(t *testing.T) {
	post := overlayPost("x", []string{"web"}, map[string]string{
		"svc.web.backupEngine": "volume", "svc.web.backupDestination": " main ", "svc.web.backupCopyTo": "offsite, cold  archive", "svc.web.backupCopyKeep": "14",
	})
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatal("parse refused where backups go")
	}
	want := &app.Backup{Engine: "volume", Destination: "main", CopyTo: []string{"offsite", "cold", "archive"}, CopyKeep: 14}
	if got := d.request().Services["web"].Backup; !reflect.DeepEqual(got, want) {
		t.Errorf("backup = %+v, want %+v", got, want)
	}
	back := draftOf(d.request())
	web, _ := back.service("web")
	if web.BackupDestination.Value != "main" || web.BackupCopyTo.Value != "offsite, cold, archive" || web.BackupCopyKeep.Value != "14" {
		t.Errorf("draftOf = %q, %q, %q", web.BackupDestination.Value, web.BackupCopyTo.Value, web.BackupCopyKeep.Value)
	}
	if got := back.request().Services["web"].Backup; !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !web.hasBackupInput() {
		t.Error("a service with only places hides the backup inputs")
	}

	for _, tt := range []struct {
		name, on, text string
		inputs         map[string]string
	}{
		{"a bad number", "backupCopyKeep", "whole number from 1 to 100", map[string]string{"backupEngine": "volume", "backupCopyTo": "offsite", "backupCopyKeep": "lots"}},
		{"too many", "backupCopyKeep", "whole number from 1 to 100", map[string]string{"backupEngine": "volume", "backupCopyTo": "offsite", "backupCopyKeep": "101"}},
		{"no engine", "backupEngine", "Choose an engine to set where backups go.", map[string]string{"backupCopyTo": "offsite"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inputs := map[string]string{}
			for k, v := range tt.inputs {
				inputs["svc.web."+k] = v
			}
			d, ok := parseComposeDraft(overlayPost("x", []string{"web"}, inputs))
			s, _ := d.service("web")
			var errText string
			for _, f := range s.allFields() {
				if len(f.Error) > 0 {
					errText += f.ID + ": " + f.Error.String() + "; "
				}
			}
			if ok || !strings.Contains(errText, tt.on) || !strings.Contains(errText, tt.text) {
				t.Errorf("ok = %v, errors = %q; want %q on %s", ok, errText, tt.text, tt.on)
			}
		})
	}
}
