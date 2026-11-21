package dnstest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/cosmic-chimps/caravana/internal/dns"
)

// Provider is an in-memory dns.Provider with knobs, for the service's tests.
// It is shared by every provider the factory builds, so a test sees all calls.
type Provider struct {
	mu          sync.Mutex
	wantToken   string
	zones       map[string]string
	records     map[string][]dns.Record
	refuse      bool
	unavailable bool
	failCreate  int // Create fails (unavailable) after this many successes; -1 never
	next        int
	calls       int
	byOp        map[string]int
	tokens      []string
}

// NewProvider returns an empty provider that accepts any token.
func NewProvider() *Provider {
	return &Provider{zones: map[string]string{}, records: map[string][]dns.Record{}, byOp: map[string]int{}, failCreate: -1}
}

// WantToken makes every provider built with another token refuse.
func (p *Provider) WantToken(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wantToken = token
}

// AddZone makes a zone and returns its ID.
func (p *Provider) AddZone(name string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	id := fmt.Sprintf("zone%d", p.next)
	p.zones[name] = id
	return id
}

// Seed adds a hand-made record to a zone.
func (p *Provider) Seed(zone string, r dns.Record) dns.Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	r.ID = fmt.Sprintf("rec%d", p.next)
	if r.TTL == 0 {
		r.TTL = 1
	}
	id := p.zones[zone]
	p.records[id] = append(p.records[id], r)
	return r
}

// SetRefuse makes every call fail with dns.ErrRefused.
func (p *Provider) SetRefuse(v bool) { p.mu.Lock(); p.refuse = v; p.mu.Unlock() }

// SetUnavailable makes every call fail with dns.ErrUnavailable.
func (p *Provider) SetUnavailable(v bool) { p.mu.Lock(); p.unavailable = v; p.mu.Unlock() }

// FailCreateAfter makes Create fail (unavailable) once n records were made;
// -1 turns it off.
func (p *Provider) FailCreateAfter(n int) { p.mu.Lock(); p.failCreate = n; p.mu.Unlock() }

// Records returns the zone's records now.
func (p *Provider) Records(zone string) []dns.Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.records[p.zones[zone]])
}

// Calls is how many provider calls were made.
func (p *Provider) Calls() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

// CallsOf counts the calls of one operation ("FindZone", "Records", "Create",
// "Update", "Delete").
func (p *Provider) CallsOf(op string) int { p.mu.Lock(); defer p.mu.Unlock(); return p.byOp[op] }

// ResetCalls forgets the calls counted so far.
func (p *Provider) ResetCalls() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls, p.byOp = 0, map[string]int{}
}

// Tokens lists the tokens the factory was given, in order.
func (p *Provider) Tokens() []string { p.mu.Lock(); defer p.mu.Unlock(); return slices.Clone(p.tokens) }

// Factory is the dns.Factory that builds clients of p.
func (p *Provider) Factory() dns.Factory {
	return func(kind, token string) (dns.Provider, error) {
		if kind != "cloudflare" {
			return nil, fmt.Errorf("dns provider %q: %w", kind, dns.ErrRefused)
		}
		p.mu.Lock()
		p.tokens = append(p.tokens, token)
		p.mu.Unlock()
		return &client{p: p, token: token}, nil
	}
}

type client struct {
	p     *Provider
	token string
}

// enter counts the call and returns the error a knob asks for. The caller
// holds p.mu.
func (c *client) enter(op string) error {
	p := c.p
	p.calls++
	p.byOp[op]++
	switch {
	case p.refuse, p.wantToken != "" && c.token != p.wantToken:
		return &dns.ProviderError{Op: op, Err: dns.ErrRefused}
	case p.unavailable:
		return &dns.ProviderError{Op: op, Err: dns.ErrUnavailable}
	}
	return nil
}

func (c *client) FindZone(_ context.Context, zone string) (string, error) {
	c.p.mu.Lock()
	defer c.p.mu.Unlock()
	if err := c.enter("FindZone"); err != nil {
		return "", err
	}
	id, ok := c.p.zones[zone]
	if !ok {
		return "", &dns.ProviderError{Op: "find zone", Err: dns.ErrZoneNotFound}
	}
	return id, nil
}

func (c *client) Records(_ context.Context, zoneID, name string) ([]dns.Record, error) {
	c.p.mu.Lock()
	defer c.p.mu.Unlock()
	if err := c.enter("Records"); err != nil {
		return nil, err
	}
	var out []dns.Record
	for _, r := range c.p.records[zoneID] {
		if name == "" || r.Name == name {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *client) Create(_ context.Context, zoneID string, r dns.Record) (dns.Record, error) {
	c.p.mu.Lock()
	defer c.p.mu.Unlock()
	if err := c.enter("Create"); err != nil {
		return dns.Record{}, err
	}
	if c.p.failCreate >= 0 && c.p.byOp["Create"] > c.p.failCreate {
		return dns.Record{}, &dns.ProviderError{Op: "create record", Err: dns.ErrUnavailable}
	}
	c.p.next++
	r.ID = fmt.Sprintf("rec%d", c.p.next)
	if r.TTL == 0 {
		r.TTL = 1
	}
	c.p.records[zoneID] = append(c.p.records[zoneID], r)
	return r, nil
}

func (c *client) Update(_ context.Context, zoneID string, r dns.Record) error {
	c.p.mu.Lock()
	defer c.p.mu.Unlock()
	if err := c.enter("Update"); err != nil {
		return err
	}
	i := slices.IndexFunc(c.p.records[zoneID], func(x dns.Record) bool { return x.ID == r.ID })
	if i < 0 {
		return &dns.ProviderError{Op: "update record", Err: dns.ErrRecordNotFound}
	}
	c.p.records[zoneID][i].Proxied = r.Proxied
	return nil
}

func (c *client) Delete(_ context.Context, zoneID, recordID string) error {
	c.p.mu.Lock()
	defer c.p.mu.Unlock()
	if err := c.enter("Delete"); err != nil {
		return err
	}
	c.p.records[zoneID] = slices.DeleteFunc(c.p.records[zoneID], func(x dns.Record) bool { return x.ID == recordID })
	return nil
}
