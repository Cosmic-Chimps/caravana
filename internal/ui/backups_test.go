package ui

import (
	"context"
	"net/http"
	"net/url"
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
