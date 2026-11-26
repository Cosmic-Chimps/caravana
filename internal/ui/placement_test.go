package ui

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 042, Configure: the Placement panel of each service, its form (a deploy on the page's revision),
// the kept-file app's read-only rules, and the Compose form keeping the overlay's placement.

var shopPlacement = &app.Placement{
	Require: map[string]string{"ssd": "true"},
	Prefer:  []app.Preference{{Label: "region", Value: "eu", Weight: 80}},
	Avoid:   []string{"fsn-2"},
}

// placementEnv is a cluster of three servers with duo deployed.
func placementEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := threeServers(t)
	e.deploy(t, duoApp())
	return e
}

// submitPlacement posts the service's placement form from the page as a browser would: the page's own
// revision, and what is typed (a checkbox that isn't named is not checked; formsIn can't tell, so avoid is
// always set).
func (e *consoleEnv) submitPlacement(t *testing.T, page, svc string, set url.Values) (*http.Response, string) {
	t.Helper()
	f := formWith(t, page, "/apps/duo/services/"+svc+"/placement")
	over := url.Values{"require": nil, "prefer": nil, "avoid": nil}
	for k, v := range set {
		over[k] = v
	}
	return e.submit(t, f, over)
}

func TestPlacementRulesAreSentences(t *testing.T) {
	for _, c := range []struct {
		name string
		p    *app.Placement
		want []string
	}{
		{"none", nil, []string{"Any server that accepts work."}},
		{"empty", &app.Placement{}, []string{"Any server that accepts work."}},
		{"all three", shopPlacement, []string{"Only on servers with ssd=true", "Prefers region=eu (weight 80)", "Never on fsn-2"}},
		{"several", &app.Placement{Require: map[string]string{"ssd": "true", "region": "eu", "gpu": "a100"},
			Prefer: []app.Preference{{Label: "ssd", Value: "true"}, {Label: "zone", Value: "b", Weight: 50}}, Avoid: []string{"one", "two", "three"}},
			[]string{"Only on servers with gpu=a100, region=eu and ssd=true", "Prefers ssd=true", "Prefers zone=b", "Never on one, two and three"}},
	} {
		var got []string
		for _, r := range placementRules(c.p) {
			got = append(got, strings.Join(strings.Fields(plainOf(sentenceParts(r))), " "))
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: rules = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParsePreferAndPlacement(t *testing.T) {
	prefs, err := parsePrefer("ssd=true 80, region=eu, fast")
	want := []app.Preference{{Label: "ssd", Value: "true", Weight: 80}, {Label: "region", Value: "eu"}, {Label: "fast", Value: "true"}}
	if err != nil || !reflect.DeepEqual(prefs, want) {
		t.Errorf("parsePrefer = %+v, %v", prefs, err)
	}
	if got := preferText(prefs); got != "ssd=true 80, region=eu, fast=true" {
		t.Errorf("preferText = %q", got)
	}
	for _, bad := range []string{"ssd=true 1 2", "ssd=true heavy", "=true 5"} {
		if _, err := parsePrefer(bad); err == nil {
			t.Errorf("parsePrefer(%q) is not refused", bad)
		}
	}
	post := placementPost{Require: "", Prefer: "", Avoid: nil}
	if p, ok := post.placement(); p != nil || !ok {
		t.Errorf("an empty form = %+v, %v; want nil", p, ok)
	}
	post = placementPost{Require: "ssd", Prefer: "x=y 3", Avoid: []string{"b", "a", "b"}}
	p, ok := post.placement()
	if !ok || !reflect.DeepEqual(p, &app.Placement{Require: map[string]string{"ssd": "true"},
		Prefer: []app.Preference{{Label: "x", Value: "y", Weight: 3}}, Avoid: []string{"a", "b"}}) {
		t.Errorf("placement = %+v, %v", p, ok)
	}
}

func TestConfigureShowsPlacementPerService(t *testing.T) {
	e := placementEnv(t)
	_, body := e.get(t, "/apps/duo/configure/services")
	words := text(body)
	if n := strings.Count(words, "Any server that accepts work."); n != 2 {
		t.Errorf("%d placement panels say any server, want 2 (web and worker):\n%s", n, words)
	}
	for _, svc := range []string{"web", "worker"} {
		f := formWith(t, body, "/apps/duo/services/"+svc+"/placement")
		if f.Values.Get("revision") != e.revisionOf(t, "duo") {
			t.Errorf("%s: the form's revision = %q", svc, f.Values.Get("revision"))
		}
	}
	// One checkbox per managed server, none checked; the waiting and joining ones aren't offered.
	for _, name := range []string{"hel-1", "fsn-1", "fsn-2"} {
		if !strings.Contains(body, `name="avoid" value="`+name+`"`) {
			t.Errorf("no checkbox for %s", name)
		}
	}
	if strings.Contains(body, " checked>") && strings.Contains(body, `name="avoid"`) {
		t.Error("a server is checked with no rule")
	}
}

func TestSavePlacementDeploysTheRules(t *testing.T) {
	e := placementEnv(t)
	before := len(e.deployments(t, "duo"))
	_, page := e.get(t, "/apps/duo/configure/services")
	resp, body := e.submitPlacement(t, page, "web", url.Values{"require": {"ssd"}, "prefer": {"region=eu 80"}, "avoid": {"fsn-2"}})
	recs := e.deployments(t, "duo")
	if resp.StatusCode != http.StatusSeeOther || len(recs) != before+1 || location(t, resp) != "/apps/duo/deployments#deployment-"+recs[0].Deployment.ID {
		t.Fatalf("POST = %d to %q (%d deployments):\n%s", resp.StatusCode, location(t, resp), len(recs), text(body))
	}
	e.waitDeploy(t, recs[0].Deployment.ID)
	stored := e.storedApp(t, "duo")
	if got := stored.Services[0].Placement; !reflect.DeepEqual(got, shopPlacement) {
		t.Errorf("web's placement = %+v, want %+v", got, shopPlacement)
	}
	if stored.Services[1].Placement != nil {
		t.Errorf("worker has a placement: %+v", stored.Services[1].Placement)
	}
	// The rest of the app is as it was.
	if stored.Services[0].Image != duoApp().Services[0].Image || len(stored.Services[0].Env) != 1 {
		t.Errorf("the save changed more than the placement: %+v", stored.Services[0])
	}
	// The page says the rules, and the form holds them.
	_, page = e.get(t, "/apps/duo/configure/services")
	words := text(page)
	for _, want := range []string{"Only on servers with ssd=true", "Prefers region=eu (weight 80)", "Never on fsn-2"} {
		if !strings.Contains(words, want) {
			t.Errorf("Configure lacks %q:\n%s", want, words)
		}
	}
	f := formWith(t, page, "/apps/duo/services/web/placement")
	if f.Values.Get("require") != "ssd=true" || f.Values.Get("prefer") != "region=eu 80" ||
		!strings.Contains(page, `id="svc-web-placement-avoid-fsn-2" name="avoid" value="fsn-2" checked`) ||
		strings.Contains(page, `value="fsn-1" checked`) {
		t.Errorf("the form holds %v:\n%s", f.Values, page)
	}
	// An empty form clears the rules (a deploy again).
	resp, _ = e.submitPlacement(t, page, "web", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("clearing = %d", resp.StatusCode)
	}
	if got := e.storedApp(t, "duo").Services[0].Placement; got != nil {
		t.Errorf("placement after clearing = %+v", got)
	}
}

func TestSavePlacementRefusalsAreDrawnAtTheField(t *testing.T) {
	for _, c := range []struct {
		name  string
		set   url.Values
		field string // the input's id suffix that has the message
		want  string
	}{
		{"a bad weight", url.Values{"prefer": {"ssd=true 101"}}, "prefer", "Preference 1 (weight)"},
		{"a weight that isn't a number", url.Values{"prefer": {"ssd=true heavy"}}, "prefer", "is not a weight"},
		{"too many words", url.Values{"prefer": {"ssd=true 5 6"}}, "prefer", "as label=value, then a weight"},
		{"an upper-case label", url.Values{"require": {"SSD=true"}}, "require", "Label name"},
		{"a bad value", url.Values{"require": {"ssd=no way"}}, "require", "ssd: "},
		{"a label twice", url.Values{"require": {"ssd, ssd=false"}}, "require", "Ssd is written twice"},
		{"a preferred label twice", url.Values{"prefer": {"ssd=true, ssd=false"}}, "prefer", "Preference 2 (label)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := placementEnv(t)
			before, deployments := e.storedApp(t, "duo"), len(e.deployments(t, "duo"))
			_, page := e.get(t, "/apps/duo/configure/services")
			resp, body := e.submitPlacement(t, page, "web", c.set)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, text(body))
			}
			id := "svc-web-placement-" + c.field
			i := strings.Index(body, `id="`+id+`"`)
			j := strings.Index(body, `id="`+id+`-error"`)
			if i < 0 || j < 0 || !strings.Contains(text(body[j:j+400]), c.want) {
				t.Errorf("no %q at the %s input:\n%s", c.want, c.field, text(body))
			}
			if !strings.Contains(body[i:i+700], `aria-invalid="true"`) {
				t.Errorf("the %s input isn't marked invalid", c.field)
			}
			for _, v := range c.set {
				if !strings.Contains(body, `value="`+v[0]+`"`) {
					t.Errorf("what was typed (%q) was lost", v[0])
				}
			}
			if !reflect.DeepEqual(e.storedApp(t, "duo"), before) || len(e.deployments(t, "duo")) != deployments {
				t.Error("a refused post changed the app or deployed")
			}
		})
	}
}

// A rule no server can satisfy is refused by the deploy: the sentence is at the field, and says the app was
// saved because the attempt is in the history.
func TestSavePlacementImpossibleIsDrawnAtTheRule(t *testing.T) {
	e := placementEnv(t)
	e.Nomad.SetPlanFailure("web", "${meta.caravana.label.gpu} = true", 3)
	_, page := e.get(t, "/apps/duo/configure/services")
	resp, body := e.submitPlacement(t, page, "web", url.Values{"require": {"gpu"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST = %d, want 409:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)
	for _, want := range []string{"No server that accepts work has the label gpu=true.", savedNote} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	if j := strings.Index(body, `id="svc-web-placement-require-error"`); j < 0 {
		t.Errorf("the sentence isn't at the Require input:\n%s", words)
	}
	// The failed attempt is on the Deployments tab, and nothing was registered for it.
	if got := e.deployments(t, "duo")[0].Status; got != store.StatusFailed {
		t.Errorf("the attempt is %q, want failed", got)
	}
}

func TestSavePlacementStaleRevisionAndFileApps(t *testing.T) {
	e := placementEnv(t)
	before := e.storedApp(t, "duo")
	_, page := e.get(t, "/apps/duo/configure/services")
	f := formWith(t, page, "/apps/duo/services/web/placement")
	resp, body := e.submitPlacement(t, page, "web", url.Values{"revision": {"0000000000000000"}, "require": {"ssd"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), changedSincePage) || !reflect.DeepEqual(e.storedApp(t, "duo"), before) {
		t.Errorf("stale = %d:\n%s", resp.StatusCode, text(body))
	}
	// A service the app doesn't have, and a post with no revision.
	if resp, body := e.postForm(t, "/apps/duo/services/nope/placement", url.Values{"revision": {f.Values.Get("revision")}}); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "This app has no service nope.") {
		t.Errorf("an unknown service = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, _ := e.postForm(t, "/apps/duo/services/web/placement", url.Values{"require": {"ssd"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no revision = %d, want 400", resp.StatusCode)
	}
	if resp, _ := e.postForm(t, "/apps/nope/services/web/placement", url.Values{"revision": {"x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown app = %d, want 404", resp.StatusCode)
	}
}

// A name the service avoids that is no server any more stays listed and checked, so saving never drops it.
func TestPlacementKeepsAnAvoidedNameThatIsNoServer(t *testing.T) {
	e := placementEnv(t)
	a := e.storedApp(t, "duo")
	a.Services[0].Placement = &app.Placement{Avoid: []string{"old-box"}}
	e.deploy(t, a)
	_, page := e.get(t, "/apps/duo/configure/services")
	if !strings.Contains(text(page), "old-box (not a server any more)") {
		t.Errorf("the gone name isn't listed:\n%s", text(page))
	}
	if !strings.Contains(page, `name="avoid" value="old-box" checked`) {
		t.Fatalf("the gone name isn't checked:\n%s", page)
	}
	// Saving with fsn-1 added keeps both.
	resp, _ := e.submitPlacement(t, page, "web", url.Values{"avoid": {"old-box", "fsn-1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST = %d", resp.StatusCode)
	}
	if got := e.storedApp(t, "duo").Services[0].Placement.Avoid; !reflect.DeepEqual(got, []string{"fsn-1", "old-box"}) {
		t.Errorf("avoid = %v", got)
	}
}

// An app that keeps its Compose file shows the rules read-only; a post made by hand is refused; the Compose
// form keeps the overlay's placement through a deploy.
func TestKeptFileAppPlacementIsReadOnly(t *testing.T) {
	req := shopRequest()
	req.Compose = keptShopFile
	ov := req.Services["web"]
	ov.Placement = shopPlacement
	req.Services["web"] = ov
	e, revision := deployShop(t, req)

	_, page := e.get(t, "/apps/shop/configure/services")
	words := text(page)
	for _, want := range []string{"Only on servers with ssd=true", "Prefers region=eu (weight 80)", "Never on fsn-2", "Set through the API, in the Compose settings."} {
		if !strings.Contains(words, want) {
			t.Errorf("Configure lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(page, `/services/web/placement`) {
		t.Errorf("a kept-file app has a placement form:\n%s", page)
	}
	resp, body := e.postForm(t, "/apps/shop/services/web/placement", url.Values{"revision": {revision}, "require": {"x"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "placement is set in the file's overlay") {
		t.Errorf("a hand-made post = %d:\n%s", resp.StatusCode, text(body))
	}

	// The Compose form's deploy keeps them, on the app and in the kept overlay.
	_, source := e.get(t, "/apps/shop/configure/source")
	resp, body = e.postForm(t, "/apps/shop/compose", shopFormOf(t, source, url.Values{"compose": {"# edited\n" + keptShopFile}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)
	rec, err := e.Store.GetApp(t.Context(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Compose.Services["web"].Placement; !reflect.DeepEqual(got, shopPlacement) {
		t.Errorf("kept overlay placement = %+v, want %+v", got, shopPlacement)
	}
	if got := webService(t, rec.App).Placement; !reflect.DeepEqual(got, shopPlacement) {
		t.Errorf("app placement = %+v, want %+v", got, shopPlacement)
	}
}

// An app whose file isn't kept keeps its placement through the Compose form too, read from the stored app.
func TestNotKeptFileDeployKeepsPlacement(t *testing.T) {
	e, _ := deployShop(t, shopRequest())
	stored, err := e.Store.GetApp(t.Context(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	a := stored.App
	for i := range a.Services {
		if a.Services[i].Name == "web" {
			a.Services[i].Placement = shopPlacement
		}
	}
	legacy, err := e.Store.SaveApp(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := e.postForm(t, "/apps/shop/compose", url.Values{"revision": {legacy.Revision}, "compose": {shopCompose},
		"svc.web.overlay": {"1"}, "svc.web.healthPath": {"/health"}, "svc.db.overlay": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)
	if got := webService(t, e.storedApp(t, "shop")).Placement; !reflect.DeepEqual(got, shopPlacement) {
		t.Errorf("app placement = %+v, want %+v", got, shopPlacement)
	}
}

// The draft sends the placement it keeps, even for a service with no other overlay, and a new app's draft
// sends none.
func TestComposeDraftPlacement(t *testing.T) {
	d, ok := parseComposeDraft(url.Values{"compose": {shopCompose}, "svc.worker.overlay": {"1"}})
	if !ok {
		t.Fatal("the draft was refused")
	}
	if _, has := d.request().Services["worker"]; has {
		t.Error("a new app's draft sends an overlay for a service with nothing set")
	}
	d.keepPlacement(store.AppRecord{App: app.App{Services: []app.Service{{Name: "worker", Placement: shopPlacement}}}})
	got := d.request().Services["worker"].Placement
	if !reflect.DeepEqual(got, shopPlacement) {
		t.Errorf("an existing app's placement = %+v, want the kept one", got)
	}
	got.Avoid[0] = "changed"
	if shopPlacement.Avoid[0] != "fsn-2" {
		t.Error("the draft shares memory with the stored placement")
	}
	// A kept file's overlay is what is kept, not the app's.
	d.keepPlacement(store.AppRecord{App: app.App{Services: []app.Service{{Name: "worker"}}},
		Compose: &compose.Request{Services: map[string]compose.Overlay{"worker": {Placement: &app.Placement{Avoid: []string{"b"}}}}}})
	if got := d.request().Services["worker"].Placement; !reflect.DeepEqual(got, &app.Placement{Avoid: []string{"b"}}) {
		t.Errorf("kept overlay placement = %+v", got)
	}
}

// A save of the service's own form keeps its placement (and the rest it doesn't offer).
func TestApplyEditKeepsPlacement(t *testing.T) {
	stored := duoApp()
	stored.Services[0].Placement = shopPlacement
	post := postOf(formOf(stored, "r"))
	post.Set(inputName("web", "memory"), "512")
	form, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatal("the form was refused")
	}
	edited := applyEdit(stored, form)
	if !reflect.DeepEqual(edited.Services[0].Placement, shopPlacement) || edited.Services[0].Resources.MemoryMB != 512 {
		t.Errorf("edited = %+v", edited.Services[0])
	}
}

// A stopped app's placement forms are disabled and have no submit: a deploy would be refused.
func TestStoppedAppPlacementFormHasNoSubmit(t *testing.T) {
	e := placementEnv(t)
	if resp, body := e.postAction(t, "/apps/duo/stop"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("stop = %d:\n%s", resp.StatusCode, text(body))
	}
	_, page := e.get(t, "/apps/duo/configure/services")
	if n := strings.Count(page, `cv-plfields" disabled>`); n != 2 || strings.Contains(page, "Save placement") {
		t.Errorf("%d disabled placement forms, and a submit: %v", n, strings.Contains(page, "Save placement"))
	}
	// A post anyway is refused the way every deploy of a stopped app is.
	resp, body := e.postForm(t, "/apps/duo/services/web/placement", url.Values{"revision": {e.revisionOf(t, "duo")}, "require": {"ssd"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), appStoppedText) {
		t.Errorf("post on a stopped app = %d:\n%s", resp.StatusCode, text(body))
	}
}
