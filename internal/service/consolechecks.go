package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// ConsoleRouteProbe asks whether Traefik, at edgeAddress (host:port of its HTTPS entrypoint), routes
// host to Caravana: nil when an HTTPS request for host answers like Caravana's health check. It does
// not verify the certificate; the certificate is its own check. Tests give a fake.
type ConsoleRouteProbe func(ctx context.Context, edgeAddress, host string) error

// SetConsoleRouteProbe replaces the route check (the real one dials the edge address with the host
// as the TLS server name and the Host header). A nil probe keeps the current one.
func (s *Service) SetConsoleRouteProbe(p ConsoleRouteProbe) {
	if p != nil {
		s.consoleAddr.probe.Store(&p)
	}
}

func (s *Service) routeProbe() ConsoleRouteProbe {
	if p := s.consoleAddr.probe.Load(); p != nil {
		return *p
	}
	return dialConsoleRoute
}

var errRouteNotCaravana = errors.New("the answer isn't Caravana's health check")

// dialConsoleRoute is the real ConsoleRouteProbe: GET https://host/v1/health through the edge address.
// It follows no redirect and reads no more than a few hundred bytes.
func dialConsoleRoute(ctx context.Context, edgeAddress, host string) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, edgeAddress)
			},
			// The certificate is checked on its own, by edge.Check; this asks only about the route.
			TLSClientConfig:   &tls.Config{ServerName: host, InsecureSkipVerify: true}, //nolint:gosec // see above
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/v1/health", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&body) != nil || body.Status != "ok" {
		return errRouteNotCaravana
	}
	return nil
}

// routeAndCertificate runs the two checks that wait on Traefik: the route and the certificate. They
// are the last two of a change's checks.
func (s *Service) routeAndCertificate(ctx context.Context, domain string) (route, cert store.ConsoleCheck) {
	es := s.edgeSettings()
	warn, err := s.warnWindow(ctx)
	if err != nil {
		warn = 14 * 24 * time.Hour
	}
	route = store.ConsoleCheck{Name: checkRoute, State: checkOK, Message: "Traefik routes " + domain + " to Caravana"}
	if s.routeProbe()(ctx, es.edgeAddress, domain) != nil {
		route.State, route.Message = checkWaiting, "Traefik doesn't route "+domain+" to Caravana yet"
	}
	// edge.Check also looks at DNS; only its certificate is used here, and a name that was checked a
	// moment ago doesn't need a second opinion.
	facts := s.probe(ctx, es, domain, true, s.hostAddresses(ctx), warn)
	cert = consoleCertificateCheck(domain, facts.Certificate)
	return route, cert
}

// consoleCertificateCheck words what the handshake showed. A trusted certificate passes, and so does
// Traefik's own on an installation with no certificate resolver (--acme none): that is the certificate
// the domain will keep.
func consoleCertificateCheck(domain string, c edge.Certificate) store.ConsoleCheck {
	ck := store.ConsoleCheck{Name: checkCertificate, State: checkWaiting}
	switch {
	case c.Valid():
		ck.State = checkOK
		ck.Message = fmt.Sprintf("A certificate for %s from %s, valid until %s", domain, c.Issuer, c.NotAfter.Format("2006-01-02"))
	case c.Status == edge.CertSelf:
		ck.State = checkOK
		ck.Message = "Traefik's own certificate (this installation doesn't use Let's Encrypt)"
	case c.Status == edge.CertPending:
		ck.Message = "Traefik doesn't have the certificate for " + domain + " yet: it asks Let's Encrypt once the name reaches this server"
	case c.Status == edge.CertError && c.Issuer != "":
		ck.Message = fmt.Sprintf("The certificate presented for %s (from %s) isn't one a browser trusts", domain, c.Issuer)
	default:
		ck.Message = "No certificate for " + domain + " yet"
	}
	return ck
}

// withChecks returns c's checks with the route and certificate ones replaced.
func withChecks(c store.ConsoleAddressChange, route, cert store.ConsoleCheck) []store.ConsoleCheck {
	out := slices.Clone(c.Checks)
	for _, n := range []store.ConsoleCheck{route, cert} {
		i := slices.IndexFunc(out, func(x store.ConsoleCheck) bool { return x.Name == n.Name })
		if i < 0 {
			out = append(out, n)
		} else {
			out[i] = n
		}
	}
	return out
}

// runConsoleChecks is the check goroutine of one change: it puts the domain in Traefik's route, then
// looks every poll for the route and the certificate until both pass (the change is ready), the change
// is cancelled, or the limit passes (check_failed, and the route is taken back). It runs under the
// process context; a restart ends it, and the next pass settles the change.
func (s *Service) runConsoleChecks(ctx context.Context, id string) {
	defer func() {
		s.consoleAddr.mu.Lock()
		delete(s.consoleAddr.running, id)
		s.consoleAddr.mu.Unlock()
	}()

	s.consoleAddr.mu.Lock()
	err := s.reconcileConsoleRouteLocked(ctx)
	s.consoleAddr.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("console address: put the new domain in Traefik's route", "error", err)
			s.endConsoleCheck(ctx, id, store.ConsoleCheckFailed, "Caravana couldn't put the domain in Traefik's route (Nomad didn't accept the change): try again in a moment.", nil)
		}
		return
	}

	deadline := time.Now().Add(s.consoleAddr.limit)
	for {
		c, err := s.store.ConsoleAddressChange(ctx, id)
		if err != nil || c.Status != store.ConsoleChecking {
			return // cancelled, or the store is going away
		}
		route, cert := s.routeAndCertificate(ctx, c.Domain)
		if ctx.Err() != nil {
			return
		}
		checks := withChecks(c, route, cert)
		switch {
		case route.State == checkOK && cert.State == checkOK:
			s.endConsoleCheck(ctx, id, store.ConsoleReady, "", checks)
			return
		case !time.Now().Before(deadline):
			s.endConsoleCheck(ctx, id, store.ConsoleCheckFailed, consoleCheckTimeout(c.Domain, s.consoleAddr.limit, route), failWaiting(checks))
			return
		}
		s.saveConsoleChecks(ctx, c, checks)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.consoleAddr.poll):
		}
	}
}

// failWaiting turns the checks still waiting into failed ones, for a change that ran out of time.
func failWaiting(checks []store.ConsoleCheck) []store.ConsoleCheck {
	out := slices.Clone(checks)
	for i := range out {
		if out[i].State == checkWaiting {
			out[i].State = checkFailed
		}
	}
	return out
}

// consoleCheckTimeout is the reason of a change whose route or certificate didn't come in time.
func consoleCheckTimeout(domain string, limit time.Duration, route store.ConsoleCheck) string {
	if route.State != checkOK {
		return fmt.Sprintf("Traefik didn't route %s to Caravana within %s. Check that Nomad and Traefik are running, then try again.", domain, limit)
	}
	return fmt.Sprintf("%s got no certificate within %s. Let's Encrypt must reach this server on port 80, and the name must point here. Check, then try again.", domain, limit)
}

// saveConsoleChecks writes a change's checks when they differ from the stored ones.
func (s *Service) saveConsoleChecks(ctx context.Context, c store.ConsoleAddressChange, checks []store.ConsoleCheck) {
	if slices.Equal(c.Checks, checks) {
		return
	}
	c.Checks = checks
	if err := s.store.UpdateConsoleAddressChange(ctx, c, c.Status); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) && ctx.Err() == nil {
			s.log.Warn("console address: save the checks", "error", err)
		}
		return
	}
	s.consoleChanged()
}

// endConsoleCheck moves a checking change to ready or check_failed, with its last checks (nil keeps the
// stored ones), and takes the pending domain out of the route when it failed. A change cancelled in the
// meantime stays cancelled: the move is from checking only.
func (s *Service) endConsoleCheck(ctx context.Context, id, status, reason string, checks []store.ConsoleCheck) {
	s.consoleAddr.mu.Lock()
	defer s.consoleAddr.mu.Unlock()
	c, err := s.store.ConsoleAddressChange(ctx, id)
	if err != nil {
		return
	}
	if checks != nil {
		c.Checks = checks
	}
	now := s.clock.Now().UTC()
	c.Status, c.Reason = status, reason
	if status == store.ConsoleReady {
		c.ReadyAt = now
	} else {
		c.SettledAt = now
	}
	if err := s.store.UpdateConsoleAddressChange(ctx, c, store.ConsoleChecking); err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) && ctx.Err() == nil {
			s.log.Warn("console address: end the checks", "error", err)
		}
		return
	}
	s.consoleChanged()
	if status != store.ConsoleReady {
		if err := s.reconcileConsoleRouteLocked(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("console address: take the domain out of the route; the pass tries again", "error", err)
		}
	}
}

// freshConsoleCheck is the check the switch makes once more, without waiting: DNS, route and
// certificate. It writes the result into c and the store (the change stays ready), and returns the
// first failure as a *DomainNotHereError or *ConsoleCheckError. The caller holds the console mutex.
func (s *Service) freshConsoleCheck(ctx context.Context, c *store.ConsoleAddressChange) error {
	expected := s.hostAddresses(ctx)
	warn, err := s.warnWindow(ctx)
	if err != nil {
		return err
	}
	f := s.probe(ctx, s.edgeSettings(), c.Domain, false, expected, warn)
	dnsCheck := store.ConsoleCheck{Name: checkDNS, State: checkOK, Message: fmt.Sprintf("%s points at this server (%s)", c.Domain, addrList(f.DNS.Seen))}
	var dnsErr error
	if f.DNS.Status != edge.DNSOK {
		dnsCheck.State = checkFailed
		dnsCheck.Message = fmt.Sprintf("%s doesn't point at this server any more (DNS says %s)", c.Domain, f.DNS.Status)
		dnsErr = &DomainNotHereError{Field: "domain", Words: dnsCheck.Message + ". Point it at this server, then switch."}
	}
	route, cert := s.routeAndCertificate(ctx, c.Domain)
	checks := withChecks(*c, route, cert)
	checks[slices.IndexFunc(checks, func(x store.ConsoleCheck) bool { return x.Name == checkDNS })] = dnsCheck
	for i := range checks {
		if checks[i].State == checkWaiting {
			checks[i].State = checkFailed // nothing waits at the switch
		}
	}
	if !slices.Equal(c.Checks, checks) {
		c.Checks = checks
		if err := s.store.UpdateConsoleAddressChange(ctx, *c, store.ConsoleReady); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return err
		}
		s.consoleChanged()
	}
	switch {
	case dnsErr != nil:
		return dnsErr
	case route.State != checkOK:
		return &ConsoleCheckError{Field: "domain", Words: fmt.Sprintf("Traefik doesn't route %s to Caravana right now, so the switch would leave the console unreachable there.", c.Domain)}
	case cert.State != checkOK:
		return &ConsoleCheckError{Field: "domain", Words: fmt.Sprintf("%s has no certificate right now, so browsers would refuse it after the switch.", c.Domain)}
	}
	return nil
}
