package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sbID(c string) string { return strings.Repeat(c, 32) }

var sbT0 = time.Date(2026, 11, 28, 3, 0, 0, 0, time.UTC)

func TestSelfBackupSettings(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	got, err := s.SelfBackupSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Hour != 3 || got.Keep != 14 || got.HasKey() || got.KeyFingerprint != "" || !got.KeyCreatedAt.IsZero() {
		t.Errorf("defaults = %+v", got)
	}
	if err := s.SetSelfBackupSchedule(ctx, false, 22, 30); err != nil {
		t.Fatal(err)
	}
	pub := bytes.Repeat([]byte{7}, 32)
	if err := s.SetSelfBackupKey(ctx, pub, "0123456789abcdef", sbT0); err != nil {
		t.Fatal(err)
	}
	got, _ = s.SelfBackupSettings(ctx)
	if got.Enabled || got.Hour != 22 || got.Keep != 30 || !bytes.Equal(got.PublicKey, pub) || got.KeyFingerprint != "0123456789abcdef" || !got.KeyCreatedAt.Equal(sbT0) || !got.HasKey() {
		t.Errorf("after = %+v", got)
	}
	// The table refuses what the service should have refused already.
	for _, c := range [][2]int{{24, 14}, {-1, 14}, {3, 0}, {3, 91}} {
		if err := s.SetSelfBackupSchedule(ctx, true, c[0], c[1]); err == nil {
			t.Errorf("hour %d keep %d was stored", c[0], c[1])
		}
	}
}

func TestSelfBackupRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, ok, err := s.OpenSelfBackup(ctx); err != nil || ok {
		t.Fatalf("OpenSelfBackup with none = %v, %v", ok, err)
	}
	a, err := s.InsertSelfBackup(ctx, SelfBackup{ID: sbID("a"), Trigger: SelfBackupByOwner, Actor: "ana@example.com", Destination: "main", KeyFingerprint: "ff"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != SelfBackupRunning || a.StartedAt.IsZero() {
		t.Errorf("a new run = %+v", a)
	}
	// One run at a time, and a second ID can't be told from a taken one only by the error.
	if _, err := s.InsertSelfBackup(ctx, SelfBackup{ID: sbID("b"), Trigger: SelfBackupBySchedule}); !errors.Is(err, ErrSelfBackupOpen) {
		t.Errorf("a second running run = %v, want ErrSelfBackupOpen", err)
	}
	if _, err := s.InsertSelfBackup(ctx, SelfBackup{ID: sbID("a"), Trigger: SelfBackupBySchedule}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("the same ID = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.InsertSelfBackup(ctx, SelfBackup{ID: sbID("c"), Trigger: "weird"}); err == nil {
		t.Error("an unknown trigger was stored")
	}
	open, ok, _ := s.OpenSelfBackup(ctx)
	if !ok || open.ID != a.ID || !open.EndedAt.IsZero() || open.Actor != "ana@example.com" {
		t.Errorf("OpenSelfBackup = %+v, %v", open, ok)
	}
	end := sbT0.Add(time.Minute)
	if err := s.FinishSelfBackup(ctx, a.ID, SelfBackupSucceeded, end, SelfBackupResult{Destination: "main", Object: "p/_caravana/1/caravana-1.cvbackup",
		Bytes: 1234, KeyFingerprint: "ff", Version: "0.30.0", Schema: 49, Warning: "one old backup couldn't be deleted"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSelfBackup(ctx, a.ID)
	if err != nil || got.Status != SelfBackupSucceeded || got.Bytes != 1234 || got.Object == "" || got.Schema != 49 || got.Version != "0.30.0" ||
		got.Warning == "" || !got.EndedAt.Equal(end) || got.Error != "" {
		t.Fatalf("finished = %+v, %v", got, err)
	}
	// Finishing again changes nothing; an unknown ID is not found; a non-final status is refused.
	if err := s.FinishSelfBackup(ctx, a.ID, SelfBackupFailed, end.Add(time.Hour), SelfBackupResult{Error: "late"}); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetSelfBackup(ctx, a.ID); again.Status != SelfBackupSucceeded {
		t.Errorf("a settled run changed to %q", again.Status)
	}
	if err := s.FinishSelfBackup(ctx, sbID("9"), SelfBackupFailed, end, SelfBackupResult{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown run = %v", err)
	}
	if err := s.FinishSelfBackup(ctx, a.ID, SelfBackupRunning, end, SelfBackupResult{}); err == nil {
		t.Error("running is not a final status")
	}

	// A later failed run; the lists.
	if _, err := s.InsertSelfBackup(ctx, SelfBackup{ID: sbID("d"), Trigger: SelfBackupBySchedule, StartedAt: sbT0.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishSelfBackup(ctx, sbID("d"), SelfBackupFailed, sbT0.Add(25*time.Hour), SelfBackupResult{Error: "the destination refused the upload"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSelfBackups(ctx, 10)
	if err != nil || len(list) != 2 || list[0].ID != sbID("d") || list[1].ID != a.ID {
		t.Errorf("ListSelfBackups = %+v, %v", list, err)
	}
	if one, _ := s.ListSelfBackups(ctx, 1); len(one) != 1 {
		t.Errorf("limit 1 gave %d", len(one))
	}
	if _, err := s.ListSelfBackups(ctx, 0); err == nil {
		t.Error("limit 0 was accepted")
	}
	if l, ok, _ := s.LatestSelfBackup(ctx); !ok || l.ID != sbID("d") {
		t.Errorf("latest = %+v", l)
	}
	if l, ok, _ := s.LatestSucceededSelfBackup(ctx); !ok || l.ID != a.ID {
		t.Errorf("latest succeeded = %+v", l)
	}
	if l, ok, _ := s.LatestFinishedSelfBackup(ctx); !ok || l.ID != sbID("d") {
		t.Errorf("latest finished = %+v", l)
	}
	if _, err := s.GetSelfBackup(ctx, sbID("9")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSelfBackup of nothing = %v", err)
	}
}

func TestScheduledSelfBackupSinceAndFailRunning(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	slot := sbT0
	if spent, err := s.ScheduledSelfBackupSince(ctx, slot); err != nil || spent {
		t.Fatalf("no runs: %v, %v", spent, err)
	}
	// An owner's run doesn't spend the clock's slot; a running scheduled run isn't spent yet.
	s.InsertSelfBackup(ctx, SelfBackup{ID: sbID("a"), Trigger: SelfBackupByOwner, StartedAt: slot.Add(time.Minute)})
	s.FinishSelfBackup(ctx, sbID("a"), SelfBackupSucceeded, slot.Add(2*time.Minute), SelfBackupResult{})
	if spent, _ := s.ScheduledSelfBackupSince(ctx, slot); spent {
		t.Error("an owner's run spent the slot")
	}
	s.InsertSelfBackup(ctx, SelfBackup{ID: sbID("b"), Trigger: SelfBackupBySchedule, StartedAt: slot.Add(3 * time.Minute)})
	if spent, _ := s.ScheduledSelfBackupSince(ctx, slot); spent {
		t.Error("a running run spent the slot")
	}
	// A restart settles the one left running: failed, and then it is spent.
	n, err := s.FailRunningSelfBackups(ctx, slot.Add(4*time.Minute), "Caravana restarted during the backup")
	if err != nil || n != 1 {
		t.Fatalf("FailRunningSelfBackups = %d, %v", n, err)
	}
	b, _ := s.GetSelfBackup(ctx, sbID("b"))
	if b.Status != SelfBackupFailed || b.Error != "Caravana restarted during the backup" || b.EndedAt.IsZero() {
		t.Errorf("settled = %+v", b)
	}
	if spent, _ := s.ScheduledSelfBackupSince(ctx, slot); !spent {
		t.Error("a failed scheduled run didn't spend the slot")
	}
	if spent, _ := s.ScheduledSelfBackupSince(ctx, slot.Add(time.Hour)); spent {
		t.Error("an earlier run spent a later slot")
	}
	if n, _ := s.FailRunningSelfBackups(ctx, slot, "x"); n != 0 {
		t.Errorf("nothing was running, %d settled", n)
	}
}

func TestSelfRestoreRecords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, ok, err := s.LatestSelfRestore(ctx); err != nil || ok {
		t.Fatalf("LatestSelfRestore with none = %v, %v", ok, err)
	}
	r, err := s.InsertSelfRestore(ctx, SelfRestore{ID: sbID("r"), Object: "p/_caravana/1/caravana-1.cvbackup", BackupCreatedAt: sbT0, BackupVersion: "0.30.0"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != SelfRestorePending || r.RestoredAt.IsZero() {
		t.Errorf("a new restore = %+v", r)
	}
	if _, err := s.InsertSelfRestore(ctx, SelfRestore{ID: sbID("r"), BackupCreatedAt: sbT0}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("the same ID = %v", err)
	}
	if err := s.AddSelfRestoreItems(ctx, r.ID, []SelfRestoreItem{{Kind: RestoreItemRedeploy, App: "web"}, {Kind: RestoreItemRedeploy, App: "blog", Status: RestoreItemSkipped, Reason: "never deployed"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSelfRestoreItems(ctx, r.ID, []SelfRestoreItem{{Kind: RestoreItemData, App: "web", Service: "db"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSelfRestoreItems(ctx, sbID("9"), []SelfRestoreItem{{Kind: RestoreItemData, App: "x"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("items for no restore = %v", err)
	}
	items, err := s.ListSelfRestoreItems(ctx, r.ID)
	if err != nil || len(items) != 3 || items[0].Seq != 1 || items[2].Seq != 3 || items[2].Service != "db" || items[0].Status != RestoreItemWaiting || items[1].Reason == "" {
		t.Fatalf("items = %+v, %v", items, err)
	}
	if err := s.UpdateSelfRestoreItem(ctx, r.ID, 1, RestoreItemDone, "dep-1", "", sbT0, sbT0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	items, _ = s.ListSelfRestoreItems(ctx, r.ID)
	if items[0].Status != RestoreItemDone || items[0].Ref != "dep-1" || !items[0].EndedAt.Equal(sbT0.Add(time.Minute)) || !items[0].StartedAt.Equal(sbT0) {
		t.Errorf("updated = %+v", items[0])
	}
	if err := s.UpdateSelfRestoreItem(ctx, r.ID, 9, RestoreItemDone, "", "", time.Time{}, time.Time{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown item = %v", err)
	}
	if err := s.SetSelfRestoreStatus(ctx, r.ID, SelfRestoreRunning, sbT0); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.LatestSelfRestore(ctx); got.Status != SelfRestoreRunning || !got.EndedAt.IsZero() {
		t.Errorf("running = %+v", got)
	}
	if err := s.SetSelfRestoreStatus(ctx, r.ID, SelfRestoreDone, sbT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.LatestSelfRestore(ctx); got.Status != SelfRestoreDone || !got.EndedAt.Equal(sbT0.Add(time.Hour)) {
		t.Errorf("done = %+v", got)
	}
	if err := s.SetSelfRestoreStatus(ctx, sbID("9"), SelfRestoreDone, sbT0); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown restore = %v", err)
	}
}

func TestCopyToMakesACheckedCopy(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreWithPerson(t)
	if s.DataDir() == "" {
		t.Fatal("no data dir")
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil || v < 49 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	apps, people, err := s.Counts(ctx)
	if err != nil || apps != 0 || people != 1 {
		t.Errorf("Counts = %d, %d, %v", apps, people, err)
	}
	if err := s.SetSelfBackupSchedule(ctx, true, 5, 7); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "it's a copy.db") // a quote in the name must not break the statement
	if err := s.CopyTo(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	// The copy is a database of its own with what the store holds, and the store went on working.
	db, err := sql.Open("sqlite", "file:"+dsnPath(copyPath)+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var hour, keep, nPeople int
	if err := db.QueryRow(`SELECT hour, keep, (SELECT count(*) FROM people) FROM self_backup_settings`).Scan(&hour, &keep, &nPeople); err != nil || hour != 5 || keep != 7 || nPeople != 1 {
		t.Errorf("the copy holds hour %d keep %d people %d (%v)", hour, keep, nPeople, err)
	}
	if err := QuickCheckFile(ctx, copyPath); err != nil {
		t.Errorf("QuickCheckFile = %v", err)
	}
	if _, err := s.SelfBackupSettings(ctx); err != nil {
		t.Errorf("the store stopped working: %v", err)
	}
	// An existing file is never overwritten.
	if err := s.CopyTo(ctx, copyPath); err == nil {
		t.Error("CopyTo overwrote a file")
	}
	// A file that isn't a database fails the check.
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, bytes.Repeat([]byte("not a database"), 100), 0o600)
	if err := QuickCheckFile(ctx, junk); err == nil {
		t.Error("QuickCheckFile accepted junk")
	}
}

func TestSpaceErrorSaysItInWords(t *testing.T) {
	e := &SpaceError{Free: 5 << 20, Need: 40 << 20}
	if got := e.Error(); !strings.Contains(got, "5 MB") || !strings.Contains(got, "40 MB") {
		t.Errorf("Error() = %q", got)
	}
}
