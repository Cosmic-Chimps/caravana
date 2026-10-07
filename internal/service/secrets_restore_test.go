package service

import (
	"context"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// revertedRollout is how Nomad reports a rollout that failed and went back.
var revertedRollout = api.Deployment{Status: api.DeploymentStatusFailed,
	StatusDescription: "Failed due to unhealthy allocations - rolling back to job version 0"}

// stuckRollout never ends.
var stuckRollout = api.Deployment{Status: api.DeploymentStatusRunning, StatusDescription: "Deployment is running"}

var (
	runningSets = secrets.Sets{"web": {"API_KEY": "good"}}
	badSets     = secrets.Sets{"web": {"API_KEY": "bad"}}
)

// waitLive waits until appID's live secrets are want and nothing is kept.
func waitLive(t *testing.T, e *env, appID string, want secrets.Sets) {
	t.Helper()
	waitFor(t, "the secrets to settle", func() bool {
		owner, _ := e.secrets.Kept(appID)
		return owner == "" && e.secrets.Live(appID).Equal(want)
	})
}

// US4, FR-013: a deploy whose copies never get healthy puts the running
// version's secrets back.
func TestFailedRolloutRestoresSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "good"))) // job version 0
	e.nomad.Script("whoami", 1, stuckRollout)

	rec, err := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "bad")), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.secrets.Live("whoami"); !got.Equal(badSets) {
		t.Fatalf("live during the rollout = %v, want the new value (the new copies read it)", got)
	}
	e.nomad.Script("whoami", 1, revertedRollout)
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusReverted {
		t.Fatalf("deploy = %s, want reverted", final.Status)
	}
	waitLive(t, e, "whoami", runningSets)

	// The job holds no value, and its template never re-renders a running copy.
	tmpl := e.nomad.Job("whoami").TaskGroups[0].Tasks[0].Templates[0]
	if *tmpl.ChangeMode != "noop" {
		t.Errorf("change mode = %s, want noop", *tmpl.ChangeMode)
	}
}

// FR-013 across a restart: the next start settles the deployment and puts
// the secrets back.
func TestRestartMidRolloutRestoresSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "good")))
	e.nomad.Script("whoami", 1, stuckRollout)

	// A second Caravana deploys and stops while the rollout runs.
	first := New(e.store, newDeployer(t, e.registry, e.nomad), e.secrets, discardLogger())
	firstCtx, stop := context.WithCancel(ctx)
	if err := first.Start(firstCtx); err != nil {
		t.Fatal(err)
	}
	rec, err := first.Deploy(ctx, withSecrets(set("API_KEY", "bad")), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	first.Wait()
	if got, _ := e.store.GetDeployment(ctx, rec.Deployment.ID); got.Status != store.StatusRunning {
		t.Fatalf("after the stop the deployment is %s, want still running", got.Status)
	}

	// The rollout fails while nobody watches; the next start finds it.
	e.nomad.Script("whoami", 1, revertedRollout)
	startService(t, e.store, e.registry, e.nomad, e.secrets)
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusReverted {
		t.Fatalf("deploy = %s, want reverted", final.Status)
	}
	waitLive(t, e, "whoami", runningSets)
}

// A deploy that starts while an older one still rolls out takes the kept
// copy over: when it fails, the version that ran before both comes back.
func TestNewerDeployTakesTheKeptSecretsOver(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "good"))) // version 0
	e.nomad.Script("whoami", 1, stuckRollout)
	e.nomad.Script("whoami", 2, stuckRollout, revertedRollout)

	older, err := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "older")), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "bad")), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if final := waitFinal(t, e.store, older.Deployment.ID); final.Status != store.StatusSuperseded {
		t.Errorf("older = %s, want superseded", final.Status)
	}
	if final := waitFinal(t, e.store, newer.Deployment.ID); final.Status != store.StatusReverted {
		t.Fatalf("newer = %s, want reverted", final.Status)
	}
	waitLive(t, e, "whoami", runningSets)
}
