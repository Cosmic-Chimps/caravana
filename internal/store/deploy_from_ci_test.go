package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func TestAPITokenScope(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	t0 := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

	full := APITokenRecord{ID: "id1", Name: "full", Hash: "h1", CreatedAt: t0}
	scoped := APITokenRecord{ID: "id2", Name: "shop-web-ci", Hash: "h2", CreatedAt: t0.Add(time.Minute),
		ScopeProject: "shop", ScopeApp: "web"}
	for _, r := range []APITokenRecord{full, scoped} {
		if err := s.CreateAPIToken(ctx, r, t0); err != nil {
			t.Fatalf("create %s: %v", r.Name, err)
		}
	}

	got, err := s.GetAPITokenByHash(ctx, "h2")
	if err != nil || got.ScopeProject != "shop" || got.ScopeApp != "web" {
		t.Errorf("scoped token = %+v, %v; want scope shop/web", got, err)
	}
	got, err = s.GetAPITokenByHash(ctx, "h1")
	if err != nil || got.ScopeProject != "" || got.ScopeApp != "" {
		t.Errorf("full token = %+v, %v; want no scope", got, err)
	}

	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 2 || list[0].ScopeApp != "web" || list[1].ScopeApp != "" {
		t.Errorf("ListAPITokens = %+v, %v", list, err)
	}
	byName, err := s.ActiveAPITokenByName(ctx, "shop-web-ci", t0)
	if err != nil || byName.ScopeProject != "shop" {
		t.Errorf("ActiveAPITokenByName = %+v, %v", byName, err)
	}
}

func TestDeploymentOrigin(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t)

	// No origin: none comes back.
	plain, err := s.CreateDeployment(ctx, "whoami", newDeployment("d1"), nil, nil, "")
	if err != nil || plain.Deployment.Origin != nil {
		t.Fatalf("plain deployment = %+v, %v; want no origin", plain.Deployment.Origin, err)
	}
	// An empty origin is stored as none.
	d := newDeployment("d2")
	d.Origin = &app.Origin{}
	if rec, err := s.CreateDeployment(ctx, "whoami", d, nil, nil, ""); err != nil || rec.Deployment.Origin != nil {
		t.Fatalf("empty origin = %+v, %v; want none", rec.Deployment.Origin, err)
	}

	want := app.Origin{Commit: "abc1234", Ref: "refs/heads/main", Actor: "ana",
		Run: "https://github.com/o/r/actions/runs/7", Repository: "o/r"}
	d = newDeployment("d3")
	d.Origin = &want
	rec, err := s.CreateDeployment(ctx, "whoami", d, nil, nil, "")
	if err != nil || rec.Deployment.Origin == nil || !reflect.DeepEqual(*rec.Deployment.Origin, want) {
		t.Fatalf("created = %+v, %v; want origin %+v", rec.Deployment.Origin, err, want)
	}

	// Every read has it, and a status update keeps it.
	if _, err := s.UpdateDeployment(ctx, "d3", DeploymentUpdate{Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDeployment(ctx, "d3")
	if err != nil || got.Deployment.Origin == nil || *got.Deployment.Origin != want {
		t.Errorf("GetDeployment origin = %+v, %v", got.Deployment.Origin, err)
	}
	list, err := s.ListDeployments(ctx, "whoami", 0)
	if err != nil || len(list) != 3 || list[0].Deployment.Origin == nil || list[1].Deployment.Origin != nil {
		t.Errorf("ListDeployments origins wrong: %+v, %v", list, err)
	}
	latest, err := s.LatestDeployments(ctx)
	if err != nil || latest["whoami"].Deployment.Origin == nil {
		t.Errorf("LatestDeployments origin = %+v, %v", latest["whoami"].Deployment.Origin, err)
	}
}
