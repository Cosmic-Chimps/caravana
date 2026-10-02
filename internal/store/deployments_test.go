package store

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// storeWithApp is a test store holding the whoami app.
func storeWithApp(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if _, err := s.SaveApp(context.Background(), apptest.Whoami().App); err != nil {
		t.Fatalf("SaveApp: %v", err)
	}
	return s
}

func newDeployment(id string) app.Deployment {
	d := apptest.Whoami().Deployment
	d.ID = id
	return d
}

func TestDeployments(t *testing.T) {
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		s := storeWithApp(t)
		if _, err := s.CreateDeployment(ctx, "nope", newDeployment("d1")); !errors.Is(err, ErrNotFound) {
			t.Errorf("create for an unknown app = %v, want ErrNotFound", err)
		}

		rec, err := s.CreateDeployment(ctx, "whoami", newDeployment("d1"))
		if err != nil {
			t.Fatalf("CreateDeployment: %v", err)
		}
		if rec.Status != StatusPending || rec.JobVersion != nil || len(rec.Warnings) != 0 || rec.Error != "" {
			t.Errorf("new record = %+v, want pending with no job version, warnings or error", rec)
		}
		if rec.AppID != "whoami" || !reflect.DeepEqual(rec.Deployment, newDeployment("d1")) {
			t.Errorf("new record = %+v, want app whoami and the given deployment", rec)
		}
		got, err := s.GetDeployment(ctx, "d1")
		if err != nil || !reflect.DeepEqual(got, rec) {
			t.Errorf("GetDeployment = %+v, %v; want %+v", got, err, rec)
		}

		if _, err := s.CreateDeployment(ctx, "whoami", newDeployment("d1")); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("duplicate create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("list newest first", func(t *testing.T) {
		s := storeWithApp(t)
		for _, id := range []string{"d1", "d2", "d3"} {
			if _, err := s.CreateDeployment(ctx, "whoami", newDeployment(id)); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
		}
		assertListOrder(t, s, "d3", "d2", "d1")
	})

	t.Run("list order with identical timestamps", func(t *testing.T) {
		s := storeWithApp(t)
		fixed := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
		s.now = func() time.Time { return fixed }
		for _, id := range []string{"a", "c", "b"} {
			if _, err := s.CreateDeployment(ctx, "whoami", newDeployment(id)); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
		}
		assertListOrder(t, s, "b", "c", "a") // insertion order decides
	})

	t.Run("not found", func(t *testing.T) {
		s := storeWithApp(t)
		if _, err := s.GetDeployment(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetDeployment(nope) = %v, want ErrNotFound", err)
		}
		if _, err := s.UpdateDeployment(ctx, "nope", DeploymentUpdate{Status: StatusRunning}); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateDeployment(nope) = %v, want ErrNotFound", err)
		}
		if _, err := s.ListDeployments(ctx, "nope", 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("ListDeployments(nope) = %v, want ErrNotFound", err)
		}
		list, err := s.ListDeployments(ctx, "whoami", 0)
		if err != nil || len(list) != 0 {
			t.Errorf("ListDeployments(whoami) = %v, %v; want empty", list, err)
		}
	})

	t.Run("update", func(t *testing.T) {
		s := storeWithApp(t)
		created, err := s.CreateDeployment(ctx, "whoami", newDeployment("d1"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}

		warnings := []app.Warning{{Field: "services[0].ports", Message: "example"}}
		running, err := s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: StatusRunning, Warnings: warnings})
		if err != nil {
			t.Fatalf("pending -> running: %v", err)
		}
		if !running.UpdatedAt.After(created.UpdatedAt) {
			t.Errorf("UpdatedAt did not move: %v -> %v", created.UpdatedAt, running.UpdatedAt)
		}

		withVersion, err := s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: StatusRunning, JobVersion: new(uint64(3))})
		if err != nil {
			t.Fatalf("running -> running: %v", err)
		}
		if withVersion.JobVersion == nil || *withVersion.JobVersion != 3 {
			t.Errorf("JobVersion = %v, want 3", withVersion.JobVersion)
		}
		if !reflect.DeepEqual(withVersion.Warnings, warnings) {
			t.Errorf("warnings = %v, want %v kept", withVersion.Warnings, warnings)
		}

		digests := map[string]string{"web": "traefik/whoami@sha256:" + strings.Repeat("b", 64)}
		withDigests, err := s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: StatusRunning, Digests: digests})
		if err != nil {
			t.Fatalf("running -> running with digests: %v", err)
		}
		if !reflect.DeepEqual(withDigests.Deployment.Digests, digests) || *withDigests.JobVersion != 3 {
			t.Errorf("digests = %v, version %v; want the new digests and version 3 kept", withDigests.Deployment.Digests, withDigests.JobVersion)
		}

		reverted, err := s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: StatusReverted, Error: "health check failed"})
		if err != nil {
			t.Fatalf("running -> reverted: %v", err)
		}
		got, err := s.GetDeployment(ctx, "d1")
		if err != nil {
			t.Fatalf("GetDeployment: %v", err)
		}
		if !reflect.DeepEqual(got, reverted) || got.Status != StatusReverted || got.Error != "health check failed" || *got.JobVersion != 3 {
			t.Errorf("stored = %+v, want reverted with the error and job version 3", got)
		}
		if !reflect.DeepEqual(got.Deployment.Digests, digests) {
			t.Errorf("digests = %v, want %v kept", got.Deployment.Digests, digests)
		}
	})

	t.Run("list with a limit", func(t *testing.T) {
		s := storeWithApp(t)
		for _, id := range []string{"d1", "d2", "d3"} {
			if _, err := s.CreateDeployment(ctx, "whoami", newDeployment(id)); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
		}
		for _, tc := range []struct {
			limit int
			want  []string
		}{
			{1, []string{"d3"}},
			{2, []string{"d3", "d2"}},
			{5, []string{"d3", "d2", "d1"}},
			{0, []string{"d3", "d2", "d1"}},
			{-1, []string{"d3", "d2", "d1"}},
		} {
			list, err := s.ListDeployments(ctx, "whoami", tc.limit)
			if err != nil {
				t.Fatalf("ListDeployments(limit %d): %v", tc.limit, err)
			}
			if got := deploymentIDs(list); !slices.Equal(got, tc.want) {
				t.Errorf("limit %d: %v, want %v", tc.limit, got, tc.want)
			}
		}
	})

	t.Run("unfinished", func(t *testing.T) {
		s := storeWithApp(t)
		final := map[string]Status{"healthy": StatusHealthy, "failed": StatusFailed, "reverted": StatusReverted, "superseded": StatusSuperseded}
		for _, id := range []string{"pending", "running", "healthy", "failed", "reverted", "superseded"} {
			if _, err := s.CreateDeployment(ctx, "whoami", newDeployment(id)); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
			if id == "pending" {
				continue
			}
			if _, err := s.UpdateDeployment(ctx, id, DeploymentUpdate{Status: StatusRunning}); err != nil {
				t.Fatalf("%s -> running: %v", id, err)
			}
			if st, ok := final[id]; ok {
				if _, err := s.UpdateDeployment(ctx, id, DeploymentUpdate{Status: st}); err != nil {
					t.Fatalf("%s -> %s: %v", id, st, err)
				}
			}
		}
		list, err := s.ListUnfinishedDeployments(ctx)
		if err != nil {
			t.Fatalf("ListUnfinishedDeployments: %v", err)
		}
		if got := deploymentIDs(list); !slices.Equal(got, []string{"pending", "running"}) {
			t.Errorf("unfinished = %v, want [pending running] (oldest first)", got)
		}
	})
}

func deploymentIDs(list []DeploymentRecord) []string {
	var ids []string
	for _, d := range list {
		ids = append(ids, d.Deployment.ID)
	}
	return ids
}

func assertListOrder(t *testing.T, s *Store, want ...string) {
	t.Helper()
	list, err := s.ListDeployments(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if got := deploymentIDs(list); !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestTransitions(t *testing.T) {
	ctx := context.Background()
	all := []Status{StatusPending, StatusRunning, StatusHealthy, StatusFailed, StatusReverted, StatusSuperseded}
	allowed := map[[2]Status]bool{
		{StatusPending, StatusRunning}:    true,
		{StatusPending, StatusFailed}:     true,
		{StatusPending, StatusSuperseded}: true,
		{StatusRunning, StatusRunning}:    true,
		{StatusRunning, StatusHealthy}:    true,
		{StatusRunning, StatusFailed}:     true,
		{StatusRunning, StatusReverted}:   true,
		{StatusRunning, StatusSuperseded}: true,
	}
	// How to bring a new deployment to each status.
	pathTo := map[Status][]Status{
		StatusPending:    nil,
		StatusRunning:    {StatusRunning},
		StatusHealthy:    {StatusRunning, StatusHealthy},
		StatusFailed:     {StatusFailed},
		StatusReverted:   {StatusRunning, StatusReverted},
		StatusSuperseded: {StatusSuperseded},
	}

	for _, from := range all {
		for _, to := range append(all, "bogus") {
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				s := storeWithApp(t)
				if _, err := s.CreateDeployment(ctx, "whoami", newDeployment("d1")); err != nil {
					t.Fatalf("create: %v", err)
				}
				for _, step := range pathTo[from] {
					if _, err := s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: step}); err != nil {
						t.Fatalf("reach %s via %s: %v", from, step, err)
					}
				}
				before, _ := s.GetDeployment(ctx, "d1")

				_, err := s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: to, Error: "x"})
				if allowed[[2]Status{from, to}] {
					if err != nil {
						t.Errorf("%s -> %s = %v, want allowed", from, to, err)
					}
					return
				}
				if !errors.Is(err, ErrInvalidTransition) {
					t.Errorf("%s -> %s = %v, want ErrInvalidTransition", from, to, err)
				}
				if after, _ := s.GetDeployment(ctx, "d1"); !reflect.DeepEqual(after, before) {
					t.Errorf("rejected change modified the record: %+v -> %+v", before, after)
				}
			})
		}
	}
}

func TestLatestHealthyDeployment(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t)
	if _, err := s.LatestHealthyDeployment(ctx, "whoami"); !errors.Is(err, ErrNotFound) {
		t.Errorf("with no deployments = %v, want ErrNotFound", err)
	}

	// end creates a deployment and moves it to a final status.
	end := func(id string, final Status) {
		t.Helper()
		if _, err := s.CreateDeployment(ctx, "whoami", newDeployment(id)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateDeployment(ctx, id, DeploymentUpdate{Status: StatusRunning}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateDeployment(ctx, id, DeploymentUpdate{Status: final}); err != nil {
			t.Fatal(err)
		}
	}
	end("failed", StatusFailed)
	if _, err := s.LatestHealthyDeployment(ctx, "whoami"); !errors.Is(err, ErrNotFound) {
		t.Errorf("with only a failed deployment = %v, want ErrNotFound", err)
	}
	end("h1", StatusHealthy)
	end("reverted", StatusReverted)
	end("superseded", StatusSuperseded)
	if got, err := s.LatestHealthyDeployment(ctx, "whoami"); err != nil || got.Deployment.ID != "h1" {
		t.Errorf("= %s, %v; want h1", got.Deployment.ID, err)
	}
	end("h2", StatusHealthy)
	if got, err := s.LatestHealthyDeployment(ctx, "whoami"); err != nil || got.Deployment.ID != "h2" {
		t.Errorf("= %s, %v; want h2, the newest healthy one", got.Deployment.ID, err)
	}
}
