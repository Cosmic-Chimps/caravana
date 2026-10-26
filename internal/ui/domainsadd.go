package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Add panel (feature 023, contracts/domains-screen.md "Add a domain"), on
// /domains and on the app's Domains page, where the app is fixed. It is one
// post form with two submits: "Check now" (action=check) runs a fresh check of
// the typed host (service.CheckDomain) and draws the form again with the DNS
// and certificate lines; "Add domain and deploy" (action=add, the default)
// deploys the app with one more domain on the revision the page was drawn
// from. The service select of /domains is drawn once an app is picked: a pick
// redraws the field (with script), and so does Check now (without).

// domainAddInput is what the Add form posted (or the address asked for).
type domainAddInput struct {
	Host, App, Service, Path string
}

// domainAddView is the Add panel as the page draws it.
type domainAddView struct {
	Action string // where it posts
	// FixedApp is the app the form adds to on the app's own page: it is drawn
	// as text (Fixed), not a select, and Revision is its revision.
	FixedApp string
	Fixed    sentence
	Revision string
	// Revisions are, on /domains, every listed app's revision, posted as
	// revision-<id> so the add is checked against the app as the page drew it.
	Revisions []appRevision
	Host      formField
	Path      formField
	App       formField
	Apps      []formOption
	Service   formField
	// Services is the service select: the chosen app's services when it has
	// several (ServiceShown). It is drawn only once an app is picked (a pick
	// redraws it, through /parts/domains/add-service, and so does Check now);
	// until then FirstService says the app's first service is the default.
	// HiddenService is the one service posted when there is no select.
	Services      []formOption
	ServiceShown  bool
	FirstService  bool
	HiddenService string
	// Port is the chosen service's port, ":8080", drawn as text after it.
	Port    string
	Records recordBox
	// Checked says Check now ran: DNS and Cert hold its results.
	Checked bool
	DNS     checkLine
	Cert    checkLine
	Notice  sentence
	// Primary says Add is the primary button: the check found DNS pointing
	// here, or none ran.
	Primary    bool
	CancelHref string
	// NoTargets says no app has a service a domain can go to.
	NoTargets bool
}

// appRevision is one hidden revision-<id> input of /domains' Add form.
type appRevision struct {
	App, Revision string
}

// checkLine is one line of the Add panel's Check: an 8px mark, the words, and
// a reason under them.
type checkLine struct {
	Mark   string
	Words  string
	Reason sentence
}

// Spoken is the line's mark in words, for a screen reader.
func (c checkLine) Spoken() string {
	switch c.Mark {
	case "healthy":
		return "passed"
	case "failed":
		return "failed"
	case "warning":
		return "warning"
	case "rolling":
		return "in progress"
	}
	return "not yet"
}

// addTargetsOf are the services of an app a domain can go to; one whose port
// isn't HTTP (notHTTPPort) is among them, marked, so the select can list it
// disabled.
func addTargetsOf(a app.App) []domainTarget {
	var out []domainTarget
	for _, svc := range a.Services {
		name, ok := domainPort(svc)
		if !ok {
			continue
		}
		for _, p := range svc.Ports {
			if p.Name == name {
				out = append(out, domainTarget{Service: svc.Name, Port: p.ContainerPort, NotHTTP: notHTTPPort(svc, p.ContainerPort)})
			}
		}
	}
	return out
}

// firstHTTPTarget is the index of the first target with an HTTP port, else 0:
// the service a domain goes to when none is named.
func firstHTTPTarget(targets []domainTarget) int {
	if i := slices.IndexFunc(targets, func(t domainTarget) bool { return !t.NotHTTP }); i >= 0 {
		return i
	}
	return 0
}

// hasHTTPTarget says one of targets has an HTTP port.
func hasHTTPTarget(targets []domainTarget) bool {
	return slices.ContainsFunc(targets, func(t domainTarget) bool { return !t.NotHTTP })
}

// domainAddFor builds the Add panel for in. fixedApp is the app of the page
// ("" on /domains). Nothing is checked here.
func (s *server) domainAddFor(ctx context.Context, in domainAddInput, fixedApp string) (*domainAddView, error) {
	sums, err := s.svc.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		sum     service.AppSummary
		targets []domainTarget
	}
	var cands []candidate
	for _, sum := range sums {
		if fixedApp != "" && sum.App.ID != fixedApp {
			continue
		}
		if t := addTargetsOf(sum.App); sum.App.Defined() && len(t) > 0 {
			cands = append(cands, candidate{sum: sum, targets: t})
		}
	}

	// Cancel goes back to the page, not to the panel it is in.
	v := &domainAddView{Action: "/domains", CancelHref: "/domains",
		Host:    formField{ID: "domain-host", Value: in.Host},
		Path:    formField{ID: "domain-path", Value: in.Path},
		App:     formField{ID: "domain-app", Value: in.App},
		Service: formField{ID: "domain-service", Value: in.Service},
		DNS:     checkLine{Mark: "idle", Words: "DNS points at caravana"},
		Cert:    checkLine{Mark: "idle", Words: "HTTPS certificate"},
		Primary: true}
	v.Records = recordBoxFor(in.Host, s.hostAddressTexts(ctx))
	if fixedApp != "" {
		v.Action, v.CancelHref, v.FixedApp = "/apps/"+url.PathEscape(fixedApp)+"/domains", "/apps/"+url.PathEscape(fixedApp)+"/domains", fixedApp
	}
	if len(cands) == 0 {
		v.NoTargets = true
		return v, nil
	}

	// One app to pick from: it is the app.
	appID := in.App
	if fixedApp != "" {
		appID = fixedApp
	}
	if appID == "" && len(cands) == 1 {
		appID = cands[0].sum.App.ID
	}
	chosen := slices.IndexFunc(cands, func(c candidate) bool { return c.sum.App.ID == appID })
	v.App.Value = appID

	if fixedApp != "" {
		if chosen < 0 {
			v.NoTargets = true
			return v, nil
		}
		v.Revision = cands[chosen].sum.Revision
		v.Fixed = sentence{mono(pl.appLabel(cands[chosen].sum.AppRecord))}
	} else {
		if chosen < 0 {
			v.Apps = append(v.Apps, formOption{Value: "", Label: "Choose an app", Selected: true})
		}
		for i, c := range cands {
			label := pl.appLabel(c.sum.AppRecord)
			disabled := true
			switch {
			case c.sum.StoppedAt != nil:
				label += " (stopped)"
			case !hasHTTPTarget(c.targets):
				label += " (no HTTP port)"
			default:
				disabled = false
			}
			v.Apps = append(v.Apps, formOption{Value: c.sum.App.ID, Label: label, Selected: i == chosen, Disabled: disabled})
			v.Revisions = append(v.Revisions, appRevision{App: c.sum.App.ID, Revision: c.sum.Revision})
		}
	}

	if chosen < 0 {
		// No app picked yet: no service select, which a pick or Check now draws. The app's first
		// service is the default, said only when some app has more than one.
		v.FirstService = slices.ContainsFunc(cands, func(c candidate) bool { return len(c.targets) > 1 })
		return v, nil
	}
	targets := cands[chosen].targets
	svcName := in.Service
	if _, after, ok := strings.Cut(svcName, pairSep); ok {
		svcName = after
	}
	at := slices.IndexFunc(targets, func(t domainTarget) bool { return t.Service == svcName && !t.NotHTTP })
	if at < 0 {
		at = firstHTTPTarget(targets)
	}
	if len(targets) > 1 {
		v.ServiceShown = true
		for i, t := range targets {
			v.Services = append(v.Services, formOption{Value: t.Service, Label: t.Label(), Selected: i == at, Disabled: t.NotHTTP})
		}
	} else {
		v.HiddenService = targets[0].Service
		v.Port = ":" + strconv.Itoa(targets[0].Port)
	}
	return v, nil
}

// partDomainAddService answers GET /parts/domains/add-service?app=: the Add
// panel's service field for the app just picked in /domains' app select (its
// select when it has several services, else its port as text), which htmx
// puts in place of the field. Without script, Check now draws the same.
func (s *server) partDomainAddService(w http.ResponseWriter, r *http.Request, session string) {
	v, err := s.domainAddFor(r.Context(), domainAddInput{App: strings.TrimSpace(r.URL.Query().Get("app"))}, "")
	if err != nil {
		s.log.Error("draw the add form", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "domains", "domain-add-service", v)
}

// applyCheck puts a fresh check's results in the panel: the records from it,
// the DNS line and the certificate line.
func (v *domainAddView) applyCheck(chk service.DomainCheck) {
	v.Checked = true
	var addrs []string
	for _, r := range chk.Records {
		addrs = append(addrs, r.Value)
	}
	v.Records = recordBoxFor(chk.Host, addrs)

	// The lines' marks and words are the state table's (domainstates.go): the state the result
	// would give the row in the list.
	v.DNS = checkLine{Mark: "healthy", Words: "DNS points at caravana"}
	behindProxy := chk.DNS.Status == edge.DNSElsewhere && (chk.Certificate.Valid() || chk.DNS.Proxy != "")
	if behindProxy {
		// An address that isn't this host's is not a failure when something stands in front of the
		// host: a trusted certificate (edge.StateOf's rule, for a host already served) or Cloudflare's
		// ranges (a new host has no certificate yet, so it is a warning, not a pass).
		seen := mono(addressesOf(seenElsewhere(chk.DNS)))
		switch {
		case chk.Certificate.Valid():
			v.DNS.Reason = sentence{plain("points at "), seen, plain(proxyNote(chk.DNS))}
		default:
			v.DNS.Mark = "warning"
			v.DNS.Reason = sentence{plain("points at Cloudflare's proxy ("), seen, plain("). That works when Cloudflare forwards to this host: its origin should be "),
				mono(addressesOf(expectedTexts(chk.DNS))), plain(". The certificate is checked after the deploy.")}
		}
	} else if st, ok := dnsState(chk.DNS.Status); ok {
		info := stateInfo(st)
		v.DNS.Mark, v.DNS.Reason = info.Mark, plainS(info.Words)
		switch st {
		case edge.StateDNSElsewhere:
			v.DNS.Reason = sentence{plain("points at "), mono(addressesOf(seenElsewhere(chk.DNS)))}
		case edge.StateUnknown:
			if len(chk.Records) == 0 {
				v.DNS.Reason = plainS(info.Words + ": this host's public address isn't known")
			} else {
				v.DNS.Reason = plainS(info.Words + ": the resolvers didn't answer")
			}
		}
	}
	v.Primary = chk.DNS.Status == edge.DNSOK || behindProxy

	v.Cert = checkLine{Mark: "idle", Words: "HTTPS certificate"}
	c := chk.Certificate
	if c.Status == edge.CertNone || c.Status == "" {
		v.Cert.Reason = plainS("issued after you add it")
		return
	}
	info := stateInfo(certState(c.Status))
	v.Cert.Mark, v.Cert.Reason = info.Mark, plainS(info.Words)
	switch {
	case c.Status == edge.CertIssued:
		v.Cert.Reason = plainS("issued by " + c.Issuer)
	case info.Cert != "" && info.Cert != "—":
		v.Cert.Reason = plainS(info.Cert) // waiting for Let's Encrypt, Traefik's own
	}
}

// consoleHostOf is the host of the console's own address, lowercase, "" for none.
func consoleHostOf(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// takenBy is the app that already has host, other than appID ("" for none).
func (s *server) takenBy(ctx context.Context, host, appID string) (string, error) {
	v, err := s.svc.Domain(ctx, host)
	switch {
	case errors.Is(err, service.ErrDomainNotFound):
		return "", nil
	case err != nil:
		return "", err
	case v.App == appID:
		return "", nil
	}
	return v.App, nil
}

// showAddRefusal draws the page the Add form is on again with the form as it
// was posted, its errors and notice in it.
func (s *server) showAddRefusal(w http.ResponseWriter, r *http.Request, status int, form *domainAddView) {
	if form.FixedApp != "" {
		s.domainsPage(w, r, status, form.FixedApp, form)
		return
	}
	s.renderDomains(w, r, status, domainsQuery{}, nil, form)
}

// addDomainsPost answers POST /domains: the Add panel of /domains.
func (s *server) addDomainsPost(w http.ResponseWriter, r *http.Request, session string) {
	s.postDomainAdd(w, r, "")
}

// postDomainAdd is the Add panel's post, on /domains (fixedApp "") or on the
// app's own page: Check now, or add the domain with a deploy.
func (s *server) postDomainAdd(w http.ResponseWriter, r *http.Request, fixedApp string) {
	ctx := r.Context()
	if !s.readForm(w, r) {
		return
	}
	in := domainAddInput{Host: strings.TrimSpace(r.PostForm.Get("host")), App: strings.TrimSpace(r.PostForm.Get("app")),
		Service: strings.TrimSpace(r.PostForm.Get("service")), Path: strings.TrimSpace(r.PostForm.Get("path"))}
	if fixedApp != "" {
		in.App = fixedApp
	}
	// "app › service" picked while no app was: the pair names the app.
	pairApp := ""
	if a, svc, ok := strings.Cut(in.Service, pairSep); ok {
		pairApp, in.Service = a, svc
		if in.App == "" {
			in.App = a
		}
	}
	checking := r.PostForm.Get("action") == "check"

	// The app page checks the revision first, as it always did.
	revision := ""
	if fixedApp != "" && !checking {
		var ok bool
		if revision, ok = s.formRevision(w, r, "add the domain"); !ok {
			return
		}
	}
	rec, err := s.svc.GetApp(ctx, in.App)
	switch {
	case fixedApp != "" && errors.Is(err, store.ErrNotFound):
		s.appNotFound(w, r, fixedApp)
		return
	case in.App != "" && err != nil && !errors.Is(err, store.ErrNotFound):
		s.serverError(w, "read app", err)
		return
	}
	known := err == nil && in.App != ""

	form, err := s.domainAddFor(ctx, in, fixedApp)
	if err != nil {
		s.serverError(w, "draw the add form", err)
		return
	}
	refuse := func(status int) { s.showAddRefusal(w, r, status, form) }
	fieldError := func(f *formField, msg sentence) {
		f.Error, f.Alert = msg, true
		refuse(http.StatusUnprocessableEntity)
	}

	// The host: typed, not the console's own, not another app's.
	// DNS names are case-insensitive: Shop.Example.COM is shop.example.com, not an error.
	host := strings.ToLower(in.Host)
	if host == "" && checking {
		fieldError(&form.Host, plainS("Enter a domain."))
		return
	}
	if console := consoleHostOf(s.publicURL()); host != "" && console != "" && strings.TrimSuffix(strings.ToLower(host), ".") == console {
		fieldError(&form.Host, sentence{mono(host), plain(" is the console's own address: an app can't use it, or the console would stop answering. Use another domain.")})
		return
	}
	if host != "" {
		other, err := s.takenBy(ctx, host, in.App)
		if err != nil {
			s.serverError(w, "read domains", err)
			return
		}
		if other != "" {
			fieldError(&form.Host, sentence{mono(host), plain(" is already a domain of "), mono(other), plain(". Remove it there first.")})
			return
		}
	}

	if checking {
		chk, err := s.svc.CheckDomain(ctx, host, "")
		var fe *app.FieldError
		switch {
		case errors.As(err, &fe):
			fieldError(&form.Host, sentence{mono(host), plain(" is not a domain name: lowercase letters, digits, dashes and dots.")})
			return
		case err != nil:
			s.serverError(w, "check a domain", err)
			return
		}
		// Answer 303 to the same page at the panel, so a reload doesn't post again.
		s.checked.put(host, checkedAdd{in: in, check: chk}, s.now())
		http.Redirect(w, r, checkedBack(fixedApp, host), http.StatusSeeOther)
		return
	}

	// Add: an app that can take it.
	switch {
	case in.App == "":
		fieldError(&form.App, plainS("Choose the app the domain sends traffic to."))
		return
	case !known:
		fieldError(&form.App, sentence{plain("There is no app "), mono(in.App), plain(" anymore.")})
		return
	case pairApp != "" && pairApp != in.App:
		// Under the service select when there is one, else under the app's.
		f := &form.Service
		if !form.ServiceShown {
			f = &form.App
		}
		fieldError(f, plainS("That service is another app's: pick one of the chosen app's services."))
		return
	}
	if fixedApp == "" {
		revision = strings.TrimSpace(r.PostForm.Get("revision-" + in.App))
		if revision == "" {
			s.message(w, http.StatusBadRequest, "Bad request", "That form has no revision for the app.", "Open the page again and add the domain from there.")
			return
		}
	}
	// Alone, a service needn't be named: the app's first service with an HTTP port.
	if in.Service == "" {
		if t := addTargetsOf(rec.App); len(t) > 0 {
			in.Service = t[firstHTTPTarget(t)].Service
		}
	}
	i, found := serviceNamed(rec.App, in.Service)
	port, can := "", false
	if found {
		port, can = domainPort(rec.App.Services[i])
	}
	switch {
	case !found:
		form.Notice = sentence{plain("This app has no service "), mono(in.Service), plain(".")}
		refuse(http.StatusUnprocessableEntity)
		return
	case !can:
		form.Notice = sentence{plain("The service "), mono(in.Service), plain(" has no port a domain can go to.")}
		refuse(http.StatusUnprocessableEntity)
		return
	case host == "":
		fieldError(&form.Host, plainS("Enter a domain."))
		return
	case host != strings.TrimSuffix(host, "."):
		// SetDomainsIf would drop the dot; the definition's rule is that a host is typed without one.
		fieldError(&form.Host, plainS(strconv.Quote(host)+" is not a hostname: leave out the final dot"))
		return
	}

	edited := rec.App
	edited.Services = slices.Clone(edited.Services)
	edited.Services[i].Domains = append(slices.Clone(edited.Services[i].Domains), app.Domain{Host: host, Port: port, Path: in.Path})
	// An app that keeps its Compose file is edited through its overlay: SetDomainsIf does either.
	deployment, err := s.svc.SetDomainsIf(ctx, in.App, rec.App.Services[i].Name, edited.Services[i].Domains, service.Condition{IfRevision: revision}, app.TriggerConsole)
	if err == nil {
		http.Redirect(w, r, "/apps/"+url.PathEscape(in.App)+"/deployments#deployment-"+deployment.Deployment.ID, http.StatusSeeOther)
		return
	}
	var fieldErr *app.FieldError
	var taken *service.DomainTakenError
	switch {
	case errors.As(err, &taken):
		fieldError(&form.Host, sentence{mono(taken.Host), plain(" is already a domain of "), mono(taken.App), plain(". Remove it there first.")})
	case errors.Is(err, store.ErrNotFound):
		if fixedApp != "" {
			s.appNotFound(w, r, fixedApp)
			return
		}
		fieldError(&form.App, sentence{plain("There is no app "), mono(in.App), plain(" anymore.")})
	case errors.Is(err, service.ErrAppChanged):
		form.Notice = sentence{plain(changedSincePage)}
		refuse(http.StatusConflict)
	case errors.As(err, &fieldErr) && strings.Contains(fieldErr.Field, ".domains[") && strings.HasSuffix(fieldErr.Field, ".path"):
		fieldError(&form.Path, sentence{plain(fieldErr.Message)})
	case errors.As(err, &fieldErr) && strings.Contains(fieldErr.Field, ".domains["):
		fieldError(&form.Host, sentence{plain(fieldErr.Message)})
	default:
		status, notice, ok := refusalOf(edited, err)
		if !ok {
			s.serverError(w, "add a domain", err)
			return
		}
		form.Notice = notice
		refuse(status)
	}
}
