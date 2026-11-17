package ui

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Placement on Configure (feature 042): where a service's copies may run. Each service has a Placement
// panel that says the rules as sentences; for an app edited in the console it also has a form with the
// three rules, posted to /apps/{id}/services/{service}/placement with the page's revision, which is a
// deploy like the secrets' edits. An app that keeps its Compose file shows the rules read-only: they are
// set in the file's overlay, and the Compose form keeps them (composeDraft.keepPlacement).

// placementView is one service's Placement panel. Editable is false for an app that keeps its Compose
// file; Stopped says the app is stopped, so the form has no submit (a deploy would be refused).
type placementView struct {
	Service  string
	AnchorID string
	Rules    []sentence
	Editable bool
	Stopped  bool
	Action   string
	Revision string
	Require  formField
	Prefer   formField
	Avoid    []avoidOption
	// AvoidError is about the servers' checkboxes as a whole.
	AvoidError sentence
	// Notice is about the panel as a whole: a refusal that names no field, or that the failed deploy
	// was saved.
	Notice sentence
}

// avoidOption is one checkbox of "Never on": a server by name. Gone is a name the service avoids that is
// no server any more: it stays listed, so saving the form never drops it silently.
type avoidOption struct {
	ID      string
	Name    string
	Checked bool
	Gone    bool
}

// placementPost is what a refused placement post typed, with the messages at its inputs; configurePage
// draws the panel of that service from it instead of from the stored app.
type placementPost struct {
	Service               string
	Require, Prefer       string
	Avoid                 []string
	RequireErr, PreferErr sentence
	AvoidErr, Notice      sentence
}

// listOf joins items with commas and "and", each in mono: `a`, `a and b`, `a, b and c`.
func listOf(items []string) sentence {
	var out sentence
	for i, it := range items {
		switch {
		case i == 0:
		case i == len(items)-1:
			out = append(out, plain(" and "))
		default:
			out = append(out, plain(", "))
		}
		out = append(out, mono(it))
	}
	return out
}

// placementRules says a placement's rules as sentences. No placement is "Any server that accepts work."
func placementRules(p *app.Placement) []sentence {
	if p == nil || p.Empty() {
		return []sentence{plainS("Any server that accepts work.")}
	}
	var out []sentence
	if len(p.Require) > 0 {
		var pairs []string
		for _, name := range sortedKeys(p.Require) {
			pairs = append(pairs, name+"="+p.Require[name])
		}
		out = append(out, join(plainS("Only on servers with "), listOf(pairs)))
	}
	for _, pref := range p.Prefer {
		s := join(plainS("Prefers "), sentence{mono(pref.Label + "=" + pref.Value)})
		if pref.Weight != 0 && pref.Weight != app.DefaultPreferenceWeight {
			s = append(s, plain(" (weight "+strconv.Itoa(pref.Weight)+")"))
		}
		out = append(out, s)
	}
	if len(p.Avoid) > 0 {
		out = append(out, join(plainS("Never on "), listOf(p.Avoid)))
	}
	return out
}

// preferText is the Prefer input for these preferences: "ssd=true 80, region=eu".
func preferText(prefs []app.Preference) string {
	items := make([]string, len(prefs))
	for i, p := range prefs {
		items[i] = p.Label + "=" + p.Value
		if p.Weight != 0 {
			items[i] += " " + strconv.Itoa(p.Weight)
		}
	}
	return strings.Join(items, ", ")
}

// parsePrefer reads "ssd=true 80, region=eu": pairs as for labels, each with an optional weight after a
// space. The weight's range is the model's to judge.
func parsePrefer(text string) ([]app.Preference, error) {
	var out []app.Preference
	for _, item := range splitItems(text) {
		pair, weight := item, ""
		if f := strings.Fields(item); len(f) > 1 {
			if len(f) > 2 {
				return nil, fmt.Errorf("write %q as label=value, then a weight if you want one", item)
			}
			pair, weight = f[0], f[1]
		}
		labels, err := parsePairs(pair)
		if err != nil {
			return nil, err
		}
		pref := app.Preference{}
		for name, value := range labels {
			pref.Label, pref.Value = name, value
		}
		if weight != "" {
			w, err := strconv.Atoi(weight)
			if err != nil {
				return nil, fmt.Errorf("%q is not a weight: use a whole number from 1 to %d", weight, app.MaxPreferenceWeight)
			}
			pref.Weight = w
		}
		out = append(out, pref)
	}
	return out, nil
}

// placement is the placement a post holds, nil when it holds no rule. A problem of the text is set on
// the draft's field and ok is false.
func (p *placementPost) placement() (*app.Placement, bool) {
	ok := true
	out := &app.Placement{}
	if labels, err := parsePairs(p.Require); err != nil {
		p.RequireErr, ok = plainS(refusalWords(err)), false
	} else if len(labels) > 0 {
		out.Require = labels
	}
	if prefs, err := parsePrefer(p.Prefer); err != nil {
		p.PreferErr, ok = plainS(refusalWords(err)), false
	} else {
		out.Prefer = prefs
	}
	out.Avoid = slices.Clone(p.Avoid)
	slices.Sort(out.Avoid)
	out.Avoid = slices.Compact(out.Avoid)
	if out.Empty() {
		return nil, ok
	}
	return out, ok
}

// serverNames are the names of the servers Caravana manages and that have joined, in order: what "Never
// on" offers.
func (s *server) serverNames(r *http.Request) []string {
	v, err := s.svc.ListServers(r.Context())
	if err != nil {
		s.log.Warn("can't list the servers for Placement", "error", err)
		return nil
	}
	var names []string
	for _, sv := range v.Servers {
		if sv.Managed && sv.Name != "" && sv.State != "waiting" && sv.State != "joining" {
			names = append(names, sv.Name)
		}
	}
	slices.Sort(names)
	return names
}

// placementViewOf builds one service's panel. servers are the names "Never on" offers; draft, when its
// service is this one, holds what a refused post typed.
func placementViewOf(id, revision string, svc app.Service, servers []string, editable, stopped bool, draft *placementPost) placementView {
	v := placementView{
		Service: svc.Name, AnchorID: "placement-" + svc.Name, Rules: placementRules(svc.Placement),
		Editable: editable, Stopped: stopped, Revision: revision,
		Action:  "/apps/" + id + "/services/" + svc.Name + "/placement",
		Require: formField{ID: inputID(svc.Name, "placement-require")},
		Prefer:  formField{ID: inputID(svc.Name, "placement-prefer")},
	}
	avoided := []string(nil)
	if p := svc.Placement; p != nil {
		v.Require.Value, v.Prefer.Value = labelsText(p.Require), preferText(p.Prefer)
		avoided = p.Avoid
	}
	if draft != nil && draft.Service == svc.Name {
		v.Require.Value, v.Prefer.Value, avoided = draft.Require, draft.Prefer, draft.Avoid
		v.Require.Error, v.Prefer.Error = draft.RequireErr, draft.PreferErr
		v.AvoidError, v.Notice = draft.AvoidErr, draft.Notice
		v.Require.Alert = len(v.Require.Error) > 0
		v.Prefer.Alert = len(v.Prefer.Error) > 0 && len(v.Require.Error) == 0
	}
	names := slices.Clone(servers)
	for _, name := range avoided {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		v.Avoid = append(v.Avoid, avoidOption{ID: inputID(svc.Name, "placement-avoid-"+name), Name: name,
			Checked: slices.Contains(avoided, name), Gone: !slices.Contains(servers, name)})
	}
	return v
}

// withPlacement is the app with service i's placement replaced; a is not changed.
func withPlacement(a app.App, i int, p *app.Placement) app.App {
	a.Services = slices.Clone(a.Services)
	a.Services[i].Placement = p
	return a
}

// placementFieldRE splits a model path about placement: services[<i>].placement[.<rule>].
var placementFieldRE = regexp.MustCompile(`^services\[\d+\]\.placement(?:\.(require|prefer|avoid))?(?:\.([a-z0-9-]+)|\[(\d+)\](?:\.(\w+))?)?$`)

// placeProblem puts a model path's message at the input it is about. named says to start a label's
// message with the label (a value's error does; a refusal that already names it doesn't). It reports false
// for a path that is no placement rule's.
func (p *placementPost) placeProblem(path, message string, named bool) bool {
	m := placementFieldRE.FindStringSubmatch(path)
	if m == nil {
		return false
	}
	text := plainS(refusalWords(errors.New(message)))
	switch m[1] {
	case "require":
		if m[2] != "" && named {
			text = sentence{mono(m[2]), plain(": " + message)}
		}
		p.RequireErr = text
	case "prefer":
		if m[3] != "" {
			n, _ := strconv.Atoi(m[3])
			where := fmt.Sprintf("Preference %d", n+1)
			if m[4] != "" {
				where += " (" + m[4] + ")"
			}
			text = plainS(where + ": " + message)
		}
		p.PreferErr = text
	case "avoid":
		p.AvoidErr = text
	default:
		return false
	}
	return true
}

// savePlacement answers POST /apps/{id}/services/{service}/placement: the three rules typed replace the
// service's placement, as a deploy on the revision the page was drawn from; an empty form clears them.
// Success goes to the deployment's entry. A refusal draws the Configure tab again with what was typed and
// the message at its input.
func (s *server) savePlacement(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id, name := r.PathValue("id"), r.PathValue("service")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "save it")
	if !ok {
		return
	}
	rec, err := s.svc.GetApp(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	i, found := serviceNamed(rec.App, name)
	if !found {
		s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Notice: sentence{plain("This app has no service "), mono(name), plain(".")}})
		return
	}
	if rec.Compose != nil {
		s.configurePage(w, r, http.StatusConflict, id, configureExtras{Notice: sentence{plain("This app keeps its Compose file, so its placement is set in the file's overlay. Change it in the file above and deploy it.")}})
		return
	}

	post := placementPost{Service: name, Require: r.PostForm.Get("require"), Prefer: r.PostForm.Get("prefer"), Avoid: r.PostForm["avoid"]}
	p, ok := post.placement()
	if !ok {
		s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Placement: &post})
		return
	}
	edited := withPlacement(rec.App, i, p)
	deployment, err := s.svc.DeployIf(ctx, edited, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}

	status := http.StatusUnprocessableEntity
	var fieldErr *app.FieldError
	var placeErr *deploy.PlacementError
	var deployErr *service.DeployError
	_ = errors.As(err, &deployErr)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case errors.Is(err, service.ErrAppChanged):
		status, post.Notice = http.StatusConflict, plainS(changedSincePage)
	case errors.As(err, &fieldErr) && post.placeProblem(fieldErr.Field, fieldErr.Message, true):
	case errors.As(err, &placeErr):
		status = http.StatusConflict
		if !post.placeProblem(placeErr.Field, placeErr.Message, false) {
			post.Notice = plainS(refusalWords(errors.New(placeErr.Message)))
		}
		if deployErr != nil {
			post.Notice = append(post.Notice, plain(" "+savedNote))
		}
	default:
		var notice sentence
		if status, notice, ok = refusalOf(edited, err); !ok {
			s.serverError(w, r, "save placement", err)
			return
		}
		post.Notice = notice
	}
	s.configurePage(w, r, status, id, configureExtras{Placement: &post})
}
