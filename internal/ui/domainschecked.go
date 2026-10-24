package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Check now (the Add panel's action=check) runs the check on the post and
// answers 303 to the same page with ?checked=<host>#add, so a reload doesn't
// post again and the browser lands on the panel, where the result is. The
// check isn't stored by the service for a host no app has yet, so the
// console keeps it for a minute, with what was typed, and the page the 303
// leads to draws the panel from it. An unknown or expired host draws the
// panel empty.

// checkedFor is how long a Check now's result is kept for the page that
// follows it.
const checkedFor = time.Minute

// checkedAdd is one Check now: what was typed and what the check found.
type checkedAdd struct {
	in    domainAddInput
	check service.DomainCheck
	at    time.Time
}

// checkedAdds is the small map of them, by host. The zero value is ready.
// (C#: a lock around a Dictionary; Go's zero-value mutex needs no setup.)
type checkedAdds struct {
	mu sync.Mutex
	m  map[string]checkedAdd
}

// put keeps e for host and forgets every entry that has expired.
func (c *checkedAdds) put(host string, e checkedAdd, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for h, old := range c.m {
		if now.Sub(old.at) >= checkedFor {
			delete(c.m, h)
		}
	}
	if c.m == nil {
		c.m = map[string]checkedAdd{}
	}
	e.at = now
	c.m[host] = e
}

// get is the check kept for host, unless it has expired.
func (c *checkedAdds) get(host string, now time.Time) (checkedAdd, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[host]
	if !ok || now.Sub(e.at) >= checkedFor {
		delete(c.m, host)
		return checkedAdd{}, false
	}
	return e, true
}

// checkedBack is where Check now leads: the page, with the host that was
// checked, at the panel.
func checkedBack(fixedApp, host string) string {
	page := "/domains"
	if fixedApp != "" {
		page = "/apps/" + url.PathEscape(fixedApp) + "/domains"
	}
	return page + "?checked=" + url.QueryEscape(host) + "#add"
}

// addPanelFor builds the Add panel a GET draws: from the check ?checked= names
// when one is kept, else a fresh panel for the app the page filters by.
func (s *server) addPanelFor(ctx context.Context, r *http.Request, filterApp, fixedApp string) (*domainAddView, error) {
	if host := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("checked"))); host != "" {
		if e, ok := s.checked.get(host, s.now()); ok {
			v, err := s.domainAddFor(ctx, e.in, fixedApp)
			if err != nil {
				return nil, err
			}
			v.applyCheck(e.check)
			return v, nil
		}
	}
	return s.domainAddFor(ctx, domainAddInput{App: filterApp}, fixedApp)
}
