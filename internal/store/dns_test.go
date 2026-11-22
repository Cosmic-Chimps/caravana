package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func TestDNSZones(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 21, 9, 0, 0, 123456789, time.UTC)
	zone := func(name string) DNSZone {
		return DNSZone{Zone: name, Provider: "cloudflare", Proxied: true, CreatedAt: t0, UpdatedAt: t0}
	}
	s := openTestStore(t)

	if got, err := s.ListDNSZones(ctx); err != nil || len(got) != 0 {
		t.Errorf("ListDNSZones() = %v, %v; want none", got, err)
	}
	if _, err := s.GetDNSZone(ctx, "example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetDNSZone() error = %v, want ErrNotFound", err)
	}

	want := zone("example.com")
	if err := s.SaveDNSZone(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDNSZone(ctx, "example.com")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetDNSZone() = %+v, %v; want %+v", got, err, want)
	}

	// The test outcome and the provider's ID survive a save of the settings.
	test := DNSZoneTest{At: t0.Add(time.Minute), OK: true, Message: "12 records, 2 made by Caravana", Count: 12}
	if err := s.SaveDNSZoneTest(ctx, "example.com", test); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDNSZoneID(ctx, "example.com", "zone-1"); err != nil {
		t.Fatal(err)
	}
	later := zone("example.com")
	later.Proxied, later.UpdatedAt, later.CreatedAt = false, t0.Add(time.Hour), t0.Add(time.Hour)
	if err := s.SaveDNSZone(ctx, later); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDNSZone(ctx, "example.com")
	if got.Proxied || got.ZoneID != "zone-1" || !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0.Add(time.Hour)) ||
		got.LastTest == nil || !reflect.DeepEqual(*got.LastTest, test) {
		t.Errorf("zone after a second save = %+v", got)
	}

	if err := s.ClearDNSZoneTest(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetDNSZone(ctx, "example.com"); got.LastTest != nil {
		t.Errorf("LastTest after Clear = %+v", got.LastTest)
	}
	if err := s.SaveDNSZone(ctx, zone("a.org")); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListDNSZones(ctx); len(list) != 2 || list[0].Zone != "a.org" {
		t.Errorf("ListDNSZones() = %+v, want a.org first", list)
	}
	for name, err := range map[string]error{
		"test":  s.SaveDNSZoneTest(ctx, "nope.org", test),
		"clear": s.ClearDNSZoneTest(ctx, "nope.org"),
		"id":    s.SetDNSZoneID(ctx, "nope.org", "x"),
		"del":   s.DeleteDNSZone(ctx, "nope.org"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s of an unknown zone = %v, want ErrNotFound", name, err)
		}
	}
	if err := s.DeleteDNSZone(ctx, "a.org"); err != nil {
		t.Fatal(err)
	}
}

func TestDNSRecords(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	rec := func(host, typ, zone, appID, id string) DNSRecord {
		return DNSRecord{Host: host, Type: typ, Zone: zone, AppID: appID, RecordID: id, Content: "203.0.113.1", Proxied: true, CreatedAt: t0}
	}
	s := openTestStore(t)
	for _, r := range []DNSRecord{
		rec("web.example.com", "A", "example.com", "web", "r1"),
		rec("web.example.com", "AAAA", "example.com", "web", "r2"),
		rec("api.other.org", "A", "other.org", "api", "r3"),
	} {
		if err := s.InsertDNSRecord(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListDNSRecords(ctx)
	if err != nil || len(all) != 3 || all[0].Host != "api.other.org" {
		t.Fatalf("ListDNSRecords() = %+v, %v", all, err)
	}
	if got, _ := s.ListDNSRecordsInZone(ctx, "example.com"); len(got) != 2 {
		t.Errorf("by zone = %+v, want 2", got)
	}
	if got, _ := s.ListDNSRecordsOfApp(ctx, "api"); len(got) != 1 || got[0].RecordID != "r3" {
		t.Errorf("by app = %+v", got)
	}

	// The same provider ID is replaced, whoever had it (an adopted record).
	if err := s.InsertDNSRecord(ctx, rec("web.example.com", "A", "example.com", "web2", "r1")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListDNSRecordsOfApp(ctx, "web2"); len(got) != 1 || got[0].RecordID != "r1" {
		t.Errorf("after a replace = %+v", got)
	}
	// Another record of the same host and type is another row.
	if err := s.InsertDNSRecord(ctx, rec("web.example.com", "A", "example.com", "web2", "r9")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListDNSRecordsOfApp(ctx, "web2"); len(got) != 2 {
		t.Errorf("two A records of one host = %+v, want 2 rows", got)
	}
	if err := s.SetDNSRecordProxied(ctx, "r2", false); err != nil {
		t.Fatal(err)
	}
	for _, r := range mustList(t, s, "example.com") {
		if r.RecordID == "r2" && r.Proxied {
			t.Errorf("AAAA row still proxied: %+v", r)
		}
	}
	if err := s.DeleteDNSRecord(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDNSRecord(ctx, "r1"); err != nil {
		t.Errorf("DeleteDNSRecord() of a row that isn't there = %v", err)
	}
	if err := s.DeleteDNSRecordsInZone(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListDNSRecords(ctx); len(got) != 1 {
		t.Errorf("after dropping a zone's rows = %+v, want 1", got)
	}
}

func mustList(t *testing.T, s *Store, zone string) []DNSRecord {
	t.Helper()
	got, err := s.ListDNSRecordsInZone(context.Background(), zone)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Deleting an app leaves its records' rows: the records still exist at the
// provider, and the service removes them first (feature 048, D3).
func TestDeleteAppLeavesDNSRecords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	a := app.App{ID: "web", Source: app.Source{Kind: app.SourceImage}}
	if _, err := s.InsertApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	r := DNSRecord{Host: "web.example.com", Type: "A", Zone: "example.com", AppID: "web", RecordID: "r1", Content: "203.0.113.1", CreatedAt: time.Now()}
	if err := s.InsertDNSRecord(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteApp(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListDNSRecordsOfApp(ctx, "web"); len(got) != 1 {
		t.Errorf("rows after DeleteApp = %+v, want the one record's row kept", got)
	}
}
