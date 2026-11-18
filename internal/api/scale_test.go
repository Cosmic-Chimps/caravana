package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// scaledBody is a service with a domain and the given scale.
func scaledBody(scale string) string {
	return `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest",
		"ports": [{"name": "http", "containerPort": 80}], "domains": [{"host": "scaled.example.com", "port": "http"}],
		"scale": ` + scale + `, "health": {"path": "/", "port": "http"}}]}`
}

// The scale is part of the app (feature 043): it deploys and reads back, the
// model's refusals are 400 on the field, a scale step is an action with its
// counts and reason, and the schemas match.
func TestScaleAPI(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	post := func(id, body string) (int, []byte) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/"+id+"/deployments", "application/json", body)
		return status, raw
	}

	t.Run("it deploys and reads back", func(t *testing.T) {
		d := deployOK(t, e, "scaled", scaledBody(`{"min": 1, "max": 3, "target": {"requestsPerCopy": 50}, "cooldownSeconds": 120}`))
		waitHealthy(t, e, "scaled", d.ID)
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/scaled", "", "")
		if status != http.StatusOK {
			t.Fatalf("getApp = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "AppRecord")
		var rec struct {
			App struct {
				Services []struct {
					Scale json.RawMessage `json:"scale"`
				} `json:"services"`
			} `json:"app"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		if got := string(rec.App.Services[0].Scale); got != `{"min":1,"max":3,"target":{"requestsPerCopy":50},"cooldownSeconds":120}` {
			t.Errorf("scale = %s", got)
		}
	})

	t.Run("the model's refusals", func(t *testing.T) {
		for _, tc := range []struct{ name, scale, field string }{
			{"min zero", `{"min": 0, "max": 3, "target": {"cpu": 70}}`, "services[0].scale.min"},
			{"max over 20", `{"min": 1, "max": 21, "target": {"cpu": 70}}`, "services[0].scale.max"},
			{"no target", `{"min": 1, "max": 3, "target": {}}`, "services[0].scale.target"},
			{"two targets", `{"min": 1, "max": 3, "target": {"cpu": 70, "p95": 300}}`, "services[0].scale.target"},
			{"cpu out of range", `{"min": 1, "max": 3, "target": {"cpu": 5}}`, "services[0].scale.target.cpu"},
			{"cooldown out of range", `{"min": 1, "max": 3, "target": {"cpu": 70}, "cooldownSeconds": 5}`, "services[0].scale.cooldownSeconds"},
		} {
			status, raw := post("bad", scaledBody(tc.scale))
			wantError(t, tc.name, status, raw, http.StatusBadRequest, codeInvalidField, tc.field)
		}
		// A requests target needs a domain.
		status, raw := post("bad", `{"source": {"kind": "image"}, "services": [{"name": "web", "image": "traefik/whoami:latest",
			"ports": [{"name": "http", "containerPort": 80}], "scale": {"min": 1, "max": 3, "target": {"requestsPerCopy": 5}},
			"health": {"path": "/", "port": "http"}}]}`)
		wantError(t, "requests without a domain", status, raw, http.StatusBadRequest, codeInvalidField, "services[0].scale.target")
	})

	t.Run("a scale step is an action", func(t *testing.T) {
		at := time.Now().UTC()
		if err := e.Store.InsertAction(t.Context(), store.ActionRecord{ID: "act_scale1", AppID: "scaled", Action: store.ActionScale,
			Trigger: string(app.TriggerAutoscale), Status: store.ActionSucceeded, Service: "web", StartedAt: at, FinishedAt: &at,
			FromCount: new(1), ToCount: new(2), Reason: "62 requests a second per copy over the last 3 minutes, target 50"}); err != nil {
			t.Fatal(err)
		}
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/scaled/actions", "", "")
		if status != http.StatusOK {
			t.Fatalf("listAppActions = %d %s", status, raw)
		}
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 {
			t.Fatalf("actions = %s, %v", raw, err)
		}
		checkSchema(t, doc, list[0], "AppAction")
		for _, want := range []string{`"action":"scale"`, `"trigger":"autoscale"`, `"fromCount":1`, `"toCount":2`, `"reason":"62 requests a second`} {
			if !strings.Contains(string(list[0]), want) {
				t.Errorf("action = %s, want %s", list[0], want)
			}
		}
	})
}
