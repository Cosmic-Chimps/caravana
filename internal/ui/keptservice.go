package ui

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// saveKeptService answers POST /apps/{id}/services/{service}/settings (feature 059): the form of one
// service on Configure › Services for an app that keeps its Compose file. The health check is written
// into the file's overlay and the copies and memory into the file's text, both in one deploy
// (Service.SetServiceIf). It reads the inputs as the Configure form does, so an empty path is the TCP
// check and empty copies is one, and deploys on the revision the page was drawn from. Only what the post
// holds is changed. Success goes to the deployment's entry; a refusal draws Services again with what was
// typed and the message at its input.
func (s *server) saveKeptService(w http.ResponseWriter, r *http.Request, session string) {
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
	if !found {
		s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Section: sectionServices, Notice: sentence{plain("This app has no service "), mono(name), plain(".")}})
		return
	}
	set := settingsOf(svc, r.PostForm)
	if set.Health == nil && set.Size == (service.Size{}) {
		s.configurePage(w, r, http.StatusUnprocessableEntity, id, configureExtras{Section: sectionServices, Notice: sentence{mono(name), plain(" has no port, so it has no health check, and its size isn't in the form.")}})
		return
	}
	if !svc.parse(r.PostForm) {
		form.markFirstError()
		s.drawEdit(w, r, http.StatusUnprocessableEntity, rec.App, form, nil)
		return
	}
	set = settingsOf(svc, r.PostForm) // with the parsed values
	res, err := s.svc.SetServiceIf(ctx, id, name, set, service.Condition{IfRevision: revision}, app.TriggerConsole)
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
	var editErr *compose.EditError
	if errors.As(err, &editErr) {
		if f, ok := form.field(inputName(name, keptServiceInput(editErr.Path, name, fresh.App))); ok {
			f.Error = sentence{plain("The Compose file can't take this here: " + editErr.Reason + ". Change it on Source.")}
		} else {
			form.Notice = sentence{plain("The Compose file can't take this here: "), mono(editErr.Path), plain(" " + editErr.Reason + ". Change it on Source.")}
		}
		form.markFirstError()
		s.drawEdit(w, r, http.StatusUnprocessableEntity, fresh.App, form, nil)
		return
	}
	var fieldErr *app.FieldError
	if errors.As(err, &fieldErr) {
		if input := keptServiceInput(fieldErr.Field, name, fresh.App); input != "" {
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
		s.serverError(w, r, "save the service from the console", err)
		return
	}
	form.markFirstError()
	s.drawEdit(w, r, status, fresh.App, form, nil)
}

// settingsOf is what a post of the service's form changes: the health check when the service has one
// and the post holds its path, and each of copies and memory the post holds (copies only for a service
// that has the input). It reads the service's Edit, which parse has set from the post.
func settingsOf(svc *editService, post url.Values) service.ServiceSettings {
	has := func(field string) bool { return post.Has(inputName(svc.Name, field)) }
	var set service.ServiceSettings
	e := svc.Edit
	if svc.ShowHealth && has("healthPath") {
		set.Health = &service.HealthSettings{Path: e.HealthPath, Interval: e.HealthInterval, Timeout: e.HealthTimeout}
	}
	if !svc.Scaled && !svc.OneCopy && has("copies") {
		set.Size.Replicas = new(1) // empty is one copy
		if e.Replicas != nil {
			set.Size.Replicas = new(*e.Replicas)
		}
	}
	if has("memory") {
		set.Size.MemoryMB = new(e.MemoryMB)
	}
	if has("memoryMax") {
		set.Size.MemoryMaxMB = new(e.MemoryMaxMB)
	}
	return set
}

// keptServiceInput is the Services input a refused path about the service belongs to: a path of the
// file ("services.web.health.interval", "services.web.deploy.replicas", "services.web.mem_limit"), of
// the app it makes ("services.web.resources.memoryMB") or of a definition ("services[0].replicas"). It
// is empty for any other path.
func keptServiceInput(path, service string, a app.App) string {
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
		case "replicas", "deploy.replicas", "scale":
			return "copies"
		case "resources.memoryMB", "mem_reservation", "deploy.resources.reservations.memory":
			return "memory"
		case "resources.memoryMaxMB", "mem_limit", "deploy.resources.limits.memory":
			return "memoryMax"
		}
	}
	return ""
}
