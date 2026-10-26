package ui

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Options form of a domain's row (feature 023, contracts/domains-screen.md
// "The row's detail"), on /domains and on the app's Domains page, which draw the
// same row. Three posts, all taking the row's app and service:
//
//	POST /domains/{host}/options        Team only, the www redirect and the path, saved with a deploy
//	POST /domains/{host}/remove         the service stops answering on the host, with a deploy
//	POST /domains/{host}/team-password  a new password for a Team-only domain (no deploy)
//
// Every change is service.SetDomainsIf on the revision the page was drawn from,
// so a Compose-file app is edited through its overlay like any other. A
// password is in the answer of the post that made it (200, never cached) and in
// no address or later page. The form's hidden "page" says which page to come
// back to: "app" for the app's own, else /domains.

// domainPost is what the three posts share: the row they are about, read from
// the form and the stored app.
type domainPost struct {
	host, appID, service, revision, page string
	rec                                  store.AppRecord
	// svc and dom are the indexes of the service and of the domain in it.
	svc, dom int
}

// back is where a post that changed nothing comes back to, with the row open.
func (p domainPost) back() string {
	if p.page == "app" {
		return "/apps/" + url.PathEscape(p.appID) + "/domains?open=" + url.QueryEscape(p.host) + "#" + domainAnchor(p.host)
	}
	return domainHref(p.host)
}

// draw draws the page the post came from again, with the row open. notice is
// a refusal's sentence (nil for none); extra carries a draft or a password.
func (s *server) drawDomainPost(w http.ResponseWriter, r *http.Request, p domainPost, status int, notice sentence, extra domainsQuery) {
	extra.Open = p.host
	if p.page == "app" {
		form, err := s.domainAddFor(r.Context(), domainAddInput{}, p.appID)
		if err != nil {
			s.serverError(w, "draw the add form", err)
			return
		}
		form.Notice = notice
		s.domainsPageWith(w, r, status, p.appID, form, extra)
		return
	}
	s.renderDomains(w, r, status, extra, notice, nil)
}

// readDomainPost reads the row a post is about. It answers itself, and returns
// false, for a form with no revision, an app that doesn't exist, and a row that
// isn't there (any more).
func (s *server) readDomainPost(w http.ResponseWriter, r *http.Request, what string, needRevision bool) (domainPost, bool) {
	ctx := r.Context()
	p := domainPost{host: r.PathValue("host")}
	if !s.readForm(w, r) {
		return p, false
	}
	p.appID = strings.TrimSpace(r.PostForm.Get("app"))
	p.service = strings.TrimSpace(r.PostForm.Get("service"))
	if r.PostForm.Get("page") == "app" {
		p.page = "app"
	}
	if needRevision {
		var ok bool
		if p.revision, ok = s.formRevision(w, r, what); !ok {
			return p, false
		}
	}
	rec, err := s.svc.GetApp(ctx, p.appID)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, p.appID)
		return p, false
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return p, false
	}
	p.rec = rec
	var found bool
	if p.svc, found = serviceNamed(rec.App, p.service); found {
		p.dom = slices.IndexFunc(rec.App.Services[p.svc].Domains, func(d app.Domain) bool { return d.Host == p.host })
	}
	if !found || p.dom < 0 {
		s.drawDomainPost(w, r, p, http.StatusUnprocessableEntity,
			sentence{plain("This app has no domain "), mono(p.host), plain(" on that service. It may have been removed already.")}, domainsQuery{})
		return p, false
	}
	return p, true
}

// refuseDomainChange says why a change of domains failed, in words and with the
// status the API gives it. A path the deploy refused is drawn under the Path
// field of the row, with what was typed; ok is false for an error these pages
// have no words for.
func (s *server) refuseDomainChange(w http.ResponseWriter, r *http.Request, p domainPost, edited app.App, draft domainDraft, err error) {
	var fieldErr *app.FieldError
	var taken *service.DomainTakenError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, p.appID)
	case errors.Is(err, service.ErrAppStopped):
		s.drawDomainPost(w, r, p, http.StatusConflict, plainS(appStoppedText), domainsQuery{})
	case errors.Is(err, service.ErrAppChanged):
		s.drawDomainPost(w, r, p, http.StatusConflict, plainS(changedSincePage), domainsQuery{})
	case errors.As(err, &taken):
		s.drawDomainPost(w, r, p, http.StatusConflict,
			sentence{mono(taken.Host), plain(" is already a domain of "), mono(taken.App), plain(". Remove it there first.")}, domainsQuery{})
	case errors.As(err, &fieldErr) && strings.Contains(fieldErr.Field, ".domains[") && strings.HasSuffix(fieldErr.Field, ".wwwRedirect"):
		// The redirect's pair is another app's ("www.a.org would redirect a.org, which is a domain of app b").
		s.drawDomainPost(w, r, p, http.StatusUnprocessableEntity, plainS("The redirect can't be saved: "+fieldErr.Message+"."), domainsQuery{})
	case errors.As(err, &fieldErr) && strings.Contains(fieldErr.Field, ".domains[") && strings.HasSuffix(fieldErr.Field, ".path"):
		draft.PathErr = plainS(fieldErr.Message)
		s.drawDomainPost(w, r, p, http.StatusUnprocessableEntity, nil, domainsQuery{Draft: &draft})
	default:
		status, notice, ok := refusalOf(edited, err)
		if !ok {
			s.serverError(w, "change a domain", err)
			return
		}
		s.drawDomainPost(w, r, p, status, notice, domainsQuery{})
	}
}

// saveDomainOptions answers POST /domains/{host}/options: Team only, the www
// redirect and the path of one domain, as a deploy on the page's revision.
// Turning Team only on makes the password, which the answer shows once.
func (s *server) saveDomainOptions(w http.ResponseWriter, r *http.Request, session string) {
	p, ok := s.readDomainPost(w, r, "save it", true)
	if !ok {
		return
	}
	d := p.rec.App.Services[p.svc].Domains[p.dom]
	d.TeamOnly = r.PostForm.Get("teamOnly") != ""
	d.WWWRedirect = r.PostForm.Get("wwwRedirect") != "" && d.RedirectPair() != ""
	d.Path = strings.TrimSpace(r.PostForm.Get("path"))
	domains := slices.Clone(p.rec.App.Services[p.svc].Domains)
	domains[p.dom] = d

	res, err := s.svc.SetDomainsIf(r.Context(), p.appID, p.service, domains, service.Condition{IfRevision: p.revision}, app.TriggerConsole)
	if err != nil {
		edited := p.rec.App
		edited.Services = slices.Clone(edited.Services)
		edited.Services[p.svc].Domains = domains
		s.refuseDomainChange(w, r, p, edited, domainDraft{Host: p.host, Path: d.Path, TeamOnly: d.TeamOnly, WWWRedirect: d.WWWRedirect}, err)
		return
	}
	if pw := res.TeamPasswords[p.host]; pw != "" {
		s.drawDomainPost(w, r, p, http.StatusOK, nil, domainsQuery{Once: &teamPasswordPanel{Host: p.host, Password: pw}})
		return
	}
	http.Redirect(w, r, p.back(), http.StatusSeeOther)
}

// removeDomain answers POST /domains/{host}/remove: the service stops
// answering on the host, as a deploy on the page's revision.
func (s *server) removeDomain(w http.ResponseWriter, r *http.Request, session string) {
	p, ok := s.readDomainPost(w, r, "remove the domain", true)
	if !ok {
		return
	}
	domains := slices.Clone(p.rec.App.Services[p.svc].Domains)
	domains = slices.Delete(domains, p.dom, p.dom+1)
	res, err := s.svc.SetDomainsIf(r.Context(), p.appID, p.service, domains, service.Condition{IfRevision: p.revision}, app.TriggerConsole)
	if err != nil {
		edited := p.rec.App
		edited.Services = slices.Clone(edited.Services)
		edited.Services[p.svc].Domains = domains
		s.refuseDomainChange(w, r, p, edited, domainDraft{}, err)
		return
	}
	http.Redirect(w, r, "/apps/"+url.PathEscape(p.appID)+"/deployments#deployment-"+res.Deployment.ID, http.StatusSeeOther)
}

// rotateTeamPassword answers POST /domains/{host}/team-password: a Team-only
// domain gets a new password, shown once in this answer. The old one stops
// working at once; nothing is deployed.
func (s *server) rotateTeamPassword(w http.ResponseWriter, r *http.Request, session string) {
	p, ok := s.readDomainPost(w, r, "make a new password", false)
	if !ok {
		return
	}
	pw, err := s.svc.RotateTeamPassword(r.Context(), p.appID, p.host)
	switch {
	case err == nil:
		s.drawDomainPost(w, r, p, http.StatusOK, nil, domainsQuery{Once: &teamPasswordPanel{Host: p.host, Password: pw, New: true}})
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, p.appID)
	case errors.Is(err, service.ErrAppStopped):
		s.drawDomainPost(w, r, p, http.StatusConflict, plainS(appStoppedText), domainsQuery{})
	case errors.Is(err, service.ErrNotTeamOnly):
		s.drawDomainPost(w, r, p, http.StatusConflict, sentence{mono(p.host), plain(" isn't Team only, so it has no password.")}, domainsQuery{})
	case errors.Is(err, service.ErrDomainNotFound):
		s.drawDomainPost(w, r, p, http.StatusUnprocessableEntity, sentence{plain("This app has no domain "), mono(p.host), plain(".")}, domainsQuery{})
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't write the Team-only passwords", "error", err)
		s.drawDomainPost(w, r, p, http.StatusBadGateway, plainS("Nomad couldn't keep the new password, so the old one still works."), domainsQuery{})
	default:
		s.serverError(w, "make a new Team-only password", err)
	}
}
