package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// originHeadersFor is a full set of Caravana-Origin-* headers.
func originHeadersFor(commit string) [][2]string {
	return [][2]string{
		{app.HeaderOriginCommit, commit},
		{app.HeaderOriginRef, "refs/heads/main"},
		{app.HeaderOriginActor, "ana"},
		{app.HeaderOriginRun, "https://github.com/acme/shop/actions/runs/212"},
		{app.HeaderOriginRepository, "acme/shop"},
	}
}

// originOfDeployment reads a deployment's origin object, decoded, from the API.
func originOfDeployment(t *testing.T, e *testEnv, appID, id string) map[string]string {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/"+appID+"/deployments/"+id, "", "")
	if status != http.StatusOK {
		t.Fatalf("getDeployment = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Deployment")
	return decodeAs[struct {
		Origin map[string]string `json:"origin"`
	}](t, raw).Origin
}

func TestDeployRecordsOrigin(t *testing.T) {
	e := newTestEnv(t, "")
	url := e.URL + "/v1/apps/whoami/deployments"

	status, _, raw := callHeaders(t, http.MethodPost, url, whoamiBody, originHeadersFor("ABC1234DEF"))
	if status != http.StatusAccepted {
		t.Fatalf("deployApp with origin headers = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Deployment")
	d := decodeAs[struct {
		deployment
		Origin map[string]string `json:"origin"`
	}](t, raw)
	want := map[string]string{"commit": "abc1234def", "ref": "refs/heads/main", "actor": "ana",
		"run": "https://github.com/acme/shop/actions/runs/212", "repository": "acme/shop"}
	for k, v := range want {
		if d.Origin[k] != v {
			t.Errorf("origin.%s in the answer = %q, want %q (origin %v)", k, d.Origin[k], v, d.Origin)
		}
	}
	if d.Trigger != "manual" {
		t.Errorf("trigger = %q, want manual", d.Trigger)
	}
	waitHealthy(t, e, "whoami", d.ID)
	if got := originOfDeployment(t, e, "whoami", d.ID); got["commit"] != "abc1234def" || got["actor"] != "ana" {
		t.Errorf("origin read back = %v", got)
	}
	// It is in the list, too.
	status, _, raw = call(t, http.MethodGet, url, "", "")
	if !strings.Contains(string(raw), `"commit":"abc1234def"`) {
		t.Errorf("listDeployments = %d %s, want the origin in it", status, raw)
	}

	// A redeploy with only some headers records only those; none, no origin.
	status, _, raw = callHeaders(t, http.MethodPost, url, "", [][2]string{{app.HeaderOriginActor, "bo"}})
	if status != http.StatusAccepted {
		t.Fatalf("redeploy with an actor = %d %s", status, raw)
	}
	r := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "whoami", r.ID)
	if got := originOfDeployment(t, e, "whoami", r.ID); len(got) != 1 || got["actor"] != "bo" {
		t.Errorf("partial origin = %v, want only the actor", got)
	}
	plain := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", plain.ID)
	if got := originOfDeployment(t, e, "whoami", plain.ID); got != nil {
		t.Errorf("origin of a deploy with no headers = %v, want absent", got)
	}
	// An empty header is an absent one.
	status, _, raw = callHeaders(t, http.MethodPost, url, "", [][2]string{{app.HeaderOriginCommit, ""}})
	if status != http.StatusAccepted || strings.Contains(string(raw), `"origin"`) {
		t.Errorf("an empty origin header = %d %s, want 202 and no origin", status, raw)
	}
}

func TestOriginHeaderErrors(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)
	before, _ := e.Store.ListDeployments(context.Background(), "whoami", 0)

	long := strings.Repeat("x", 600)
	tests := []struct {
		name   string
		header [2]string
		field  string
	}{
		{"commit too short", [2]string{app.HeaderOriginCommit, "abc12"}, app.HeaderOriginCommit},
		{"commit not hex", [2]string{app.HeaderOriginCommit, "zzzzzzzz"}, app.HeaderOriginCommit},
		{"ref too long", [2]string{app.HeaderOriginRef, long}, app.HeaderOriginRef},
		{"actor too long", [2]string{app.HeaderOriginActor, strings.Repeat("a", 101)}, app.HeaderOriginActor},
		{"run over http", [2]string{app.HeaderOriginRun, "http://github.com/x"}, app.HeaderOriginRun},
		{"run not a URL", [2]string{app.HeaderOriginRun, "javascript:alert(1)"}, app.HeaderOriginRun},
		{"repository too long", [2]string{app.HeaderOriginRepository, strings.Repeat("r", 201)}, app.HeaderOriginRepository},
	}
	for _, tt := range tests {
		for _, call := range []struct{ name, method, path, body string }{
			{"deployApp", http.MethodPost, "/v1/apps/whoami/deployments", whoamiBody},
			{"redeploy", http.MethodPost, "/v1/apps/whoami/deployments", ""},
			{"deployCompose", http.MethodPost, "/v1/apps/blog/compose", `{"compose": "services:\n  web:\n    image: traefik/whoami:latest\n"}`},
			{"promoteApp", http.MethodPost, "/v1/apps/whoami/promote", `{"to": "production"}`},
		} {
			t.Run(call.name+"/"+tt.name, func(t *testing.T) {
				status, _, raw := callHeaders(t, call.method, e.URL+call.path, call.body, [][2]string{tt.header})
				wantError(t, tt.name, status, raw, http.StatusUnprocessableEntity, codeInvalidField, tt.field)
			})
		}
	}
	t.Run("a header sent twice", func(t *testing.T) {
		status, _, raw := callHeaders(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "",
			[][2]string{{app.HeaderOriginActor, "ana"}, {app.HeaderOriginActor, "bo"}})
		wantError(t, "twice", status, raw, http.StatusUnprocessableEntity, codeInvalidField, app.HeaderOriginActor)
	})

	after, _ := e.Store.ListDeployments(context.Background(), "whoami", 0)
	if len(after) != len(before) {
		t.Errorf("%d deployments after the refusals, %d before: a bad header must deploy nothing", len(after), len(before))
	}
	if a, _ := e.Store.GetApp(context.Background(), "blog"); a.App.ID != "" {
		t.Error("a refused deployCompose saved the app")
	}
}

func TestPromoteAndWebhookRecordOrigin(t *testing.T) {
	e := newTestEnv(t, "")
	_, staging := shopEnvs(t, e)

	status, _, raw := callHeaders(t, http.MethodPost, e.URL+"/v1/apps/shop--staging--web/promote", `{"to": "production"}`,
		originHeadersFor("abc1234"))
	if status != http.StatusAccepted {
		t.Fatalf("promoteApp = %d %s", status, raw)
	}
	p := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "shop--production--web", p.ID)
	if got := originOfDeployment(t, e, "shop--production--web", p.ID); got["commit"] != "abc1234" || got["repository"] != "acme/shop" {
		t.Errorf("origin of a promotion = %v", got)
	}
	_ = staging

	// The webhook: headers are read after the token, and recorded; trigger webhook.
	e2 := newTestEnv(t, "")
	tok := withWebhook(t, e2)
	e2.Registry.AddIndex("traefik/whoami", "latest", movedDigest)
	req, _ := http.NewRequest(http.MethodPost, e2.RawURL+"/v1/webhooks/whoami?token="+tok, nil)
	for _, h := range originHeadersFor("def5678") {
		req.Header.Set(h[0], h[1])
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook = %d, want 202 (the tag moved)", resp.StatusCode)
	}
	list, _ := e2.Store.ListDeployments(context.Background(), "whoami", 1)
	if len(list) != 1 || list[0].Deployment.Trigger != app.TriggerWebhook || list[0].Deployment.Origin == nil ||
		list[0].Deployment.Origin.Commit != "def5678" {
		t.Errorf("newest deployment = %+v, want a webhook deploy with the origin", list)
	}

	// A bad header on the webhook is 422, and a wrong token still gets the 401 first.
	bad := func(token string) int {
		r, _ := http.NewRequest(http.MethodPost, e2.RawURL+"/v1/webhooks/whoami?token="+token, nil)
		r.Header.Set(app.HeaderOriginCommit, "nope")
		resp, err := noRedirects.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := bad(tok); got != http.StatusUnprocessableEntity {
		t.Errorf("webhook with a bad origin header = %d, want 422", got)
	}
	if got := bad("cvwh_wrong"); got != http.StatusUnauthorized {
		t.Errorf("webhook with a wrong token and a bad header = %d, want 401", got)
	}
}
