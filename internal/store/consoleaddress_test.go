package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var caT0 = time.Date(2026, 11, 23, 9, 30, 0, 123456789, time.UTC)

func newConsoleChange(id string, at time.Time) ConsoleAddressChange {
	return ConsoleAddressChange{ID: id, Domain: "console2.example.com", OldDomain: "1-2-3-4.sslip.io", RedirectDays: 30, CreatedAt: at}
}

func TestConsoleAddressChanges(t *testing.T) {
	ctx := context.Background()
	id1, id2, id3 := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)

	t.Run("a new store has none", func(t *testing.T) {
		s := openTestStore(t)
		if _, ok, err := s.OpenConsoleAddressChange(ctx); err != nil || ok {
			t.Errorf("OpenConsoleAddressChange() = %v, %v; want none", ok, err)
		}
		if _, ok, err := s.LatestConsoleAddressChange(ctx); err != nil || ok {
			t.Errorf("LatestConsoleAddressChange() = %v, %v; want none", ok, err)
		}
		if got, err := s.ConsoleOldDomains(ctx, caT0); err != nil || len(got) != 0 {
			t.Errorf("ConsoleOldDomains() = %v, %v; want none", got, err)
		}
		if _, err := s.ConsoleAddressChange(ctx, id1); !errors.Is(err, ErrNotFound) {
			t.Errorf("ConsoleAddressChange(unknown) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("insert, read back every field, move through the states", func(t *testing.T) {
		s := openTestStore(t)
		c := newConsoleChange(id1, caT0)
		c.Checks = []ConsoleCheck{{Name: "hostname", State: "ok", Message: "console2.example.com is a host name"}}
		got, err := s.InsertConsoleAddressChange(ctx, c)
		if err != nil || got.Status != ConsoleChecking {
			t.Fatalf("Insert = %+v, %v; want status checking", got, err)
		}
		open, ok, err := s.OpenConsoleAddressChange(ctx)
		if err != nil || !ok {
			t.Fatalf("OpenConsoleAddressChange() = %v, %v; want the change", ok, err)
		}
		if open.ID != id1 || open.Domain != c.Domain || open.OldDomain != c.OldDomain || open.RedirectDays != 30 ||
			!open.CreatedAt.Equal(caT0) || len(open.Checks) != 1 || open.Checks[0] != c.Checks[0] ||
			open.DNSRecordCreated || !open.ReadyAt.IsZero() || !open.RedirectUntil.IsZero() {
			t.Errorf("open change = %+v, lost a field", open)
		}

		ready := caT0.Add(40 * time.Second)
		open.Status, open.ReadyAt = ConsoleReady, ready
		open.DNSRecordCreated = true
		open.Checks = append(open.Checks, ConsoleCheck{Name: "dns", State: "ok", Message: "resolves here"})
		if err := s.UpdateConsoleAddressChange(ctx, open, ConsoleChecking); err != nil {
			t.Fatalf("checking -> ready: %v", err)
		}
		again, _ := s.ConsoleAddressChange(ctx, id1)
		if again.Status != ConsoleReady || !again.ReadyAt.Equal(ready) || !again.DNSRecordCreated || len(again.Checks) != 2 {
			t.Errorf("ready change = %+v", again)
		}

		// A second update from the state it already left changes nothing.
		cancelled := again
		cancelled.Status = ConsoleCancelled
		if err := s.UpdateConsoleAddressChange(ctx, cancelled, ConsoleChecking); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("update from the wrong state: err = %v, want ErrInvalidTransition", err)
		}
		if err := s.UpdateConsoleAddressChange(ctx, newConsoleChange(id3, caT0), ConsoleChecking); !errors.Is(err, ErrNotFound) {
			t.Errorf("update of an unknown ID: err = %v, want ErrNotFound", err)
		}
		if err := s.UpdateConsoleAddressChange(ctx, again); err == nil {
			t.Error("update without a state to change from was accepted")
		}
		if now, _ := s.ConsoleAddressChange(ctx, id1); now.Status != ConsoleReady {
			t.Errorf("a refused update changed the status to %q", now.Status)
		}
	})

	t.Run("one change is open at a time", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.InsertConsoleAddressChange(ctx, newConsoleChange(id1, caT0)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InsertConsoleAddressChange(ctx, newConsoleChange(id2, caT0.Add(time.Minute))); !errors.Is(err, ErrConsoleChangeOpen) {
			t.Fatalf("second open change: err = %v, want ErrConsoleChangeOpen", err)
		}
		if _, err := s.InsertConsoleAddressChange(ctx, newConsoleChange(id1, caT0)); !errors.Is(err, ErrConsoleChangeOpen) && !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("same ID again: err = %v", err)
		}
		// Once it is settled another may start; the newest is the latest.
		first, _ := s.ConsoleAddressChange(ctx, id1)
		first.Status = ConsoleCancelled
		first.SettledAt = caT0.Add(time.Second)
		if err := s.UpdateConsoleAddressChange(ctx, first, ConsoleChecking, ConsoleReady); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.OpenConsoleAddressChange(ctx); ok {
			t.Error("a cancelled change is still open")
		}
		if _, err := s.InsertConsoleAddressChange(ctx, newConsoleChange(id2, caT0.Add(time.Minute))); err != nil {
			t.Fatalf("a new change after a settled one: %v", err)
		}
		if latest, ok, err := s.LatestConsoleAddressChange(ctx); err != nil || !ok || latest.ID != id2 {
			t.Errorf("latest = %q, %v, %v; want %q", latest.ID, ok, err, id2)
		}
	})

	t.Run("the table refuses what the service never sends", func(t *testing.T) {
		s := openTestStore(t)
		bad := newConsoleChange(id1, caT0)
		bad.RedirectDays = 5
		if _, err := s.InsertConsoleAddressChange(ctx, bad); err == nil {
			t.Error("redirect days 5 was stored")
		}
		bad = newConsoleChange(id1, caT0)
		bad.Status = "done"
		if _, err := s.InsertConsoleAddressChange(ctx, bad); err == nil {
			t.Error("status done was stored")
		}
	})

	t.Run("old domains are the applied changes whose window hasn't ended", func(t *testing.T) {
		s := openTestStore(t)
		now := caT0.Add(24 * time.Hour)
		add := func(id, old string, at time.Time, status string, until time.Time) {
			c := newConsoleChange(id, at)
			c.OldDomain = old
			if _, err := s.InsertConsoleAddressChange(ctx, c); err != nil {
				t.Fatal(err)
			}
			c.Status, c.RedirectUntil, c.SettledAt = status, until, at.Add(time.Minute)
			if err := s.UpdateConsoleAddressChange(ctx, c, ConsoleChecking); err != nil {
				t.Fatal(err)
			}
		}
		add(id1, "old-a.example.com", caT0, ConsoleApplied, now.Add(10*24*time.Hour))
		add(id2, "old-b.example.com", caT0.Add(time.Hour), ConsoleApplied, now.Add(-time.Second))      // ended
		add(id3, "old-a.example.com", caT0.Add(2*time.Hour), ConsoleApplied, now.Add(20*24*time.Hour)) // same domain, later end
		add(strings.Repeat("d", 32), "old-c.example.com", caT0.Add(3*time.Hour), ConsoleFailed, now.Add(time.Hour))
		got, err := s.ConsoleOldDomains(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Domain != "old-a.example.com" || !got[0].Until.Equal(now.Add(20*24*time.Hour)) {
			t.Errorf("ConsoleOldDomains = %+v; want old-a.example.com until the later end only", got)
		}
	})
}

func TestConsoleAddressApplied(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if ok, err := s.ConsoleAddressApplied(ctx); err != nil || ok {
		t.Fatalf("ConsoleAddressApplied() on a new store = %v, %v; want false", ok, err)
	}
	c := newConsoleChange(strings.Repeat("a", 32), caT0)
	if _, err := s.InsertConsoleAddressChange(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Status = ConsoleCancelled
	if err := s.UpdateConsoleAddressChange(ctx, c, ConsoleChecking); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ConsoleAddressApplied(ctx); ok {
		t.Error("a cancelled change counts as applied")
	}
	d := newConsoleChange(strings.Repeat("b", 32), caT0.Add(time.Hour))
	if _, err := s.InsertConsoleAddressChange(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Status = ConsoleApplied
	if err := s.UpdateConsoleAddressChange(ctx, d, ConsoleChecking); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ConsoleAddressApplied(ctx); err != nil || !ok {
		t.Errorf("ConsoleAddressApplied() after an applied change = %v, %v; want true", ok, err)
	}
}

func TestCountWebhookTokens(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t)
	if n, err := s.CountWebhookTokens(ctx); err != nil || n != 0 {
		t.Fatalf("CountWebhookTokens() = %d, %v; want 0", n, err)
	}
	if _, err := s.SetWebhookToken(ctx, "whoami", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountWebhookTokens(ctx); err != nil || n != 1 {
		t.Errorf("CountWebhookTokens() = %d, %v; want 1", n, err)
	}
}
