package ui

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Domains page (features 021 and 023, /apps/{id}/domains): every address
// the app answers on, drawn as /domains draws them (domainspage.go) and
// filtered to the app, and the Add panel with the app fixed (domainsadd.go).
// Adding or removing a domain is a deploy of the stored app with that one
// change, on the page's revision.

// domainsTab is the body of the Domains page.
type domainsTab struct {
	ID       string
	Revision string
	Defined  bool
	Kept     bool // the app keeps its Compose file: the table is read-only
	Stopped  bool
	Region   domainsRegion
	Add      *domainAddView
}

// CanEdit says the page offers Add and Remove.
func (t domainsTab) CanEdit() bool { return t.Defined && !t.Kept && !t.Stopped }

// domainTarget is a service a domain can be sent to.
type domainTarget struct {
	Service string
	Port    int
}

// Label is how the select names it: "web · :80".
func (t domainTarget) Label() string { return t.Service + " · :" + strconv.Itoa(t.Port) }

// dnsRecord is one record of the box under "Point it at caravana": a record the
// owner adds where they manage the domain's DNS. Name is empty until a host
// was typed.
type dnsRecord struct {
	Type  string
	Name  string
	Value string
	// Known says Value is an address; otherwise it is the words for one.
	Known bool
}

// hostIPWords stand where the address would be when the console doesn't know it.
const hostIPWords = "this host's public IP address"

// appDomainsEmpty is the app page's empty state: Add domain when the page can add.
func appDomainsEmpty(canEdit bool) emptyView {
	e := emptyView{Title: "No domains.", Text: "Copies are reachable only inside the app."}
	if canEdit {
		e.ActionLabel, e.ActionHref = "Add domain", "#add"
	}
	return e
}

// domainsPage draws the Domains page with the given status. add is the Add
// panel as posted (nil draws a fresh one).
func (s *server) domainsPage(w http.ResponseWriter, r *http.Request, status int, id string, add *domainAddView) {
	ctx := r.Context()
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	head, err := s.shellPage(r, id+" domains", navProjects, s.appCrumbs(ctx, id)...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id + "?tab=domains"
	a := sum.App
	tab := &domainsTab{ID: a.ID, Revision: sum.Revision, Defined: a.Defined(), Kept: sum.Compose != nil, Stopped: sum.StoppedAt != nil}
	if tab.Defined {
		q := domainsQuery{App: id, Open: strings.TrimSpace(r.URL.Query().Get("open"))}
		if tab.Region, err = s.loadDomains(ctx, q, appDomainsEmpty(tab.CanEdit()), true); err != nil {
			s.serverError(w, "list domains", err)
			return
		}
		if add == nil {
			if add, err = s.domainAddFor(ctx, domainAddInput{}, id); err != nil {
				s.serverError(w, "draw the add form", err)
				return
			}
		}
		tab.Add = add
	}
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(ctx, sum, "configure"), DomainsTab: tab})
}

// showDomains answers GET /apps/{id}/domains.
func (s *server) showDomains(w http.ResponseWriter, r *http.Request, session string) {
	s.domainsPage(w, r, http.StatusOK, r.PathValue("id"), nil)
}

// addDomain answers POST /apps/{id}/domains: the Add panel of the app's page.
// Check now draws the page again with the checks; Add gives the service one
// more domain, as a deploy on the page's revision. The host is checked as the
// definition checks it (a lowercase host name, once in the app, not the
// console's own, not another app's).
func (s *server) addDomain(w http.ResponseWriter, r *http.Request, session string) {
	s.postDomainAdd(w, r, r.PathValue("id"))
}

// removeDomain answers POST /apps/{id}/domains/remove: the service stops
// answering on the host, as a deploy on the page's revision.
func (s *server) removeDomain(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "remove the domain")
	if !ok {
		return
	}
	rec, err := s.svc.GetApp(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	refuse := func(status int, notice sentence) {
		form, err := s.domainAddFor(ctx, domainAddInput{}, id)
		if err != nil {
			s.serverError(w, "draw the add form", err)
			return
		}
		form.Notice = notice
		s.domainsPage(w, r, status, id, form)
	}
	if rec.Compose != nil {
		refuse(http.StatusConflict, sentence{plain(fileEditedSentence + " Change the file in Configure and deploy it.")})
		return
	}
	host := strings.TrimSpace(r.PostForm.Get("host"))
	i, found := serviceNamed(rec.App, strings.TrimSpace(r.PostForm.Get("service")))
	if !found || !slices.ContainsFunc(rec.App.Services[i].Domains, func(d app.Domain) bool { return d.Host == host }) {
		refuse(http.StatusUnprocessableEntity, sentence{plain("This app has no domain "), mono(host), plain(" on that service. It may have been removed already.")})
		return
	}
	edited := rec.App
	edited.Services = slices.Clone(edited.Services)
	edited.Services[i].Domains = slices.DeleteFunc(slices.Clone(edited.Services[i].Domains), func(d app.Domain) bool { return d.Host == host })
	deployment, err := s.svc.DeployIf(ctx, edited, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case errors.Is(err, service.ErrAppChanged):
		refuse(http.StatusConflict, sentence{plain(changedSincePage)})
	default:
		status, notice, ok := refusalOf(edited, err)
		if !ok {
			s.serverError(w, "remove a domain", err)
			return
		}
		refuse(status, notice)
	}
}
