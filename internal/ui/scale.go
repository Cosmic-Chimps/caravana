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
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Scaling on Configure (feature 043): how many copies a service runs, and whether Caravana changes
// that count by itself. Each service has a Scaling panel that says the rule as one sentence; for an
// app edited in the console it also has a form (Min, Max, Target, Cooldown, Starting count), posted
// to /apps/{id}/services/{service}/scale with the page's revision, which is a deploy like the
// placement's. "Stop scaling" is the same route with action=stop: it removes the rule and keeps the
// count the service has now. An app that keeps its Compose file shows the rule read-only: it is set
// in the file's overlay, and the Compose form keeps it (composeDraft.keepScale).

// The cooldowns the select offers, in seconds.
var scaleCooldowns = []int{60, 120, 300, 600, 1800, 3600}

// The targets the select offers, with how each reads in the select.
var scaleKinds = []struct{ Kind, Label string }{
	{app.ScaleOnRequests, "Requests a second per copy"},
	{app.ScaleOnCPU, "CPU, percent of the reservation"},
	{app.ScaleOnP95, "Slowest 5 %, in milliseconds"},
}

// scaleView is one service's Scaling panel. Editable is false for an app that keeps its Compose file;
// Stopped says the app is stopped, so the form has no submit (a deploy would be refused). Blocked is
// why the service can't scale at all (volumes, a published port): then there is no form.
type scaleView struct {
	Service  string
	AnchorID string
	Scaled   bool
	Rule     sentence
	Blocked  sentence
	Editable bool
	Stopped  bool
	// Open says the form starts open (a refused post, or a service that scales already).
	Open     bool
	Action   string
	Revision string

	Min, Max, Value, Start formField
	KindID, CooldownID     string
	Kinds, Cooldowns       []formOption
	CooldownError          sentence
	// Notice is about the panel as a whole.
	Notice sentence
}

// F returns the named number input of the form ("min", "max", "value", "start"), ready for the "field"
// template. The inputs post as svc.<service>.scale-<name>.
func (v scaleView) F(name string) fieldView {
	f := map[string]*formField{"min": &v.Min, "max": &v.Max, "value": &v.Value, "start": &v.Start}[name]
	spec := inputSpec{Name: "scale-" + name, Numeric: true}
	switch name {
	case "min":
		spec.Label = "Min"
	case "max":
		spec.Label = "Max"
	case "value":
		spec.Label = "Target"
		spec.Hint = "Requests a second per copy (1 to 100000), or percent of the CPU the copy reserves (10 to 200), or milliseconds (10 to 60000), by the kind chosen."
	case "start":
		spec.Label = "Starting count"
		spec.Hint = "Where the first deploy begins. Later deploys keep the count the service has now."
	}
	return fieldView{inputSpec: spec, Field: f, Service: v.Service}
}

// KindName and CooldownName are the names of the two selects in the posted form.
func (v scaleView) KindName() string     { return inputName(v.Service, "scale-kind") }
func (v scaleView) CooldownName() string { return inputName(v.Service, "scale-cooldown") }

// scalePost is what a refused scale post typed, with the messages at its inputs; configurePage draws
// the panel of that service from it instead of from the stored app.
type scalePost struct {
	Service                                string
	Min, Max, Kind, Value, Cooldown, Start string
	MinErr, MaxErr, ValueErr               sentence
	CooldownErr, StartErr, Notice          sentence
}

// cooldownWords says a cooldown: "5 minutes", "1 minute", "90 seconds".
func cooldownWords(seconds int) string {
	if seconds%60 == 0 {
		return plural(seconds/60, "minute")
	}
	return plural(seconds, "second")
}

// scaleRule says a service's scaling as one sentence.
func scaleRule(svc app.Service) sentence {
	sc := svc.Scale
	if sc == nil {
		n := 1
		if svc.Replicas != nil {
			n = *svc.Replicas
		}
		return sentence{plain("Runs "), mono(copyCount(n)), plain(".")}
	}
	var s sentence
	if sc.Min == sc.Max {
		s = sentence{plain("Runs "), mono(copyCount(sc.Min))}
	} else {
		s = sentence{plain("Runs "), mono(fmt.Sprintf("%d to %d", sc.Min, sc.Max)), plain(" copies")}
	}
	t := sc.Target
	switch t.Kind() {
	case app.ScaleOnCPU:
		s = append(s, plain(", keeping each copy's CPU near "), mono(strconv.Itoa(t.CPU)+" %"), plain(" of its reservation"))
	case app.ScaleOnP95:
		s = append(s, plain(", keeping the slowest 5 % under "), mono(strconv.Itoa(t.P95)+" ms"))
	default:
		s = append(s, plain(", keeping each near "), mono(strconv.Itoa(t.RequestsPerCopy)), plain(" requests a second"))
	}
	return append(s, plain("; waits "+cooldownWords(int(sc.Cooldown().Seconds()))+" between steps."))
}

// scaleBlocked is why a service can't scale on its own, or nil.
func scaleBlocked(svc app.Service) sentence {
	switch {
	case len(svc.Volumes) > 0:
		return plainS("A service with volumes runs one copy, so it can't scale.")
	case svc.Publishes():
		return plainS("A service with a published port runs one copy, so it can't scale.")
	}
	return nil
}

// scaleViewOf builds one service's panel. draft, when its service is this one, holds what a refused
// post typed.
func scaleViewOf(id, revision string, svc app.Service, editable, stopped bool, draft *scalePost) scaleView {
	v := scaleView{
		Service: svc.Name, AnchorID: "scale-" + svc.Name, Scaled: svc.Scale != nil, Rule: scaleRule(svc),
		Blocked: scaleBlocked(svc), Editable: editable, Stopped: stopped, Revision: revision,
		Action: "/apps/" + id + "/services/" + svc.Name + "/scale",
		Min:    formField{ID: inputID(svc.Name, "scale-min")}, Max: formField{ID: inputID(svc.Name, "scale-max")},
		Value: formField{ID: inputID(svc.Name, "scale-value")}, Start: formField{ID: inputID(svc.Name, "scale-start")},
		KindID: inputID(svc.Name, "scale-kind"), CooldownID: inputID(svc.Name, "scale-cooldown"),
	}
	kind, cooldown := app.ScaleOnRequests, app.DefaultScaleCooldown
	if len(svc.Domains) == 0 {
		kind = app.ScaleOnCPU // requests and the slowest 5 % are measured at the proxy: a domain is needed
	}
	// What an unscaled service's form starts with: its count as the floor, up to three copies.
	start := 1
	if svc.Replicas != nil {
		start = *svc.Replicas
	}
	v.Min.Value, v.Max.Value = "1", strconv.Itoa(max(3, start))
	v.Value.Value = map[string]string{app.ScaleOnRequests: "50", app.ScaleOnCPU: "70", app.ScaleOnP95: "300"}[kind]
	if svc.Replicas != nil {
		v.Start.Value = strconv.Itoa(start)
	}
	if sc := svc.Scale; sc != nil {
		v.Open = true
		v.Min.Value, v.Max.Value = strconv.Itoa(sc.Min), strconv.Itoa(sc.Max)
		kind, cooldown = sc.Target.Kind(), int(sc.Cooldown().Seconds())
		v.Value.Value = strconv.Itoa(map[string]int{app.ScaleOnRequests: sc.Target.RequestsPerCopy, app.ScaleOnCPU: sc.Target.CPU, app.ScaleOnP95: sc.Target.P95}[kind])
	}
	if draft != nil && draft.Service == svc.Name {
		v.Open = true
		v.Min.Value, v.Max.Value, v.Value.Value, v.Start.Value = draft.Min, draft.Max, draft.Value, draft.Start
		kind = draft.Kind
		if n, err := strconv.Atoi(draft.Cooldown); err == nil {
			cooldown = n
		}
		v.Min.Error, v.Max.Error, v.Value.Error, v.Start.Error = draft.MinErr, draft.MaxErr, draft.ValueErr, draft.StartErr
		v.Notice = draft.Notice
		// The first message is the one announced.
		for _, f := range []*formField{&v.Min, &v.Max, &v.Value, &v.Start} {
			if len(f.Error) > 0 {
				f.Alert = true
				break
			}
		}
		v.CooldownError = draft.CooldownErr
	}
	for _, k := range scaleKinds {
		v.Kinds = append(v.Kinds, formOption{Value: k.Kind, Label: k.Label, Selected: k.Kind == kind})
	}
	seconds := slices.Clone(scaleCooldowns)
	if !slices.Contains(seconds, cooldown) && cooldown > 0 {
		seconds = append(seconds, cooldown) // a value set through the API stays choosable, so a save never changes it
		slices.Sort(seconds)
	}
	for _, c := range seconds {
		v.Cooldowns = append(v.Cooldowns, formOption{Value: strconv.Itoa(c), Label: cooldownWords(c), Selected: c == cooldown})
	}
	return v
}

// scaleOf is the scaling a post holds and the starting count (nil: not typed). A problem of a typed
// value is set on the draft's field and ok is false; the model's own limits are its to judge.
func (p *scalePost) scale() (sc *app.Scale, start *int, ok bool) {
	ok = true
	whole := func(text string, min int, what string, errAt *sentence) int {
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < min {
			*errAt, ok = plainS(what), false
			return 0
		}
		return n
	}
	out := &app.Scale{}
	out.Min = whole(p.Min, 1, "Enter a whole number, 1 or more.", &p.MinErr)
	out.Max = whole(p.Max, 1, "Enter a whole number, 1 or more.", &p.MaxErr)
	value := whole(p.Value, 1, "Enter a whole number, 1 or more.", &p.ValueErr)
	switch p.Kind {
	case app.ScaleOnRequests:
		out.Target.RequestsPerCopy = value
	case app.ScaleOnCPU:
		out.Target.CPU = value
	case app.ScaleOnP95:
		out.Target.P95 = value
	default:
		p.ValueErr, ok = plainS("Choose what to keep the copies near."), false
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(p.Cooldown))
	switch {
	case err != nil || seconds < app.MinScaleCooldown || seconds > app.MaxScaleCooldown:
		p.CooldownErr, ok = plainS("Choose a wait from the list."), false
	case seconds != app.DefaultScaleCooldown:
		out.CooldownSeconds = seconds // the default stays unwritten, as the API leaves it
	}
	if strings.TrimSpace(p.Start) != "" {
		n := whole(p.Start, 1, "Enter a whole number, 1 or more.", &p.StartErr)
		start = &n
	}
	return out, start, ok
}

// scaleFieldRE splits a model path about scaling: services[<i>].scale[.<part>[.<kind>]] or .replicas.
var scaleFieldRE = regexp.MustCompile(`^services\[\d+\]\.(?:scale(?:\.(min|max|target|cooldownSeconds)(?:\.\w+)?)?|(replicas))$`)

// scaleProblem puts a model path's message at the input it is about, led by the input's name when the
// message starts mid-sentence ("is 5, less than min"). It reports false for a path that isn't a
// scaling input's.
func (p *scalePost) scaleProblem(path, message string) bool {
	m := scaleFieldRE.FindStringSubmatch(path)
	if m == nil {
		return false
	}
	named := func(label string) sentence {
		if strings.HasPrefix(message, "is ") || strings.HasPrefix(message, "must ") {
			message = label + " " + message
		}
		return plainS(refusalWords(errors.New(message)))
	}
	switch {
	case m[2] != "":
		p.StartErr = named("Starting count")
	case m[1] == "min":
		p.MinErr = named("Min")
	case m[1] == "max":
		p.MaxErr = named("Max")
	case m[1] == "target":
		p.ValueErr = named("Target")
	case m[1] == "cooldownSeconds":
		p.CooldownErr = named("Cooldown")
	default:
		p.Notice = named("")
	}
	return true
}

// withScale is the app with service i's scale and replicas replaced; a is not changed.
func withScale(a app.App, i int, sc *app.Scale, replicas *int) app.App {
	a.Services = slices.Clone(a.Services)
	a.Services[i].Scale, a.Services[i].Replicas = sc, replicas
	return a
}

// copiesNow is the count the service has now: a scaled service's stored count, else its replicas.
func copiesNow(sum service.AppSummary, svc app.Service) int {
	for _, ss := range sum.Status.Services {
		if ss.Name == svc.Name && ss.Wanted > 0 {
			return ss.Wanted
		}
	}
	return svc.CountFor(nil)
}

// saveScale answers POST /apps/{id}/services/{service}/scale: the form's rule replaces the service's
// scale, as a deploy on the revision the page was drawn from. With action=stop it removes the rule and
// keeps the count the service has now as its copies. Success goes to the deployment's entry. A refusal
// draws the Configure tab again with what was typed and the message at its input.
func (s *server) saveScale(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id, name := r.PathValue("id"), r.PathValue("service")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "save it")
	if !ok {
		return
	}
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	i, found := serviceNamed(sum.App, name)
	if !found {
		s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Notice: sentence{plain("This app has no service "), mono(name), plain(".")}})
		return
	}
	if sum.Compose != nil {
		s.configurePage(w, r, http.StatusConflict, id, configureExtras{Notice: sentence{plain("This app keeps its Compose file, so its scaling is set in the file's overlay. Change it in the file above and deploy it.")}})
		return
	}

	var edited app.App
	typed := func(field string) string { return r.PostForm.Get(inputName(name, "scale-"+field)) }
	post := scalePost{Service: name, Min: typed("min"), Max: typed("max"), Kind: typed("kind"),
		Value: typed("value"), Cooldown: typed("cooldown"), Start: typed("start")}
	if r.PostForm.Get("action") == "stop" {
		if sum.App.Services[i].Scale == nil {
			s.configurePage(w, r, http.StatusConflict, id, configureExtras{Notice: sentence{mono(name), plain(" doesn't scale on its own.")}})
			return
		}
		n := copiesNow(sum, sum.App.Services[i])
		edited = withScale(sum.App, i, nil, &n)
	} else {
		sc, start, ok := post.scale()
		if !ok {
			s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Scale: &post})
			return
		}
		edited = withScale(sum.App, i, sc, start)
	}
	deployment, err := s.svc.DeployIf(ctx, edited, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}

	status := http.StatusUnprocessableEntity
	var fieldErr *app.FieldError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case errors.Is(err, service.ErrAppChanged):
		status, post.Notice = http.StatusConflict, plainS(changedSincePage)
	case errors.As(err, &fieldErr) && post.scaleProblem(fieldErr.Field, fieldErr.Message):
	default:
		var notice sentence
		if status, notice, ok = refusalOf(edited, err); !ok {
			s.serverError(w, r, "save scaling", err)
			return
		}
		post.Notice = notice
	}
	if r.PostForm.Get("action") == "stop" {
		if len(post.Notice) == 0 {
			post.Notice = plainS("Couldn't stop scaling.")
		}
		s.configurePage(w, r, status, id, configureExtras{Notice: post.Notice})
		return
	}
	s.configurePage(w, r, status, id, configureExtras{Scale: &post})
}
