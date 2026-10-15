package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// openTestStore opens a store in a temp dir with a fake clock that moves one
// second forward on every call, so timestamps are predictable.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	clock := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	return s
}

func TestAppsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, c := range apptest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			if _, err := s.SaveApp(ctx, c.App, nil); err != nil {
				t.Fatalf("SaveApp: %v", err)
			}
			got, err := s.GetApp(ctx, c.App.ID)
			if err != nil {
				t.Fatalf("GetApp: %v", err)
			}
			if !reflect.DeepEqual(got.App, c.App) {
				t.Errorf("round trip changed the app\n got: %+v\nwant: %+v", got.App, c.App)
			}
		})
	}
}

func TestAppsSave(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App

	first, err := s.SaveApp(ctx, a, nil)
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	if !first.CreatedAt.Equal(first.UpdatedAt) {
		t.Errorf("new app: CreatedAt %v != UpdatedAt %v", first.CreatedAt, first.UpdatedAt)
	}

	// Saving the same app again changes nothing, not even UpdatedAt.
	same, err := s.SaveApp(ctx, apptest.Whoami().App, nil)
	if err != nil {
		t.Fatalf("SaveApp identical: %v", err)
	}
	if !same.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("identical save moved UpdatedAt from %v to %v", first.UpdatedAt, same.UpdatedAt)
	}

	changed := apptest.Whoami().App
	changed.Services[0].Replicas = new(3)
	after, err := s.SaveApp(ctx, changed, nil)
	if err != nil {
		t.Fatalf("SaveApp changed: %v", err)
	}
	if !after.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("changed save kept UpdatedAt %v", after.UpdatedAt)
	}
	if !after.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("changed save moved CreatedAt from %v to %v", first.CreatedAt, after.CreatedAt)
	}
	if *after.App.Services[0].Replicas != 3 {
		t.Errorf("replicas = %d, want 3", *after.App.Services[0].Replicas)
	}

	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM apps").Scan(&n); err != nil || n != 1 {
		t.Errorf("apps rows = %d, %v; want 1", n, err)
	}

	if _, err := s.SaveApp(ctx, app.App{}, nil); err == nil {
		t.Error("SaveApp with an empty ID succeeded")
	}
}

func TestAppsErrors(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if _, err := s.GetApp(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetApp(nope) = %v, want ErrNotFound", err)
	}

	if _, err := s.SaveApp(ctx, apptest.Whoami().App, nil); err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE apps SET schema_version = 2"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := s.GetApp(ctx, "whoami"); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("GetApp with a newer document = %v, want ErrSchemaTooNew", err)
	}
}

func TestListApps(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	empty, err := s.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps on an empty store: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("ListApps on an empty store = %#v, want an empty, non-nil slice", empty)
	}

	// Saved out of order: the list comes back by ID.
	for _, id := range []string{"whoami", "blog", "shop"} {
		a := apptest.Whoami().App
		a.ID = id
		if _, err := s.SaveApp(ctx, a, nil); err != nil {
			t.Fatalf("SaveApp %s: %v", id, err)
		}
	}
	got, err := s.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	var ids []string
	for _, rec := range got {
		ids = append(ids, rec.App.ID)
	}
	if want := []string{"blog", "shop", "whoami"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ListApps IDs = %v, want %v", ids, want)
	}

	// Each record is the same one GetApp returns.
	for _, rec := range got {
		one, err := s.GetApp(ctx, rec.App.ID)
		if err != nil || !reflect.DeepEqual(one, rec) {
			t.Errorf("ListApps record %q = %+v, GetApp = %+v, %v; want equal", rec.App.ID, rec, one, err)
		}
	}

	// A document from a newer Caravana is an error naming the app, like GetApp.
	if _, err := s.db.ExecContext(ctx, "UPDATE apps SET schema_version = 2 WHERE id = 'blog'"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := s.ListApps(ctx); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("ListApps with a newer document = %v, want ErrSchemaTooNew", err)
	}
}

func TestInsertApp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App

	rec, err := s.InsertApp(ctx, a, nil)
	if err != nil {
		t.Fatalf("InsertApp: %v", err)
	}
	if !reflect.DeepEqual(rec.App, a) || !rec.CreatedAt.Equal(rec.UpdatedAt) {
		t.Errorf("InsertApp = %+v, want the app as sent, created and updated together", rec)
	}

	// The same ID again is refused, and the stored app stays as it was.
	other := apptest.Whoami().App
	other.Services[0].Replicas = new(5)
	if _, err := s.InsertApp(ctx, other, nil); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second InsertApp = %v, want ErrAlreadyExists", err)
	}
	got, err := s.GetApp(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if !reflect.DeepEqual(got, rec) {
		t.Errorf("the stored app changed\n got: %+v\nwant: %+v", got, rec)
	}

	if _, err := s.InsertApp(ctx, app.App{}, nil); err == nil {
		t.Error("InsertApp without an ID did not fail")
	}
}

func TestAppRevision(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	first, err := s.SaveApp(ctx, apptest.Whoami().App, nil)
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	if len(first.Revision) != 16 {
		t.Errorf("Revision = %q, want 16 hex digits", first.Revision)
	}

	// The same document gives the same revision, from every reader.
	same, err := s.SaveApp(ctx, apptest.Whoami().App, nil)
	if err != nil {
		t.Fatalf("SaveApp identical: %v", err)
	}
	got, err := s.GetApp(ctx, "whoami")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	listed, err := s.ListApps(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListApps = %v, %v", listed, err)
	}
	for name, rev := range map[string]string{"SaveApp": same.Revision, "GetApp": got.Revision, "ListApps": listed[0].Revision} {
		if rev != first.Revision {
			t.Errorf("%s revision = %q, want %q", name, rev, first.Revision)
		}
	}

	// A change gives a new one.
	changed := apptest.Whoami().App
	changed.Services[0].Replicas = new(3)
	after, err := s.SaveApp(ctx, changed, nil)
	if err != nil {
		t.Fatalf("SaveApp changed: %v", err)
	}
	if after.Revision == first.Revision {
		t.Errorf("a changed app kept revision %q", after.Revision)
	}

	// A fresh insert reports its revision too.
	other := apptest.Whoami().App
	other.ID = "other"
	ins, err := s.InsertApp(ctx, other, nil)
	if err != nil || ins.Revision == "" {
		t.Errorf("InsertApp revision = %q, %v; want one", ins.Revision, err)
	}
}

func TestSaveAppIf(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	first, err := s.SaveApp(ctx, apptest.Whoami().App, nil)
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}

	t.Run("matching revision saves", func(t *testing.T) {
		edited := apptest.Whoami().App
		edited.Services[0].Replicas = new(4)
		rec, err := s.SaveAppIf(ctx, edited, nil, first.Revision)
		if err != nil {
			t.Fatalf("SaveAppIf: %v", err)
		}
		if !reflect.DeepEqual(rec.App, edited) || rec.Revision == first.Revision || !rec.UpdatedAt.After(first.UpdatedAt) {
			t.Errorf("SaveAppIf = %+v, want the edited app, a new revision and a later UpdatedAt", rec)
		}
		got, err := s.GetApp(ctx, "whoami")
		if err != nil || !reflect.DeepEqual(got, rec) {
			t.Errorf("GetApp = %+v, %v; want the returned record", got, err)
		}
	})

	t.Run("stale revision is refused and writes nothing", func(t *testing.T) {
		before, err := s.GetApp(ctx, "whoami")
		if err != nil {
			t.Fatalf("GetApp: %v", err)
		}
		edited := apptest.Whoami().App
		edited.Services[0].Replicas = new(9)
		if _, err := s.SaveAppIf(ctx, edited, nil, first.Revision); !errors.Is(err, ErrRevisionMismatch) {
			t.Fatalf("SaveAppIf with a stale revision = %v, want ErrRevisionMismatch", err)
		}
		after, err := s.GetApp(ctx, "whoami")
		if err != nil || !reflect.DeepEqual(after, before) {
			t.Errorf("a refused save changed the app\n got: %+v\nwant: %+v (%v)", after, before, err)
		}
	})

	t.Run("an identical document keeps UpdatedAt", func(t *testing.T) {
		before, err := s.GetApp(ctx, "whoami")
		if err != nil {
			t.Fatalf("GetApp: %v", err)
		}
		rec, err := s.SaveAppIf(ctx, before.App, nil, before.Revision)
		if err != nil {
			t.Fatalf("SaveAppIf: %v", err)
		}
		if !reflect.DeepEqual(rec, before) {
			t.Errorf("an identical save changed the record\n got: %+v\nwant: %+v", rec, before)
		}
	})

	t.Run("unknown app", func(t *testing.T) {
		a := apptest.Whoami().App
		a.ID = "nope"
		if _, err := s.SaveAppIf(ctx, a, nil, "0000000000000000"); !errors.Is(err, ErrNotFound) {
			t.Errorf("SaveAppIf on a missing app = %v, want ErrNotFound", err)
		}
		if _, err := s.GetApp(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("SaveAppIf created the app: %v", err)
		}
	})

	t.Run("no ID", func(t *testing.T) {
		if _, err := s.SaveAppIf(ctx, app.App{}, nil, first.Revision); err == nil {
			t.Error("SaveAppIf without an ID did not fail")
		}
	})
}

// Two saves from the same revision: exactly one wins.
func TestSaveAppIfConcurrent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base, err := s.SaveApp(ctx, apptest.Whoami().App, nil)
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}

	const writers = 8
	errs := make(chan error, writers)
	for i := range writers {
		go func() {
			a := apptest.Whoami().App
			a.Services[0].Replicas = new(i + 3)
			_, err := s.SaveAppIf(ctx, a, nil, base.Revision)
			errs <- err
		}()
	}
	var won, refused int
	for range writers {
		switch err := <-errs; {
		case err == nil:
			won++
		case errors.Is(err, ErrRevisionMismatch):
			refused++
		default:
			t.Errorf("SaveAppIf: unexpected error %v", err)
		}
	}
	if won != 1 || refused != writers-1 {
		t.Errorf("%d saves won and %d were refused, want 1 and %d", won, refused, writers-1)
	}
}

func TestDeleteApp(t *testing.T) {
	ctx := context.Background()
	count := func(t *testing.T, s *Store, query string, args ...any) int {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}

	t.Run("removes the token, the deployments and the app", func(t *testing.T) {
		s := storeWithApps(t)
		for _, id := range []string{"d1", "d2"} {
			if _, err := s.CreateDeployment(ctx, "blog", newDeployment(id), nil, nil, ""); err != nil {
				t.Fatalf("CreateDeployment: %v", err)
			}
		}
		if _, err := s.SetWebhookToken(ctx, "blog", "aa"); err != nil {
			t.Fatalf("SetWebhookToken: %v", err)
		}

		if err := s.DeleteApp(ctx, "blog"); err != nil {
			t.Fatalf("DeleteApp: %v", err)
		}
		if _, err := s.GetApp(ctx, "blog"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetApp after delete = %v, want ErrNotFound", err)
		}
		for _, table := range []string{"deployments", "webhook_tokens"} {
			if n := count(t, s, "SELECT count(*) FROM "+table+" WHERE app_id = 'blog'"); n != 0 {
				t.Errorf("%s still has %d rows of the app", table, n)
			}
		}
		if _, err := s.GetDeployment(ctx, "d1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetDeployment after delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("an app with nothing else attached is deleted too", func(t *testing.T) {
		s := storeWithApps(t)
		if err := s.DeleteApp(ctx, "shop"); err != nil {
			t.Fatalf("DeleteApp: %v", err)
		}
		if n := count(t, s, "SELECT count(*) FROM apps"); n != 2 {
			t.Errorf("apps left = %d, want 2", n)
		}
	})

	t.Run("an unknown ID is ErrNotFound and nothing changes", func(t *testing.T) {
		s := storeWithApps(t)
		if _, err := s.CreateDeployment(ctx, "shop", newDeployment("d1"), nil, nil, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteApp(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteApp(nope) = %v, want ErrNotFound", err)
		}
		if n := count(t, s, "SELECT count(*) FROM apps"); n != 3 {
			t.Errorf("apps = %d, want 3", n)
		}
		if n := count(t, s, "SELECT count(*) FROM deployments"); n != 1 {
			t.Errorf("deployments = %d, want 1", n)
		}
	})

	t.Run("another app's rows are untouched", func(t *testing.T) {
		s := storeWithApps(t)
		for _, id := range []string{"blog", "shop"} {
			if _, err := s.CreateDeployment(ctx, id, newDeployment("d-"+id), nil, nil, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetWebhookToken(ctx, id, "bb"); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteApp(ctx, "blog"); err != nil {
			t.Fatalf("DeleteApp: %v", err)
		}
		if _, err := s.GetApp(ctx, "shop"); err != nil {
			t.Errorf("GetApp(shop) = %v", err)
		}
		if _, err := s.GetDeployment(ctx, "d-shop"); err != nil {
			t.Errorf("GetDeployment(d-shop) = %v", err)
		}
		if _, err := s.GetWebhookToken(ctx, "shop"); err != nil {
			t.Errorf("GetWebhookToken(shop) = %v", err)
		}
	})

	t.Run("the ID can be stored again", func(t *testing.T) {
		s := storeWithApps(t)
		if err := s.DeleteApp(ctx, "blog"); err != nil {
			t.Fatal(err)
		}
		a := apptest.Whoami().App
		a.ID = "blog"
		if _, err := s.InsertApp(ctx, a, nil); err != nil {
			t.Errorf("InsertApp after delete = %v", err)
		}
	})
}

func TestFormatTimeSorts(t *testing.T) {
	base := time.Date(2026, 10, 8, 12, 0, 5, 0, time.UTC)
	earlier, later := base.Add(761200*time.Microsecond), base.Add(761230*time.Microsecond)
	if formatTime(earlier) >= formatTime(later) {
		t.Errorf("%s sorts after %s", formatTime(earlier), formatTime(later))
	}
	if got, err := parseTime(formatTime(earlier)); err != nil || !got.Equal(earlier) {
		t.Errorf("round trip = %v, %v", got, err)
	}
	if got, err := parseTime("2026-10-08T12:00:05.7612Z"); err != nil || !got.Equal(earlier) {
		t.Errorf("an older trimmed time = %v, %v", got, err)
	}
}
