package ui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Domains page (feature 021, /apps/{id}/domains): every address the app
// answers on, and the form that adds one. Adding or removing a domain is a
// deploy of the stored app with that one change, on the page's revision. The
// page draws only what the console knows today: no certificate or DNS state.

// domainsTab is the body of the Domains page.
type domainsTab struct {
	ID       string
	Revision string
	Defined  bool
	Kept     bool // the app keeps its Compose file: the table is read-only
	Stopped  bool
	// Environment is the app's environment, said after each domain's port.
	Environment string
	Rows        []domainTableRow
	// Targets are the services that can take a domain: they have a port a new
	// domain can go to. Many says there is more than one, so the form asks which.
	Targets []domainTarget
	Many    bool
	Add     addDomainForm
	Record  dnsRecord
}

// Count is the line under the head: "3 domains".
func (t domainsTab) Count() string { return plural(len(t.Rows), "domain") }

// CanEdit says the page offers Add and Remove.
func (t domainsTab) CanEdit() bool { return t.Defined && !t.Kept && !t.Stopped }

// domainTableRow is one domain, in the table.
type domainTableRow struct {
	Host, Service string
	Port          int
	URL           string
}

// domainTarget is a service a domain can be sent to.
type domainTarget struct {
	Service string
	Port    int
}

// Label is how the select names it: "web · :80".
func (t domainTarget) Label() string { return t.Service + " · :" + strconv.Itoa(t.Port) }

// addDomainForm is the "Add a domain" form as typed.
type addDomainForm struct {
	Service string
	Host    formField
	Notice  sentence
}

// dnsRecord is the box under "Point it at Caravana": the A record the owner adds
// where they manage the domain's DNS. Name is empty until a host was typed.
type dnsRecord struct {
	Type  string
	Name  string
	Value string
	// Known says Value is an address; otherwise it is the words for one.
	Known bool
}

// hostIPWords stand where the address would be when the console doesn't know it.
const hostIPWords = "this host's public IP address"

// recordName is the Name of the A record for a host: "@" for an apex, the first
// label otherwise. An apex is a host with exactly one dot (example.com). Telling
// the registered domain apart in general (example.co.uk, example.com.br) needs
// the public suffix list, which is a module Caravana doesn't carry, so a host
// like example.co.uk is read as a subdomain "example". An empty host has no name.
func recordName(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	switch {
	case host == "":
		return ""
	case app.IsApex(host):
		return "@"
	}
	label, _, _ := strings.Cut(host, ".")
	return label
}

// hostIP finds the address the console's own host name answers on, which is what a
// domain's A record should point at. The host comes from CARAVANA_PUBLIC_URL; the
// lookup runs the first time a page asks, waits two seconds at most, and a
// success is kept for the life of the process. A failure is kept a minute, so a
// host that doesn't resolve doesn't make every page view wait.
type hostIP struct {
	mu      sync.Mutex
	ip      string
	failed  time.Time
	lookup  func(ctx context.Context, network, host string) ([]net.IP, error)
	timeout time.Duration
	retry   time.Duration
}

// newHostIP is a lookup through the system resolver.
func newHostIP() *hostIP {
	return &hostIP{lookup: net.DefaultResolver.LookupIP, timeout: 2 * time.Second, retry: time.Minute}
}

// find returns the IPv4 address publicURL's host answers on, or "" when there is
// no URL, no such host, or only an address that isn't a public one (a loopback
// or unspecified address is the console running on a laptop).
func (h *hostIP) find(ctx context.Context, publicURL string, now time.Time) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ip != "" {
		return h.ip
	}
	if publicURL == "" || (!h.failed.IsZero() && now.Sub(h.failed) < h.retry) {
		return ""
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	ips, _ := h.lookup(ctx, "ip4", u.Hostname())
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil && !v4.IsLoopback() && !v4.IsUnspecified() {
			h.ip = v4.String()
			return h.ip
		}
	}
	h.failed = now
	return ""
}

// recordFor is the DNS box for a host: the A record with its address, or the
// words for it.
func (s *server) recordFor(ctx context.Context, host string) dnsRecord {
	rec := dnsRecord{Type: "A", Name: recordName(host), Value: hostIPWords}
	if ip := s.hostIP.find(ctx, s.publicURL(), s.now()); ip != "" {
		rec.Value, rec.Known = ip, true
	}
	return rec
}

// domainsTabOf builds the page's body from the app's summary. It is pure but
// for the record, which the caller works out.
func domainsTabOf(sum service.AppSummary, typed *addDomainForm, record dnsRecord) *domainsTab {
	a := sum.App
	tab := &domainsTab{ID: a.ID, Revision: sum.Revision, Defined: a.Defined(), Kept: sum.Compose != nil,
		Stopped: sum.StoppedAt != nil, Environment: sum.Place.Environment, Record: record}
	if tab.Environment == "" {
		tab.Environment = app.DefaultEnvironment
	}
	for _, row := range domainRowsOf(a) {
		tab.Rows = append(tab.Rows, domainTableRow{Host: row.Host, Service: row.Service, Port: row.Port, URL: "https://" + row.Host})
	}
	slices.SortStableFunc(tab.Rows, func(x, y domainTableRow) int { return strings.Compare(x.Host, y.Host) })
	for _, svc := range a.Services {
		name, ok := domainPort(svc)
		if !ok {
			continue
		}
		for _, p := range svc.Ports {
			if p.Name == name {
				tab.Targets = append(tab.Targets, domainTarget{Service: svc.Name, Port: p.ContainerPort})
			}
		}
	}
	tab.Many = len(tab.Targets) > 1
	tab.Add = addDomainForm{Host: formField{ID: "domain-host"}}
	if typed != nil {
		tab.Add = *typed
		tab.Add.Host.ID = "domain-host"
	}
	if tab.Add.Service == "" && len(tab.Targets) > 0 {
		tab.Add.Service = tab.Targets[0].Service
	}
	return tab
}

// domainsPage draws the Domains page with the given status.
func (s *server) domainsPage(w http.ResponseWriter, r *http.Request, status int, id string, typed *addDomainForm) {
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
	head.LiveURL = "/live/apps/" + id
	host := ""
	if typed != nil {
		host = typed.Host.Value
	}
	// The address is looked up only for a page that draws the box.
	record := dnsRecord{Type: "A", Name: recordName(host), Value: hostIPWords}
	if sum.App.Defined() && sum.Compose == nil && sum.StoppedAt == nil {
		record = s.recordFor(ctx, host)
	}
	s.render(w, status, "app", appPage{pageData: head, appHeader: s.header(ctx, sum, "configure"), DomainsTab: domainsTabOf(sum, typed, record)})
}

// showDomains answers GET /apps/{id}/domains.
func (s *server) showDomains(w http.ResponseWriter, r *http.Request, session string) {
	s.domainsPage(w, r, http.StatusOK, r.PathValue("id"), nil)
}

// addDomain answers POST /apps/{id}/domains: the service gets one more domain,
// as a deploy on the page's revision. The host is checked as the definition
// checks it (a lowercase host name, once in the app, not the console's own).
func (s *server) addDomain(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.readForm(w, r) {
		return
	}
	revision, ok := s.formRevision(w, r, "add the domain")
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
	form := addDomainForm{Service: strings.TrimSpace(r.PostForm.Get("service")), Host: formField{ID: "domain-host"}}
	form.Host.Value = strings.TrimSpace(r.PostForm.Get("host"))
	if rec.Compose != nil {
		form.Notice = sentence{plain(fileEditedSentence + " Change the file in Configure and deploy it.")}
		s.domainsPage(w, r, http.StatusConflict, id, &form)
		return
	}
	// Alone, a service needn't be named.
	if form.Service == "" {
		for _, svc := range rec.App.Services {
			if _, can := domainPort(svc); can {
				form.Service = svc.Name
				break
			}
		}
	}
	i, found := serviceNamed(rec.App, form.Service)
	port, can := "", false
	if found {
		port, can = domainPort(rec.App.Services[i])
	}
	switch {
	case !found:
		form.Notice = sentence{plain("This app has no service "), mono(form.Service), plain(".")}
	case !can:
		form.Notice = sentence{plain("The service "), mono(form.Service), plain(" has no port a domain can go to.")}
	case form.Host.Value == "":
		form.Host.Error = sentence{plain("Enter a domain.")}
	}
	if len(form.Notice) > 0 || len(form.Host.Error) > 0 {
		form.Host.Alert = len(form.Host.Error) > 0
		s.domainsPage(w, r, http.StatusUnprocessableEntity, id, &form)
		return
	}

	edited := rec.App
	edited.Services = slices.Clone(edited.Services)
	edited.Services[i].Domains = append(slices.Clone(edited.Services[i].Domains), app.Domain{Host: form.Host.Value, Port: port})
	deployment, err := s.svc.DeployIf(ctx, edited, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+id+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}
	var fieldErr *app.FieldError
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, id)
	case errors.Is(err, service.ErrAppChanged):
		form.Notice = sentence{plain(changedSincePage)}
		s.domainsPage(w, r, http.StatusConflict, id, &form)
	case errors.As(err, &fieldErr) && strings.Contains(fieldErr.Field, ".domains["):
		form.Host.Error, form.Host.Alert = sentence{plain(fieldErr.Message)}, true
		s.domainsPage(w, r, http.StatusUnprocessableEntity, id, &form)
	default:
		status, notice, ok := refusalOf(edited, err)
		if !ok {
			s.serverError(w, "add a domain", err)
			return
		}
		form.Notice = notice
		s.domainsPage(w, r, status, id, &form)
	}
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
		s.domainsPage(w, r, status, id, &addDomainForm{Notice: notice, Host: formField{ID: "domain-host"}})
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
