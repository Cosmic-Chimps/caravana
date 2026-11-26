package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// An images-only deployApp (the stored app sent back with one image changed: what CI and the
// MCP tool do) on an app that keeps its Compose file keeps the file, with the new image
// written into it. A body that changes anything else still drops it.
func TestDeployAppImagesOnlyKeepsFile(t *testing.T) {
	e := newTestEnv(t, "")
	e.Registry.AddIndex("traefik/whoami", "v1.10", whoamiDigest)
	// The app has a health check, which a deploy from a definition needs (the stored app is
	// what the client sends back).
	first := composeBody(t, keptFile, map[string]any{
		"web": map[string]any{"health": map[string]any{"path": "/", "interval": "10s"}},
	})
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/compose", "application/json", first)
	if status != http.StatusAccepted {
		t.Fatalf("deployCompose: status %d: %s", status, raw)
	}
	waitHealthy(t, e, "whoami", decodeAs[deployment](t, raw).ID)
	url := e.URL + "/v1/apps/whoami/deployments"

	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	// The app as the API wrote it, with only the first service's image changed.
	next := decodeAs[struct {
		App map[string]any `json:"app"`
	}](t, raw).App
	web := next["services"].([]any)[0].(map[string]any)
	web["image"] = "traefik/whoami:v1.10"
	body, _ := json.Marshal(next)

	status, _, raw = call(t, http.MethodPost, url, "application/json", string(body))
	if status != http.StatusAccepted {
		t.Fatalf("status %d body %s, want 202", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Deployment")
	d := decodeAs[deployment](t, raw)
	waitHealthy(t, e, "whoami", d.ID)
	if !d.ComposeKept {
		t.Errorf("composeKept = false, want true (%s)", raw)
	}
	for _, w := range d.Warnings {
		if strings.Contains(w.Message, "Compose file was dropped") {
			t.Errorf("warnings = %+v, want no drop warning", d.Warnings)
		}
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/whoami/compose", "", "")
	if status != http.StatusOK {
		t.Fatalf("getCompose after the deploy: %d %s, want 200", status, raw)
	}
	got := decodeAs[keptRequest](t, raw)
	want := strings.Replace(keptFile, "traefik/whoami:latest", "traefik/whoami:v1.10", 1)
	if got.Compose != want {
		t.Errorf("kept file = %q, want %q", got.Compose, want)
	}

	// Anything beyond an image still drops the file.
	web["env"] = []any{map[string]any{"name": "MODE", "value": "x"}}
	body, _ = json.Marshal(next)
	status, _, raw = call(t, http.MethodPost, url, "application/json", string(body))
	if status != http.StatusAccepted {
		t.Fatalf("status %d body %s, want 202", status, raw)
	}
	if d := decodeAs[deployment](t, raw); d.ComposeKept {
		t.Errorf("composeKept = true after an env change, want false (%s)", raw)
	}
}
