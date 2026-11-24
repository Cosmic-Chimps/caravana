package ui

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
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
	// Count is the Label over the table (certificatesHead).
	Count string
	Rows  []certificateRow
	// The issuer's rows, read-only, and the editable one.
	Issuer, IssuerHint string
	Email, EmailHint   string
	WarnDays           int
	Warn               []formOption
	// WarnLocked says Warn when is drawn disabled: installed without Let's Encrypt, nothing is
	// issued, so there is nothing to warn about yet. The form posts nothing; the API still takes it.
	WarnLocked bool
	// Notice is what a saved form says; Alert what a refused one says.
	Notice string
	Alert  string
	// Open says the Edit form is drawn open: it was refused.
	Open bool

	// Uploaded certificates (feature 034). The form values come from a refused post (UploadOpen,
	// UploadCert, the two errors and the alert); the key is never one of them.
	Uploads        []uploadedRow
	UploadsHead    string
	UploadWhy      string // why uploads are unavailable, "" when they are
	UploadOpen     bool
	UploadCert     string
	UploadCertErr  string
	UploadKeyErr   string
	UploadAlert    string
	UploadedNotice string // what a saved upload or a removal says
	// UploadedID is the certificate a saved upload made (its names are in the notice); UploadReplaced says it
	// replaced one with the same names.
	UploadedID     string
	UploadReplaced bool
	// IssuerSaved draws the notice of a saved issuer, which names the issuer.
	IssuerSaved bool

	// The issuer. IssuerValue and IssuerNote are its row; Issue is the Change issuer form.
	IssuerValue string
	IssuerNote  string
	IssuerExtra string // "EAB key ID abc", for a custom issuer that has one
	Issue       issuerForm
}

// issuerForm is the Change issuer form: what is drawn in it and what a refused post puts back.
type issuerForm struct {
	// Why is the sentence that says the issuer can't be changed here, "" when it can.
	Why  string
	Open bool
	// Choice is "installer" or "custom".
	Choice string
	// The values. A refused post keeps all of them but the HMAC key, which no page draws.
	Server, Email, EABKeyID, CA string
	RemoveCA                    bool
	// EABKeySet says a key is stored (the hint says leaving the field empty keeps it); HasCA that a CA is.
	EABKeySet, HasCA bool
	Installer        string // the installer's issuer in words, for the radio
	Presets          []issuerPreset
	Errs             map[string]string
	Alert            string
	// Set says the values came from a post or a preset link, so the stored ones don't fill them.
	Set bool
}

// issuerPreset is an issuer the form offers by name: the link draws the form open with its address.
type issuerPreset struct {
	Name string
	Href string
}

// uploadedRow is one uploaded certificate of the page.
type uploadedRow struct {
	ID    string
	Mark  string
	Names []string // the first three
	More  int      // how many more names there are
	// AllNames is every name, for the title.
	AllNames  string
	Issuer    string
	Expiry    domainCertCell
	KeyType   string
	Untrusted bool
	Serves    sentence
	Kept      sentence
	// Back says where the hosts go when the certificate is removed.
	Back string
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

// certificateRowOf says a certificate in the table's words: its mark and its expiry are the
// state table's (domainstates.go), so a pending certificate says "waiting for Let's Encrypt"
// here as in the list, and Traefik's own, whose Issuer column says it already, has "—" to tell.
func certificateRowOf(now time.Time, c service.CertificateView) certificateRow {
	row := certificateRow{Mark: certRowMark(c.Status), Host: c.Host, App: c.App, Issuer: c.Issuer, Href: domainHref(c.Host)}
	if c.Uploaded != nil {
		row.Issuer = "uploaded: " + c.Uploaded.Subject
	}
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
	if words := stateInfo(certState(c.Status)).Expiry; words != "" {
		row.Expiry = domainCertCell{Text: words}
	} else {
		row.Expiry = certCellFor(now, cert)
	}
	return row
}

// certificatesHead is the Label over the table. It counts the certificates
// that exist, as the nav does (Traefik's own and pending ones aren't
// certificates yet; an expiring one still is): "Domain certificates · 12
// certificates". With none, it says how many domains there are and why:
// installed without Let's Encrypt, or nothing issued yet.
func certificatesHead(issuer string, certs []service.CertificateView) string {
	const head = "Domain certificates · "
	issued := 0
	for _, c := range certs {
		if c.Status == edge.CertIssued || c.Status == edge.CertExpiring {
			issued++
		}
	}
	switch {
	case len(certs) == 0:
		return head + "0 certificates"
	case issued > 0:
		return head + plural(issued, "certificate")
	case issuer == service.IssuerNone:
		return head + plural(len(certs), "domain") + " · no certificates: installed without Let's Encrypt"
	}
	return head + plural(len(certs), "domain") + " · none issued yet"
}

// certificatesLedeFor is the page's sentence for the installation's settings: the lede of the
// issuer, with the custom issuer's name, and one more sentence when certificates are uploaded.
func certificatesLedeFor(cs service.CertificateSettings, uploads int) string {
	lede := certificatesLede(cs.Issuer)
	if cs.Issuer == service.IssuerCustom {
		lede = "HTTPS certificates for every domain come from " + cs.IssuerName + " and renew 30 days before they expire."
	}
	if uploads > 0 {
		lede += " Hosts an uploaded certificate covers use it instead."
	}
	return lede
}

// certificatesLede is the page's sentence, by how certificates are issued.
func certificatesLede(issuer string) string {
	switch issuer {
	case service.IssuerNone:
		return "This caravana was installed without Let's Encrypt: Traefik serves its own certificate."
	case service.IssuerLetsEncryptStaging:
		return "Certificates come from Let's Encrypt's staging issuer: browsers don't trust them. Run the installer with the real issuer when the domains are ready."
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

// issuerRowOf is the Issuer row of the settings: its value, its note and, for a custom issuer
// that has one, its External Account Binding key ID.
func issuerRowOf(cs service.CertificateSettings) (value, note, extra string) {
	if cs.Issuer != service.IssuerCustom || cs.ACME == nil {
		value, note = issuerWords(cs.Issuer)
		return value, note, ""
	}
	if cs.ACME.EABKeyID != "" {
		extra = "EAB key ID " + cs.ACME.EABKeyID
	}
	return cs.IssuerName, cs.ACME.Server + ". Set here.", extra
}

// certificatesSection fills in the page from the facts (the table) and the
// settings. A refused form keeps its notice, alert and open state.
func (s *server) certificatesSection(ctx context.Context, now time.Time, f settingsFacts, form certificatesData) (certificatesData, error) {
	cs, err := s.svc.CertificateSettings(ctx)
	if err != nil {
		return form, err
	}
	uploaded, err := s.svc.UploadedCertificates(ctx)
	if err != nil {
		return form, err
	}
	data := form
	data.Lede = certificatesLedeFor(cs, len(uploaded))
	data.Issuer, data.IssuerHint, data.IssuerExtra = issuerRowOf(cs)
	data.Email, data.EmailHint = cs.ContactEmail, "Gets expiry warnings from the issuer. Set when caravana was installed; run the installer again to change it."
	if cs.Issuer == service.IssuerCustom {
		data.EmailHint = "Gets expiry warnings from the issuer. Set here."
	}
	if data.Email == "" {
		data.Email = "not set"
	}
	s.uploadsSection(&data, now, cs, uploaded)
	issuerFormOf(&data, cs)
	if data.IssuerSaved {
		where := cs.IssuerName
		if where == "" {
			where = "the installer's issuer"
		}
		data.Notice = "Saved. Traefik is restarting; domains get their certificates from " + where + " as they are asked for."
	}
	data.WarnDays = cs.WarnBeforeDays
	data.WarnLocked = cs.Issuer == service.IssuerNone
	for _, d := range warnChoices {
		data.Warn = append(data.Warn, formOption{Value: strconv.Itoa(d), Label: strconv.Itoa(d) + " d before expiry", Selected: d == cs.WarnBeforeDays})
	}
	data.Count = certificatesHead(cs.Issuer, f.Certs)
	for _, c := range f.Certs {
		row := certificateRowOf(now, c)
		row.Expiry.Text = withIssuer(row.Expiry.Text, cs.IssuerName)
		data.Rows = append(data.Rows, row)
		if c.Status != edge.CertExpiring {
			continue
		}
		var notAfter time.Time
		if c.NotAfter != nil {
			notAfter = *c.NotAfter
		}
		v := service.DomainView{Host: c.Host, State: edge.StateCertExpiring, Certificate: edge.Certificate{Status: c.Status, NotAfter: notAfter}, Uploaded: c.Uploaded}
		b, ok := bannerOf(now, cs.IssuerName, v, domainCheckForm{Action: "/domains/" + url.PathEscape(c.Host) + "/check"})
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

// showCertificates answers GET /settings/certificates. ?preset=<key> draws the Change issuer form
// open with that issuer's address filled in.
func (s *server) showCertificates(w http.ResponseWriter, r *http.Request, session string) {
	data := settingsData{}
	q := r.URL.Query()
	switch {
	case q.Get("warn") == "saved":
		data.Certificates.Notice = "Saved. Certificates inside that window show as expiring now."
	case q.Get("issuer") == "saved":
		data.Certificates.IssuerSaved = true
	case q.Get("upload") == "saved", q.Get("upload") == "replaced":
		data.Certificates.UploadedID, data.Certificates.UploadReplaced = q.Get("certificate"), q.Get("upload") == "replaced"
		data.Certificates.UploadedNotice = "Uploaded."
	case q.Get("upload") == "removed":
		data.Certificates.UploadedNotice = "Removed. Its hosts go back to their earlier certificate once Traefik has noticed."
	}
	if key := q.Get("preset"); key != "" {
		for _, p := range service.ACMEPresets {
			if p.Key == key {
				data.Certificates.Issue = issuerForm{Open: true, Set: true, Choice: "custom", Server: p.Server}
			}
		}
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
		s.serverError(w, r, "set the certificate warning window", err)
	}
}

// ---- Uploaded certificates -------------------------------------------------------------

// uploadsSection fills the Uploaded certificates section: its rows, its count and why uploads
// are unavailable when they are.
func (s *server) uploadsSection(data *certificatesData, now time.Time, cs service.CertificateSettings, list []service.UploadedCertificateView) {
	data.UploadsHead = "Uploaded certificates · " + plural(len(list), "certificate")
	data.UploadWhy = service.UnavailableWords(cs.UploadUnavailable)
	back := "Its hosts go back to Traefik's own certificate."
	if cs.IssuerName != "" {
		back = "Its hosts go back to " + cs.IssuerName + "."
	}
	for _, u := range list {
		data.Uploads = append(data.Uploads, uploadedRowOf(now, u, back))
		if u.ID == data.UploadedID && data.UploadedNotice != "" {
			data.UploadedNotice = "Uploaded. It covers " + strings.Join(u.Names, ", ") + "."
			if data.UploadReplaced {
				data.UploadedNotice += " It replaces the earlier certificate for the same names."
			}
		}
	}
}

// maxNamesShown is how many names an uploaded certificate's row lists before "+n".
const maxNamesShown = 3

// uploadedRowOf says an uploaded certificate in the page's words: its mark and expiry follow its
// status, its names are mono, and the lines under it say which domains present it and which keep an
// earlier certificate (and why).
func uploadedRowOf(now time.Time, u service.UploadedCertificateView, back string) uploadedRow {
	row := uploadedRow{ID: u.ID, Names: u.Names, Issuer: u.Issuer, KeyType: u.KeyType, Untrusted: !u.Trusted, Back: back,
		AllNames: strings.Join(u.Names, ", ")}
	if len(row.Names) > maxNamesShown {
		row.More, row.Names = len(u.Names)-maxNamesShown, u.Names[:maxNamesShown]
	}
	if row.Issuer == "" {
		row.Issuer = u.Subject
	}
	title := "expires " + u.NotAfter.UTC().Format("2006-01-02 15:04 UTC")
	switch u.Status {
	case service.UploadExpired:
		row.Mark, row.Expiry = "failed", domainCertCell{Text: "expired", Warn: true, Title: "expired " + u.NotAfter.UTC().Format("2006-01-02 15:04 UTC")}
	case service.UploadExpiring:
		row.Mark, row.Expiry = "warning", domainCertCell{Text: "expires in " + dayCount(u.NotAfter.Sub(now)), Warn: true, Title: title}
	default:
		row.Mark, row.Expiry = "healthy", domainCertCell{Text: "expires in " + dayCount(u.NotAfter.Sub(now)), Title: title}
	}
	if len(u.Domains) > 0 {
		row.Serves = sentence{plain("Serves "), mono(strings.Join(u.Domains, ", ")), plain(".")}
	}
	if len(u.KeptEarlier) > 0 {
		hosts := make([]string, len(u.KeptEarlier))
		for i, k := range u.KeptEarlier {
			hosts[i] = k.Host
		}
		row.Kept = sentence{plain("Still on their earlier certificate: "), mono(strings.Join(hosts, ", ")), plain(".")}
		plainKept := false
		// Hosts that ask for the same other names are one sentence.
		var order []string
		hostsBy := map[string][]string{}
		for _, k := range u.KeptEarlier {
			if len(k.AlsoAsks) == 0 {
				plainKept = true
				continue
			}
			asks := strings.Join(k.AlsoAsks, ", ")
			if _, ok := hostsBy[asks]; !ok {
				order = append(order, asks)
			}
			hostsBy[asks] = append(hostsBy[asks], k.Host)
		}
		for _, asks := range order {
			word, verb := "The route of ", " also asks for "
			if len(hostsBy[asks]) > 1 {
				word, verb = "The routes of ", " also ask for "
			}
			row.Kept = append(row.Kept, plain(" "+word), mono(strings.Join(hostsBy[asks], ", ")), plain(verb), mono(asks),
				plain(": upload a certificate that covers them too."))
		}
		if plainKept {
			row.Kept = append(row.Kept, plain(" Traefik keeps a certificate it already has: run the installer again on this server to drop it."))
		}
	}
	return row
}

// certFieldMessage is a refusal of the certificates forms as a sentence for the input it names: the
// service's messages are predicates ("is required", "no certificate found"), so the ones that
// continue the field's name get it in front, and the others start a sentence of their own.
func certFieldMessage(field, message string) string {
	label := map[string]string{"server": "The directory URL", "email": "The email", "eabKeyId": "The EAB key ID",
		"eabHmacKey": "The EAB HMAC key", "caCertificate": "The CA certificate", "certificate": "The certificate", "key": "The key"}[field]
	for _, lead := range []string{"is ", "isn't ", "may ", "must ", "has ", "contains "} {
		if label != "" && strings.HasPrefix(message, lead) {
			return label + " " + message + "."
		}
	}
	if message == "" {
		return ""
	}
	r := []rune(message)
	r[0] = unicode.ToUpper(r[0])
	text := string(r)
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return text
}

// certificateBlocksOnly is the pasted text to draw back after a refusal: as typed, unless it holds a
// private key (the key may be pasted into the certificate's box), and then only its CERTIFICATE
// blocks, re-encoded. The key is never drawn back.
func certificateBlocksOnly(text string) string {
	if !strings.Contains(text, "PRIVATE KEY") {
		return text
	}
	var out strings.Builder
	rest := []byte(text)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return out.String()
		}
		if block.Type == "CERTIFICATE" {
			out.Write(pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: block.Bytes}))
		}
	}
}

// postUpload answers POST /settings/certificates/upload: the certificate and its key, pasted.
func (s *server) uploadCertificate(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	cert, key := r.PostFormValue("certificate"), r.PostFormValue("key")
	view, err := s.svc.UploadCertificate(r.Context(), cert, key)
	if err == nil {
		kind := "saved"
		if view.Replaced != "" {
			kind = "replaced"
		}
		http.Redirect(w, r, "/settings/certificates?upload="+kind+"&certificate="+url.QueryEscape(view.ID), http.StatusSeeOther)
		return
	}
	// A refusal draws the page again with the certificate as pasted, but only its certificate
	// blocks: the key may have been pasted into the same box, and the key is never drawn back.
	form := certificatesData{UploadOpen: true, UploadCert: certificateBlocksOnly(cert)}
	status := http.StatusUnprocessableEntity
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr) && fieldErr.Field == "key":
		form.UploadKeyErr = certFieldMessage("key", fieldErr.Message)
	case errors.As(err, &fieldErr):
		form.UploadCertErr = certFieldMessage("certificate", fieldErr.Message)
	case errors.Is(err, service.ErrCertificatesUnavailable):
		form.UploadAlert, status = err.Error(), http.StatusConflict
	case errors.Is(err, service.ErrTooManyCertificates):
		form.UploadAlert = err.Error() + "."
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't keep an uploaded certificate", "error", err)
		form.UploadAlert, status = "Nomad couldn't keep the certificate, so nothing was saved.", http.StatusBadGateway
	default:
		s.serverError(w, r, "upload a certificate", err)
		return
	}
	s.renderSettings(w, r, entryCertificates, settingsData{Certificates: form, Status: status})
}

// removeUploadedCertificate answers POST /settings/certificates/uploaded/{id}/remove.
func (s *server) removeUploadedCertificate(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	err := s.svc.DeleteUploadedCertificate(r.Context(), r.PathValue("id"))
	switch {
	case err == nil, errors.Is(err, service.ErrCertificateNotFound):
		// Already gone (a second click, another tab): the page says it is.
		http.Redirect(w, r, "/settings/certificates?upload=removed", http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't remove an uploaded certificate", "error", err)
		s.renderSettings(w, r, entryCertificates, settingsData{Certificates: certificatesData{UploadAlert: "Nomad couldn't remove the certificate, so nothing changed."}, Status: http.StatusBadGateway})
	default:
		s.serverError(w, r, "remove an uploaded certificate", err)
	}
}

// ---- The issuer -------------------------------------------------------------------------

// issuerFormOf fills the Change issuer form: what the installation has now unless a refused post or a
// preset link gave values, and the preset links.
func issuerFormOf(data *certificatesData, cs service.CertificateSettings) {
	f := data.Issue
	f.Why = service.UnavailableWords(cs.IssuerUnavailable)
	f.Installer = "Let's Encrypt"
	if cs.Issuer != service.IssuerCustom && cs.IssuerName != "" {
		f.Installer = cs.IssuerName
	}
	for _, p := range service.ACMEPresets {
		f.Presets = append(f.Presets, issuerPreset{Name: p.Name, Href: "/settings/certificates?preset=" + p.Key + "#issuer"})
	}
	if cs.ACME != nil {
		f.EABKeySet, f.HasCA = cs.ACME.EABKeySet, cs.ACME.PrivateCA
	}
	if !f.Set {
		f.Choice = "installer"
		if cs.Issuer == service.IssuerCustom {
			f.Choice = "custom"
		}
	}
	if f.Choice == "custom" {
		// What is stored fills what the post or the preset left empty.
		if cs.ACME != nil {
			if f.Server == "" {
				f.Server = cs.ACME.Server
			}
			if f.Email == "" {
				f.Email = cs.ACME.Email
			}
			if f.EABKeyID == "" && !f.Set {
				f.EABKeyID = cs.ACME.EABKeyID
			}
		}
		if f.Email == "" {
			f.Email = cs.ContactEmail
		}
	}
	if f.Errs == nil {
		f.Errs = map[string]string{}
	}
	data.Issue = f
}

// setCertificateIssuer answers POST /settings/certificates/issuer.
func (s *server) setCertificateIssuer(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	form := issuerForm{Open: true, Set: true, Choice: r.PostFormValue("issuer"),
		Server: strings.TrimSpace(r.PostFormValue("server")), Email: strings.TrimSpace(r.PostFormValue("email")),
		EABKeyID: strings.TrimSpace(r.PostFormValue("eabKeyId")), CA: r.PostFormValue("caCertificate"),
		RemoveCA: r.PostFormValue("removeCA") == "1", Errs: map[string]string{}}
	var err error
	switch form.Choice {
	case "installer":
		_, err = s.svc.ResetCertificateIssuer(r.Context())
	case "custom":
		in := service.CertificateIssuerInput{Server: form.Server, Email: form.Email, EABKeyID: form.EABKeyID}
		// An empty HMAC field keeps the stored key. The CA is sent only when the box has one or its removal
		// is asked for: an empty box on a stored CA means keep.
		if v := r.PostFormValue("eabHmacKey"); strings.TrimSpace(v) != "" {
			in.EABHMACKey = &v
		}
		switch {
		case strings.TrimSpace(form.CA) != "":
			in.CACertificate = &form.CA
		case form.RemoveCA:
			empty := ""
			in.CACertificate = &empty
		}
		_, err = s.svc.SetCertificateIssuer(r.Context(), in)
	default:
		form.Choice = ""
		err = app.NewFieldError("issuer", "must be installer or custom")
	}
	if err == nil {
		http.Redirect(w, r, "/settings/certificates?issuer=saved", http.StatusSeeOther)
		return
	}
	status := http.StatusUnprocessableEntity
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr) && fieldErr.Field == "issuer":
		form.Alert = "Choose the installer's issuer or another ACME issuer."
	case errors.As(err, &fieldErr):
		form.Errs[fieldErr.Field] = certFieldMessage(fieldErr.Field, fieldErr.Message)
	case errors.Is(err, service.ErrIssuerUnavailable):
		form.Alert, status = err.Error(), http.StatusConflict
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't save the certificate issuer", "error", err)
		form.Alert, status = "Nomad couldn't keep the issuer's settings, so nothing was saved.", http.StatusBadGateway
	default:
		s.serverError(w, r, "save the certificate issuer", err)
		return
	}
	s.renderSettings(w, r, entryCertificates, settingsData{Certificates: certificatesData{Issue: form}, Status: status})
}
