package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 046: the mysql and mongodb dump engines read like Postgres in the console.

func TestDumpEngineOfImage(t *testing.T) {
	for _, tc := range []struct {
		image, engine, name string
	}{
		{"postgres:17", app.BackupPostgres, "PostgreSQL"},
		{"mysql:8.4", app.BackupMySQL, "MySQL"},
		{"mariadb:11.4", app.BackupMySQL, "MariaDB"},
		{"docker.io/library/mariadb@sha256:" + strings.Repeat("a", 64), app.BackupMySQL, "MariaDB"},
		{"ghcr.io/acme/mysql:8", app.BackupMySQL, "MySQL"},
		{"mongo:8.0", app.BackupMongoDB, "MongoDB"},
		{"quay.io/acme/mongo", app.BackupMongoDB, "MongoDB"},
		{"mongo-express:1", "", ""},
		{"percona/percona-server:8", "", ""},
		{"traefik/whoami", "", ""},
		{"Not A Ref!!", "", ""},
	} {
		engine, name, ok := dumpEngineOfImage(tc.image)
		if engine != tc.engine || name != tc.name || ok != (tc.engine != "") {
			t.Errorf("dumpEngineOfImage(%q) = %q, %q, %v; want %q, %q", tc.image, engine, name, ok, tc.engine, tc.name)
		}
	}
}

func TestDumpName(t *testing.T) {
	for _, tc := range []struct{ engine, image, want string }{
		{app.BackupPostgres, "postgres:17", "PostgreSQL"},
		{app.BackupMySQL, "mariadb:11", "MariaDB"},
		{app.BackupMySQL, "mysql:8", "MySQL"},
		{app.BackupMySQL, "ghcr.io/acme/db:1", "MySQL"}, // a custom image: the engine's name
		{app.BackupMongoDB, "mongo:8", "MongoDB"},
	} {
		if got := dumpName(tc.engine, tc.image); got != tc.want {
			t.Errorf("dumpName(%q, %q) = %q, want %q", tc.engine, tc.image, got, tc.want)
		}
	}
}

func TestBuiltinCommandDumps(t *testing.T) {
	for engine, want := range map[string]string{
		app.BackupPostgres: "pg_dump → backups",
		app.BackupMySQL:    "mysqldump → backups",
		app.BackupMongoDB:  "mongodump → backups",
	} {
		if got := builtinCommand(engine, "backups"); got != want {
			t.Errorf("builtinCommand(%q) = %q, want %q", engine, got, want)
		}
	}
	if got := builtinCommand(app.BackupMongoDB, ""); got != "mongodump, no destination set" {
		t.Errorf("without a destination = %q", got)
	}
}

// The tab names the database in a gap's meta and in a row's What.
func TestBackupsTabNamesTheDump(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	port := func(n int) []app.Port { return []app.Port{{Name: "db", ContainerPort: n}} }
	a := app.App{ID: "stack", Services: []app.Service{
		{Name: "maria", Image: "mariadb:11.4", Ports: port(3306)},
		{Name: "my", Image: "mysql:8.4", Ports: port(3306)},
		{Name: "mongo", Image: "mongo:8.0", Ports: port(27017)},
		{Name: "pg", Image: "postgres:17", Ports: port(5432)},
		{Name: "backed", Image: "mariadb:11.4", Ports: port(3306), Health: &app.HealthCheck{}, Backup: &app.Backup{Engine: app.BackupMySQL}},
		{Name: "docs", Image: "mongo:8.0", Ports: port(27017), Health: &app.HealthCheck{}, Backup: &app.Backup{Engine: app.BackupMongoDB}},
	}}
	tab := backupsTabOf(now, service.AppSummary{AppRecord: store.AppRecord{App: a}}, nil, nil, nil, []store.BackupDestination{{Name: "main", Bucket: "b"}}, backupsTab{})
	gaps := map[string]backupGap{}
	for _, g := range tab.Gaps {
		gaps[g.Service] = g
	}
	for svc, want := range map[string]struct{ engine, meta string }{
		"maria": {app.BackupMySQL, "MariaDB · 0 copies kept"},
		"my":    {app.BackupMySQL, "MySQL · 0 copies kept"},
		"mongo": {app.BackupMongoDB, "MongoDB · 0 copies kept"},
		"pg":    {app.BackupPostgres, "PostgreSQL · 0 copies kept"},
	} {
		if g := gaps[svc]; g.Engine != want.engine || g.Meta != want.meta {
			t.Errorf("gap %s = engine %q, meta %q; want %q, %q", svc, g.Engine, g.Meta, want.engine, want.meta)
		}
	}
	whats := map[string]string{}
	for _, r := range tab.Rows {
		whats[r.Name] = r.What
		if r.Volume || r.SQLite {
			t.Errorf("row %s is marked as a volume or sqlite backup", r.Name)
		}
	}
	if whats["backed"] != "MariaDB" || whats["docs"] != "MongoDB" {
		t.Errorf("What = %v", whats)
	}
}

func TestKindOfDumpEngines(t *testing.T) {
	custom := app.App{ID: "x", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
		{Name: "db", Image: "ghcr.io/acme/db:1", Backup: &app.Backup{Engine: app.BackupMongoDB}}}}
	if got := kindOf(custom, false); got != kindDatabase {
		t.Errorf("a mongodb backup on a custom image = %q, want database", got)
	}
	custom.Services[0].Backup.Engine = app.BackupMySQL
	if got := kindOf(custom, false); got != kindDatabase {
		t.Errorf("a mysql backup on a custom image = %q, want database", got)
	}
	mongo := app.App{ID: "x", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{Name: "db", Image: "mongo:8"}}}
	if got := kindOf(mongo, false); got != kindDatabase {
		t.Errorf("a mongo image = %q, want database", got)
	}
}

func TestComposeDraftDumpEngines(t *testing.T) {
	for _, engine := range []string{app.BackupMySQL, app.BackupMongoDB} {
		d, ok := parseComposeDraft(overlayPost("x", []string{"db"}, map[string]string{
			"svc.db.backupEngine": engine, "svc.db.backupSchedule": "never", "svc.db.backupKeep": "4",
		}))
		if !ok {
			t.Fatalf("%s: parse refused it", engine)
		}
		got := d.request().Services["db"].Backup
		if got == nil || got.Engine != engine || got.Schedule != "never" || got.Keep != 4 {
			t.Errorf("%s: backup = %+v", engine, got)
		}
	}
}

// The Compose form offers the dump engines, and the Backups tab's "Back up
// daily" form on a MariaDB or MongoDB service sets the right engine.
func TestDumpEnginesInTheConsole(t *testing.T) {
	e := dumpEnv(t)
	resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
		"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {destSecret}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /settings/backups = %d:\n%s", resp.StatusCode, text(body))
	}
	e.deploy(t, app.App{ID: "stack", Name: "Stack", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
		{Name: "maria", Image: "mariadb:11.4", Ports: []app.Port{{Name: "db", ContainerPort: 3306}}, Health: &app.HealthCheck{},
			Secrets: []app.Secret{{Name: "MARIADB_ROOT_PASSWORD", Value: new("pw")}}},
		{Name: "docs", Image: "mongo:8.0", Ports: []app.Port{{Name: "db", ContainerPort: 27017}}, Health: &app.HealthCheck{},
			Secrets: []app.Secret{{Name: "MONGO_INITDB_ROOT_USERNAME", Value: new("admin")}, {Name: "MONGO_INITDB_ROOT_PASSWORD", Value: new("pw")}}},
	}})

	_, body = e.get(t, "/apps/stack/backups")
	got := text(body)
	for _, want := range []string{"MariaDB · 0 copies kept", "MongoDB · 0 copies kept", "Back up daily"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}

	revision := e.revisionOf(t, "stack")
	resp, _ = e.postForm(t, "/apps/stack/backups/policy", url.Values{"revision": {revision}, "service": {"maria"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("Back up daily on maria = %d", resp.StatusCode)
	}
	if b := e.storedApp(t, "stack").Services[0].Backup; b == nil || b.Engine != app.BackupMySQL {
		t.Errorf("maria's backup = %+v, want the mysql engine", b)
	}
	_, body = e.get(t, "/apps/stack/backups")
	if got := text(body); !strings.Contains(got, "maria MariaDB") {
		t.Errorf("the row doesn't name MariaDB:\n%s", got)
	}

	// The Compose form offers the backup inputs, with both new engines, for the
	// database images only.
	resp, body = e.previewNew(t, "dbs", "services:\n  maria:\n    image: mariadb:11.4\n  docs:\n    image: mongo:8.0\n  web:\n    image: traefik/whoami:latest\n")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d:\n%s", resp.StatusCode, text(body))
	}
	for _, want := range []string{`name="svc.maria.backupEngine"`, `name="svc.docs.backupEngine"`, `<option value="mysql"`, `<option value="mongodb"`,
		"MySQL or MariaDB (the service keeps running)", "MongoDB (the service keeps running)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the Compose form lacks %q", want)
		}
	}
	if strings.Contains(body, `name="svc.web.backupEngine"`) {
		t.Error("the Compose form offers a backup for a service that isn't a database")
	}
}

const (
	dumpSecretSentinel = "s3cret-sentinel-046-secret"
	dumpPlainSentinel  = "s3cret-sentinel-046-plain"
)

// A mysql and a mongodb service whose passwords are a secret and a plain
// variable: no password is on the Backups tab, Configure's secrets, the Compose
// form, the deployments page, the apps list or the log. (Plain variables are
// shown on Configure by design, so the plain sentinel is searched for
// everywhere else.)
func TestDumpBackupsShowNoPassword(t *testing.T) {
	e := dumpEnv(t)
	resp, body := e.postForm(t, "/settings/backups", url.Values{"endpoint": {"http://10.0.0.5:9000"}, "region": {"us-east-1"},
		"bucket": {"backups"}, "accessKeyId": {"AK"}, "secretAccessKey": {destSecret}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /settings/backups = %d:\n%s", resp.StatusCode, text(body))
	}
	port := func(n int) []app.Port { return []app.Port{{Name: "db", ContainerPort: n}} }
	e.deploy(t, app.App{ID: "stack", Name: "Stack", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
		{Name: "maria", Image: "mariadb:11.4", Ports: port(3306), Health: &app.HealthCheck{},
			Secrets: []app.Secret{{Name: "MARIADB_ROOT_PASSWORD", Value: new(dumpSecretSentinel)}},
			Backup:  &app.Backup{Engine: app.BackupMySQL}},
		{Name: "mysql", Image: "mysql:8.4", Ports: port(3306), Health: &app.HealthCheck{},
			Env:    []app.EnvVar{{Name: "MYSQL_ROOT_PASSWORD", Value: dumpPlainSentinel}},
			Backup: &app.Backup{Engine: app.BackupMySQL}},
		{Name: "docs", Image: "mongo:8.0", Ports: port(27017), Health: &app.HealthCheck{},
			Env:     []app.EnvVar{{Name: "MONGO_INITDB_ROOT_USERNAME", Value: "admin"}},
			Secrets: []app.Secret{{Name: "MONGO_INITDB_ROOT_PASSWORD", Value: new(dumpSecretSentinel + "-mongo")}},
			Backup:  &app.Backup{Engine: app.BackupMongoDB}},
		{Name: "docs2", Image: "mongo:8.0", Ports: port(27017), Health: &app.HealthCheck{},
			Env: []app.EnvVar{{Name: "MONGO_INITDB_ROOT_USERNAME", Value: "admin"},
				{Name: "MONGO_INITDB_ROOT_PASSWORD", Value: dumpPlainSentinel + "-mongo"}},
			Backup: &app.Backup{Engine: app.BackupMongoDB}},
	}})

	// Back up now on each, so the records and the run rows exist.
	for _, svc := range []string{"maria", "mysql", "docs", "docs2"} {
		resp, _ := e.postForm(t, "/apps/stack/backups", url.Values{"service": {svc}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("Back up now on %s = %d", svc, resp.StatusCode)
		}
	}

	everywhere := []string{dumpSecretSentinel, dumpSecretSentinel + "-mongo", dumpPlainSentinel, dumpPlainSentinel + "-mongo"}
	// Configure draws plain variables as they are (014), so it is checked for the secrets only.
	for _, p := range []string{"/apps/stack/backups", "/apps/stack/compose", "/apps/stack/deployments", "/apps/stack",
		"/apps", "/schedules", "/parts/apps/stack/deployments", "/parts/activity", "/parts/attention"} {
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
	_, body = e.get(t, "/apps/stack/configure")
	for _, s := range []string{dumpSecretSentinel, dumpSecretSentinel + "-mongo"} {
		if strings.Contains(body, s) {
			t.Errorf("Configure draws the secret %q", s)
		}
	}
	if logs := e.Logs.String(); containsAny(logs, everywhere) {
		t.Errorf("the log has a password:\n%s", logs)
	}

	_, body = e.get(t, "/apps/stack/backups")
	got := text(body)
	for _, want := range []string{"maria MariaDB", "mysql MySQL", "docs MongoDB", "docs2 MongoDB"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}

	// The deploy of a plain password warns, on the deployments page, without the value.
	recs := e.deployments(t, "stack")
	var warned bool
	for _, d := range recs {
		for _, w := range d.Warnings {
			if strings.Contains(w.Message, "MYSQL_ROOT_PASSWORD") || strings.Contains(w.Message, "MONGO_INITDB_ROOT_PASSWORD") {
				warned = true
			}
			if containsAny(w.Message, everywhere) {
				t.Errorf("a warning holds a password: %s", w.Message)
			}
		}
	}
	if !warned {
		t.Errorf("no deploy warning names a plain password variable: %+v", recs)
	}

	// The records and the SQLite rows of the runs hold no password either.
	backups, _, err := e.Svc.ListBackups(context.Background(), "stack")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, b := range backups {
		lines = append(lines, b.Service+":"+b.Engine+":"+b.Message)
	}
	if all := strings.Join(lines, "\n"); containsAny(all, everywhere) || !strings.Contains(all, ":mysql:") || !strings.Contains(all, ":mongodb:") {
		t.Errorf("backup records:\n%s", all)
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// dumpEnv is a console whose registry knows the database images the tests deploy.
func dumpEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.Reg.AddIndex("library/mariadb", "11.4", otherDigest)
	e.Reg.AddIndex("library/mysql", "8.4", otherDigest)
	e.Reg.AddIndex("library/mongo", "8.0", otherDigest)
	return e
}
