package ui

import (
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// New app › Build from Git (feature 050, plan D10): the app's one service is built from a repository
// on the host and pushed to the image repository the form names. Both buttons are plain posts to
// /apps/new/git. "Build and deploy" is for a repository anyone can read: a deploy key or a token has
// to be made for the service first, and it can't be before the app exists, so with either the app is
// created ("Create, then add access") and its page's Next steps make the key or take the token.

// accessNeedsAppFirst is the refusal of "Build and deploy" with a deploy key or a token.
const accessNeedsAppFirst = "A deploy key or a token can only be made once the app exists. Choose Create, then add access: the next page makes it."

// createFromGit answers POST /apps/new/git.
func (s *server) createFromGit(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	val := func(name string) string { return strings.TrimSpace(r.PostForm.Get(name)) }
	form := newForm()
	form.Git = true
	form.Name.Value, form.Image.Value = val("name"), val("image")
	form.Port.Value, form.Domain.Value, form.Health.Value = val("port"), val("domain"), val("healthPath")
	form.Repository.Value, form.Branch.Value, form.Folder.Value, form.Dockerfile.Value = val("repository"), val("branch"), val("folder"), val("dockerfile")
	form.Access.Value = val("access")
	if form.Access.Value == "" {
		form.Access.Value = app.BuildAccessPublic
	}
	form.Place.Value = val("place")
	deployNow := r.PostForm.Get("action") != actionCreate

	port, ok := form.checkNamePort()
	if form.Repository.Value == "" {
		form.Repository.Error, ok = sentence{plain("Enter the repository's address, like "), mono("https://github.com/you/app.git"), plain(".")}, false
	}
	switch {
	case form.Image.Value == "":
		form.Image.Error, ok = sentence{plain("Enter the image the build pushes to, with no tag, like "), mono("ghcr.io/you/app"), plain(".")}, false
	case imageRepository(form.Image.Value) != form.Image.Value:
		form.Image.Error, ok = sentence{plain("Leave the tag out: each build pushes "), mono(form.Image.Value + ":git-<commit>"), plain(" itself.")}, false
	}
	switch form.Access.Value {
	case app.BuildAccessPublic:
	case app.BuildAccessDeployKey, app.BuildAccessToken:
		if deployNow {
			form.Access.Error, ok = sentence{plain(accessNeedsAppFirst)}, false
		}
	default:
		form.Access.Error, ok = sentence{plain("Choose Public, Deploy key or Token.")}, false
	}

	pl, err := s.loadPlaces(r.Context())
	if err != nil {
		s.serverError(w, r, "list projects", err)
		return
	}
	place, refusal, placed := pl.placeFor(form.Place.Value, form.Name.Value)
	if !placed {
		form.Place.Error, ok = refusal, false
	}
	if !ok {
		form.markFirstError()
		s.renderNewApp(w, r, http.StatusUnprocessableEntity, form)
		return
	}

	svc := app.Service{Name: form.Name.Value, Image: form.Image.Value,
		Build: &app.Build{Repository: form.Repository.Value, Branch: form.Branch.Value, Path: form.Folder.Value,
			Dockerfile: form.Dockerfile.Value, Access: form.Access.Value}}
	form.addPort(&svc, port)
	a := app.App{ID: place.ID(), Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{svc}}

	if deployNow {
		_, err = s.svc.CreateApp(r.Context(), a, app.TriggerConsole)
	} else {
		_, err = s.svc.CreateAppOnly(r.Context(), a)
	}
	if err == nil {
		to := "/apps/" + a.ID + "/deployments"
		if !deployNow {
			to = "/apps/" + a.ID + "?created=1#next-steps"
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	status, known := refuseNewApp(&form, a.ID, err)
	if !known {
		s.serverError(w, r, "create app from a Git repository", err)
		return
	}
	form.markFirstError()
	s.renderNewApp(w, r, status, form)
}
