package ui

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// destForm is the form of a destination called name with bucket as its bucket; extra pairs are added.
func destForm(name, bucket string, extra ...string) url.Values {
	v := url.Values{"name": {name}, "endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"}, "bucket": {bucket}, "accessKeyId": {"AK-" + bucket}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v
}

// addDest adds a destination through the console's add form and fails the test unless it is saved.
func addDest(t *testing.T, e *consoleEnv, name, bucket, secret string, extra ...string) {
	t.Helper()
	form := destForm(name, bucket, extra...)
	form.Set("add", "1")
	form.Set("secretAccessKey", secret)
	resp, body := e.postForm(t, "/settings/backups", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add %s = %d:\n%s", name, resp.StatusCode, text(body))
	}
}

func TestBackupStorageListsDestinations(t *testing.T) {
	e := backupsEnv(t, true) // main, the default, and duo/web with a policy
	addDest(t, e, "offsite", "offsite-bkt", "SK-off", "prefix", "caravana")

	page := e.mustGet(t, "/settings/backup-storage")
	got := text(page)
	for _, want := range []string{"main default", "10.0.0.5:9000/backups", "offsite", "10.0.0.5:9000/offsite-bkt/caravana", "used by 1 service", "not used",
		"Make default", "Add a destination", "Make it the default"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	// One Make default: the default has none. Each row has Test, Edit and Remove under its name.
	if n := strings.Count(page, `aria-label="Make offsite the default"`); n != 1 || strings.Contains(page, `aria-label="Make main the default"`) {
		t.Errorf("Make default buttons: offsite %d, main present %v", n, strings.Contains(page, `aria-label="Make main the default"`))
	}
	for _, label := range []string{"Test main", "Test offsite", "Edit main", "Edit offsite", "Remove main", "Remove offsite"} {
		if !strings.Contains(page, `aria-label="`+label+`"`) {
			t.Errorf("no %q button", label)
		}
	}
	// Two destinations: the nav counts them.
	if !regexp.MustCompile(`Backup storage[^<]*<[^>]*>[^<]*2`).MatchString(page) && !strings.Contains(text(page), "Backup storage 2") {
		t.Logf("nav text: %s", text(page)[:400])
	}
	// No page holds a key, and no key field has a value.
	if strings.Contains(page, destSecret) || strings.Contains(page, "SK-off") {
		t.Error("the page holds a secret key")
	}
	for _, m := range regexp.MustCompile(`<input[^>]*name="secretAccessKey"[^>]*>`).FindAllString(page, -1) {
		if strings.Contains(m, "value=") {
			t.Errorf("a key input has a value: %s", m)
		}
	}
}

func TestBackupStorageAddRefusals(t *testing.T) {
	e := backupsEnv(t, true)
	for _, tc := range []struct {
		name, field string
		form        url.Values
	}{
		{"no name", "name", func() url.Values { v := destForm("", "bkt-x"); v.Set("secretAccessKey", "s"); return v }()},
		{"a bad name", "name", func() url.Values { v := destForm("Off_Site", "bkt-x"); v.Set("secretAccessKey", "s"); return v }()},
		{"no key", "secretAccessKey", destForm("offsite", "bkt-x")},
		{"a name that is taken", "name", func() url.Values { v := destForm("main", "bkt-x"); v.Set("secretAccessKey", "s"); return v }()},
		{"a bad bucket", "bucket", func() url.Values { v := destForm("offsite", "No_Such"); v.Set("secretAccessKey", "s"); return v }()},
	} {
		tc.form.Set("add", "1")
		resp, body := e.postForm(t, "/settings/backups", tc.form)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422 (the page drawn again, never a redirect)", tc.name, resp.StatusCode)
			continue
		}
		if !strings.Contains(body, `id="dest-add-`+tc.field+`-error"`) {
			t.Errorf("%s: no error under %s:\n%s", tc.name, tc.field, text(body))
		}
		if tc.field != "name" && !strings.Contains(body, `name="name" value="`+strings.ToLower(tc.form.Get("name"))+`"`) {
			t.Errorf("%s: what was typed isn't kept", tc.name)
		}
		if strings.Contains(body, `value="s"`) {
			t.Errorf("%s: the key was put back in the form", tc.name)
		}
	}
	if _, err := e.Svc.GetBackupDestinationNamed(t.Context(), "offsite"); err == nil {
		t.Error("a refused add stored a destination")
	}
	if main, err := e.Svc.GetBackupDestinationNamed(t.Context(), "main"); err != nil || main.Bucket == "bkt-x" {
		t.Errorf("an add under a taken name changed it: %+v, %v", main, err)
	}
}

func TestBackupStorageEditKeepsTheKey(t *testing.T) {
	e := backupsEnv(t, true)
	addDest(t, e, "offsite", "offsite-bkt", "SK-off")
	form := destForm("offsite", "offsite-two", "edit", "1")
	resp, body := e.postForm(t, "/settings/backups", form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/backup-storage?backups=saved" {
		t.Fatalf("edit = %d %s", resp.StatusCode, text(body))
	}
	if got := e.Nomad.Variable("caravana/backup-destinations/offsite")["secretAccessKey"]; got != "SK-off" {
		t.Errorf("key = %q, want it kept", got)
	}
	// A refused edit opens its form again with what was typed, in that row.
	bad := destForm("offsite", "No_Such", "edit", "1")
	resp, body = e.postForm(t, "/settings/backups", bad)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `value="No_Such"`) || !strings.Contains(body, "-bucket-error") {
		t.Errorf("a refused edit = %d", resp.StatusCode)
	}
	if !regexp.MustCompile(`<details[^>]*id="destination-edit-\d+"[^>]* open>`).MatchString(body) {
		t.Error("the refused edit form isn't open")
	}
	// The default can't be turned off from its own form: there is no box for it.
	_, page := e.get(t, "/settings/backup-storage")
	if strings.Count(page, `name="default"`) != 2 { // offsite's edit form and the add form; main's has none
		t.Errorf("default boxes = %d, want 2", strings.Count(page, `name="default"`))
	}
}

func TestBackupStorageTestMakeDefaultAndRemove(t *testing.T) {
	e := backupsEnv(t, true)
	addDest(t, e, "offsite", "offsite-bkt", "SK-off")
	result := "caravana-check: ok\n"
	failing := false
	e.Nomad.FinishOnRegisterPrefix("_backup-check-", true, "", func(*api.Job) (string, string) {
		if failing {
			return "caravana-check: failed write\n", "ERROR: AccessDenied: not allowed\n"
		}
		return result, ""
	})

	// Test: the answer announces the outcome under the row, and the row keeps it with a clock.
	resp, body := e.postForm(t, "/settings/backups/test", url.Values{"name": {"offsite"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "Tested now: listed, wrote and removed a test object.") {
		t.Fatalf("test = %d:\n%s", resp.StatusCode, text(body))
	}
	page := e.mustGet(t, "/settings/backup-storage")
	if !strings.Contains(text(page), "listed, wrote and removed a test object tested") || !strings.Contains(page, `<time class="cv-clock"`) {
		t.Errorf("the row doesn't keep the test with its time:\n%s", text(page))
	}
	failing = true
	resp, body = e.postForm(t, "/settings/backups/test", url.Values{"name": {"offsite"}})
	got := text(body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(got, "Tested now, and it can't be reached: could not write: ") || !strings.Contains(got, "AccessDenied") {
		t.Errorf("failed test = %d:\n%s", resp.StatusCode, got)
	}
	if !strings.Contains(body, "a destination failed its test") || !strings.Contains(body, "cv-mark-failed") {
		t.Error("the nav and the row don't carry the failed test")
	}
	if resp, _ = e.postForm(t, "/settings/backups/test", url.Values{"name": {"ghost"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("test of an unknown destination = %d, want a redirect", resp.StatusCode)
	}

	// Remove: the default and a destination in use are refused on the row, with the page drawn again.
	resp, body = e.postForm(t, "/settings/backups/remove", url.Values{"name": {"main"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "is the default backup destination: make another the default first") {
		t.Errorf("remove the default = %d:\n%s", resp.StatusCode, text(body))
	}
	if !regexp.MustCompile(`<details[^>]*name="conn-\d+" open>\s*<summary[^>]*aria-label="Remove main"`).MatchString(body) {
		t.Error("the Remove confirmation of the refused row isn't open")
	}

	// Make default moves the mark; then main is used by the service, offsite isn't.
	resp, _ = e.postForm(t, "/settings/backups/default", url.Values{"name": {"offsite"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/backup-storage?backups=default" {
		t.Fatalf("make default = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	page = e.mustGet(t, "/settings/backup-storage?backups=default")
	if !strings.Contains(text(page), "Made the default.") || !strings.Contains(text(page), "offsite default") {
		t.Errorf("after Make default:\n%s", text(page))
	}
	if resp, _ = e.postForm(t, "/settings/backups/default", url.Values{"name": {"ghost"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("make default of an unknown destination = %d", resp.StatusCode)
	}

	// A policy that copies to main (while offsite is the default) keeps it in use.
	a := duoApp()
	a.Services[0].Replicas = nil
	a.Services[0].Env = append(a.Services[0].Env, app.EnvVar{Name: "POSTGRES_PASSWORD", Value: "pw"})
	a.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres, Destination: "offsite", CopyTo: []string{"main"}}
	rec, err := e.Svc.Deploy(t.Context(), a, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	resp, body = e.postForm(t, "/settings/backups/remove", url.Values{"name": {"main"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "is used by duo/web (copy)") {
		t.Errorf("remove a destination in use = %d:\n%s", resp.StatusCode, text(body))
	}
	// A free one goes, and the page says so.
	addDest(t, e, "cold", "cold-bkt", "SK-cold")
	resp, _ = e.postForm(t, "/settings/backups/remove", url.Values{"name": {"cold"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/backup-storage?backups=removed" {
		t.Errorf("remove a free destination = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if e.Nomad.Variable("caravana/backup-destinations/cold") != nil {
		t.Error("the removed destination's key is still stored")
	}
	if page = e.mustGet(t, "/settings/backup-storage?backups=removed"); !strings.Contains(text(page), "Removed. Backups already kept there stay in the bucket.") {
		t.Error("no removal notice")
	}
}

func TestDestinationsState(t *testing.T) {
	ok := &store.DestinationTest{OK: true}
	bad := &store.DestinationTest{OK: false}
	main := store.BackupDestination{Name: "main", IsDefault: true}
	off := store.BackupDestination{Name: "offsite"}
	good := &store.BackupRecord{Status: store.BackupSucceeded, Destination: "offsite"}
	failed := &store.BackupRecord{Status: store.BackupFailed, Destination: "main"}
	oldFailed := &store.BackupRecord{Status: store.BackupFailed} // from before destinations had names: the default's
	for _, tc := range []struct {
		name  string
		f     backupFacts
		rows  []string
		mark  string
		count int
	}{
		{"nothing ran or was tested", backupFacts{Dests: []store.BackupDestination{main, off}}, []string{"idle", "idle"}, "", 2},
		{"a test that passed", backupFacts{Dests: []store.BackupDestination{withTest(main, ok), off}}, []string{"healthy", "idle"}, "", 2},
		{"a test that failed", backupFacts{Dests: []store.BackupDestination{main, withTest(off, bad)}}, []string{"idle", "failed"}, "failed", 0},
		{"the last backup went to offsite and worked", backupFacts{Dests: []store.BackupDestination{main, off}, Last: good}, []string{"idle", "healthy"}, "", 2},
		{"the last backup failed on main", backupFacts{Dests: []store.BackupDestination{main, off}, Last: failed, Failing: failed}, []string{"failed", "idle"}, "failed", 0},
		{"a record with no destination is the default's", backupFacts{Dests: []store.BackupDestination{main, off}, Last: oldFailed, Failing: oldFailed}, []string{"failed", "idle"}, "failed", 0},
		{"a service's newest failed while the last went well", backupFacts{Dests: []store.BackupDestination{main, off}, Last: good, Failing: failed}, []string{"warning", "healthy"}, "warning", 0},
		{"one destination has no count", backupFacts{Dests: []store.BackupDestination{main}}, []string{"idle"}, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := backupStorageState(tc.f)
			if strings.Join(st.Rows, ",") != strings.Join(tc.rows, ",") || st.Mark != tc.mark || st.Count != tc.count {
				t.Errorf("state = %+v, want rows %v mark %q count %d", st, tc.rows, tc.mark, tc.count)
			}
		})
	}
}

func withTest(d store.BackupDestination, t *store.DestinationTest) store.BackupDestination {
	d.Test = t
	return d
}

func TestDestUsedBy(t *testing.T) {
	for _, tc := range []struct {
		uses []service.DestinationUse
		want string
	}{
		{nil, "not used"},
		{[]service.DestinationUse{{App: "a", Service: "db", Role: "destination"}, {App: "a", Service: "db", Role: "copy"}}, "used by 1 service"},
		{[]service.DestinationUse{{App: "a", Service: "db"}, {App: "b", Service: "db"}}, "used by 2 services"},
		{[]service.DestinationUse{{Project: "shop"}}, "default of 1 project"},
		{[]service.DestinationUse{{App: "a", Service: "db"}, {Project: "shop"}, {Project: "blog"}}, "used by 1 service and 2 projects"},
	} {
		if got := destUsedBy(tc.uses); got != tc.want {
			t.Errorf("destUsedBy(%v) = %q, want %q", tc.uses, got, tc.want)
		}
	}
}
