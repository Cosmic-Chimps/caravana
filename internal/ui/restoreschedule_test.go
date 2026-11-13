package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 036 on the Backups tab: Pin… and Unpin on the runs, the restore schedule's line and form,
// and who started each restore. backupsEnv has duo with web backed up as PostgreSQL and a destination.

// seedRuns stores n succeeded backups of web, one a day, the oldest first named web-1790000000+.
func seedRuns(t *testing.T, e *consoleEnv, n int) []string {
	t.Helper()
	var ids []string
	base := time.Now().UTC().Add(-time.Duration(n) * 24 * time.Hour)
	for i := range n {
		at := base.Add(time.Duration(i) * 24 * time.Hour)
		done := at.Add(5 * time.Second)
		id := fmt.Sprintf("web-%d", at.Unix())
		err := e.Store.SaveBackup(context.Background(), store.BackupRecord{AppID: "duo", ID: id, Service: "web", Engine: app.BackupPostgres,
			Status: store.BackupSucceeded, Object: "backups/duo/web/" + id + ".dump", Bytes: 2048, StartedAt: at, FinishedAt: &done})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestBackupsTabPins(t *testing.T) {
	e := backupsEnv(t, true)
	ids := seedRuns(t, e, 12) // ids[0] is the oldest: past "Show older"

	_, body := e.get(t, "/apps/duo/backups")
	got := text(body)
	if !strings.Contains(got, "7 backups") || strings.Contains(got, "plus") || strings.Contains(got, "pinned golden") {
		t.Errorf("the tab with no pin:\n%s", got)
	}
	if n := strings.Count(body, ">Pin…</summary>"); n != backupRunsShown {
		t.Errorf("Pin… offered on %d runs, want the %d listed", n, backupRunsShown)
	}
	for _, want := range []string{`action="/apps/duo/backups/` + ids[11] + `/pin"`, `name="label" value="golden"`, `maxlength="32"`,
		"A pinned backup is never deleted by retention and isn't counted in the backups kept. Pinning a label another backup has moves it here."} {
		if !strings.Contains(body, want) && !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q", want)
		}
	}
	if strings.Contains(body, `id="backup-`+ids[0]+`"`) {
		t.Error("the oldest run is listed before Show older")
	}

	// Pin the oldest by the stored record: it is listed, past the ten, with its label and Unpin.
	if _, err := e.Store.PinBackup(context.Background(), "duo", ids[0], "golden", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(t, "/apps/duo/backups")
	got = text(body)
	if !strings.Contains(body, `id="backup-`+ids[0]+`"`) || !strings.Contains(got, "pinned golden") || !strings.Contains(got, "7 backups, plus 1 pinned") ||
		!strings.Contains(body, `action="/apps/duo/backups/`+ids[0]+`/unpin"`) || !strings.Contains(got, "Unpin") {
		t.Errorf("the tab with a pinned run past the ten:\n%s", got)
	}
	if strings.Contains(body, `action="/apps/duo/backups/`+ids[0]+`/pin"`) {
		t.Error("Pin… is offered on a pinned run")
	}
}

func TestPinRoutes(t *testing.T) {
	ctx := context.Background()
	e := backupsEnv(t, true)
	ids := seedRuns(t, e, 3)
	pinOf := func(id string) string {
		t.Helper()
		b, err := e.Store.GetBackup(ctx, "duo", id)
		if err != nil {
			t.Fatal(err)
		}
		return b.Pin
	}

	resp, _ := e.postForm(t, "/apps/duo/backups/"+ids[0]+"/pin", url.Values{"label": {"golden"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/duo/backups#svc-web" {
		t.Fatalf("Pin = %d to %q, want 303 to the service's row", resp.StatusCode, resp.Header.Get("Location"))
	}
	if pinOf(ids[0]) != "golden" {
		t.Errorf("pin = %q, want golden", pinOf(ids[0]))
	}
	// The label moves.
	if resp, _ := e.postForm(t, "/apps/duo/backups/"+ids[1]+"/pin", url.Values{"label": {" golden "}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("second Pin = %d", resp.StatusCode)
	}
	if pinOf(ids[0]) != "" || pinOf(ids[1]) != "golden" {
		t.Errorf("pins = %q, %q; want the label moved", pinOf(ids[0]), pinOf(ids[1]))
	}

	for _, tt := range []struct {
		name, path string
		form       url.Values
		status     int
		want       string
	}{
		{"a bad label", "/apps/duo/backups/" + ids[2] + "/pin", url.Values{"label": {"My Golden"}}, http.StatusUnprocessableEntity, "must be 1 to 32 lowercase letters"},
		{"no label", "/apps/duo/backups/" + ids[2] + "/pin", url.Values{}, http.StatusUnprocessableEntity, "must be 1 to 32 lowercase letters"},
		{"an unknown backup", "/apps/duo/backups/web-1/pin", url.Values{"label": {"x"}}, http.StatusNotFound, "can't"},
		{"an unknown backup to unpin", "/apps/duo/backups/web-1/unpin", url.Values{}, http.StatusNotFound, "not found"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.postForm(t, tt.path, tt.form)
			if resp.StatusCode != tt.status || !strings.Contains(text(body), tt.want) {
				t.Errorf("= %d:\n%s\nwant %d saying %q", resp.StatusCode, text(body), tt.status, tt.want)
			}
		})
	}
	if pinOf(ids[2]) != "" {
		t.Error("a refused pin changed a backup")
	}

	// A backup running refuses both.
	if err := e.Store.SaveBackup(ctx, store.BackupRecord{AppID: "duo", ID: "web-1792000000", Service: "web", Engine: app.BackupPostgres,
		Status: store.BackupRunning, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Postgres says it is running from Nomad's runs, not the store: start one through the job.
	if resp, _ := e.postForm(t, "/apps/duo/backups", url.Values{"service": {"web"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Back up now = %d", resp.StatusCode)
	}
	for _, path := range []string{"/apps/duo/backups/" + ids[2] + "/pin", "/apps/duo/backups/" + ids[1] + "/unpin"} {
		resp, body := e.postForm(t, path, url.Values{"label": {"other"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "is running") {
			t.Errorf("%s while a backup runs = %d:\n%s", path, resp.StatusCode, text(body))
		}
	}
}

func TestPinUnpinRoute(t *testing.T) {
	ctx := context.Background()
	e := backupsEnv(t, true)
	ids := seedRuns(t, e, 2)
	if _, err := e.Store.PinBackup(ctx, "duo", ids[0], "golden", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, _ := e.postForm(t, "/apps/duo/backups/"+ids[0]+"/unpin", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/duo/backups#svc-web" {
		t.Fatalf("Unpin = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if b, _ := e.Store.GetBackup(ctx, "duo", ids[0]); b.Pin != "" {
		t.Errorf("pin = %q after Unpin", b.Pin)
	}
	// Unpinning an unpinned backup is not an error.
	if resp, _ := e.postForm(t, "/apps/duo/backups/"+ids[1]+"/unpin", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Unpin of an unpinned backup = %d, want 303", resp.StatusCode)
	}
}

func TestRestoreScheduleForm(t *testing.T) {
	ctx := context.Background()
	e := backupsEnv(t, true)
	ids := seedRuns(t, e, 2)
	post := func(form url.Values) (*http.Response, string) {
		t.Helper()
		form.Set("service", "web")
		if form.Get("revision") == "" {
			form.Set("revision", e.revisionOf(t, "duo"))
		}
		return e.postForm(t, "/apps/duo/backups/restore-schedule", form)
	}

	// Before: the form offers the defaults and no line.
	_, body := e.get(t, "/apps/duo/backups")
	got := text(body)
	if !strings.Contains(got, "Restore on a schedule…") || !strings.Contains(got, "At each time, the backup pinned under this label replaces what the service holds now. This deploys the app.") ||
		!strings.Contains(body, `name="schedule" value="0 4 * * *"`) || !strings.Contains(body, `name="from" value="golden"`) ||
		strings.Contains(got, "Stop restoring") || strings.Contains(got, "Restores golden") {
		t.Errorf("the tab before a schedule:\n%s", got)
	}

	// Refusals change nothing.
	n := e.deployCount(t, "duo")
	for _, tt := range []struct {
		name   string
		form   url.Values
		status int
		want   string
	}{
		{"a schedule alone", url.Values{"schedule": {"0 4 * * *"}}, http.StatusUnprocessableEntity, "Give both a schedule and a label, or neither."},
		{"a label alone", url.Values{"from": {"golden"}}, http.StatusUnprocessableEntity, "Give both a schedule and a label, or neither."},
		{"a bad label", url.Values{"schedule": {"0 4 * * *"}, "from": {"Golden"}}, http.StatusUnprocessableEntity, "A label is 1 to 32 lowercase letters"},
		{"a bad schedule", url.Values{"schedule": {"nightly"}, "from": {"golden"}}, http.StatusUnprocessableEntity, "The schedule"},
		{"a stale revision", url.Values{"schedule": {"0 4 * * *"}, "from": {"golden"}, "revision": {"0000000000000000"}}, http.StatusConflict, changedSincePage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := post(tt.form)
			if resp.StatusCode != tt.status || !strings.Contains(text(body), tt.want) {
				t.Errorf("= %d:\n%s\nwant %d saying %q", resp.StatusCode, text(body), tt.status, tt.want)
			}
		})
	}
	if e.deployCount(t, "duo") != n {
		t.Error("a refused schedule deployed")
	}

	// Set: 303 to the deployment, the stored app has it.
	resp, _ := post(url.Values{"schedule": {"0 4 * * *"}, "from": {"golden"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/apps/duo/deployments#deployment-") {
		t.Fatalf("Save = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	e.waitDeploy(t, strings.TrimPrefix(resp.Header.Get("Location"), "/apps/duo/deployments#deployment-"))
	rec, err := e.Store.GetApp(ctx, "duo")
	if err != nil {
		t.Fatal(err)
	}
	if want := (&app.RestoreSchedule{Schedule: "0 4 * * *", From: "golden"}); !reflect.DeepEqual(rec.App.Services[0].Backup.Restore, want) {
		t.Fatalf("restore = %+v, want %+v", rec.App.Services[0].Backup.Restore, want)
	}

	// The line, the note while nothing is pinned, and Stop restoring.
	_, body = e.get(t, "/apps/duo/backups")
	got = text(body)
	for _, want := range []string{"Restores golden every day at 04:00 UTC", "No backup of web is pinned as golden yet: each restore is refused until one is.", "Stop restoring"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab with a schedule lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "last:") {
		t.Error("the line says a last restore before there is one")
	}

	// Pinned: the note goes. A scheduled restore that failed is told.
	if _, err := e.Store.PinBackup(ctx, "duo", ids[0], "golden", time.Now()); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-8 * time.Hour)
	done := at.Add(time.Second)
	if err := e.Store.SaveRestore(ctx, store.RestoreRecord{ID: "r1", AppID: "duo", Service: "web", Status: store.BackupFailed,
		Message: "the service has no running copy", CreatedAt: at, FinishedAt: &done, Trigger: store.RestoreTriggerSchedule, Pin: "golden"}); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(t, "/apps/duo/backups")
	got = text(body)
	if strings.Contains(got, "is refused until one is") || !strings.Contains(got, "last: failed — the service has no running copy") {
		t.Errorf("the tab with the label pinned and a failed slot:\n%s", got)
	}
	if !strings.Contains(body, `name="schedule" value="0 4 * * *"`) {
		t.Error("the form isn't filled with the current schedule")
	}

	// A later restore that worked, and one skipped: the newest is told.
	later := time.Now().UTC().Add(-5 * time.Hour)
	if err := e.Store.SaveRestore(ctx, store.RestoreRecord{ID: "r2", AppID: "duo", Service: "web", BackupID: ids[0], Status: store.BackupSucceeded,
		CreatedAt: later, FinishedAt: &later, Trigger: store.RestoreTriggerSchedule, Pin: "golden"}); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(t, "/apps/duo/backups")
	if got := text(body); !strings.Contains(got, "hours ago, succeeded") {
		t.Errorf("the tab after a restore that worked:\n%s", got)
	}

	// Stop restoring: the same form with the stop button.
	resp, _ = post(url.Values{"schedule": {"0 4 * * *"}, "from": {"golden"}, "stop": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Stop restoring = %d", resp.StatusCode)
	}
	e.waitDeploy(t, strings.TrimPrefix(resp.Header.Get("Location"), "/apps/duo/deployments#deployment-"))
	if rec, _ := e.Store.GetApp(ctx, "duo"); rec.App.Services[0].Backup.Restore != nil {
		t.Errorf("restore = %+v after Stop restoring", rec.App.Services[0].Backup.Restore)
	}
	// An emptied form does the same.
	resp, _ = post(url.Values{"schedule": {""}, "from": {""}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("an emptied form = %d, want 303", resp.StatusCode)
	}
}

func TestRestoreScheduleFormNeedsABackedUpService(t *testing.T) {
	e := backupsEnv(t, true)
	resp, body := e.postForm(t, "/apps/duo/backups/restore-schedule", url.Values{"service": {"nope"}, "revision": {e.revisionOf(t, "duo")},
		"schedule": {"0 4 * * *"}, "from": {"golden"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "has no backed-up service nope") {
		t.Errorf("an unknown service = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestRecentRestoresSayWhoStartedThem(t *testing.T) {
	ctx := context.Background()
	e := backupsEnv(t, true)
	at := time.Now().UTC().Add(-5 * time.Hour)
	done := at.Add(time.Second)
	for _, r := range []store.RestoreRecord{
		{ID: "r-owner", AppID: "duo", Service: "web", BackupID: "web-1", Status: store.BackupSucceeded, CreatedAt: at, FinishedAt: &done},
		{ID: "r-sched", AppID: "duo", Service: "web", BackupID: "web-2", Status: store.BackupSucceeded, CreatedAt: at.Add(time.Hour), FinishedAt: &done,
			Trigger: store.RestoreTriggerSchedule, Pin: "golden"},
		{ID: "r-skip", AppID: "duo", Service: "web", Status: store.RestoreSkipped, Message: "a backup or restore of web was running",
			CreatedAt: at.Add(2 * time.Hour), FinishedAt: &done, Trigger: store.RestoreTriggerSchedule, Pin: "golden"},
	} {
		if err := e.Store.SaveRestore(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	_, body := e.get(t, "/apps/duo/backups")
	got := text(body)
	for _, want := range []string{"web from web-1 · by you", "web from web-2 · on schedule from golden", "web · on schedule from golden", "skipped",
		"a backup or restore of web was running"} {
		if !strings.Contains(got, want) {
			t.Errorf("the restores lack %q:\n%s", want, got)
		}
	}
	// A skipped restore has the idle mark, not the failed one.
	if i := strings.Index(body, "a backup or restore of web was running"); i < 0 || !strings.Contains(body[strings.LastIndex(body[:i], `role="row"`):i], "cv-mark-idle") {
		t.Error("a skipped restore doesn't have the idle mark")
	}
}

// The Compose form keeps a service's restore schedule: it round-trips through the draft, is refused
// without a label or an engine, and is a deploy's overlay.
func TestComposeFormRestoreSchedule(t *testing.T) {
	post := overlayPost("x", []string{"web"}, map[string]string{
		"svc.web.backupEngine": "volume", "svc.web.backupRestoreSchedule": " 0 4 * * * ", "svc.web.backupRestoreFrom": "golden",
	})
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatal("parse refused a restore schedule")
	}
	want := &app.Backup{Engine: "volume", Restore: &app.RestoreSchedule{Schedule: "0 4 * * *", From: "golden"}}
	if got := d.request().Services["web"].Backup; !reflect.DeepEqual(got, want) {
		t.Errorf("backup = %+v, want %+v", got, want)
	}

	// From the kept request back into the inputs, and out again.
	back := draftOf(d.request())
	web, _ := back.service("web")
	if web.BackupRestoreSchedule.Value != "0 4 * * *" || web.BackupRestoreFrom.Value != "golden" {
		t.Errorf("draftOf = %q, %q", web.BackupRestoreSchedule.Value, web.BackupRestoreFrom.Value)
	}
	if got := back.request().Services["web"].Backup; !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !web.hasBackupInput() {
		t.Error("a service with only a restore schedule hides the backup inputs")
	}

	// The same from the stored app's policy.
	rev := compose.Request{Compose: "x", Services: map[string]compose.Overlay{"web": {Backup: want}}}
	if got := draftOf(rev).request().Services["web"].Backup; !reflect.DeepEqual(got, want) {
		t.Errorf("from a request = %+v", got)
	}

	for _, tt := range []struct {
		name     string
		inputs   map[string]string
		wantOn   string
		wantText string
	}{
		{"a schedule without a label", map[string]string{"backupEngine": "volume", "backupRestoreSchedule": "0 4 * * *"}, "backupRestoreFrom", "Give both a schedule and a label, or neither."},
		{"a label without a schedule", map[string]string{"backupEngine": "volume", "backupRestoreFrom": "golden"}, "backupRestoreSchedule", "Give both a schedule and a label, or neither."},
		{"a bad label", map[string]string{"backupEngine": "volume", "backupRestoreSchedule": "0 4 * * *", "backupRestoreFrom": "Golden"}, "backupRestoreFrom", "A label is 1 to 32 lowercase letters"},
		{"no engine", map[string]string{"backupRestoreSchedule": "0 4 * * *", "backupRestoreFrom": "golden"}, "backupEngine", "Choose an engine to restore on a schedule."},
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
			if ok || !strings.Contains(errText, "-"+tt.wantOn+": ") && !strings.Contains(errText, tt.wantOn) || !strings.Contains(errText, tt.wantText) {
				t.Errorf("ok = %v, errors = %q; want %q on %s", ok, errText, tt.wantText, tt.wantOn)
			}
		})
	}
}
