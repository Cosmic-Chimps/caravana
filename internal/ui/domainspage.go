package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Domains (feature 023): the workspace page /domains lists every address the
// apps answer on, each a row that opens, with the state Caravana's own checks
// last found (service.Domains: DNS against this host's addresses, and the
// certificate Traefik presents). The app's Domains page (domains.go) draws the
// same rows, filtered to the app. Both draw the Add panel (domainsadd.go).
// Nothing here dials out: the list reads the stored checks; "Check again" and
// the Add panel's "Check now" ask service.CheckDomain for a fresh one. No row,
// banner or notice holds a password or a hash: a Team-only domain shows a lock
// and nothing else.

const (
	// maxDomainBanners is how many problem banners stand above the table; the
	// rest are "and N more".
	maxDomainBanners = 3
	// checksEvery is how often the watcher checks while a domains page is open.
	checksEvery = "checks every minute"
)

// domainsPage is the template set "domains": the list with its Add panel.
type domainsPage struct {
	pageData
	List *domainsListView
}

// domainsListView is /domains: the head, the filter, a notice or a refusal,
// the live region and the Add panel.
type domainsListView struct {
	Filter schedFilterView
	Notice sentence
	Alert  sentence
	Region domainsRegion
	Add    *domainAddView
}

// domainsRegion is the live region of both pages: the count line, the
// banners, the table (or the empty state) and the footer. /parts/domains draws
// it alone.
type domainsRegion struct {
	Count       string
	Banners     []domainBanner
	More        *domainMore
	Rows        []domainRowView
	Empty       *emptyView
	NoMatch     bool
	ClearHref   string
	RefreshHref string
}

// domainBanner is one problem above the table: a DNS record that points
// elsewhere, or a certificate that is expiring.
type domainBanner struct {
	Host    string
	Mark    string
	Line    sentence // "`a.example.com` points at `185.199.108.153`, which isn't this host."
	Explain sentence // the fixed explanation
	Check   domainCheckForm
}

// domainMore is "and N more", linking to the first row without a banner.
type domainMore struct {
	N    int
	Href string
}

// domainCheckForm is a "Check again" post: the host, and the page to come back to.
type domainCheckForm struct {
	Action string // "/domains/<host>/check"
	App    string // the app's ID when the post comes from its page
}

// domainRowView is one domain: the summary row and its detail.
type domainRowView struct {
	Host   string
	Anchor string // "domain-<host>", the <details>'s stable id
	URL    string
	Open   bool
	// State is what a screen reader hears first: "a.example.com, live · HTTPS".
	State    string
	TeamOnly bool
	Path     string
	Pill     statusView
	// The Goes to cell: the app, then the service when the app has several, the
	// port and the environment; or, for a redirect row, the host it redirects to.
	App, AppID, Service string
	Port                string // ":8080", "" when the service has no such port
	Environment         string
	RedirectTo          string
	Cert                domainCertCell
	Detail              domainDetail
}

// domainCertCell is the Certificate column: "renews in 61 d", "expires in 6 d"
// (Warn), "Traefik's own" or "—". Title is the expiry date, for the hover.
type domainCertCell struct {
	Text  string
	Warn  bool
	Title string
}

// domainFact is one line of the detail's facts grid. Expires and Checked are
// drawn after the value as a clock time and as a relative time; After follows them.
type domainFact struct {
	Key     string
	Value   sentence
	Failed  bool
	Expires *clockTime
	Checked *when
	After   string
}

// domainDetail is what opens under a row: the state's sentence and facts at
// the left, the Options column at the right.
type domainDetail struct {
	Sentence sentence
	Quiet    bool
	Facts    []domainFact
	Wide     bool // the facts' keys are 110px (live, redirect), else 90px
	// Records is the record box of a domain with no DNS record.
	Records *recordBox
	Check   *domainCheckForm
	Link    *actionLink
	Options domainOptions
}

// domainOptions is the detail's right column. The Options form itself is the
// second console round's; for now it holds Remove… or why there is none.
type domainOptions struct {
	Note   sentence
	Remove *domainRemove
}

// domainRemove is the Remove… confirmation of a row: a deploy of the app
// without the host, on the revision the page was drawn from.
type domainRemove struct {
	Action, Revision, Service, Host string
}

// recordBox is the DNS records that point a host here: one group of Type, Name
// and Value per address of this host, or one A record with the words for the
// address when none is known.
type recordBox struct {
	Records []dnsRecord
}

// ---- Building the rows (pure) --------------------------------------------------------

// domainAppInfo is what a row needs of its app beyond the view: how many
// services it has, and what its Remove… needs.
type domainAppInfo struct {
	services int
	revision string
	defined  bool
	kept     bool
	stopped  bool
}

// domainPill is the row's status: the state's mark and words. A redirect row
// names its target, and a row that isn't served says why.
func domainPill(v service.DomainView) statusView {
	pill := statusView{Kind: v.State.Mark(), Word: v.State.Words()}
	switch {
	case v.State == edge.StateNotServed && v.Stopped:
		pill.Word = "app stopped"
	case v.State == edge.StateNotServed:
		pill.Word = "not deployed"
	case v.State == edge.StateRedirect && v.RedirectOf != "":
		pill.Word = "redirects to " + v.RedirectOf
	case v.State == edge.StateUnknown && v.CheckedAt == nil:
		pill.Word = "not checked yet"
	}
	return pill
}

// dayCount is a duration as whole days, "6 d"; under a day is "0 d".
func dayCount(d time.Duration) string {
	return strconv.Itoa(int(d/(24*time.Hour))) + " d"
}

// certCellOf is the Certificate column of a row.
func certCellOf(now time.Time, v service.DomainView) domainCertCell {
	if v.State == edge.StateNotServed {
		return domainCertCell{Text: "—"}
	}
	return certCellFor(now, v.Certificate)
}

// certCellFor is the Certificate column's words for a certificate: the same on
// /domains and in Settings › Certificates.
func certCellFor(now time.Time, c edge.Certificate) domainCertCell {
	title := ""
	if !c.NotAfter.IsZero() {
		title = "expires " + c.NotAfter.UTC().Format("2006-01-02 15:04 UTC")
	}
	switch c.Status {
	case edge.CertSelf:
		return domainCertCell{Text: edge.TraefikOwn}
	case edge.CertExpiring:
		left := c.NotAfter.Sub(now)
		if left <= 0 {
			return domainCertCell{Text: "expired", Warn: true, Title: title}
		}
		return domainCertCell{Text: "expires in " + dayCount(left), Warn: true, Title: title}
	case edge.CertIssued:
		if c.RenewsAt.After(now) {
			return domainCertCell{Text: "renews in " + dayCount(c.RenewsAt.Sub(now)), Title: title}
		}
		return domainCertCell{Text: "expires in " + dayCount(c.NotAfter.Sub(now)), Title: title}
	}
	return domainCertCell{Text: "—"}
}

// domainAppText is the app as a row names it: its name, which is its ID in
// Default › production.
func domainAppText(v service.DomainView) string {
	if v.AppName != "" {
		return v.AppName
	}
	return v.App
}

// seenElsewhere are the addresses DNS answered that aren't this host's (all of
// them when every one is), as "A · 185.199.108.153" parts.
func seenElsewhere(d edge.DNS) []string {
	var out, all []string
	for _, a := range d.Seen {
		typ := "A"
		if a.Is6() {
			typ = "AAAA"
		}
		all = append(all, typ+" · "+a.String())
		if !slices.Contains(d.Expected, a) {
			out = append(out, typ+" · "+a.String())
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

// addressesOf are the bare addresses seenElsewhere names, for a sentence.
func addressesOf(typed []string) string {
	out := make([]string, len(typed))
	for i, s := range typed {
		_, addr, _ := strings.Cut(s, " · ")
		out[i] = addr
	}
	return strings.Join(out, ", ")
}

// expectedText is "A · 203.0.113.1" for each of this host's addresses, or the
// words for one when none is known.
func expectedText(d edge.DNS) string {
	var out []string
	for _, a := range d.Expected {
		typ := "A"
		if a.Is6() {
			typ = "AAAA"
		}
		out = append(out, typ+" · "+a.String())
	}
	if len(out) == 0 {
		return "A · " + hostIPWords
	}
	return strings.Join(out, ", ")
}

// checkedWhen is when a check ran, as a relative time: "40 s ago" under a
// minute, then whenOf's words.
func checkedWhen(now time.Time, t *time.Time) when {
	w := whenOf(now, t, "not yet")
	if t != nil {
		if d := now.Sub(*t); d >= 0 && d < time.Minute {
			w.Text = strconv.Itoa(int(d/time.Second)) + " s ago"
		}
	}
	return w
}

// dnsFactText is the DNS fact of a live row: "A → 203.0.113.1", or the status
// in words when nothing was seen.
func dnsFactText(d edge.DNS) string {
	var out []string
	for _, a := range d.Seen {
		typ := "A"
		if a.Is6() {
			typ = "AAAA"
		}
		out = append(out, typ+" → "+a.String())
	}
	if len(out) > 0 {
		return strings.Join(out, ", ")
	}
	switch d.Status {
	case edge.DNSNone:
		return "no record"
	case edge.DNSOK:
		return "points here"
	}
	return "couldn't check"
}

// certFact is the certificate fact of a live row: "Let's Encrypt · renews in
// 61 d" and the expiry as a clock time.
func certFact(now time.Time, c edge.Certificate) domainFact {
	f := domainFact{Key: "certificate"}
	issuer := c.Issuer
	if issuer == "" {
		issuer = "unknown issuer"
	}
	switch c.Status {
	case edge.CertIssued, edge.CertExpiring:
		words := issuer
		if c.RenewsAt.After(now) {
			words += " · renews in " + dayCount(c.RenewsAt.Sub(now))
		}
		f.Value = plainS(words + " · expires ")
		f.Expires = new(clockOf(now, c.NotAfter))
	case edge.CertSelf:
		f.Value = plainS(edge.TraefikOwn)
	case edge.CertPending:
		f.Value = plainS("waiting for Let's Encrypt")
	case edge.CertError:
		f.Value = plainS("the handshake failed")
	default:
		f.Value = plainS("—")
	}
	return f
}

// domainDetailOf builds what opens under a row, by its state. box is the
// record box for this host (used when DNS has no record); opts its Options column.
func domainDetailOf(now time.Time, v service.DomainView, box recordBox, check domainCheckForm, opts domainOptions) domainDetail {
	d := domainDetail{Options: opts}
	appName := domainAppText(v)
	checked := checkedWhen(now, v.CheckedAt)
	switch v.State {
	case edge.StateDNSElsewhere:
		seen := seenElsewhere(v.DNS)
		d.Sentence = sentence{plain("The domain points at "), mono(addressesOf(seen)), plain(", which isn't this host. Until it changes, caravana can't get a certificate and visitors won't reach "), mono(appName), plain(".")}
		d.Facts = []domainFact{
			{Key: "now", Value: plainS(strings.Join(seen, ", ")), Failed: true},
			{Key: "should be", Value: plainS(expectedText(v.DNS))},
			{Key: "last check", Checked: &checked, After: " · " + checksEvery},
		}
		d.Check = &check
	case edge.StateCertExpiring:
		d.Sentence = plainS(expiringSentence(now, v.Certificate) + " Let's Encrypt renews 30 days before expiry, so renewal has been failing: check that DNS points here and that port 80 reaches this host.")
		d.Check = &check
	case edge.StateSelfSigned:
		d.Sentence, d.Quiet = plainS("Traefik serves this domain with its own certificate: browsers will warn. Certificates are issued when caravana is installed with Let's Encrypt."), true
	case edge.StateCertPending:
		d.Sentence, d.Quiet = plainS("Waiting for Let's Encrypt. DNS points here; the certificate usually arrives within a minute of the first visit."), true
	case edge.StateDNSNone:
		d.Sentence = plainS("No DNS record yet. Add this record where you manage DNS:")
		d.Records = &box
		d.Check = &check
	case edge.StateLive, edge.StateRedirect:
		router := sentence{mono(v.Router)}
		if v.Router != v.App+"-"+v.Service {
			router = append(router, plain(" (its own, for its options)"))
		}
		d.Wide = true
		d.Facts = []domainFact{
			certFact(now, v.Certificate),
			{Key: "DNS", Value: plainS(dnsFactText(v.DNS))},
			{Key: "router", Value: router},
			{Key: "checked", Checked: &checked},
		}
	case edge.StateNotServed:
		href := "/apps/" + url.PathEscape(v.App)
		if v.Stopped {
			d.Sentence = plainS("The app is stopped: start it to serve this domain.")
			d.Link = &actionLink{Label: "Open " + appName, Href: href}
		} else {
			d.Sentence = plainS("Nothing is deployed yet.")
			d.Link = &actionLink{Label: "Deploy " + appName, Href: href + "/deployments"}
		}
		d.Quiet = true
	default: // unknown
		if v.CheckedAt == nil {
			d.Sentence = plainS("Not checked yet. Checks run every minute while this page is open.")
		} else {
			d.Sentence = plainS("The last check couldn't tell: the resolvers didn't answer, or Traefik's edge didn't present a certificate.")
			d.Wide = true
			d.Facts = []domainFact{{Key: "checked", Checked: &checked, After: " · " + checksEvery}}
		}
		d.Quiet = true
		d.Check = &check
	}
	return d
}

// expiringSentence is "The certificate expires in 6 days and hasn't been
// renewed." (or "expired … ago").
func expiringSentence(now time.Time, c edge.Certificate) string {
	left := c.NotAfter.Sub(now)
	if left <= 0 {
		return "The certificate expired " + relative(now, c.NotAfter) + " and hasn't been renewed."
	}
	return "The certificate expires in " + plural(int(left/(24*time.Hour)), "day") + " and hasn't been renewed."
}

// bannerOf is the banner of a problem row; ok is false for a row that has none.
func bannerOf(now time.Time, v service.DomainView, check domainCheckForm) (domainBanner, bool) {
	b := domainBanner{Host: v.Host, Mark: v.State.Mark(), Check: check}
	switch v.State {
	case edge.StateDNSElsewhere:
		b.Line = sentence{mono(v.Host), plain(" points at "), mono(addressesOf(seenElsewhere(v.DNS))), plain(", which isn't this host.")}
		b.Explain = sentence{plain("Until it changes, caravana can't get a certificate and visitors won't reach "), mono(domainAppText(v)), plain(". Change the record where you manage DNS.")}
	case edge.StateCertExpiring:
		left := v.Certificate.NotAfter.Sub(now)
		if left <= 0 {
			b.Line = sentence{mono(v.Host), plain(" expired " + relative(now, v.Certificate.NotAfter) + " and hasn't been renewed.")}
		} else {
			b.Line = sentence{mono(v.Host), plain(" expires in " + plural(int(left/(24*time.Hour)), "day") + " and hasn't been renewed.")}
		}
		b.Explain = plainS("Let's Encrypt renews 30 days before expiry, so renewal has been failing: check that DNS points here and that port 80 reaches this host.")
	default:
		return domainBanner{}, false
	}
	return b, true
}

// domainsCount is the count line: "12 domains · 2 need you · 1 getting a
// certificate", or "· all healthy" when every row is live or redirects.
func domainsCount(rows []service.DomainView) string {
	needs, pending, fine := 0, 0, 0
	for _, v := range rows {
		switch v.State.Mark() {
		case "failed", "warning":
			needs++
		case "rolling":
			pending++
		}
		if v.State == edge.StateLive || v.State == edge.StateRedirect {
			fine++
		}
	}
	parts := []string{plural(len(rows), "domain")}
	switch {
	case needs == 1:
		parts = append(parts, "1 needs you")
	case needs > 1:
		parts = append(parts, strconv.Itoa(needs)+" need you")
	}
	if pending > 0 {
		parts = append(parts, strconv.Itoa(pending)+" getting a certificate")
	}
	if len(rows) > 0 && fine == len(rows) {
		parts = append(parts, "all healthy")
	}
	return strings.Join(parts, " · ")
}

// domainProblem says a row's state needs the owner: it opens by itself.
func domainProblem(s edge.State) bool {
	m := s.Mark()
	return m == "failed" || m == "warning"
}

// domainAnchor is a row's element id.
func domainAnchor(host string) string { return "domain-" + host }

// domainHref is the page a domain's row is on, opened.
func domainHref(host string) string {
	return "/domains?open=" + url.QueryEscape(host) + "#" + domainAnchor(host)
}

// domainOptionsOf is the Options column of a row: Remove… for a domain of an
// app the page can change, or why it can't.
func domainOptionsOf(v service.DomainView, info domainAppInfo) domainOptions {
	switch {
	case v.RedirectOf != "":
		return domainOptions{Note: sentence{plain("Redirects to "), mono(v.RedirectOf), plain(". Change it on that domain's row.")}}
	case info.kept:
		return domainOptions{Note: plainS(fileEditedSentence + " Change its domains in the file, in Configure.")}
	case info.stopped:
		return domainOptions{Note: plainS("The app is stopped: start it first.")}
	case !info.defined || info.revision == "":
		return domainOptions{}
	}
	return domainOptions{Remove: &domainRemove{Action: "/apps/" + url.PathEscape(v.App) + "/domains/remove",
		Revision: info.revision, Service: v.Service, Host: v.Host}}
}

// domainViewsOf builds the rows and banners from the service's list (already
// sorted). open is the host the address asks to open ("" for none): without
// it, the first row whose state needs the owner opens by itself. appPage says
// the rows are drawn on the app's own page, so Check again comes back there.
func domainViewsOf(now time.Time, list []service.DomainView, apps map[string]domainAppInfo, box func(host string) recordBox, open string, appPage bool) ([]domainRowView, []domainBanner, *domainMore) {
	explicit := open != "" && slices.ContainsFunc(list, func(v service.DomainView) bool { return v.Host == open })
	var rows []domainRowView
	var banners []domainBanner
	var more *domainMore
	opened := false
	for _, v := range list {
		info := apps[v.App]
		check := domainCheckForm{Action: "/domains/" + url.PathEscape(v.Host) + "/check"}
		if appPage {
			check.App = v.App
		}
		pill := domainPill(v)
		row := domainRowView{Host: v.Host, Anchor: domainAnchor(v.Host), URL: "https://" + v.Host + v.Path,
			State: v.Host + ", " + pill.Word, TeamOnly: v.TeamOnly, Path: v.Path, Pill: pill,
			App: domainAppText(v), AppID: v.App, Environment: v.Environment, RedirectTo: v.RedirectOf,
			Cert: certCellOf(now, v)}
		if row.Environment == "" {
			row.Environment = app.DefaultEnvironment
		}
		if info.services > 1 {
			row.Service = v.Service
		}
		if v.ContainerPort > 0 {
			row.Port = ":" + strconv.Itoa(v.ContainerPort)
		}
		switch {
		case explicit:
			row.Open = v.Host == open
		case !opened && domainProblem(v.State):
			row.Open, opened = true, true
		}
		row.Detail = domainDetailOf(now, v, box(v.Host), check, domainOptionsOf(v, info))
		if b, ok := bannerOf(now, v, check); ok {
			if len(banners) < maxDomainBanners {
				banners = append(banners, b)
			} else if more == nil {
				more = &domainMore{N: 1, Href: "#" + row.Anchor}
			} else {
				more.N++
			}
		}
		rows = append(rows, row)
	}
	return rows, banners, more
}

// ---- Loading --------------------------------------------------------------------------

// domainsQuery is what ?project=, ?app= and ?open= ask for.
type domainsQuery struct {
	Project, App, Open string
}

func domainsQueryOf(q url.Values) domainsQuery {
	return domainsQuery{Project: strings.TrimSpace(q.Get("project")), App: strings.TrimSpace(q.Get("app")), Open: strings.TrimSpace(q.Get("open"))}
}

// filterQuery is the query string of the filter alone ("" for none).
func (q domainsQuery) filterQuery() string {
	return schedulesQuery{Project: q.Project, App: q.App}.filterQuery()
}

// recordBoxFor is the record box of a host with this host's addresses: an A
// record per IPv4 and an AAAA per IPv6 address, or one A record with the
// words for the address when none is known.
func recordBoxFor(host string, addrs []string) recordBox {
	name := service.DNSRecordName(host)
	var box recordBox
	for _, a := range addrs {
		typ := "A"
		if strings.Contains(a, ":") {
			typ = "AAAA"
		}
		box.Records = append(box.Records, dnsRecord{Type: typ, Name: name, Value: a, Known: true})
	}
	if len(box.Records) == 0 {
		box.Records = []dnsRecord{{Type: "A", Name: name, Value: hostIPWords}}
	}
	return box
}

// hostAddressTexts are this host's public addresses as text.
func (s *server) hostAddressTexts(ctx context.Context) []string {
	var out []string
	for _, a := range s.svc.HostAddresses(ctx) {
		out = append(out, a.String())
	}
	return out
}

// loadDomains reads the domains the filter keeps and builds the region. empty
// is the empty state to draw when there are none and no filter is set;
// appPage is domainViewsOf's. A filter that names no project or app is "No
// domain matches".
func (s *server) loadDomains(ctx context.Context, q domainsQuery, empty emptyView, appPage bool) (domainsRegion, error) {
	refresh := url.Values{}
	if q.Project != "" {
		refresh.Set("project", q.Project)
	}
	if q.App != "" {
		refresh.Set("app", q.App)
	}
	if appPage {
		refresh.Set("page", "app")
	}
	region := domainsRegion{RefreshHref: "/parts/domains"}
	if len(refresh) > 0 {
		region.RefreshHref += "?" + refresh.Encode()
	}
	list, err := s.svc.Domains(ctx, service.DomainFilter{App: q.App, Project: q.Project})
	switch {
	case errors.Is(err, service.ErrProjectNotFound), errors.Is(err, store.ErrNotFound):
		region.Count, region.NoMatch, region.ClearHref = plural(0, "domain"), true, "/domains"
		return region, nil
	case err != nil:
		return domainsRegion{}, err
	}
	sums, err := s.svc.ListApps(ctx)
	if err != nil {
		return domainsRegion{}, err
	}
	apps := make(map[string]domainAppInfo, len(sums))
	for _, sum := range sums {
		apps[sum.App.ID] = domainAppInfo{services: len(sum.App.Services), revision: sum.Revision, defined: sum.App.Defined(),
			kept: sum.Compose != nil, stopped: sum.StoppedAt != nil}
	}
	// This host's addresses are read only when a row draws the record box.
	var addrs []string
	if slices.ContainsFunc(list, func(v service.DomainView) bool { return v.State == edge.StateDNSNone }) {
		addrs = s.hostAddressTexts(ctx)
	}
	box := func(host string) recordBox { return recordBoxFor(host, addrs) }

	region.Count = domainsCount(list)
	region.Rows, region.Banners, region.More = domainViewsOf(s.now(), list, apps, box, q.Open, appPage)
	if len(list) == 0 {
		if q.filterQuery() != "" && !appPage {
			region.NoMatch, region.ClearHref = true, "/domains"
		} else {
			region.Empty = &empty
		}
	}
	return region, nil
}

// domainsEmpty is /domains' empty state.
var domainsEmpty = emptyView{Title: "No domains yet.", Text: "Give an app a domain and HTTPS is set up for it.",
	ActionLabel: "Add domain", ActionHref: "#add"}

// ---- Handlers -------------------------------------------------------------------------

// showDomains answers GET /domains.
func (s *server) showDomainsPage(w http.ResponseWriter, r *http.Request, session string) {
	q := domainsQueryOf(r.URL.Query())
	add, err := s.domainAddFor(r.Context(), domainAddInput{App: q.App}, "")
	if err != nil {
		s.serverError(w, "draw the add form", err)
		return
	}
	s.renderDomains(w, r, http.StatusOK, q, nil, add)
}

// renderDomains draws /domains with a refusal (nil for none) and the Add panel.
func (s *server) renderDomains(w http.ResponseWriter, r *http.Request, status int, q domainsQuery, alert sentence, add *domainAddView) {
	ctx := r.Context()
	region, err := s.loadDomains(ctx, q, domainsEmpty, false)
	if err != nil {
		s.serverError(w, "list domains", err)
		return
	}
	filter, err := s.schedulesFilterOf(ctx, schedulesQuery{Project: q.Project, App: q.App})
	if err != nil {
		s.serverError(w, "list apps", err)
		return
	}
	head, err := s.shellPage(r, "Domains", navDomains, crumb{Label: "Domains"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live?watch=domains"
	s.render(w, status, "domains", domainsPage{pageData: head, List: &domainsListView{Filter: filter, Alert: alert, Region: region, Add: add}})
}

// partDomains answers GET /parts/domains: the count line, the banners, the
// table and the footer, with the filter of the page (?page=app: the app's own
// page, whose empty state and Check again differ).
func (s *server) partDomains(w http.ResponseWriter, r *http.Request, session string) {
	q := domainsQueryOf(r.URL.Query())
	q.Open = ""
	appPage := r.URL.Query().Get("page") == "app" && q.App != ""
	empty := domainsEmpty
	if appPage {
		empty = appDomainsEmpty(false)
	}
	region, err := s.loadDomains(r.Context(), q, empty, appPage)
	if err != nil {
		s.log.Error("list domains", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "domains", "domains-region", region)
}

// checkDomainAgain answers POST /domains/{host}/check: the host is checked
// now, and the page it came from is drawn again with its row open.
func (s *server) checkDomainAgain(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	host := r.PathValue("host")
	if !s.readForm(w, r) {
		return
	}
	_, err := s.svc.CheckDomain(ctx, host, "")
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr):
		s.message(w, http.StatusUnprocessableEntity, "Not a domain", fmt.Sprintf("%q is not a domain caravana can check.", host), "Go back to Domains and try again.")
		return
	case err != nil:
		s.serverError(w, "check a domain", err)
		return
	}
	back := domainHref(host)
	if id := strings.TrimSpace(r.PostForm.Get("app")); id != "" {
		if _, err := s.svc.GetApp(ctx, id); err == nil {
			back = "/apps/" + url.PathEscape(id) + "/domains?open=" + url.QueryEscape(host) + "#" + domainAnchor(host)
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
