package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// destinationSentinel is a secret key no answer, error, log line or stored row
// may hold.
const destinationSentinel = "s3cret-sentinel-052-key"

// saveDestination saves a destination called name, bucket "<name>-bucket", with
// the given secret key.
func saveDestination(t *testing.T, e *env, name, secret string) store.BackupDestination {
	t.Helper()
	d, err := e.svc.SaveBackupDestination(context.Background(), name, DestinationInput{
		Endpoint: "http://10.0.0.5:9000", Region: "us-east-1", Bucket: name + "-bucket", Prefix: "caravana",
		AccessKeyID: "AK-" + name, SecretAccessKey: &secret})
	if err != nil {
		t.Fatalf("SaveBackupDestination(%s): %v", name, err)
	}
	return d
}

// dbWith is the postgres service of db() with the policy changed by f.
func dbWith(f func(*app.Backup)) app.App {
	a := db()
	f(a.Services[0].Backup)
	return a
}

func TestSaveAndListBackupDestinations(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	if list, err := e.svc.ListBackupDestinations(ctx); err != nil || len(list) != 0 {
		t.Fatalf("list on a new installation = %+v, %v; want none", list, err)
	}
	main := saveDestination(t, e, "main", "SK-main")
	if !main.IsDefault || main.SecretPath != "caravana/backup-destinations/main" {
		t.Errorf("first destination = %+v, want the default with its own secret path", main)
	}
	offsite := saveDestination(t, e, "offsite", "SK-offsite")
	if offsite.IsDefault {
		t.Error("the second destination became the default by itself")
	}
	if got := e.nomad.Variable("caravana/backup-destinations/offsite")["secretAccessKey"]; got != "SK-offsite" {
		t.Errorf("offsite's secret = %q, want it at its own path", got)
	}

	list, err := e.svc.ListBackupDestinations(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "main" || list[1].Name != "offsite" {
		t.Fatalf("list = %+v, %v; want main, offsite", list, err)
	}

	// A save without a secret keeps it; the destination is repointed at the new settings.
	again, err := e.svc.SaveBackupDestination(ctx, "offsite", DestinationInput{
		Endpoint: "http://10.0.0.6:9000", Region: "r", Bucket: "offsite-two", AccessKeyID: "AK2"})
	if err != nil || again.Bucket != "offsite-two" {
		t.Fatalf("save without a secret = %+v, %v", again, err)
	}
	if got := e.nomad.Variable("caravana/backup-destinations/offsite")["secretAccessKey"]; got != "SK-offsite" {
		t.Errorf("offsite's secret = %q, want it kept", got)
	}

	// The refusals name their field.
	secret := "x"
	for _, tt := range []struct {
		name  string
		dest  string
		in    DestinationInput
		field string
	}{
		{"a name that isn't a label", "Off_Site", DestinationInput{Endpoint: "http://x", Region: "r", Bucket: "bkt", AccessKeyID: "AK", SecretAccessKey: &secret}, "name"},
		{"a bad endpoint", "third", DestinationInput{Endpoint: "ftp://x", Region: "r", Bucket: "bkt", AccessKeyID: "AK", SecretAccessKey: &secret}, "endpoint"},
		{"a bad bucket", "third", DestinationInput{Endpoint: "http://x", Region: "r", Bucket: "Bad_Bucket", AccessKeyID: "AK", SecretAccessKey: &secret}, "bucket"},
		{"a new destination without a secret", "third", DestinationInput{Endpoint: "http://x", Region: "r", Bucket: "bkt", AccessKeyID: "AK"}, "secretAccessKey"},
		{"the default made not the default", "main", DestinationInput{Endpoint: "http://x", Region: "r", Bucket: "bkt", AccessKeyID: "AK", Default: new(false)}, "default"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.SaveBackupDestination(ctx, tt.dest, tt.in)
			if got := fieldOf(err); got != tt.field {
				t.Errorf("field = %q, want %q (%v)", got, tt.field, err)
			}
		})
	}
	if _, err := e.store.GetBackupDestinationNamed(ctx, "third"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused destination was stored: %v", err)
	}

	// Default true moves the default; the old one is no longer it.
	if _, err := e.svc.SaveBackupDestination(ctx, "offsite", DestinationInput{
		Endpoint: "http://10.0.0.6:9000", Region: "r", Bucket: "offsite-two", AccessKeyID: "AK2", Default: new(true)}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.svc.GetBackupDestination(ctx); d.Name != "offsite" {
		t.Errorf("default = %q, want offsite", d.Name)
	}
}

// The old single-destination call reads and writes the default.
func TestSetBackupDestinationWritesTheDefault(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	in := DestinationInput{Endpoint: "http://10.0.0.5:9000", Region: "r", Bucket: "backups", AccessKeyID: "AK", SecretAccessKey: new("SK")}
	d, err := e.svc.SetBackupDestination(ctx, in)
	if err != nil || d.Name != "main" || !d.IsDefault {
		t.Fatalf("first SetBackupDestination = %+v, %v; want main, the default", d, err)
	}
	saveDestination(t, e, "offsite", "SK2")
	if err := e.svc.SetDefaultBackupDestination(ctx, "offsite"); err != nil {
		t.Fatal(err)
	}
	in.Bucket = "changed"
	if d, err = e.svc.SetBackupDestination(ctx, in); err != nil || d.Name != "offsite" || d.Bucket != "changed" {
		t.Errorf("SetBackupDestination now = %+v, %v; want it to write offsite, the default", d, err)
	}
	if main, _ := e.store.GetBackupDestinationNamed(ctx, "main"); main.Bucket != "backups" {
		t.Errorf("main's bucket = %q, want it untouched", main.Bucket)
	}
}

func TestDeleteBackupDestination(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	saveDestination(t, e, "main", "SK-main")
	saveDestination(t, e, "offsite", "SK-offsite")
	saveDestination(t, e, "cold", "SK-cold")

	if err := e.svc.DeleteBackupDestination(ctx, "ghost"); !errors.Is(err, ErrDestinationNotFound) {
		t.Errorf("delete an unknown destination = %v, want ErrDestinationNotFound", err)
	}
	if err := e.svc.DeleteBackupDestination(ctx, "main"); !errors.Is(err, ErrDestinationIsDefault) {
		t.Errorf("delete the default = %v, want ErrDestinationIsDefault", err)
	}

	// A policy that copies to it, and a project that has it as its default, use it.
	deployHealthy(t, e, dbWith(func(b *app.Backup) { b.CopyTo = []string{"offsite"} }))
	if _, err := e.svc.CreateProject(ctx, "acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetProjectBackupDestination(ctx, "acme", "cold"); err != nil {
		t.Fatal(err)
	}
	var inUse *DestinationInUseError
	err := e.svc.DeleteBackupDestination(ctx, "offsite")
	if !errors.As(err, &inUse) || len(inUse.Users) != 1 || inUse.Users[0] != "whoami/web (copy)" ||
		!strings.Contains(err.Error(), "whoami/web (copy)") {
		t.Errorf("delete a destination a policy copies to = %v, want it named", err)
	}
	if err := e.svc.DeleteBackupDestination(ctx, "cold"); !errors.As(err, &inUse) || inUse.Users[0] != "project acme" {
		t.Errorf("delete a project's default = %v, want the project named", err)
	}
	if got := e.nomad.Variable("caravana/backup-destinations/offsite"); got == nil {
		t.Error("a refused delete removed the secret")
	}

	// Free again, it goes with its secret key.
	if _, err := e.svc.SetProjectBackupDestination(ctx, "acme", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteBackupDestination(ctx, "cold"); err != nil {
		t.Fatalf("delete a free destination: %v", err)
	}
	if e.nomad.Variable("caravana/backup-destinations/cold") != nil {
		t.Error("the secret key of a removed destination is still stored")
	}
	if _, err := e.store.GetBackupDestinationNamed(ctx, "cold"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cold is still stored: %v", err)
	}

	// The error lists at most five users, then "and N more".
	long := &DestinationInUseError{Name: "x", Users: []string{"a/1", "a/2", "a/3", "a/4", "a/5", "a/6", "a/7"}}
	if got := long.Error(); !strings.Contains(got, "a/5") || strings.Contains(got, "a/6") || !strings.HasSuffix(got, "and 2 more") {
		t.Errorf("long error = %q", got)
	}
}

// A destination moved to the default (and the old default deleted) keeps the
// secret key of the migrated destination where it was.
func TestMigratedDestinationKeepsItsSecretPath(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	// Make a destination as migration 0042 leaves one: a row whose secret is at the old path.
	if err := e.store.SaveBackupDestination(ctx, store.BackupDestination{Name: "main", Endpoint: "http://10.0.0.5:9000",
		Region: "r", Bucket: "backups", AccessKeyID: "AK", SecretPath: "caravana/backup-destination"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.deployer.SaveBackupSecret(ctx, "caravana/backup-destination", "SK-old"); err != nil {
		t.Fatal(err)
	}
	dest, ok, err := e.svc.backupDestination(ctx)
	if err != nil || !ok || dest.Name != "main" || dest.SecretAccessKey != "SK-old" {
		t.Fatalf("backupDestination = %+v, %v, %v; want main with the old secret", dest, ok, err)
	}
	// A save without a secret keeps the key at the old path.
	if _, err := e.svc.SaveBackupDestination(ctx, "main", DestinationInput{Endpoint: "http://10.0.0.5:9000", Region: "r", Bucket: "backups2", AccessKeyID: "AK"}); err != nil {
		t.Fatal(err)
	}
	if got := e.nomad.Variable("caravana/backup-destination")["secretAccessKey"]; got != "SK-old" {
		t.Errorf("secret = %q, want it kept at the old path", got)
	}
	if e.nomad.Variable("caravana/backup-destinations/main") != nil {
		t.Error("a second copy of the key was written at the new path")
	}
	// Removing it (once another is the default) deletes the old variable.
	saveDestination(t, e, "other", "SK")
	if err := e.svc.SetDefaultBackupDestination(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteBackupDestination(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if e.nomad.Variable("caravana/backup-destination") != nil {
		t.Error("the migrated destination's secret key is still stored")
	}
}

func TestBackupPlacesResolution(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	if _, _, err := e.svc.backupPlaces(ctx, "whoami", app.Backup{}); !errors.Is(err, ErrNoBackupDestination) {
		t.Fatalf("backupPlaces with no destination = %v, want ErrNoBackupDestination", err)
	}
	saveDestination(t, e, "main", "SK")
	saveDestination(t, e, "acme", "SK")
	saveDestination(t, e, "own", "SK")
	saveDestination(t, e, "offsite", "SK")

	place := func(id string, b app.Backup) string {
		t.Helper()
		primary, _, err := e.svc.backupPlaces(ctx, id, b)
		if err != nil {
			t.Fatal(err)
		}
		return primary
	}
	if got := place("whoami", app.Backup{}); got != "main" {
		t.Errorf("no destination named, no project default: %q, want the installation's, main", got)
	}
	if _, err := e.svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetProjectBackupDestination(ctx, "shop", "acme"); err != nil {
		t.Fatal(err)
	}
	shopID := "shop--production--web"
	if got := place(shopID, app.Backup{}); got != "acme" {
		t.Errorf("in a project with a default: %q, want acme", got)
	}
	if got := place(shopID, app.Backup{Destination: "own"}); got != "own" {
		t.Errorf("a policy's own destination: %q, want own", got)
	}
	if got := place("whoami", app.Backup{}); got != "main" {
		t.Errorf("Default's app: %q, want main: the project default is shop's only", got)
	}
	if _, copies, _ := e.svc.backupPlaces(ctx, "whoami", app.Backup{CopyTo: []string{"offsite"}}); len(copies) != 1 || copies[0] != "offsite" {
		t.Errorf("copies = %v, want offsite", copies)
	}
}

func TestCheckBackupsRefusals(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	_, err := e.svc.Deploy(ctx, db(), app.TriggerManual)
	if got := fieldOf(err); got != "services[0].backup" || !strings.Contains(err.Error(), "no backup destination is set") {
		t.Fatalf("no destination at all: %v, want the field services[0].backup and today's words", err)
	}
	saveDestination(t, e, "main", "SK")
	saveDestination(t, e, "offsite", "SK")

	for _, tt := range []struct {
		name   string
		policy func(*app.Backup)
		field  string
	}{
		{"an unknown destination", func(b *app.Backup) { b.Destination = "ghost" }, "services[0].backup.destination"},
		{"an unknown copy", func(b *app.Backup) { b.CopyTo = []string{"offsite", "ghost"} }, "services[0].backup.copyTo[1]"},
		{"a copy where the backups already go, by default", func(b *app.Backup) { b.CopyTo = []string{"main"} }, "services[0].backup.copyTo[0]"},
		{"a copy where the backups already go, named", func(b *app.Backup) { b.Destination = "offsite"; b.CopyTo = []string{"offsite"} }, "services[0].backup.copyTo[0]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.Deploy(ctx, dbWith(tt.policy), app.TriggerManual)
			if got := fieldOf(err); got != tt.field {
				t.Errorf("field = %q, want %q (%v)", got, tt.field, err)
			}
		})
	}
	if !strings.Contains(func() string {
		_, err := e.svc.Deploy(ctx, dbWith(func(b *app.Backup) { b.Destination = "ghost" }), app.TriggerManual)
		return err.Error()
	}(), `no backup destination is named "ghost"; add it in Settings › Backup storage`) {
		t.Error("an unknown destination isn't said in the spec's words")
	}
	if _, err := e.store.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused deploy stored the app: %v", err)
	}

	// A good policy deploys, its job points at the destination it names.
	deployHealthy(t, e, dbWith(func(b *app.Backup) { b.Destination = "offsite"; b.CopyTo = []string{"main"} }))
	v := e.nomad.Variable("nomad/jobs/whoami_web_backup")
	if v["BACKUP_DESTINATION"] != "offsite" || v["BACKUP_PATH"] != "offsite-bucket/caravana/whoami/web" {
		t.Errorf("backup variable = %v, want it pointed at offsite", v)
	}
}

// Each service of an app goes to its own destination.
func TestSyncBackupsUsesEachServicesDestination(t *testing.T) {
	e := newEnv(t)
	saveDestination(t, e, "main", "SK-main")
	saveDestination(t, e, "client", "SK-client")
	a := db()
	other := a.Services[0]
	other.Name = "web2"
	other.Domains = nil
	other.Backup = &app.Backup{Engine: app.BackupPostgres, Keep: 2, Destination: "client"}
	a.Services = append(a.Services, other)
	deployHealthy(t, e, a)

	first, second := e.nomad.Variable("nomad/jobs/whoami_web_backup"), e.nomad.Variable("nomad/jobs/whoami_web2_backup")
	if first["BACKUP_DESTINATION"] != "main" || first["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK-main" {
		t.Errorf("web's variable = %v, want main", first)
	}
	if second["BACKUP_DESTINATION"] != "client" || second["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK-client" ||
		second["BACKUP_PATH"] != "client-bucket/caravana/whoami/web2" {
		t.Errorf("web2's variable = %v, want client", second)
	}
}

// The dump run's record says where it went: the destination its job's variable
// named, else where the policy sends backups now.
func TestBackupRecordsTheDestination(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	saveDestination(t, e, "main", "SK-main")
	saveDestination(t, e, "offsite", "SK-offsite")
	deployHealthy(t, e, db())

	first, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	if first.Destination != "main" {
		t.Errorf("a started run's destination = %q, want main", first.Destination)
	}
	e.nomad.FinishRun(first.JobID, true, "", "caravana-backup key=main-bucket/caravana/whoami/web/1.dump bytes=10 dest=offsite\n", "")
	waitFor(t, "the first backup to be recorded", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", first.ID)
		return err == nil && b.Status == store.BackupSucceeded
	})
	if b, _ := e.store.GetBackup(ctx, "whoami", first.ID); b.Destination != "offsite" {
		t.Errorf("recorded destination = %q, want the job's word, offsite", b.Destination)
	}

	second, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	e.nomad.FinishRun(second.JobID, true, "", "caravana-backup key=main-bucket/caravana/whoami/web/2.dump bytes=10\n", "")
	waitFor(t, "the second backup to be recorded", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", second.ID)
		return err == nil && b.Status == store.BackupSucceeded
	})
	if b, _ := e.store.GetBackup(ctx, "whoami", second.ID); b.Destination != "main" {
		t.Errorf("recorded destination of a job that didn't say = %q, want main", b.Destination)
	}

	// A restore records where it read from.
	r, err := e.svc.Restore(ctx, "whoami", second.ID, "")
	if err != nil || r.Destination != "main" {
		t.Errorf("Restore = %+v, %v; want destination main", r, err)
	}
}

func TestRepointAfterChanges(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	saveDestination(t, e, "main", "SK-main")
	saveDestination(t, e, "offsite", "SK-offsite")
	deployHealthy(t, e, db())
	const path = "nomad/jobs/whoami_web_backup"
	if v := e.nomad.Variable(path); v["BACKUP_PATH"] != "main-bucket/caravana/whoami/web" {
		t.Fatalf("variable = %v", v)
	}

	// Saving the destination moves the job to the new settings and the new key,
	// and keeps the credentials.
	secret := "SK-main-2"
	if _, err := e.svc.SaveBackupDestination(ctx, "main", DestinationInput{Endpoint: "http://10.0.0.9:9000", Region: "r",
		Bucket: "moved", AccessKeyID: "AK9", SecretAccessKey: &secret}); err != nil {
		t.Fatal(err)
	}
	v := e.nomad.Variable(path)
	if v["BACKUP_PATH"] != "moved/whoami/web" || v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK-main-2" ||
		v["RCLONE_CONFIG_DEST_ENDPOINT"] != "http://10.0.0.9:9000" || v["BACKUP_DESTINATION"] != "main" || v["PGPASSWORD"] != marker {
		t.Errorf("variable after a save = %v", v)
	}

	// A destination that no service uses leaves the job alone.
	if _, err := e.svc.SaveBackupDestination(ctx, "offsite", DestinationInput{Endpoint: "http://10.0.0.7:9000", Region: "r",
		Bucket: "offsite-2", AccessKeyID: "AK7"}); err != nil {
		t.Fatal(err)
	}
	if v := e.nomad.Variable(path); v["BACKUP_DESTINATION"] != "main" || v["BACKUP_PATH"] != "moved/whoami/web" {
		t.Errorf("variable after saving another destination = %v, want main's", v)
	}

	// A default change points the services that follow the default at it.
	if err := e.svc.SetDefaultBackupDestination(ctx, "offsite"); err != nil {
		t.Fatal(err)
	}
	if v := e.nomad.Variable(path); v["BACKUP_DESTINATION"] != "offsite" || v["BACKUP_PATH"] != "offsite-2/whoami/web" ||
		v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK-offsite" {
		t.Errorf("variable after a default change = %v, want offsite", v)
	}
	if err := e.svc.SetDefaultBackupDestination(ctx, "ghost"); !errors.Is(err, ErrDestinationNotFound) {
		t.Errorf("default change to an unknown destination = %v, want ErrDestinationNotFound", err)
	}

	// A project's default points its services at it, and "" puts them back.
	saveDestination(t, e, "acme", "SK-acme")
	if _, err := e.svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	shop := db()
	shop.ID = "shop--production--whoami"
	shop.Services[0].Domains = nil // whoami.localhost is Default's app's
	deployHealthy(t, e, shop)
	shopPath := "nomad/jobs/shop--production--whoami_web_backup"
	if v := e.nomad.Variable(shopPath); v["BACKUP_DESTINATION"] != "offsite" {
		t.Fatalf("shop's variable = %v, want the installation's default", v)
	}
	if _, err := e.svc.SetProjectBackupDestination(ctx, "shop", "acme"); err != nil {
		t.Fatal(err)
	}
	if v := e.nomad.Variable(shopPath); v["BACKUP_DESTINATION"] != "acme" || v["BACKUP_PATH"] != "acme-bucket/caravana/shop--production--whoami/web" {
		t.Errorf("shop's variable after the project default = %v, want acme", v)
	}
	if v := e.nomad.Variable(path); v["BACKUP_DESTINATION"] != "offsite" {
		t.Errorf("Default's variable moved with shop's default: %v", v)
	}
	if _, err := e.svc.SetProjectBackupDestination(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	if v := e.nomad.Variable(shopPath); v["BACKUP_DESTINATION"] != "offsite" {
		t.Errorf("shop's variable after clearing = %v, want offsite", v)
	}
}

func TestSetProjectBackupDestinationRefusals(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	saveDestination(t, e, "main", "SK")
	saveDestination(t, e, "offsite", "SK")
	if _, err := e.svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetProjectBackupDestination(ctx, "ghost", "main"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("unknown project = %v, want ErrProjectNotFound", err)
	}
	_, err := e.svc.SetProjectBackupDestination(ctx, "shop", "nowhere")
	if got := fieldOf(err); got != "backupDestination" {
		t.Errorf("unknown destination: field %q, want backupDestination", got)
	}
	_, err = e.svc.SetProjectBackupDestination(ctx, "shop", "Not A Label")
	if got := fieldOf(err); got != "backupDestination" {
		t.Errorf("a bad name: field %q, want backupDestination", got)
	}

	// A policy of the project that copies to offsite, with no destination of its own,
	// would copy onto itself under offsite as the project's default.
	copying := db()
	copying.ID = "shop--production--whoami"
	copying.Services[0].Domains = nil
	copying.Services[0].Backup.CopyTo = []string{"offsite"}
	deployHealthy(t, e, copying)
	_, err = e.svc.SetProjectBackupDestination(ctx, "shop", "offsite")
	if got := fieldOf(err); got != "backupDestination" || !strings.Contains(err.Error(), "shop--production--whoami/web") {
		t.Errorf("a copy that would meet its destination: %v, want the service named on backupDestination", err)
	}
	if p, _ := e.svc.Project(ctx, "shop"); p.BackupDestination != "" {
		t.Errorf("a refused change was stored: %q", p.BackupDestination)
	}
	// With its own destination the same policy is fine.
	copying.Services[0].Backup.Destination = "main"
	deployHealthy(t, e, copying)
	if p, err := e.svc.SetProjectBackupDestination(ctx, "shop", "offsite"); err != nil || p.BackupDestination != "offsite" {
		t.Errorf("SetProjectBackupDestination = %+v, %v; want it saved", p, err)
	}
	// The same rule stops the installation's default meeting a copy.
	other := db()
	other.ID = "plain"
	other.Services[0].Domains = nil
	other.Services[0].Backup.CopyTo = []string{"offsite"}
	deployHealthy(t, e, other)
	if err := e.svc.SetDefaultBackupDestination(ctx, "offsite"); fieldOf(err) != "name" {
		t.Errorf("a default that meets a copy = %v, want a field error on name", err)
	}
}

// A destination's secret key is in no error, log line, stored row or file; it is
// only in its Nomad Variable.
func TestDestinationSecretSentinel(t *testing.T) {
	ctx := context.Background()
	e := newTeamEnv(t)
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	saveDestination(t, e.env, "main", "SK-main")
	saveDestination(t, e.env, "offsite", destinationSentinel)
	deployHealthy(t, e.env, dbWith(func(b *app.Backup) { b.CopyTo = []string{"offsite"} }))
	deployHealthy(t, e.env, dbWith(func(b *app.Backup) { b.Destination = "offsite" }))
	rec, err := e.svc.BackUpNow(ctx, "whoami", "web")
	collect(err)
	e.nomad.FinishRun(rec.JobID, false, "upload", "", "rclone: error: AccessDenied: access denied\n")
	waitFor(t, "the failure to be recorded", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", rec.ID)
		return err == nil && b.Status == store.BackupFailed
	})
	_, err = e.svc.SaveBackupDestination(ctx, "offsite", DestinationInput{Endpoint: "ftp://bad", Region: "r", Bucket: "bkt", AccessKeyID: "AK"})
	collect(err)
	_, err = e.svc.SaveBackupDestination(ctx, "third", DestinationInput{Endpoint: "http://x", Region: "r", Bucket: "bkt", AccessKeyID: "AK"})
	collect(err)
	collect(e.svc.DeleteBackupDestination(ctx, "offsite"))
	_, err = e.svc.Deploy(ctx, dbWith(func(b *app.Backup) { b.Destination = "ghost" }), app.TriggerManual)
	collect(err)
	list, err := e.svc.ListBackupDestinations(ctx)
	collect(err)

	for _, err := range errs {
		if strings.Contains(err.Error(), destinationSentinel) {
			t.Errorf("an error holds the secret key: %v", err)
		}
	}
	for _, d := range list {
		if strings.Contains(strings.Join([]string{d.Name, d.Endpoint, d.Bucket, d.Prefix, d.AccessKeyID, d.SecretPath}, " "), destinationSentinel) {
			t.Errorf("a listed destination holds the secret key: %+v", d)
		}
	}
	if strings.Contains(e.logs.String(), destinationSentinel) {
		t.Error("the log holds the secret key")
	}
	for _, row := range storeRows(t, e.dir) {
		if strings.Contains(row, destinationSentinel) {
			t.Errorf("a stored row holds the secret key: %s", row)
		}
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, "caravana.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), destinationSentinel) {
		t.Error("the SQLite file holds the secret key")
	}
	for _, job := range []string{"whoami_web_backup", "whoami"} {
		if j := e.nomad.Job(job); j != nil && strings.Contains(mustMarshal(t, j), destinationSentinel) {
			t.Errorf("job %s holds the secret key", job)
		}
	}
	if got := e.nomad.Variable("nomad/jobs/whoami_web_backup")["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"]; got != destinationSentinel {
		t.Errorf("the backup job's own variable has %q, want the secret key there", got)
	}
}
