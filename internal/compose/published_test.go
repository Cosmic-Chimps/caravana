package compose

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

const turnFile = `services:
  turn:
    image: coturn/coturn:4.6
    ports:
      - "3478:3478/udp"
      - "3478:3478/tcp"
      - "8080:8080"
`

func translateTurn(t *testing.T, file string, publish ...Publish) (app.App, []app.Warning, error) {
	t.Helper()
	return Translate("calls", Request{Compose: file, Services: map[string]Overlay{
		"turn": {Publish: publish, Health: &Health{Port: 8080}},
	}})
}

// A TCP and a UDP entry for one container port make one port that speaks both,
// and the overlay's publish list is the only thing that publishes.
func TestPublishedPorts(t *testing.T) {
	t.Run("protocols and the tcp+udp merge", func(t *testing.T) {
		a, _, err := translateTurn(t, turnFile)
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		want := []app.Port{
			{Name: "p3478", ContainerPort: 3478, Protocol: app.ProtocolTCPUDP},
			{Name: "p8080", ContainerPort: 8080},
		}
		if got := a.Services[0].Ports; !reflect.DeepEqual(got, want) {
			t.Errorf("ports = %+v, want %+v", got, want)
		}
	})

	t.Run("a udp port alone", func(t *testing.T) {
		a, _, err := Translate("calls", Request{Compose: "services:\n  turn:\n    image: x\n    ports: [\"7882:7882/udp\", \"8080\"]\n",
			Services: map[string]Overlay{"turn": {Health: &Health{Port: 8080}}}})
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		if got := a.Services[0].Ports[0]; got.Protocol != app.ProtocolUDP || got.Published != 0 {
			t.Errorf("port = %+v, want udp, unpublished", got)
		}
	})

	t.Run("expose with a protocol", func(t *testing.T) {
		a, _, err := Translate("calls", Request{Compose: "services:\n  turn:\n    image: x\n    expose: [\"3478/udp\", \"3478/tcp\", \"9000\", \"9001/tcp\"]\n",
			Services: map[string]Overlay{"turn": {Health: &Health{Port: 9000}}}})
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		want := []app.Port{
			{Name: "p3478", ContainerPort: 3478, Protocol: app.ProtocolTCPUDP},
			{Name: "p9000", ContainerPort: 9000},
			{Name: "p9001", ContainerPort: 9001},
		}
		if got := a.Services[0].Ports; !reflect.DeepEqual(got, want) {
			t.Errorf("ports = %+v, want %+v", got, want)
		}
	})

	t.Run("without the overlay the file's published still warns", func(t *testing.T) {
		a, warnings, err := translateTurn(t, turnFile)
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		for _, p := range a.Services[0].Ports {
			if p.Published != 0 {
				t.Errorf("port %+v is published", p)
			}
		}
		var got []string
		for _, w := range warnings {
			if strings.Contains(w.Message, "overlay's publish list") {
				got = append(got, w.Field+": "+w.Message)
			}
		}
		if len(got) != 3 {
			t.Fatalf("warnings = %q, want one per published entry", got)
		}
		if !strings.Contains(got[0], `{"port": 3478}`) {
			t.Errorf("warning %q doesn't show the overlay entry", got[0])
		}
	})

	t.Run("the overlay publishes, as the same number or another", func(t *testing.T) {
		a, warnings, err := translateTurn(t, turnFile, Publish{Port: 3478}, Publish{Port: 8080, Published: 18080})
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		ports := a.Services[0].Ports
		if ports[0].Published != 3478 || ports[1].Published != 18080 {
			t.Errorf("ports = %+v, want 3478 and 18080 published", ports)
		}
		if strings.Contains(strings.Join(warningTexts(warnings), "\n"), "overlay's publish list") {
			t.Errorf("warnings = %v: a published port still warns", warnings)
		}
		// The warning remains for the entry the overlay doesn't publish.
		_, warnings, _ = translateTurn(t, turnFile, Publish{Port: 3478})
		if n := strings.Count(strings.Join(warningTexts(warnings), "\n"), "overlay's publish list"); n != 1 {
			t.Errorf("warned %d times, want once (for 8080:8080)", n)
		}
	})

	t.Run("loopback in the file is never published", func(t *testing.T) {
		a, _, err := Translate("calls", Request{Compose: "services:\n  db:\n    image: x\n    ports: [\"127.0.0.1:5432:5432\"]\n"})
		if err != nil {
			t.Fatalf("Translate: %v", err)
		}
		if a.Services[0].Ports[0].Published != 0 {
			t.Errorf("port = %+v", a.Services[0].Ports[0])
		}
	})
}

func warningTexts(ws []app.Warning) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Message
	}
	return out
}

func TestPublishRefusals(t *testing.T) {
	tests := []struct {
		name    string
		publish []Publish
		field   string
	}{
		{"a port the file doesn't have", []Publish{{Port: 9999}}, "services.turn.publish[0].port"},
		{"the same port twice", []Publish{{Port: 3478}, {Port: 3478}}, "services.turn.publish[1].port"},
		{"a reserved number is named at the overlay", []Publish{{Port: 3478}, {Port: 8080, Published: 443}}, "services.turn.publish[1].published"},
		{"a number published twice", []Publish{{Port: 3478}, {Port: 8080, Published: 3478}}, "services.turn.publish[1].published"},
		{"a number out of range", []Publish{{Port: 3478, Published: 70000}}, "services.turn.publish[0].published"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := translateTurn(t, turnFile, tt.publish...)
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}
}

// The kept request keeps the publish list: it holds no secret.
func TestRequestWithoutSecretValuesKeepsPublish(t *testing.T) {
	req := Request{Compose: turnFile, Services: map[string]Overlay{"turn": {
		Publish: []Publish{{Port: 3478}}, Secrets: []app.Secret{{Name: "KEY", Value: new("s3cret")}},
	}}}
	got := req.WithoutSecretValues().Services["turn"]
	if !reflect.DeepEqual(got.Publish, []Publish{{Port: 3478}}) || got.Secrets[0].Value != nil {
		t.Errorf("overlay = %+v", got)
	}
}
