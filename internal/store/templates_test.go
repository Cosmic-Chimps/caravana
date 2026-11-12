package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

func TestAppTemplate(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := apptest.Whoami().App

	t.Run("an app saved the usual way has no template", func(t *testing.T) {
		if _, err := s.SaveApp(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		rec, err := s.GetApp(ctx, a.ID)
		if err != nil || rec.Template.Set() || rec.Template != (AppTemplate{}) {
			t.Errorf("Template = %+v, %v; want none", rec.Template, err)
		}
	})

	t.Run("a row written before the columns existed reads as none", func(t *testing.T) {
		// The migration's default is what an old row has.
		if _, err := s.db.ExecContext(ctx, `INSERT INTO apps (id, schema_version, document, created_at, updated_at, project, environment, app_name)
			VALUES ('old', ?, '{"id":"old","name":"old","source":{"kind":"image"},"services":[]}', ?, ?, 'default', 'production', 'old')`,
			appSchemaVersion, formatTime(time.Now()), formatTime(time.Now())); err != nil {
			t.Fatal(err)
		}
		rec, err := s.GetApp(ctx, "old")
		if err != nil || rec.Template.Set() {
			t.Errorf("old row: %+v, %v", rec.Template, err)
		}
	})

	t.Run("recording a template changes nothing else", func(t *testing.T) {
		before, err := s.GetApp(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetAppTemplate(ctx, a.ID, "hollo", "0.9.25-1"); err != nil {
			t.Fatal(err)
		}
		after, err := s.GetApp(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Template != (AppTemplate{Name: "hollo", Version: "0.9.25-1"}) || !after.Template.Set() {
			t.Errorf("Template = %+v", after.Template)
		}
		if after.Revision != before.Revision || !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("revision %s -> %s, updated %v -> %v: the template must not move them",
				before.Revision, after.Revision, before.UpdatedAt, after.UpdatedAt)
		}
		list, err := s.ListApps(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range list {
			if r.App.ID == a.ID {
				found = r.Template.Name == "hollo"
			}
		}
		if !found {
			t.Error("ListApps lost the template")
		}
	})

	t.Run("a later save keeps it", func(t *testing.T) {
		if _, err := s.SaveApp(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		rec, err := s.GetApp(ctx, a.ID)
		if err != nil || rec.Template.Name != "hollo" {
			t.Errorf("after a save: %+v, %v", rec.Template, err)
		}
	})

	t.Run("an unknown app", func(t *testing.T) {
		if err := s.SetAppTemplate(ctx, "ghost", "x", "1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetAppTemplate of an unknown app = %v, want ErrNotFound", err)
		}
	})
}

func TestTemplateCatalogRow(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	row, err := s.TemplateCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !row.Checking || row.Bundle != nil || row.Version != "" || !row.CheckedAt.IsZero() || row.Error != "" {
		t.Errorf("a new store's row = %+v, want checking on and nothing else", row)
	}

	t0 := time.Date(2026, 11, 12, 9, 0, 0, 123456789, time.UTC)
	want := TemplateCatalogRow{
		Checking: true, Bundle: []byte(`{"format":1}`), BundleSum: "abc", Version: "2026.11.13",
		FetchedAt: t0, CheckedAt: t0.Add(time.Minute), CheckedOKAt: t0.Add(time.Minute), Error: "",
	}
	if err := s.SaveTemplateCheck(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.TemplateCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Bundle) != string(want.Bundle) || got.BundleSum != "abc" || got.Version != "2026.11.13" ||
		!got.FetchedAt.Equal(t0) || !got.CheckedOKAt.Equal(want.CheckedOKAt) || !got.Checking {
		t.Errorf("read back %+v, want %+v", got, want)
	}

	t.Run("the setting and a check don't undo each other", func(t *testing.T) {
		if err := s.SetTemplateChecking(ctx, false); err != nil {
			t.Fatal(err)
		}
		failed := want
		failed.Checking = true // a check that started before the setting changed
		failed.CheckedAt, failed.Error = t0.Add(time.Hour), "GitHub didn't answer"
		if err := s.SaveTemplateCheck(ctx, failed); err != nil {
			t.Fatal(err)
		}
		got, _ := s.TemplateCatalog(ctx)
		if got.Checking {
			t.Error("the check turned the owner's setting back on")
		}
		if got.Error != "GitHub didn't answer" || string(got.Bundle) != string(want.Bundle) {
			t.Errorf("after a failed check: %+v", got)
		}
		if err := s.SetTemplateChecking(ctx, true); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.TemplateCatalog(ctx); !got.Checking || got.Error == "" {
			t.Errorf("turning it on changed more: %+v", got)
		}
	})

	t.Run("no bundle is NULL, not empty", func(t *testing.T) {
		if err := s.SaveTemplateCheck(ctx, TemplateCatalogRow{Error: "x"}); err != nil {
			t.Fatal(err)
		}
		var isNull int
		if err := s.db.QueryRowContext(ctx, "SELECT bundle IS NULL FROM template_catalog WHERE id = 1").Scan(&isNull); err != nil || isNull != 1 {
			t.Errorf("bundle IS NULL = %d, %v", isNull, err)
		}
	})
}
