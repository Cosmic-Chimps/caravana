package service

import (
	"strconv"
	"testing"
	"time"
)

func TestCeremonies(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	signIn := func(address string) ceremony { return ceremony{kind: ceremonySignIn, address: address} }

	t.Run("take returns what was added, once", func(t *testing.T) {
		var c ceremonies
		id, err := c.add(t0, ceremony{kind: ceremonyConfirm, address: "a", sessionHash: "s1", rp: RelyingParty{ID: "x.example.com"}})
		if err != nil || len(id) != 22 { // 16 bytes in base64url without padding
			t.Fatalf("add = %q, %v", id, err)
		}
		got, ok := c.take(t0.Add(time.Minute), id)
		if !ok || got.kind != ceremonyConfirm || got.sessionHash != "s1" || got.rp.ID != "x.example.com" {
			t.Fatalf("take = %+v, %v", got, ok)
		}
		if _, ok := c.take(t0.Add(time.Minute), id); ok {
			t.Error("a ceremony was taken twice")
		}
	})

	t.Run("an unknown ID", func(t *testing.T) {
		var c ceremonies
		if _, ok := c.take(t0, "nope"); ok {
			t.Error("took a ceremony that doesn't exist")
		}
	})

	t.Run("expires after five minutes, and is used up by the try", func(t *testing.T) {
		var c ceremonies
		fresh, _ := c.add(t0, signIn("a"))
		if _, ok := c.take(t0.Add(ceremonyLife-time.Second), fresh); !ok {
			t.Error("a ceremony just under five minutes old was refused")
		}
		late, _ := c.add(t0, signIn("a"))
		if _, ok := c.take(t0.Add(ceremonyLife), late); ok {
			t.Error("a five-minute-old ceremony was accepted")
		}
		if c.pending() != 0 {
			t.Errorf("pending = %d, want the expired one gone too", c.pending())
		}
	})

	t.Run("expired ceremonies are dropped when another is added", func(t *testing.T) {
		var c ceremonies
		for range 3 {
			c.add(t0, signIn("a"))
		}
		c.add(t0.Add(time.Hour), signIn("b"))
		if c.pending() != 1 {
			t.Errorf("pending = %d, want 1", c.pending())
		}
	})

	t.Run("eight per address, the oldest goes", func(t *testing.T) {
		var c ceremonies
		var ids []string
		for range 9 {
			id, _ := c.add(t0, signIn("a"))
			ids = append(ids, id)
		}
		other, _ := c.add(t0, signIn("b"))
		if _, ok := c.take(t0, ids[0]); ok {
			t.Error("the oldest of nine from one address survived")
		}
		for _, id := range ids[1:] {
			if _, ok := c.take(t0, id); !ok {
				t.Error("a ceremony within the cap was dropped")
			}
		}
		if _, ok := c.take(t0, other); !ok {
			t.Error("another address's ceremony was dropped by the cap on a")
		}
	})

	t.Run("512 in all, the oldest goes", func(t *testing.T) {
		var c ceremonies
		first, _ := c.add(t0, signIn("addr-0"))
		for i := 1; i <= ceremonyTotal; i++ { // one address each: only the total can drop
			c.add(t0, signIn("addr-"+strconv.Itoa(i)))
		}
		if c.pending() != ceremonyTotal {
			t.Errorf("pending = %d, want %d", c.pending(), ceremonyTotal)
		}
		if _, ok := c.take(t0, first); ok {
			t.Error("the oldest ceremony survived the cap on the total")
		}
	})

	t.Run("IDs are random", func(t *testing.T) {
		var c ceremonies
		a, _ := c.add(t0, signIn("a"))
		b, _ := c.add(t0, signIn("a"))
		if a == b {
			t.Error("two ceremonies got one ID")
		}
	})
}
