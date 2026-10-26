package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/edge"
)

func testCheck(host, appID string) DomainCheckRecord {
	notAfter := time.Date(2027, 1, 20, 8, 30, 0, 0, time.UTC)
	return DomainCheckRecord{
		Host: host, AppID: appID, Service: "web",
		State: "live", DNSStatus: "ok",
		DNSSeen: []string{"203.0.113.7"}, DNSExpected: []string{"203.0.113.7", "2001:db8::1"},
		CertStatus: "issued", CertIssuer: "Let's Encrypt", CertNotAfter: &notAfter,
		CheckedAt: time.Date(2026, 10, 24, 9, 15, 30, 123456789, time.UTC),
	}
}

func TestDomainCheckRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	in := testCheck("shop.example.com", "shop")
	if err := s.UpsertDomainCheck(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDomainCheck(ctx, "shop.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Errorf("got %+v, want %+v", got, in)
	}

	// No certificate and no addresses: nil date, empty lists.
	bare := DomainCheckRecord{
		Host: "a.example.com", AppID: "shop", Service: "web", State: "not_served",
		DNSStatus: "unknown", CertStatus: "none", CheckedAt: in.CheckedAt,
	}
	if err := s.UpsertDomainCheck(ctx, bare); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetDomainCheck(ctx, "a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.CertNotAfter != nil || len(got.DNSSeen) != 0 || len(got.DNSExpected) != 0 {
		t.Errorf("bare check read back as %+v", got)
	}

	list, err := s.ListDomainChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Host != "a.example.com" || list[1].Host != "shop.example.com" {
		t.Errorf("list = %+v, want the two hosts by name", list)
	}

	if _, err := s.GetDomainCheck(ctx, "nope.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of an unknown host = %v, want ErrNotFound", err)
	}
}

func TestDomainCheckUpsertReplaces(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	in := testCheck("shop.example.com", "shop")
	if err := s.UpsertDomainCheck(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.State, in.DNSStatus = "dns_elsewhere", "elsewhere"
	in.DNSSeen = []string{"198.51.100.9"}
	in.CertStatus, in.CertIssuer, in.CertNotAfter = "pending", "Traefik's own", nil
	in.CheckedAt = in.CheckedAt.Add(time.Minute)
	in.Service = "api"
	if err := s.UpsertDomainCheck(ctx, in); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListDomainChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !reflect.DeepEqual(list[0], in) {
		t.Errorf("list = %+v, want the one replaced row %+v", list, in)
	}
}

func TestDomainCheckDeletes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, r := range []DomainCheckRecord{
		testCheck("a.example.com", "one"), testCheck("b.example.com", "one"), testCheck("c.example.com", "two"),
	} {
		if err := s.UpsertDomainCheck(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteDomainCheck(ctx, "a.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDomainCheck(ctx, "never.example.com"); err != nil {
		t.Errorf("deleting a host without a check = %v, want nil", err)
	}
	if _, err := s.GetDomainCheck(ctx, "a.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a.example.com still there: %v", err)
	}
	if err := s.DeleteDomainChecks(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListDomainChecks(ctx)
	if len(list) != 1 || list[0].Host != "c.example.com" {
		t.Errorf("after deleting app one's checks: %+v, want only c.example.com", list)
	}
}

func TestDomainCheckRefusesAWordItDoesNotKnow(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for name, mutate := range map[string]func(*DomainCheckRecord){
		"state":      func(r *DomainCheckRecord) { r.State = "healthy" },
		"dns status": func(r *DomainCheckRecord) { r.DNSStatus = "fine" },
		"cert state": func(r *DomainCheckRecord) { r.CertStatus = "valid" },
	} {
		r := testCheck("shop.example.com", "shop")
		mutate(&r)
		if err := s.UpsertDomainCheck(ctx, r); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("%s: err = %v, want a CHECK constraint failure", name, err)
		}
	}
}

// Every state of package edge is one the table accepts.
func TestDomainCheckAcceptsEveryEdgeState(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, st := range edge.States {
		r := testCheck("shop.example.com", "shop")
		r.State = string(st)
		if err := s.UpsertDomainCheck(ctx, r); err != nil {
			t.Errorf("state %q: %v", st, err)
		}
	}
	for _, v := range []string{edge.DNSOK, edge.DNSElsewhere, edge.DNSNone, edge.DNSUnknown} {
		r := testCheck("shop.example.com", "shop")
		r.DNSStatus = v
		if err := s.UpsertDomainCheck(ctx, r); err != nil {
			t.Errorf("dns status %q: %v", v, err)
		}
	}
	for _, v := range []string{edge.CertIssued, edge.CertPending, edge.CertSelf, edge.CertExpiring, edge.CertError, edge.CertNone} {
		r := testCheck("shop.example.com", "shop")
		r.CertStatus = v
		if err := s.UpsertDomainCheck(ctx, r); err != nil {
			t.Errorf("cert status %q: %v", v, err)
		}
	}
}

func TestTeamPasswords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	at := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

	if _, err := s.TeamPassword(ctx, "a.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("TeamPassword of an unknown host = %v, want ErrNotFound", err)
	}
	if err := s.SetTeamPassword(ctx, "a.example.com", "one", "$2a$10$aaaa", at); err != nil {
		t.Fatal(err)
	}
	got, err := s.TeamPassword(ctx, "a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := TeamPasswordRecord{Host: "a.example.com", AppID: "one", Hash: "$2a$10$aaaa", CreatedAt: at}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// A rotation replaces the hash and the time.
	later := at.Add(time.Hour)
	if err := s.SetTeamPassword(ctx, "a.example.com", "one", "$2a$10$bbbb", later); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TeamPassword(ctx, "a.example.com")
	if got.Hash != "$2a$10$bbbb" || !got.CreatedAt.Equal(later) {
		t.Errorf("after rotation: %+v", got)
	}

	for host, app := range map[string]string{"b.example.com": "one", "c.example.com": "two"} {
		if err := s.SetTeamPassword(ctx, host, app, "$2a$10$x", at); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.TeamPasswords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Host != "a.example.com" || all[2].Host != "c.example.com" {
		t.Errorf("TeamPasswords = %+v, want three by host", all)
	}

	if err := s.DeleteTeamPassword(ctx, "a.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTeamPassword(ctx, "a.example.com"); err != nil {
		t.Errorf("deleting twice = %v, want nil", err)
	}
	if err := s.DeleteTeamPasswords(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	all, _ = s.TeamPasswords(ctx)
	if len(all) != 1 || all[0].Host != "c.example.com" {
		t.Errorf("after deleting app one's: %+v, want only c.example.com", all)
	}
}

func TestCertificateSettings(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	days, updated, err := s.CertificateSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if days != 14 {
		t.Errorf("default = %d days, want 14", days)
	}
	if updated.IsZero() {
		t.Error("the migration's row has no time")
	}

	if err := s.SetWarnBeforeDays(ctx, 7); err != nil {
		t.Fatal(err)
	}
	days, updated2, err := s.CertificateSettings(ctx)
	if err != nil || days != 7 {
		t.Fatalf("after set: %d, %v, want 7", days, err)
	}
	// The migration stamps the real time; the test store's clock is a fixed one.
	if want := time.Date(2026, 10, 2, 12, 0, 1, 0, time.UTC); !updated2.Equal(want) {
		t.Errorf("updatedAt = %v, want the store clock's %v", updated2, want)
	}
	for _, ok := range []int{30, 14} {
		if err := s.SetWarnBeforeDays(ctx, ok); err != nil {
			t.Errorf("SetWarnBeforeDays(%d) = %v", ok, err)
		}
	}
	for _, bad := range []int{0, 1, 10, 15, 60, -7} {
		if err := s.SetWarnBeforeDays(ctx, bad); err == nil {
			t.Errorf("SetWarnBeforeDays(%d) = nil, want a refusal", bad)
		}
	}
	if days, _, _ := s.CertificateSettings(ctx); days != 14 {
		t.Errorf("a refused value changed the setting to %d", days)
	}

	// The table itself refuses a value outside 7, 14, 30.
	if _, err := s.db.ExecContext(ctx, "UPDATE certificate_settings SET warn_before_days = 9"); err == nil {
		t.Error("the table accepted 9 days")
	}
}

func TestDeleteAppDeletesDomainRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	appID := scheduleFixture(t, s)
	other := "other"

	for _, id := range []string{appID, other} {
		if err := s.UpsertDomainCheck(ctx, testCheck(id+".example.com", id)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTeamPassword(ctx, id+".example.com", id, "$2a$10$x", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteApp(ctx, appID); err != nil {
		t.Fatal(err)
	}
	checks, _ := s.ListDomainChecks(ctx)
	passwords, _ := s.TeamPasswords(ctx)
	if len(checks) != 1 || checks[0].AppID != other || len(passwords) != 1 || passwords[0].AppID != other {
		t.Errorf("after DeleteApp: checks %+v, passwords %+v, want only %q's", checks, passwords, other)
	}
}
