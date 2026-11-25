package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func dropID(c string) string { return strings.Repeat(c, 32) }

func TestCertificateDropLifecycle(t *testing.T) {
	ctx := context.Background()
	s, v := cleanupFixture(t)
	if _, ok, err := s.OpenCertificateDrop(ctx); err != nil || ok {
		t.Fatalf("OpenCertificateDrop with none = %v, %v", ok, err)
	}
	d, err := s.CreateCertificateDrop(ctx, CertificateDrop{ID: dropID("a"), ServerIdx: v.Index, ServerName: "one", Actor: "ana@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != DropRequested || d.RequestedAt.IsZero() || d.Dropped == nil {
		t.Errorf("a new drop = %+v", d)
	}
	// One open drop in all: another ID, the same ID, and a server that isn't there.
	if _, err := s.CreateCertificateDrop(ctx, CertificateDrop{ID: dropID("b"), ServerIdx: v.Index, ServerName: "one"}); !errors.Is(err, ErrCertificateDropOpen) {
		t.Errorf("a second open drop = %v, want ErrCertificateDropOpen", err)
	}
	if _, err := s.CreateCertificateDrop(ctx, CertificateDrop{ID: dropID("a"), ServerIdx: v.Index, ServerName: "one"}); !errors.Is(err, ErrAlreadyExists) && !errors.Is(err, ErrCertificateDropOpen) {
		t.Errorf("the same ID twice = %v", err)
	}
	if _, err := s.CreateCertificateDrop(ctx, CertificateDrop{ID: dropID("c"), ServerIdx: 40, ServerName: "x", Status: DropDone}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a drop of no server = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateCertificateDrop(ctx, CertificateDrop{ID: dropID("d"), ServerIdx: v.Index, ServerName: "one", Status: "weird"}); err == nil {
		t.Error("an unknown status was stored")
	}

	open, ok, err := s.OpenCertificateDrop(ctx)
	if err != nil || !ok || open.ID != d.ID || open.Actor != "ana@example.com" || !open.StartedAt.IsZero() || len(open.Dropped) != 0 {
		t.Fatalf("OpenCertificateDrop = %+v, %v, %v", open, ok, err)
	}

	// Handed out: running, and a second hand-out doesn't move the start.
	handed := spT0.Add(30 * time.Second)
	if err := s.StartCertificateDrop(ctx, d.ID, handed); err != nil {
		t.Fatal(err)
	}
	if err := s.StartCertificateDrop(ctx, d.ID, handed.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetCertificateDrop(ctx, d.ID)
	if got.Status != DropRunning || !got.StartedAt.Equal(handed) {
		t.Errorf("after the hand-out = %+v", got)
	}

	// The agent rewrote the store: restarting, with the names; a repeat changes nothing.
	rewritten := handed.Add(10 * time.Second)
	if err := s.RestartCertificateDrop(ctx, d.ID, rewritten, []string{"a.example.com", "b.example.com+www.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RestartCertificateDrop(ctx, d.ID, rewritten.Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetCertificateDrop(ctx, d.ID)
	if got.Status != DropRestarting || !got.RewrittenAt.Equal(rewritten) ||
		!reflect.DeepEqual(got.Dropped, []string{"a.example.com", "b.example.com+www.example.com"}) {
		t.Errorf("restarting = %+v", got)
	}
	if _, ok, _ := s.OpenCertificateDrop(ctx); !ok {
		t.Error("a restarting drop isn't open")
	}

	// Settled: done, once; later settles and restarts leave it alone.
	end := rewritten.Add(20 * time.Second)
	if err := s.SettleCertificateDrop(ctx, d.ID, DropDone, end, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleCertificateDrop(ctx, d.ID, DropFailed, end.Add(time.Hour), "late"); err != nil {
		t.Fatal(err)
	}
	if err := s.RestartCertificateDrop(ctx, d.ID, end, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetCertificateDrop(ctx, d.ID)
	if got.Status != DropDone || !got.FinishedAt.Equal(end) || got.Error != "" || len(got.Dropped) != 2 {
		t.Errorf("settled = %+v", got)
	}
	if err := s.SettleCertificateDrop(ctx, d.ID, DropRunning, end, ""); err == nil {
		t.Error("an open status was accepted as a final one")
	}
	if _, ok, _ := s.OpenCertificateDrop(ctx); ok {
		t.Error("a settled drop is still open")
	}

	// Unknown IDs.
	for name, err := range map[string]error{
		"get":     func() error { _, err := s.GetCertificateDrop(ctx, dropID("9")); return err }(),
		"start":   s.StartCertificateDrop(ctx, dropID("9"), spT0),
		"restart": s.RestartCertificateDrop(ctx, dropID("9"), spT0, nil),
		"settle":  s.SettleCertificateDrop(ctx, dropID("9"), DropLost, spT0, "x"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s of an unknown drop = %v, want ErrNotFound", name, err)
		}
	}

	// A new one may be made once the first is settled, and the list is newest first.
	d2, err := s.CreateCertificateDrop(ctx, CertificateDrop{ID: dropID("e"), ServerIdx: v.Index, ServerName: "one", RequestedAt: spT0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SettleCertificateDrop(ctx, d2.ID, DropFailed, spT0.Add(2*time.Hour), "The server's agent didn't pick it up."); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListCertificateDrops(ctx, 10)
	if err != nil || len(list) != 2 || list[0].ID != d2.ID || list[0].Error == "" || list[1].ID != d.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list, _ = s.ListCertificateDrops(ctx, 1); len(list) != 1 {
		t.Errorf("a limit of 1 gave %d", len(list))
	}
	if _, err := s.ListCertificateDrops(ctx, 0); err == nil {
		t.Error("a limit of 0 was accepted")
	}
}

// A drop goes with its server.
func TestCertificateDropIsDeletedWithItsServer(t *testing.T) {
	ctx := context.Background()
	s, v := cleanupFixture(t)
	if _, err := s.CreateCertificateDrop(ctx, CertificateDrop{ID: dropID("a"), ServerIdx: v.Index, ServerName: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE idx = ?`, v.Index); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListCertificateDrops(ctx, 5); err != nil || len(list) != 0 {
		t.Errorf("list after the server's deletion = %v, %v", list, err)
	}
}
