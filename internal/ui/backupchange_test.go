package ui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// changeForm is the "Change the policy" post of duo's web service on the page's revision.
func (e *consoleEnv) changeForm(t *testing.T, id, service string, set url.Values) url.Values {
	t.Helper()
	v := url.Values{"revision": {e.revisionOf(t, id)}, "service": {service}}
	for k, vs := range set {
		v[k] = vs
	}
	return v
}

// Feature 059: each backed-up service's row has "Change the policy…", a plain form (no script) with the
// engine, the schedule and the number to keep as the policy has them now. Saving deploys on the page's
// revision and changes only those; the restore schedule, destination and copies stay.
func TestBackupsTabChangeThePolicy(t *testing.T) {
	e := twoDestinations(t)
	// Give the policy copies and a restore schedule that the change has to leave alone.
	if resp, body := e.postForm(t, "/apps/duo/backups/places", e.placesForm(t, url.Values{"copyTo": {"offsite"}, "copyKeep": {"9"}})); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("places = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, body := e.postForm(t, "/apps/duo/backups/restore-schedule", e.placesForm(t, url.Values{"schedule": {"0 4 * * *"}, "from": {"golden"}})); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("restore schedule = %d:\n%s", resp.StatusCode, text(body))
	}

	body := e.pageOf(t, "/apps/duo/backups")
	got := text(body)
	for _, want := range []string{"Change the policy…", "Engine", "PostgreSQL (the service keeps running)", "Daily at 03:00 UTC", "No automatic backups", "Backups to keep"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(body, `action="/apps/duo/backups/policy/change"`) || strings.Contains(body, "hx-post") {
		t.Error("the form is missing, or needs script")
	}
	// A service with no volume can't be changed to SQLite: no choice, no file input.
	if strings.Contains(body, `value="sqlite"`) || strings.Contains(body, `name="file"`) {
		t.Error("the form offers SQLite to a service with no volume")
	}
	if !strings.Contains(body, `id="chg-keep-web" type="text" inputmode="numeric" name="keep" value="7"`) {
		t.Errorf("the number to keep isn't prefilled with the default:\n%s", body)
	}

	n := e.deployCount(t, "duo")
	resp, _ := e.postForm(t, "/apps/duo/backups/policy/change", e.changeForm(t, "duo", "web",
		url.Values{"engine": {"postgres"}, "schedule": {app.BackupScheduleNever}, "keep": {"3"}}))
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/apps/duo/deployments#deployment-") {
		t.Fatalf("save = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if e.deployCount(t, "duo") != n+1 {
		t.Error("changing the policy didn't deploy")
	}
	b := e.storedApp(t, "duo").Services[0].Backup
	if b == nil || b.Engine != app.BackupPostgres || b.Schedule != app.BackupScheduleNever || b.Keep != 3 {
		t.Fatalf("the stored policy = %+v, want postgres, never, keeping 3", b)
	}
	if len(b.CopyTo) != 1 || b.CopyTo[0] != "offsite" || b.CopyKeep != 9 || b.Restore == nil || b.Restore.From != "golden" {
		t.Errorf("the stored policy = %+v, want the copies and the restore schedule as they were", b)
	}

	// The select now says none, and keeps the stored line as "Keep …" when it is another one.
	got = text(e.pageOf(t, "/apps/duo/backups"))
	if !strings.Contains(got, "No automatic backups") {
		t.Errorf("the tab lacks the schedule:\n%s", got)
	}
	e.postForm(t, "/apps/duo/backups/policy/change", e.changeForm(t, "duo", "web", url.Values{"engine": {"postgres"}, "schedule": {"30 2 * * *"}, "keep": {"5"}}))
	if page := e.pageOf(t, "/apps/duo/backups"); !strings.Contains(page, `<option value="30 2 * * *" selected>Keep every day at 02:30 UTC</option>`) {
		t.Errorf("a stored line that isn't a choice isn't offered as Keep:\n%s", text(page))
	}
}

func TestBackupsChangeThePolicyRefusals(t *testing.T) {
	e := backupsEnv(t, true)
	for _, tc := range []struct {
		name string
		set  url.Values
		id   string
		want string
	}{
		{"keep not a number", url.Values{"engine": {"postgres"}, "keep": {"lots"}}, "chg-keep-web-error", "whole number"},
		{"keep out of range", url.Values{"engine": {"postgres"}, "keep": {"500"}}, "chg-keep-web-error", "whole number"},
		{"a schedule that isn't cron", url.Values{"engine": {"postgres"}, "schedule": {"nightly"}, "keep": {"4"}}, "chg-schedule-web-error", "schedule"},
		{"an engine the service can't use", url.Values{"engine": {"volume"}, "keep": {"4"}}, "chg-engine-web-error", "volume"},
	} {
		n := e.deployCount(t, "duo")
		resp, body := e.postForm(t, "/apps/duo/backups/policy/change", e.changeForm(t, "duo", "web", tc.set))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="`+tc.id+`"`) || !strings.Contains(strings.ToLower(text(body)), tc.want) {
			t.Errorf("%s = %d, want the tab with %q under its field:\n%s", tc.name, resp.StatusCode, tc.id, text(body))
		}
		if !strings.Contains(body, `<details class="cv-confirm" open>`) {
			t.Errorf("%s: the form isn't open", tc.name)
		}
		if e.deployCount(t, "duo") != n {
			t.Errorf("%s deployed", tc.name)
		}
	}
	// What was posted is drawn again.
	_, body := e.postForm(t, "/apps/duo/backups/policy/change", e.changeForm(t, "duo", "web", url.Values{"engine": {"postgres"}, "keep": {"lots"}}))
	if !strings.Contains(body, `name="keep" value="lots"`) {
		t.Error("the refused form lost what was typed")
	}

	stale := e.changeForm(t, "duo", "web", url.Values{"engine": {"postgres"}})
	stale.Set("revision", "0000000000000000")
	if resp, _ := e.postForm(t, "/apps/duo/backups/policy/change", stale); resp.StatusCode != http.StatusConflict {
		t.Errorf("a stale revision = %d, want 409", resp.StatusCode)
	}
	if resp, _ := e.postForm(t, "/apps/duo/backups/policy/change", e.changeForm(t, "duo", "nope", nil)); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an unknown service = %d, want 422", resp.StatusCode)
	}
}

// A service with a writable volume can be changed to a SQLite file: the form has its choice and the
// Database file input, and the file is read only for that engine.
func TestBackupsChangeThePolicyToSQLite(t *testing.T) {
	e := volumeBackupsEnv(t)
	body := e.pageOf(t, "/apps/kuma/backups")
	for _, want := range []string{`<option value="sqlite">SQLite file (the service keeps running)</option>`, `name="file"`, `<option value="volume" selected>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q:\n%s", want, text(body))
		}
	}

	resp, refused := e.postForm(t, "/apps/kuma/backups/policy/change", e.changeForm(t, "kuma", "uptime-kuma", url.Values{"engine": {"sqlite"}, "file": {"/elsewhere/app.db"}, "keep": {"3"}}))
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(refused, `id="chg-file-uptime-kuma-error"`) {
		t.Errorf("a file outside the volume = %d, want the form with the error at the file:\n%s", resp.StatusCode, text(refused))
	}

	// A stale file in a volume change is ignored: the file belongs to the SQLite engine alone.
	resp, _ = e.postForm(t, "/apps/kuma/backups/policy/change", e.changeForm(t, "kuma", "uptime-kuma", url.Values{"engine": {"volume"}, "file": {"/app/data/app.db"}, "keep": {"3"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a volume change with a file typed = %d, want 303", resp.StatusCode)
	}
	e.waitDeploy(t, e.deployments(t, "kuma")[0].Deployment.ID)

	resp, _ = e.postForm(t, "/apps/kuma/backups/policy/change", e.changeForm(t, "kuma", "uptime-kuma", url.Values{"engine": {"sqlite"}, "file": {" /app/data/app.db "}, "keep": {"3"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a change to SQLite = %d, want 303", resp.StatusCode)
	}
	if b := e.storedApp(t, "kuma").Services[0].Backup; b == nil || b.Engine != app.BackupSQLite || b.File != "/app/data/app.db" {
		t.Errorf("the stored policy = %+v, want sqlite on /app/data/app.db", b)
	}
}

// The form isn't drawn for a stopped app, and no deploy comes of a post to a stopped one.
func TestBackupsChangeThePolicyStopped(t *testing.T) {
	e := backupsEnv(t, true)
	if resp, body := e.postAction(t, "/apps/duo/stop"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("stop = %d:\n%s", resp.StatusCode, text(body))
	}
	body := e.pageOf(t, "/apps/duo/backups")
	if strings.Contains(body, "Change the policy") {
		t.Error("a stopped app offers Change the policy")
	}
	n := e.deployCount(t, "duo")
	resp, page := e.postForm(t, "/apps/duo/backups/policy/change", e.changeForm(t, "duo", "web", url.Values{"engine": {"postgres"}, "keep": {"3"}}))
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), appStoppedText) || e.deployCount(t, "duo") != n {
		t.Errorf("a post to a stopped app = %d, want 409 and no deploy:\n%s", resp.StatusCode, text(page))
	}
}
