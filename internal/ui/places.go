package ui

import (
	"context"
	"net/url"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// This file is what the pages know of projects and environments (feature 019):
// how a place is written, which places exist, and the place select of New app.

// places is the installation's projects with their environments, read once per
// request. The zero value knows no project: every app is then "at home".
type places struct {
	Projects []service.Project
}

// loadPlaces reads the projects for one request.
func (s *server) loadPlaces(ctx context.Context) (places, error) {
	projects, err := s.svc.Projects(ctx)
	if err != nil {
		return places{}, err
	}
	return places{Projects: projects}, nil
}

// single says the installation is as it was before projects: one project with
// one environment (Default's production). The pages then show no project or
// environment anywhere.
func (p places) single() bool {
	return len(p.Projects) == 1 && len(p.Projects[0].Environments) == 1
}

// display is the project's display name, or its name when it isn't known.
func (p places) display(project string) string {
	for _, pr := range p.Projects {
		if pr.ID == project {
			return pr.DisplayName
		}
	}
	return project
}

// has says whether the project has the environment.
func (p places) has(project, environment string) bool {
	for _, pr := range p.Projects {
		if pr.ID != project {
			continue
		}
		for _, e := range pr.Environments {
			if e.Name == environment {
				return true
			}
		}
	}
	return false
}

// atHome says the place is Default's production, where an app keeps its bare
// name as its ID. An unknown place (the zero value, which an item about no
// app, or one deleted meanwhile, has) counts as home: it is shown by its ID.
func atHome(pl app.Place) bool {
	return pl.Project == "" || (pl.Project == app.DefaultProject && pl.Environment == app.DefaultEnvironment)
}

// appCrumbs are the top bar's crumbs for a page of an app: its project, then
// its environment, then more (a page that isn't one of the app's tabs adds the
// app and itself). The app's own name is not a crumb when the page is a tab:
// the header's h1 carries it. A project or environment is always named, also
// at home ("Default / production"). The project links to its page; so does the
// environment, which has none of its own. An app that can't be read is a
// "Projects" crumb before more.
func (s *server) appCrumbs(ctx context.Context, id string, more ...crumb) []crumb {
	rec, err := s.svc.GetApp(ctx, id)
	if err != nil {
		return append([]crumb{{Label: "Projects", Href: "/projects"}}, more...)
	}
	project, environment := rec.Place.Project, rec.Place.Environment
	if project == "" {
		project, environment = app.DefaultProject, app.DefaultEnvironment
	}
	label := project
	if pl, err := s.loadPlaces(ctx); err == nil {
		label = pl.display(project)
	}
	href := "/projects/" + url.PathEscape(project)
	return append([]crumb{{Label: label, Href: href}, {Label: environment, Href: href, Container: true}}, more...)
}

// label writes where an app is, as the console says it: the app's ID for an
// app at home, else "<project display> › <environment> › <app name>" with the
// names in mono. suffix, when given, is glued to the app's name inside its mono
// part ("/web" for "<app>/web").
func (p places) label(pl app.Place, id, suffix string) sentence {
	if atHome(pl) {
		return sentence{mono(id + suffix)}
	}
	return sentence{plain(p.display(pl.Project) + " › "), mono(pl.Environment), plain(" › "), mono(pl.Name + suffix)}
}

// takenSentence says an app of that ID exists already, naming it as the form
// that asked for it knew it: the bare name at home, else the name and where.
func takenSentence(id string) sentence {
	pl, err := app.PlaceOf(id)
	if err != nil || atHome(pl) {
		return sentence{plain("An app called "), mono(id), plain(" already exists.")}
	}
	return sentence{plain("An app called "), mono(pl.Name), plain(" already exists in "), mono(pl.Project + "/" + pl.Environment), plain(".")}
}

// ---- the place select ----------------------------------------------------------------

// homeValue is the place select's value for Default's production.
const homeValue = app.DefaultProject + "/" + app.DefaultEnvironment

// placeOption is one environment in the place select.
type placeOption struct {
	Value    string // "<project>/<environment>"
	Label    string // "<project display> › <environment>"
	Selected bool
}

// placeChoice is the place select of both New app tabs. Visible is false while
// only Default's production exists (the select would have one option); the form
// then posts no place and the app goes there.
type placeChoice struct {
	Visible bool
	Value   string // the selected option's value; before drawing, what was asked for
	Options []placeOption
	Error   sentence
}

// choice lists every environment as an option, with the one named by selected
// chosen (Default's production when it is empty or isn't an option).
func (p places) choice(selected string) placeChoice {
	c := placeChoice{Visible: !p.single()}
	for _, pr := range p.Projects {
		for _, e := range pr.Environments {
			c.Options = append(c.Options, placeOption{Value: pr.ID + "/" + e.Name, Label: pr.DisplayName + " › " + e.Name})
		}
	}
	c.Value = homeValue
	for _, o := range c.Options {
		if o.Value == selected {
			c.Value = selected
		}
	}
	for i := range c.Options {
		c.Options[i].Selected = c.Options[i].Value == c.Value
	}
	return c
}

// Query is the part of an address that keeps the choice from one tab of New
// app to the other: "" for Default's production.
func (c placeChoice) Query() string {
	project, environment, ok := strings.Cut(c.Value, "/")
	if !ok || c.Value == homeValue {
		return ""
	}
	return "project=" + url.QueryEscape(project) + "&environment=" + url.QueryEscape(environment)
}

// placeAsked reads the place a New app request asks for: the ?project= and
// ?environment= of the link "New app here", as the select's value ("" for
// neither).
func placeAsked(q url.Values) string {
	project, environment := q.Get("project"), q.Get("environment")
	if project == "" || environment == "" {
		return ""
	}
	return project + "/" + environment
}

// placeFor says which place a posted New app form puts its app in. value is
// the select's posted value. With no select (only Default's production existed
// when the page was drawn) the app goes there; a value that names no
// environment is refused (ok false, with the sentence to show).
func (p places) placeFor(value, name string) (pl app.Place, refusal sentence, ok bool) {
	pl = app.Place{Project: app.DefaultProject, Environment: app.DefaultEnvironment, Name: name}
	if value == "" {
		return pl, nil, true
	}
	project, environment, found := strings.Cut(value, "/")
	if !found || !p.has(project, environment) {
		return pl, sentence{plain("Choose one of the environments in the list.")}, false
	}
	pl.Project, pl.Environment = project, environment
	return pl, nil, true
}
