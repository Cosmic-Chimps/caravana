package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// withData is whoami, one copy, with a volume.
func withData() app.App {
	a := whoami()
	a.Services[0].Replicas = nil
	a.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	return a
}

// US4, US5: the first deploy creates the volume, a redeploy keeps it, and
// deleting the app deletes it.
func TestVolumesLiveWithTheApp(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withData())
	if got := e.nomad.HostVolumes(); !slices.Equal(got, []string{"whoami_data"}) {
		t.Fatalf("volumes = %v, want whoami_data", got)
	}
	rec, err := e.svc.Redeploy(ctx, "whoami", app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("redeploy = %s (%s)", final.Status, final.Error)
	}
	if got := e.nomad.HostVolumes(); !slices.Equal(got, []string{"whoami_data"}) {
		t.Errorf("volumes after the redeploy = %v", got)
	}

	// A deploy that stops mounting it warns, and keeps it.
	rec, err = e.svc.Deploy(ctx, func() app.App { a := withData(); a.Services[0].Volumes = nil; return a }(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Warnings) == 0 || rec.Warnings[0].Field != "" || len(e.nomad.HostVolumes()) != 1 {
		t.Errorf("warnings = %+v, volumes = %v; want a warning and the volume kept", rec.Warnings, e.nomad.HostVolumes())
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if got := e.nomad.HostVolumes(); len(got) != 0 {
		t.Errorf("volumes after delete = %v", got)
	}
}

func TestDeleteAppKeepsTheAppWhenVolumesCantGo(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withData())
	e.nomad.FailVolumes(500)
	if err := e.svc.DeleteApp(ctx, "whoami"); !errors.Is(err, deploy.ErrNomadRejected) {
		t.Fatalf("DeleteApp = %v, want ErrNomadRejected", err)
	}
	if _, err := e.svc.GetApp(ctx, "whoami"); err != nil {
		t.Fatalf("the app is gone although its volume wasn't deleted: %v", err)
	}
	e.nomad.FailVolumes(0)
	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatalf("second DeleteApp: %v", err)
	}
}
