package service

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Settings › Certificates (feature 023, research R5). The certificate
// authority and its contact email are the installer's (CARAVANA_ACME,
// CARAVANA_ACME_EMAIL) until the owner sets an issuer of their own (feature
// 034, certissuer.go), and uploads certificates (uploadedcerts.go). The other
// setting Caravana owns is how long before a certificate's end it counts as
// expiring (7, 14 or 30 days; 14 until changed), which every check reads from
// the store.

// The issuers CertificateSettings names.
const (
	IssuerLetsEncrypt        = "letsencrypt"
	IssuerLetsEncryptStaging = "letsencrypt-staging"
	IssuerNone               = "none"    // Traefik serves its own certificate
	IssuerUnknown            = "unknown" // the installer hasn't said (it predates 023): rerun it
	IssuerCustom             = "custom"  // the issuer the owner set (feature 034)
)

// CertificateSettings is how this installation gets its certificates.
type CertificateSettings struct {
	// Issuer is one of the Issuer constants.
	Issuer string `json:"issuer"`
	// ContactEmail is the email given to Let's Encrypt; "" when unknown.
	ContactEmail string `json:"contactEmail"`
	// WarnBeforeDays is how many days before its end a certificate counts as
	// expiring: 7, 14 or 30.
	WarnBeforeDays int `json:"warnBeforeDays"`
	// EdgeAddress is where the certificate checks reach Traefik.
	EdgeAddress string `json:"edgeAddress"`
	// IssuerName is the issuer in words: "Let's Encrypt", "Let's Encrypt (staging)", a preset's name
	// when the custom issuer's server is its address, else the server's host. "" for none and unknown.
	IssuerName string `json:"issuerName"`
	// ACME is the custom issuer; nil while the installer's applies (feature 034).
	ACME *ACMEIssuerView `json:"acme"`
	// IssuerUnavailable is "" when the issuer can be changed, else why not: Unavailable… below.
	IssuerUnavailable string `json:"issuerUnavailable"`
	// UploadUnavailable is "" when certificates can be uploaded, else why not.
	UploadUnavailable string `json:"uploadUnavailable"`
}

// ACMEIssuerView is the custom issuer as the API and the console show it. The EAB HMAC key is
// never in it: EABKeySet says one is stored.
type ACMEIssuerView struct {
	Server    string    `json:"server"`
	Email     string    `json:"email"`
	EABKeyID  string    `json:"eabKeyId"`
	EABKeySet bool      `json:"eabKeySet"`
	PrivateCA bool      `json:"privateCA"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CertificateView is one row of the certificates table: the certificate the
// last check of a domain found.
type CertificateView struct {
	Host    string `json:"host"`
	App     string `json:"app"`
	Service string `json:"service"`
	// Issuer is the issuer's name ("Let's Encrypt", "Traefik's own"); "" when
	// the handshake presented nothing.
	Issuer string `json:"issuer"`
	// Status is edge's certificate status: issued, pending, self, expiring or
	// error.
	Status   string     `json:"status"`
	NotAfter *time.Time `json:"notAfter"`
	RenewsAt *time.Time `json:"renewsAt"`
	// Fingerprint is the SHA-256 of the leaf the handshake showed, hex; "" when none. Uploaded is the
	// uploaded certificate the host presents, nil for any other (feature 034).
	Fingerprint string       `json:"fingerprint"`
	Uploaded    *UploadedRef `json:"uploaded"`
}

// CertificateSettings answers how this installation gets its certificates. It asks Nomad for the
// features of Traefik's job (feature 034) and never fails on that: a Nomad that can't be read is a
// reason in IssuerUnavailable and UploadUnavailable.
func (s *Service) CertificateSettings(ctx context.Context) (CertificateSettings, error) {
	days, _, err := s.store.CertificateSettings(ctx)
	if err != nil {
		return CertificateSettings{}, err
	}
	mode, email := s.ACME()
	out := CertificateSettings{
		Issuer:         issuerOf(mode),
		ContactEmail:   email,
		WarnBeforeDays: days,
		EdgeAddress:    s.edgeSettings().edgeAddress,
		IssuerName:     issuerNameOf(mode),
	}
	custom, ok, err := s.store.ACMEIssuer(ctx)
	if err != nil {
		return CertificateSettings{}, err
	}
	if ok {
		out.Issuer, out.ContactEmail, out.IssuerName = IssuerCustom, custom.Email, customIssuerName(custom.Server)
		out.ACME = &ACMEIssuerView{Server: custom.Server, Email: custom.Email, EABKeyID: custom.EABKeyID,
			EABKeySet: custom.EABKeySet, PrivateCA: custom.CA != "", UpdatedAt: custom.UpdatedAt}
	}
	out.IssuerUnavailable, out.UploadUnavailable = s.certificateFeatures(ctx, mode)
	return out, nil
}

// issuerOf names the issuer of CARAVANA_ACME's mode.
func issuerOf(mode string) string {
	switch mode {
	case "production":
		return IssuerLetsEncrypt
	case "staging":
		return IssuerLetsEncryptStaging
	case "none":
		return IssuerNone
	}
	return IssuerUnknown
}

// issuerNameOf is the installer's issuer in words, "" when it has none or isn't known.
func issuerNameOf(mode string) string {
	switch mode {
	case "production":
		return "Let's Encrypt"
	case "staging":
		return "Let's Encrypt (staging)"
	}
	return ""
}

// SetWarnBeforeDays sets how many days before its end a certificate counts as
// expiring: 7, 14 or 30 (an *app.FieldError on "warnBeforeDays" otherwise).
// The cached checks are judged again at once with the new window, from the
// dates they hold (nothing is dialed), so the Domains page, the certificates
// table and the needs-you list follow without waiting for the next check.
func (s *Service) SetWarnBeforeDays(ctx context.Context, days int) error {
	if !slices.Contains(store.WarnBeforeDays, days) {
		return app.NewFieldError("warnBeforeDays", "must be 7, 14 or 30 days, not %d", days)
	}
	if err := s.store.SetWarnBeforeDays(ctx, days); err != nil {
		return err
	}
	if err := s.rejudgeCertificates(ctx, time.Duration(days)*24*time.Hour); err != nil {
		// The setting is saved; the next check judges every row with it.
		s.log.Warn("judge the certificates again with the new warning window", "error", err)
	}
	return nil
}

// rejudgeCertificates turns each cached issued certificate inside warn into
// expiring, and each expiring one outside it back into issued, and saves the
// state that follows. Rows of hosts whose app isn't served are left alone.
func (s *Service) rejudgeCertificates(ctx context.Context, warn time.Duration) error {
	targets, err := s.domainTargets(ctx, DomainFilter{})
	if err != nil {
		return err
	}
	checks, err := s.store.ListDomainChecks(ctx)
	if err != nil {
		return err
	}
	byHost := make(map[string]store.DomainCheckRecord, len(checks))
	for _, c := range checks {
		byHost[c.Host] = c
	}
	now := s.clock.Now().UTC()
	changed := map[string]bool{}
	seen := map[string]bool{}
	for _, t := range targets {
		c, ok := byHost[t.host]
		if !ok || seen[t.host] || !t.served() || c.CertNotAfter == nil ||
			(c.CertStatus != edge.CertIssued && c.CertStatus != edge.CertExpiring) {
			continue
		}
		seen[t.host] = true
		f := factsOf(c)
		f.Certificate.Status = edge.CertIssued
		if c.CertNotAfter.Sub(now) <= warn {
			f.Certificate.Status = edge.CertExpiring
		}
		rec := recordOf(t, f, stateFor(t, f))
		if rec.CertStatus == c.CertStatus && rec.State == c.State {
			continue
		}
		if err := s.store.UpsertDomainCheck(ctx, rec); err != nil {
			return err
		}
		changed[rec.AppID] = true
	}
	for appID := range changed {
		s.domainsChanged(appID)
	}
	if len(changed) > 0 {
		s.publish(Change{What: ChangeAttention})
	}
	return nil
}

// certOrder ranks a certificate status for the table: problems first.
var certOrder = map[string]int{
	edge.CertExpiring: 0, edge.CertError: 1, edge.CertPending: 2, edge.CertIssued: 3, edge.CertSelf: 4,
}

// Certificates lists the certificate of every domain whose app is served, as
// the last check found it: problems first (expiring, error, pending), then
// issued and Traefik's own, by host. A domain never checked, or whose app
// isn't served, has no row. It reads only SQLite.
func (s *Service) Certificates(ctx context.Context) ([]CertificateView, error) {
	rows, err := s.Domains(ctx, DomainFilter{})
	if err != nil {
		return nil, err
	}
	out := []CertificateView{}
	for _, v := range rows {
		if v.State == edge.StateNotServed || v.CheckedAt == nil || v.Certificate.Status == edge.CertNone {
			continue
		}
		c := CertificateView{Host: v.Host, App: v.App, Service: v.Service, Issuer: v.Certificate.Issuer, Status: v.Certificate.Status,
			Fingerprint: v.Certificate.Fingerprint, Uploaded: v.Uploaded}
		if !v.Certificate.NotAfter.IsZero() {
			c.NotAfter = new(v.Certificate.NotAfter.UTC())
		}
		if !v.Certificate.RenewsAt.IsZero() {
			c.RenewsAt = new(v.Certificate.RenewsAt.UTC())
		}
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(x, y CertificateView) int {
		return cmp.Or(
			cmp.Compare(certRank(x.Status), certRank(y.Status)),
			strings.Compare(x.Host, y.Host),
			strings.Compare(x.App, y.App),
		)
	})
	return out, nil
}

func certRank(status string) int {
	if r, ok := certOrder[status]; ok {
		return r
	}
	return len(certOrder)
}
