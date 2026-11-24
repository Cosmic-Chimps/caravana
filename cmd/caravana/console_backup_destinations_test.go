package main

// The console step of feature 052: a second backup destination and a copy, through the forms.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// backupDestinations:
//  1. offsite is added through Settings › Backup storage (the add form, a plain post) and Test says it listed,
//     wrote and removed an object; neither page holds the key;
//  2. a busybox app with a volume is made from the Compose form with a volume backup policy, and its Backups tab's
//     "Where backups go…" form, posted with "Also copy to: offsite", is a deploy that ends healthy;
//  3. Back up now; the backup's row says "in <main> · copied to offsite" once the copy settles;
//  4. the backup's Restore form offers "Restore from" with both places;
//  5. the app is deleted and offsite removed through the console.
func (c *console) backupDestinations(t *testing.T) {
	const id = "con052"
	c.removeLabApp(id) // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	t.Cleanup(func() {
		c.removeLabApp(id)
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/backups/destinations/offsite", "", ""); err == nil {
			resp.Body.Close()
		}
	})

	c.g.startBucket(t) // the default destination, pointing at the bucket job
	secret := "gate-" + c.g.run
	var def struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
	}
	c.g.getJSON(t, "/v1/backups/destination", &def)
	if def.Name == "" || def.Endpoint == "" {
		t.Fatalf("the default destination = %+v", def)
	}
	noKey := func(what, page string) {
		t.Helper()
		if strings.Contains(page, secret) {
			t.Errorf("%s holds the destination's secret key", what)
		}
	}

	// 1. Add and Test.
	resp, body := c.post(t, "/settings/backups", url.Values{"add": {"1"}, "name": {"offsite"}, "endpoint": {def.Endpoint}, "region": {"us-east-1"},
		"bucket": {"offsite"}, "prefix": {"gate"}, "accessKeyId": {"GATEKEY"}, "secretAccessKey": {secret}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/backup-storage?backups=saved" {
		t.Fatalf("POST the add form = %d → %q, want 303 to Backup storage: %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	noKey("the add form's answer", body)
	_, page := c.get(t, "/settings/backup-storage?backups=saved")
	for _, want := range []string{`aria-label="Test offsite"`, `aria-label="Edit offsite"`, `aria-label="Make offsite the default"`, "offsite"} {
		if !strings.Contains(page, want) {
			t.Errorf("Backup storage lacks %q:\n%s", want, page)
		}
	}
	noKey("Backup storage", page)
	resp, body = c.post(t, "/settings/backups/test", url.Values{"name": {"offsite"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "listed, wrote and removed a test object") {
		t.Fatalf("POST Test offsite = %d, want 200 saying it listed, wrote and removed an object:\n%s", resp.StatusCode, body)
	}
	noKey("the Test answer", body)

	// 2. The app, and "Where backups go…".
	resp, preview := c.post(t, "/apps/new/compose/preview", url.Values{"name": {id}, "compose": {consoleVolumeCompose}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST the preview = %d: %s", resp.StatusCode, preview)
	}
	form := formValuesOf(t, preview, "compose-form")
	form.Set("svc.web.healthPath", "/v")
	form.Set("svc.web.backupEngine", "volume")
	form.Set("svc.web.backupKeep", "2")
	resp, body = c.post(t, "/apps/new/compose", form)
	c.deployedFrom(t, id, resp, body)

	_, page = c.get(t, "/apps/"+id+"/backups")
	m := regexp.MustCompile(`(?s)<form method="post" action="/apps/` + id + `/backups/places"[^>]*>(.*?)</form>`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(m[1], `name="copyTo" value="offsite"`) || strings.Contains(m[1], `name="copyTo" value="`+def.Name+`"`) {
		t.Fatalf("the Backups tab's \"Where backups go…\" form is missing, or doesn't offer only offsite as a copy:\n%s", page)
	}
	if !strings.Contains(text(page), "to "+def.Name) {
		t.Errorf("the tab doesn't say where backups go (to %s):\n%s", def.Name, page)
	}
	places := valuesIn(m[1])
	places.Set("copyTo", "offsite")
	places.Set("copyKeep", "2")
	resp, body = c.post(t, "/apps/"+id+"/backups/places", places)
	c.deployedFrom(t, id, resp, body)
	_, page = c.get(t, "/apps/"+id+"/backups")
	if !strings.Contains(text(page), "to "+def.Name+", copied to offsite (keeps 2)") {
		t.Errorf("the policy line after saving:\n%s", page)
	}

	// 3. Back up now, and the copy.
	resp, body = c.post(t, "/apps/"+id+"/backups", url.Values{"service": {"web"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST Back up now = %d: %s", resp.StatusCode, body)
	}
	copied := "in " + def.Name + " · copied to offsite"
	waitFor(t, "the backup row to say it was copied to offsite", 6*time.Minute, func() bool {
		_, page = c.get(t, "/apps/"+id+"/backups")
		if strings.Contains(text(page), "copy to offsite failed") {
			t.Fatalf("the copy failed:\n%s", text(page))
		}
		return strings.Contains(text(page), copied)
	})
	noKey("the Backups tab", page)

	// 4. Restore from.
	restore := regexp.MustCompile(`(?s)<form method="post" action="/apps/` + id + `/backups/[^"]+/restore"[^>]*>.*?</form>`).FindString(page)
	for _, want := range []string{`name="from"`, "Automatic: the first that has it", `<option value="` + def.Name + `">`, `<option value="offsite">`} {
		if !strings.Contains(restore, want) {
			t.Errorf("the restore form lacks %q:\n%s", want, restore)
		}
	}

	// 5. Delete the app, then remove offsite.
	resp, body = c.post(t, "/apps/"+id+"/delete", url.Values{"confirm": {id}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST delete = %d: %s", resp.StatusCode, body)
	}
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	resp, body = c.post(t, "/settings/backups/remove", url.Values{"name": {"offsite"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/backup-storage?backups=removed" {
		t.Fatalf("POST Remove offsite = %d → %q, want 303 (the app that copied to it is gone): %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
}
