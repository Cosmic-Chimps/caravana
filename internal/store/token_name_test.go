package store

import (
	"context"
	"testing"
)

func TestDeploymentTokenName(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t)

	plain, err := s.CreateDeployment(ctx, "whoami", newDeployment("d1"), nil, nil, "")
	if err != nil || plain.Deployment.TokenName != "" {
		t.Fatalf("plain deployment token name = %q, %v; want none", plain.Deployment.TokenName, err)
	}
	d := newDeployment("d2")
	d.TokenName = "ci-github"
	rec, err := s.CreateDeployment(ctx, "whoami", d, nil, nil, "")
	if err != nil || rec.Deployment.TokenName != "ci-github" {
		t.Fatalf("created token name = %q, %v; want ci-github", rec.Deployment.TokenName, err)
	}

	// Every read has it, and a status update keeps it.
	if _, err := s.UpdateDeployment(ctx, "d2", DeploymentUpdate{Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDeployment(ctx, "d2")
	if err != nil || got.Deployment.TokenName != "ci-github" {
		t.Errorf("GetDeployment token name = %q, %v", got.Deployment.TokenName, err)
	}
	list, err := s.ListDeployments(ctx, "whoami", 0)
	if err != nil || len(list) != 2 || list[0].Deployment.TokenName != "ci-github" || list[1].Deployment.TokenName != "" {
		t.Errorf("ListDeployments token names wrong: %+v, %v", list, err)
	}
	latest, err := s.LatestDeployments(ctx)
	if err != nil || latest["whoami"].Deployment.TokenName != "ci-github" {
		t.Errorf("LatestDeployments token name = %q, %v", latest["whoami"].Deployment.TokenName, err)
	}
}
