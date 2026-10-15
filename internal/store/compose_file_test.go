package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// TestRevisionPinned pins the revision of the whoami app. A revision is an
// ETag clients hold, so an app without a Compose file must keep exactly the
// value it had before the file was kept (feature 015): if this fails after a
// change to revisionOf, every open console page would turn stale.
func TestRevisionPinned(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	rec, err := s.SaveApp(ctx, apptest.Whoami().App, nil)
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	const want = "119bc057e8f11b33" // computed with the code before feature 015
	if rec.Revision != want {
		t.Errorf("revision = %q, want %q", rec.Revision, want)
	}
}

// keptFile is a Compose request with what a person would write: comments,
// blank lines, a trailing newline and a secret with a value.
func keptFile() *compose.Request {
	return &compose.Request{
		Compose: "# my stack\nservices:\n  web:\n    image: nginx   # pinned later\n\n  db:\n    image: postgres:17\n",
		Name:    "Kuma",
		Services: map[string]compose.Overlay{
			"web": {
				Domains: []compose.Domain{{Host: "kuma.example.com", Port: 80}},
				Secrets: []app.Secret{{Name: "TOKEN", Value: new("s3cret-sentinel-015")}},
			},
		},
	}
}

// withoutValues is keptFile as the store gives it back: secret names only.
func withoutValues() *compose.Request {
	f := keptFile().WithoutSecretValues()
	return &f
}

func TestComposeFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	a := apptest.Whoami().App
	if _, err := s.InsertApp(ctx, a, keptFile()); err != nil {
		t.Fatalf("InsertApp: %v", err)
	}
	got, err := s.GetApp(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.Compose == nil {
		t.Fatal("GetApp: the kept file is nil")
	}
	if want := keptFile().Compose; got.Compose.Compose != want {
		t.Errorf("file text changed\n got: %q\nwant: %q", got.Compose.Compose, want)
	}
	if !reflect.DeepEqual(got.Compose, withoutValues()) {
		t.Errorf("kept request\n got: %+v\nwant: %+v", got.Compose, withoutValues())
	}

	list, err := s.ListApps(ctx)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0].Compose, got.Compose) {
		t.Errorf("ListApps = %+v, %v; want the same kept file", list, err)
	}

	// An app without a file reads back with none.
	if _, err := s.SaveApp(ctx, a, nil); err != nil {
		t.Fatalf("SaveApp without a file: %v", err)
	}
	if got, _ = s.GetApp(ctx, a.ID); got.Compose != nil {
		t.Errorf("dropping the file left %+v", got.Compose)
	}

	// A deployment keeps the file it ran with.
	if _, err := s.SaveApp(ctx, a, keptFile()); err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	if _, err := s.CreateDeployment(ctx, a.ID, newDeployment("d1"), &a, keptFile(), ""); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if _, err := s.CreateDeployment(ctx, a.ID, newDeployment("d0"), &a, nil, ""); err != nil {
		t.Fatalf("CreateDeployment without a file: %v", err)
	}
	d1, err := s.GetDeployment(ctx, "d1")
	if err != nil || !reflect.DeepEqual(d1.Compose, withoutValues()) {
		t.Errorf("deployment file = %+v, %v; want %+v", d1.Compose, err, withoutValues())
	}
	d0, err := s.GetDeployment(ctx, "d0")
	if err != nil || d0.Compose != nil {
		t.Errorf("deployment without a file = %+v, %v; want nil", d0.Compose, err)
	}
	recent, err := s.ListRecentDeployments(ctx, 10)
	if err != nil || len(recent) != 2 {
		t.Fatalf("ListRecentDeployments = %d, %v", len(recent), err)
	}
	for _, r := range recent {
		if (r.Deployment.ID == "d1") != (r.Compose != nil) {
			t.Errorf("deployment %s: file = %+v", r.Deployment.ID, r.Compose)
		}
	}
}

// TestComposeFileNeverStoresSecretValues reads the raw columns: the store
// strips the values itself, so even a caller that forgets to can't keep one.
func TestComposeFileNeverStoresSecretValues(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App

	if _, err := s.InsertApp(ctx, a, keptFile()); err != nil {
		t.Fatalf("InsertApp: %v", err)
	}
	// Each write path gets its own check: change the file, save it again.
	writes := map[string]func(file *compose.Request) error{
		"SaveApp": func(file *compose.Request) error {
			_, err := s.SaveApp(ctx, a, file)
			return err
		},
		"SaveAppIf": func(file *compose.Request) error {
			_, err := s.SaveAppIf(ctx, a, file, mustRevision(t, s, a.ID))
			return err
		},
	}
	for name, write := range writes {
		file := keptFile()
		file.Compose += "# written by " + name + "\n"
		if err := write(file); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if file.Services["web"].Secrets[0].Value == nil {
			t.Errorf("%s changed the caller's request", name)
		}
		assertNoSentinel(t, s, "apps")
	}
	if _, err := s.CreateDeployment(ctx, a.ID, newDeployment("d1"), &a, keptFile(), ""); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	assertNoSentinel(t, s, "deployments")
}

// assertNoSentinel fails when the table's compose column holds the secret
// value keptFile sets, or has lost the secret's name.
func assertNoSentinel(t *testing.T, s *Store, table string) {
	t.Helper()
	var raw string
	if err := s.db.QueryRowContext(context.Background(), "SELECT compose FROM "+table).Scan(&raw); err != nil {
		t.Fatalf("read %s.compose: %v", table, err)
	}
	if strings.Contains(raw, "s3cret-sentinel-015") {
		t.Errorf("%s.compose holds a secret value: %s", table, raw)
	}
	if !strings.Contains(raw, "TOKEN") {
		t.Errorf("%s.compose lost the secret's name: %s", table, raw)
	}
}

func mustRevision(t *testing.T, s *Store, id string) string {
	t.Helper()
	rec, err := s.GetApp(context.Background(), id)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	return rec.Revision
}

func TestComposeFileRevision(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App

	plain, err := s.SaveApp(ctx, a, nil)
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	first, err := s.SaveApp(ctx, a, keptFile())
	if err != nil {
		t.Fatalf("SaveApp with a file: %v", err)
	}
	if first.Revision == plain.Revision {
		t.Error("keeping a file did not move the revision")
	}
	if !first.UpdatedAt.After(plain.UpdatedAt) {
		t.Error("keeping a file did not move UpdatedAt")
	}

	t.Run("a comment-only change moves the revision and UpdatedAt", func(t *testing.T) {
		edited := keptFile()
		edited.Compose = strings.Replace(edited.Compose, "# my stack", "# my other stack", 1)
		got, err := s.SaveApp(ctx, a, edited)
		if err != nil {
			t.Fatalf("SaveApp: %v", err)
		}
		if got.Revision == first.Revision {
			t.Error("revision did not move")
		}
		if !got.UpdatedAt.After(first.UpdatedAt) {
			t.Errorf("UpdatedAt %v did not move past %v", got.UpdatedAt, first.UpdatedAt)
		}
		if got.Compose == nil || got.Compose.Compose != edited.Compose {
			t.Errorf("stored file = %+v, want the edited text", got.Compose)
		}
	})

	t.Run("saving the same app and file again changes nothing", func(t *testing.T) {
		before, err := s.GetApp(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		same := *before.Compose
		for _, save := range []struct {
			name string
			do   func() (AppRecord, error)
		}{
			{"SaveApp", func() (AppRecord, error) { return s.SaveApp(ctx, a, &same) }},
			{"SaveAppIf", func() (AppRecord, error) { return s.SaveAppIf(ctx, a, &same, before.Revision) }},
		} {
			got, err := save.do()
			if err != nil {
				t.Fatalf("%s: %v", save.name, err)
			}
			if got.Revision != before.Revision || !got.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("%s moved the revision or UpdatedAt: %+v -> %+v", save.name, before, got)
			}
		}
	})

	t.Run("dropping the file moves the revision back", func(t *testing.T) {
		got, err := s.SaveApp(ctx, a, nil)
		if err != nil {
			t.Fatalf("SaveApp: %v", err)
		}
		if got.Revision != plain.Revision {
			t.Errorf("revision = %s, want %s (the app without a file)", got.Revision, plain.Revision)
		}
	})
}

func TestSaveAppIfRefusesStaleFileRevision(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App

	read, err := s.SaveApp(ctx, a, keptFile())
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	// Someone else changes only the file's comments.
	other := keptFile()
	other.Compose += "# added by someone else\n"
	if _, err := s.SaveApp(ctx, a, other); err != nil {
		t.Fatalf("SaveApp: %v", err)
	}

	mine := keptFile()
	mine.Compose = "# mine\n" + mine.Compose
	if _, err := s.SaveAppIf(ctx, a, mine, read.Revision); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("SaveAppIf from a stale revision = %v, want ErrRevisionMismatch", err)
	}
	got, err := s.GetApp(ctx, a.ID)
	if err != nil || got.Compose.Compose != other.Compose {
		t.Errorf("the stale save changed the file: %+v, %v", got.Compose, err)
	}
}

func TestDeleteAppLeavesNoFile(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App

	if _, err := s.InsertApp(ctx, a, keptFile()); err != nil {
		t.Fatalf("InsertApp: %v", err)
	}
	if _, err := s.CreateDeployment(ctx, a.ID, newDeployment("d1"), &a, keptFile(), ""); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if err := s.DeleteApp(ctx, a.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	for _, table := range []string{"apps", "deployments"} {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE compose IS NOT NULL").Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows holding a file after DeleteApp = %d, %v; want 0", table, n, err)
		}
	}
}
