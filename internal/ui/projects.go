package ui

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// This file is the console's projects (feature 019): New project, a project's
// page with its environments, and what changes them. Every form is a plain post
// that answers with a redirect, or with the page again and the reason.

// newProjectForm is the New project form with what was typed.
type newProjectForm struct {
	Name, DisplayName formField
	Notice            sentence
}

func newProjectFormOf(name, displayName string) newProjectForm {
	return newProjectForm{Name: formField{ID: "name", Value: name}, DisplayName: formField{ID: "displayName", Value: displayName}}
}

// projectApp is an app in a list of a project's page.
type projectApp struct {
	Name string
	Href string
}

// envView is one environment on a project's page. Delete is offered unless the
// environment is Default's production; Reason, when set, says why the button is
// off (the apps are the ones to move or delete first).
type envView struct {
	Name   string
	Apps   []projectApp
	Action string // where its Delete posts
	Offer  bool   // the page has a Delete button for it
	Reason string // why the button is disabled; "" when it works
	Note   string // for the environment that can't be deleted at all
}

// projectDelete is the Delete project section.
type projectDelete struct {
	Offered bool
	Open    bool // the confirmation opens by itself after a refusal
	Action  string
	ID      string
	Error   sentence // under the name field
	Notice  sentence // a refusal that isn't about the field
	Apps    []projectApp
}

// projectView is a project's page.
type projectView struct {
	ID, Display string
	Notice      sentence // a calm fact: created, saved
	Alert       sentence // a refusal of an environment's delete
	Name        formField
	Envs        []envView
	AddEnv      formField
	Delete      projectDelete
}

// projectsPage is the New project page, or a project's page, or the sentence
// that there is no such project (only one is set).
type projectsPage struct {
	pageData
	New     *newProjectForm
	Project *projectView
	Gone    sentence
}

// labelName matches a project or environment name; only that is echoed from a
// query.
var labelName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// reservedProject is a name the console can't give a page: /projects/new is the
// form.
const reservedProject = "new"

// ---- New project --------------------------------------------------------------------

// showNewProject answers GET /projects/new.
func (s *server) showNewProject(w http.ResponseWriter, r *http.Request, session string) {
	s.renderNewProject(w, r, http.StatusOK, newProjectFormOf("", ""))
}

func (s *server) renderNewProject(w http.ResponseWriter, r *http.Request, status int, form newProjectForm) {
	head, err := s.shellPage(r, "New project", "apps", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: "New project"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, status, "projects", projectsPage{pageData: head, New: &form})
}

// createProject answers POST /projects: a project with its production
// environment, then its page. A refusal draws the form again, with the reason
// under the field it is about.
func (s *server) createProject(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	form := newProjectFormOf(strings.TrimSpace(r.PostForm.Get("name")), strings.TrimSpace(r.PostForm.Get("displayName")))
	refuse := func(status int, field *formField, text sentence) {
		field.Error, field.Alert = text, true
		s.renderNewProject(w, r, status, form)
	}
	switch form.Name.Value {
	case "":
		refuse(http.StatusUnprocessableEntity, &form.Name, sentence{plain("Enter a name for the project.")})
		return
	case reservedProject:
		refuse(http.StatusUnprocessableEntity, &form.Name, sentence{plain("Choose another name: the console uses "), mono("/projects/new"), plain(" for this form.")})
		return
	}

	p, err := s.svc.CreateProject(r.Context(), form.Name.Value, form.DisplayName.Value)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/projects/"+url.PathEscape(p.ID)+"?created=1", http.StatusSeeOther)
	case errors.Is(err, service.ErrProjectExists):
		refuse(http.StatusConflict, &form.Name, sentence{plain("A project called "), mono(form.Name.Value), plain(" already exists.")})
	case errors.As(err, &fieldErr) && fieldErr.Field == "displayName":
		refuse(http.StatusUnprocessableEntity, &form.DisplayName, sentence{plain("The display name " + fieldErr.Message + ".")})
	case errors.As(err, &fieldErr):
		refuse(http.StatusUnprocessableEntity, &form.Name, sentence{plain("The name " + strings.TrimPrefix(fieldErr.Message, `"`+form.Name.Value+`" `) + ".")})
	default:
		s.serverError(w, "create project", err)
	}
}

// ---- A project's page ----------------------------------------------------------------

// projectExtras is what a response adds to the stored facts on a project's page:
// what was typed in a refused form, and the sentences about what happened.
type projectExtras struct {
	Notice, Alert sentence
	Name          *formField // the display name form, as typed
	AddEnv        *formField
	Delete        *projectDelete // the Delete project section, with its refusal
}

// projectPage draws a project's page with the given status. An unknown project
// is the not-found sentence inside the shell (404).
func (s *server) projectPage(w http.ResponseWriter, r *http.Request, status int, id string, extra projectExtras) {
	ctx := r.Context()
	p, err := s.svc.Project(ctx, id)
	if errors.Is(err, service.ErrProjectNotFound) {
		s.projectGone(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read project", err)
		return
	}
	sums, err := s.svc.ListAppsIn(ctx, id, "")
	if err != nil {
		s.serverError(w, "list apps of project", err)
		return
	}
	byEnv := map[string][]projectApp{}
	for _, sum := range sums {
		link := projectApp{Name: cmpName(sum), Href: "/apps/" + url.PathEscape(sum.App.ID)}
		byEnv[sum.Place.Environment] = append(byEnv[sum.Place.Environment], link)
	}

	base := "/projects/" + url.PathEscape(id)
	v := &projectView{ID: id, Display: p.DisplayName, Notice: extra.Notice, Alert: extra.Alert,
		Name:   formField{ID: "displayName", Value: p.DisplayName},
		AddEnv: formField{ID: "environment"},
		Delete: projectDelete{Offered: id != app.DefaultProject, Action: base + "/delete", ID: id}}
	if extra.Name != nil {
		v.Name = *extra.Name
	}
	if extra.AddEnv != nil {
		v.AddEnv = *extra.AddEnv
	}
	if extra.Delete != nil {
		v.Delete.Open, v.Delete.Error, v.Delete.Notice, v.Delete.Apps = extra.Delete.Open, extra.Delete.Error, extra.Delete.Notice, extra.Delete.Apps
	}
	for _, e := range p.Environments {
		ev := envView{Name: e.Name, Apps: byEnv[e.Name], Action: base + "/environments/" + url.PathEscape(e.Name) + "/delete"}
		switch {
		case id == app.DefaultProject && e.Name == app.DefaultEnvironment:
			ev.Note = "Apps that existed before projects live here, so it can't be deleted."
		case len(ev.Apps) > 0:
			ev.Offer, ev.Reason = true, "Move or delete its "+appCount(len(ev.Apps))+" first."
		default:
			ev.Offer = true
		}
		v.Envs = append(v.Envs, ev)
	}

	head, err := s.shellPage(r, p.DisplayName, "apps", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: p.DisplayName})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, status, "projects", projectsPage{pageData: head, Project: v})
}

// cmpName is the name an app goes by in its environment.
func cmpName(sum service.AppSummary) string {
	if sum.Place.Name != "" {
		return sum.Place.Name
	}
	return sum.App.ID
}

// projectGone is the shell with "There's no project called x." (404).
func (s *server) projectGone(w http.ResponseWriter, r *http.Request, id string) {
	head, err := s.shellPage(r, id, "apps", crumb{Label: "Apps", Href: "/apps"}, crumb{Label: id, Mono: true})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, http.StatusNotFound, "projects", projectsPage{pageData: head,
		Gone: sentence{plain("There's no project called "), mono(id), plain(".")}})
}

// showProject answers GET /projects/{project}. ?created=1 and ?saved=1 are the
// calm sentences after the posts that redirect here.
func (s *server) showProject(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("project")
	var extra projectExtras
	switch {
	case r.URL.Query().Get("created") == "1":
		if p, err := s.svc.Project(r.Context(), id); err == nil {
			extra.Notice = sentence{plain("Project "), mono(p.DisplayName), plain(" created with a production environment.")}
		}
	case r.URL.Query().Get("saved") == "1":
		extra.Notice = sentence{plain("Display name saved.")}
	}
	s.projectPage(w, r, http.StatusOK, id, extra)
}

// updateProject answers POST /projects/{project}: the display name. The name
// itself never changes, since it is part of each app's ID.
func (s *server) updateProject(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("project")
	if !s.readForm(w, r) {
		return
	}
	typed := strings.TrimSpace(r.PostForm.Get("displayName"))
	_, err := s.svc.SetProjectDisplayName(r.Context(), id, typed)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/projects/"+url.PathEscape(id)+"?saved=1", http.StatusSeeOther)
	case errors.Is(err, service.ErrProjectNotFound):
		s.projectGone(w, r, id)
	case errors.As(err, &fieldErr):
		f := formField{ID: "displayName", Value: typed, Alert: true, Error: sentence{plain("The display name " + fieldErr.Message + ".")}}
		s.projectPage(w, r, http.StatusUnprocessableEntity, id, projectExtras{Name: &f})
	default:
		s.serverError(w, "set project display name", err)
	}
}

// addEnvironment answers POST /projects/{project}/environments.
func (s *server) addEnvironment(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("project")
	if !s.readForm(w, r) {
		return
	}
	typed := strings.TrimSpace(r.PostForm.Get("environment"))
	refuse := func(status int, text sentence) {
		f := formField{ID: "environment", Value: typed, Alert: true, Error: text}
		s.projectPage(w, r, status, id, projectExtras{AddEnv: &f})
	}
	if typed == "" {
		refuse(http.StatusUnprocessableEntity, sentence{plain("Enter a name for the environment.")})
		return
	}
	_, err := s.svc.CreateEnvironment(r.Context(), id, typed)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/projects/"+url.PathEscape(id), http.StatusSeeOther)
	case errors.Is(err, service.ErrProjectNotFound):
		s.projectGone(w, r, id)
	case errors.Is(err, service.ErrEnvironmentExists):
		refuse(http.StatusConflict, sentence{plain("This project already has an environment called "), mono(typed), plain(".")})
	case errors.As(err, &fieldErr):
		refuse(http.StatusUnprocessableEntity, sentence{plain("The name " + strings.TrimPrefix(fieldErr.Message, `"`+typed+`" `) + ".")})
	default:
		s.serverError(w, "add environment", err)
	}
}

// deleteEnvironment answers POST /projects/{project}/environments/{environment}/delete.
// An environment that is already gone is what was asked for.
func (s *server) deleteEnvironment(w http.ResponseWriter, r *http.Request, session string) {
	id, name := r.PathValue("project"), r.PathValue("environment")
	err := s.svc.DeleteEnvironment(r.Context(), id, name)
	switch {
	case err == nil, errors.Is(err, service.ErrEnvironmentNotFound):
		http.Redirect(w, r, "/projects/"+url.PathEscape(id), http.StatusSeeOther)
	case errors.Is(err, service.ErrProjectNotFound):
		s.projectGone(w, r, id)
	case errors.Is(err, service.ErrNotEmpty):
		s.projectPage(w, r, http.StatusConflict, id, projectExtras{Alert: refusedWithApps(sentence{plain("Can't delete "), mono(name), plain(": ")}, err)})
	case errors.Is(err, service.ErrProtected):
		s.projectPage(w, r, http.StatusConflict, id, projectExtras{Alert: sentence{plain("Can't delete "), mono(name),
			plain(": apps that existed before projects live there, and their IDs depend on it.")}})
	default:
		s.serverError(w, "delete environment", err)
	}
}

// refusedWithApps is lead followed by the apps a *service.NotEmptyError names.
func refusedWithApps(lead sentence, err error) sentence {
	out := join(lead, plainS("it still has "))
	var ne *service.NotEmptyError
	if !errors.As(err, &ne) {
		return join(out, plainS("apps."))
	}
	out = join(out, plainS(appCount(len(ne.AppIDs))+": "))
	for i, id := range ne.AppIDs {
		if i > 0 {
			out = append(out, plain(", "))
		}
		out = append(out, mono(id))
	}
	return append(out, plain(". Move or delete them first."))
}

// deleteProject answers POST /projects/{project}/delete. The form must carry the
// project's name as "confirm", exactly, as deleting an app does; the script only
// keeps the button off until it matches. Success goes to Apps, which says so.
func (s *server) deleteProject(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("project")
	if !s.readForm(w, r) {
		return
	}
	if r.PostForm.Get("confirm") != id {
		s.projectPage(w, r, http.StatusUnprocessableEntity, id, projectExtras{Delete: &projectDelete{Open: true,
			Error: sentence{plain("Type "), mono(id), plain(" to confirm.")}}})
		return
	}
	err := s.svc.DeleteProject(r.Context(), id)
	switch {
	case err == nil, errors.Is(err, service.ErrProjectNotFound):
		http.Redirect(w, r, "/apps?projectDeleted="+url.QueryEscape(id), http.StatusSeeOther)
	case errors.Is(err, service.ErrNotEmpty):
		var ne *service.NotEmptyError
		del := projectDelete{Open: true, Notice: sentence{plain("This project still has apps, so nothing was deleted. Move or delete them first.")}}
		if errors.As(err, &ne) {
			for _, appID := range ne.AppIDs {
				del.Apps = append(del.Apps, projectApp{Name: appID, Href: "/apps/" + url.PathEscape(appID)})
			}
		}
		s.projectPage(w, r, http.StatusConflict, id, projectExtras{Delete: &del})
	case errors.Is(err, service.ErrProtected):
		s.projectPage(w, r, http.StatusConflict, id, projectExtras{Delete: &projectDelete{Open: true,
			Notice: sentence{plain("This project can't be deleted: apps that existed before projects live in it.")}}})
	default:
		s.serverError(w, "delete project", err)
	}
}
