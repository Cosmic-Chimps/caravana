package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// previewApp is whoami placed in a preview environment of shop.
func previewApp(env string) app.App {
	a := apptest.Whoami().App
	a.ID = app.Place{Project: "shop", Environment: env, Name: "web"}.ID()
	return a
}

func TestProjectPreviewDefaults(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, id := range []string{"default", "shop"} {
		if id != "default" {
			if _, err := s.InsertProject(ctx, id, id); err != nil {
				t.Fatal(err)
			}
		}
		p, err := s.GetProject(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if p.PreviewLimit != 5 || p.PreviewExpireDays != 7 || p.PreviewPasswordHash != "" {
			t.Errorf("%s preview settings = %d, %d, %q; want 5, 7, none", id, p.PreviewLimit, p.PreviewExpireDays, p.PreviewPasswordHash)
		}
	}
}

func TestInsertPreviewEnvironmentLimit(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	two := 2
	if _, err := s.SetPreviewSettings(ctx, "shop", PreviewSettings{Limit: &two}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"pr-12", "pr-3"} { // staging doesn't count
		if _, err := s.InsertPreviewEnvironment(ctx, "shop", name); err != nil {
			t.Fatalf("InsertPreviewEnvironment(%s): %v", name, err)
		}
	}
	_, err := s.InsertPreviewEnvironment(ctx, "shop", "pr-9")
	var le *PreviewLimitError
	if !errors.Is(err, ErrPreviewLimit) || !errors.As(err, &le) {
		t.Fatalf("a third preview = %v, want ErrPreviewLimit", err)
	}
	if le.Limit != 2 || le.Project != "shop" || !slices.Equal(le.Open, []string{"pr-3", "pr-12"}) {
		t.Errorf("limit error = %+v, want the open ones by number", le)
	}
	if msg := le.Error(); !strings.Contains(msg, "project shop has 2 open previews (pr-3, pr-12), its limit") {
		t.Errorf("message = %q", msg)
	}
	if ok, _ := s.EnvironmentExists(ctx, "shop", "pr-9"); ok {
		t.Error("the refused preview was made anyway")
	}

	// Taken names and unknown projects come before the limit.
	if _, err := s.InsertPreviewEnvironment(ctx, "shop", "pr-3"); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("a taken name = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.InsertPreviewEnvironment(ctx, "nope", "pr-3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown project = %v, want ErrNotFound", err)
	}

	// Limit 0 is "off", with its own words.
	zero := 0
	if _, err := s.SetPreviewSettings(ctx, "shop", PreviewSettings{Limit: &zero}); err != nil {
		t.Fatal(err)
	}
	_, err = s.InsertPreviewEnvironment(ctx, "shop", "pr-20")
	if !errors.As(err, &le) || !strings.Contains(le.Error(), "previews are off for project shop (its limit is 0)") {
		t.Errorf("limit 0 = %v, want the off message", err)
	}
	// Closing one makes room again at a raised limit.
	five := 5
	if _, err := s.SetPreviewSettings(ctx, "shop", PreviewSettings{Limit: &five}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertPreviewEnvironment(ctx, "shop", "pr-20"); err != nil {
		t.Errorf("after raising the limit: %v", err)
	}
}

func TestSetPreviewSettings(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	limit, days, hash := 3, 14, "$2a$10$abc"
	p, err := s.SetPreviewSettings(ctx, "shop", PreviewSettings{Limit: &limit, ExpireDays: &days, PasswordHash: &hash})
	if err != nil || p.PreviewLimit != 3 || p.PreviewExpireDays != 14 || p.PreviewPasswordHash != hash {
		t.Fatalf("set all = %+v, %v", p, err)
	}
	// Absent fields keep what there is.
	days = 2
	p, err = s.SetPreviewSettings(ctx, "shop", PreviewSettings{ExpireDays: &days})
	if err != nil || p.PreviewLimit != 3 || p.PreviewExpireDays != 2 || p.PreviewPasswordHash != hash {
		t.Fatalf("set days only = %+v, %v", p, err)
	}
	p, err = s.SetPreviewSettings(ctx, "shop", PreviewSettings{ClearPassword: true})
	if err != nil || p.PreviewPasswordHash != "" || p.PreviewLimit != 3 {
		t.Fatalf("clear = %+v, %v", p, err)
	}
	if _, err := s.SetPreviewSettings(ctx, "shop", PreviewSettings{ClearPassword: true, PasswordHash: &hash}); err == nil {
		t.Error("set and clear together = nil error")
	}
	if _, err := s.SetPreviewSettings(ctx, "nope", PreviewSettings{Limit: &limit}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}
}

func TestPreviewActivity(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"pr-12", "pr-3"} {
		if _, err := s.InsertPreviewEnvironment(ctx, "shop", n); err != nil {
			t.Fatal(err)
		}
	}
	// Every other place of the project is no preview: an app in staging and
	// one of another project don't count.
	if _, err := s.SaveApp(ctx, previewApp("staging"), nil); err != nil {
		t.Fatal(err)
	}

	acts, err := s.PreviewActivity(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 2 || acts[0].Environment != "pr-3" || acts[1].Environment != "pr-12" || acts[0].Number != 3 {
		t.Fatalf("activity = %+v, want pr-3 then pr-12", acts)
	}
	for _, a := range acts {
		if !a.LastDeployment.IsZero() || !a.Idle().Equal(a.CreatedAt) {
			t.Errorf("%s: no deployment yet, so idle = created: %+v", a.Environment, a)
		}
	}

	// A deployment of an app in pr-3 moves its clock, the one in staging doesn't.
	a3 := previewApp("pr-3")
	if _, err := s.SaveApp(ctx, a3, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeployment(ctx, a3.ID, app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{}}, &a3, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDeployment(ctx, previewApp("staging").ID, app.Deployment{ID: "d2", Trigger: app.TriggerManual, Digests: map[string]string{}}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	acts, err = s.PreviewActivity(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if !acts[0].LastDeployment.After(acts[0].CreatedAt) || !acts[0].Idle().Equal(acts[0].LastDeployment) {
		t.Errorf("pr-3 = %+v, want its deployment as its clock", acts[0])
	}
	if !acts[1].LastDeployment.IsZero() {
		t.Errorf("pr-12 = %+v, want no deployment", acts[1])
	}
	if _, err := s.PreviewActivity(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}
}

func TestSetPreviewTeamHash(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"staging", "pr-7"} {
		if env == "pr-7" {
			if _, err := s.InsertPreviewEnvironment(ctx, "shop", env); err != nil {
				t.Fatal(err)
			}
		} else if _, err := s.InsertEnvironment(ctx, "shop", env); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveApp(ctx, previewApp(env), nil); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	if err := s.SetTeamPassword(ctx, "pr-7.example.test", "shop--pr-7--web", "old", at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTeamPassword(ctx, "staging.example.test", "shop--staging--web", "keep", at); err != nil {
		t.Fatal(err)
	}

	old, err := s.SetPreviewTeamHash(ctx, "shop", "new", at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 || old[0].Host != "pr-7.example.test" || old[0].Hash != "old" {
		t.Errorf("old rows = %+v, want the preview's", old)
	}
	got, _ := s.TeamPassword(ctx, "pr-7.example.test")
	if got.Hash != "new" || got.AppID != "shop--pr-7--web" {
		t.Errorf("preview row = %+v, want the new hash", got)
	}
	if kept, _ := s.TeamPassword(ctx, "staging.example.test"); kept.Hash != "keep" {
		t.Errorf("staging row = %+v, want it untouched", kept)
	}
	// The old rows put it back.
	for _, r := range old {
		if err := s.SetTeamPassword(ctx, r.Host, r.AppID, r.Hash, r.CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.TeamPassword(ctx, "pr-7.example.test"); got.Hash != "old" {
		t.Errorf("after the undo = %+v", got)
	}
}
