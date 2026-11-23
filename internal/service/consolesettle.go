package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// What keeps the console's address right while a change goes on and after it (feature 051): the route's
// variable, the pass that settles changes a restart left, the hand-off to the server agent and the
// redirect of the old domain.

// reconcileConsoleRouteLocked makes Traefik's console route say what it should and refreshes what the
// redirect and the app checks read. The hosts are the current one first, then the open change's domain
// (and, while it is switching, the old one: after the restart the current host is the new one, and the
// old one must keep answering until the change is settled), then each old domain whose window hasn't
// ended. On an installation that never used the feature nothing is written: the Traefik job's own
// domain is the route. The caller holds the console mutex. An error is Nomad refusing the write; the
// cache is already fresh, and the next pass writes again.
func (s *Service) reconcileConsoleRouteLocked(ctx context.Context) error {
	hosts, used, err := s.refreshConsoleRoutedLocked(ctx)
	if err != nil {
		return err
	}
	// The variable is the route only once the feature was used: before that, or after a change that
	// was cancelled or failed with nothing applied, the job's own domain stays the route.
	want := ""
	if used {
		want = strings.Join(hosts, " ")
	}
	if s.consoleAddr.written != nil && *s.consoleAddr.written == want {
		return nil
	}
	if want == "" {
		if s.consoleAddr.written == nil {
			// First look in this process: only a host that ever had a change can have a variable.
			if _, any, err := s.store.LatestConsoleAddressChange(ctx); err != nil {
				return err
			} else if !any {
				s.consoleAddr.written = &want
				return nil
			}
		}
		if err := s.deployer.SetConsoleHosts(ctx, nil); err != nil {
			return err
		}
	} else if err := s.deployer.SetConsoleHosts(ctx, hosts); err != nil {
		return err
	}
	s.consoleAddr.written = &want
	return nil
}

// refreshConsoleRoutedLocked reads the store and makes the cache that the redirect and the app checks
// read say which hosts the console's route carries. It returns those hosts, current first, and whether
// the feature is in use (a change is open, or one was applied), which decides whether the variable is
// the route at all. It touches nothing outside the process. The caller holds the console mutex.
func (s *Service) refreshConsoleRoutedLocked(ctx context.Context) (hosts []string, used bool, err error) {
	now := s.clock.Now().UTC()
	open, hasOpen, err := s.store.OpenConsoleAddressChange(ctx)
	if err != nil {
		return nil, false, err
	}
	olds, err := s.store.ConsoleOldDomains(ctx, now)
	if err != nil {
		return nil, false, err
	}
	applied, err := s.store.ConsoleAddressApplied(ctx)
	if err != nil {
		return nil, false, err
	}
	current := s.consoleHost()

	routed := &consoleRouted{reserved: map[string]bool{}, old: map[string]time.Time{}}
	add := func(h string) {
		if h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	add(current)
	if hasOpen {
		routed.pending = open.Domain
		add(open.Domain)
		if open.Status == store.ConsoleSwitching {
			add(open.OldDomain)
		}
	}
	for _, o := range olds {
		if o.Domain != current {
			routed.old[o.Domain] = o.Until
			add(o.Domain)
		}
	}
	for _, h := range hosts {
		if h != current {
			routed.reserved[h] = true
		}
	}
	s.consoleAddr.routed.Store(routed)
	return hosts, hasOpen || applied, nil
}

// consoleReserved says host is one the console's route carries besides the current one (a domain being
// checked, or an old one still redirecting): no app may use it.
func (s *Service) consoleReserved(host string) bool {
	r := s.consoleAddr.routed.Load()
	return r != nil && r.reserved[host]
}

// ConsoleRedirect says where a request for an old console domain goes. host is the request's Host (a
// port is ignored) and requestURI its path and query. When host is an old domain whose redirect
// window hasn't ended, location is the same path on the new address and ok is true; the caller answers
// 308 there (and serves /v1/ and /.well-known/ as before, see cmd/caravana's root). Any other host is
// not redirected. It reads memory only.
func (s *Service) ConsoleRedirect(host, requestURI string) (location string, ok bool) {
	r := s.consoleAddr.routed.Load()
	if r == nil || len(r.old) == 0 {
		return "", false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = normalizeHost(host)
	until, old := r.old[host]
	base := s.PublicURL()
	if !old || base == "" || host == s.consoleHost() || !s.clock.Now().Before(until) {
		return "", false
	}
	if !strings.HasPrefix(requestURI, "/") {
		requestURI = "/"
	}
	return base + requestURI, true
}

// runConsoleAddressPass runs the pass one gap after start and then every gap: Start made the first one.
func (s *Service) runConsoleAddressPass(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.consoleAddressPass(ctx)
	}
}

// Reasons of the changes the pass settles.
const (
	reasonConsoleRestarted = "Caravana restarted during the check."
	reasonConsoleLost      = "The server agent reported nothing for 5 minutes. Look at its log on the server: journalctl -u caravana-node."
)

// consoleAddressPass settles an open change a restart left and keeps the route right. A change that is
// checking and has no check goroutine in this process was left by one that ended: check_failed. A
// switching change whose domain is now the public URL's host is applied (the agent changed the address
// and Caravana restarted under it); one that waited longer than five minutes for the agent is lost. Then
// the route is made to say what it should, which also drops old domains whose window ended. A failure is
// logged and the next pass tries again.
func (s *Service) consoleAddressPass(ctx context.Context) {
	s.consoleAddr.mu.Lock()
	defer s.consoleAddr.mu.Unlock()
	if err := s.settleConsoleLocked(ctx); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("console address: settle the open change; the pass tries again", "error", err)
		}
		return
	}
	if err := s.reconcileConsoleRouteLocked(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("console address: update Traefik's route; the pass tries again", "error", err)
	}
}

func (s *Service) settleConsoleLocked(ctx context.Context) error {
	c, ok, err := s.store.OpenConsoleAddressChange(ctx)
	if err != nil || !ok {
		return err
	}
	now := s.clock.Now().UTC()
	switch c.Status {
	case store.ConsoleChecking:
		if !s.consoleAddr.running[c.ID] {
			return s.endOpenConsoleChange(ctx, c, store.ConsoleChecking, store.ConsoleCheckFailed, reasonConsoleRestarted, now)
		}
	case store.ConsoleSwitching:
		switch {
		case s.consoleHost() == c.Domain:
			return s.markConsoleApplied(ctx, c, now)
		case now.Sub(c.SwitchAt) > s.consoleAddr.lostAfter:
			return s.endOpenConsoleChange(ctx, c, store.ConsoleSwitching, store.ConsoleLost, reasonConsoleLost, now)
		}
	}
	return nil
}

// endOpenConsoleChange moves an open change from one state to a final one. A change that moved in the
// meantime is left alone.
func (s *Service) endOpenConsoleChange(ctx context.Context, c store.ConsoleAddressChange, from, to, reason string, now time.Time) error {
	c.Status, c.Reason, c.SettledAt = to, cleanConsoleReason(reason), now
	if err := s.store.UpdateConsoleAddressChange(ctx, c, from); err != nil {
		if errors.Is(err, store.ErrInvalidTransition) {
			return nil
		}
		return err
	}
	s.consoleChanged()
	return nil
}

// markConsoleApplied settles a switching change as applied: Caravana runs under the new address. The old
// domain keeps redirecting for the window the owner chose (none for 0 days).
func (s *Service) markConsoleApplied(ctx context.Context, c store.ConsoleAddressChange, now time.Time) error {
	c.Status, c.Reason, c.SettledAt = store.ConsoleApplied, "", now
	if c.RedirectDays > 0 {
		c.RedirectUntil = now.AddDate(0, 0, c.RedirectDays)
	}
	if err := s.store.UpdateConsoleAddressChange(ctx, c, store.ConsoleSwitching); err != nil {
		if errors.Is(err, store.ErrInvalidTransition) {
			return nil
		}
		return err
	}
	s.consoleChanged()
	return nil
}

// cleanConsoleReason makes a reason one printable line of at most 300 bytes: what an agent reports is
// shown on a page and kept in the store.
func cleanConsoleReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > consoleReasonMax {
		s = strings.ToValidUTF8(s[:consoleReasonMax], "")
	}
	return s
}

// ---- the node agent's side of the sync -----------------------------------------------------------

// NodeConsoleOutcome is the outcome of the last console address request an agent handled, in its report
// (contracts/agent.md). State is applied, failed, refused or running; Reason is one printable line.
type NodeConsoleOutcome struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// NodeConsoleAddress is the request in a sync answer: switch the console to Domain. ID is the change's.
type NodeConsoleAddress struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
}

var consoleIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// validateConsoleOutcome checks the outcome of a report: an ID of 32 hex digits and a known state. The
// reason is never refused, so an agent can't be stuck on a report the console turns down: what is kept
// is cleaned and cut to one printable line (cleanConsoleReason).
func validateConsoleOutcome(o *NodeConsoleOutcome) error {
	if o == nil {
		return nil
	}
	if !consoleIDRE.MatchString(o.ID) {
		return app.NewFieldError("consoleAddress.id", "must be 32 lowercase hex digits")
	}
	if !slices.Contains([]string{"applied", "failed", "refused", "running"}, o.State) {
		return app.NewFieldError("consoleAddress.state", "must be applied, failed, refused or running")
	}
	return nil
}

// consoleURLOf is the console's public URL as an agent may be told it: https://host[:port] with nothing
// after it, or "" (no address the agents could follow).
func consoleURLOf(public string) string {
	u, ok := mesh.CleanIssuer(public)
	if !ok {
		return ""
	}
	return u
}

// consoleHandoff is what a sync does about the console's address, called with serverMu held after the
// report is stored. First, the outcome the agent reports for the change that is switching settles it
// (failed and refused end it; applied ends it when Caravana runs under the new address, and is failed
// when it doesn't). Then the answer: consoleURL for every agent, and the request itself, only to an
// agent that reports it runs the console, while a change is switching. It never fails the sync: what
// goes wrong is logged and the answer has no request.
func (s *Service) consoleHandoff(ctx context.Context, report NodeReport, now time.Time) (consoleURL string, req *NodeConsoleAddress) {
	consoleURL = consoleURLOf(s.PublicURL())
	if !report.RunsConsole {
		return consoleURL, nil
	}
	s.consoleAddr.mu.Lock()
	defer s.consoleAddr.mu.Unlock()
	c, ok, err := s.store.OpenConsoleAddressChange(ctx)
	if err != nil {
		s.log.Warn("console address: read the open change", "error", err)
		return consoleURL, nil
	}
	if !ok || c.Status != store.ConsoleSwitching {
		return consoleURL, nil
	}
	if o := report.ConsoleAddress; o != nil && o.ID == c.ID {
		switch o.State {
		case "failed", "refused":
			to := store.ConsoleFailed
			if o.State == "refused" {
				to = store.ConsoleRefused
			}
			if err := s.endOpenConsoleChange(ctx, c, store.ConsoleSwitching, to, o.Reason, now); err != nil {
				s.log.Warn("console address: settle from the agent's report", "error", err)
				return consoleURL, nil
			}
			if err := s.reconcileConsoleRouteLocked(ctx); err != nil {
				s.log.Warn("console address: take the domain out of the route; the pass tries again", "error", err)
			}
			return consoleURL, nil
		case "applied":
			var err error
			if s.consoleHost() == c.Domain {
				err = s.markConsoleApplied(ctx, c, now)
			} else {
				err = s.endOpenConsoleChange(ctx, c, store.ConsoleSwitching, store.ConsoleFailed,
					fmt.Sprintf("Caravana restarted, but its public URL isn't https://%s.", c.Domain), now)
			}
			if err != nil {
				s.log.Warn("console address: settle from the agent's report", "error", err)
			}
			return consoleURL, nil
		}
	}
	return consoleURL, &NodeConsoleAddress{ID: c.ID, Domain: c.Domain}
}
