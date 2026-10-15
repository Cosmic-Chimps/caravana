package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// whoamiEnv is whoamiBody with an env value and a secret.
func whoamiEnv(mode string) string {
	return strings.Replace(whoamiBody, `"image": "traefik/whoami:latest",`,
		`"image": "traefik/whoami:latest", "env": [{"name": "MODE", "value": "`+mode+`"}],`+
			`"secrets": [{"name": "TOKEN", "value": "api-secret-value"}],`, 1)
}

// Feature 011: redeploy a past version, export the live one and a past one.
func TestRedeployAndExport(t *testing.T) {
	e := newTestEnv(t, "")
	base := e.URL + "/v1/apps/whoami"

	first := deployOK(t, e, "whoami", whoamiEnv("a"))
	waitHealthy(t, e, "whoami", first.ID)
	second := deployOK(t, e, "whoami", whoamiEnv("b"))
	waitHealthy(t, e, "whoami", second.ID)

	t.Run("a deployment says whether it can be redeployed", func(t *testing.T) {
		_, _, raw := call(t, http.MethodGet, base+"/deployments/"+first.ID, "", "")
		if !strings.Contains(string(raw), `"definitionRecorded":true`) {
			t.Errorf("deployment = %s, want definitionRecorded true", raw)
		}
	})

	t.Run("redeploy a past version", func(t *testing.T) {
		status, header, raw := call(t, http.MethodPost, base+"/deployments/"+first.ID+"/redeploy", "", "")
		if status != http.StatusAccepted {
			t.Fatalf("status %d body %s, want 202", status, raw)
		}
		checkSchema(t, loadContract(t), raw, "Deployment")
		d := decodeAs[struct {
			deployment
			Source string `json:"source"`
		}](t, raw)
		if d.Source != first.ID || d.Digests["web"] != first.Digests["web"] {
			t.Errorf("redeploy = %s, want source %s with its digest", raw, first.ID)
		}
		if loc := header.Get("Location"); loc != "/v1/apps/whoami/deployments/"+d.ID {
			t.Errorf("Location = %q", loc)
		}
		waitHealthy(t, e, "whoami", d.ID)
	})

	t.Run("export the live version", func(t *testing.T) {
		status, header, raw := call(t, http.MethodGet, base+"/jobspec", "", "")
		if status != http.StatusOK {
			t.Fatalf("status %d body %s, want 200", status, raw)
		}
		if ct := header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		if cd := header.Get("Content-Disposition"); cd != `attachment; filename=whoami.nomad.hcl` {
			t.Errorf("Content-Disposition = %q", cd)
		}
		body := string(raw)
		if !strings.Contains(body, `job "whoami" {`) || !strings.Contains(body, `"MODE" = "a"`) {
			t.Errorf("the live export isn't the redeployed version A:\n%s", body)
		}
		if strings.Contains(body, "api-secret-value") || strings.Contains(body, e.Token) {
			t.Error("the export holds a secret value or a token")
		}
	})

	t.Run("export a past version", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, base+"/deployments/"+second.ID+"/jobspec", "", "")
		if status != http.StatusOK || !strings.Contains(string(raw), `"MODE" = "b"`) {
			t.Errorf("status %d body %s, want version B", status, raw)
		}
	})
}

func TestRedeployAndExportErrors(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t, "")
	if _, err := e.Store.SaveApp(ctx, apptest.Whoami().App, nil); err != nil {
		t.Fatal(err)
	}
	def := apptest.Whoami().App
	record := func(id string, def *app.App, final store.Status) {
		t.Helper()
		if _, err := e.Store.CreateDeployment(ctx, "whoami", app.Deployment{ID: id, Trigger: app.TriggerManual,
			Digests: map[string]string{"web": apptest.WhoamiDigest}}, def, nil, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: final}); err != nil {
			t.Fatal(err)
		}
	}
	base := e.URL + "/v1/apps/whoami"

	// No healthy deployment yet: the live export has nothing to export.
	expect(t, http.MethodGet, base+"/jobspec", http.StatusConflict, codeNoLiveDeployment)

	record("failed-one", &def, store.StatusFailed)
	record("old", nil, store.StatusFailed)

	tests := []struct {
		name, method, url string
		status            int
		code              string
	}{
		{"redeploy a failed deployment", http.MethodPost, base + "/deployments/failed-one/redeploy", http.StatusConflict, codeDeploymentNotHealthy},
		{"redeploy one without a definition", http.MethodPost, base + "/deployments/old/redeploy", http.StatusConflict, codeDefinitionNotRecorded},
		{"export one without a definition", http.MethodGet, base + "/deployments/old/jobspec", http.StatusConflict, codeDefinitionNotRecorded},
		{"redeploy an unknown deployment", http.MethodPost, base + "/deployments/nope/redeploy", http.StatusNotFound, codeNotFound},
		{"export an unknown app", http.MethodGet, e.URL + "/v1/apps/nope/jobspec", http.StatusNotFound, codeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { expect(t, tt.method, tt.url, tt.status, tt.code) })
	}

	t.Run("an image gone from its registry", func(t *testing.T) {
		if _, err := e.Store.CreateDeployment(ctx, "whoami", app.Deployment{ID: "gone", Trigger: app.TriggerManual,
			Digests: map[string]string{"web": "traefik/whoami@sha256:" + strings.Repeat("e", 64)}}, &def, nil, ""); err != nil {
			t.Fatal(err)
		}
		for _, st := range []store.Status{store.StatusRunning, store.StatusHealthy} {
			if _, err := e.Store.UpdateDeployment(ctx, "gone", store.DeploymentUpdate{Status: st}); err != nil {
				t.Fatal(err)
			}
		}
		raw := expect(t, http.MethodPost, base+"/deployments/gone/redeploy", http.StatusUnprocessableEntity, codeImageNotFound)
		if f := decodeAs[errorBody](t, raw).Error.Field; f != "services[0].image" {
			t.Errorf("field = %q, want services[0].image", f)
		}
	})
}

// expect calls method url and checks the error status and code.
func expect(t *testing.T, method, url string, status int, code string) []byte {
	t.Helper()
	got, _, raw := call(t, method, url, "", "")
	if got != status || decodeAs[errorBody](t, raw).Error.Code != code {
		t.Errorf("%s %s = %d %s, want %d %s", method, url, got, raw, status, code)
	}
	return raw
}
