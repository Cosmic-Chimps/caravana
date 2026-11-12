package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func testPasskey(id, credential, rp string, created time.Time) PasskeyRecord {
	return PasskeyRecord{
		ID: id, UserHandle: []byte("user-handle-32-bytes-xxxxxxxxxxx"), CredentialID: []byte(credential), RPID: rp,
		PublicKey: []byte("cose-key"), AttestationType: "none", AttestationFormat: "none",
		AAGUID: []byte("0123456789abcdef"), SignCount: 0, Transports: []string{"internal", "hybrid"},
		UserPresent: true, UserVerified: true, BackupEligible: true, BackupState: false,
		Name: "MacBook", CreatedAt: created,
	}
}

func TestPasskeys(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	t.Run("insert and get round trip", func(t *testing.T) {
		s := openTestStore(t)
		want := testPasskey("pk1", "cred-1", "console.example.com", t0)
		if err := s.InsertPasskey(ctx, want); err != nil {
			t.Fatalf("InsertPasskey: %v", err)
		}
		got, err := s.GetPasskey(ctx, "pk1")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("GetPasskey = %+v, %v; want %+v", got, err, want)
		}
		byCred, err := s.GetPasskeyByCredential(ctx, []byte("cred-1"))
		if err != nil || byCred.ID != "pk1" {
			t.Errorf("GetPasskeyByCredential = %+v, %v", byCred, err)
		}
	})

	t.Run("no transports and no AAGUID", func(t *testing.T) {
		s := openTestStore(t)
		p := testPasskey("pk1", "cred-1", "localhost", t0)
		p.Transports, p.AAGUID = nil, nil
		if err := s.InsertPasskey(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetPasskey(ctx, "pk1")
		if len(got.Transports) != 0 || len(got.AAGUID) != 0 {
			t.Errorf("transports = %v, aaguid = %x; want none", got.Transports, got.AAGUID)
		}
	})

	t.Run("a credential ID is unique", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.InsertPasskey(ctx, testPasskey("pk1", "cred-1", "a.example.com", t0)); err != nil {
			t.Fatal(err)
		}
		err := s.InsertPasskey(ctx, testPasskey("pk2", "cred-1", "b.example.com", t0))
		if !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("second insert of one credential = %v, want ErrAlreadyExists", err)
		}
		if n, _ := s.CountPasskeys(ctx, ""); n != 1 {
			t.Errorf("count = %d, want 1", n)
		}
	})

	t.Run("unknown IDs", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.GetPasskey(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetPasskey = %v, want ErrNotFound", err)
		}
		if _, err := s.GetPasskeyByCredential(ctx, []byte("nope")); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetPasskeyByCredential = %v, want ErrNotFound", err)
		}
		if err := s.RenamePasskey(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("RenamePasskey = %v, want ErrNotFound", err)
		}
		if err := s.DeletePasskey(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeletePasskey = %v, want ErrNotFound", err)
		}
		if err := s.UpdatePasskeyUse(ctx, "nope", 1, true, true, false, t0); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdatePasskeyUse = %v, want ErrNotFound", err)
		}
	})

	t.Run("list is oldest first", func(t *testing.T) {
		s := openTestStore(t)
		// Inserted out of order: the list goes by created_at.
		for _, p := range []PasskeyRecord{
			testPasskey("late", "c3", "a.example.com", t0.Add(2*time.Hour)),
			testPasskey("early", "c1", "a.example.com", t0),
			testPasskey("middle", "c2", "a.example.com", t0.Add(time.Hour)),
		} {
			if err := s.InsertPasskey(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.ListPasskeys(ctx)
		if err != nil || len(list) != 3 || list[0].ID != "early" || list[1].ID != "middle" || list[2].ID != "late" {
			t.Errorf("ListPasskeys = %v, %v; want early, middle, late", list, err)
		}
	})

	t.Run("rename and delete", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.InsertPasskey(ctx, testPasskey("pk1", "c1", "a.example.com", t0)); err != nil {
			t.Fatal(err)
		}
		if err := s.RenamePasskey(ctx, "pk1", "Work laptop"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetPasskey(ctx, "pk1"); got.Name != "Work laptop" {
			t.Errorf("name = %q, want Work laptop", got.Name)
		}
		if err := s.DeletePasskey(ctx, "pk1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetPasskey(ctx, "pk1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetPasskey after delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("a use updates the counter, the flags and the time", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.InsertPasskey(ctx, testPasskey("pk1", "c1", "a.example.com", t0)); err != nil {
			t.Fatal(err)
		}
		at := t0.Add(time.Hour)
		if err := s.UpdatePasskeyUse(ctx, "pk1", 7, true, true, true, at); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetPasskey(ctx, "pk1")
		if got.SignCount != 7 || !got.BackupState || got.LastUsedAt == nil || !got.LastUsedAt.Equal(at) {
			t.Errorf("after use: count = %d, backupState = %v, lastUsed = %v", got.SignCount, got.BackupState, got.LastUsedAt)
		}
		if got.Name != "MacBook" || !got.CreatedAt.Equal(t0) {
			t.Errorf("a use changed the name or creation time: %+v", got)
		}
	})

	t.Run("count by relying party", func(t *testing.T) {
		s := openTestStore(t)
		for i, rp := range []string{"a.example.com", "a.example.com", "b.example.com"} {
			if err := s.InsertPasskey(ctx, testPasskey(string(rune('a'+i)), string(rune('x'+i)), rp, t0)); err != nil {
				t.Fatal(err)
			}
		}
		for rp, want := range map[string]int{"": 3, "a.example.com": 2, "b.example.com": 1, "c.example.com": 0} {
			if n, err := s.CountPasskeys(ctx, rp); err != nil || n != want {
				t.Errorf("CountPasskeys(%q) = %d, %v; want %d", rp, n, err, want)
			}
		}
	})
}
