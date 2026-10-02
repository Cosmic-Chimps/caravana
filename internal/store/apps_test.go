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
			if _, err := s.SaveApp(ctx, c.App); err != nil {
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

	first, err := s.SaveApp(ctx, a)
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	if !first.CreatedAt.Equal(first.UpdatedAt) {
		t.Errorf("new app: CreatedAt %v != UpdatedAt %v", first.CreatedAt, first.UpdatedAt)
	}

	// Saving the same app again changes nothing, not even UpdatedAt.
	same, err := s.SaveApp(ctx, apptest.Whoami().App)
	if err != nil {
		t.Fatalf("SaveApp identical: %v", err)
	}
	if !same.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("identical save moved UpdatedAt from %v to %v", first.UpdatedAt, same.UpdatedAt)
	}

	changed := apptest.Whoami().App
	changed.Services[0].Replicas = new(3)
	after, err := s.SaveApp(ctx, changed)
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

	if _, err := s.SaveApp(ctx, app.App{}); err == nil {
		t.Error("SaveApp with an empty ID succeeded")
	}
}

func TestAppsErrors(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if _, err := s.GetApp(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetApp(nope) = %v, want ErrNotFound", err)
	}

	if _, err := s.SaveApp(ctx, apptest.Whoami().App); err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE apps SET schema_version = 2"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := s.GetApp(ctx, "whoami"); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("GetApp with a newer document = %v, want ErrSchemaTooNew", err)
	}
}
