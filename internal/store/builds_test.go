package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func mkBuild(id string) BuildRecord {
	return BuildRecord{ID: id, Service: "web", Repository: "https://github.com/acme/web.git", Branch: "main", Image: "ghcr.io/acme/web"}
}

func TestBuildLifecycle(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t)

	rec, err := s.CreateDeploymentWithBuild(ctx, "whoami", newDeployment("d1"), nil, nil, "", mkBuild("b1"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Build == nil || rec.Build.ID != "b1" || rec.Build.Status != BuildQueued || rec.Build.QueuedAt.IsZero() ||
		rec.Build.Repository != "https://github.com/acme/web.git" || rec.Build.DeploymentID != "d1" {
		t.Fatalf("record's build = %+v", rec.Build)
	}
	if _, err := s.CreateDeploymentWithBuild(ctx, "whoami", newDeployment("d1"), nil, nil, "", mkBuild("b9")); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate deployment = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.GetBuild(ctx, "b9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the refused deployment left a build row: %v", err)
	}

	open, err := s.OpenBuilds(ctx)
	if err != nil || len(open) != 1 || open[0].ID != "b1" {
		t.Fatalf("OpenBuilds = %+v, %v", open, err)
	}

	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	if err := s.StartBuild(ctx, "b1", at); err != nil {
		t.Fatal(err)
	}
	if err := s.StartBuild(ctx, "b1", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("starting a running build = %v, want ErrNotFound", err)
	}
	if err := s.SetBuildNode(ctx, "b1", "node-1"); err != nil {
		t.Fatal(err)
	}

	end := BuildEnd{Status: BuildSucceeded, Commit: "4e0a77c1b2d3e4f5", Author: "Ana", Subject: "Retry timeouts",
		Image: "ghcr.io/acme/web:git-4e0a77c1b2d3", Digest: "sha256:abc", Log: "line one\nline two\n", LogCut: true, At: at.Add(41 * time.Second)}
	if err := s.EndBuild(ctx, "b1", end); err != nil {
		t.Fatal(err)
	}
	if err := s.EndBuild(ctx, "b1", BuildEnd{Status: BuildFailed}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ending a settled build = %v, want ErrNotFound", err)
	}
	if err := s.EndBuild(ctx, "b1", BuildEnd{Status: BuildRunning}); err == nil {
		t.Error("EndBuild took a non-final status")
	}

	got, err := s.GetBuildOfDeployment(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != BuildSucceeded || got.Commit != end.Commit || got.Author != "Ana" || got.Digest != "sha256:abc" ||
		got.Log != end.Log || !got.LogCut || got.Node != "node-1" || !got.StartedAt.Equal(at) || !got.EndedAt.Equal(end.At) || !got.Final() {
		t.Errorf("settled build = %+v", got)
	}

	// The record's summary carries the fields but not the log.
	rec, err = s.GetDeployment(ctx, "d1")
	if err != nil || rec.Build == nil {
		t.Fatalf("GetDeployment = %+v, %v", rec, err)
	}
	if rec.Build.Log != "" || rec.Build.Digest != "sha256:abc" || !rec.Build.LogCut || !rec.Build.EndedAt.Equal(end.At) || rec.Build.Node != "node-1" {
		t.Errorf("summary = %+v", rec.Build)
	}
	if open, _ := s.OpenBuilds(ctx); len(open) != 0 {
		t.Errorf("a settled build is still open: %+v", open)
	}

	// A deployment that didn't build has none.
	if _, err := s.CreateDeployment(ctx, "whoami", newDeployment("d2"), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.GetDeployment(ctx, "d2"); rec.Build != nil {
		t.Errorf("a plain deployment has a build: %+v", rec.Build)
	}
	if _, err := s.GetBuildOfDeployment(ctx, "d2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBuildOfDeployment of a plain deployment = %v", err)
	}

	// Every list read carries the build, in one query each.
	list, err := s.ListDeployments(ctx, "whoami", 0)
	if err != nil || len(list) != 2 || list[1].Build == nil || list[0].Build != nil {
		t.Errorf("ListDeployments = %+v, %v", list, err)
	}
	latest, _ := s.LatestDeployments(ctx)
	if latest["whoami"].Deployment.ID != "d2" {
		t.Errorf("latest = %+v", latest)
	}
	recent, _ := s.ListRecentDeployments(ctx, 10)
	if len(recent) != 2 || recent[1].Build == nil {
		t.Errorf("recent = %+v", recent)
	}
}

func TestBuildLogsArePrunedToTheNewest(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t)
	const total = BuildLogsKept + 3
	for i := 0; i < total; i++ {
		d, b := fmt.Sprintf("d%03d", i), fmt.Sprintf("b%03d", i)
		if _, err := s.CreateDeploymentWithBuild(ctx, "whoami", newDeployment(d), nil, nil, "", mkBuild(b)); err != nil {
			t.Fatal(err)
		}
		if err := s.EndBuild(ctx, b, BuildEnd{Status: BuildFailed, Error: "boom " + b, Log: "log of " + b}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < total; i++ {
		b, err := s.GetBuild(ctx, fmt.Sprintf("b%03d", i))
		if err != nil {
			t.Fatal(err)
		}
		wantLog := i >= total-BuildLogsKept
		if (b.Log != "") != wantLog {
			t.Errorf("build %d: log kept = %v, want %v", i, b.Log != "", wantLog)
		}
		if !strings.HasPrefix(b.Error, "boom") || b.Status != BuildFailed {
			t.Errorf("build %d lost its fields: %+v", i, b)
		}
	}
}

func TestDeletingTheAppDeletesItsBuilds(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t)
	if _, err := s.CreateDeploymentWithBuild(ctx, "whoami", newDeployment("d1"), nil, nil, "", mkBuild("b1")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBuild(ctx, "b1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("build survived its app: %v", err)
	}
}

func TestBuildSettings(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	got, err := s.GetBuildSettings(ctx)
	want := BuildSettings{MemoryMB: 2048, CPUMHz: 2000, TimeoutMinutes: 30, Concurrency: 1, CacheMB: 5120}
	if err != nil || got != want {
		t.Fatalf("defaults = %+v, %v; want %+v", got, err, want)
	}
	next := BuildSettings{MemoryMB: 4096, CPUMHz: 4000, TimeoutMinutes: 60, Concurrency: 2, CacheMB: 0}
	if err := s.SaveBuildSettings(ctx, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBuildSettings(ctx); got != next {
		t.Errorf("saved = %+v, want %+v", got, next)
	}
	if err := s.SaveBuildSettings(ctx, BuildSettings{MemoryMB: 1, CPUMHz: 2000, TimeoutMinutes: 30, Concurrency: 1}); err == nil {
		t.Error("the table accepted 1 MB of memory")
	}
}
