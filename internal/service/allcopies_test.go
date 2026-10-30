package service

import (
	"context"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// AllCopies lists the copies of the apps only, by app ID: the jobs that aren't apps are left out, and
// an app Nomad knows nothing of has an empty list.
func TestAllCopiesAreTheAppsOnly(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := apptest.Whoami().App
	saveApp(t, e, a)
	e.nomad.SetCopies(a.ID, api.AllocationListStub{TaskGroup: "web", ClientStatus: "running"})
	e.nomad.SetCopies("traefik", api.AllocationListStub{TaskGroup: "traefik", ClientStatus: "running"})
	got, err := e.svc.AllCopies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[a.ID]) != 1 {
		t.Errorf("AllCopies = %v, want one app with one copy", got)
	}
	e.nomad.Down()
	if _, err := e.svc.AllCopies(ctx); err == nil {
		t.Error("AllCopies with Nomad down answered")
	}
}
