package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A deployment says when Nomad accepted its job (registeredAt), between its
// creation and its last update; the field is always present, null until then.
func TestDeploymentRegisteredAt(t *testing.T) {
	e := newTestEnv(t, "")

	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments/"+d.ID, "", "")
	if status != http.StatusOK {
		t.Fatalf("getDeployment = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Deployment")
	got := decodeAs[struct {
		CreatedAt    time.Time  `json:"createdAt"`
		UpdatedAt    time.Time  `json:"updatedAt"`
		RegisteredAt *time.Time `json:"registeredAt"`
	}](t, raw)
	if got.RegisteredAt == nil {
		t.Fatalf("a healthy deployment has no registeredAt: %s", raw)
	}
	if got.RegisteredAt.Before(got.CreatedAt) || got.RegisteredAt.After(got.UpdatedAt) {
		t.Errorf("registeredAt %s should be between createdAt %s and updatedAt %s",
			got.RegisteredAt, got.CreatedAt, got.UpdatedAt)
	}

	// The list carries it too.
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/deployments", "", "")
	if status != http.StatusOK {
		t.Fatalf("listDeployments = %d %s", status, raw)
	}
	if !strings.Contains(string(raw), `"registeredAt"`) {
		t.Errorf("listDeployments has no registeredAt: %s", raw)
	}
}
