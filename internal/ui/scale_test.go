package ui

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 043, console: the Scaling panel of each service on Configure and its form (a deploy on the
// page's revision), Stop scaling, the kept-file app's read-only rule, the per-service form leaving the
// count to scaling, the scale steps in Recent actions and on Monitoring, and the Running tab's line.

// scaleKey is the posted name of a Scaling input.
func scaleKey(svc, field string) string { return inputName(svc, "scale-"+field) }

// scaleEnv has duo deployed: web has two copies and a domain, worker has one copy and no domain.
func scaleEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.deploy(t, duoApp())
	return e
}

// submitScale posts the service's Scaling form from the page as a browser would: the markup's own values
// and the selects (which formsIn can't read) at what the page selects by default, with set laid over them.
func (e *consoleEnv) submitScale(t *testing.T, page, svc string, set url.Values) (*http.Response, string) {
	t.Helper()
	f := formWith(t, page, "/apps/duo/services/"+svc+"/scale")
	over := url.Values{scaleKey(svc, "kind"): {"requestsPerCopy"}, scaleKey(svc, "cooldown"): {"300"}}
	for k, v := range set {
		over[k] = v
	}
	return e.submit(t, f, over)
}

// scaleOf is a scale of web the tests share.
var webScale = &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{RequestsPerCopy: 50}}

func TestScaleRuleSentences(t *testing.T) {
	two := 2
	for _, c := range []struct {
		name string
		svc  app.Service
		want string
	}{
		{"unscaled", app.Service{Replicas: &two}, "Runs 2 copies."},
		{"unscaled, no count", app.Service{}, "Runs 1 copy."},
		{"requests", app.Service{Scale: webScale}, "Runs 1 to 3 copies, keeping each near 50 requests a second; waits 5 minutes between steps."},
		{"cpu", app.Service{Scale: &app.Scale{Min: 2, Max: 6, Target: app.ScaleTarget{CPU: 70}, CooldownSeconds: 600}},
			"Runs 2 to 6 copies, keeping each copy's CPU near 70 % of its reservation; waits 10 minutes between steps."},
		{"p95", app.Service{Scale: &app.Scale{Min: 1, Max: 4, Target: app.ScaleTarget{P95: 300}, CooldownSeconds: 60}},
			"Runs 1 to 4 copies, keeping the slowest 5 % under 300 ms; waits 1 minute between steps."},
		{"seconds", app.Service{Scale: &app.Scale{Min: 1, Max: 2, Target: app.ScaleTarget{CPU: 70}, CooldownSeconds: 90}},
			"Runs 1 to 2 copies, keeping each copy's CPU near 70 % of its reservation; waits 90 seconds between steps."},
		{"one size", app.Service{Scale: &app.Scale{Min: 2, Max: 2, Target: app.ScaleTarget{CPU: 70}}},
			"Runs 2 copies, keeping each copy's CPU near 70 % of its reservation; waits 5 minutes between steps."},
	} {
		got := strings.Join(strings.Fields(plainOf(sentenceParts(scaleRule(c.svc)))), " ")
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestConfigureShowsScalingPerService(t *testing.T) {
	e := scaleEnv(t)
	_, body := e.get(t, "/apps/duo/configure/services")
	words := text(body)
	for _, want := range []string{"Runs 2 copies.", "Runs 1 copy.", "Scale on its own…"} {
		if !strings.Contains(words, want) {
			t.Errorf("Configure lacks %q:\n%s", want, words)
		}
	}
	for _, svc := range []string{"web", "worker"} {
		f := formWith(t, body, "/apps/duo/services/"+svc+"/scale")
		if f.Values.Get("revision") != e.revisionOf(t, "duo") {
			t.Errorf("%s: the form's revision = %q", svc, f.Values.Get("revision"))
		}
	}
	// What a service that doesn't scale starts the form with: its domain says requests, none says CPU.
	f := formWith(t, body, "/apps/duo/services/web/scale")
	if f.Values.Get(scaleKey("web", "min")) != "1" || f.Values.Get(scaleKey("web", "max")) != "3" ||
		f.Values.Get(scaleKey("web", "value")) != "50" || f.Values.Get(scaleKey("web", "start")) != "2" {
		t.Errorf("web's form holds %v", f.Values)
	}
	if !strings.Contains(body, `<option value="requestsPerCopy" selected>`) || !strings.Contains(body, `<option value="cpu" selected>`) {
		t.Errorf("the selects don't start at requests for web and CPU for worker:\n%s", body)
	}
	// No form is open until asked for, and there is nothing to stop.
	if strings.Contains(body, `class="cv-scaledetails" open`) || strings.Contains(body, "Stop scaling") {
		t.Error("an unscaled service's form starts open or offers Stop scaling")
	}
}

func TestSaveScaleDeploysTheRule(t *testing.T) {
	e := scaleEnv(t)
	before := len(e.deployments(t, "duo"))
	_, page := e.get(t, "/apps/duo/configure/services")
	resp, body := e.submitScale(t, page, "web", nil)
	recs := e.deployments(t, "duo")
	if resp.StatusCode != http.StatusSeeOther || len(recs) != before+1 || location(t, resp) != "/apps/duo/deployments#deployment-"+recs[0].Deployment.ID {
		t.Fatalf("POST = %d to %q (%d deployments):\n%s", resp.StatusCode, location(t, resp), len(recs), text(body))
	}
	e.waitDeploy(t, recs[0].Deployment.ID)
	stored := e.storedApp(t, "duo")
	web := stored.Services[0]
	if !reflect.DeepEqual(web.Scale, webScale) || web.Replicas == nil || *web.Replicas != 2 {
		t.Errorf("web = scale %+v, replicas %v; want %+v and 2", web.Scale, web.Replicas, webScale)
	}
	if stored.Services[1].Scale != nil || stored.Services[1].Image != duoApp().Services[1].Image {
		t.Errorf("worker changed: %+v", stored.Services[1])
	}
	// The page says the rule, opens the form with it, and offers Stop scaling.
	_, page = e.get(t, "/apps/duo/configure/services")
	if want := "Runs 1 to 3 copies, keeping each near 50 requests a second; waits 5 minutes between steps."; !strings.Contains(text(page), want) {
		t.Errorf("Configure lacks %q:\n%s", want, text(page))
	}
	if !strings.Contains(page, `class="cv-scaledetails" open`) || !strings.Contains(page, ">Stop scaling<") {
		t.Errorf("a scaled service's form isn't open, or has no Stop scaling:\n%s", page)
	}
	// A different rule: CPU, two to five, a longer wait, and the default wait left unwritten.
	resp, _ = e.submitScale(t, page, "web", url.Values{scaleKey("web", "min"): {"2"}, scaleKey("web", "max"): {"5"},
		scaleKey("web", "kind"): {"cpu"}, scaleKey("web", "value"): {"80"}, scaleKey("web", "cooldown"): {"600"}, scaleKey("web", "start"): {""}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("second save = %d", resp.StatusCode)
	}
	web = e.storedApp(t, "duo").Services[0]
	if want := (&app.Scale{Min: 2, Max: 5, Target: app.ScaleTarget{CPU: 80}, CooldownSeconds: 600}); !reflect.DeepEqual(web.Scale, want) || web.Replicas != nil {
		t.Errorf("web = scale %+v, replicas %v; want %+v and none", web.Scale, web.Replicas, want)
	}
}

func TestSaveScaleRefusalsAreDrawnAtTheField(t *testing.T) {
	for _, c := range []struct {
		name  string
		svc   string
		set   url.Values
		field string // the input's id suffix that has the message
		want  string
	}{
		{"min isn't a number", "web", url.Values{scaleKey("web", "min"): {"many"}}, "scale-min", "Enter a whole number"},
		{"min is zero", "web", url.Values{scaleKey("web", "min"): {"0"}}, "scale-min", "Enter a whole number"},
		{"max is below min", "web", url.Values{scaleKey("web", "min"): {"3"}, scaleKey("web", "max"): {"2"}, scaleKey("web", "start"): {""}}, "scale-max", "Max is 2, less than min (3)."},
		{"max is too many", "web", url.Values{scaleKey("web", "max"): {"21"}}, "scale-max", "Max is 21; at most 20 copies are allowed."},
		{"a target out of range", "web", url.Values{scaleKey("web", "kind"): {"cpu"}, scaleKey("web", "value"): {"500"}}, "scale-value", "Target is 500; it must be 10 to 200"},
		{"a target that isn't a number", "web", url.Values{scaleKey("web", "value"): {"fast"}}, "scale-value", "Enter a whole number"},
		{"requests without a domain", "worker", nil, "scale-value", "RequestsPerCopy is measured at the proxy"},
		{"a start outside the range", "web", url.Values{scaleKey("web", "start"): {"9"}}, "scale-start", "Starting count is 9, outside the scale's 1 to 3"},
		{"a wait that isn't offered", "web", url.Values{scaleKey("web", "cooldown"): {"45"}}, "scale-cooldown", "Choose a wait from the list."},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := scaleEnv(t)
			before, deployments := e.storedApp(t, "duo"), len(e.deployments(t, "duo"))
			_, page := e.get(t, "/apps/duo/configure/services")
			resp, body := e.submitScale(t, page, c.svc, c.set)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, text(body))
			}
			id := "svc-" + c.svc + "-" + c.field
			i := strings.Index(body, `id="`+id+`"`)
			j := strings.Index(body, `id="`+id+`-error"`)
			if i < 0 || j < 0 || !strings.Contains(text(body[j:j+400]), c.want) {
				t.Errorf("no %q at the %s input:\n%s", c.want, c.field, text(body))
			}
			if !strings.Contains(body[i:i+700], `aria-invalid="true"`) {
				t.Errorf("the %s input isn't marked invalid", c.field)
			}
			if !strings.Contains(body, `class="cv-scaledetails" open`) {
				t.Error("the refused form isn't open")
			}
			if !reflect.DeepEqual(e.storedApp(t, "duo"), before) || len(e.deployments(t, "duo")) != deployments {
				t.Error("a refused post changed the app or deployed")
			}
		})
	}
	// What was typed comes back in the form.
	e := scaleEnv(t)
	_, page := e.get(t, "/apps/duo/configure/services")
	_, body := e.submitScale(t, page, "web", url.Values{scaleKey("web", "min"): {"3"}, scaleKey("web", "max"): {"2"}, scaleKey("web", "start"): {""}})
	if !strings.Contains(body, `name="svc.web.scale-min" value="3"`) || !strings.Contains(body, `name="svc.web.scale-max" value="2"`) {
		t.Errorf("what was typed was lost:\n%s", body)
	}
}

func TestStopScalingKeepsTheCountItHasNow(t *testing.T) {
	e := scaleEnv(t)
	a := e.storedApp(t, "duo")
	a.Services[0].Scale = webScale
	e.deploy(t, a)
	// The autoscaler moved web to 3 copies.
	if err := e.Store.SetServiceCount(context.Background(), "duo", "web", 3); err != nil {
		t.Fatal(err)
	}
	_, page := e.get(t, "/apps/duo/configure/services")
	revision := e.revisionOf(t, "duo")
	before := len(e.deployments(t, "duo"))
	resp, body := e.postForm(t, "/apps/duo/services/web/scale", url.Values{"revision": {revision}, "action": {"stop"}})
	recs := e.deployments(t, "duo")
	if resp.StatusCode != http.StatusSeeOther || len(recs) != before+1 {
		t.Fatalf("Stop scaling = %d (%d deployments):\n%s", resp.StatusCode, len(recs), text(body))
	}
	e.waitDeploy(t, recs[0].Deployment.ID)
	web := e.storedApp(t, "duo").Services[0]
	if web.Scale != nil || web.Replicas == nil || *web.Replicas != 3 {
		t.Errorf("web = scale %+v, replicas %v; want no scale and 3 copies", web.Scale, web.Replicas)
	}
	if !strings.Contains(text(page), "Stop scaling") {
		t.Error("the page had no Stop scaling")
	}
	// A service that doesn't scale has nothing to stop.
	resp, body = e.postForm(t, "/apps/duo/services/worker/scale", url.Values{"revision": {e.revisionOf(t, "duo")}, "action": {"stop"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "doesn't scale on its own.") {
		t.Errorf("stopping nothing = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestSaveScaleStaleRevisionAndUnknowns(t *testing.T) {
	e := scaleEnv(t)
	before := e.storedApp(t, "duo")
	_, page := e.get(t, "/apps/duo/configure/services")
	resp, body := e.submitScale(t, page, "web", url.Values{"revision": {"0000000000000000"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), changedSincePage) || !reflect.DeepEqual(e.storedApp(t, "duo"), before) {
		t.Errorf("stale = %d:\n%s", resp.StatusCode, text(body))
	}
	rev := e.revisionOf(t, "duo")
	if resp, body := e.postForm(t, "/apps/duo/services/nope/scale", url.Values{"revision": {rev}}); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "This app has no service nope.") {
		t.Errorf("an unknown service = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, _ := e.postForm(t, "/apps/duo/services/web/scale", url.Values{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no revision = %d, want 400", resp.StatusCode)
	}
	if resp, _ := e.postForm(t, "/apps/nope/services/web/scale", url.Values{"revision": {"x"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown app = %d, want 404", resp.StatusCode)
	}
}

// A service that can't scale says why and has no form.
func TestScalingBlockedByVolumesAndPublishedPorts(t *testing.T) {
	e := newConsoleEnv(t)
	a := duoApp()
	a.Services[1].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	e.deploy(t, a)
	_, body := e.get(t, "/apps/duo/configure/services")
	if !strings.Contains(text(body), "A service with volumes runs one copy, so it can't scale.") || strings.Contains(body, "/services/worker/scale") {
		t.Errorf("worker's panel:\n%s", text(body))
	}
	if !strings.Contains(body, "/services/web/scale") {
		t.Error("web has no form")
	}
}

// The per-service form leaves a scaled service's count to the Scaling panel, and a save keeps it.
func TestPerServiceFormLeavesTheCountToScaling(t *testing.T) {
	stored := duoApp()
	stored.Services[0].Scale = webScale // replicas stays 2: the starting count
	post := postOf(formOf(stored, "r"))
	post.Set(inputName("web", "memory"), "512")
	post.Set(inputName("web", "copies"), "9") // a form drawn before scaling, or a hand-made post
	form, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatal("the form was refused")
	}
	edited := applyEdit(stored, form)
	web := edited.Services[0]
	if !reflect.DeepEqual(web.Scale, webScale) || web.Replicas == nil || *web.Replicas != 2 || web.Resources.MemoryMB != 512 {
		t.Errorf("edited web = %+v", web)
	}
	// An unscaled service still takes its Copies.
	post.Set(inputName("worker", "copies"), "4")
	form, _ = parseEditForm(post, stored)
	if got := applyEdit(stored, form).Services[1].Replicas; got == nil || *got != 4 {
		t.Errorf("worker's replicas = %v, want 4", got)
	}

	e := scaleEnv(t)
	a := e.storedApp(t, "duo")
	a.Services[0].Scale = webScale
	e.deploy(t, a)
	_, page := e.get(t, "/apps/duo/configure/services")
	if !strings.Contains(text(page), "Copies Set by scaling, 1–3") || strings.Contains(page, `name="svc.web.copies"`) || !strings.Contains(page, `name="svc.worker.copies"`) {
		t.Errorf("web's Copies input isn't replaced by the words:\n%s", text(page))
	}
}

// A kept-file app shows the rule read-only; a post made by hand is refused; the Compose form keeps the
// overlay's scale through a deploy.
func TestKeptFileAppScalingIsReadOnly(t *testing.T) {
	req := shopRequest()
	req.Compose = keptShopFile
	ov := req.Services["web"]
	ov.Scale = &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{CPU: 70}}
	req.Services["web"] = ov
	e, revision := deployShop(t, req)

	_, page := e.get(t, "/apps/shop/configure/services")
	words := text(page)
	for _, want := range []string{"Runs 1 to 3 copies, keeping each copy's CPU near 70 % of its reservation", "Set it in the Compose overlay."} {
		if !strings.Contains(words, want) {
			t.Errorf("Configure lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(page, `/services/web/scale`) {
		t.Errorf("a kept-file app has a scaling form:\n%s", page)
	}
	resp, body := e.postForm(t, "/apps/shop/services/web/scale", url.Values{"revision": {revision}, scaleKey("web", "min"): {"1"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "scaling is set in the file's overlay") {
		t.Errorf("a hand-made post = %d:\n%s", resp.StatusCode, text(body))
	}

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
	want := &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{CPU: 70}}
	if got := rec.Compose.Services["web"].Scale; !reflect.DeepEqual(got, want) {
		t.Errorf("kept overlay scale = %+v, want %+v", got, want)
	}
	if got := webService(t, rec.App).Scale; !reflect.DeepEqual(got, want) {
		t.Errorf("app scale = %+v, want %+v", got, want)
	}
}

// The draft sends the scale it keeps, even for a service with no other overlay.
func TestComposeDraftScale(t *testing.T) {
	d, ok := parseComposeDraft(url.Values{"compose": {shopCompose}, "svc.worker.overlay": {"1"}})
	if !ok {
		t.Fatal("the draft was refused")
	}
	d.keepScale(store.AppRecord{App: app.App{Services: []app.Service{{Name: "worker", Scale: webScale}}}})
	got := d.request().Services["worker"].Scale
	if !reflect.DeepEqual(got, webScale) {
		t.Errorf("an existing app's scale = %+v, want the kept one", got)
	}
	got.Max = 9
	if webScale.Max != 3 {
		t.Error("the draft shares memory with the stored scale")
	}
	d.keepScale(store.AppRecord{App: app.App{Services: []app.Service{{Name: "worker"}}},
		Compose: &compose.Request{Services: map[string]compose.Overlay{"worker": {Scale: &app.Scale{Min: 2, Max: 4, Target: app.ScaleTarget{CPU: 60}}}}}})
	if got := d.request().Services["worker"].Scale; got == nil || got.Min != 2 {
		t.Errorf("kept overlay scale = %+v", got)
	}
}

func TestStoppedAppScalingFormHasNoSubmit(t *testing.T) {
	e := scaleEnv(t)
	if resp, body := e.postAction(t, "/apps/duo/stop"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("stop = %d:\n%s", resp.StatusCode, text(body))
	}
	_, page := e.get(t, "/apps/duo/configure/services")
	if n := strings.Count(page, `cv-scfields" disabled>`); n != 2 || strings.Contains(page, "Save scaling") {
		t.Errorf("%d disabled forms and a Save scaling: %v", n, strings.Contains(page, "Save scaling"))
	}
	resp, body := e.postForm(t, "/apps/duo/services/web/scale", url.Values{"revision": {e.revisionOf(t, "duo")},
		scaleKey("web", "min"): {"1"}, scaleKey("web", "max"): {"3"}, scaleKey("web", "kind"): {"cpu"}, scaleKey("web", "value"): {"70"}, scaleKey("web", "cooldown"): {"300"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), appStoppedText) {
		t.Errorf("post on a stopped app = %d:\n%s", resp.StatusCode, text(body))
	}
}

// ---- Recent actions ---------------------------------------------------------------------

func TestRecentActionsSayScaleSteps(t *testing.T) {
	e := scaleEnv(t)
	at := time.Now().UTC().Add(-time.Minute)
	ok := store.ActionRecord{ID: "act_ok", AppID: "duo", Action: store.ActionScale, Trigger: string(app.TriggerAutoscale), Status: store.ActionSucceeded,
		Service: "web", StartedAt: at, FinishedAt: &at, FromCount: new(1), ToCount: new(2), Reason: "62 requests a second per copy over the last 3 minutes, target 50"}
	bad := ok
	bad.ID, bad.Status, bad.Error, bad.Reason, bad.StartedAt = "act_bad", store.ActionFailed, "Nomad refused the new count.", "", at.Add(-time.Minute)
	bad.FromCount, bad.ToCount = new(2), new(3)
	for _, a := range []store.ActionRecord{ok, bad} {
		if err := e.Store.InsertAction(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	_, body := e.get(t, "/parts/apps/duo/actions")
	words := text(body)
	for _, want := range []string{
		"Scaled web 1 → 2 on its own", "62 requests a second per copy over the last 3 minutes, target 50",
		"Couldn't scale web 2 → 3 on its own", "Nomad refused the new count.",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("Recent actions lack %q:\n%s", want, words)
		}
	}
	// The failed step has no reason line of its own: the error is the one under it.
	if strings.Count(words, "target 50") != 1 {
		t.Errorf("the reason is drawn more than once:\n%s", words)
	}
}

// ---- Monitoring -------------------------------------------------------------------------

func TestMonitoringRowSaysTheBounds(t *testing.T) {
	v := service.MetricsView{Range: metrics.Range6h, At: monNow, Services: []service.ServiceMetrics{
		{AppID: "shop", AppName: "shop", Service: "web", Copies: 2, ScaleMin: 1, ScaleMax: 3, HasDomain: true},
		{AppID: "shop", AppName: "shop", Service: "db", Copies: 1, HasDomain: true},
		{AppID: "old", AppName: "old", Service: "web", Copies: 2, ScaleMin: 1, ScaleMax: 3, HasDomain: true, State: service.ServiceStopped},
	}}
	rows := monBuild(v, false).table().Rows
	if rows[0].Copies != "2 copies (1–3)" || rows[1].Copies != "1 copy" || rows[2].Copies != "stopped" {
		t.Errorf("copies cells = %q, %q, %q", rows[0].Copies, rows[1].Copies, rows[2].Copies)
	}
}

func TestMonitoringListsTheLastScaleSteps(t *testing.T) {
	e := scaleEnv(t)
	// Nothing taken yet: no Scaling section, and the slot a refresh fills is on the page.
	_, page := e.get(t, "/monitoring")
	if strings.Contains(page, `aria-label="Scaling"`) || !strings.Contains(page, `id="mon-scales"`) {
		t.Errorf("an empty list is drawn, or its slot is missing:\n%s", text(page))
	}
	at := time.Now().UTC().Add(-time.Minute)
	for i, st := range []struct {
		from, to int
		failed   bool
	}{{1, 2, false}, {2, 3, true}} {
		a := store.ActionRecord{ID: "act_" + string(rune('a'+i)), AppID: "duo", Action: store.ActionScale, Trigger: string(app.TriggerAutoscale), Status: store.ActionSucceeded,
			Service: "web", StartedAt: at.Add(time.Duration(i) * time.Second), FinishedAt: &at, FromCount: new(st.from), ToCount: new(st.to), Reason: "62 requests a second per copy, target 50"}
		if st.failed {
			a.Status, a.Reason, a.Error = store.ActionFailed, "", "Nomad refused the new count."
		}
		if err := e.Store.InsertAction(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/monitoring", "/parts/monitoring"} {
		_, body := e.get(t, path)
		words := text(body)
		for _, want := range []string{"Scaling", "duo/web 1 → 2", "62 requests a second per copy, target 50", "duo/web couldn't scale 2 → 3", "Nomad refused the new count."} {
			if !strings.Contains(words, want) {
				t.Errorf("%s lacks %q:\n%s", path, want, words)
			}
		}
	}
	// The part swaps the list by its id, so it refreshes on the metrics notice.
	if _, part := e.get(t, "/parts/monitoring"); !strings.Contains(part, `hx-swap-oob="innerHTML:#mon-scales"`) {
		t.Errorf("the refresh doesn't swap the list:\n%s", part)
	}
}

// ---- Running -----------------------------------------------------------------------------

func TestRunningTabSaysWhatScales(t *testing.T) {
	e := scaleEnv(t)
	a := e.storedApp(t, "duo")
	a.Services[0].Scale = webScale
	e.deploy(t, a)
	e.copies("duo", "web", 1, 2, 0)
	_, body := e.get(t, "/parts/apps/duo/copies")
	if want := "web 2 copies · scales 1–3"; !strings.Contains(text(body), want) {
		t.Errorf("the Running tab lacks %q:\n%s", want, text(body))
	}
	if strings.Contains(text(body), "worker 1 copy") {
		t.Error("a service that doesn't scale has a line")
	}
	// The autoscaler moved it: the line follows the stored count.
	if err := e.Store.SetServiceCount(context.Background(), "duo", "web", 3); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(t, "/parts/apps/duo/copies")
	if want := "web 3 copies · scales 1–3"; !strings.Contains(text(body), want) {
		t.Errorf("the Running tab lacks %q:\n%s", want, text(body))
	}
}

// The draft sends the disconnect policy it keeps, even for a service with no other overlay.
func TestComposeDraftDisconnect(t *testing.T) {
	d, ok := parseComposeDraft(url.Values{"compose": {shopCompose}, "svc.worker.overlay": {"1"}})
	if !ok {
		t.Fatal("the draft was refused")
	}
	kept := &app.Disconnect{LostAfterSeconds: 120}
	d.keepDisconnect(store.AppRecord{App: app.App{Services: []app.Service{{Name: "worker", Disconnect: kept}}}})
	got := d.request().Services["worker"].Disconnect
	if !reflect.DeepEqual(got, kept) {
		t.Errorf("an existing app's disconnect = %+v, want the kept one", got)
	}
	got.LostAfterSeconds = 999
	if kept.LostAfterSeconds != 120 {
		t.Error("the draft shares memory with the stored policy")
	}
	d.keepDisconnect(store.AppRecord{App: app.App{Services: []app.Service{{Name: "worker"}}},
		Compose: &compose.Request{Services: map[string]compose.Overlay{"worker": {Disconnect: &app.Disconnect{LostAfterSeconds: 600}}}}})
	if got := d.request().Services["worker"].Disconnect; got == nil || got.LostAfterSeconds != 600 {
		t.Errorf("kept overlay disconnect = %+v", got)
	}
}
