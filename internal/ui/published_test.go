package ui

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Spec 041: the console shows a service's published ports, a save of the form
// keeps them, and the Compose form keeps the overlay's publish list.

func publishingService() app.Service {
	return app.Service{
		Name:  "turn",
		Image: "coturn/coturn:4.6",
		Ports: []app.Port{
			{Name: "turn", ContainerPort: 3478, Protocol: app.ProtocolTCPUDP, Published: 3478},
		},
		Health: &app.HealthCheck{},
	}
}

func TestPublishedRows(t *testing.T) {
	svc := publishingService()
	svc.Ports = append(svc.Ports, app.Port{Name: "relay", ContainerPort: 7882, Protocol: app.ProtocolUDP, Published: 17882}, app.Port{Name: "http", ContainerPort: 80})

	if got := publishedRows(app.Service{Name: "web", Ports: []app.Port{{Name: "http", ContainerPort: 80}}}, "one"); got != nil {
		t.Errorf("a service that publishes nothing has rows %+v", got)
	}

	rows := publishedRows(svc, "one")
	if len(rows) != 3 || rows[0].Label != "Published ports" {
		t.Fatalf("rows = %+v", rows)
	}
	wantPorts := []string{"3478 · tcp+udp → container 3478", "17882 · udp → container 7882"}
	if !reflect.DeepEqual(rows[0].Values, wantPorts) {
		t.Errorf("ports = %q, want %q", rows[0].Values, wantPorts)
	}
	if got := rows[1].Values[0]; got != "the address of server one, while the copy runs" {
		t.Errorf("open on = %q", got)
	}
	if got := publishedRows(svc, "")[1].Values[0]; !strings.Contains(got, "this server") {
		t.Errorf("without a server name: %q", got)
	}

	// Both panels show them.
	a := app.App{ID: "calls", Services: []app.Service{svc}}
	shown := func(rows []defRow) bool {
		for _, r := range rows {
			if r.Label == "Published ports" {
				return true
			}
		}
		return false
	}
	if !shown(alsoSet(svc, newEditService(svc), "one")) {
		t.Error("Also set lacks the published ports")
	}
	var panelRows []defRow
	for _, p := range servicePanels(a, "one") {
		panelRows = append(panelRows, p.Rows...)
	}
	if !shown(panelRows) {
		t.Error("the Compose panels lack the published ports")
	}
}

// Saving the form of a service with one port, even with a new container port,
// keeps the port's protocol and published number.
func TestApplyEditKeepsPublishedPorts(t *testing.T) {
	stored := app.App{ID: "calls", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{publishingService()}}
	before := stored.WithoutSecretValues()
	post := postOf(formOf(stored, "r"))
	post.Set(inputName("turn", "port"), "3479")
	f, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	got := applyEdit(stored, f).Services[0].Ports[0]
	want := app.Port{Name: "turn", ContainerPort: 3479, Protocol: app.ProtocolTCPUDP, Published: 3478}
	if got != want {
		t.Errorf("port = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(stored, before) {
		t.Error("applyEdit changed the stored app")
	}
}

// The Configure page of a service that publishes says so.
func TestConfigureShowsPublishedPorts(t *testing.T) {
	e := newConsoleEnv(t)
	a := app.App{ID: "calls", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{publishingService()}}
	if _, err := e.Store.InsertApp(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	resp, body := e.get(t, "/apps/calls/configure")
	if resp.StatusCode != 200 {
		t.Fatalf("configure = %d", resp.StatusCode)
	}
	page := text(body)
	for _, want := range []string{"Published ports 3478 · tcp+udp → container 3478", "Open on the address of this server, while the copy runs", "Set through the API or the Compose overlay"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// The draft sends the published ports it keeps, even for a service with no
// other overlay, as the overlay's publish list, and a new app's draft sends none.
func TestComposeDraftKeepsPublish(t *testing.T) {
	d, ok := parseComposeDraft(url.Values{"compose": {shopCompose}, "svc.worker.overlay": {"1"}})
	if !ok {
		t.Fatal("the draft was refused")
	}
	if _, has := d.request().Services["worker"]; has {
		t.Error("a new app's draft sends an overlay for a service with nothing set")
	}

	// An app that keeps its file: the overlay's list, as written.
	written := []compose.Publish{{Port: 3478}, {Port: 8080, Published: 18080}}
	d.keepPublish(store.AppRecord{
		App:     app.App{Services: []app.Service{{Name: "worker"}}},
		Compose: &compose.Request{Services: map[string]compose.Overlay{"worker": {Publish: written}}},
	})
	if got := d.request().Services["worker"].Publish; !reflect.DeepEqual(got, written) {
		t.Errorf("kept-file publish = %+v, want %+v", got, written)
	}

	// An app with no kept file: read from its ports; the same number is left out.
	d.keepPublish(store.AppRecord{App: app.App{Services: []app.Service{{Name: "worker", Ports: []app.Port{
		{Name: "turn", ContainerPort: 3478, Published: 3478},
		{Name: "http", ContainerPort: 8080, Published: 18080},
		{Name: "metrics", ContainerPort: 9100},
	}}}}})
	want := []compose.Publish{{Port: 3478}, {Port: 8080, Published: 18080}}
	if got := d.request().Services["worker"].Publish; !reflect.DeepEqual(got, want) {
		t.Errorf("publish from ports = %+v, want %+v", got, want)
	}
}

// A deploy from the Compose form keeps the overlay's publish list, in the kept
// overlay and on the app's ports (the form has no input for it).
func TestKeptFileDeployKeepsPublish(t *testing.T) {
	e := composeEnv(t)
	e.Nomad.AddNode("n1", "one", "ready", "10.99.0.1")
	e.Nomad.SetPublicNetwork("n1", true)
	req := shopRequest()
	req.Compose = keptShopFile
	ov := req.Services["web"]
	ov.Publish = []compose.Publish{{Port: 80, Published: 18080}}
	req.Services["web"] = ov
	rec, err := e.Svc.DeployCompose(context.Background(), "shop", req, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)

	_, page := e.get(t, "/apps/shop/configure")
	resp, body := e.postForm(t, "/apps/shop/compose", shopFormOf(t, page, url.Values{"compose": {"# edited\n" + keptShopFile}}))
	if resp.StatusCode != 303 {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)

	stored, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Compose.Services["web"].Publish; !reflect.DeepEqual(got, []compose.Publish{{Port: 80, Published: 18080}}) {
		t.Errorf("kept overlay publish = %+v", got)
	}
	if got := webService(t, stored.App).Ports[0]; got.Published != 18080 {
		t.Errorf("web port = %+v, want published 18080", got)
	}
}
