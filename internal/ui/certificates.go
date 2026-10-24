package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Settings › Certificates (feature 023, contracts/domains-screen.md): the
// certificate each domain's last check found, problems first, and how
// certificates are issued. The issuer and the contact email are Traefik's
// static configuration, set by the installer, so they are read-only here; the
// one editable setting is "Warn when" (7, 14 or 30 days before expiry), which
// service.SetWarnBeforeDays judges the stored checks by again at once. Nothing
// here dials out: the table reads the stored checks (service.Certificates).

// warnChoices are the days "Warn when" may take (store.WarnBeforeDays).
var warnChoices = []int{7, 14, 30}

// certificatesData is the Certificates page.
type certificatesData struct {
	// Lede is the page's sentence under its title: it depends on the issuer.
	Lede string
	// Banners are the certificates that are expiring, at most maxDomainBanners.
	Banners []domainBanner
	More    *domainMore
	// Count is "Domain certificates · 12 certificates".
	Count string
	Rows  []certificateRow
	// The issuer's rows, read-only, and the editable one.
	Issuer, IssuerHint string
	Email, EmailHint   string
	WarnDays           int
	Warn               []formOption
	// Notice is what a saved form says; Alert what a refused one says.
	Notice string
	Alert  string
	// Open says the Edit form is drawn open: it was refused.
	Open bool
}

// certificateRow is one certificate of the table.
type certificateRow struct {
	Mark   string
	Host   string
	App    string
	Issuer string
	Expiry domainCertCell
	Href   string // the domain's row on /domains
}

// certificateRowOf says a certificate in the table's words. The expiry column
// says "waiting for DNS" for a certificate that is still pending, and "—" for
// Traefik's own, which has no renewal to tell.
func certificateRowOf(now time.Time, c service.CertificateView) certificateRow {
	row := certificateRow{Mark: certRowMark(c.Status), Host: c.Host, App: c.App, Issuer: c.Issuer, Href: domainHref(c.Host)}
	if row.Issuer == "" {
		row.Issuer = "—"
	}
	cert := edge.Certificate{Status: c.Status, Issuer: c.Issuer}
	if c.NotAfter != nil {
		cert.NotAfter = *c.NotAfter
	}
	if c.RenewsAt != nil {
		cert.RenewsAt = *c.RenewsAt
	}
	row.Expiry = certCellFor(now, cert)
	switch c.Status {
	case edge.CertPending:
		row.Expiry = domainCertCell{Text: "waiting for DNS"}
	case edge.CertSelf:
		row.Expiry = domainCertCell{Text: "—"} // the Issuer column says it already
	}
	return row
}

// certificatesLede is the page's sentence, by how certificates are issued.
func certificatesLede(issuer string) string {
	switch issuer {
	case service.IssuerNone:
		return "This caravana was installed without Let's Encrypt: Traefik serves its own certificate."
	case service.IssuerUnknown:
		return "Traefik issues the HTTPS certificates. This caravana was installed before it could say how: run the installer again."
	}
	return "HTTPS certificates for every domain come from Let's Encrypt and renew 30 days before they expire."
}

// issuerWords is the Issuer row: its value and its note.
func issuerWords(issuer string) (value, note string) {
	switch issuer {
	case service.IssuerLetsEncrypt:
		return "Let's Encrypt", "Certificates are issued and renewed for you. Set when caravana was installed."
	case service.IssuerLetsEncryptStaging:
		return "Let's Encrypt (staging)", "Staging certificates aren't trusted by browsers. Set when caravana was installed."
	case service.IssuerNone:
		return "none", "Traefik serves its own certificate. Set when caravana was installed."
	}
	return "not known", "Run the installer again so caravana knows."
}

// certificatesSection fills in the page from the facts (the table) and the
// settings. A refused form keeps its notice, alert and open state.
func (s *server) certificatesSection(ctx context.Context, now time.Time, f settingsFacts, form certificatesData) (certificatesData, error) {
	cs, err := s.svc.CertificateSettings(ctx)
	if err != nil {
		return form, err
	}
	data := form
	data.Lede = certificatesLede(cs.Issuer)
	data.Issuer, data.IssuerHint = issuerWords(cs.Issuer)
	data.Email, data.EmailHint = cs.ContactEmail, "Gets expiry warnings from the issuer. Set when caravana was installed; run the installer again to change it."
	if data.Email == "" {
		data.Email = "not set"
	}
	data.WarnDays = cs.WarnBeforeDays
	for _, d := range warnChoices {
		data.Warn = append(data.Warn, formOption{Value: strconv.Itoa(d), Label: strconv.Itoa(d) + " d before expiry", Selected: d == cs.WarnBeforeDays})
	}
	data.Count = "Domain certificates · " + plural(len(f.Certs), "certificate")
	for _, c := range f.Certs {
		data.Rows = append(data.Rows, certificateRowOf(now, c))
		if c.Status != edge.CertExpiring {
			continue
		}
		var notAfter time.Time
		if c.NotAfter != nil {
			notAfter = *c.NotAfter
		}
		v := service.DomainView{Host: c.Host, State: edge.StateCertExpiring, Certificate: edge.Certificate{Status: c.Status, NotAfter: notAfter}}
		b, ok := bannerOf(now, v, domainCheckForm{Action: "/domains/" + url.PathEscape(c.Host) + "/check"})
		if !ok {
			continue
		}
		if len(data.Banners) < maxDomainBanners {
			data.Banners = append(data.Banners, b)
		} else if data.More == nil {
			data.More = &domainMore{N: 1, Href: "#cert-" + c.Host}
		} else {
			data.More.N++
		}
	}
	return data, nil
}

// showCertificates answers GET /settings/certificates.
func (s *server) showCertificates(w http.ResponseWriter, r *http.Request, session string) {
	data := settingsData{}
	if r.URL.Query().Get("warn") == "saved" {
		data.Certificates.Notice = "Saved. Certificates inside that window show as expiring now."
	}
	s.renderSettings(w, r, entryCertificates, data)
}

// setCertificates answers POST /settings/certificates: the Warn when form.
// A day count that isn't 7, 14 or 30 draws the page again with the Edit form
// open, as the other Settings forms draw a refusal.
func (s *server) setCertificates(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("warnBeforeDays")))
	if err != nil {
		err = app.NewFieldError("warnBeforeDays", "must be 7, 14 or 30 days")
	} else {
		err = s.svc.SetWarnBeforeDays(r.Context(), days)
	}
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/certificates?warn=saved", http.StatusSeeOther)
	case errors.As(err, &fieldErr):
		data := settingsData{}
		data.Certificates.Alert, data.Certificates.Open = "Warn when "+fieldErr.Message+".", true
		s.renderSettings(w, r, entryCertificates, data)
	default:
		s.serverError(w, "set the certificate warning window", err)
	}
}
