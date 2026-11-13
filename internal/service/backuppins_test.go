package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// saveRun stores a finished backup of service in appID, named for its
// timestamp. A zero status means succeeded.
func saveRun(t *testing.T, st *store.Store, appID, service string, ts int64, status string, expired bool) store.BackupRecord {
	t.Helper()
	if status == "" {
		status = store.BackupSucceeded
	}
	started := time.Unix(ts, 0).UTC()
	done := started.Add(time.Second)
	b := store.BackupRecord{AppID: appID, ID: service + "-" + strconv.FormatInt(ts, 10), Service: service, Engine: app.BackupVolume,
		Status: status, Object: "backups/caravana/" + appID + "/" + service + "/" + strconv.FormatInt(ts, 10) + "/", StartedAt: started,
		FinishedAt: &done, Expired: expired}
	if err := st.SaveBackup(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPinBackup(t *testing.T) {
	ctx := context.Background()
	l := newVolumeLab(t)
	b1 := saveRun(t, l.store, "whoami", "web", 1791990000, "", false)
	b2 := saveRun(t, l.store, "whoami", "web", 1791990100, "", false)
	failed := saveRun(t, l.store, "whoami", "web", 1791990200, store.BackupFailed, false)
	gone := saveRun(t, l.store, "whoami", "web", 1791990300, "", true)

	t.Run("a bad label is a field error", func(t *testing.T) {
		for _, label := range []string{"", "Golden", "my golden", "-x", strings.Repeat("a", 33)} {
			_, err := l.svc.PinBackup(ctx, "whoami", b1.ID, label)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != "label" {
				t.Errorf("PinBackup(%q) = %v, want a field error on label", label, err)
			}
		}
	})

	t.Run("a backup that failed, or that retention deleted, can't be pinned", func(t *testing.T) {
		for _, b := range []store.BackupRecord{failed, gone} {
			if _, err := l.svc.PinBackup(ctx, "whoami", b.ID, "golden"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("PinBackup(%s) = %v, want ErrNotFound", b.ID, err)
			}
		}
		if _, err := l.svc.PinBackup(ctx, "whoami", "web-1", "golden"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("PinBackup of an unknown backup = %v, want ErrNotFound", err)
		}
		if _, err := l.svc.PinBackup(ctx, "nope", b1.ID, "golden"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("PinBackup in an unknown app = %v, want ErrNotFound", err)
		}
	})

	t.Run("pinning sets the label, and the label moves", func(t *testing.T) {
		got, err := l.svc.PinBackup(ctx, "whoami", b1.ID, "golden")
		if err != nil || got.Pin != "golden" || got.PinnedAt == nil || !got.PinnedAt.Equal(l.clock.Now().UTC()) {
			t.Fatalf("PinBackup = %+v, %v; want golden, pinned at the clock's time", got, err)
		}
		got, err = l.svc.PinBackup(ctx, "whoami", b2.ID, "golden")
		if err != nil || got.Pin != "golden" {
			t.Fatalf("second PinBackup = %+v, %v", got, err)
		}
		if old, _ := l.store.GetBackup(ctx, "whoami", b1.ID); old.Pin != "" {
			t.Errorf("the first backup still has the label %q", old.Pin)
		}
		// One label per backup: another label replaces the first.
		got, err = l.svc.PinBackup(ctx, "whoami", b2.ID, "demo")
		if err != nil || got.Pin != "demo" {
			t.Fatalf("relabel = %+v, %v", got, err)
		}
		if _, err := l.store.PinnedBackup(ctx, "whoami", "web", "golden"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("golden after the relabel = %v, want ErrNotFound", err)
		}
	})

	t.Run("a pin is refused while a deploy or volume run holds the app, or a backup runs", func(t *testing.T) {
		state := l.svc.stateOf("whoami")
		state.mu.Lock()
		_, err := l.svc.PinBackup(ctx, "whoami", b1.ID, "other")
		state.mu.Unlock()
		if !errors.Is(err, ErrAppBusy) {
			t.Errorf("PinBackup while the app is held = %v, want ErrAppBusy", err)
		}

		running := store.BackupRecord{AppID: "whoami", ID: "web-1791999999", Service: "web", Engine: app.BackupVolume,
			Status: store.BackupRunning, StartedAt: time.Unix(1791999999, 0).UTC()}
		if err := l.store.SaveBackup(ctx, running); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.PinBackup(ctx, "whoami", b1.ID, "other"); !errors.Is(err, ErrBackupRunning) {
			t.Errorf("PinBackup while a backup runs = %v, want ErrBackupRunning", err)
		}
		if _, err := l.svc.UnpinBackup(ctx, "whoami", b2.ID); !errors.Is(err, ErrBackupRunning) {
			t.Errorf("UnpinBackup while a backup runs = %v, want ErrBackupRunning", err)
		}
		// Nothing changed.
		if b, _ := l.store.GetBackup(ctx, "whoami", b2.ID); b.Pin != "demo" {
			t.Errorf("a refused unpin changed the pin to %q", b.Pin)
		}
		running.Status = store.BackupFailed
		if err := l.store.SaveBackup(ctx, running); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unpinning clears the label; a backup with none is returned as it is, even while the app is held", func(t *testing.T) {
		got, err := l.svc.UnpinBackup(ctx, "whoami", b2.ID)
		if err != nil || got.Pin != "" || got.PinnedAt != nil {
			t.Fatalf("UnpinBackup = %+v, %v", got, err)
		}
		state := l.svc.stateOf("whoami")
		state.mu.Lock()
		defer state.mu.Unlock()
		if got, err := l.svc.UnpinBackup(ctx, "whoami", b2.ID); err != nil || got.ID != b2.ID {
			t.Errorf("UnpinBackup of an unpinned backup = %+v, %v; want it, no error", got, err)
		}
		if _, err := l.svc.UnpinBackup(ctx, "whoami", "web-1"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("UnpinBackup of an unknown backup = %v, want ErrNotFound", err)
		}
	})
}

// A Postgres service keeps its pins' timestamps in its backup job's variable,
// which a pin change and a deploy both write, and which Nomad refusing rolls
// back.
func TestPinBackupPostgres(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, db())
	const path = "nomad/jobs/whoami_web_backup"
	save := func(ts int64) store.BackupRecord {
		b := saveRun(t, e.store, "whoami", "web", ts, "", false)
		b.Engine = app.BackupPostgres
		if err := e.store.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	b1, b2 := save(1791990000), save(1791990100)
	pinnedItem := func() string { return e.nomad.Variable(path)["BACKUP_PINNED"] }

	if _, ok := e.nomad.Variable(path)["BACKUP_PINNED"]; ok {
		t.Fatal("a service with no pins has BACKUP_PINNED")
	}
	if _, err := e.svc.PinBackup(ctx, "whoami", b1.ID, "golden"); err != nil {
		t.Fatal(err)
	}
	if got := pinnedItem(); got != "1791990000" {
		t.Errorf("BACKUP_PINNED = %q, want 1791990000", got)
	}
	if _, err := e.svc.PinBackup(ctx, "whoami", b2.ID, "demo"); err != nil {
		t.Fatal(err)
	}
	if got := pinnedItem(); got != "1791990000 1791990100" {
		t.Errorf("BACKUP_PINNED = %q, want both, oldest first", got)
	}
	// The label moves: the list follows.
	if _, err := e.svc.PinBackup(ctx, "whoami", b2.ID, "golden"); err != nil {
		t.Fatal(err)
	}
	if got := pinnedItem(); got != "1791990100" {
		t.Errorf("BACKUP_PINNED after the label moved = %q, want 1791990100", got)
	}
	if v := e.nomad.Variable(path); v["PGPASSWORD"] == "" || v["BACKUP_PATH"] == "" {
		t.Errorf("the pin list rewrote the whole variable: %v", v)
	}

	// A deploy writes the list again with the rest of the variable.
	deployHealthy(t, e, db())
	if got := pinnedItem(); got != "1791990100" {
		t.Errorf("BACKUP_PINNED after a deploy = %q, want 1791990100", got)
	}

	// Nomad refuses: nothing changes, in the store or in Nomad.
	e.nomad.FailVariables(403)
	if _, err := e.svc.PinBackup(ctx, "whoami", b1.ID, "golden"); err == nil {
		t.Fatal("PinBackup with Nomad refusing succeeded")
	}
	if _, err := e.svc.UnpinBackup(ctx, "whoami", b2.ID); err == nil {
		t.Fatal("UnpinBackup with Nomad refusing succeeded")
	}
	e.nomad.FailVariables(0)
	got1, _ := e.store.GetBackup(ctx, "whoami", b1.ID)
	got2, _ := e.store.GetBackup(ctx, "whoami", b2.ID)
	if got1.Pin != "" || got2.Pin != "golden" || got2.PinnedAt == nil {
		t.Errorf("after the refusals b1 = %q, b2 = %q (at %v); want the store as it was", got1.Pin, got2.Pin, got2.PinnedAt)
	}
	if got := pinnedItem(); got != "1791990100" {
		t.Errorf("BACKUP_PINNED after the refusals = %q", got)
	}

	if _, err := e.svc.UnpinBackup(ctx, "whoami", b2.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.nomad.Variable(path)["BACKUP_PINNED"]; ok {
		t.Errorf("BACKUP_PINNED after the last unpin = %v", e.nomad.Variable(path))
	}
}

// A volume backup's request carries the pinned timestamps for its retention,
// and the next request starts without them if nothing is pinned.
func TestVolumeBackupRequestCarriesPins(t *testing.T) {
	ctx := context.Background()
	l := newVolumeLab(t)
	pinned := saveRun(t, l.store, "whoami", "web", 1791990000, "", false)
	other := saveRun(t, l.store, "whoami", "web", 1791990100, "", false)
	if _, err := l.svc.PinBackup(ctx, "whoami", pinned.ID, "golden"); err != nil {
		t.Fatal(err)
	}
	// A pinned backup retention already deleted (an older version's) isn't sent.
	gone := saveRun(t, l.store, "whoami", "web", 1791980000, "", true)
	if _, err := l.store.PinBackup(ctx, "whoami", gone.ID, "old", l.t0); err != nil {
		t.Fatal(err)
	}
	_ = other

	var asked map[string]string
	id := l.id(0)
	l.render(id)
	l.nomad.OnRestart(func(copyID string) {
		asked = l.nomad.Variable(labVar)
		l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\n")
	})
	if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
		t.Fatal(err)
	}
	l.settled(id)
	l.released()
	if asked["ACTION"] != "backup" || asked["PINNED"] != "1791990000" {
		t.Errorf("the request = %v, want a backup that pins 1791990000", asked)
	}
	if got := l.nomad.Variable(labVar)["PINNED"]; got != "" {
		t.Errorf("PINNED = %q after the run, want it cleared with the request", got)
	}

	// With no pin there is no item.
	if _, err := l.svc.UnpinBackup(ctx, "whoami", pinned.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.store.UnpinBackup(ctx, "whoami", gone.ID); err != nil {
		t.Fatal(err)
	}
	l.clock.Set(l.t0.Add(time.Hour))
	id = "web-" + strconv.FormatInt(l.t0.Add(time.Hour).Unix(), 10)
	l.render(id)
	l.nomad.OnRestart(func(copyID string) {
		asked = l.nomad.Variable(labVar)
		l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\n")
	})
	if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
		t.Fatal(err)
	}
	l.settled(id)
	if _, ok := asked["PINNED"]; ok {
		t.Errorf("the request = %v, want no PINNED without pins", asked)
	}
}

// A sqlite upload is given the pinned timestamps as its sixth argument.
func TestSQLiteUploadCarriesPins(t *testing.T) {
	ctx := context.Background()
	l := newSQLiteLab(t)
	b := saveRun(t, l.store, "whoami", "web", 1791990000, "", false)
	b.Engine = app.BackupSQLite
	if err := l.store.SaveBackup(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.PinBackup(ctx, "whoami", b.ID, "golden"); err != nil {
		t.Fatal(err)
	}
	id := l.id(0)
	l.onSQLite(l.sqliteOK(id))
	if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
		t.Fatal(err)
	}
	l.settled(id)
	l.released()

	var uploads [][]string
	for _, e := range l.nomad.Execs() {
		if e.Command[2] == nomadjob.SQLiteUploadScript {
			uploads = append(uploads, e.Command[4:])
		}
	}
	if len(uploads) != 1 || len(uploads[0]) != 6 || uploads[0][5] != "1791990000" {
		t.Errorf("upload arguments = %q, want the pinned timestamp last", uploads)
	}
	if slices.Contains(uploads[0], "golden") {
		t.Error("the label went into the command; only timestamps should")
	}
}

func TestBackupStamp(t *testing.T) {
	for _, tt := range []struct {
		b    store.BackupRecord
		want string
		ok   bool
	}{
		{store.BackupRecord{ID: "web-1791990000", Service: "web"}, "1791990000", true},
		{store.BackupRecord{ID: "my-db-17", Service: "my-db"}, "17", true},
		{store.BackupRecord{ID: "web-", Service: "web"}, "", false},
		{store.BackupRecord{ID: "web-17x", Service: "web"}, "", false},
		{store.BackupRecord{ID: "web-17 18", Service: "web"}, "", false},
		{store.BackupRecord{ID: "other-17", Service: "web"}, "", false},
	} {
		if got, ok := backupStamp(tt.b); got != tt.want || ok != tt.ok {
			t.Errorf("backupStamp(%q of %q) = %q, %v; want %q, %v", tt.b.ID, tt.b.Service, got, ok, tt.want, tt.ok)
		}
	}
}
