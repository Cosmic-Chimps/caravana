package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// scaledWhoami is whoami with its one service scaled 1 to 3 on cpu and no
// starting count.
func scaledWhoami() app.App {
	a := whoami()
	a.Services[0].Replicas = nil
	a.Services[0].Scale = &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{CPU: 70}}
	return a
}

func groupOf(t *testing.T, job *api.Job, name string) *api.TaskGroup {
	t.Helper()
	for _, g := range job.TaskGroups {
		if *g.Name == name {
			return g
		}
	}
	t.Fatalf("job has no group %q", name)
	return nil
}

// A deploy registers each scaled service at the count it has now, and the
// canaries follow the count (feature 043, D4).
func TestRegisterKeepsTheScaledCount(t *testing.T) {
	ctx := context.Background()

	t.Run("a new job starts at the starting count", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a := scaledWhoami()
		a.Services[0].Replicas = new(2)
		reg, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil)
		if err != nil {
			t.Fatal(err)
		}
		g := groupOf(t, nomad.Job("whoami"), "web")
		if *g.Count != 2 || *g.Update.Canary != 2 {
			t.Errorf("count %d canary %d, want 2 and 2", *g.Count, *g.Update.Canary)
		}
		if reg.Counts["web"] != 2 {
			t.Errorf("Counts = %v, want web 2", reg.Counts)
		}
		if a.Services[0].Replicas == nil || *a.Services[0].Replicas != 2 {
			t.Error("Register changed the app it was given")
		}
	})

	t.Run("without replicas it starts at min", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a := scaledWhoami()
		a.Services[0].Scale.Min = 2
		if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil); err != nil {
			t.Fatal(err)
		}
		if g := groupOf(t, nomad.Job("whoami"), "web"); *g.Count != 2 {
			t.Errorf("count = %d, want min 2", *g.Count)
		}
	})

	t.Run("a deploy keeps the count the group has", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a := scaledWhoami()
		if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil); err != nil {
			t.Fatal(err)
		}
		if _, found, err := d.Scale(ctx, "whoami", "web", 3, "test"); err != nil || !found {
			t.Fatalf("Scale = %v, %v", found, err)
		}
		reg, err := d.Register(ctx, a, deployment("d2"), ownsIDs("d1"), nil)
		if err != nil {
			t.Fatal(err)
		}
		g := groupOf(t, nomad.Job("whoami"), "web")
		if *g.Count != 3 || *g.Update.Canary != 3 {
			t.Errorf("count %d canary %d, want 3 and 3", *g.Count, *g.Update.Canary)
		}
		if reg.Counts["web"] != 3 {
			t.Errorf("Counts = %v, want web 3", reg.Counts)
		}
		if a.Services[0].Replicas != nil {
			t.Errorf("the stored app's Replicas = %d, want it left unset", *a.Services[0].Replicas)
		}
	})

	t.Run("a smaller max brings the count down", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a := scaledWhoami()
		if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.Scale(ctx, "whoami", "web", 3, "test"); err != nil {
			t.Fatal(err)
		}
		a.Services[0].Scale.Max = 2
		if _, err := d.Register(ctx, a, deployment("d2"), ownsIDs("d1"), nil); err != nil {
			t.Fatal(err)
		}
		if g := groupOf(t, nomad.Job("whoami"), "web"); *g.Count != 2 {
			t.Errorf("count = %d, want 2", *g.Count)
		}
	})

	t.Run("removing scale returns to replicas", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a := scaledWhoami()
		if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.Scale(ctx, "whoami", "web", 3, "test"); err != nil {
			t.Fatal(err)
		}
		a.Services[0].Scale = nil
		reg, err := d.Register(ctx, a, deployment("d2"), ownsIDs("d1"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if g := groupOf(t, nomad.Job("whoami"), "web"); *g.Count != 1 {
			t.Errorf("count = %d, want 1", *g.Count)
		}
		if reg.Counts != nil {
			t.Errorf("Counts = %v, want none for an unscaled app", reg.Counts)
		}
	})

	t.Run("a stopped job starts again at the starting count", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a := scaledWhoami()
		a.Services[0].Replicas = new(2)
		if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs(), nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.Scale(ctx, "whoami", "web", 3, "test"); err != nil {
			t.Fatal(err)
		}
		if err := d.StopApp(ctx, "whoami", nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Register(ctx, a, deployment("d2"), ownsIDs("d1"), nil); err != nil {
			t.Fatal(err)
		}
		if g := groupOf(t, nomad.Job("whoami"), "web"); *g.Count != 2 {
			t.Errorf("count = %d, want the starting 2", *g.Count)
		}
	})
}

func TestScaleAndState(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if st, err := d.ScaleState(ctx, "whoami"); err != nil || st.Exists {
		t.Fatalf("ScaleState of no job = %+v, %v; want not existing", st, err)
	}
	if _, err := d.Register(ctx, scaledWhoami(), deployment("d1"), ownsIDs(), nil); err != nil {
		t.Fatal(err)
	}
	st, err := d.ScaleState(ctx, "whoami")
	if err != nil || !st.Exists || st.Stopped || st.Rolling || st.Version != 0 || st.Counts["web"] != 1 {
		t.Fatalf("ScaleState = %+v, %v", st, err)
	}

	version, found, err := d.Scale(ctx, "whoami", "web", 2, "62 requests a second")
	if err != nil || !found || version != 1 {
		t.Fatalf("Scale = %d, %v, %v; want version 1 found", version, found, err)
	}
	reqs := nomad.Scales()
	if len(reqs) != 1 || reqs[0].Group != "web" || reqs[0].Count != 2 || reqs[0].Message != "62 requests a second" || reqs[0].Meta["caravana.scale"] != "1" {
		t.Errorf("scale requests = %+v", reqs)
	}
	if job := nomad.Job("whoami"); job.Meta["caravana.deployment"] != "d1" {
		t.Errorf("a scale changed the job's deployment meta: %v", job.Meta)
	}
	if st, _ := d.ScaleState(ctx, "whoami"); st.Counts["web"] != 2 || st.Version != 1 {
		t.Errorf("after the scale: %+v", st)
	}

	nomad.SetDeployment("whoami", 1, api.DeploymentStatusRunning)
	if st, _ := d.ScaleState(ctx, "whoami"); !st.Rolling {
		t.Error("a running Nomad deployment isn't Rolling")
	}
	nomad.SetDeployment("whoami", 1, api.DeploymentStatusSuccessful)
	if st, _ := d.ScaleState(ctx, "whoami"); st.Rolling {
		t.Error("a successful Nomad deployment is Rolling")
	}

	if err := d.StopApp(ctx, "whoami", nil, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := d.ScaleState(ctx, "whoami"); !st.Stopped {
		t.Error("a stopped job isn't Stopped")
	}

	nomad.RejectScale(500, "boom")
	if _, _, err := d.Scale(ctx, "whoami", "web", 3, "x"); !errors.Is(err, ErrNomadRejected) {
		t.Errorf("a refused scale = %v, want ErrNomadRejected", err)
	}
	if _, _, err := d.Scale(ctx, "ghost", "web", 3, "x"); err == nil {
		t.Error("scaling a job that doesn't exist succeeded")
	}
	nomad.Down()
	if _, err := d.ScaleState(ctx, "whoami"); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("ScaleState with Nomad down = %v, want ErrNomadUnavailable", err)
	}
}

// Copies of a job version a scale made show the deployment's version, in
// Copies, AllCopies and LogCopies; without a hook, or when it fails, they show
// Nomad's.
func TestVersionAliases(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "a1", TaskGroup: "web", ClientStatus: "running", JobVersion: 7},
		api.AllocationListStub{ID: "a2", TaskGroup: "web", ClientStatus: "running", JobVersion: 5},
		// A scale-down's copy: terminal, of the version before the scale. It stays hidden.
		api.AllocationListStub{ID: "a3", TaskGroup: "web", ClientStatus: "complete", JobVersion: 6},
	)
	versionsOf := func(copies []Copy) map[string]uint64 {
		out := map[string]uint64{}
		for _, c := range copies {
			out[c.ID] = c.Version
		}
		return out
	}

	copies, err := d.Copies(ctx, "whoami")
	if err != nil || versionsOf(copies)["a1"] != 7 {
		t.Fatalf("no hook: %v, %v; want Nomad's versions", versionsOf(copies), err)
	}

	d.VersionAliases = func(context.Context) (map[string]map[uint64]uint64, error) {
		return map[string]map[uint64]uint64{"whoami": {6: 5, 7: 5}, "other": {7: 1}}, nil
	}
	copies, err = d.Copies(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	got := versionsOf(copies)
	if got["a1"] != 5 || got["a2"] != 5 {
		t.Errorf("Copies versions = %v, want 5 for both running copies", got)
	}
	if _, shown := got["a3"]; shown {
		t.Errorf("Copies showed the scale-down's stopped copy: %v", got)
	}
	all, err := d.AllCopies(ctx)
	if err != nil || versionsOf(all["whoami"])["a1"] != 5 {
		t.Errorf("AllCopies = %v, %v", versionsOf(all["whoami"]), err)
	}
	logs, err := d.LogCopies(ctx, []string{"whoami"})
	if err != nil || versionsOf(logs)["a1"] != 5 || versionsOf(logs)["a3"] != 5 {
		t.Errorf("LogCopies = %v, %v", versionsOf(logs), err)
	}

	d.VersionAliases = func(context.Context) (map[string]map[uint64]uint64, error) {
		return nil, errors.New("the store is down")
	}
	copies, err = d.Copies(ctx, "whoami")
	if err != nil || versionsOf(copies)["a1"] != 7 {
		t.Errorf("a failing hook: %v, %v; want Nomad's versions and no error", versionsOf(copies), err)
	}
}
