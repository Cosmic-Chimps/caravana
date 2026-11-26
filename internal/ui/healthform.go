package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// saveHealth answers POST /apps/{id}/services/{service}/health (feature 059): the
// health check form of Configure › Services for an app that keeps its Compose
// file, where the check is written into the file's overlay (Service.SetHealthIf).
// It reads the three inputs as the Configure form does, so an empty path is the
// TCP check, and deploys on the revision the page was drawn from. Success goes to
// the deployment's entry; a refusal draws Services again with what was typed and
// the message at its input.
func (s *server) saveHealth(w http.ResponseWriter, r *http.Request, session string) {
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
	form := formOf(rec.App, revision)
	svc, found := form.service(name)
	switch {
	case !found:
		s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Section: sectionServices, Notice: sentence{plain("This app has no service "), mono(name), plain(".")}})
		return
	case !svc.ShowHealth:
		s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Section: sectionServices, Notice: sentence{mono(name), plain(" has no port, so it has no health check.")}})
		return
	}
	if !svc.parse(r.PostForm) {
		form.markFirstError()
		s.drawEdit(w, r, http.StatusUnprocessableEntity, rec.App, form, nil)
		return
	}
	e := svc.Edit
	res, err := s.svc.SetHealthIf(ctx, id, name, e.HealthPath, e.HealthInterval, e.HealthTimeout, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+res.Deployment.ID, http.StatusSeeOther)
		return
	}
	if errors.Is(err, service.ErrAppStopped) {
		s.configurePage(w, r, http.StatusConflict, id, configureExtras{Section: sectionServices, Notice: sentence{plain("The app is stopped: start it first.")}})
		return
	}

	// Whatever happened, draw the form from the app as it is stored now: a deploy that failed after
	// saving, or a stale save, both left a newer one.
	fresh, getErr := s.svc.GetApp(ctx, id)
	switch {
	case errors.Is(getErr, store.ErrNotFound):
		s.appNotFound(w, r, id)
		return
	case getErr != nil:
		s.serverError(w, r, "read app", getErr)
		return
	}
	form = formOf(fresh.App, fresh.Revision)
	if fs, found := form.service(name); found {
		fs.parse(r.PostForm)
	}
	if errors.Is(err, service.ErrAppChanged) {
		s.drawEdit(w, r, http.StatusConflict, fresh.App, form, staleOf(fresh.App, form))
		return
	}
	var fieldErr *app.FieldError
	if errors.As(err, &fieldErr) {
		if input := healthInput(fieldErr.Field, name, fresh.App); input != "" {
			if f, ok := form.field(inputName(name, input)); ok {
				f.Error = sentence{plain(fieldMessage(fieldErr.Field, fieldErr.Message))}
				form.markFirstError()
				s.drawEdit(w, r, http.StatusUnprocessableEntity, fresh.App, form, nil)
				return
			}
		}
	}
	status, ok := refuseEdit(&form, rec.App, err)
	if !ok {
		s.serverError(w, r, "save the health check from the console", err)
		return
	}
	form.markFirstError()
	s.drawEdit(w, r, status, fresh.App, form, nil)
}

// healthInput is the Configure input a refused path about service's health check
// belongs to: "services.web.health.interval" (an app that keeps its file, whose
// paths are the file's) or "services[0].health.interval" (a definition's). It is
// empty for any other path.
func healthInput(path, service string, a app.App) string {
	prefixes := []string{"services." + service + "."}
	if i, found := serviceNamed(a, service); found {
		prefixes = append(prefixes, fmt.Sprintf("services[%d].", i))
	}
	for _, prefix := range prefixes {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		switch rest {
		case "health", "health.path":
			return "healthPath"
		case "health.interval":
			return "healthInterval"
		case "health.timeout":
			return "healthTimeout"
		}
	}
	return ""
}
