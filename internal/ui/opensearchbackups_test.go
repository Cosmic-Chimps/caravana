package ui

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 061: a service backed up with the opensearch engine names its snapshot folder, says it
// keeps running, and its runs carry what the restore confirmation says.
func TestBackupsTabOpenSearchRow(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	done := started.Add(40 * time.Second)
	const folder = "/usr/share/opensearch/snapshots"
	a := app.App{ID: "search", Services: []app.Service{{Name: "os", Image: "opensearchproject/opensearch:2.19.1",
		Ports:   []app.Port{{Name: "api", ContainerPort: 9200}},
		Volumes: []app.Volume{{Name: "snaps", Path: folder}},
		Backup:  &app.Backup{Engine: app.BackupOpenSearch, Repository: folder}}}}
	sum := service.AppSummary{AppRecord: store.AppRecord{App: a}}
	backups := []store.BackupRecord{{AppID: "search", ID: "os-1", Service: "os", Engine: app.BackupOpenSearch,
		Status: store.BackupSucceeded, Bytes: 4096, StartedAt: started, FinishedAt: &done}}
	dest := []store.BackupDestination{{Name: "main", Bucket: "backups"}}

	tab := backupsTabOf(now, sum, backups, nil, nil, dest, backupsTab{})
	if len(tab.Rows) != 1 || len(tab.Gaps) != 0 {
		t.Fatalf("rows = %+v, gaps = %+v", tab.Rows, tab.Gaps)
	}
	row := tab.Rows[0]
	if !row.OpenSearch || row.Volume || row.SQLite || !row.KeepsRunning() || row.What != "OpenSearch · "+folder {
		t.Errorf("row = OpenSearch %v, Volume %v, SQLite %v, What %q", row.OpenSearch, row.Volume, row.SQLite, row.What)
	}
	if len(row.Runs) != 1 || !row.Runs[0].OpenSearch || !row.Runs[0].CanRestore || row.Runs[0].Stopped != "" {
		t.Errorf("run = %+v", row.Runs)
	}
	if got := builtinCommand(app.BackupOpenSearch, "backups"); got != "opensearch snapshot → backups" {
		t.Errorf("builtinCommand = %q", got)
	}
	if got := engineText(*a.Services[0].Backup); got != "opensearch "+folder {
		t.Errorf("engineText = %q", got)
	}
}

// openSearchApp is a cluster with a port and a writable volume, which the policy form can offer
// the opensearch engine to.
func openSearchApp() app.App {
	return app.App{ID: "search", Name: "Search", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
		Name: "os", Image: "traefik/whoami:latest", Health: &app.HealthCheck{},
		Ports:   []app.Port{{Name: "api", ContainerPort: 9200}},
		Volumes: []app.Volume{{Name: "snaps", Path: "/usr/share/opensearch/snapshots"}},
		Backup:  &app.Backup{Engine: app.BackupVolume, Keep: 3}}}}
}

func openSearchEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	if resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
		"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {destSecret}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /settings/backups = %d:\n%s", resp.StatusCode, text(body))
	}
	rec, err := e.Svc.Deploy(context.Background(), openSearchApp(), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	return e
}

// A service with a port and a writable volume can be changed to an OpenSearch snapshot: the form has
// its choice and the Snapshot folder input, and the folder is read only for that engine.
func TestBackupsChangeThePolicyToOpenSearch(t *testing.T) {
	e := openSearchEnv(t)
	body := e.pageOf(t, "/apps/search/backups")
	for _, want := range []string{`<option value="opensearch">OpenSearch or Elasticsearch snapshot (the service keeps running)</option>`,
		`name="repository"`, `id="chg-repo-os"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q:\n%s", want, text(body))
		}
	}

	resp, refused := e.postForm(t, "/apps/search/backups/policy/change", e.changeForm(t, "search", "os",
		url.Values{"engine": {"opensearch"}, "repository": {"/elsewhere/snaps"}, "keep": {"3"}}))
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(refused, `id="chg-repo-os-error"`) {
		t.Errorf("a folder outside the volume = %d, want the form with the error at the folder:\n%s", resp.StatusCode, text(refused))
	}
	if !strings.Contains(refused, `name="repository" value="/elsewhere/snaps"`) {
		t.Error("the refused form lost what was typed")
	}

	// A stale folder in a volume change is ignored: the folder belongs to the OpenSearch engine alone.
	resp, _ = e.postForm(t, "/apps/search/backups/policy/change", e.changeForm(t, "search", "os",
		url.Values{"engine": {"volume"}, "repository": {"/usr/share/opensearch/snapshots"}, "keep": {"3"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a volume change with a folder typed = %d, want 303", resp.StatusCode)
	}
	e.waitDeploy(t, e.deployments(t, "search")[0].Deployment.ID)

	resp, _ = e.postForm(t, "/apps/search/backups/policy/change", e.changeForm(t, "search", "os",
		url.Values{"engine": {"opensearch"}, "repository": {" /usr/share/opensearch/snapshots/run "}, "keep": {"3"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a change to OpenSearch = %d, want 303", resp.StatusCode)
	}
	b := e.storedApp(t, "search").Services[0].Backup
	if b == nil || b.Engine != app.BackupOpenSearch || b.Repository != "/usr/share/opensearch/snapshots/run" {
		t.Fatalf("the stored policy = %+v, want opensearch on /usr/share/opensearch/snapshots/run", b)
	}

	// Back on the tab: the row names the folder, the form starts on it, and Configure lists the engine and folder.
	e.waitDeploy(t, e.deployments(t, "search")[0].Deployment.ID)
	body = e.pageOf(t, "/apps/search/backups")
	got := text(body)
	for _, want := range []string{"OpenSearch · /usr/share/opensearch/snapshots/run", "The service keeps running."} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(body, `name="repository" value="/usr/share/opensearch/snapshots/run"`) {
		t.Error("the form doesn't start on the stored folder")
	}
	if cfg := text(e.pageOf(t, "/apps/search/configure")); !strings.Contains(cfg, "OpenSearch snapshot of /usr/share/opensearch/snapshots/run, every day") {
		t.Errorf("Configure's Overview doesn't name the engine and folder:\n%s", cfg)
	}
}

// A service with no port isn't offered the engine: the cluster is reached over its port.
func TestBackupsChangeThePolicyNoOpenSearchWithoutPort(t *testing.T) {
	e := volumeBackupsEnv(t)
	body := e.pageOf(t, "/apps/kuma/backups")
	if strings.Contains(body, `value="opensearch"`) || strings.Contains(body, `name="repository"`) {
		t.Error("the form offers OpenSearch to a service with no port")
	}
}

// The Compose form (New app) takes the engine and its folder; a folder with another engine is refused at it.
func TestComposeDraftOpenSearch(t *testing.T) {
	post := overlayPost("x", []string{"os"}, map[string]string{
		"svc.os.backupEngine": "opensearch", "svc.os.backupRepository": " /usr/share/opensearch/snapshots ", "svc.os.backupKeep": "3",
	})
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatal("parse refused an opensearch backup")
	}
	want := &app.Backup{Engine: "opensearch", Repository: "/usr/share/opensearch/snapshots", Keep: 3}
	if got := d.request().Services["os"].Backup; !reflect.DeepEqual(got, want) {
		t.Errorf("opensearch backup = %+v, want %+v", got, want)
	}

	for name, inputs := range map[string]map[string]string{
		"a folder with another engine": {"svc.os.backupEngine": "volume", "svc.os.backupRepository": "/data/snaps"},
		"a folder with no engine":      {"svc.os.backupRepository": "/data/snaps"},
	} {
		d, ok := parseComposeDraft(overlayPost("x", []string{"os"}, inputs))
		if ok {
			t.Errorf("%s: parse accepted it", name)
			continue
		}
		os, _ := d.service("os")
		if name == "a folder with another engine" && !strings.Contains(os.BackupRepository.Error.String(), "Only the OpenSearch engine") {
			t.Errorf("%s: the folder's error = %q", name, os.BackupRepository.Error.String())
		}
		if name == "a folder with no engine" && !strings.Contains(os.BackupEngine.Error.String(), "Choose an engine") {
			t.Errorf("%s: the engine's error = %q", name, os.BackupEngine.Error.String())
		}
	}
}

// The preview offers the engine and the Snapshot folder input for a service with a volume, and a
// folder the model refuses is drawn at that input.
func TestComposeFormOpenSearchInputs(t *testing.T) {
	e := newConsoleEnv(t)
	e.Reg.AddIndex("opensearchproject/opensearch", "2.19.1", otherDigest)
	file := "services:\n  os:\n    image: opensearchproject/opensearch:2.19.1\n    ports:\n      - 9200\n    volumes:\n      - snaps:/usr/share/opensearch/snapshots\nvolumes:\n  snaps:\n"
	resp, body := e.previewNew(t, "search", file)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
	for _, want := range []string{`<option value="opensearch"`, "OpenSearch or Elasticsearch snapshot (the service keeps running)",
		`name="svc.os.backupRepository"`, "Snapshot folder"} {
		if !strings.Contains(body, want) {
			t.Errorf("the Compose form lacks %q", want)
		}
	}
}

const (
	osSecretSentinel = "s3cret-sentinel-061-secret"
	osPlainSentinel  = "s3cret-sentinel-061-plain"
)

// An OpenSearch service whose admin password is a secret, and one whose password is a plain
// variable: the secret is on no page, answer or the log, and the plain one on none but Configure,
// which draws plain variables by design (014).
func TestOpenSearchBackupsShowNoPassword(t *testing.T) {
	e := newConsoleEnv(t)
	e.Reg.AddIndex("opensearchproject/opensearch", "2.19.1", otherDigest)
	if resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
		"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {destSecret}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /settings/backups = %d:\n%s", resp.StatusCode, text(body))
	}
	svc := func(name, folder string, env []app.EnvVar, secrets []app.Secret) app.Service {
		return app.Service{Name: name, Image: "opensearchproject/opensearch:2.19.1", Health: &app.HealthCheck{},
			Ports:   []app.Port{{Name: "api", ContainerPort: 9200}},
			Volumes: []app.Volume{{Name: name + "-snaps", Path: folder}},
			Env:     append([]app.EnvVar{{Name: "path.repo", Value: folder}}, env...), Secrets: secrets,
			Backup: &app.Backup{Engine: app.BackupOpenSearch, Repository: folder}}
	}
	e.deploy(t, app.App{ID: "search", Name: "Search", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
		svc("one", "/snaps/one", nil, []app.Secret{{Name: "OPENSEARCH_INITIAL_ADMIN_PASSWORD", Value: new(osSecretSentinel)}}),
		svc("two", "/snaps/two", []app.EnvVar{{Name: "OPENSEARCH_INITIAL_ADMIN_PASSWORD", Value: osPlainSentinel}}, nil),
	}})

	everywhere := []string{osSecretSentinel, osPlainSentinel}
	for _, p := range []string{"/apps/search/backups", "/apps/search/compose", "/apps/search/deployments", "/apps/search",
		"/apps", "/schedules", "/parts/apps/search/deployments", "/parts/activity", "/parts/attention"} {
		resp, body := e.get(t, p)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", p, resp.StatusCode)
		}
		for _, s := range everywhere {
			if strings.Contains(body, s) {
				t.Errorf("GET %s draws %q", p, s)
			}
		}
	}
	_, body := e.get(t, "/apps/search/configure")
	if strings.Contains(body, osSecretSentinel) {
		t.Error("Configure draws the secret")
	}
	if logs := e.Logs.String(); containsAny(logs, everywhere) {
		t.Errorf("the log has a password:\n%s", logs)
	}
	if got := text(e.pageOf(t, "/apps/search/backups")); !strings.Contains(got, "one OpenSearch · /snaps/one") || !strings.Contains(got, "two OpenSearch · /snaps/two") {
		t.Errorf("the tab doesn't name both rows:\n%s", got)
	}

	// The deploy of a plain password warns, on the deployments page, without the value.
	var warned bool
	for _, d := range e.deployments(t, "search") {
		for _, w := range d.Warnings {
			if strings.Contains(w.Message, "OPENSEARCH_INITIAL_ADMIN_PASSWORD") {
				warned = true
			}
			if containsAny(w.Message, everywhere) {
				t.Errorf("a warning holds a password: %s", w.Message)
			}
		}
	}
	if !warned {
		t.Error("no deploy warning names the plain password variable")
	}
}
