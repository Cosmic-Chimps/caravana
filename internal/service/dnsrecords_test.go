package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/dns/dnstest"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// dnsSentinel is a token no answer, error, log line or row may hold.
const dnsSentinel = "cf-s3cret-sentinel-048-service"

// dnsEnv is a service whose console resolves to 203.0.113.1 (and 2001:db8::1
// when v6), with a fake provider that knows example.com and a zone saved for it.
type dnsEnv struct {
	*teamEnv
	p *dnstest.Provider
}

func newDNSEnv(t *testing.T, v6 bool) *dnsEnv {
	t.Helper()
	e := &dnsEnv{teamEnv: newTeamEnv(t), p: dnstest.NewProvider()}
	e.svc.SetPublicURL("https://console.example.com")
	addrs := []string{hostV4.String()}
	if v6 {
		addrs = append(addrs, hostV6.String())
	}
	e.svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{"console.example.com": addrs}}, nil)
	e.p.AddZone("example.com")
	e.p.WantToken(dnsSentinel)
	e.svc.SetDNSProviders(e.p.Factory())
	e.saveZone(t, "example.com", false)
	e.p.ResetCalls()
	return e
}

func (e *dnsEnv) saveZone(t *testing.T, zone string, proxied bool) {
	t.Helper()
	if _, _, err := e.svc.SaveDNSZone(context.Background(), DNSZoneInput{Zone: zone, Provider: "cloudflare", Proxied: proxied, Token: new(dnsSentinel)}); err != nil {
		t.Fatalf("SaveDNSZone(%s): %v", zone, err)
	}
}

func hostDomains(hosts ...string) []app.Domain {
	var out []app.Domain
	for _, h := range hosts {
		out = append(out, app.Domain{Host: h})
	}
	return out
}

func (e *dnsEnv) deploy(t *testing.T, id string, hosts ...string) {
	t.Helper()
	e.teamEnv.deployResult(t, domainApp(id, hostDomains(hosts...)...))
}

func commented(recs []dns.Record, comment string) []dns.Record {
	var out []dns.Record
	for _, r := range recs {
		if r.Comment == comment {
			out = append(out, r)
		}
	}
	return out
}

func TestSaveDNSZoneValidation(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	tests := []struct {
		name      string
		in        DNSZoneInput
		wantField string
	}{
		{"fine", DNSZoneInput{Zone: "other.org", Provider: "cloudflare", Token: new("t")}, ""},
		{"capitals are lowered", DNSZoneInput{Zone: "Third.ORG", Provider: "cloudflare", Token: new("t")}, ""},
		{"no dot", DNSZoneInput{Zone: "localhost", Provider: "cloudflare", Token: new("t")}, "zone"},
		{"an address", DNSZoneInput{Zone: "203.0.113.1", Provider: "cloudflare", Token: new("t")}, "zone"},
		{"a path", DNSZoneInput{Zone: "a.org/x", Provider: "cloudflare", Token: new("t")}, "zone"},
		{"another provider", DNSZoneInput{Zone: "x.org", Provider: "route53", Token: new("t")}, "provider"},
		{"no token the first time", DNSZoneInput{Zone: "y.org", Provider: "cloudflare"}, "token"},
		{"an empty token", DNSZoneInput{Zone: "z.org", Provider: "cloudflare", Token: new("")}, "token"},
		{"no token needed on an edit", DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Proxied: true}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := e.svc.SaveDNSZone(ctx, tc.in)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("SaveDNSZone() = %v", err)
				}
				return
			}
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.wantField {
				t.Fatalf("SaveDNSZone() = %v, want a field error on %q", err, tc.wantField)
			}
		})
	}
	zones, _ := e.svc.DNSZones(ctx)
	var names []string
	for _, z := range zones {
		names = append(names, z.Zone)
	}
	if strings.Join(names, " ") != "example.com other.org third.org" {
		t.Errorf("zones = %v, want example.com, other.org and third.org", names)
	}
}

func TestSaveDNSZoneWithoutProviders(t *testing.T) {
	e := newTeamEnv(t)
	_, _, err := e.svc.SaveDNSZone(context.Background(), DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Token: new("t")})
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "provider" {
		t.Errorf("SaveDNSZone() with no factory = %v, want a field error on provider", err)
	}
}

func TestDNSZoneTestListsRecords(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	e.p.Seed("example.com", dns.Record{Type: "A", Name: "hand.example.com", Content: "198.51.100.7"})
	e.deploy(t, "web", "web.example.com")

	res, err := e.svc.TestDNSZone(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if res.Zone.LastTest == nil || !res.Zone.LastTest.OK || res.Zone.LastTest.Message != "2 records, 1 made by Caravana" || res.Zone.LastTest.Count != 2 {
		t.Errorf("last test = %+v", res.Zone.LastTest)
	}
	if res.Zone.ZoneID == "" {
		t.Error("the test didn't store the provider's zone ID")
	}
	if res.Zone.Records != 1 || len(res.Records) != 2 {
		t.Fatalf("zone records = %d, listed = %+v", res.Zone.Records, res.Records)
	}
	for _, r := range res.Records {
		if r.Managed != (r.Name == "web.example.com") || (r.Managed && r.App != "web") {
			t.Errorf("record %+v: managed marks are wrong", r)
		}
	}
	// The records themselves are in no stored row.
	for _, row := range storeRows(t, e.dir) {
		if strings.Contains(row, "hand.example.com") {
			t.Errorf("the provider's record is stored: %s", row)
		}
	}

	t.Run("a refusing provider is an outcome", func(t *testing.T) {
		e.p.SetRefuse(true)
		defer e.p.SetRefuse(false)
		res, err := e.svc.TestDNSZone(ctx, "example.com")
		if err != nil || res.Zone.LastTest == nil || res.Zone.LastTest.OK || len(res.Records) != 0 {
			t.Fatalf("TestDNSZone() = %+v, %v; want a failed outcome with no records", res, err)
		}
		if !strings.Contains(res.Zone.LastTest.Message, "refused the token") {
			t.Errorf("message = %q", res.Zone.LastTest.Message)
		}
	})
	t.Run("an unreachable provider is an outcome", func(t *testing.T) {
		e.p.SetUnavailable(true)
		defer e.p.SetUnavailable(false)
		res, _ := e.svc.TestDNSZone(ctx, "example.com")
		if res.Zone.LastTest == nil || res.Zone.LastTest.OK || !strings.Contains(res.Zone.LastTest.Message, "could not be reached") {
			t.Errorf("last test = %+v", res.Zone.LastTest)
		}
	})
	t.Run("a zone the provider lacks", func(t *testing.T) {
		e.saveZone(t, "nowhere.org", false)
		res, _ := e.svc.TestDNSZone(ctx, "nowhere.org")
		if res.Zone.LastTest == nil || res.Zone.LastTest.OK || !strings.Contains(res.Zone.LastTest.Message, "no zone nowhere.org") {
			t.Errorf("last test = %+v", res.Zone.LastTest)
		}
	})
	t.Run("an unknown zone", func(t *testing.T) {
		if _, err := e.svc.TestDNSZone(ctx, "unknown.org"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("TestDNSZone(unknown) = %v, want ErrNotFound", err)
		}
	})
}

func TestRecordsFollowDomains(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, true)

	// A deploy with a domain under the zone makes both records, marked.
	e.deploy(t, "web", "web.example.com")
	recs := commented(e.p.Records("example.com"), "caravana:web")
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want an A and an AAAA", e.p.Records("example.com"))
	}
	got := map[string]string{}
	for _, r := range recs {
		got[r.Type] = r.Content
		if r.Name != "web.example.com" || r.TTL != 1 || r.Proxied {
			t.Errorf("record = %+v", r)
		}
	}
	if got["A"] != "203.0.113.1" || got["AAAA"] != "2001:db8::1" {
		t.Errorf("record contents = %v", got)
	}
	v, err := e.svc.Domain(ctx, "web.example.com")
	if err != nil || v.Record == nil || v.Record.Zone != "example.com" || v.Record.Provider != "cloudflare" || v.Record.Proxied {
		t.Errorf("Domain().Record = %+v, %v", v.Record, err)
	}

	// A redeploy and a deploy of the same domains make no provider call.
	e.p.ResetCalls()
	if _, err := e.svc.Redeploy(ctx, "web", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	e.deploy(t, "web", "web.example.com")
	if n := e.p.Calls(); n != 0 {
		t.Errorf("provider calls on a redeploy = %d, want 0", n)
	}

	// A deploy without the domain removes both records and their rows.
	e.deploy(t, "web", "other.example.com") // a different host: the first goes, the second comes
	if left := commented(e.p.Records("example.com"), "caravana:web"); len(left) != 2 || left[0].Name != "other.example.com" {
		t.Errorf("records after moving to another host = %+v", left)
	}
	if rows, _ := e.store.ListDNSRecordsOfApp(ctx, "web"); len(rows) != 2 || rows[0].Host != "other.example.com" {
		t.Errorf("rows = %+v", rows)
	}

	// Deleting the app removes its records.
	if err := e.svc.DeleteApp(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if left := e.p.Records("example.com"); len(left) != 0 {
		t.Errorf("records after DeleteApp = %+v", left)
	}
	if rows, _ := e.store.ListDNSRecords(ctx); len(rows) != 0 {
		t.Errorf("rows after DeleteApp = %+v", rows)
	}
}

func TestRecordsOfAHostWithAPairAndOutsideTheZone(t *testing.T) {
	e := newDNSEnv(t, false)
	a := domainApp("web", app.Domain{Host: "example.com", WWWRedirect: true}, app.Domain{Host: "elsewhere.net"})
	e.teamEnv.deployResult(t, a)
	hosts := map[string]bool{}
	for _, r := range e.p.Records("example.com") {
		hosts[r.Name] = true
	}
	if len(hosts) != 2 || !hosts["example.com"] || !hosts["www.example.com"] {
		t.Errorf("records for the apex and its www pair = %v (and none for elsewhere.net)", hosts)
	}
}

func TestProxySettingOfNewRecords(t *testing.T) {
	e := newDNSEnv(t, false)
	e.saveZone(t, "example.com", true)
	e.deploy(t, "web", "web.example.com")
	if recs := e.p.Records("example.com"); len(recs) != 1 || !recs[0].Proxied {
		t.Errorf("records = %+v, want one proxied", recs)
	}
}

func TestADomainWhoseRecordPointsElsewhereIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		rec   dns.Record
		app   app.App
		field string
	}{
		{"an A record", dns.Record{Type: "A", Name: "taken.example.com", Content: "198.51.100.9"},
			domainApp("web", hostDomains("web.example.com", "taken.example.com")...), "services[0].domains[1].host"},
		{"a CNAME", dns.Record{Type: "CNAME", Name: "taken.example.com", Content: "lb.example.net"},
			domainApp("web", hostDomains("taken.example.com")...), "services[0].domains[0].host"},
		{"the www pair", dns.Record{Type: "A", Name: "www.example.com", Content: "198.51.100.9"},
			domainApp("web", app.Domain{Host: "example.com", WWWRedirect: true}), "services[0].domains[0].wwwRedirect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDNSEnv(t, false)
			e.deploy(t, "web", "web.example.com") // the app exists, at version one
			e.p.Seed("example.com", tc.rec)
			before, _ := e.store.GetApp(ctx, "web")
			beforeRecs := len(e.p.Records("example.com"))

			_, err := e.svc.DeployIfResult(ctx, tc.app, Condition{}, app.TriggerManual)
			var ex *DNSRecordExistsError
			if !errors.As(err, &ex) || !errors.Is(err, ErrDNSRecordExists) {
				t.Fatalf("deploy = %v, want a DNSRecordExistsError", err)
			}
			if ex.Field != tc.field || ex.Host != tc.rec.Name || ex.Content != tc.rec.Content || ex.Zone != "example.com" {
				t.Errorf("error = %+v, want field %q host %q", ex, tc.field, tc.rec.Name)
			}
			after, _ := e.store.GetApp(ctx, "web")
			if after.Revision != before.Revision {
				t.Error("the refused deploy changed the stored app")
			}
			if got := len(e.p.Records("example.com")); got != beforeRecs {
				t.Errorf("records after a refusal = %d, want %d", got, beforeRecs)
			}
		})
	}
}

func TestAHandMadeRecordPointingHereIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	hand := e.p.Seed("example.com", dns.Record{Type: "A", Name: "web.example.com", Content: "203.0.113.1", Comment: "mine"})
	e.deploy(t, "web", "web.example.com")
	if recs := e.p.Records("example.com"); len(recs) != 1 || recs[0].ID != hand.ID || recs[0].Comment != "mine" {
		t.Errorf("records = %+v, want the hand-made one untouched", recs)
	}
	if rows, _ := e.store.ListDNSRecords(ctx); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the record isn't Caravana's", rows)
	}
	// Dropping the domain never removes it.
	e.deploy(t, "web", "again.example.com")
	found := false
	for _, r := range e.p.Records("example.com") {
		found = found || r.ID == hand.ID
	}
	if !found {
		t.Error("the hand-made record was removed")
	}
}

func TestARecordWithOurCommentAndNoRowIsAdopted(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	lost := e.p.Seed("example.com", dns.Record{Type: "A", Name: "web.example.com", Content: "203.0.113.1", Comment: "caravana:web"})
	e.deploy(t, "web", "web.example.com")
	if recs := e.p.Records("example.com"); len(recs) != 1 {
		t.Errorf("records = %+v, want the adopted one only", recs)
	}
	if rows, _ := e.store.ListDNSRecordsOfApp(ctx, "web"); len(rows) != 1 || rows[0].RecordID != lost.ID {
		t.Errorf("rows = %+v, want the adopted record's", rows)
	}
}

func TestProviderFailuresRefuseTheDeploy(t *testing.T) {
	ctx := context.Background()
	t.Run("refused", func(t *testing.T) {
		e := newDNSEnv(t, false)
		e.p.SetRefuse(true)
		_, err := e.svc.DeployIfResult(ctx, domainApp("web", hostDomains("web.example.com")...), Condition{}, app.TriggerManual)
		var pe *DNSProviderError
		if !errors.As(err, &pe) || !errors.Is(err, dns.ErrRefused) || pe.Field != "services[0].domains[0].host" {
			t.Fatalf("deploy = %v, want a refused DNSProviderError at the host", err)
		}
		if _, err := e.store.GetApp(ctx, "web"); !errors.Is(err, store.ErrNotFound) {
			t.Error("the app was saved though the check refused it")
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		e := newDNSEnv(t, false)
		e.p.SetUnavailable(true)
		_, err := e.svc.DeployIfResult(ctx, domainApp("web", hostDomains("web.example.com")...), Condition{}, app.TriggerManual)
		if !errors.Is(err, dns.ErrUnavailable) {
			t.Fatalf("deploy = %v, want unavailable", err)
		}
	})
	t.Run("a zone the provider doesn't know", func(t *testing.T) {
		e := newDNSEnv(t, false)
		e.saveZone(t, "nowhere.org", false)
		_, err := e.svc.DeployIfResult(ctx, domainApp("web", hostDomains("web.nowhere.org")...), Condition{}, app.TriggerManual)
		if !errors.Is(err, dns.ErrRefused) || !strings.Contains(err.Error(), "no zone nowhere.org") {
			t.Fatalf("deploy = %v, want refused: no such zone", err)
		}
	})
	t.Run("the address isn't known", func(t *testing.T) {
		e := newDNSEnv(t, false)
		e.svc.SetPublicURL("")
		_, err := e.svc.DeployIfResult(ctx, domainApp("web", hostDomains("web.example.com")...), Condition{}, app.TriggerManual)
		if !errors.Is(err, ErrHostAddressUnknown) {
			t.Fatalf("deploy = %v, want ErrHostAddressUnknown", err)
		}
		if e.p.Calls() != 0 {
			t.Error("the provider was called with no address to make a record for")
		}
	})
	t.Run("a deploy outside every zone never calls the provider", func(t *testing.T) {
		e := newDNSEnv(t, false)
		e.p.SetRefuse(true)
		e.deploy(t, "plain", "plain.elsewhere.net")
		if e.p.Calls() != 0 {
			t.Errorf("provider calls = %d, want 0", e.p.Calls())
		}
	})
}

// A failure half-way leaves what was made owned, and the next deploy makes the
// rest without making the first again.
func TestACreateFailingHalfWayKeepsWhatWasMade(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	e.p.FailCreateAfter(1)
	res, err := e.svc.DeployIfResult(ctx, domainApp("web", hostDomains("one.example.com", "two.example.com")...), Condition{}, app.TriggerManual)
	if !errors.Is(err, dns.ErrUnavailable) {
		t.Fatalf("deploy = %v, want unavailable", err)
	}
	var de *DeployError
	if !errors.As(err, &de) {
		t.Fatalf("error %v is not a DeployError: the deployment should be recorded as failed", err)
	}
	_ = res
	if rows, _ := e.store.ListDNSRecordsOfApp(ctx, "web"); len(rows) != 1 || rows[0].Host != "one.example.com" {
		t.Fatalf("rows = %+v, want the one record made", rows)
	}
	e.p.FailCreateAfter(-1)
	e.p.ResetCalls()
	e.deploy(t, "web", "one.example.com", "two.example.com")
	if n := e.p.CallsOf("Create"); n != 1 {
		t.Errorf("Create calls on the second deploy = %d, want 1", n)
	}
	if recs := e.p.Records("example.com"); len(recs) != 2 {
		t.Errorf("records = %+v, want 2", recs)
	}
}

// A removal that fails never fails the deploy or the delete; the row stays and
// the pass removes the record later.
func TestAFailedRemovalIsLeftToThePass(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	e.deploy(t, "web", "web.example.com", "old.example.com")

	e.p.SetUnavailable(true)
	e.deploy(t, "web", "web.example.com") // old.example.com goes; the provider is down
	rows, _ := e.store.ListDNSRecordsOfApp(ctx, "web")
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want both kept", rows)
	}
	e.svc.dnsPass(ctx) // still down: nothing changes
	if rows, _ = e.store.ListDNSRecordsOfApp(ctx, "web"); len(rows) != 2 {
		t.Fatalf("rows after a failing pass = %+v", rows)
	}
	e.p.SetUnavailable(false)
	e.svc.dnsPass(ctx)
	if rows, _ = e.store.ListDNSRecordsOfApp(ctx, "web"); len(rows) != 1 || rows[0].Host != "web.example.com" {
		t.Errorf("rows after the pass = %+v, want only web.example.com", rows)
	}
	if recs := e.p.Records("example.com"); len(recs) != 1 || recs[0].Name != "web.example.com" {
		t.Errorf("records after the pass = %+v", recs)
	}

	// The same for a delete.
	e.p.SetUnavailable(true)
	if err := e.svc.DeleteApp(ctx, "web"); err != nil {
		t.Fatalf("DeleteApp() with the provider down = %v, want nil", err)
	}
	if rows, _ := e.store.ListDNSRecords(ctx); len(rows) != 1 {
		t.Fatalf("rows after a delete with the provider down = %+v, want the row kept", rows)
	}
	e.p.SetUnavailable(false)
	e.svc.dnsPass(ctx)
	if rows, _ := e.store.ListDNSRecords(ctx); len(rows) != 0 {
		t.Errorf("rows after the pass = %+v", rows)
	}
	if recs := e.p.Records("example.com"); len(recs) != 0 {
		t.Errorf("records after the pass = %+v", recs)
	}
}

func TestRemovingAZoneKeepsTheRecords(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	e.deploy(t, "web", "web.example.com")
	if err := e.svc.DeleteDNSZone(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if recs := e.p.Records("example.com"); len(recs) != 1 {
		t.Errorf("records after removing the zone = %+v, want the one left at the provider", recs)
	}
	if rows, _ := e.store.ListDNSRecords(ctx); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
	if _, ok, _ := e.svc.deployer.DNSZoneTokenFor(ctx, "example.com"); ok {
		t.Error("the token is still stored")
	}
	if err := e.svc.DeleteDNSZone(ctx, "example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteDNSZone() twice = %v, want ErrNotFound", err)
	}
	// With the zone gone, a deploy under it calls no provider.
	e.p.ResetCalls()
	e.deploy(t, "web2", "web2.example.com")
	if e.p.Calls() != 0 {
		t.Errorf("provider calls after the zone was removed = %d", e.p.Calls())
	}
}

func TestChangingTheProxySettingUpdatesTheRecords(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	e.deploy(t, "web", "web.example.com")
	hand := e.p.Seed("example.com", dns.Record{Type: "A", Name: "hand.example.com", Content: "198.51.100.7"})

	info, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Proxied: true})
	if err != nil || !info.Proxied {
		t.Fatalf("SaveDNSZone() = %+v, %v", info, err)
	}
	for _, r := range e.p.Records("example.com") {
		if want := r.Name == "web.example.com"; r.Proxied != want {
			t.Errorf("record %s proxied = %v, want %v (a hand-made record is never touched)", r.Name, r.Proxied, want)
		}
	}
	if rows, _ := e.store.ListDNSRecordsInZone(ctx, "example.com"); !rows[0].Proxied {
		t.Error("the row didn't follow")
	}
	_ = hand

	// A provider that can't take it: the zone is saved, with an error that says so, and the next save tries again.
	e.p.SetUnavailable(true)
	info, _, err = e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Proxied: false})
	var pe *DNSProviderError
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "saved, but the old setting stays on 1 record; the next save tries again") {
		t.Fatalf("SaveDNSZone() with the provider down = %v", err)
	}
	if info.Zone != "example.com" || info.Proxied {
		t.Errorf("the saved zone didn't come back: %+v", info)
	}
	e.p.SetUnavailable(false)
	if _, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Proxied: false}); err != nil {
		t.Fatalf("the next save = %v", err)
	}
	for _, r := range e.p.Records("example.com") {
		if r.Name == "web.example.com" && r.Proxied {
			t.Error("the record kept the old setting after the retry")
		}
	}
}

func TestTheTokenIsKeptOnAnEditAndForgottenTestsOnChange(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	if _, err := e.svc.TestDNSZone(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	// An edit without a token keeps the stored one, and the test with a same setting.
	if info, created, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.com", Provider: "cloudflare"}); err != nil || created || info.LastTest == nil {
		t.Fatalf("a save with nothing changed = %+v, %v, %v; want the test kept", info, created, err)
	}
	if got := e.p.Tokens(); got[len(got)-1] != dnsSentinel {
		t.Errorf("tokens used = %v", got)
	}
	// A new token or proxy setting forgets the test.
	info, _, err := e.svc.SaveDNSZone(ctx, DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Token: new(dnsSentinel)})
	if err != nil || info.LastTest != nil || info.ZoneID != "" {
		t.Errorf("after a new token: %+v, %v; want no test and no zone ID", info, err)
	}
}

// The token is in no error, log line, stored row or file.
func TestDNSTokenSentinel(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	e.deploy(t, "web", "web.example.com")
	e.p.Seed("example.com", dns.Record{Type: "A", Name: "taken.example.com", Content: "198.51.100.9"})
	_, err := e.svc.DeployIfResult(ctx, domainApp("web", hostDomains("web.example.com", "taken.example.com")...), Condition{}, app.TriggerManual)
	collect(err)
	e.p.WantToken("another-token")
	_, err = e.svc.DeployIfResult(ctx, domainApp("web", hostDomains("web.example.com", "new.example.com")...), Condition{}, app.TriggerManual)
	collect(err)
	res, err := e.svc.TestDNSZone(ctx, "example.com")
	collect(err)
	e.p.SetUnavailable(true)
	collect(e.svc.DeleteApp(ctx, "web"))
	e.svc.dnsPass(ctx)
	e.p.SetUnavailable(false)
	e.p.WantToken(dnsSentinel)
	e.svc.dnsPass(ctx)

	for _, err := range errs {
		if strings.Contains(err.Error(), dnsSentinel) {
			t.Errorf("an error holds the token: %v", err)
		}
	}
	if res.Zone.LastTest != nil && strings.Contains(res.Zone.LastTest.Message, dnsSentinel) {
		t.Errorf("the test outcome holds the token: %q", res.Zone.LastTest.Message)
	}
	if strings.Contains(e.logs.String(), dnsSentinel) {
		t.Error("the log holds the token")
	}
	for _, row := range storeRows(t, e.dir) {
		if strings.Contains(row, dnsSentinel) {
			t.Errorf("a stored row holds the token: %s", row)
		}
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, "caravana.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), dnsSentinel) {
		t.Error("the SQLite file holds the token")
	}
	if tok, ok, _ := e.svc.deployer.DNSZoneTokenFor(ctx, "example.com"); !ok || tok != dnsSentinel {
		t.Errorf("the token isn't in its Nomad Variable: %q, %v", tok, ok)
	}
}

func TestManagedZoneAndDomainCheck(t *testing.T) {
	ctx := context.Background()
	e := newDNSEnv(t, false)
	e.saveZone(t, "dev.example.com", false)
	for host, want := range map[string]string{
		"web.example.com":        "example.com",
		"a.b.dev.example.com":    "dev.example.com",
		"example.com":            "example.com",
		"web.elsewhere.net":      "",
		"notexample.com":         "",
		"WEB.Example.com.":       "example.com",
		"dev.example.com.evil":   "",
		"web.dev.example.com":    "dev.example.com",
		"x.deep.dev.example.com": "dev.example.com",
	} {
		z, ok, err := e.svc.ManagedZone(ctx, host)
		if err != nil || z.Zone != want || ok != (want != "") {
			t.Errorf("ManagedZone(%q) = %q, %v, %v; want %q", host, z.Zone, ok, err, want)
		}
	}
	c, err := e.svc.CheckDomain(ctx, "web.example.com", "")
	if err != nil || c.ManagedZone != "example.com" {
		t.Errorf("CheckDomain().ManagedZone = %q, %v", c.ManagedZone, err)
	}
	c, err = e.svc.CheckDomain(ctx, "web.elsewhere.net", "")
	if err != nil || c.ManagedZone != "" {
		t.Errorf("CheckDomain().ManagedZone of an unmanaged host = %q, %v", c.ManagedZone, err)
	}
}
