package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func copyItemsOf(t *testing.T, e *env) []AttentionItem {
	t.Helper()
	att, err := e.svc.Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []AttentionItem
	for _, it := range att.Items {
		if it.Kind == AttentionBackupCopyFailed {
			out = append(out, it)
		}
	}
	return out
}

// failCopy ends the copy of backup b to offsite as failed.
func finishCopy(t *testing.T, e *env, b store.BackupRecord, status, message string) {
	t.Helper()
	ctx := context.Background()
	if err := e.store.QueueBackupCopies(ctx, b.AppID, b.ID, []string{"offsite"}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 23, 4, 0, int(b.StartedAt.Unix()%50), 0, time.UTC)
	if err := e.store.SaveBackupCopy(ctx, store.BackupCopy{AppID: b.AppID, BackupID: b.ID, Destination: "offsite", Status: status,
		Message: message, StartedAt: &at, FinishedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

// backup_copy_failed is a warning item per service and destination: it opens with the newest finished
// copy failing, a copy still running leaves it, a later copy that succeeds resolves it, and so does a
// policy that stops copying there.
func TestBackupCopyFailedItem(t *testing.T) {
	ctx := context.Background()
	e := newCopyEnv(t)
	freezeCopies(t, e)
	if got := copyItemsOf(t, e); len(got) != 0 {
		t.Fatalf("items = %+v, want none before any copy", got)
	}
	first := seedBackup(t, e, 0, false)
	finishCopy(t, e, first, store.CopyFailed, "copy: Failed to copyto: AccessDenied")
	items := copyItemsOf(t, e)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one", items)
	}
	it := items[0]
	if it.AppID != "whoami" || it.Service != "web" || it.Destination != "offsite" || it.Detail != "copy: Failed to copyto: AccessDenied" || it.When.IsZero() ||
		it.AppName != "whoami" || it.Project != "default" {
		t.Errorf("item = %+v", it)
	}

	// A newer copy that is still waiting doesn't hide it.
	second := seedBackup(t, e, 1, true)
	if got := copyItemsOf(t, e); len(got) != 1 {
		t.Errorf("a pending copy changed the item: %+v", got)
	}
	// One that succeeds resolves it.
	finishCopy(t, e, second, store.CopySucceeded, "")
	if got := copyItemsOf(t, e); len(got) != 0 {
		t.Errorf("items = %+v, want the success to resolve the failure", got)
	}
	// Failing again opens it again, naming the destination.
	third := seedBackup(t, e, 2, false)
	finishCopy(t, e, third, store.CopyFailed, "the destination offsite was removed")
	if got := copyItemsOf(t, e); len(got) != 1 || got[0].Detail != "the destination offsite was removed" {
		t.Fatalf("items = %+v", got)
	}
	// A policy that no longer copies there has nothing to warn about.
	deployHealthy(t, e, db())
	if got := copyItemsOf(t, e); len(got) != 0 {
		t.Errorf("items = %+v, want none: the policy doesn't copy to offsite any more", got)
	}
	// And a deleted app takes its copies with it.
	deployHealthy(t, e, dbWith(func(b *app.Backup) { b.CopyTo = []string{"offsite"} }))
	if got := copyItemsOf(t, e); len(got) != 1 {
		t.Fatalf("items = %+v, want it back with the policy", got)
	}
	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatal(err)
	}
	if got := copyItemsOf(t, e); len(got) != 0 {
		t.Errorf("items = %+v after the app was deleted", got)
	}
}

func TestBackupFailedNamesItsDestination(t *testing.T) {
	ctx := context.Background()
	e := newCopyEnv(t)
	freezeCopies(t, e)
	b := seedBackup(t, e, 0, false)
	done := b.StartedAt.Add(time.Second)
	bad := store.BackupRecord{AppID: "whoami", ID: "web-1800000000", Service: "web", Engine: "postgres", Status: store.BackupFailed,
		Message: "pg_dump: error: connection failed", StartedAt: b.StartedAt.Add(time.Hour), FinishedAt: &done, Destination: "main"}
	if err := e.store.SaveBackup(ctx, bad); err != nil {
		t.Fatal(err)
	}
	att, err := e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *AttentionItem
	for i, it := range att.Items {
		if it.Kind == AttentionBackupFailed {
			found = &att.Items[i]
		}
	}
	if found == nil || found.Destination != "main" || found.Detail != "pg_dump: error: connection failed" {
		t.Fatalf("item = %+v, want the failed backup with its destination", found)
	}
}

func TestBackupCopyFailedWords(t *testing.T) {
	it := AttentionItem{Kind: AttentionBackupCopyFailed, AppID: "blog", Service: "db", Destination: "offsite", Detail: "copy: boom"}
	ev := problemEvent(it, "https://console.example.com", time.Now())
	if ev.Title != "blog: the copy of db's backup to offsite failed" || ev.Text != "The copy of blog/db's backup to offsite failed." ||
		ev.Destination != "offsite" || ev.Detail != "copy: boom" || ev.URL != "https://console.example.com/apps/blog/backups" {
		t.Errorf("event = %+v", ev)
	}
	if got := problemKey(it); got != "backup_copy_failed|blog|db|offsite" {
		t.Errorf("key = %q", got)
	}
	// Two destinations of one service are two problems.
	other := it
	other.Destination = "cold"
	if problemKey(it) == problemKey(other) {
		t.Error("the copies to two destinations share a key")
	}
	// A failed backup says where it was going, when it knows.
	failed := AttentionItem{Kind: AttentionBackupFailed, AppID: "blog", Service: "db", Destination: "main"}
	if ev := problemEvent(failed, "", time.Now()); ev.Text != "The latest backup of blog/db to main failed." || ev.Destination != "main" {
		t.Errorf("backup_failed event = %+v", ev)
	}
	failed.Destination = ""
	if ev := problemEvent(failed, "", time.Now()); ev.Text != "The latest backup of blog/db failed." || ev.Destination != "" {
		t.Errorf("backup_failed event without a destination = %+v", ev)
	}
	parts := ProblemParts(it, func(id string) string { return id })
	var plain strings.Builder
	for _, p := range parts {
		plain.WriteString(p.Text)
	}
	if !strings.Contains(plain.String(), "backup to offsite failed.") || !strings.Contains(plain.String(), "db") {
		t.Errorf("words = %q", plain.String())
	}
}
