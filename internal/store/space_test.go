package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var spT0 = time.Date(2026, 11, 20, 4, 0, 0, 123456789, time.UTC)

func cleanupFixture(t *testing.T) (*Store, Server) {
	t.Helper()
	s := openTestStore(t)
	v, err := s.InsertServer(context.Background(), Server{ID: "a", Name: "one", State: ServerReady})
	if err != nil {
		t.Fatal(err)
	}
	return s, v
}

func TestCleanupSettings(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	got, err := s.CleanupSettings(ctx)
	if err != nil || !got.Automatic || got.KeepDeployments != 3 || got.LastAutomaticDay != "" {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	if err := s.SetLastAutomaticDay(ctx, "2026-11-20"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCleanupSettings(ctx, false, 7); err != nil {
		t.Fatal(err)
	}
	got, _ = s.CleanupSettings(ctx)
	if got.Automatic || got.KeepDeployments != 7 || got.LastAutomaticDay != "2026-11-20" {
		t.Errorf("after save = %+v: the day of the last daily run must survive a save", got)
	}
	for _, bad := range []int{0, 11, -1} {
		if err := s.SaveCleanupSettings(ctx, true, bad); err == nil {
			t.Errorf("kept %d deployments was stored", bad)
		}
	}
	if got, _ = s.CleanupSettings(ctx); got.KeepDeployments != 7 {
		t.Errorf("a refused save changed the settings: %+v", got)
	}
}

func TestImageCleanupLifecycle(t *testing.T) {
	ctx := context.Background()
	s, v := cleanupFixture(t)
	if _, ok, err := s.OpenImageCleanup(ctx, v.Index); err != nil || ok {
		t.Fatalf("OpenImageCleanup with none = %v, %v", ok, err)
	}

	c, err := s.CreateImageCleanup(ctx, ImageCleanup{ID: strings.Repeat("a", 32), ServerIdx: v.Index, ServerName: "one", Trigger: CleanupByOwner, DockerVolumes: true})
	if err != nil {
		t.Fatalf("CreateImageCleanup: %v", err)
	}
	if c.Status != CleanupRequested || c.RequestedAt.IsZero() {
		t.Errorf("a new clean-up = %+v, want requested with a request time", c)
	}
	// One open per server, and an ID is used once.
	if _, err := s.CreateImageCleanup(ctx, ImageCleanup{ID: strings.Repeat("b", 32), ServerIdx: v.Index, ServerName: "one", Trigger: CleanupBySchedule}); !errors.Is(err, ErrCleanupOpen) {
		t.Errorf("a second open clean-up = %v, want ErrCleanupOpen", err)
	}
	if _, err := s.CreateImageCleanup(ctx, ImageCleanup{ID: strings.Repeat("a", 32), ServerIdx: v.Index, ServerName: "one", Trigger: CleanupByOwner}); !errors.Is(err, ErrAlreadyExists) && !errors.Is(err, ErrCleanupOpen) {
		t.Errorf("the same ID twice = %v", err)
	}
	if _, err := s.CreateImageCleanup(ctx, ImageCleanup{ID: strings.Repeat("c", 32), ServerIdx: 40, ServerName: "x", Trigger: CleanupByOwner}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a clean-up of no server = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateImageCleanup(ctx, ImageCleanup{ID: strings.Repeat("d", 32), ServerIdx: v.Index, ServerName: "one", Trigger: "robot", Status: CleanupDone}); err == nil {
		t.Error("an unknown trigger was stored")
	}

	open, ok, err := s.OpenImageCleanup(ctx, v.Index)
	if err != nil || !ok || open.ID != c.ID || !open.DockerVolumes || open.Trigger != CleanupByOwner || !open.StartedAt.IsZero() || !open.FinishedAt.IsZero() {
		t.Fatalf("OpenImageCleanup = %+v, %v, %v", open, ok, err)
	}
	if all, err := s.OpenImageCleanups(ctx); err != nil || len(all) != 1 {
		t.Errorf("OpenImageCleanups = %v, %v", all, err)
	}

	if err := s.StartImageCleanup(ctx, c.ID, spT0); err != nil {
		t.Fatal(err)
	}
	// A second start leaves the first.
	if err := s.StartImageCleanup(ctx, c.ID, spT0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetImageCleanup(ctx, c.ID)
	if got.Status != CleanupRunning || !got.StartedAt.Equal(spT0) {
		t.Errorf("running clean-up = %+v", got)
	}

	end := spT0.Add(2 * time.Minute)
	if err := s.SettleImageCleanup(ctx, c.ID, CleanupDone, end, 7, 1200000000, 154, ""); err != nil {
		t.Fatal(err)
	}
	// A settled clean-up stays as it was settled: the agent's report repeats until a newer one.
	if err := s.SettleImageCleanup(ctx, c.ID, CleanupFailed, end.Add(time.Hour), 0, 0, 0, "later"); err != nil {
		t.Fatalf("settling twice = %v, want no error and no change", err)
	}
	got, err = s.GetImageCleanup(ctx, c.ID)
	if err != nil || got.Status != CleanupDone || !got.FinishedAt.Equal(end) || got.ImagesRemoved != 7 || got.BytesFreed != 1200000000 || got.VolumesRemoved != 154 || got.Error != "" {
		t.Errorf("settled clean-up = %+v, %v", got, err)
	}
	if _, ok, _ := s.OpenImageCleanup(ctx, v.Index); ok {
		t.Error("a settled clean-up is still open")
	}
	if err := s.SettleImageCleanup(ctx, c.ID, CleanupRunning, end, 0, 0, 0, ""); err == nil {
		t.Error("settled with an open status")
	}
	if err := s.SettleImageCleanup(ctx, "nope", CleanupDone, end, 0, 0, 0, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("settling an unknown ID = %v, want ErrNotFound", err)
	}
	if err := s.StartImageCleanup(ctx, "nope", end); !errors.Is(err, ErrNotFound) {
		t.Errorf("starting an unknown ID = %v, want ErrNotFound", err)
	}
	if _, err := s.GetImageCleanup(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetImageCleanup(nope) = %v", err)
	}

	// With the first settled, a new one can be requested; the list is newest first.
	second, err := s.CreateImageCleanup(ctx, ImageCleanup{ID: strings.Repeat("e", 32), ServerIdx: v.Index, ServerName: "one", Trigger: CleanupBySchedule})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListImageCleanups(ctx, v.Index, 20)
	if err != nil || len(list) != 2 || list[0].ID != second.ID || list[1].ID != c.ID {
		t.Errorf("ListImageCleanups = %+v, %v", list, err)
	}
	if list, _ := s.ListImageCleanups(ctx, v.Index, 1); len(list) != 1 {
		t.Errorf("limit 1 gave %d", len(list))
	}
	if _, err := s.ListImageCleanups(ctx, v.Index, 0); err == nil {
		t.Error("a limit of 0 was accepted")
	}

	// A clean-up goes with its server.
	if err := s.DeleteServer(ctx, v.Index); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetImageCleanup(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a clean-up outlived its server: %v", err)
	}
}

func TestVolumeDeletionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	d, err := s.CreateVolumeDeletion(ctx, VolumeDeletion{ID: "vd_1", Volume: "shop_uploads", AppID: "shop", ServerName: "one", BackUpFirst: true, Status: DeletionArchiving, Bytes: 2300000000})
	if err != nil {
		t.Fatal(err)
	}
	if d.Trigger != CleanupByOwner || d.RequestedAt.IsZero() {
		t.Errorf("a new deletion = %+v", d)
	}
	if _, err := s.CreateVolumeDeletion(ctx, VolumeDeletion{ID: "vd_1", Volume: "x", Status: DeletionArchiving}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("the same ID twice = %v", err)
	}
	if _, err := s.CreateVolumeDeletion(ctx, VolumeDeletion{ID: "vd_x", Volume: "x", Status: "weird"}); err == nil {
		t.Error("an unknown status was stored")
	}
	open, err := s.OpenVolumeDeletions(ctx)
	if err != nil || len(open) != 1 || open[0].ID != "vd_1" || !open[0].BackUpFirst || open[0].Bytes != 2300000000 {
		t.Fatalf("OpenVolumeDeletions = %+v, %v", open, err)
	}
	if err := s.SetVolumeDeletionDeleting(ctx, "vd_1", "shop/volumes/20261120T040000Z/shop_uploads.tar.gz", 2300000000); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVolumeDeletionDeleting(ctx, "vd_1", "again", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("advancing a deleting deletion = %v, want ErrNotFound", err)
	}
	end := spT0.Add(time.Minute)
	if err := s.SettleVolumeDeletion(ctx, "vd_1", DeletionDone, end, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleVolumeDeletion(ctx, "vd_1", DeletionFailed, end, "later"); !errors.Is(err, ErrNotFound) {
		t.Errorf("settling twice = %v, want ErrNotFound", err)
	}
	if err := s.SettleVolumeDeletion(ctx, "vd_2", DeletionDeleting, end, ""); err == nil {
		t.Error("settled with an open status")
	}
	got, err := s.GetVolumeDeletion(ctx, "vd_1")
	if err != nil || got.Status != DeletionDone || !got.FinishedAt.Equal(end) || !strings.HasSuffix(got.ArchiveObject, "shop_uploads.tar.gz") || got.AppID != "shop" {
		t.Errorf("settled deletion = %+v, %v", got, err)
	}
	if open, _ := s.OpenVolumeDeletions(ctx); len(open) != 0 {
		t.Errorf("a settled deletion is open: %v", open)
	}
	if _, err := s.GetVolumeDeletion(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetVolumeDeletion(nope) = %v", err)
	}
}

// The feed lists the clean-ups and deletions that ended, and keeps them in the server-only view.
func TestActivityHasCleanupsAndDeletions(t *testing.T) {
	ctx := context.Background()
	s, v := cleanupFixture(t)
	mk := func(id, status string, at time.Time, removed int, freed int64, volumes int, errText string) {
		t.Helper()
		if _, err := s.CreateImageCleanup(ctx, ImageCleanup{ID: id, ServerIdx: v.Index, ServerName: "one", Trigger: CleanupBySchedule, RequestedAt: at}); err != nil {
			t.Fatal(err)
		}
		if status != CleanupRequested {
			if err := s.SettleImageCleanup(ctx, id, status, at.Add(time.Minute), removed, freed, volumes, errText); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk(strings.Repeat("1", 32), CleanupDone, spT0, 7, 1200000000, 3, "")
	mk(strings.Repeat("2", 32), CleanupFailed, spT0.Add(time.Hour), 2, 5, 0, "conflict: unable to delete")
	mk(strings.Repeat("3", 32), CleanupRequested, spT0.Add(2*time.Hour), 0, 0, 0, "") // open: not listed

	for _, d := range []VolumeDeletion{
		{ID: "vd_a", Volume: "shop_uploads", AppID: "shop", ServerName: "one", Status: DeletionArchiving, RequestedAt: spT0.Add(3 * time.Hour), Bytes: 99}, // open: not listed
		{ID: "vd_b", Volume: "shop_cache", AppID: "shop", ServerName: "one", Status: DeletionArchiving, RequestedAt: spT0.Add(4 * time.Hour), Bytes: 2300000000},
	} {
		if _, err := s.CreateVolumeDeletion(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetVolumeDeletionDeleting(ctx, "vd_b", "shop/volumes/x.tar.gz", 2300000000); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleVolumeDeletion(ctx, "vd_b", DeletionDone, spT0.Add(5*time.Hour), ""); err != nil {
		t.Fatal(err)
	}

	for _, serverOnly := range []bool{false, true} {
		rows, err := s.ListActivity(ctx, 50, time.Time{}, serverOnly)
		if err != nil {
			t.Fatal(err)
		}
		var cleanups, deletions []ActivityRow
		for _, r := range rows {
			switch r.Source {
			case ActivityCleanup:
				cleanups = append(cleanups, r)
			case ActivityDeletion:
				deletions = append(deletions, r)
			}
		}
		if len(cleanups) != 2 || len(deletions) != 1 {
			t.Fatalf("serverOnly=%v: %d clean-ups and %d deletions in the feed, want 2 and 1 (open ones aren't listed)", serverOnly, len(cleanups), len(deletions))
		}
		failed, done := cleanups[0], cleanups[1] // newest first
		if failed.Status != CleanupFailed || failed.Service != "one" || failed.Trigger != CleanupBySchedule || failed.Error != "conflict: unable to delete" ||
			failed.ExitCode == nil || *failed.ExitCode != 2 || failed.Version == nil || *failed.Version != 5 || !failed.At.Equal(spT0.Add(time.Hour+time.Minute)) {
			t.Errorf("failed clean-up row = %+v", failed)
		}
		if done.Status != CleanupDone || *done.ExitCode != 7 || *done.Version != 1200000000 || done.Detail != "3" || done.RowID != strings.Repeat("1", 32) {
			t.Errorf("done clean-up row = %+v", done)
		}
		del := deletions[0]
		if del.AppID != "shop" || del.Service != "one" || del.Label != "shop_cache" || del.Status != DeletionDone || del.Detail != "shop/volumes/x.tar.gz" ||
			del.Version == nil || *del.Version != 2300000000 || !del.At.Equal(spT0.Add(5*time.Hour)) {
			t.Errorf("deletion row = %+v", del)
		}
	}
}
