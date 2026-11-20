package templates

import (
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// dumpedTemplate is goodTemplate with its database swapped for a MySQL or
// MongoDB one, backed up with that engine (feature 046). env is the db
// service's environment in the Compose file; secrets are what the overlay makes
// secrets.
func dumpedTemplate(engine, image, port, env string, secrets ...string) Template {
	x := goodTemplate()
	db := x.Services["db"]
	db.Backup = &app.Backup{Engine: engine}
	db.Secrets = nil
	for _, name := range secrets {
		value := "{{db_password}}"
		db.Secrets = append(db.Secrets, app.Secret{Name: name, Value: &value})
	}
	x.Services["db"] = db
	start := strings.Index(x.Compose, "  db:\n")
	end := strings.Index(x.Compose, "volumes:\n  data")
	x.Compose = x.Compose[:start] + "  db:\n    image: " + image + "\n    expose: [\"" + port + "\"]\n" + env +
		"    volumes:\n      - data:/data/db\n" + x.Compose[end:]
	return x
}

// Every dump engine's password names are refused as plain env in a template, and
// a secret of the same name passes (D14).
func TestCheckDumpEnginePasswords(t *testing.T) {
	settings := CheckSettings()
	tests := []struct {
		name string
		x    Template
		want string // "" means it passes
	}{
		{"mysql with its passwords as secrets",
			dumpedTemplate(app.BackupMySQL, "mariadb:11.4", "3306", "    environment:\n      MARIADB_DATABASE: demo\n", "MARIADB_USER", "MARIADB_PASSWORD"), ""},
		{"mysql with a plain root password",
			dumpedTemplate(app.BackupMySQL, "mariadb:11.4", "3306", "    environment:\n      MARIADB_ROOT_PASSWORD: plain\n"),
			"services.db.environment.MARIADB_ROOT_PASSWORD"},
		{"mysql with a plain user password",
			dumpedTemplate(app.BackupMySQL, "mysql:8.4", "3306", "    environment:\n      MYSQL_PASSWORD: plain\n", "MYSQL_USER"),
			"services.db.environment.MYSQL_PASSWORD"},
		{"mysql without any login",
			dumpedTemplate(app.BackupMySQL, "mariadb:11.4", "3306", ""), "MARIADB_ROOT_PASSWORD"},
		{"mongodb with its password as a secret",
			dumpedTemplate(app.BackupMongoDB, "mongo:8.0", "27017", "    environment:\n      MONGO_INITDB_ROOT_USERNAME: demo\n", "MONGO_INITDB_ROOT_PASSWORD"), ""},
		{"mongodb with a plain password",
			dumpedTemplate(app.BackupMongoDB, "mongo:8.0", "27017", "    environment:\n      MONGO_INITDB_ROOT_USERNAME: demo\n      MONGO_INITDB_ROOT_PASSWORD: plain\n"),
			"services.db.environment.MONGO_INITDB_ROOT_PASSWORD"},
		{"mongodb without its user",
			dumpedTemplate(app.BackupMongoDB, "mongo:8.0", "27017", "", "MONGO_INITDB_ROOT_PASSWORD"), "MONGO_INITDB_ROOT_USERNAME"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Check(tt.x, settings)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Check: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Check accepted it")
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "demo: ") {
				t.Errorf("error = %q, want the template name and %q", err, tt.want)
			}
		})
	}
}
