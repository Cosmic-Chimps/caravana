package service

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/certs"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Uploaded certificates (feature 034, spec User Story 2). The owner pastes a PEM chain and its
// private key; internal/certs checks them; the normalized chain and the key go to the Nomad
// Variable caravana/certificates/<id>, which Traefik's job renders into its file provider (no
// restart), and SQLite keeps only what is public: names, subject, issuer, dates, fingerprint, key
// type and trust. The private key is in no answer, no error and no log line.

const (
	// maxUploadedCertificates is how many certificates may be uploaded: the task's secrets folder
	// is a 1 MiB tmpfs (research R6).
	maxUploadedCertificates = 30
	// defaultCertRecheck is how long after an upload or a removal the covered hosts are checked
	// again: Traefik's file watch takes about 10 seconds to pick the change up.
	defaultCertRecheck = 15 * time.Second
)

// The status of an uploaded certificate, by the warning window.
const (
	UploadValid    = "valid"
	UploadExpiring = "expiring" // inside the warning window
	UploadExpired  = "expired"
)

var (
	// ErrTooManyCertificates means 30 certificates are uploaded already.
	ErrTooManyCertificates = errors.New("30 certificates are uploaded already: remove one first")
	// ErrCertificateNotFound means no uploaded certificate has the ID.
	ErrCertificateNotFound = errors.New("no uploaded certificate has this ID")
)

// UploadedRef names an uploaded certificate where a domain says which one it presents.
type UploadedRef struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
}

// KeptEarlier is a served host an uploaded certificate covers that still presents another
// certificate: Traefik keeps a certificate it already has (research R5). AlsoAsks are the other
// hosts of the host's route that the upload doesn't cover: Traefik orders a certificate for every
// name of a route when one isn't covered, so the earlier one stays (research R4's limit). It is
// empty when the route's hosts are all covered.
type KeptEarlier struct {
	Host     string   `json:"host"`
	AlsoAsks []string `json:"alsoAsks"`
}

// UploadedCertificateView is an uploaded certificate with what the console and the API show.
type UploadedCertificateView struct {
	ID          string    `json:"id"`
	Names       []string  `json:"names"`
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	Fingerprint string    `json:"fingerprint"`
	KeyType     string    `json:"keyType"`
	Trusted     bool      `json:"trusted"`
	// Status is UploadValid, UploadExpiring (inside the warning window) or UploadExpired.
	Status     string    `json:"status"`
	UploadedAt time.Time `json:"uploadedAt"`
	// Domains are the served domains whose last check presented this certificate.
	Domains []string `json:"domains"`
	// KeptEarlier are the served domains it covers that present another certificate.
	KeptEarlier []KeptEarlier `json:"keptEarlier"`
	// Replaced is the ID of the certificate this upload replaced (same names); only UploadCertificate
	// answers it.
	Replaced string `json:"replaced,omitempty"`
}

// certStatus is a certificate's status at now, with warn as the window.
func certStatus(notAfter, now time.Time, warn time.Duration) string {
	switch {
	case !notAfter.After(now):
		return UploadExpired
	case notAfter.Sub(now) <= warn:
		return UploadExpiring
	}
	return UploadValid
}

// UploadedCertificates lists the uploaded certificates, oldest upload first.
func (s *Service) UploadedCertificates(ctx context.Context) ([]UploadedCertificateView, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return nil, err
	}
	list, err := s.store.ListUploadedCertificates(ctx)
	if err != nil {
		return nil, err
	}
	return s.uploadedViews(ctx, list)
}

// UploadedCertificate returns one uploaded certificate, or ErrCertificateNotFound.
func (s *Service) UploadedCertificate(ctx context.Context, id string) (UploadedCertificateView, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return UploadedCertificateView{}, err
	}
	c, err := s.store.GetUploadedCertificate(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return UploadedCertificateView{}, fmt.Errorf("certificate %q: %w", id, ErrCertificateNotFound)
	}
	if err != nil {
		return UploadedCertificateView{}, err
	}
	views, err := s.uploadedViews(ctx, []store.UploadedCertificate{c})
	if err != nil {
		return UploadedCertificateView{}, err
	}
	return views[0], nil
}

// UploadCertificate checks a chain and its key and stores them. Both may hold certificates in any
// order, and the key may be in either; the key must not be encrypted. A refusal is an
// *app.FieldError on "certificate" or "key" in plain words, ErrCertificatesUnavailable when Traefik
// can't take uploads (the message says what to run) and ErrTooManyCertificates past 30. A
// certificate with exactly the names of an earlier one replaces it (the view says which).
//
// The variable is written first and the row second; a row that can't be written takes the variable
// back, and a replaced certificate's variable and row go last, after the new ones exist. Covered hosts
// are checked again a few seconds later, when Traefik has picked the file up.
func (s *Service) UploadCertificate(ctx context.Context, certificatePEM, keyPEM string) (UploadedCertificateView, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return UploadedCertificateView{}, err
	}
	mode, _ := s.ACME()
	if _, reason := s.certificateFeatures(ctx, mode); reason != "" {
		return UploadedCertificateView{}, fmt.Errorf("%w: %s", ErrCertificatesUnavailable, UnavailableWords(reason))
	}
	p, err := certs.Parse([]byte(certificatePEM), []byte(keyPEM), certs.Options{Now: s.clock.Now()})
	if err != nil {
		return UploadedCertificateView{}, certFieldError(err)
	}

	s.certMu.Lock()
	defer s.certMu.Unlock()
	existing, err := s.store.ListUploadedCertificates(ctx)
	if err != nil {
		return UploadedCertificateView{}, err
	}
	var replaced *store.UploadedCertificate
	for i := range existing {
		if certs.SameNames(existing[i].Names, p.Names) {
			replaced = &existing[i]
			break
		}
	}
	if replaced == nil && len(existing) >= maxUploadedCertificates {
		return UploadedCertificateView{}, ErrTooManyCertificates
	}

	id, err := newCertificateID()
	if err != nil {
		return UploadedCertificateView{}, err
	}
	row := store.UploadedCertificate{ID: id, Names: p.Names, Subject: p.Subject, Issuer: p.Issuer,
		NotBefore: p.NotBefore, NotAfter: p.NotAfter, Fingerprint: p.Fingerprint, KeyType: p.KeyType,
		Trusted: p.Trusted, UploadedAt: s.clock.Now().UTC()}
	if err := s.deployer.SaveCertificate(ctx, id, string(p.ChainPEM), string(p.KeyPEM), p.Names); err != nil {
		return UploadedCertificateView{}, err
	}
	if err := s.store.InsertUploadedCertificate(ctx, row); err != nil {
		s.forgetCertificate(id, false)
		return UploadedCertificateView{}, err
	}
	if replaced != nil {
		if err := s.removeCertificate(ctx, replaced.ID); err != nil {
			s.forgetCertificate(id, true) // both would collide in Traefik's store: back to how it was
			return UploadedCertificateView{}, err
		}
	}

	s.scheduleCertRecheck(p.Names)
	view, err := s.viewOne(ctx, row)
	if err != nil {
		return UploadedCertificateView{}, err
	}
	if replaced != nil {
		view.Replaced = replaced.ID
	}
	return view, nil
}

// DeleteUploadedCertificate removes an uploaded certificate: its variable, then its row. Hosts it
// covered go back to the ACME issuer (or Traefik's own certificate without one) once Traefik's
// watch has run; they are checked again a few seconds later. ErrCertificateNotFound for an ID that
// isn't one.
func (s *Service) DeleteUploadedCertificate(ctx context.Context, id string) error {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return err
	}
	s.certMu.Lock()
	defer s.certMu.Unlock()
	c, err := s.store.GetUploadedCertificate(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("certificate %q: %w", id, ErrCertificateNotFound)
	}
	if err != nil {
		return err
	}
	if err := s.removeCertificate(ctx, id); err != nil {
		return err
	}
	s.scheduleCertRecheck(c.Names)
	return nil
}

// removeCertificate deletes a certificate's variable and then its row.
func (s *Service) removeCertificate(ctx context.Context, id string) error {
	if err := s.deployer.DeleteCertificate(ctx, id); err != nil {
		return err
	}
	return s.store.DeleteUploadedCertificate(ctx, id)
}

// forgetCertificate undoes a half-made upload: the variable and, with row, the row. It uses a
// context of its own, since the request's may be the reason it failed, and only logs.
func (s *Service) forgetCertificate(id string, row bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.bg), 10*time.Second)
	defer cancel()
	if err := s.deployer.DeleteCertificate(ctx, id); err != nil {
		s.log.Warn("remove the variable of a certificate that wasn't saved", "certificate", id, "error", err)
	}
	if row {
		if err := s.store.DeleteUploadedCertificate(ctx, id); err != nil {
			s.log.Warn("remove the row of a certificate that wasn't saved", "certificate", id, "error", err)
		}
	}
}

// newCertificateID is "crt_" and 12 random lowercase hex digits.
func newCertificateID() (string, error) {
	var b [6]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a certificate ID: %w", err)
	}
	return "crt_" + hex.EncodeToString(b[:]), nil
}

// ---- views ------------------------------------------------------------------------

func (s *Service) viewOne(ctx context.Context, c store.UploadedCertificate) (UploadedCertificateView, error) {
	views, err := s.uploadedViews(ctx, []store.UploadedCertificate{c})
	if err != nil {
		return UploadedCertificateView{}, err
	}
	return views[0], nil
}

// uploadedViews adds to each certificate its status, the domains that present it and the ones that
// keep an earlier certificate.
func (s *Service) uploadedViews(ctx context.Context, list []store.UploadedCertificate) ([]UploadedCertificateView, error) {
	out := make([]UploadedCertificateView, 0, len(list))
	if len(list) == 0 {
		return out, nil
	}
	warn, err := s.warnWindow(ctx)
	if err != nil {
		return nil, err
	}
	targets, err := s.domainTargets(ctx, DomainFilter{})
	if err != nil {
		return nil, err
	}
	checks, err := s.store.ListDomainChecks(ctx)
	if err != nil {
		return nil, err
	}
	byHost := make(map[string]store.DomainCheckRecord, len(checks))
	for _, c := range checks {
		byHost[c.Host] = c
	}
	now := s.clock.Now().UTC()

	// The served hosts, once each, and which hosts ask for the same router (the same app, service
	// and router name): Traefik orders a certificate for all of them when one isn't covered.
	type route struct{ app, service, router string }
	var served []string
	routeOf := map[string]route{}
	routeHosts := map[route][]string{}
	for _, t := range targets {
		if !t.served() {
			continue
		}
		r := route{t.rec.App.ID, t.service.Name, routerOf(t.rec.App.ID, t.service.Name, t.domain)}
		if _, dup := routeOf[t.host]; !dup {
			served = append(served, t.host)
			routeOf[t.host] = r
		}
		if !slices.Contains(routeHosts[r], t.host) {
			routeHosts[r] = append(routeHosts[r], t.host)
		}
	}

	for _, c := range list {
		v := UploadedCertificateView{ID: c.ID, Names: c.Names, Subject: c.Subject, Issuer: c.Issuer,
			NotBefore: c.NotBefore.UTC(), NotAfter: c.NotAfter.UTC(), Fingerprint: c.Fingerprint, KeyType: c.KeyType,
			Trusted: c.Trusted, Status: certStatus(c.NotAfter, now, warn), UploadedAt: c.UploadedAt.UTC(),
			Domains: []string{}, KeptEarlier: []KeptEarlier{}}
		for _, host := range served {
			check, checked := byHost[host]
			switch {
			case checked && check.CertFingerprint == c.Fingerprint:
				v.Domains = append(v.Domains, host)
			case checked && certs.Covers(c.Names, host) && check.CertFingerprint != "" && check.CertIssuer != edge.TraefikOwn:
				k := KeptEarlier{Host: host, AlsoAsks: []string{}}
				for _, other := range routeHosts[routeOf[host]] {
					if other != host && !certs.Covers(c.Names, other) {
						k.AlsoAsks = append(k.AlsoAsks, other)
					}
				}
				slices.Sort(k.AlsoAsks)
				v.KeptEarlier = append(v.KeptEarlier, k)
			}
		}
		slices.Sort(v.Domains)
		slices.SortFunc(v.KeptEarlier, func(a, b KeptEarlier) int { return strings.Compare(a.Host, b.Host) })
		out = append(out, v)
	}
	return out, nil
}

// ---- recognising an uploaded certificate in a check -------------------------------

// uploadsByFingerprint maps the SHA-256 of each uploaded leaf to its row.
func (s *Service) uploadsByFingerprint(ctx context.Context) (map[string]store.UploadedCertificate, error) {
	list, err := s.store.ListUploadedCertificates(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]store.UploadedCertificate, len(list))
	for _, c := range list {
		out[c.Fingerprint] = c
	}
	return out, nil
}

// judgeUploaded corrects what a handshake showed when the certificate is an uploaded one: it is
// judged by the upload's own dates (a chain from a private CA isn't trusted by the system, which
// the handshake's verification says), its issuer is the upload's, and nothing renews it. A
// certificate that isn't an upload, or whose names don't cover the host, is left as it is.
func (s *Service) judgeUploaded(ctx context.Context, host string, c *edge.Certificate, warn time.Duration) {
	if c.Fingerprint == "" {
		return
	}
	uploads, err := s.uploadsByFingerprint(ctx)
	if err != nil {
		s.log.Warn("read the uploaded certificates for a domain check", "domain", host, "error", err)
		return
	}
	u, ok := uploads[c.Fingerprint]
	if !ok || !certs.Covers(u.Names, host) {
		return
	}
	c.Issuer, c.NotAfter, c.RenewsAt = u.Issuer, u.NotAfter.UTC(), time.Time{}
	c.Status = edge.CertIssued
	if certStatus(u.NotAfter, s.clock.Now(), warn) != UploadValid {
		c.Status = edge.CertExpiring
	}
}

// scheduleCertRecheck checks the served hosts the names cover again after certRecheck, in the
// background, until the service is stopped.
func (s *Service) scheduleCertRecheck(names []string) {
	delay := s.certRecheck
	s.backupWatcher.Go(func() {
		select {
		case <-s.bg.Done():
			return
		case <-time.After(delay):
		}
		if err := s.recheckCovered(s.bg, names); err != nil {
			s.log.Warn("check the hosts of a certificate again", "error", err)
		}
	})
}

// recheckCovered checks every served host that names cover now, as the watcher would, and saves
// what it finds. The pages are told even when no state changed: the certificate did.
func (s *Service) recheckCovered(ctx context.Context, names []string) error {
	targets, err := s.domainTargets(ctx, DomainFilter{})
	if err != nil {
		return err
	}
	warn, err := s.warnWindow(ctx)
	if err != nil {
		return err
	}
	var probe []domainTarget
	seen := map[string]bool{}
	for _, t := range targets {
		if seen[t.host] || !t.served() || !certs.Covers(names, t.host) {
			continue
		}
		seen[t.host] = true
		probe = append(probe, t)
	}
	if len(probe) == 0 {
		return nil
	}
	expected := s.hostAddresses(ctx)
	es := s.edgeSettings()
	slots := make(chan struct{}, domainProbeParallel)
	var mu sync.Mutex
	changed := map[string]bool{}
	var wg sync.WaitGroup
	for _, t := range probe {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			f := s.probe(ctx, es, t.host, true, expected, warn)
			if _, err := s.saveCheck(ctx, t, f); err != nil {
				s.log.Warn("save a domain's check", "domain", t.host, "app", t.rec.App.ID, "error", err)
				return
			}
			mu.Lock()
			changed[t.rec.App.ID] = true
			mu.Unlock()
		})
	}
	wg.Wait()
	for appID := range changed {
		s.domainsChanged(appID)
	}
	if len(changed) > 0 {
		s.publish(Change{What: ChangeAttention})
	}
	return nil
}
