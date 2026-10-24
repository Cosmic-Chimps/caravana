package main

// The console-domains-states step of the lab test (feature 023, quickstart §6):
// the Domains page's states, the Add panel, the Options form, Team only and
// Settings › Certificates through the console's forms (no JavaScript), against
// real Nomad and Traefik. The DNS side is the lab's fake resolver
// (elsewhere.test → 203.0.113.9, *.invalid not found, *.localhost loopback);
// the certificate side is Traefik's own, since the lab's has no ACME resolver.
// It is in its own file only because console_lab_test.go is long; runConsole
// lists it after console-schedules.

import (
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	// pillRe is the quiet status pill of a Domains row: its mark and its words.
	pillRe = regexp.MustCompile(`cv-pill cv-pill-(\w+) cv-pill-quiet cv-pill-sm"><span class="cv-mark cv-mark-\w+" aria-hidden="true"></span>([^<]*)</span>`)
	// certCellRe is a row's Certificate column.
	certCellRe = regexp.MustCompile(`class="cv-dmcert[^"]*" data-label="Certificate"[^>]*>([^<]*)</span>`)
	// onceRe is the password the shown-once panel draws.
	onceRe = regexp.MustCompile(`id="team-password-value">([^<]+)</code>`)
	// teamSwitchRe is the Team only switch of an Options form, with " checked" when it is on.
	teamSwitchRe = regexp.MustCompile(`name="teamOnly" value="on"( checked)?>`)
)

// domainRow is the markup of one domain's row on a Domains page: from its
// <details> to the next row's (or the footer). A row holds <details> of its own
// (Remove…, New password) with another class, so the class marks the end.
func domainRow(page, host string) string {
	start := strings.Index(page, `<details class="cv-dmdetails" id="domain-`+host+`"`)
	if start < 0 {
		return ""
	}
	rest := page[start+1:]
	end := len(rest)
	for _, next := range []string{`<details class="cv-dmdetails"`, `<p class="cv-dmfoot">`} {
		if i := strings.Index(rest, next); i >= 0 && i < end {
			end = i
		}
	}
	return page[start : start+1+end]
}

// formMarkup is the markup of the first form of the page that posts to action.
func formMarkup(page, action string) string {
	m := regexp.MustCompile(`(?s)<form method="post" action="` + regexp.QuoteMeta(action) + `(?:#[^"]*)?"[^>]*>(.*?)</form>`).FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return m[1]
}

// prose is pageText with the space a mono span leaves before a comma or a
// full stop taken out: the sentences read as the owner reads them.
func prose(page string) string {
	t := pageText(page)
	for _, p := range []string{",", "."} {
		t = strings.ReplaceAll(t, " "+p, p)
	}
	return t
}

// ensureSignedIn signs the console client in when the step runs alone
// (-run 'TestConsoleLab/console-domains-states$' skips console-sign-in).
func (c *console) ensureSignedIn(t *testing.T) {
	t.Helper()
	if resp, _ := c.get(t, "/apps"); resp.StatusCode == http.StatusOK {
		return
	}
	resp, body := c.post(t, "/login", url.Values{"email": {labEmail}, "password": {labPassword}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /login = %d: %s", resp.StatusCode, pageText(body))
	}
}

// domainsStates: the Domains page and what it offers, on a whoami app cd023
// made through New app's form, with the domain cd023.localhost, and four more
// domains added through the Add panel (elsewhere.test, nothing.invalid,
// www.cd023.localhost, www.rd023.localhost). The addresses are reached through
// the lab's edge, 127.0.0.1:8443, with the host as the TLS server name.
//
//  1. cd023.localhost's row, on the app's page after Check again and on
//     /domains: Traefik's own certificate, the Goes to and Certificate cells,
//     the sentence of its state; its Options form (Force HTTPS locked on,
//     Team only off, the Path field, Remove…) in the detail /domains?open=
//     draws.
//  2. The Add panel's Check now for elsewhere.test (DNS points at 203.0.113.9;
//     the certificate "issued after you add it"), nothing.invalid and a host
//     that points here; nothing is deployed by a check. Add domain and deploy
//     for four hosts, each a deploy that ends healthy.
//  3. Check again on every row, then /domains?app=cd023: elsewhere.test is
//     "DNS points elsewhere" with its banner, the facts and the address seen,
//     nothing.invalid is "no DNS record" with the record to add, the rest are
//     Traefik's own certificate; the count line says two need you.
//  4. Options: a form from before the adds is stale (409, nothing deployed);
//     Team only on for cd023.localhost answers 200 no-store with the password
//     once, and the edge answers 401 without it and 200 with team:<it>;
//     New password (from the app's page) shows another once and the old one
//     stops; a domain that isn't Team only has no password to make; a path
//     without a slash is refused under the field and /api is saved on
//     www.cd023.localhost (200 under it, 404 outside); the redirect is
//     refused while the pair is a domain of the app and saved for
//     www.rd023.localhost (rd023.localhost answers 301 or 308 with the
//     path and query kept).
//  5. The needs-you list on /apps names elsewhere.test with the address seen
//     and "Open domain"; Stop (through the form) makes every row "app stopped"
//     and the item goes, and Options are refused with the stopped sentence;
//     Start brings the app, the password and the item back.
//  6. Settings › Certificates: the nav entry (its count is the issued
//     certificates, none in the lab), the table, the issuer rows, Warn when 14 →
//     7 → 14, and a refused value.
//  7. Remove elsewhere.test (its confirmation's form): it leaves /domains and
//     the item goes; the same form again is refused.
//  8. Neither password, nor a bcrypt hash, is in any page, API answer, the Nomad
//     job, Traefik's variable, the SQLite data or Caravana's log after the
//     answers that showed them; the app is deleted through its form and
//     Traefik's variable loses its items.
//
// dns_elsewhere needs the console's address (CARAVANA_PUBLIC_URL) to know which
// addresses are this host's; the lab's resolver gives that name 203.0.113.1.
// Without it elsewhere.test is "couldn't check" here, and the step says so.
// Budget: nine deploys, each waited for up to 4 minutes, normally half one.
func (c *console) domainsStates(t *testing.T) {
	const (
		id        = "cd023"
		apex      = "cd023.localhost"
		www       = "www.cd023.localhost"
		elsewhere = "elsewhere.test"
		nothing   = "nothing.invalid"
		rdApex    = "rd023.localhost"
		rdWWW     = "www.rd023.localhost"
	)
	c.ensureSignedIn(t)
	expectElsewhere := strings.TrimSpace(os.Getenv("CARAVANA_PUBLIC_URL")) != ""
	if !expectElsewhere {
		t.Log("CARAVANA_PUBLIC_URL isn't set: Caravana doesn't know this host's addresses, so elsewhere.test can't be \"DNS points elsewhere\" here")
	}

	// Every password the step makes, so a failure message never prints one.
	var passwords []string
	safe := func(text string) string {
		for _, p := range passwords {
			if p != "" {
				text = strings.ReplaceAll(text, p, "<password>")
			}
		}
		return text
	}
	text := func(page string) string { return safe(prose(page)) }

	c.removeLabApp(id) // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	t.Cleanup(func() { c.removeLabApp(id) })
	t.Cleanup(func() { logTail(t, c.g.target) })
	// Warn when is restored whatever happens in step 6.
	t.Cleanup(func() {
		if resp, err := c.g.apiRequest(http.MethodPut, "/v1/settings/certificates", "application/json", `{"warnBeforeDays":14}`); err == nil {
			resp.Body.Close()
		}
	})

	// settled waits for the deployment a post started (the newest one, which
	// isn't before) to end healthy.
	settled := func(before string) {
		t.Helper()
		d := c.latestDeployment(t, id)
		if d.ID == before {
			t.Fatalf("no new deployment of %s after the post (still %s)", id, before)
		}
		if final := c.g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deployment %s of %s ended %s: %s", d.ID, id, final.Status, final.Error)
		}
	}
	newest := func() string {
		var list struct {
			Deployments []gateDeployment `json:"deployments"`
		}
		c.g.getJSON(t, "/v1/apps/"+id+"/deployments?limit=1", &list)
		if len(list.Deployments) == 0 {
			return ""
		}
		return list.Deployments[0].ID
	}
	// optionsOf is the Options form of a host's row on a page, as a browser sends it:
	// the checkboxes that aren't switched on are left out by the caller.
	optionsOf := func(page, host string) url.Values {
		t.Helper()
		forms := formsPosting(page, "/domains/"+host+"/options")
		if len(forms) != 1 || forms[0].Get("revision") == "" || forms[0].Get("app") != id || forms[0].Get("service") != id {
			t.Fatalf("the Options form of %s = %v, want one with the app, its service and a revision:\n%s", host, forms, text(page))
		}
		f := forms[0]
		f.Del("teamOnly")
		f.Del("wwwRedirect")
		return f
	}
	domainsPage := func(query string) string {
		t.Helper()
		resp, page := c.get(t, "/domains"+query)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /domains%s = %d: %s", query, resp.StatusCode, text(page))
		}
		return page
	}
	// expectRow checks a row's mark, words and Certificate cell.
	expectRow := func(page, host, mark, words, cert string) {
		t.Helper()
		row := domainRow(page, host)
		if row == "" {
			t.Errorf("the page has no row for %s:\n%s", host, text(page))
			return
		}
		m := pillRe.FindStringSubmatch(row)
		if m == nil || m[1] != mark || html.UnescapeString(m[2]) != words {
			t.Errorf("the status of %s = %q, want %s %q", host, m, mark, words)
		}
		if cm := certCellRe.FindStringSubmatch(row); cm == nil || html.UnescapeString(cm[1]) != cert {
			t.Errorf("the Certificate cell of %s = %q, want %q", host, cm, cert)
		}
	}
	// checkAgain is Check again on a row: a post that checks the host and comes back to its row.
	checkAgain := func(host string) {
		t.Helper()
		resp, body := c.post(t, "/domains/"+host+"/check", url.Values{"app": {id}})
		want := "/apps/" + id + "/domains?open=" + host + "#domain-" + host
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
			t.Fatalf("POST the check of %s = %d → %q, want 303 to %s: %s", host, resp.StatusCode, resp.Header.Get("Location"), want, text(body))
		}
	}
	appDomains := func() string {
		t.Helper()
		resp, page := c.get(t, "/apps/"+id+"/domains")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /apps/%s/domains = %d: %s", id, resp.StatusCode, text(page))
		}
		return page
	}
	// addDomain posts the Add panel of the app's page for host: action check or add.
	addDomain := func(host, action string) (*http.Response, string) {
		t.Helper()
		forms := formsPosting(appDomains(), "/apps/"+id+"/domains")
		if len(forms) != 1 || forms[0].Get("revision") == "" {
			t.Fatalf("the Add panel of %s's page = %v, want one form with a revision", id, forms)
		}
		f := forms[0]
		f.Set("host", host)
		f.Set("action", action)
		resp, body := c.post(t, "/apps/"+id+"/domains", f)
		if action == "check" {
			// Check now answers 303 to the page at the panel (?checked=<host>#add), which is drawn
			// from the check's cached result, so the answer the owner reads is that page.
			want := "/apps/" + id + "/domains?checked=" + host + "#add"
			if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
				t.Fatalf("Check now for %s = %d → %q, want 303 to %s: %s", host, resp.StatusCode, resp.Header.Get("Location"), want, text(body))
			}
			resp, body = c.get(t, strings.TrimSuffix(want, "#add"))
		}
		return resp, body
	}
	rowsOf := func() map[string]gateDomain { return c.g.domainRows(t, id, false) }

	// 1. The app, from New app's form, with its first domain.
	resp, body := c.post(t, "/apps", url.Values{"name": {id}, "image": {"traefik/whoami:latest"}, "port": {"80"},
		"domain": {apex}, "healthPath": {"/"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+id+"/deployments" {
		t.Fatalf("POST /apps = %d → %q, want 303 to /apps/%s/deployments: %s", resp.StatusCode, resp.Header.Get("Location"), id, text(body))
	}
	settled("")
	c.g.waitDomainStatus(t, http.StatusOK, time.Minute, apex, http.MethodGet, "/", "", "")

	checkAgain(apex)
	_, page := c.get(t, "/apps/"+id+"/domains?open="+apex)
	expectRow(page, apex, "idle", "Traefik's own certificate", "Traefik's own")
	row := domainRow(page, apex)
	if !strings.Contains(row, `id="domain-`+apex+`" open`) {
		t.Errorf("the row of %s isn't open after ?open=", apex)
	}
	if got := text(row); !strings.Contains(got, "cd023 :80 · production") ||
		!strings.Contains(got, "Traefik serves this domain with its own certificate: browsers will warn. Certificates are issued when caravana is installed with Let's Encrypt.") {
		t.Errorf("the row of %s = %s, want it going to cd023 :80 · production, with the sentence of Traefik's own certificate", apex, got)
	}
	if r := rowsOf()[apex]; r.State != "self_signed" || r.Router != id+"-"+id || r.DNS.Status != "ok" || r.Certificate.Status != "self" {
		t.Errorf("the API's row of %s = %+v, want self_signed on router %s-%s, DNS ok, certificate self", apex, r, id, id)
	}

	page = domainsPage("?open=" + apex)
	expectRow(page, apex, "idle", "Traefik's own certificate", "Traefik's own")
	row = domainRow(page, apex)
	opts := formMarkup(page, "/domains/"+apex+"/options")
	switch {
	case !strings.Contains(row, `id="domain-`+apex+`" open`):
		t.Errorf("/domains?open=%s doesn't draw its row open", apex)
	case !strings.Contains(opts, `role="switch" checked disabled><span>Force HTTPS</span>`) || !strings.Contains(text(row), "On for every domain."):
		t.Errorf("the Options of %s have no Force HTTPS locked on with \"On for every domain.\":\n%s", apex, text(row))
	case !strings.Contains(opts, "Team only (a shared password)") || teamSwitchRe.FindStringSubmatch(opts) == nil || teamSwitchRe.FindStringSubmatch(opts)[1] != "":
		t.Errorf("the Team only switch of %s isn't there and off:\n%s", apex, opts)
	case !strings.Contains(opts, `name="path" value=""`) || !strings.Contains(opts, "Save and deploy"):
		t.Errorf("the Options of %s have no empty Path field and Save and deploy:\n%s", apex, opts)
	case !strings.Contains(row, "Remove and deploy") || strings.Contains(row, "New password"):
		t.Errorf("the row of %s should offer Remove… and not New password:\n%s", apex, text(row))
	}
	stale := optionsOf(page, apex) // read before the adds below: its revision goes stale

	// 2. The Add panel.
	before := c.deploymentCount(t, id)
	resp, body = addDomain(elsewhere, "check")
	got := text(body)
	switch {
	case resp.StatusCode != http.StatusOK:
		t.Fatalf("Check now for %s = %d: %s", elsewhere, resp.StatusCode, got)
	case expectElsewhere && !strings.Contains(got, "points at 203.0.113.9"):
		t.Errorf("Check now for %s doesn't name the address 203.0.113.9:\n%s", elsewhere, got)
	case !expectElsewhere && !strings.Contains(got, "couldn't check: this host's public address isn't known"):
		t.Errorf("Check now for %s without a console address = %s, want \"couldn't check: this host's public address isn't known\"", elsewhere, got)
	case !strings.Contains(got, "HTTPS certificate") || !strings.Contains(got, "issued after you add it"):
		t.Errorf("Check now for %s: the certificate line isn't \"issued after you add it\":\n%s", elsewhere, got)
	case !strings.Contains(body, `value="`+elsewhere+`"`):
		t.Errorf("Check now for %s doesn't keep what was typed", elsewhere)
	}
	if expectElsewhere && !regexp.MustCompile(`cv-mark-failed[^>]*></span><span class="cv-dmcheck-text">.{0,120}DNS points at caravana`).MatchString(body) {
		t.Errorf("the DNS line of Check now for %s isn't marked failed", elsewhere)
	}
	resp, body = addDomain(nothing, "check")
	if got = text(body); resp.StatusCode != http.StatusOK || !strings.Contains(got, "no DNS record") || !strings.Contains(got, "issued after you add it") {
		t.Errorf("Check now for %s = %d: %s, want \"no DNS record\"", nothing, resp.StatusCode, got)
	}
	if !regexp.MustCompile(`cv-mark-warning[^>]*></span><span class="cv-dmcheck-text">.{0,120}DNS points at caravana`).MatchString(body) {
		t.Errorf("the DNS line of Check now for %s isn't marked warning (a missing record is a warning, as the list says)", nothing)
	}
	resp, body = addDomain(www, "check")
	if got = text(body); resp.StatusCode != http.StatusOK || strings.Contains(got, "no DNS record") || strings.Contains(got, "points at 203.0.113.9") ||
		!strings.Contains(got, "issued after you add it") {
		t.Errorf("Check now for %s, which points here = %d: %s, want no reason under DNS", www, resp.StatusCode, got)
	}
	if n := c.deploymentCount(t, id); n != before {
		t.Errorf("the checks left %d deployments, want %d: Check now deploys nothing", n, before)
	}
	for _, host := range []string{elsewhere, nothing, www, rdWWW} {
		resp, body = addDomain(host, "add")
		c.deployedFrom(t, id, resp, body)
	}
	for _, host := range []string{www, rdWWW} {
		c.g.waitDomainStatus(t, http.StatusOK, time.Minute, host, http.MethodGet, "/", "", "")
	}

	// 3. Check again on every row; the states.
	hosts := []string{apex, elsewhere, nothing, www, rdWWW} // rdApex joins once it redirects
	for _, host := range hosts {
		checkAgain(host)
	}
	page = domainsPage("?app=" + id + "&open=" + elsewhere)
	selfCert := "Traefik's own"
	expectRow(page, apex, "idle", "Traefik's own certificate", selfCert)
	expectRow(page, www, "idle", "Traefik's own certificate", selfCert)
	expectRow(page, rdWWW, "idle", "Traefik's own certificate", selfCert)
	expectRow(page, nothing, "warning", "no DNS record", selfCert)
	if got = text(page); !strings.Contains(got, "5 domains · 2 need you") {
		t.Errorf("the count line of /domains?app=%s = %s, want \"5 domains · 2 need you\" (elsewhere.test and nothing.invalid)", id, got)
	}
	if expectElsewhere {
		expectRow(page, elsewhere, "failed", "DNS points elsewhere", selfCert)
		row = text(domainRow(page, elsewhere))
		// The banner above already says what is wrong, so the detail says it once: the record to
		// change it to, what DNS says now and the last check (the critique's ruling 4).
		for _, want := range []string{
			"Change the record to:", "A", "@", "203.0.113.1",
			"now A · 203.0.113.9", "last check", "checks every minute"} {
			if !strings.Contains(row, want) {
				t.Errorf("the row of %s lacks %q:\n%s", elsewhere, want, row)
			}
		}
		if strings.Contains(row, "which isn't this host") {
			t.Errorf("the row of %s repeats the banner's sentence:\n%s", elsewhere, row)
		}
		if strings.Contains(domainRow(page, elsewhere), `aria-label="Open https://`+elsewhere) {
			t.Errorf("the row of %s has an open link, which would open wherever DNS points today", elsewhere)
		}
		banner := regexp.MustCompile(`(?s)<div class="cv-panel cv-dmbanner">.*?</div>\s*</div>`).FindString(page)
		if got = text(banner); !strings.Contains(got, "elsewhere.test points at 203.0.113.9, which isn't this host.") ||
			!strings.Contains(got, "Until it changes, caravana can't get a certificate and visitors won't reach cd023. Change the record where you manage DNS.") ||
			!strings.Contains(got, "Check again") || strings.Contains(got, nothing) {
			t.Errorf("the banner = %s, want only elsewhere.test's, with its explanation and Check again", got)
		}
	} else {
		expectRow(page, elsewhere, "idle", "couldn't check", selfCert)
	}
	_, page = c.get(t, "/domains?app="+id+"&open="+nothing)
	row = text(domainRow(page, nothing))
	wantValue := "203.0.113.1"
	if !expectElsewhere {
		wantValue = "this host's public IP address"
	}
	for _, want := range []string{"No DNS record yet. Add this record where you manage DNS:", "Type A Name @ Value " + wantValue} {
		if !strings.Contains(row, want) {
			t.Errorf("the row of %s lacks %q:\n%s", nothing, want, row)
		}
	}
	rows := rowsOf()
	for host, want := range map[string]struct{ state, dns string }{
		apex: {"self_signed", "ok"}, www: {"self_signed", "ok"}, rdWWW: {"self_signed", "ok"},
		nothing:   {"dns_none", "none"},
		elsewhere: {map[bool]string{true: "dns_elsewhere", false: "self_signed"}[expectElsewhere], map[bool]string{true: "elsewhere", false: "unknown"}[expectElsewhere]},
	} {
		if r := rows[host]; r.State != want.state || r.DNS.Status != want.dns {
			t.Errorf("the API's row of %s = state %s, DNS %s, want %s, %s", host, r.State, r.DNS.Status, want.state, want.dns)
		}
	}
	if expectElsewhere && !slices.Equal(rows[elsewhere].DNS.Seen, []string{"203.0.113.9"}) {
		t.Errorf("dns.seen of %s = %v, want [203.0.113.9]", elsewhere, rows[elsewhere].DNS.Seen)
	}

	// 4. Options.
	_, page = c.get(t, "/domains?open="+apex)
	// A form from before the adds is stale: refused, and nothing deployed.
	staleOn := url.Values{}
	for k, v := range stale {
		staleOn[k] = slices.Clone(v)
	}
	staleOn.Set("teamOnly", "on")
	n := c.deploymentCount(t, id)
	resp, body = c.post(t, "/domains/"+apex+"/options", staleOn)
	if got = text(body); resp.StatusCode != http.StatusConflict ||
		!strings.Contains(got, "This app changed since you opened this page. What is shown now is current: make the change again.") ||
		strings.Contains(body, "team-password-value") {
		t.Errorf("POST a stale Options form = %d: %s, want 409 with the changed-since sentence and no password", resp.StatusCode, got)
	}
	if after := c.deploymentCount(t, id); after != n {
		t.Errorf("a stale Options form made %d deployments, want none", after-n)
	}

	// Team only on: the password comes once, in this answer.
	form := optionsOf(page, apex)
	form.Set("teamOnly", "on")
	last := newest()
	resp, body = c.post(t, "/domains/"+apex+"/options", form)
	pw := ""
	if m := onceRe.FindStringSubmatch(body); m != nil {
		pw = m[1]
		passwords = append(passwords, pw)
	}
	if got = text(body); resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || pw == "" ||
		!strings.Contains(got, "Copy the password for cd023.localhost now. User name team.") {
		t.Fatalf("POST Team only on = %d (Cache-Control %q, password %v): %s, want 200 no-store with the password for %s once",
			resp.StatusCode, resp.Header.Get("Cache-Control"), pw != "", got, apex)
	}
	settled(last)
	header := c.g.waitDomainStatus(t, http.StatusUnauthorized, time.Minute, apex, http.MethodGet, "/", "", "")
	if got := header.Get("WWW-Authenticate"); got != `Basic realm="caravana"` {
		t.Errorf("WWW-Authenticate of %s = %q, want Basic realm=\"caravana\"", apex, got)
	}
	c.g.waitDomainStatus(t, http.StatusOK, 30*time.Second, apex, http.MethodGet, "/", "team", pw)
	if status, _, _, err := c.g.domainCall(apex, http.MethodGet, "/", "team", pw+"x"); err != nil || status != http.StatusUnauthorized {
		t.Errorf("%s with a wrong password = %d (%v), want 401", apex, status, err)
	}
	_, page = c.get(t, "/domains?open="+apex)
	row = domainRow(page, apex)
	if !strings.Contains(row, `title="Team only"`) || !strings.Contains(row, "New password") ||
		teamSwitchRe.FindStringSubmatch(row) == nil || teamSwitchRe.FindStringSubmatch(row)[1] != " checked" ||
		strings.Contains(page, pw) || strings.Contains(page, "team-password-value") {
		t.Errorf("the row of %s after Team only = %s, want its lock, the switch on, New password, and no password", apex, text(row))
	}

	// New password, from the app's own page.
	_, page = c.get(t, "/apps/"+id+"/domains?open="+apex)
	rotate := formsPosting(page, "/domains/"+apex+"/team-password")
	if len(rotate) != 1 || rotate[0].Get("app") != id || rotate[0].Get("page") != "app" {
		t.Fatalf("the New password form of %s = %v, want one for the app page", apex, rotate)
	}
	resp, body = c.post(t, "/domains/"+apex+"/team-password", rotate[0])
	newPw := ""
	if m := onceRe.FindStringSubmatch(body); m != nil {
		newPw = m[1]
		passwords = append(passwords, newPw)
	}
	if got = text(body); resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || newPw == "" || newPw == pw ||
		!strings.Contains(got, "Copy the new password for cd023.localhost now. User name team.") {
		t.Fatalf("POST New password = %d (Cache-Control %q, a different password %v): %s, want 200 no-store with another password once",
			resp.StatusCode, resp.Header.Get("Cache-Control"), newPw != "" && newPw != pw, got)
	}
	c.g.waitDomainStatus(t, http.StatusOK, 30*time.Second, apex, http.MethodGet, "/", "team", newPw)
	c.g.waitDomainStatus(t, http.StatusUnauthorized, 30*time.Second, apex, http.MethodGet, "/", "team", pw)
	if got := text(c.mustGet(t, "/apps/"+id+"/domains?open="+apex)); strings.Contains(got, newPw) || strings.Contains(got, "Copy the new password") {
		t.Errorf("the page after the answer that showed the new password still has it")
	}
	// A domain that isn't Team only has no password to make.
	resp, body = c.post(t, "/domains/"+www+"/team-password", url.Values{"app": {id}, "service": {id}})
	if got = text(body); resp.StatusCode != http.StatusConflict || !strings.Contains(got, "www.cd023.localhost isn't Team only, so it has no password.") {
		t.Errorf("POST New password for %s = %d: %s, want 409 saying it isn't Team only", www, resp.StatusCode, got)
	}

	// The path: a bad one is refused under its field, /api is saved.
	n = c.deploymentCount(t, id)
	_, page = c.get(t, "/domains?open="+www)
	form = optionsOf(page, www)
	form.Set("path", "api")
	resp, body = c.post(t, "/domains/"+www+"/options", form)
	if got = text(body); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(got, `"api" must start with /`) ||
		!strings.Contains(body, `name="path" value="api"`) {
		t.Errorf("POST the path api = %d: %s, want 422 under the Path field, keeping what was typed", resp.StatusCode, got)
	}
	// The redirect while the pair is a domain of the app: refused with the reason.
	form = optionsOf(page, www)
	form.Set("wwwRedirect", "on")
	resp, body = c.post(t, "/domains/"+www+"/options", form)
	if got = text(body); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(got, "The redirect can't be saved:") || !strings.Contains(got, "which is already a domain of this app") {
		t.Errorf("POST the redirect of %s, whose pair is a domain of the app = %d: %s, want 422 with the reason", www, resp.StatusCode, got)
	}
	if after := c.deploymentCount(t, id); after != n {
		t.Errorf("the refused path and redirect made %d deployments, want none", after-n)
	}
	form = optionsOf(page, www)
	form.Set("path", "/api")
	last = newest()
	resp, body = c.post(t, "/domains/"+www+"/options", form)
	if want := "/domains?open=" + www + "#domain-" + www; resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
		t.Fatalf("POST the path /api = %d → %q, want 303 to %s: %s", resp.StatusCode, resp.Header.Get("Location"), want, text(body))
	}
	settled(last)
	c.g.waitDomainStatus(t, http.StatusOK, time.Minute, www, http.MethodGet, "/api/x", "", "")
	c.g.waitDomainStatus(t, http.StatusNotFound, 30*time.Second, www, http.MethodGet, "/other", "", "")
	page = domainsPage("?app=" + id)
	if row = domainRow(page, www); !strings.Contains(row, `<span class="cv-dmpath">/api</span>`) || !strings.Contains(row, `aria-label="Open https://`+www+`/api"`) {
		t.Errorf("the row of %s after saving the path = %s, want /api beside the host and in its link", www, text(row))
	}
	if r := rowsOf()[www]; r.Path != "/api" || !strings.HasPrefix(r.Router, "cd023-cd023-") {
		t.Errorf("the API's row of %s = path %q, router %q, want /api on a router of its own", www, r.Path, r.Router)
	}

	// The redirect for the pair that isn't a domain of the app.
	_, page = c.get(t, "/domains?open="+rdWWW)
	form = optionsOf(page, rdWWW)
	if !strings.Contains(text(formMarkup(page, "/domains/"+rdWWW+"/options")), "Redirect "+rdApex+" here") {
		t.Errorf("the Options of %s have no \"Redirect %s here\" switch:\n%s", rdWWW, rdApex, text(page))
	}
	form.Set("wwwRedirect", "on")
	last = newest()
	resp, body = c.post(t, "/domains/"+rdWWW+"/options", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST the redirect of %s = %d: %s", rdWWW, resp.StatusCode, text(body))
	}
	settled(last)
	for _, call := range []struct {
		method string
		status []int
	}{{http.MethodGet, []int{http.StatusMovedPermanently, http.StatusPermanentRedirect}}, {http.MethodPost, []int{http.StatusPermanentRedirect}}} {
		var status int
		var hdr http.Header
		var err error
		waitFor(t, "the redirect of "+rdApex, time.Minute, func() bool {
			status, hdr, _, err = c.g.domainCall(rdApex, call.method, "/some/path?a=1&b=two", "", "")
			return err == nil && status != http.StatusOK && status != http.StatusNotFound
		})
		loc := hdr.Get("Location")
		if !slices.Contains(call.status, status) || (loc != "https://"+rdWWW+":8443/some/path?a=1&b=two" && loc != "https://"+rdWWW+"/some/path?a=1&b=two") {
			t.Errorf("%s https://%s/some/path?a=1&b=two = %d, Location %q, want one of %v and https://%s[:8443]/some/path?a=1&b=two", call.method, rdApex, status, loc, call.status, rdWWW)
		}
	}
	hosts = append(hosts, rdApex)
	checkAgain(rdApex)
	page = domainsPage("?app=" + id + "&open=" + rdApex)
	if row = text(domainRow(page, rdApex)); !strings.Contains(row, "→ "+rdWWW) ||
		!strings.Contains(row, "Redirects to "+rdWWW+". Change it on that domain's row.") {
		t.Errorf("the row of %s = %s, want it redirecting to %s, changed on that row", rdApex, row, rdWWW)
	}
	if r := rowsOf()[rdApex]; r.RedirectOf != rdWWW {
		t.Errorf("the API's row of %s redirects of %q, want %s", rdApex, r.RedirectOf, rdWWW)
	}

	// 5. The needs-you list, Stop and Start.
	_, page = c.get(t, "/apps")
	got = text(page)
	if expectElsewhere {
		href := `href="/domains?open=` + elsewhere + `#domain-` + elsewhere + `"`
		if !strings.Contains(got, "elsewhere.test points at 203.0.113.9, which isn't this host.") || !strings.Contains(got, "Open domain") || !strings.Contains(page, href) {
			t.Errorf("/apps doesn't say elsewhere.test points at 203.0.113.9 with Open domain (%s):\n%s", href, got)
		}
		if items := c.g.domainAttention(t); len(items) != 1 || items[0] != [3]string{"domain_dns_elsewhere", elsewhere, "203.0.113.9"} {
			t.Errorf("the domain items of /v1/attention = %v, want only domain_dns_elsewhere for %s seeing 203.0.113.9", items, elsewhere)
		}
	}
	_, page = c.get(t, "/domains?app="+id+"&open="+apex)
	stoppedForm := optionsOf(page, www)
	resp, body = c.post(t, "/apps/"+id+"/stop", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+id+"?did=stop" {
		t.Fatalf("POST stop = %d → %q, want 303 to /apps/%s?did=stop: %s", resp.StatusCode, resp.Header.Get("Location"), id, text(body))
	}
	page = domainsPage("?app=" + id)
	for _, host := range []string{apex, elsewhere, nothing, www, rdWWW, rdApex} {
		row = domainRow(page, host)
		if m := pillRe.FindStringSubmatch(row); m == nil || m[1] != "idle" || m[2] != "app stopped" {
			t.Errorf("the status of %s on a stopped app = %q, want idle \"app stopped\"", host, m)
		}
		if cm := certCellRe.FindStringSubmatch(row); cm == nil || cm[1] != "—" {
			t.Errorf("the Certificate cell of %s on a stopped app = %q, want —", host, cm)
		}
		// The redirect's row says where it redirects in its Options instead of the second sentence.
		second := "The app is stopped: start it first."
		if host == rdApex {
			second = "Redirects to " + rdWWW + "."
		}
		if got = text(row); !strings.Contains(got, "The app is stopped: start it to serve this domain.") || !strings.Contains(got, second) {
			t.Errorf("the row of %s on a stopped app = %s, want the stopped sentence and %q", host, got, second)
		}
	}
	for host, r := range rowsOf() {
		if r.State != "not_served" {
			t.Errorf("the API's state of %s on a stopped app = %s, want not_served", host, r.State)
		}
	}
	if items := c.g.domainAttention(t); len(items) != 0 {
		t.Errorf("the domain items of /v1/attention on a stopped app = %v, want none", items)
	}
	if _, page = c.get(t, "/apps"); strings.Contains(prose(page), "elsewhere.test points at") {
		t.Errorf("/apps still names elsewhere.test while its app is stopped")
	}
	resp, body = c.post(t, "/domains/"+www+"/options", stoppedForm)
	if got = text(body); resp.StatusCode != http.StatusConflict || !strings.Contains(got, "The app is stopped: start it first.") {
		t.Errorf("POST Options of a stopped app = %d: %s, want 409 with the stopped sentence", resp.StatusCode, got)
	}
	resp, body = c.post(t, "/apps/"+id+"/start", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+id+"?did=start" {
		t.Fatalf("POST start = %d → %q, want 303 to /apps/%s?did=start: %s", resp.StatusCode, resp.Header.Get("Location"), id, text(body))
	}
	waitFor(t, id+" to be healthy again", 3*time.Minute, func() bool { return c.g.appState(t, id) == "healthy" })
	c.g.waitDomainStatus(t, http.StatusOK, time.Minute, www, http.MethodGet, "/api/x", "", "")
	c.g.waitDomainStatus(t, http.StatusOK, 30*time.Second, apex, http.MethodGet, "/", "team", newPw) // the new one survives a stop
	for _, host := range hosts {
		checkAgain(host)
	}
	page = domainsPage("?app=" + id)
	expectRow(page, apex, "idle", "Traefik's own certificate", selfCert)
	if expectElsewhere {
		expectRow(page, elsewhere, "failed", "DNS points elsewhere", selfCert)
		if items := c.g.domainAttention(t); len(items) != 1 || items[0][1] != elsewhere {
			t.Errorf("the domain items of /v1/attention after Start = %v, want one for %s", items, elsewhere)
		}
	}

	// 6. Settings › Certificates.
	resp, page = c.get(t, "/settings/certificates")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings/certificates = %d: %s", resp.StatusCode, text(page))
	}
	var certs struct {
		Certificates []struct{ Host, App, Status string } `json:"certificates"`
	}
	c.g.getJSON(t, "/v1/certificates", &certs)
	issued := 0
	for _, cert := range certs.Certificates {
		if cert.Status == "issued" {
			issued++
		}
	}
	nav := regexp.MustCompile(`(?s)<a href="/settings/certificates" class="cv-snav-item"[^>]*>(.*?)</a>`).FindStringSubmatch(page)
	switch {
	case nav == nil || !strings.Contains(nav[1], "Certificates"):
		t.Errorf("Settings has no Certificates entry in its nav:\n%s", text(page))
	case issued == 0 && (strings.Contains(nav[1], "cv-snav-count") || strings.Contains(nav[1], "cv-mark")):
		t.Errorf("the Certificates entry = %s, want neither a count nor a mark: no certificate is issued in the lab", nav[1])
	case issued > 0 && !strings.Contains(nav[1], `<span class="cv-snav-count">`+strconv.Itoa(issued)+`</span>`):
		t.Errorf("the Certificates entry = %s, want the count %d", nav[1], issued)
	}
	wantLede := map[string]string{
		"production": "HTTPS certificates for every domain come from Let's Encrypt and renew 30 days before they expire.",
		"staging":    "HTTPS certificates for every domain come from Let's Encrypt and renew 30 days before they expire.",
		"none":       "This caravana was installed without Let's Encrypt: Traefik serves its own certificate.",
	}[os.Getenv("CARAVANA_ACME")]
	if wantLede == "" {
		wantLede = "Traefik issues the HTTPS certificates. This caravana was installed before it could say how: run the installer again."
	}
	got = text(page)
	for _, want := range []string{wantLede, "Domain certificates · ", "How certificates are issued", "Issuer", "Contact email",
		"Warn when 14 d before expiry", "Sends to the channels set up in Notifications."} {
		if !strings.Contains(got, want) {
			t.Errorf("Settings › Certificates lacks %q:\n%s", want, got)
		}
	}
	for _, host := range hosts {
		cert := regexp.MustCompile(`(?s)<div class="cv-certrow" id="cert-` + regexp.QuoteMeta(host) + `">(.*?)</div>`).FindStringSubmatch(page)
		if cert == nil || !strings.Contains(text(cert[1]), host) || !strings.Contains(text(cert[1]), "cd023") || !strings.Contains(text(cert[1]), "Traefik's own") {
			t.Errorf("the certificates table has no row of %s used by cd023 and issued by Traefik's own: %v", host, cert)
		}
	}
	if !strings.Contains(page, `<option value="14" selected>`) {
		t.Errorf("Warn when doesn't start at 14 d (a step before this one left it changed?):\n%s", got)
	}
	for _, days := range []string{"7", "14"} {
		resp, body = c.post(t, "/settings/certificates", url.Values{"warnBeforeDays": {days}})
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/certificates?warn=saved" {
			t.Fatalf("POST Warn when %s = %d → %q, want 303 to /settings/certificates?warn=saved: %s", days, resp.StatusCode, resp.Header.Get("Location"), text(body))
		}
		_, page = c.get(t, "/settings/certificates?warn=saved")
		if got = text(page); !strings.Contains(got, "Saved. Certificates inside that window show as expiring now.") ||
			!strings.Contains(got, "Warn when "+days+" d before expiry") || !strings.Contains(page, `<option value="`+days+`" selected>`) {
			t.Errorf("Settings › Certificates after saving %s = %s, want the notice and %s d", days, got, days)
		}
	}
	resp, body = c.post(t, "/settings/certificates", url.Values{"warnBeforeDays": {"5"}})
	if got = text(body); resp.StatusCode >= 300 && resp.StatusCode < 400 || resp.StatusCode >= 500 || !strings.Contains(got, "Warn when must be 7, 14 or 30 days") ||
		!strings.Contains(body, "<details class=\"cv-confirm cv-certedit\" open>") {
		t.Errorf("POST Warn when 5 = %d: %s, want the page again (no redirect) saying it must be 7, 14 or 30 days, with Edit open", resp.StatusCode, got)
	}

	// 7. Remove elsewhere.test.
	_, page = c.get(t, "/domains?open="+elsewhere)
	remove := formsPosting(page, "/domains/"+elsewhere+"/remove")
	if len(remove) != 1 || remove[0].Get("app") != id || remove[0].Get("service") != id || remove[0].Get("revision") == "" {
		t.Fatalf("the Remove form of %s = %v, want the app, its service and a revision:\n%s", elsewhere, remove, text(domainRow(page, elsewhere)))
	}
	resp, body = c.post(t, "/domains/"+elsewhere+"/remove", remove[0])
	c.deployedFrom(t, id, resp, body)
	page = domainsPage("?app=" + id)
	if domainRow(page, elsewhere) != "" || strings.Contains(page, `title="`+elsewhere+`"`) {
		t.Errorf("/domains still lists %s after the remove:\n%s", elsewhere, text(page))
	}
	if got = text(page); !strings.Contains(got, "5 domains · 1 needs you") {
		t.Errorf("the count line after the remove = %s, want \"5 domains · 1 needs you\" (nothing.invalid)", got)
	}
	if items := c.g.domainAttention(t); len(items) != 0 {
		t.Errorf("the domain items of /v1/attention after the remove = %v, want none", items)
	}
	n = c.deploymentCount(t, id)
	resp, body = c.post(t, "/domains/"+elsewhere+"/remove", remove[0])
	if got = text(body); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(got, "This app has no domain elsewhere.test on that service.") {
		t.Errorf("POST the same Remove form again = %d: %s, want 422 saying the app has no such domain", resp.StatusCode, got)
	}
	if after := c.deploymentCount(t, id); after != n {
		t.Errorf("removing a domain that is gone made %d deployments, want none", after-n)
	}

	// 8. No password and no hash anywhere after the answers that showed them.
	secrets := passwords
	answers := map[string]string{}
	for _, p := range []string{"/domains", "/domains?app=" + id, "/domains?open=" + apex, "/parts/domains", "/parts/domains?app=" + id + "&page=app",
		"/apps", "/apps/" + id, "/apps/" + id + "/domains?open=" + apex, "/apps/" + id + "/deployments", "/apps/" + id + "/configure",
		"/apps/" + id + "/running", "/parts/apps/" + id + "/actions", "/settings/certificates", "/settings/tokens", "/settings/notifications"} {
		resp, page := c.get(t, p)
		if resp.StatusCode == http.StatusOK {
			answers["GET "+p] = page
		}
	}
	for _, p := range []string{"/v1/apps/" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id + "/jobspec", "/v1/apps/" + id + "/actions",
		"/v1/domains", "/v1/domains/" + apex, "/v1/certificates", "/v1/attention", "/v1/apps", "/v1/settings/certificates"} {
		status, raw := c.g.send(t, http.MethodGet, p, "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d", p, status)
			continue
		}
		answers["GET "+p] = string(raw)
	}
	var deps struct {
		Deployments []gateDeployment `json:"deployments"`
	}
	c.g.getJSON(t, "/v1/apps/"+id+"/deployments", &deps)
	for _, d := range deps.Deployments {
		status, raw := c.g.send(t, http.MethodGet, "/v1/apps/"+id+"/deployments/"+d.ID+"/jobspec", "")
		if status == http.StatusOK {
			answers["the jobspec of deployment "+d.ID] = string(raw)
		}
	}
	if job, _, err := c.g.target.Nomad.Jobs().Info(id, nil); err == nil {
		answers["the Nomad job"] = string(mustJSON(t, job))
	} else {
		t.Errorf("read the job %s: %v", id, err)
	}
	if v, _, err := c.g.target.Nomad.Variables().Read("nomad/jobs/traefik", nil); err == nil {
		items := 0
		for name, value := range v.Items {
			if strings.Contains(name, id) {
				items++
			}
			answers["Traefik's variable item "+name] = value
		}
		if items == 0 {
			t.Errorf("nomad/jobs/traefik holds no item of %s: Team only should be rendered there", id)
		}
		answers["Traefik's variable item names"] = strings.Join(slices.Sorted(mapKeys(v.Items)), "\n")
	} else {
		t.Errorf("read nomad/jobs/traefik: %v", err)
	}
	if c.g.target.ServerLog != nil {
		answers["Caravana's log"] = c.g.target.ServerLog()
	}
	for where, answer := range answers {
		for _, secret := range secrets {
			if strings.Contains(answer, secret) {
				t.Errorf("%s holds a Team-only password", where)
			}
		}
		// The hash is in Traefik's variable by design; nowhere else.
		if strings.HasPrefix(where, "Traefik's variable item ") && !strings.HasSuffix(where, "names") {
			continue
		}
		for _, hash := range []string{"$2a$", "$2b$", "$2y$"} {
			if strings.Contains(answer, hash) {
				t.Errorf("%s holds a bcrypt hash (%s…)", where, hash)
			}
		}
	}
	if dir := c.g.target.DataDir; dir != "" {
		files := 0
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			files++
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, secret := range secrets {
				if strings.Contains(string(data), secret) {
					t.Errorf("%s holds a Team-only password", path)
				}
			}
			return nil
		})
		if err != nil || files == 0 {
			t.Errorf("read the data directory %s: %d files, %v", dir, files, err)
		}
	}

	// The app goes through its form, and its items leave Traefik's variable.
	if resp, body = c.post(t, "/apps/"+id+"/delete", url.Values{"confirm": {id}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("POST the delete of %s = %d: %s", id, resp.StatusCode, text(body))
	}
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	waitFor(t, id+"'s items to leave nomad/jobs/traefik", time.Minute, func() bool {
		v, _, err := c.g.target.Nomad.Variables().Read("nomad/jobs/traefik", nil)
		if err != nil {
			return true // no variable, nothing of ours in it
		}
		for name := range v.Items {
			if strings.Contains(name, id) {
				return false
			}
		}
		return true
	})
}

// mustGet is c.get for a page the step reads only for its text.
func (c *console) mustGet(t *testing.T, path string) string {
	t.Helper()
	_, page := c.get(t, path)
	return page
}
