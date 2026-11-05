package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// Feature 021, ruling 12: the Compose form of an app that exists has no
// Domains input, and a deploy from it keeps each domain with its path, www
// redirect and Team only (before, the textarea dropped all three).

// optionedShopRequest is shop whose web domain has every option the Domains
// page can set.
func optionedShopRequest() compose.Request {
	req := shopRequest()
	req.Compose = keptShopFile
	ov := req.Services["web"]
	ov.Domains = []compose.Domain{{Host: "example.com", Path: "/api", WWWRedirect: true, TeamOnly: true}}
	req.Services["web"] = ov
	return req
}

func TestKeptFileDeployKeepsDomainOptions(t *testing.T) {
	e, _ := deployShop(t, optionedShopRequest())
	_, page := e.get(t, "/apps/shop/configure")
	if strings.Contains(page, `name="svc.web.domains"`) {
		t.Fatal("the Compose form of an existing app has a Domains input")
	}
	// An edit of the file only, as a comment: the form posts no domains at all.
	resp, body := e.postForm(t, "/apps/shop/compose", shopFormOf(t, page, url.Values{"compose": {"# edited\n" + keptShopFile}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)

	rec, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	want := compose.Domain{Host: "example.com", Path: "/api", WWWRedirect: true, TeamOnly: true}
	if got := rec.Compose.Services["web"].Domains; len(got) != 1 || got[0] != want {
		t.Errorf("kept overlay domains = %+v, want %+v", got, want)
	}
	for _, svc := range rec.App.Services {
		if svc.Name != "web" {
			continue
		}
		if len(svc.Domains) != 1 || svc.Domains[0].Host != "example.com" || svc.Domains[0].Path != "/api" ||
			!svc.Domains[0].WWWRedirect || !svc.Domains[0].TeamOnly {
			t.Errorf("app domains = %+v, want the options kept", svc.Domains)
		}
	}
}

// An app made from a file that isn't kept (or from an image) keeps its domains
// through the form as well, written from the stored app.
func TestNotKeptFileDeployKeepsDomainOptions(t *testing.T) {
	e, _ := deployShop(t, optionedShopRequest())
	ctx := context.Background()
	stored, err := e.Store.GetApp(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := e.Store.SaveApp(ctx, stored.App, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := e.postForm(t, "/apps/shop/compose", url.Values{"revision": {legacy.Revision}, "compose": {shopCompose},
		"svc.web.overlay": {"1"}, "svc.web.healthPath": {"/health"}, "svc.db.overlay": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)
	got := e.storedApp(t, "shop")
	for _, svc := range got.Services {
		if svc.Name == "web" && (len(svc.Domains) != 1 || svc.Domains[0].Path != "/api" || !svc.Domains[0].WWWRedirect || !svc.Domains[0].TeamOnly) {
			t.Errorf("web's domains = %+v, want the options kept", svc.Domains)
		}
	}
}

func TestConfigureKeptFileListsDomainsOnce(t *testing.T) {
	const note = "Domains aren't set here: a deploy from this form keeps them as they are. Add and change them on the Domains page."
	e, _ := keptSentinelEnv(t)
	_, body := e.get(t, "/apps/shop/configure")
	got := text(body)
	if !strings.Contains(got, note) || !strings.Contains(body, `<a href="/apps/shop/domains">Domains page</a>`) {
		t.Errorf("the form lacks the note:\n%s", got)
	}
	if strings.Count(got, note) != 1 {
		t.Errorf("the note is drawn %d times, want once", strings.Count(got, note))
	}
	if !strings.Contains(got, "Health checks and secrets are set below, after the preview.") {
		t.Errorf("the file's hint is the new app's:\n%s", got)
	}
	// The host is in the Domains panel once; the compose form and the read-only definition don't
	// repeat it (the page header's link and the Delete section's sentence are other things).
	panel := body[strings.Index(body, `aria-labelledby="domains-title"`):]
	panel = panel[:strings.Index(panel, "</section>")]
	if strings.Count(panel, "shop.example.com") != 1 || !strings.Contains(text(panel), "Add domain") {
		t.Errorf("the Domains panel lacks the host once, or Add domain:\n%s", text(panel))
	}
	form := body[strings.Index(body, `<form id="compose-form"`):]
	form = form[:strings.Index(form, "</form>")]
	definition := got[strings.Index(got, "Definition"):strings.Index(got, "Variables & secrets")]
	if strings.Contains(form, "shop.example.com") || strings.Count(definition, "shop.example.com") != 1 {
		t.Errorf("the compose form or the definition lists the domain again:\n%s", definition)
	}

	// The stopped app has no Add domain link; its panel still lists the host.
	if resp, _ := e.postAction(t, "/apps/shop/stop"); resp.StatusCode != http.StatusSeeOther {
		t.Fatal("stop")
	}
	if strings.Contains(text(e.pageOf(t, "/apps/shop/configure")), "Add domain") {
		t.Error("Configure of a stopped app links to a form that isn't there")
	}

	// New app › Compose keeps its Domains textarea and no note.
	n := composeEnv(t)
	_, page := n.previewNew(t, "shop", shopCompose)
	if !strings.Contains(page, `name="svc.web.domains"`) || strings.Contains(text(page), "Domains aren't set here") ||
		!strings.Contains(text(page), "Domains, health checks and secrets are set below, after the preview.") {
		t.Errorf("New app's Compose form changed:\n%s", text(page))
	}
}

// New app › Compose still reads its Domains textarea into the request, and an
// existing app's draft sends the domains it keeps instead of anything typed.
func TestComposeDraftDomains(t *testing.T) {
	post := url.Values{"compose": {shopCompose}, "svc.web.overlay": {"1"}, "svc.web.domains": {"a.example.com\nb.example.com:8080"}}
	d, ok := parseComposeDraft(post)
	if !ok {
		t.Fatal("the draft was refused")
	}
	want := []compose.Domain{{Host: "a.example.com"}, {Host: "b.example.com", Port: 8080}}
	if got := d.request().Services["web"].Domains; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("a new app's domains = %+v, want %+v", got, want)
	}
	d.keptDomains = map[string][]compose.Domain{"web": {{Host: "kept.example.com", Path: "/x"}}}
	if got := d.request().Services["web"].Domains; len(got) != 1 || got[0].Host != "kept.example.com" || got[0].Path != "/x" {
		t.Errorf("an existing app's domains = %+v, want the kept ones", got)
	}
}

func TestComposeChangesNamesDomainsOfRemovedService(t *testing.T) {
	svc := func(hosts ...string) app.Service {
		s := app.Service{Name: "api"}
		for _, h := range hosts {
			s.Domains = append(s.Domains, app.Domain{Host: h, Port: "http"})
		}
		return s
	}
	tests := []struct {
		name  string
		hosts []string
		want  string
	}{
		{"no domain", nil, "Removes service api: its copies stop."},
		{"one domain", []string{"api.example.com"}, "Removes service api: its copies stop and api.example.com stops answering."},
		{"two domains", []string{"api.example.com", "v2.example.com"}, "Removes service api: its copies stop and api.example.com, v2.example.com stop answering."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := composeChanges(app.App{Services: []app.Service{svc(tc.hosts...)}}, app.App{})
			if len(got) != 1 || got[0].String() != tc.want {
				t.Errorf("composeChanges = %q, want %q", got, tc.want)
			}
		})
	}
}

// A file that drops a service with a domain says the domain stops answering,
// and a kept domain whose port the file removed is refused in the list.
func TestComposeFormDomainsAgainstTheFile(t *testing.T) {
	e, _ := deployShop(t, optionedShopRequest())
	_, page := e.get(t, "/apps/shop/configure")
	noWeb := "services:\n  worker:\n    image: traefik/whoami:latest\n  db:\n    image: postgres:17\n    expose: [\"5432\"]\n    volumes:\n      - data:/var/lib/postgresql/data\nvolumes:\n  data:\n"
	resp, body := e.postForm(t, "/apps/shop/compose/preview", shopFormOf(t, page, url.Values{"compose": {noWeb}}))
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "Removes service web: its copies stop and example.com stops answering.") {
		t.Errorf("preview = %d, lacks the sentence:\n%s", resp.StatusCode, text(body))
	}

	// The kept overlay names container port 8080, which the new file's web doesn't expose.
	req := optionedShopRequest()
	ov := req.Services["web"]
	ov.Domains[0].Port = 80
	req.Services["web"] = ov
	e2, _ := deployShop(t, req)
	_, page = e2.get(t, "/apps/shop/configure")
	moved := strings.Replace(keptShopFile, `expose: ["80"]`, `expose: ["81"]`, 1)
	resp, body = e2.postForm(t, "/apps/shop/compose/preview", shopFormOf(t, page, url.Values{"compose": {moved}}))
	got := text(body)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(got, "Caravana can't deploy this file") || !strings.Contains(got, "services.web.domains[0].port") {
		t.Errorf("preview = %d; want the refusal with the domain's path:\n%s", resp.StatusCode, got)
	}
}

// A form drawn before ruling 12 still posts its Domains text: it is ignored,
// even when it doesn't parse, and the app's domains are kept.
func TestStaleDomainsTextIsIgnored(t *testing.T) {
	e, _ := deployShop(t, optionedShopRequest())
	_, page := e.get(t, "/apps/shop/configure")
	resp, body := e.postForm(t, "/apps/shop/compose", shopFormOf(t, page, url.Values{
		"compose": {"# edited\n" + keptShopFile}, "svc.web.domains": {"bad domain:xx"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d, want 303:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)
	rec, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Compose.Services["web"].Domains; len(got) != 1 || got[0].Host != "example.com" || !got[0].TeamOnly {
		t.Errorf("kept overlay domains = %+v, want example.com kept with its options", got)
	}
}
