package main

// The gate's `templates` step (feature 035, T013): Hollo installed through the API with only a domain.
// The template makes the app's two services and the passwords between them; both services end healthy,
// Hollo answers through Traefik, and a Back up now of Postgres succeeds. The passwords are checked where
// they belong: the same one in the two services' secrets, in Nomad Variables, and nowhere an API read can
// reach.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	gateTemplatesApp  = "gate-hollo"
	gateTemplatesHost = "gate-hollo.localhost"
)

func (g *gate) templatesStep(t *testing.T) {
	const id, host = gateTemplatesApp, gateTemplatesHost
	nomad := g.target.Nomad
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(func() {
		remove()
		waitFor(t, id+"'s Nomad job to be gone", 2*time.Minute, func() bool {
			_, _, err := nomad.Jobs().Info(id, nil)
			return err != nil && strings.Contains(err.Error(), "404")
		})
	})

	g.startBucket(t) // a destination for the Postgres backup (Hollo's template has one)

	// The catalog lists it, with what it asks for.
	var list struct {
		Catalog   struct{ Version, Source string } `json:"catalog"`
		Templates []struct{ Name string }          `json:"templates"`
	}
	g.getJSON(t, "/v1/templates", &list)
	found := false
	for _, tpl := range list.Templates {
		found = found || tpl.Name == "hollo"
	}
	if !found || list.Catalog.Version == "" {
		t.Fatalf("GET /v1/templates = %+v, want the catalog with hollo", list)
	}
	var tpl struct {
		Inputs    []struct{ Name, Type string } `json:"inputs"`
		Generated []struct{ Name string }       `json:"generated"`
	}
	g.getJSON(t, "/v1/templates/hollo", &tpl)
	if len(tpl.Inputs) != 1 || tpl.Inputs[0].Name != "domain" || len(tpl.Generated) != 2 {
		t.Fatalf("the template = %+v, want one input (domain) and two generated secrets", tpl)
	}

	// Install with only a domain.
	resp := g.api(t, http.MethodPost, "/v1/templates/hollo/install", "application/json",
		`{"id": "`+id+`", "inputs": {"domain": "`+host+`"}}`)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var d gateDeployment
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil || d.ID == "" {
		t.Fatalf("install hollo: %d %s", resp.StatusCode, raw)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the install's Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	if final := g.waitFinalOf(t, id, d.ID, 8*time.Minute); final.Status != "healthy" {
		t.Fatalf("the install ended %s: %s", final.Status, final.Error)
	}

	// Both services are healthy, and the record names the template.
	var rec struct {
		Template *struct{ Name, Version string } `json:"template"`
		App      struct {
			Services []struct {
				Name   string
				Backup *struct{ Engine string }
			} `json:"services"`
		} `json:"app"`
	}
	g.getJSON(t, "/v1/apps/"+id, &rec)
	if rec.Template == nil || rec.Template.Name != "hollo" {
		t.Fatalf("the app's record = %+v, want it to name the hollo template", rec)
	}
	var summary struct {
		Status struct {
			State    string `json:"state"`
			Services []struct {
				Name          string
				WantedCopies  int
				HealthyCopies int
			} `json:"services"`
		} `json:"status"`
	}
	waitFor(t, "both services to be healthy", 3*time.Minute, func() bool {
		var all struct {
			Apps []struct {
				App    struct{ ID string } `json:"app"`
				Status struct {
					State    string `json:"state"`
					Services []struct {
						Name          string
						WantedCopies  int
						HealthyCopies int
					} `json:"services"`
				} `json:"status"`
			} `json:"apps"`
		}
		g.getJSON(t, "/v1/apps", &all)
		for _, a := range all.Apps {
			if a.App.ID == id {
				summary.Status.State, summary.Status.Services = a.Status.State, a.Status.Services
			}
		}
		if len(summary.Status.Services) != 2 {
			return false
		}
		for _, s := range summary.Status.Services {
			if s.HealthyCopies < s.WantedCopies || s.WantedCopies == 0 {
				return false
			}
		}
		return summary.Status.State == "healthy"
	})

	// Hollo answers through Traefik on the path its health check uses.
	client, base := g.edgeClient(host)
	waitFor(t, "Hollo to answer through Traefik", 2*time.Minute, func() bool {
		resp, err := client.Get(base + "/nodeinfo/2.1")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	// The two passwords: one value in Hollo's DATABASE_URL and Postgres's POSTGRES_PASSWORD, in Nomad
	// Variables only. The management token reads them; no API answer holds either.
	hollo, _, err := nomad.Variables().Read("nomad/jobs/"+id+"/hollo", nil)
	if err != nil || hollo == nil {
		t.Fatalf("read the hollo variable: %v", err)
	}
	postgres, _, err := nomad.Variables().Read("nomad/jobs/"+id+"/postgres", nil)
	if err != nil || postgres == nil {
		t.Fatalf("read the postgres variable: %v", err)
	}
	password := postgres.Items["POSTGRES_PASSWORD"]
	if len(password) != 32 || !strings.Contains(hollo.Items["DATABASE_URL"], ":"+password+"@postgres:5432/hollo") {
		t.Fatalf("the two services' passwords differ or aren't the template's 32 characters (postgres has %d)", len(password))
	}
	if key := hollo.Items["SECRET_KEY"]; len(key) != 64 {
		t.Errorf("SECRET_KEY has %d characters, want 64", len(key))
	}
	for _, path := range []string{"/v1/apps/" + id, "/v1/apps/" + id + "/compose", "/v1/apps/" + id + "/deployments",
		"/v1/apps/" + id + "/jobspec", "/v1/templates/hollo", "/v1/apps"} {
		resp := g.api(t, http.MethodGet, path, "", "")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), password) || strings.Contains(string(body), hollo.Items["SECRET_KEY"]) {
			t.Errorf("GET %s holds a generated secret", path)
		}
	}
	if job, _, err := nomad.Jobs().Info(id, nil); err == nil {
		out, _ := json.Marshal(job)
		if strings.Contains(string(out), password) {
			t.Error("the Nomad job holds the database password")
		}
	}

	// Back up now of Postgres succeeds.
	resp = g.api(t, http.MethodPost, "/v1/apps/"+id+"/backups", "application/json", `{"service": "postgres"}`)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("back up now: %d %s", resp.StatusCode, raw)
	}
	waitFor(t, "the backup to succeed", 4*time.Minute, func() bool {
		var l struct {
			Backups []struct{ Status, Message string } `json:"backups"`
		}
		g.getJSON(t, "/v1/apps/"+id+"/backups", &l)
		for _, b := range l.Backups {
			switch b.Status {
			case "succeeded":
				return true
			case "failed":
				t.Fatalf("the backup failed: %s", b.Message)
			}
		}
		return false
	})
}
