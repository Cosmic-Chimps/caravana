package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// addrApp is an app with one service per name; each has a port, but the ones
// named in noPorts.
func addrApp(id string, services ...string) app.App {
	a := whoami()
	a.ID = id
	a.Services = nil
	for _, n := range services {
		svc := whoami().Services[0]
		svc.Name = n
		svc.Ports = []app.Port{{Name: "http", ContainerPort: 5432}}
		svc.Domains, svc.Health = nil, nil
		a.Services = append(a.Services, svc)
	}
	return a
}

func TestAppAddresses(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	insert := func(a app.App) {
		t.Helper()
		if _, err := e.store.InsertApp(ctx, a, nil); err != nil {
			t.Fatalf("InsertApp %s: %v", a.ID, err)
		}
	}
	insert(addrApp("postgres", "postgres"))
	insert(addrApp("web", "web", "worker"))
	insert(addrApp("rival", "postgres")) // claims rival-postgres and rival
	insert(addrApp("shop--staging--postgres", "postgres"))
	undefined := app.App{ID: "empty"}
	insert(undefined)
	stopped := addrApp("stopped", "cache")
	insert(stopped)
	now := time.Now()
	if err := e.store.SetAppStopped(ctx, "stopped", &now); err != nil {
		t.Fatalf("SetAppStopped: %v", err)
	}

	addresses := func(id string) []app.Address {
		t.Helper()
		rec, err := e.svc.GetApp(ctx, id)
		if err != nil {
			t.Fatalf("GetApp %s: %v", id, err)
		}
		got, err := e.svc.AppAddresses(ctx, rec)
		if err != nil {
			t.Fatalf("AppAddresses %s: %v", id, err)
		}
		return got
	}

	t.Run("one service answers to the app's name", func(t *testing.T) {
		got := addresses("postgres")
		if len(got) != 1 || !slices.Equal(got[0].Names, []string{"postgres-postgres", "postgres"}) || len(got[0].Conflicts) != 0 {
			t.Errorf("postgres = %+v, want postgres-postgres and postgres, no conflict", got)
		}
	})
	t.Run("a multi-service app has its own names", func(t *testing.T) {
		got := addresses("web")
		if len(got) != 2 || got[0].Own != "web" || got[1].Own != "worker" || !slices.Equal(got[1].Names, []string{"web-worker"}) {
			t.Errorf("web = %+v", got)
		}
	})
	t.Run("a stopped app and an undefined one claim nothing, and have no addresses", func(t *testing.T) {
		if got := addresses("empty"); got != nil {
			t.Errorf("undefined app has addresses %+v", got)
		}
		// The stopped app's own addresses are still said (it can start again).
		if got := addresses("stopped"); len(got) != 1 {
			t.Errorf("stopped = %+v", got)
		}
	})
	t.Run("another environment never conflicts", func(t *testing.T) {
		got := addresses("shop--staging--postgres")
		if len(got) != 1 || len(got[0].Conflicts) != 0 || len(got[0].Names) != 2 {
			t.Errorf("staging postgres = %+v, want no conflict", got)
		}
	})
}

// A second app that claims a name makes both answer to nothing at it.
func TestAppAddressesConflict(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for _, a := range []app.App{addrApp("db", "postgres"), addrApp("db-postgres", "x")} {
		if _, err := e.store.InsertApp(ctx, a, nil); err != nil {
			t.Fatalf("InsertApp: %v", err)
		}
	}
	rec, _ := e.svc.GetApp(ctx, "db")
	got, err := e.svc.AppAddresses(ctx, rec)
	if err != nil {
		t.Fatalf("AppAddresses: %v", err)
	}
	// db's postgres claims db-postgres and db; the app db-postgres claims
	// db-postgres-x and db-postgres: the second is taken.
	if len(got) != 1 || !slices.Equal(got[0].Names, []string{"db"}) ||
		len(got[0].Conflicts) != 1 || got[0].Conflicts[0].Name != "db-postgres" || got[0].Conflicts[0].App != "db-postgres" {
		t.Errorf("db = %+v", got)
	}
}
