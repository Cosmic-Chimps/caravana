package ui

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 023: Settings › Certificates.

// The count-or-mark rule of the entry: one function says it for the nav and for the rows.
func TestCertificatesEntryRule(t *testing.T) {
	cv := func(status string) service.CertificateView {
		return service.CertificateView{Host: status + ".example.com", Status: status}
	}
	for _, tc := range []struct {
		name  string
		certs []service.CertificateView
		rows  string
		mark  string
		count int
	}{
		{"none", nil, "", "", 0},
		{"one issued", []service.CertificateView{cv(edge.CertIssued)}, "healthy", "", 1},
		{"Traefik's own is idle and not counted", []service.CertificateView{cv(edge.CertSelf)}, "idle", "", 0},
		{"pending is rolling and not counted", []service.CertificateView{cv(edge.CertPending)}, "rolling", "", 0},
		{"a failed handshake is idle", []service.CertificateView{cv(edge.CertError)}, "idle", "", 0},
		{"an expiring one is the nav's mark", []service.CertificateView{cv(edge.CertExpiring)}, "warning", "warning", 0},
		{"the mark wins over the count of issued ones", []service.CertificateView{cv(edge.CertExpiring), cv(edge.CertIssued), cv(edge.CertIssued), cv(edge.CertSelf)},
			"warning,healthy,healthy,idle", "warning", 2},
		{"issued ones are counted", []service.CertificateView{cv(edge.CertIssued), cv(edge.CertPending), cv(edge.CertIssued)}, "healthy,rolling,healthy", "", 2},
	} {
		got := certificatesState(tc.certs)
		if strings.Join(got.Rows, ",") != tc.rows || got.Mark != tc.mark || got.Count != tc.count {
			t.Errorf("%s: %+v, want rows %q mark %q count %d", tc.name, got, tc.rows, tc.mark, tc.count)
		}
	}
}

// The Label over the table counts certificates that exist, as the nav does; with none it says
// how many domains there are and why (the lab's every row is Traefik's own).
func TestCertificatesHead(t *testing.T) {
	cv := func(status string) service.CertificateView {
		return service.CertificateView{Host: status + ".example.com", Status: status}
	}
	for _, tc := range []struct {
		name   string
		issuer string
		certs  []service.CertificateView
		want   string
	}{
		{"no domains", service.IssuerLetsEncrypt, nil, "Domain certificates · 0 certificates"},
		{"issued ones only", service.IssuerLetsEncrypt, []service.CertificateView{cv(edge.CertIssued), cv(edge.CertSelf), cv(edge.CertPending), cv(edge.CertIssued)}, "Domain certificates · 2 certificates"},
		{"an expiring one is a certificate", service.IssuerLetsEncrypt, []service.CertificateView{cv(edge.CertExpiring), cv(edge.CertSelf)}, "Domain certificates · 1 certificate"},
		{"none issued yet", service.IssuerLetsEncrypt, []service.CertificateView{cv(edge.CertPending), cv(edge.CertSelf)}, "Domain certificates · 2 domains · none issued yet"},
		{"one domain, none issued", service.IssuerUnknown, []service.CertificateView{cv(edge.CertError)}, "Domain certificates · 1 domain · none issued yet"},
		{"installed without Let's Encrypt", service.IssuerNone, []service.CertificateView{cv(edge.CertSelf), cv(edge.CertSelf), cv(edge.CertSelf), cv(edge.CertSelf), cv(edge.CertSelf), cv(edge.CertSelf), cv(edge.CertSelf), cv(edge.CertSelf)},
			"Domain certificates · 8 domains · no certificates: installed without Let's Encrypt"},
	} {
		if got := certificatesHead(tc.issuer, tc.certs); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The lede says how certificates are issued, for the three issuers.
func TestCertificatesLede(t *testing.T) {
	for issuer, want := range map[string]string{
		service.IssuerLetsEncrypt:        "HTTPS certificates for every domain come from Let's Encrypt and renew 30 days before they expire.",
		service.IssuerLetsEncryptStaging: "Certificates come from Let's Encrypt's staging issuer: browsers don't trust them. Run the installer with the real issuer when the domains are ready.",
		service.IssuerNone:               "This caravana was installed without Let's Encrypt: Traefik serves its own certificate.",
	} {
		if got := certificatesLede(issuer); got != want {
			t.Errorf("%s: %q", issuer, got)
		}
	}
}

// navCertificates is the Certificates entry of the nav: its count or mark.
func navCertificates(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<a href="/settings/certificates" class="cv-snav-item"[^>]*>(.*?)</a>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no Certificates entry in the nav:\n%s", text(body))
	}
	return m[1]
}

func TestCertificatesPage(t *testing.T) {
	e := statesEnv(t)
	e.Svc.SetACME("production", "ops@example.com")
	body := e.pageOf(t, "/settings/certificates")
	got := text(body)
	for _, want := range []string{
		"Certificates", "HTTPS certificates for every domain come from Let's Encrypt and renew 30 days before they expire.",
		// The expiring one has a banner, as /domains draws it.
		"expiring.example.com expires in 6 days and hasn't been renewed.", "Let's Encrypt renews 30 days before expiry",
		"Domain certificates · 2 certificates",
		"How certificates are issued",
		"Issuer Let's Encrypt Certificates are issued and renewed for you.", "Contact email ops@example.com Gets expiry warnings from the issuer.", "Warn when 14 d before expiry", "Sends to the channels set up in Notifications.",
		"Set when caravana was installed; run the installer again to change it.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	// The nav shows the mark of an expiring certificate, not the count.
	if nav := navCertificates(t, body); !strings.Contains(nav, "cv-mark-warning") || strings.Contains(nav, "cv-snav-count") {
		t.Errorf("the nav's entry = %s", nav)
	}

	// The table: problems first (expiring), then the handshakes that failed, pending, issued and Traefik's own (the service's order).
	rows := regexp.MustCompile(`(?s)<div class="cv-certrow" id="cert-([^"]+)">(.*?)</div>`).FindAllStringSubmatch(body, -1)
	var hosts []string
	byHost := map[string]string{}
	for _, m := range rows {
		hosts = append(hosts, m[1])
		byHost[m[1]] = text(m[2])
	}
	want := []string{"expiring.example.com", "elsewhere.example.com", "none.example.com", "pending.example.com", "live.example.com", "self.example.com"}
	if len(hosts) != len(want) {
		t.Fatalf("rows = %v, want %v", hosts, want)
	}
	for i := range want {
		if hosts[i] != want[i] {
			t.Fatalf("rows = %v, want %v", hosts, want)
		}
	}
	for host, wants := range map[string][]string{
		"expiring.example.com": {"shop", "Let's Encrypt", "expires in 6 d"},
		"live.example.com":     {"shop", "Let's Encrypt", "renews in 61 d"},
		"pending.example.com":  {"Traefik's own", "waiting for DNS"},
		"self.example.com":     {"Traefik's own", "—"},
	} {
		for _, w := range wants {
			if !strings.Contains(byHost[host], w) {
				t.Errorf("%s's row = %q, lacks %q", host, byHost[host], w)
			}
		}
	}
	if !strings.Contains(body, `cv-certexp cv-certexp-warn`) {
		t.Error("the expiring certificate isn't in the warn hue")
	}
	// A row's domain links to its row on /domains.
	if !strings.Contains(body, `<a href="/domains?open=live.example.com#domain-live.example.com" class="cv-certhost"`) {
		t.Error("a certificate's domain doesn't link to its row on /domains")
	}
	// Edit holds Warn when alone: a select of 7, 14 and 30, 14 chosen.
	form := regexp.MustCompile(`(?s)<form method="post" action="/settings/certificates".*?</form>`).FindString(body)
	if form == "" || !strings.Contains(form, `<option value="14" selected>`) || strings.Count(form, "<option") != 3 || strings.Contains(form, `type="text"`) || strings.Contains(form, `type="email"`) {
		t.Errorf("the Edit form = %s", form)
	}
	if strings.Contains(body, "Edit how certificates are issued") && strings.Contains(body, `class="cv-confirm cv-certedit" open`) {
		t.Error("the Edit form is open without a refusal")
	}
}

// With no certificate expiring, the nav counts the issued ones and there is no banner.
func TestCertificatesCountInTheNav(t *testing.T) {
	e := newConsoleEnv(t)
	hosts := []string{"a.example.com", "b.example.com"}
	e.seed(t, domainsApp("shop", hosts...), seedDeploy{ID: "d1", Trigger: "manual", Status: store.StatusHealthy, Version: 1})
	for _, h := range hosts {
		e.seedCheck(t, checkSeed{host: h, app: "shop", dns: edge.DNSOK, seen: []string{"203.0.113.1"}, expected: []string{"203.0.113.1"},
			cert: edge.CertIssued, issuer: "Let's Encrypt", notAfterFromNow: 80 * day})
	}
	body := e.pageOf(t, "/settings")
	if nav := navCertificates(t, body); !strings.Contains(nav, `<span class="cv-snav-count">2</span>`) {
		t.Errorf("the nav's entry = %s", nav)
	}
	if page := e.pageOf(t, "/settings/certificates"); strings.Contains(page, "cv-dmbanner") || !strings.Contains(text(page), "Domain certificates · 2 certificates") {
		t.Errorf("the page:\n%s", text(page))
	}
}

// Nothing issued yet: the empty state and no count.
func TestCertificatesEmpty(t *testing.T) {
	e := newConsoleEnv(t)
	body := e.pageOf(t, "/settings/certificates")
	if got := text(body); !strings.Contains(got, "Domain certificates · 0 certificates") || !strings.Contains(got, "No certificates yet.") {
		t.Errorf("the empty page:\n%s", got)
	}
	if nav := navCertificates(t, body); strings.Contains(nav, "cv-snav-count") || strings.Contains(nav, "cv-mark") {
		t.Errorf("the nav's entry = %s", nav)
	}
}

// The Edit form saves Warn when; the cached checks are judged again at once.
func TestCertificatesWarnWhen(t *testing.T) {
	e := statesEnv(t)
	// 6 days from the end: inside 7, so it stays expiring; move the window to 7 and a 91-day one is still issued.
	resp, _ := e.postForm(t, "/settings/certificates", url.Values{"warnBeforeDays": {"7"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/certificates?warn=saved" {
		t.Fatalf("POST = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	body := e.pageOf(t, "/settings/certificates?warn=saved")
	got := text(body)
	if !strings.Contains(got, "Saved. Certificates inside that window show as expiring now.") || !strings.Contains(got, "Warn when 7 d before expiry") || !strings.Contains(body, `<option value="7" selected>`) {
		t.Errorf("after saving 7:\n%s", got)
	}
	if cs, err := e.Svc.CertificateSettings(t.Context()); err != nil || cs.WarnBeforeDays != 7 {
		t.Errorf("settings = %+v, %v", cs, err)
	}
	// A window of 30 days: the live certificate (91 days) isn't inside it; the expiring one still is.
	if resp, _ := e.postForm(t, "/settings/certificates", url.Values{"warnBeforeDays": {"30"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST 30 = %d", resp.StatusCode)
	}
	if got := text(e.pageOf(t, "/settings/certificates")); !strings.Contains(got, "Warn when 30 d before expiry") {
		t.Errorf("after saving 30:\n%s", got)
	}

	// Anything else is refused with the form open and nothing changed.
	for _, v := range []string{"15", "", "soon", "-7"} {
		resp, body := e.postForm(t, "/settings/certificates", url.Values{"warnBeforeDays": {v}})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("warnBeforeDays=%q = %d", v, resp.StatusCode)
		}
		if !strings.Contains(text(body), "Warn when must be 7, 14 or 30 days") || !strings.Contains(body, `class="cv-confirm cv-certedit" open`) {
			t.Errorf("warnBeforeDays=%q: no refusal with the form open:\n%s", v, text(body))
		}
	}
	if cs, _ := e.Svc.CertificateSettings(t.Context()); cs.WarnBeforeDays != 30 {
		t.Errorf("a refused value changed the window to %d", cs.WarnBeforeDays)
	}
}

// The page reads no secret and has no other form: the issuer and the email are read-only.
func TestCertificatesReadOnlyRows(t *testing.T) {
	e := statesEnv(t)
	body := e.pageOf(t, "/settings/certificates")
	if n := strings.Count(body, `action="/settings/certificates"`); n != 1 {
		t.Errorf("forms posting to /settings/certificates = %d, want 1", n)
	}
	for _, name := range []string{`name="issuer"`, `name="email"`, `name="contactEmail"`} {
		if strings.Contains(body, name) {
			t.Errorf("the page has an input %s: the issuer's rows are read-only", name)
		}
	}
}

// The Edit control of "How certificates are issued" is drawn in every state: whatever the issuer,
// with certificates or none, and its form holds Warn when alone.
func TestCertificatesEditIsAlwaysDrawn(t *testing.T) {
	for _, mode := range []string{"", "production", "staging", "none"} {
		for _, withDomains := range []bool{false, true} {
			e := newConsoleEnv(t)
			if mode != "" {
				e.Svc.SetACME(mode, "ops@example.com")
			}
			if withDomains {
				e.seed(t, domainsApp("shop", "a.example.com"), seedDeploy{ID: "d1", Trigger: "manual", Status: store.StatusHealthy, Version: 1})
				e.seedCheck(t, checkSeed{host: "a.example.com", app: "shop", dns: edge.DNSOK, seen: []string{"203.0.113.1"}, expected: []string{"203.0.113.1"}, cert: edge.CertSelf, issuer: edge.TraefikOwn})
			}
			body := e.pageOf(t, "/settings/certificates")
			head := regexp.MustCompile(`(?s)<div class="cv-certsechead">.*?</details>`).FindString(body)
			if !strings.Contains(head, "<summary") || !strings.Contains(head, ">Edit</summary>") || !strings.Contains(head, `name="warnBeforeDays"`) {
				t.Errorf("acme %q, domains %v: the section head has no Edit:\n%s", mode, withDomains, head)
			}
		}
	}
}
