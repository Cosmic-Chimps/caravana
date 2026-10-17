package api

import (
	"net/http"
	"strings"
	"testing"
)

// FR-011: the lists across apps name where each app is, and a promotion shows
// as one.
func TestListsCarryThePlace(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	_, stage := shopEnvs(t, e)

	// A deploy that fails puts its app on the needs-you list.
	bad := strings.Replace(webApp("staging"), "traefik/whoami:latest", "traefik/whoami:nope", 1)
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop--staging--bad/deployments", "application/json", bad); status != http.StatusUnprocessableEntity {
		t.Fatalf("the failing deploy = %d %s", status, raw)
	}
	// And a promotion is a deployment with a source in another environment.
	status, _, raw := call(t, http.MethodPost, promoteURL(e, "shop--staging--web", "promote"), "application/json", `{"to": "production"}`)
	if status != http.StatusAccepted {
		t.Fatalf("promote = %d %s", status, raw)
	}
	promoted := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "shop--production--web", promoted.ID)

	t.Run("listRecentDeployments", func(t *testing.T) {
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/deployments?limit=20", "", "")
		checkSchema(t, doc, raw, "DeploymentList")
		list := decodeAs[struct {
			Deployments []struct {
				ID           string `json:"id"`
				AppID        string `json:"appId"`
				Project      string `json:"project"`
				Environment  string `json:"environment"`
				AppName      string `json:"appName"`
				PromotedFrom *struct {
					App, Environment, Deployment string
				} `json:"promotedFrom"`
			} `json:"deployments"`
		}](t, raw)
		want := map[string][3]string{
			"shop--production--web": {"shop", "production", "web"},
			"shop--staging--web":    {"shop", "staging", "web"},
			"shop--staging--bad":    {"shop", "staging", "bad"},
		}
		seen := map[string]bool{}
		for _, d := range list.Deployments {
			w, ok := want[d.AppID]
			if !ok {
				t.Errorf("unexpected deployment of %s", d.AppID)
				continue
			}
			seen[d.AppID] = true
			if d.Project != w[0] || d.Environment != w[1] || d.AppName != w[2] {
				t.Errorf("%s: place %s/%s/%s, want %v", d.AppID, d.Project, d.Environment, d.AppName, w)
			}
			isPromotion := d.ID == promoted.ID
			if isPromotion != (d.PromotedFrom != nil) {
				t.Errorf("%s (%s): promotedFrom = %+v", d.AppID, d.ID, d.PromotedFrom)
			}
			if isPromotion && (d.PromotedFrom.App != "shop--staging--web" || d.PromotedFrom.Environment != "staging" || d.PromotedFrom.Deployment != stage.ID) {
				t.Errorf("promotedFrom = %+v", d.PromotedFrom)
			}
		}
		if len(seen) != len(want) {
			t.Errorf("deployments of %v, want all of %v", seen, want)
		}
	})

	t.Run("listAttention", func(t *testing.T) {
		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/attention", "", "")
		got := decodeAs[struct {
			Items []struct {
				Kind        string `json:"kind"`
				AppID       string `json:"appId"`
				Project     string `json:"project"`
				Environment string `json:"environment"`
				AppName     string `json:"appName"`
			} `json:"items"`
		}](t, raw)
		var found bool
		for _, it := range got.Items {
			if it.Kind == "deploy_failed" && it.AppID == "shop--staging--bad" {
				found = true
				if it.Project != "shop" || it.Environment != "staging" || it.AppName != "bad" {
					t.Errorf("item = %+v, want shop/staging/bad", it)
				}
			}
		}
		if !found {
			t.Errorf("attention = %s, want the failed deploy of shop--staging--bad", raw)
		}
	})
}
