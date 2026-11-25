package service

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/notify/smtptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Phase C of the audit log (feature 057), the installation: every setting that holds a secret notes its
// change without the secret, and every method that changes the installation notes one entry.

// noSecret fails when any audit entry or the SQLite file holds one of the secrets.
func noSecret(t *testing.T, st *store.Store, dir string, secrets ...string) {
	t.Helper()
	all := everyEntry(t, st)
	raw, err := os.ReadFile(filepath.Join(dir, "caravana.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if strings.Contains(all, s) {
			t.Errorf("an audit entry holds the secret %q", s)
		}
		if strings.Contains(string(raw), s) {
			t.Errorf("the SQLite file holds the secret %q", s)
		}
	}
}

func TestAuditNotesRegistries(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	got := notedOne(t, e.store, func() {
		if _, err := e.svc.SaveRegistry(ctx, "ghcr.io/acme", "ana", new(registryPassword)); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindRegistry, "ghcr.io/acme")
	if c := change(got.Changes, "password"); c == nil || c.Op != audit.OpAdded || c.From != "" || c.To != "" {
		t.Errorf("a first password = %+v, want added with no value", c)
	}
	if c := change(got.Changes, "username"); c == nil || c.To != "ana" {
		t.Errorf("username = %+v", c)
	}

	// The same user and no password: nothing changed.
	if n := len(noted(t, e.store, func() {
		if _, err := e.svc.SaveRegistry(ctx, "ghcr.io/acme", "ana", nil); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("a save that changed nothing wrote %d entries", n)
	}
	// A new password replaces the old: changed, no value.
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SaveRegistry(ctx, "ghcr.io/acme", "ana", new(registryPassword+"-2")); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindRegistry, "ghcr.io/acme")
	if c := change(got.Changes, "password"); c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
		t.Errorf("a replaced password = %+v", c)
	}
	// A refused save notes nothing.
	if n := len(noted(t, e.store, func() { _, _ = e.svc.SaveRegistry(ctx, "ghcr.io/Bad Path", "ana", new("x")) })); n != 0 {
		t.Errorf("a refused save wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.TestRegistry(ctx, "ghcr.io/acme"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionTested, audit.KindRegistry, "ghcr.io/acme")
	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteRegistry(ctx, "ghcr.io/acme"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindRegistry, "ghcr.io/acme")
	// Deleting what isn't there notes nothing.
	if n := len(noted(t, e.store, func() { _ = e.svc.DeleteRegistry(ctx, "ghcr.io/acme") })); n != 0 {
		t.Errorf("a refused delete wrote %d entries", n)
	}
	noSecret(t, e.store, e.dir, registryPassword, registryPassword+"-2")
}

func TestAuditNotesBackupDestinations(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	got := notedOne(t, e.store, func() { saveDestination(t, e, "main", destinationSentinel) })
	wantEntry(t, got, audit.ActionCreated, audit.KindBackupDestination, "main")
	if c := change(got.Changes, "secretAccessKey"); c == nil || c.Op != audit.OpAdded || c.To != "" {
		t.Errorf("secret key = %+v", c)
	}
	if c := change(got.Changes, "bucket"); c == nil || c.To != "main-bucket" {
		t.Errorf("bucket = %+v", c)
	}
	offsite := notedOne(t, e.store, func() { saveDestination(t, e, "offsite", destinationSentinel+"-offsite") })
	wantEntry(t, offsite, audit.ActionCreated, audit.KindBackupDestination, "offsite")

	// Saving the same fields without a key changes nothing; a new key is "changed".
	same := DestinationInput{Endpoint: "http://10.0.0.5:9000", Region: "us-east-1", Bucket: "offsite-bucket", Prefix: "caravana", AccessKeyID: "AK-offsite"}
	if n := len(noted(t, e.store, func() {
		if _, err := e.svc.SaveBackupDestination(ctx, "offsite", same); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("a save that changed nothing wrote %d entries", n)
	}
	same.Bucket, same.SecretAccessKey = "other-bucket", new(destinationSentinel+"-rotated")
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SaveBackupDestination(ctx, "offsite", same); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindBackupDestination, "offsite")
	if c := change(got.Changes, "secretAccessKey"); c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
		t.Errorf("a replaced key = %+v", c)
	}
	if c := change(got.Changes, "bucket"); c == nil || c.From != "offsite-bucket" || c.To != "other-bucket" {
		t.Errorf("bucket = %+v", c)
	}

	got = notedOne(t, e.store, func() {
		if err := e.svc.SetDefaultBackupDestination(ctx, "offsite"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindBackupDestination, "offsite")
	if c := change(got.Changes, "default"); c == nil || c.From != "main" || c.To != "offsite" {
		t.Errorf("default = %+v", got.Changes)
	}
	if n := len(noted(t, e.store, func() { _ = e.svc.SetDefaultBackupDestination(ctx, "offsite") })); n != 0 {
		t.Errorf("making the default the default wrote %d entries", n)
	}
	// The default can't go; main can.
	if n := len(noted(t, e.store, func() { _ = e.svc.DeleteBackupDestination(ctx, "offsite") })); n != 0 {
		t.Errorf("a refused delete wrote %d entries", n)
	}
	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteBackupDestination(ctx, "main"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindBackupDestination, "main")
	noSecret(t, e.store, e.dir, destinationSentinel, destinationSentinel+"-offsite", destinationSentinel+"-rotated")
}

func TestAuditNotesNotificationChannels(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	slack := "https://hooks.slack.com/services/T000/B000/" + notifySecret
	rcv := newReceiver(t, http.StatusOK)

	var ch CreatedNotificationChannel
	got := notedOne(t, e.store, func() {
		var err error
		if ch, err = e.svc.CreateNotificationChannel(ctx, channelInput(notify.KindSlack, slack)); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindNotificationChannel, ch.ID)
	if got.Resource.Name != "phone" {
		t.Errorf("name = %q", got.Resource.Name)
	}
	if c := change(got.Changes, "secret"); c == nil || c.Op != audit.OpAdded || c.To != "" {
		t.Errorf("the Slack address = %+v, want a redacted secret", c)
	}

	// Pausing is an update with one change; the same again changes nothing.
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Paused: new(true)}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindNotificationChannel, ch.ID)
	if c := change(got.Changes, "paused"); c == nil || c.To != "true" {
		t.Errorf("paused = %v", changePaths(got.Changes))
	}
	if n := len(noted(t, e.store, func() {
		if _, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{Paused: new(true)}); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("a patch that changed nothing wrote %d entries", n)
	}
	// A new address is a replaced secret.
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.UpdateNotificationChannel(ctx, ch.ID, NotificationChannelPatch{URL: new(slack + "-new")}); err != nil {
			t.Fatal(err)
		}
	})
	if c := change(got.Changes, "secret"); c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
		t.Errorf("a replaced address = %+v", c)
	}

	// A webhook: the signing secret returned at creation never appears; rotating notes `rotated`.
	var hook CreatedNotificationChannel
	got = notedOne(t, e.store, func() {
		in := channelInput(notify.KindWebhook, rcv.URL+"/hook")
		in.Name = "hook"
		var err error
		if hook, err = e.svc.CreateNotificationChannel(ctx, in); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindNotificationChannel, hook.ID)
	if c := change(got.Changes, "secret"); c == nil || c.Op != audit.OpAdded {
		t.Errorf("the signing secret = %+v, want a redacted secret", c)
	}
	var rotated string
	got = notedOne(t, e.store, func() {
		var err error
		if rotated, err = e.svc.RotateNotificationSecret(ctx, hook.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRotated, audit.KindNotificationChannel, hook.ID)

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.TestNotificationChannel(ctx, hook.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionTested, audit.KindNotificationChannel, hook.ID)

	// ntfy with a token, email with a password, Telegram with a bot token.
	ntfy := channelInput(notify.KindNtfy, "https://ntfy.example.com/"+notifySecret+"-topic")
	ntfy.Name, ntfy.Token = "ntfy", notifySecret+"-ntfy-token"
	srv := mailServer(t, smtptest.Options{})
	api := newBotAPI(t)
	for _, in := range []NotificationChannelInput{ntfy, emailInput(srv), telegramInput(api)} {
		var c CreatedNotificationChannel
		got = notedOne(t, e.store, func() {
			var err error
			if c, err = e.svc.CreateNotificationChannel(ctx, in); err != nil {
				t.Fatal(err)
			}
		})
		wantEntry(t, got, audit.ActionCreated, audit.KindNotificationChannel, c.ID)
	}
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.UpdateNotificationChannel(ctx, firstOfKind(t, e, notify.KindEmail), NotificationChannelPatch{Password: new(mailPassword + "-new")}); err != nil {
			t.Fatal(err)
		}
	})
	if c := change(got.Changes, "password"); c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
		t.Errorf("a replaced SMTP password = %+v", c)
	}

	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteNotificationChannel(ctx, ch.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindNotificationChannel, ch.ID)
	// A refused call (unknown channel) notes nothing.
	if n := len(noted(t, e.store, func() { _ = e.svc.DeleteNotificationChannel(ctx, "nch_missing") })); n != 0 {
		t.Errorf("a refused delete wrote %d entries", n)
	}

	noSecret(t, e.store, e.dir, notifySecret, ntfy.Token, ntfy.URL, mailPassword, mailPassword+"-new", botToken, rotated, hook.SigningSecret, slack)
}

func firstOfKind(t *testing.T, e *env, kind string) string {
	t.Helper()
	list, err := e.svc.NotificationChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Kind == kind {
			return c.ID
		}
	}
	t.Fatalf("no %s channel", kind)
	return ""
}

func TestAuditNotesDNSZones(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)

	e.p.AddZone("example.org")
	got := notedOne(t, e.store, func() {
		if _, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.org", Provider: "cloudflare", Token: new(dnsSentinel + "-org")}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindDNSZone, "example.org")
	if c := change(got.Changes, "token"); c == nil || c.Op != audit.OpAdded || c.To != "" {
		t.Errorf("the token = %+v, want a redacted secret", c)
	}
	// A proxy change with no new token is an update with the one field; a new token is "changed".
	got = notedOne(t, e.store, func() {
		if _, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.org", Provider: "cloudflare", Proxied: true}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindDNSZone, "example.org")
	if c := change(got.Changes, "proxied"); c == nil || c.To != "true" || change(got.Changes, "token") != nil {
		t.Errorf("proxy update = %v", changePaths(got.Changes))
	}
	if n := len(noted(t, e.store, func() {
		if _, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.org", Provider: "cloudflare", Proxied: true}); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("a save that changed nothing wrote %d entries", n)
	}
	got = notedOne(t, e.store, func() {
		if _, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.org", Provider: "cloudflare", Proxied: true, Token: new(dnsSentinel + "-org")}); err != nil {
			t.Fatal(err)
		}
	})
	if c := change(got.Changes, "token"); c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
		t.Errorf("a replaced token = %+v", c)
	}
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.TestDNSZone(ctx, "example.org"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionTested, audit.KindDNSZone, "example.org")
	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteDNSZone(ctx, "example.org"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindDNSZone, "example.org")
	noSecret(t, e.store, e.dir, dnsSentinel, dnsSentinel+"-org")
}

func TestAuditNotesSecretProviders(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	got := notedOne(t, e.store, func() {
		if _, _, err := e.svc.SaveSecretProvider(ctx, bellaInput("bella-prod")); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindSecretProvider, "bella-prod")
	for _, key := range []string{"apiKey", "privateKey"} {
		if c := change(got.Changes, key); c == nil || c.Op != audit.OpAdded || c.To != "" {
			t.Errorf("%s = %+v, want a redacted secret", key, c)
		}
	}
	// Keeping the keys and moving the environment: one change.
	again := bellaInput("bella-prod")
	again.APIKey, again.PrivateKey, again.Environment = nil, nil, "staging"
	got = notedOne(t, e.store, func() {
		if _, _, err := e.svc.SaveSecretProvider(ctx, again); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSecretProvider, "bella-prod")
	if c := change(got.Changes, "environment"); c == nil || c.From != "production" || c.To != "staging" || change(got.Changes, "apiKey") != nil {
		t.Errorf("update = %v", changePaths(got.Changes))
	}
	if n := len(noted(t, e.store, func() {
		if _, _, err := e.svc.SaveSecretProvider(ctx, again); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("a save that changed nothing wrote %d entries", n)
	}
	// A new API key is "changed".
	rotate := again
	rotate.APIKey = new(providerSentinel + "-2")
	got = notedOne(t, e.store, func() {
		if _, _, err := e.svc.SaveSecretProvider(ctx, rotate); err != nil {
			t.Fatal(err)
		}
	})
	if c := change(got.Changes, "apiKey"); c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
		t.Errorf("a replaced key = %+v", c)
	}
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.TestSecretProvider(ctx, "bella-prod"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionTested, audit.KindSecretProvider, "bella-prod")
	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteSecretProvider(ctx, "bella-prod"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindSecretProvider, "bella-prod")
	if n := len(noted(t, e.store, func() { _ = e.svc.DeleteSecretProvider(ctx, "bella-prod") })); n != 0 {
		t.Errorf("a refused delete wrote %d entries", n)
	}
	noSecret(t, e.store, e.dir, providerSentinel, providerSentinel+"-2", "sentinel-----END PRIVATE KEY")
}

func TestAuditNotesServers(t *testing.T) {
	ctx := context.Background()
	e := newMoveEnv(t)

	// A join command: the server is created, waiting; the token is nowhere in the entry.
	var join ServerJoin
	got := notedOne(t, e.store, func() { join = e.newJoin(t, "three") })
	wantEntry(t, got, audit.ActionCreated, audit.KindServer, join.Server.ID)
	if got.Resource.Name != "three" || change(got.Changes, "pool") == nil {
		t.Errorf("join entry = %+v", got)
	}
	// The join arriving is a request, noted once; a bad token notes nothing.
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.Join(ctx, joinRequest(t, join.Token, "three"), "203.0.113.9"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindServer, join.Server.ID)
	if c := change(got.Changes, "state"); c == nil || c.From != "waiting" || c.To != "joining" {
		t.Errorf("join state = %+v", got.Changes)
	}
	if n := len(noted(t, e.store, func() { _, _ = e.svc.Join(ctx, joinRequest(t, "cvjn_nope", "x"), "203.0.113.9") })); n != 0 {
		t.Errorf("a refused join wrote %d entries", n)
	}
	// The agent's own sync notes nothing.
	if n := len(noted(t, e.store, func() { _, _ = e.svc.NodeSync(ctx, e.firstToken, healthyReport()) })); n != 0 {
		t.Errorf("a node sync wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SetServerLabels(ctx, "two", map[string]string{"disk": "ssd"}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindServer, "id-two")
	if c := change(got.Changes, "labels"); c == nil || c.Op != audit.OpAdded {
		t.Errorf("the first label = %v", changePaths(got.Changes))
	}
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SetServerLabels(ctx, "two", map[string]string{"disk": "nvme"}); err != nil {
			t.Fatal(err)
		}
	})
	if c := change(got.Changes, "labels.disk"); c == nil || c.From != "ssd" || c.To != "nvme" {
		t.Errorf("a changed label = %v", changePaths(got.Changes))
	}
	if n := len(noted(t, e.store, func() {
		if _, err := e.svc.SetServerLabels(ctx, "two", map[string]string{"disk": "nvme"}); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("the same labels wrote %d entries", n)
	}

	// Move work off, then accept it again.
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.MoveWorkOff(ctx, "two", 5*time.Second); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRequested, audit.KindServer, "id-two")
	if n := len(noted(t, e.store, func() { _, _ = e.svc.MoveWorkOff(ctx, "nope", 5*time.Second) })); n != 0 {
		t.Errorf("a refused move wrote %d entries", n)
	}
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.AcceptWork(ctx, "two"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindServer, "id-two")
	if c := change(got.Changes, "acceptsWork"); c == nil || c.To != "true" {
		t.Errorf("accept = %+v", got.Changes)
	}

	// Removing a server that waits.
	pending := e.newJoin(t, "four")
	got = notedOne(t, e.store, func() {
		if err := e.svc.RemoveServer(ctx, "four"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRemoved, audit.KindServer, pending.Server.ID)
	if n := len(noted(t, e.store, func() { _ = e.svc.RemoveServer(ctx, "four") })); n != 0 {
		t.Errorf("removing what isn't there wrote %d entries", n)
	}
	noSecret(t, e.store, e.dir, join.Token, e.firstToken, sentinelCAKey, sentinelGossip)
}

func TestAuditNotesCleanupsAndOrphans(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	e.firstSync(t, spaceReportOf(emptySpace()))

	var c store.ImageCleanup
	got := notedOne(t, e.store, func() {
		var err error
		if c, err = e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, true); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRequested, audit.KindServer, got.Resource.ID)
	wantRecord(t, got, audit.RecordCleanup, c.ID)
	if got.Actor.Kind != audit.ActorCaravana || got.Door != audit.DoorCaravana {
		t.Errorf("a clean-up with no principal = actor %+v door %s, want Caravana", got.Actor, got.Door)
	}
	// One open at a time: a refusal notes nothing.
	if n := len(noted(t, e.store, func() { _, _ = e.svc.RequestCleanup(ctx, firstServerName, store.CleanupByOwner, false) })); n != 0 {
		t.Errorf("a refused clean-up wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.PutCleanupSettings(ctx, false, 7); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "cleanup")
	if c := change(got.Changes, "keepDeployments"); c == nil || c.From != "3" || c.To != "7" || change(got.Changes, "automatic") == nil {
		t.Errorf("settings = %v", changePaths(got.Changes))
	}
	if n := len(noted(t, e.store, func() { _, _ = e.svc.PutCleanupSettings(ctx, false, 7) })); n != 0 {
		t.Errorf("the same settings wrote %d entries", n)
	}
	if n := len(noted(t, e.store, func() { _, _ = e.svc.PutCleanupSettings(ctx, false, 99) })); n != 0 {
		t.Errorf("a refused setting wrote %d entries", n)
	}
}

func TestAuditNotesBuildAndScanSettings(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	b, err := e.svc.BuildSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.MemoryMB = 4096
	got := notedOne(t, e.store, func() {
		if _, err := e.svc.SetBuildSettings(ctx, b); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "builds")
	if c := change(got.Changes, "memoryMB"); c == nil || c.To != "4096" || len(got.Changes) != 1 {
		t.Errorf("build settings = %v", changePaths(got.Changes))
	}
	if n := len(noted(t, e.store, func() { _, _ = e.svc.SetBuildSettings(ctx, b) })); n != 0 {
		t.Errorf("the same settings wrote %d entries", n)
	}
	b.MemoryMB = 1
	if n := len(noted(t, e.store, func() { _, _ = e.svc.SetBuildSettings(ctx, b) })); n != 0 {
		t.Errorf("a refused setting wrote %d entries", n)
	}

	s, err := e.svc.ScanSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.NotifyLevel, s.DailyHour = "high", 5
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SetScanSettings(ctx, s); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "scans")
	if change(got.Changes, "notifyLevel") == nil || change(got.Changes, "dailyHour") == nil {
		t.Errorf("scan settings = %v", changePaths(got.Changes))
	}
	s.RefuseFixableCritical = true
	if n := len(noted(t, e.store, func() { _, _ = e.svc.SetScanSettings(ctx, s) })); n != 0 {
		t.Errorf("a refused setting (no scanner) wrote %d entries", n)
	}
	// A scan can't be requested without the add-on.
	if n := len(noted(t, e.store, func() { _, _ = e.svc.RequestScan(ctx, "") })); n != 0 {
		t.Errorf("a refused scan request wrote %d entries", n)
	}
}

func TestAuditNotesUpdates(t *testing.T) {
	ctx := context.Background()
	e := newUpdEnv(t)

	got := notedOne(t, e.store, func() {
		if err := e.svc.SetReleaseCheck(ctx, false); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "updates")
	if c := change(got.Changes, "checkEnabled"); c == nil || c.From != "true" || c.To != "false" {
		t.Errorf("release check = %v", changePaths(got.Changes))
	}
	if n := len(noted(t, e.store, func() { _ = e.svc.SetReleaseCheck(ctx, false) })); n != 0 {
		t.Errorf("the same setting wrote %d entries", n)
	}
	// One request that changes two settings is one entry with both.
	got = notedOne(t, e.store, func() {
		sctx, sc := e.svc.BeginAudit(ctx, audit.DoorAPI, "203.0.113.5", "PUT /v1/updates/settings")
		if err := e.svc.PutUpdateSettings(sctx, UpdateSettings{CheckEnabled: new(true), AutoUpdate: new(true), AutoUpdateHour: new(4)}); err != nil {
			t.Fatal(err)
		}
		sc.Finish(200, audit.Resource{})
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "updates")
	for _, path := range []string{"checkEnabled", "autoUpdate", "autoUpdateHour"} {
		if change(got.Changes, path) == nil {
			t.Errorf("%s is missing: %v", path, changePaths(got.Changes))
		}
	}
	if n := len(noted(t, e.store, func() {
		_ = e.svc.PutUpdateSettings(ctx, UpdateSettings{AutoUpdateHour: new(99)})
	})); n != 0 {
		t.Errorf("a refused setting wrote %d entries", n)
	}

	// A request for an update is "requested", with the run as its record, by the person.
	owner := WithPrincipal(ctx, Principal{OrgRole: OrgOwner, Email: "o@example.com", PersonID: "p1"})
	var run store.UpdateRun
	got = notedOne(t, e.store, func() {
		var err error
		if run, err = e.svc.RequestUpdate(owner, "v0.17.0", store.UpdateByConsole, ""); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRequested, audit.KindSettings, "updates")
	wantRecord(t, got, audit.RecordUpdateRun, run.ID)
	if c := change(got.Changes, "version"); c == nil || c.From != "0.16.0" || c.To != "0.17.0" || got.Actor.Email != "o@example.com" {
		t.Errorf("update request = %v, actor %+v", got.Changes, got.Actor)
	}
	// A second request while one runs is refused and noted nowhere.
	if n := len(noted(t, e.store, func() { _, _ = e.svc.RequestUpdate(owner, "v0.17.0", store.UpdateByConsole, "") })); n != 0 {
		t.Errorf("a refused request wrote %d entries", n)
	}

	// The automatic one is Caravana's: no person, the caravana door.
	a := newUpdEnv(t)
	a.seen(t, "v0.16.3")
	a.svc.clock.(*fakeClock).Set(time.Date(2026, 11, 7, 3, 5, 0, 0, time.UTC))
	if err := a.svc.SetAutoUpdate(ctx, true, 3); err != nil {
		t.Fatal(err)
	}
	got = notedOne(t, a.store, func() {
		if err := a.svc.autoUpdate(ctx); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRequested, audit.KindSettings, "updates")
	if got.Actor.Kind != audit.ActorCaravana || got.Door != audit.DoorCaravana {
		t.Errorf("an automatic update = actor %+v door %s, want Caravana", got.Actor, got.Door)
	}
}

func TestAuditNotesReleaseChecks(t *testing.T) {
	ctx := context.Background()
	l := newUpdatesLab(t)
	got := notedOne(t, l.store, func() {
		if err := l.svc.CheckRelease(ctx); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionChecked, audit.KindSettings, "updates")
	waitFor(t, "the check to finish", func() bool { return !l.view(t).Checking.Release })
	// Too soon: a refusal, no entry. Nor does the daily pass (no principal, no request) note a thing.
	if n := len(noted(t, l.store, func() { _ = l.svc.CheckRelease(ctx) })); n != 0 {
		t.Errorf("a refused check wrote %d entries", n)
	}
	if n := len(noted(t, l.store, func() { l.check(t) })); n != 0 {
		t.Errorf("the daily check wrote %d entries", n)
	}
}

func TestAuditNotesTemplates(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	l.registry.AddIndex("traefik/whoami", "1.10", whoamiDigest)
	cs := newCatalogServer(t)
	l.svc.SetTemplatesURL(cs.URL)
	cs.publish(catalogBundle(t, futureCatVer, goodCatalogTemplate("whoami")))

	got := notedOne(t, l.store, func() {
		if err := l.svc.SetTemplateChecking(ctx, false); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "templates")
	if n := len(noted(t, l.store, func() { _ = l.svc.SetTemplateChecking(ctx, false) })); n != 0 {
		t.Errorf("the same setting wrote %d entries", n)
	}
	// Off: a check is refused and noted nowhere. On: it is "checked".
	if n := len(noted(t, l.store, func() { _ = l.svc.CheckTemplates(ctx) })); n != 0 {
		t.Errorf("a refused check wrote %d entries", n)
	}
	if err := l.svc.SetTemplateChecking(ctx, true); err != nil {
		t.Fatal(err)
	}
	got = notedOne(t, l.store, func() {
		if err := l.svc.CheckTemplates(ctx); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionChecked, audit.KindSettings, "templates")
	waitFor(t, "the check to finish", func() bool { v, _ := l.svc.TemplateCatalog(ctx); return !v.Running && !v.CheckedAt.IsZero() })

	// An install reads "installed" on the app, with the template and its version.
	var res DeployResult
	got = notedOne(t, l.store, func() {
		var err error
		res, err = l.svc.InstallTemplate(ctx, "whoami", "who", map[string]string{"domain": "who.example.com"}, TemplateInstallOptions{}, app.TriggerManual)
		if err != nil {
			t.Fatalf("InstallTemplate: %v", err)
		}
	})
	waitFinal(t, l.store, res.Deployment.ID)
	wantEntry(t, got, audit.ActionInstalled, audit.KindApp, "who")
	wantRecord(t, got, audit.RecordDeployment, res.Deployment.ID)
	if c := change(got.Changes, "template"); c == nil || c.To != "whoami 1-1" {
		t.Errorf("template change = %v", changePaths(got.Changes))
	}
	// A refused install (a bad input) notes nothing.
	if n := len(noted(t, l.store, func() {
		_, _ = l.svc.InstallTemplate(ctx, "whoami", "who2", map[string]string{"domain": "not a domain"}, TemplateInstallOptions{}, app.TriggerManual)
	})); n != 0 {
		t.Errorf("a refused install wrote %d entries", n)
	}
}

func TestAuditNotesTheImageScanner(t *testing.T) {
	ctx := context.Background()
	e, _ := newScanEnv(t)
	playScans(t, e.nomad, func(p playedScan) scanPlay { return scanPlay{} })
	deployApp(t, e, whoami())

	got := notedOne(t, e.store, func() {
		if _, err := e.svc.InstallImageScanner(ctx); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionInstalled, audit.KindAddon, "image-scanner")
	waitFor(t, "the scanner to be installed", func() bool {
		st, _ := e.svc.scannerState(ctx)
		return st == store.AddonInstalled
	})
	// Installing what is installed changes nothing.
	if n := len(noted(t, e.store, func() { _, _ = e.svc.InstallAddon(ctx, "image-scanner") })); n != 0 {
		t.Errorf("installing twice wrote %d entries", n)
	}
	if n := len(noted(t, e.store, func() { _, _ = e.svc.InstallAddon(ctx, "nope") })); n != 0 {
		t.Errorf("an unknown add-on wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.RequestScan(ctx, "whoami"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRequested, audit.KindApp, "whoami")
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.RequestScan(ctx, ""); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRequested, audit.KindSettings, "scans")
	if n := len(noted(t, e.store, func() { _, _ = e.svc.RequestScan(ctx, "no-such-app") })); n != 0 {
		t.Errorf("a refused scan request wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.RemoveAddon(ctx, "image-scanner"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUninstalled, audit.KindAddon, "image-scanner")
	waitFor(t, "the scanner to be removed", func() bool {
		st, _ := e.svc.scannerState(ctx)
		return st == ""
	})
	if n := len(noted(t, e.store, func() { _, _ = e.svc.RemoveImageScanner(ctx) })); n != 0 {
		t.Errorf("removing what isn't installed wrote %d entries", n)
	}
}

func TestAuditNotesAnOrphanVolumeDeletion(t *testing.T) {
	ctx := context.Background()
	e := orphanEnv(t)
	e.firstSync(t, spaceReportOf(emptySpace()))

	// The name typed wrong: refused, nothing noted.
	if n := len(noted(t, e.store, func() {
		_, _ = e.svc.DeleteOrphanVolume(ctx, "shop_cache", "shop_cach", false, app.TriggerConsole)
	})); n != 0 {
		t.Errorf("a refused deletion wrote %d entries", n)
	}
	var d store.VolumeDeletion
	got := notedOne(t, e.store, func() {
		var err error
		if d, err = e.svc.DeleteOrphanVolume(ctx, "shop_cache", "shop_cache", false, app.TriggerConsole); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindVolume, "shop_cache")
	wantRecord(t, got, audit.RecordVolumeDeletion, d.ID)
}

func TestAuditNotesTheConsoleAddress(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	e.agent(t)

	var c store.ConsoleAddressChange
	got := notedOne(t, e.store, func() { c = e.start(t, "new.example.com") })
	wantEntry(t, got, audit.ActionRequested, audit.KindConsoleAddress, c.ID)
	wantRecord(t, got, audit.RecordConsoleAddressChange, c.ID)
	if ch := change(got.Changes, "domain"); ch == nil || ch.From != "console.example.com" || ch.To != "new.example.com" {
		t.Errorf("domain change = %v", got.Changes)
	}
	e.wait(t, c.ID, store.ConsoleReady)
	// A second change while one is open is refused and noted nowhere.
	if n := len(noted(t, e.store, func() { _, _ = e.svc.StartConsoleAddressChange(ctx, "other.example.com", 30, false) })); n != 0 {
		t.Errorf("a refused change wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.CancelConsoleAddressChange(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCancelled, audit.KindConsoleAddress, c.ID)
	if n := len(noted(t, e.store, func() { _, _ = e.svc.CancelConsoleAddressChange(ctx, c.ID) })); n != 0 {
		t.Errorf("cancelling a cancelled change wrote %d entries", n)
	}

	c = e.start(t, "new.example.com")
	e.wait(t, c.ID, store.ConsoleReady)
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SwitchConsoleAddress(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindConsoleAddress, c.ID)
	wantRecord(t, got, audit.RecordConsoleAddressChange, c.ID)
}
