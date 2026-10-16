package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const destSecret = "s3cr3t-key-marker"

// backupsEnv has duo with web as a backed-up postgres service and a
// destination, deployed through the service so its backup job exists.
func backupsEnv(t *testing.T, destination bool) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	if destination {
		resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
			"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {destSecret}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST /settings/backups = %d:\n%s", resp.StatusCode, text(body))
		}
	}
	a := duoApp()
	a.Services[0].Replicas = nil
	a.Services[0].Env = append(a.Services[0].Env, app.EnvVar{Name: "POSTGRES_PASSWORD", Value: "pw"})
	a.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres}
	if destination {
		rec, err := e.Svc.Deploy(context.Background(), a, app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		e.waitDeploy(t, rec.Deployment.ID)
	} else {
		e.seed(t, a)
	}
	return e
}

// waitDeploy waits until the deployment ends.
func (e *consoleEnv) waitDeploy(t *testing.T, id string) {
	t.Helper()
	for range 500 {
		rec, err := e.Store.GetDeployment(context.Background(), id)
		if err == nil && rec.Status != store.StatusPending && rec.Status != store.StatusRunning {
			return
		}
		sleepMs(10)
	}
	t.Fatalf("deployment %s didn't end", id)
}

// US3: the destination form keeps its secret out of the page.
func TestSettingsBackupDestination(t *testing.T) {
	e := backupsEnv(t, true)
	_, body := e.get(t, "/settings?backups=saved")
	got := text(body)
	for _, want := range []string{"Backup destination", "Saved. Every database's next backup goes here.", "Saved. Leave it empty to keep it."} {
		if !strings.Contains(got, want) {
			t.Errorf("Settings lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(body, `name="bucket" value="backups"`) || strings.Contains(body, destSecret) {
		t.Error("the form lacks the saved bucket, or shows the secret key")
	}

	resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"ftp://x"}, "region": {"r"}, "bucket": {"backups"}, "accessKeyId": {"AK"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="dest-endpoint-error"`) || !strings.Contains(body, `value="ftp://x"`) {
		t.Errorf("a bad endpoint = %d, want the form again with the error under it", resp.StatusCode)
	}
}

// US1, US2: the tab lists the database, backs up now, and restores only after
// the name is typed.
func TestBackupsTab(t *testing.T) {
	t.Run("without a destination", func(t *testing.T) {
		e := backupsEnv(t, false)
		_, body := e.get(t, "/apps/duo/backups")
		if got := text(body); !strings.Contains(got, "No backup destination is set.") || strings.Contains(got, "Back up now") {
			t.Errorf("the tab without a destination:\n%s", got)
		}
	})

	e := backupsEnv(t, true)
	_, body := e.get(t, "/apps/duo/backups")
	got := text(body)
	for _, want := range []string{"Backups", "web", "PostgreSQL, on 0 3 * * * (UTC), keeping the last 7.", "No backups yet."} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}

	resp, _ := e.postForm(t, "/apps/duo/backups", url.Values{"service": {"web"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Back up now = %d", resp.StatusCode)
	}
	resp, body = e.postForm(t, "/apps/duo/backups", url.Values{"service": {"web"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "is running") {
		t.Errorf("a second Back up now = %d:\n%s", resp.StatusCode, text(body))
	}

	runs := e.Nomad.Runs("duo_web_backup")
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	e.Nomad.FinishRun(runs[0], true, "", "caravana-backup key=backups/duo/web/1.dump bytes=2048\n", "")
	var backupID string
	for range 500 {
		backups, _, _ := e.Svc.ListBackups(context.Background(), "duo")
		if len(backups) == 1 && backups[0].Status == store.BackupSucceeded {
			backupID = backups[0].ID
			break
		}
		sleepMs(10)
	}
	if backupID == "" {
		t.Fatal("the backup wasn't recorded")
	}
	_, body = e.get(t, "/apps/duo/backups")
	if got := text(body); !strings.Contains(got, "succeeded web") || !strings.Contains(got, "2.0 KB") || !strings.Contains(body, "/backups/"+backupID+"/restore") {
		t.Errorf("the tab after the backup:\n%s", got)
	}

	resp, _ = e.postForm(t, "/apps/duo/backups/"+backupID+"/restore", url.Values{"confirm": {"we"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || e.Nomad.Job("duo_web_restore") != nil {
		t.Errorf("a wrong confirmation = %d, restore job %v", resp.StatusCode, e.Nomad.Job("duo_web_restore") != nil)
	}
	resp, _ = e.postForm(t, "/apps/duo/backups/"+backupID+"/restore", url.Values{"confirm": {"web"}})
	if resp.StatusCode != http.StatusSeeOther || e.Nomad.Job("duo_web_restore") == nil {
		t.Errorf("restore = %d, restore job %v", resp.StatusCode, e.Nomad.Job("duo_web_restore") != nil)
	}
	_, body = e.get(t, "/apps/duo/backups")
	if !strings.Contains(text(body), "restoring web") {
		t.Errorf("the tab lacks the running restore:\n%s", text(body))
	}

	_, body = e.get(t, "/apps/duo/configure")
	if !strings.Contains(text(body), "Its backups stay in the backup destination.") {
		t.Error("the delete sentence doesn't say the backups stay")
	}
}

func sleepMs(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }

// kumaApp is one service, with a name that holds "-", on a volume, backed up
// with the volume engine (feature 016). It needs no port and no credentials.
func kumaApp() app.App {
	return app.App{ID: "kuma", Name: "Kuma", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{Name: "uptime-kuma", Image: "traefik/whoami:latest",
			Volumes: []app.Volume{{Name: "data", Path: "/app/data"}},
			Backup:  &app.Backup{Engine: app.BackupVolume, Keep: 3}}}}
}

// volumeBackupsEnv has kuma deployed through the service with a destination.
func volumeBackupsEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	if resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
		"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {destSecret}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /settings/backups = %d:\n%s", resp.StatusCode, text(body))
	}
	rec, err := e.Svc.Deploy(context.Background(), kumaApp(), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	return e
}

// saveVolumeBackup stores a finished volume backup, as a run leaves it.
func (e *consoleEnv) saveVolumeBackup(t *testing.T, id string, stopped time.Duration, bytes int64) {
	t.Helper()
	started := time.Now().Add(-2 * time.Hour).UTC()
	finished := started.Add(stopped)
	err := e.Store.SaveBackup(context.Background(), store.BackupRecord{AppID: "kuma", ID: id, Service: "uptime-kuma", Engine: app.BackupVolume,
		Status: store.BackupSucceeded, Bytes: bytes, Object: "caravana/kuma/uptime-kuma/1/", StartedAt: started, FinishedAt: &finished,
		Stopped: &stopped, Volumes: []store.VolumeArchive{{Name: "data", Bytes: bytes, SizeKB: 4}}})
	if err != nil {
		t.Fatalf("SaveBackup: %v", err)
	}
}

// Feature 016: a volume service's heading line, its "Back up now" behind a
// confirmation, the stop time on its rows, and the volume Restore words; all
// as plain HTML, with no script.
func TestBackupsTabVolume(t *testing.T) {
	e := volumeBackupsEnv(t)
	e.saveVolumeBackup(t, "uptime-kuma-1000", 4*time.Second, 2048)

	_, body := e.get(t, "/apps/kuma/backups")
	got := text(body)
	for _, want := range []string{
		"Volume backup, every day at 03:00 UTC, keeping 3. The service stops while a backup runs (last time 4 s).",
		"succeeded uptime-kuma", "2.0 KB", "stopped 4 s",
		"The service stops for a few seconds while the backup runs.",
		"The service stops, its volumes are replaced by this backup's files, and it starts again. Files added since the backup are deleted.",
		"Type uptime-kuma to confirm",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	// Back up now sits in a <details>, and its form posts the service's name.
	details := regexp.MustCompile(`(?s)<details class="cv-confirm">\s*<summary[^>]*>Back up now…</summary>\s*<form method="post" action="/apps/kuma/backups"[^>]*>.*?name="service" value="uptime-kuma".*?</form>`)
	if !details.MatchString(body) {
		t.Errorf("Back up now isn't a confirmation with its own form:\n%s", body)
	}
	if strings.Contains(body, "pg_restore") || strings.Contains(got, "Clients connected meanwhile") {
		t.Error("the tab shows the database words for a volume backup")
	}

	// A wrong name opens the confirmation again with its error.
	resp, body := e.postForm(t, "/apps/kuma/backups/uptime-kuma-1000/restore", url.Values{"confirm": {"uptime"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Type uptime-kuma to confirm.") {
		t.Errorf("a wrong confirmation = %d:\n%s", resp.StatusCode, text(body))
	}
}

// Feature 016: a run's refusals are sentences on the tab, and a service that
// hasn't a running copy is told to deploy first.
func TestBackUpNowVolumeRefusals(t *testing.T) {
	e := volumeBackupsEnv(t)
	resp, body := e.postForm(t, "/apps/kuma/backups", url.Values{"service": {"uptime-kuma"}})
	if resp.StatusCode != http.StatusConflict ||
		!strings.Contains(text(body), "The service has no running copy to back up. Start it with a deploy first.") {
		t.Errorf("Back up now without a copy = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestScheduleText(t *testing.T) {
	tests := []struct{ spec, want string }{
		{"0 3 * * *", "every day at 03:00 UTC"},
		{"30 14 * * *", "every day at 14:30 UTC"},
		{"15 * * * *", "every hour at :15"},
		{"*/10 * * * *", "on */10 * * * * (UTC)"},
		{"0 3 * * 1", "on 0 3 * * 1 (UTC)"},
		{"0 25 * * *", "on 0 25 * * * (UTC)"},
		{"", "on  (UTC)"},
	}
	for _, tt := range tests {
		if got := scheduleText(tt.spec); got != tt.want {
			t.Errorf("scheduleText(%q) = %q, want %q", tt.spec, got, tt.want)
		}
	}
}

func TestStoppedText(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "under 1 s"},
		{400 * time.Millisecond, "under 1 s"},
		{500 * time.Millisecond, "1 s"},
		{4200 * time.Millisecond, "4 s"},
		{4600 * time.Millisecond, "5 s"},
		{90 * time.Second, "90 s"},
	}
	for _, tt := range tests {
		if got := stoppedText(tt.d); got != tt.want {
			t.Errorf("stoppedText(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
