package ui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Domains page (features 021 and 023, /apps/{id}/domains): every address
// the app answers on, drawn as /domains draws them (domainspage.go) and
// filtered to the app, and the Add panel with the app fixed (domainsadd.go).
// Adding, removing or changing a domain is a deploy of the app with that one
// change, on the page's revision (service.SetDomainsIf; domainsoptions.go).

// domainsTab is the body of the Domains page.
type domainsTab struct {
	ID       string
	Revision string
	Defined  bool
	Stopped  bool
	Region   domainsRegion
	Add      *domainAddView
	// Once is a Team-only password just made, drawn above everything else.
	Once *teamPasswordPanel
}

// CanEdit says the page offers Add and the Options (a kept Compose file is edited
// through its overlay, as every other page edits it).
func (t domainsTab) CanEdit() bool { return t.Defined && !t.Stopped }

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
	s.domainsPageWith(w, r, status, id, add, domainsQuery{})
}

// domainsPageWith is domainsPage with what a post's answer draws besides: the
// row to open (extra.Open, else the address's ?open=), a password shown once
// and a refused Options form in its row.
func (s *server) domainsPageWith(w http.ResponseWriter, r *http.Request, status int, id string, add *domainAddView, extra domainsQuery) {
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
	tab := &domainsTab{ID: a.ID, Revision: sum.Revision, Defined: a.Defined(), Stopped: sum.StoppedAt != nil, Once: extra.Once}
	if tab.Defined {
		q := domainsQuery{App: id, Open: strings.TrimSpace(r.URL.Query().Get("open")), Once: extra.Once, Draft: extra.Draft}
		if extra.Open != "" {
			q.Open = extra.Open
		}
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
