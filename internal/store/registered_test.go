package store

import (
	"context"
	"testing"
)

// RegisteredAt is the first update that brings a job version: not the creation,
// not a later update, and nothing for a deployment that never registered.
func TestDeploymentRegisteredAt(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t) // the clock moves one second per read

	rec, err := s.CreateDeployment(ctx, "whoami", newDeployment("d1"), nil, nil, "")
	if err != nil || rec.RegisteredAt != nil {
		t.Fatalf("created: RegisteredAt = %v, %v; want none", rec.RegisteredAt, err)
	}
	// An update without a job version (the digests were resolved) registers nothing.
	rec, err = s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: StatusPending, Digests: map[string]string{"web": "x@sha256:a"}})
	if err == nil {
		t.Fatalf("pending -> pending should be refused, got %+v", rec)
	}

	rec, err = s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: StatusRunning, JobVersion: new(uint64(3))})
	if err != nil || rec.RegisteredAt == nil {
		t.Fatalf("registered: RegisteredAt = %v, %v; want a time", rec.RegisteredAt, err)
	}
	if !rec.RegisteredAt.After(rec.CreatedAt) || rec.RegisteredAt.After(rec.UpdatedAt) {
		t.Errorf("created %v, registered %v, updated %v: want created < registered <= updated", rec.CreatedAt, rec.RegisteredAt, rec.UpdatedAt)
	}
	first := *rec.RegisteredAt

	// The end of the rollout keeps it.
	rec, err = s.UpdateDeployment(ctx, "d1", DeploymentUpdate{Status: StatusHealthy})
	if err != nil || rec.RegisteredAt == nil || !rec.RegisteredAt.Equal(first) {
		t.Errorf("after the rollout: RegisteredAt = %v, %v; want %v", rec.RegisteredAt, err, first)
	}
	if !rec.UpdatedAt.After(first) {
		t.Errorf("UpdatedAt %v should be after RegisteredAt %v", rec.UpdatedAt, first)
	}

	// A deployment that fails before registering never has one.
	if _, err := s.CreateDeployment(ctx, "whoami", newDeployment("d2"), nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	failed, err := s.UpdateDeployment(ctx, "d2", DeploymentUpdate{Status: StatusFailed, Error: "image not found"})
	if err != nil || failed.RegisteredAt != nil {
		t.Errorf("failed before register: RegisteredAt = %v, %v; want none", failed.RegisteredAt, err)
	}
	list, err := s.ListDeployments(ctx, "whoami", 0)
	if err != nil || len(list) != 2 || list[0].RegisteredAt != nil || list[1].RegisteredAt == nil {
		t.Errorf("ListDeployments registered times wrong: %+v, %v", list, err)
	}
}
