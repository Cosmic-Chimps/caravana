package api

import (
	"net/http"
	"strings"
	"testing"
)

// publishBody is a service with a TCP+UDP port published beside its dynamic
// HTTP health port.
func publishBody(published int) string {
	return strings.Replace(`{
  "source": {"kind": "image"},
  "services": [{
    "name": "turn",
    "image": "traefik/whoami:latest",
    "ports": [{"name": "http", "containerPort": 80}, {"name": "turn", "containerPort": 3478, "protocol": "tcp+udp", "published": PUBLISHED}],
    "health": {"path": "/", "port": "http"}
  }]
}`, "PUBLISHED", itoa(published), 1)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// Published ports (spec 041): the model's refusals are 400 on the field, a port
// another app publishes is 409 port_taken, and a cluster with no server that
// declares the network is 409 no_public_network.
func TestPublishedPortsAPI(t *testing.T) {
	e := newTestEnv(t, "")
	doc := loadContract(t)
	post := func(id, body string) (int, []byte) {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/"+id+"/deployments", "application/json", body)
		return status, raw
	}

	t.Run("no server offers the network", func(t *testing.T) {
		status, raw := post("calls", publishBody(3478))
		wantError(t, "deployApp with no public network", status, raw, http.StatusConflict, codeNoPublicNetwork, "services[0].ports[1].published")
	})

	e.Nomad.AddNode("n1", "one", "ready", "10.99.0.1")
	e.Nomad.SetPublicNetwork("n1", true)

	t.Run("a deploy publishes, and the port reads back", func(t *testing.T) {
		d := deployOK(t, e, "calls", publishBody(3478))
		waitHealthy(t, e, "calls", d.ID)
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/calls", "", "")
		if status != http.StatusOK {
			t.Fatalf("getApp = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "AppRecord")
		if !strings.Contains(string(raw), `"protocol":"tcp+udp"`) || !strings.Contains(string(raw), `"published":3478`) {
			t.Errorf("app = %s, want the protocol and the published port", raw)
		}
	})

	t.Run("another app's port", func(t *testing.T) {
		status, raw := post("game", publishBody(3478))
		got := wantError(t, "deployApp with another app's port", status, raw, http.StatusConflict, codePortTaken, "services[0].ports[1].published")
		if !strings.Contains(got.Error.Message, "calls") {
			t.Errorf("message = %q, want the other app named", got.Error.Message)
		}
	})

	t.Run("compose names the overlay entry", func(t *testing.T) {
		body := `{"compose": "services:\n  turn:\n    image: traefik/whoami:latest\n    ports: [\"3478:3478/udp\", \"80\"]\n", "services": {"turn": {"publish": [{"port": 3478}], "health": {"port": 80, "path": "/"}}}}`
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/game/compose", "application/json", body)
		wantError(t, "deployCompose with another app's port", status, raw, http.StatusConflict, codePortTaken, "services.turn.publish[0].published")
	})

	t.Run("the model's refusals", func(t *testing.T) {
		for _, tc := range []struct{ name, body, field string }{
			{"a reserved port", publishBody(22), "services[0].ports[1].published"},
			{"out of range", publishBody(70000), "services[0].ports[1].published"},
			{"unknown protocol", strings.Replace(publishBody(3478), `"tcp+udp"`, `"sctp"`, 1), "services[0].ports[1].protocol"},
			{"two copies", strings.Replace(publishBody(3478), `"name": "turn",`, `"name": "turn", "replicas": 2,`, 1), "services[0].replicas"},
			{"udp health", strings.Replace(strings.Replace(publishBody(3478), `"tcp+udp"`, `"udp"`, 1), `"port": "http"}`, `"port": "turn"}`, 1), "services[0].health.port"},
		} {
			status, raw := post("bad", tc.body)
			wantError(t, tc.name, status, raw, http.StatusBadRequest, codeInvalidField, tc.field)
		}
	})
}
