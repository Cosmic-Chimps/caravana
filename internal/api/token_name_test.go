package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// tokenNameOf reads a deployment's tokenName from the API ("" when absent).
func tokenNameOf(t *testing.T, e *testEnv, appID, id string) string {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/"+appID+"/deployments/"+id, "", "")
	if status != http.StatusOK {
		t.Fatalf("getDeployment = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Deployment")
	return decodeAs[struct {
		TokenName string `json:"tokenName"`
	}](t, raw).TokenName
}

// A deploy made with an API token records the token's name, whichever way it
// deploys; a session and the webhook leave it out.
func TestDeployRecordsTokenName(t *testing.T) {
	e := newTestEnv(t, "") // e.URL sends the token named "tests"

	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)
	if got := tokenNameOf(t, e, "whoami", d.ID); got != "tests" {
		t.Errorf("deployApp: tokenName = %q, want tests", got)
	}
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments", "", "")
	if !strings.Contains(string(raw), `"tokenName":"tests"`) {
		t.Errorf("listDeployments = %d %s, want tokenName in it", status, raw)
	}

	// An empty-body deploy, a redeploy of a past version and a Compose deploy.
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "", "")
	if status != http.StatusAccepted {
		t.Fatalf("redeploy = %d %s", status, raw)
	}
	r := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "whoami", r.ID)
	if got := tokenNameOf(t, e, "whoami", r.ID); got != "tests" {
		t.Errorf("empty-body deploy: tokenName = %q, want tests", got)
	}
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments/"+d.ID+"/redeploy", "", "")
	if status != http.StatusAccepted {
		t.Fatalf("redeployDeployment = %d %s", status, raw)
	}
	v := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "whoami", v.ID)
	if got := tokenNameOf(t, e, "whoami", v.ID); got != "tests" {
		t.Errorf("redeployDeployment: tokenName = %q, want tests", got)
	}
	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/apps/blog/compose", "application/json",
		`{"compose": "services:\n  web:\n    image: traefik/whoami:latest\n"}`)
	if status != http.StatusAccepted {
		t.Fatalf("deployCompose = %d %s", status, raw)
	}
	c := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "blog", c.ID)
	if got := tokenNameOf(t, e, "blog", c.ID); got != "tests" {
		t.Errorf("deployCompose: tokenName = %q, want tests", got)
	}

	// A session (the console's calls) leaves it out.
	cookie := sessionCookie(t, e)
	status, raw = cookieCall(t, cookie, http.MethodPost, e.RawURL+"/v1/apps/whoami/deployments", "", nil)
	if status != http.StatusAccepted {
		t.Fatalf("deploy with a session = %d %s", status, raw)
	}
	s := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "whoami", s.ID)
	if got := tokenNameOf(t, e, "whoami", s.ID); got != "" || strings.Contains(string(raw), "tokenName") {
		t.Errorf("session deploy: tokenName = %q in %s, want none", got, raw)
	}
}

func TestPromoteAndWebhookTokenName(t *testing.T) {
	e := newTestEnv(t, "")
	shopEnvs(t, e)

	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop--staging--web/promote", "application/json", `{"to": "production"}`)
	if status != http.StatusAccepted {
		t.Fatalf("promoteApp = %d %s", status, raw)
	}
	p := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "shop--production--web", p.ID)
	if got := tokenNameOf(t, e, "shop--production--web", p.ID); got != "tests" {
		t.Errorf("promoteApp: tokenName = %q, want tests", got)
	}

	// The webhook has its own token: nothing is recorded.
	e2 := newTestEnv(t, "")
	tok := withWebhook(t, e2)
	e2.Registry.AddIndex("traefik/whoami", "latest", movedDigest)
	req, _ := http.NewRequest(http.MethodPost, e2.RawURL+"/v1/webhooks/whoami?token="+tok, nil)
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook = %d, want 202", resp.StatusCode)
	}
	list, _ := e2.Store.ListDeployments(context.Background(), "whoami", 1)
	if len(list) != 1 || list[0].Deployment.Trigger != app.TriggerWebhook || list[0].Deployment.TokenName != "" {
		t.Errorf("newest deployment = %+v, want a webhook deploy with no token name", list)
	}
}

// A deploy token's name is recorded like a full token's.
func TestDeployTokenNameIsRecorded(t *testing.T) {
	e := newTestEnv(t, "")
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	status, _, raw := callWith(t, auth, http.MethodPost, e.RawURL+"/v1/projects/shop/environments/staging/apps?deploy=false", "application/json", shopBody("web"))
	if status != http.StatusCreated {
		t.Fatalf("createEnvironmentApp = %d %s", status, raw)
	}
	status, _, raw = callWith(t, auth, http.MethodPost, e.RawURL+"/v1/apps/shop--staging--web/deployments", "application/json", webApp("staging"))
	if status != http.StatusAccepted {
		t.Fatalf("deployApp = %d %s", status, raw)
	}
	d := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "shop--staging--web", d.ID)
	if got := tokenNameOf(t, e, "shop--staging--web", d.ID); got != "shop-web-ci" {
		t.Errorf("tokenName = %q, want shop-web-ci", got)
	}
}
