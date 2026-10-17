package api

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// webApp is a shop app body with one service, web, whose env says its
// environment, plus extra service JSON when given (the services' text, without
// brackets, starting with a comma-free service object).
func webApp(mode string, extra ...string) string {
	services := `{"name": "web", "image": "traefik/whoami:latest", "env": [{"name": "MODE", "value": "` + mode + `"}]` +
		webShape(mode) + `}`
	for _, s := range extra {
		services += "," + s
	}
	return `{"source": {"kind": "image"}, "services": [` + services + `]}`
}

// webShape makes staging's web differ from production's by its command and
// arguments, which a promotion carries over.
func webShape(mode string) string {
	if mode == "staging" {
		return `, "command": "/whoami", "args": ["--port", "8080"]`
	}
	return ""
}

type promotionView struct {
	From            string `json:"from"`
	To              string `json:"to"`
	FromEnvironment string `json:"fromEnvironment"`
	ToEnvironment   string `json:"toEnvironment"`
	FromDeployment  string `json:"fromDeployment"`
	Services        []struct {
		Name       string   `json:"name"`
		ImageFrom  string   `json:"imageFrom"`
		ImageTo    string   `json:"imageTo"`
		DigestFrom string   `json:"digestFrom"`
		DigestTo   string   `json:"digestTo"`
		Changed    []string `json:"changed"`
	} `json:"services"`
	Added          []string `json:"added"`
	Warnings       []any    `json:"warnings"`
	TargetRevision string   `json:"targetRevision"`
}

// shopEnvs makes project shop with staging, deploys production's web, moves
// the tag, deploys staging's web, and moves the tag again.
func shopEnvs(t *testing.T, e *testEnv) (production, staging deployment) {
	t.Helper()
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects", "application/json", `{"name": "shop"}`); status != http.StatusCreated {
		t.Fatalf("create project = %d %s", status, raw)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects/shop/environments", "application/json", `{"name": "staging"}`); status != http.StatusCreated {
		t.Fatalf("create environment = %d %s", status, raw)
	}
	production = deployOK(t, e, "shop--production--web", webApp("production"))
	waitHealthy(t, e, "shop--production--web", production.ID)
	e.Registry.AddIndex("traefik/whoami", "latest", movedDigest)
	staging = deployOK(t, e, "shop--staging--web", webApp("staging"))
	waitHealthy(t, e, "shop--staging--web", staging.ID)
	e.Registry.AddIndex("traefik/whoami", "latest", "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
	return production, staging
}

func promoteURL(e *testEnv, id, op string) string {
	return e.URL + "/v1/apps/" + id + "/" + op
}

func TestPromoteEndpoints(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	prod, stage := shopEnvs(t, e)
	const to = `{"to": "production"}`

	t.Run("preview", func(t *testing.T) {
		status, _, raw := call(t, http.MethodPost, promoteURL(e, "shop--staging--web", "promote/preview"), "application/json", to)
		if status != http.StatusOK {
			t.Fatalf("previewPromotion = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "PromotionPreview")
		p := decodeAs[promotionView](t, raw)
		if p.From != "shop--staging--web" || p.To != "shop--production--web" || p.FromEnvironment != "staging" ||
			p.ToEnvironment != "production" || p.FromDeployment != stage.ID || len(p.Services) != 1 || len(p.Added) != 0 || len(p.TargetRevision) != 16 {
			t.Fatalf("preview = %s", raw)
		}
		c := p.Services[0]
		if c.Name != "web" || c.ImageFrom != "traefik/whoami:latest" || c.DigestFrom != prod.Digests["web"] ||
			c.DigestTo != stage.Digests["web"] || c.DigestTo == c.DigestFrom || !slices.Equal(c.Changed, []string{"command", "args"}) {
			t.Errorf("service = %+v, want staging's digest and the shape change", c)
		}
		if strings.Contains(string(raw), "null") {
			t.Errorf("preview has a null: %s", raw)
		}
		// Nothing changed.
		_, _, deps := call(t, http.MethodGet, e.URL+"/v1/apps/shop--production--web/deployments", "", "")
		if got := decodeAs[struct{ Deployments []deployment }](t, deps); len(got.Deployments) != 1 {
			t.Errorf("production has %d deployments after a preview", len(got.Deployments))
		}
	})

	t.Run("refusals of preview and promote", func(t *testing.T) {
		for _, op := range []string{"promote/preview", "promote", "copy"} {
			u := promoteURL(e, "shop--staging--web", op)
			status, _, raw := call(t, http.MethodPost, u, "application/json", `{"to": "qa"}`)
			wantError(t, op+" to an unknown environment", status, raw, http.StatusNotFound, codeEnvironmentNotFound, "to")
			status, _, raw = call(t, http.MethodPost, u, "application/json", `{"to": "staging"}`)
			wantError(t, op+" to its own environment", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "to")
			status, _, raw = call(t, http.MethodPost, u, "application/json", `{"to": "a--b"}`)
			wantError(t, op+" to a bad name", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "to")
			status, _, raw = call(t, http.MethodPost, u, "application/json", `{}`)
			wantError(t, op+" without to", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "to")
			status, _, raw = call(t, http.MethodPost, u, "application/json", `{"to": "production", "extra": 1}`)
			wantError(t, op+" with an unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "extra")
			status, _, raw = call(t, http.MethodPost, promoteURL(e, "nothing", op), "application/json", to)
			wantError(t, op+" of an unknown app", status, raw, http.StatusNotFound, codeNotFound, "")
		}
	})

	t.Run("promote answers 202 and pins staging's digest", func(t *testing.T) {
		status, header, raw := call(t, http.MethodPost, promoteURL(e, "shop--staging--web", "promote"), "application/json", to)
		if status != http.StatusAccepted {
			t.Fatalf("promoteApp = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Deployment")
		d := decodeAs[struct {
			deployment
			PromotedFrom *struct{ App, Environment, Deployment string } `json:"promotedFrom"`
			Source       string                                         `json:"source"`
		}](t, raw)
		if d.AppID != "shop--production--web" || d.Trigger != "manual" || d.Digests["web"] != stage.Digests["web"] {
			t.Errorf("deployment = %s, want production's with staging's digest", raw)
		}
		if d.PromotedFrom == nil || d.PromotedFrom.App != "shop--staging--web" || d.PromotedFrom.Environment != "staging" || d.PromotedFrom.Deployment != stage.ID {
			t.Errorf("promotedFrom = %+v, want staging's deployment %s", d.PromotedFrom, stage.ID)
		}
		if d.Source != "" {
			t.Errorf("source = %q on a promotion, want none", d.Source)
		}
		if loc := header.Get("Location"); loc != "/v1/apps/shop--production--web/deployments/"+d.ID {
			t.Errorf("Location = %q, want the target's deployment", loc)
		}
		waitHealthy(t, e, "shop--production--web", d.ID)

		// The record reads back the same, and the shape came over.
		_, _, raw = call(t, http.MethodGet, e.URL+header.Get("Location"), "", "")
		checkSchema(t, doc, raw, "Deployment")
		if got := decodeAs[struct {
			PromotedFrom *struct{ App string } `json:"promotedFrom"`
		}](t, raw); got.PromotedFrom == nil || got.PromotedFrom.App != "shop--staging--web" {
			t.Errorf("getDeployment = %s, want promotedFrom", raw)
		}
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/shop--production--web", "", "")
		rec := decodeAs[struct {
			App struct {
				Services []struct {
					Command string   `json:"command"`
					Args    []string `json:"args"`
					Env     []struct{ Name, Value string }
				} `json:"services"`
			} `json:"app"`
		}](t, raw)
		if s := rec.App.Services[0]; s.Command != "/whoami" || !slices.Equal(s.Args, []string{"--port", "8080"}) || s.Env[0].Value != "production" {
			t.Errorf("production = %+v, want staging's command and its own env", s)
		}
	})

	t.Run("If-Match", func(t *testing.T) {
		u := promoteURL(e, "shop--staging--web", "promote")
		do := func(ifMatch string) (int, []byte) {
			req, _ := http.NewRequest(http.MethodPost, u, strings.NewReader(to))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", ifMatch)
			resp, err := noRedirects.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, raw
		}
		status, raw := do(`"0000000000000000"`)
		wantError(t, "a stale revision", status, raw, http.StatusPreconditionFailed, codeAppChanged, "")
		status, raw = do(`*`)
		wantError(t, "a malformed If-Match", status, raw, http.StatusBadRequest, codeInvalidField, "If-Match")

		// The preview's revision is the one that matches.
		_, _, pv := call(t, http.MethodPost, promoteURL(e, "shop--staging--web", "promote/preview"), "application/json", to)
		rev := decodeAs[promotionView](t, pv).TargetRevision
		if status, raw := do(`"` + rev + `"`); status != http.StatusAccepted {
			t.Errorf("promote with the preview's revision = %d %s", status, raw)
		}
	})
}

func TestPromoteRefusalsEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	shopEnvs(t, e)

	t.Run("the target has no app of that name", func(t *testing.T) {
		deployed := deployOK(t, e, "shop--staging--api", webApp("staging"))
		waitHealthy(t, e, "shop--staging--api", deployed.ID)
		status, _, raw := call(t, http.MethodPost, promoteURL(e, "shop--staging--api", "promote"), "application/json", `{"to": "production"}`)
		got := wantError(t, "promote", status, raw, http.StatusNotFound, codeAppNotInEnvironment, "to")
		if !strings.Contains(got.Error.Message, "copy") {
			t.Errorf("message %q should say to copy the app first", got.Error.Message)
		}
		status, _, raw = call(t, http.MethodPost, promoteURL(e, "shop--staging--api", "promote/preview"), "application/json", `{"to": "production"}`)
		wantError(t, "preview", status, raw, http.StatusNotFound, codeAppNotInEnvironment, "to")
	})

	t.Run("the target has a service the source lacks", func(t *testing.T) {
		body := webApp("production", `{"name": "worker", "image": "traefik/whoami:latest"}`)
		d := deployOK(t, e, "shop--production--web", body)
		waitHealthy(t, e, "shop--production--web", d.ID)
		for _, op := range []string{"promote", "promote/preview"} {
			status, _, raw := call(t, http.MethodPost, promoteURL(e, "shop--staging--web", op), "application/json", `{"to": "production"}`)
			wantError(t, op, status, raw, http.StatusConflict, codePromotionRemovesService, "services.worker")
		}
	})

	t.Run("the source's newest deployment is not healthy", func(t *testing.T) {
		// A tag that doesn't exist: the deploy is refused and its record says failed.
		bad := strings.Replace(webApp("staging"), "traefik/whoami:latest", "traefik/whoami:nope", 1)
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop--staging--web/deployments", "application/json", bad)
		wantError(t, "the failing deploy", status, raw, http.StatusUnprocessableEntity, codeImageNotFound, "services[0].image")
		for _, op := range []string{"promote", "promote/preview"} {
			status, _, raw := call(t, http.MethodPost, promoteURL(e, "shop--staging--web", op), "application/json", `{"to": "production"}`)
			wantError(t, op, status, raw, http.StatusConflict, codeDeploymentNotHealthy, "")
		}
	})
}

func TestCopyEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	shopEnvs(t, e)

	body := webApp("production", `{"name": "worker", "image": "traefik/whoami:latest", "secrets": [{"name": "QUEUE_KEY", "value": "queue-s3cret"}]}`)
	d := deployOK(t, e, "shop--production--web", body)
	waitHealthy(t, e, "shop--production--web", d.ID)
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects/shop/environments", "application/json", `{"name": "qa"}`); status != http.StatusCreated {
		t.Fatalf("create environment = %d %s", status, raw)
	}

	status, header, raw := call(t, http.MethodPost, promoteURL(e, "shop--production--web", "copy"), "application/json", `{"to": "qa"}`)
	if status != http.StatusCreated {
		t.Fatalf("copyApp = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "CopyResult")
	got := decodeAs[struct {
		appView
		Revision       string `json:"revision"`
		MissingSecrets []struct{ Service, Name string }
	}](t, raw)
	if got.App.ID != "shop--qa--web" || got.Project != "shop" || got.Environment != "qa" || got.AppName != "web" {
		t.Errorf("copy = %s", raw)
	}
	if len(got.MissingSecrets) != 1 || got.MissingSecrets[0].Service != "worker" || got.MissingSecrets[0].Name != "QUEUE_KEY" {
		t.Errorf("missingSecrets = %+v", got.MissingSecrets)
	}
	if header.Get("Location") != "/v1/apps/shop--qa--web" || header.Get("ETag") != `"`+got.Revision+`"` {
		t.Errorf("Location %q, ETag %q", header.Get("Location"), header.Get("ETag"))
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Error("a secret value is in the answer")
	}
	// Not deployed.
	_, _, deps := call(t, http.MethodGet, e.URL+"/v1/apps/shop--qa--web/deployments", "", "")
	if list := decodeAs[struct{ Deployments []deployment }](t, deps); len(list.Deployments) != 0 {
		t.Errorf("the copy has deployments: %s", deps)
	}
	// An app without secrets answers an empty list, not null.
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/projects/shop/environments", "application/json", `{"name": "demo"}`); status != http.StatusCreated {
		t.Fatalf("create environment = %d %s", status, raw)
	}
	_, _, raw = call(t, http.MethodPost, promoteURL(e, "shop--staging--web", "copy"), "application/json", `{"to": "demo"}`)
	checkSchema(t, doc, raw, "CopyResult")
	if !strings.Contains(string(raw), `"missingSecrets":[]`) {
		t.Errorf("answer = %s, want an empty missingSecrets", raw)
	}

	status, _, raw = call(t, http.MethodPost, promoteURL(e, "shop--production--web", "copy"), "application/json", `{"to": "qa"}`)
	wantError(t, "copy again", status, raw, http.StatusConflict, codeAppExists, "to")
}

func TestPromoteRoutesNeedAuthentication(t *testing.T) {
	e := newTestEnv(t, "")
	for _, op := range []string{"promote/preview", "promote", "copy"} {
		status, _, raw := call(t, http.MethodPost, e.RawURL+"/v1/apps/x/"+op, "application/json", `{"to": "production"}`)
		wantError(t, op+" without a token", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
	}
}
