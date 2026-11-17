package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// PreviewMoveOff says what MoveWorkOff would do, with the same classification, and changes nothing.
func TestPreviewMoveOff(t *testing.T) {
	ctx := context.Background()
	e := newMoveEnv(t)
	e.saveApp(t, "blog", nil, map[string][]string{"web": {"2", "2"}})
	e.saveApp(t, "db", func(a *app.App) {
		a.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
		a.Services[0].Replicas = nil
	}, map[string][]string{"web": {"2"}})
	e.nomad.SetTraefikJob("traefik:v3")
	e.nomad.SetCopies("traefik", moveCopyStub("tf-0", "traefik", "node-2", "two"))

	p, err := e.svc.PreviewMoveOff(ctx, "two")
	if err != nil {
		t.Fatalf("PreviewMoveOff: %v", err)
	}
	if len(p.Moves) != 1 || p.Moves[0] != (MovePreviewItem{App: "blog", Service: "web", Copies: 2}) || p.Copies() != 2 {
		t.Errorf("moves = %+v", p.Moves)
	}
	reasons := map[string]string{}
	for _, st := range p.Stays {
		reasons[st.App+"/"+st.Service] = st.Reason
	}
	if reasons["db/web"] != "its data is on this server" || reasons["traefik/"] != "the proxy runs here" || len(reasons) != 2 {
		t.Errorf("stays = %v", reasons)
	}
	if len(e.nomad.EligibilityRequests()) != 0 || len(e.nomad.StoppedCopies()) != 0 {
		t.Error("a preview changed something")
	}
	if v, _ := e.svc.Server(ctx, "two"); v.Move != nil || !v.AcceptsWork {
		t.Errorf("a preview left a move: %+v", v.Move)
	}

	// The refusals are MoveWorkOff's.
	if _, err := e.svc.PreviewMoveOff(ctx, "nope"); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("unknown = %v", err)
	}
	e.nomad.SetNodeEligibilityOf("node-1", "ineligible", false)
	if _, err := e.svc.PreviewMoveOff(ctx, "two"); !errors.Is(err, ErrNoOtherServer) {
		t.Errorf("no other server = %v", err)
	}
	e.nomad.SetNodeEligibilityOf("node-1", "eligible", false)
	e.svc.SetMoveTimingForTests(10*time.Millisecond, 3*time.Second, 5*time.Millisecond)
	if _, err := e.svc.MoveWorkOff(ctx, "two", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PreviewMoveOff(ctx, "two"); !errors.Is(err, ErrServerMoving) {
		t.Errorf("moving = %v", err)
	}
	e.waitMove(t, "done")
}
