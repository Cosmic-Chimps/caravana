package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

var opT0 = time.Date(2026, 10, 30, 8, 0, 0, 500, time.UTC)

func TestHostEvents(t *testing.T) {
	ctx := context.Background()

	t.Run("a new store has none", func(t *testing.T) {
		s := openTestStore(t)
		if got, err := s.HostEvents(ctx, "", 10); err != nil || len(got) != 0 {
			t.Errorf("HostEvents() = %v, %v; want none", got, err)
		}
	})

	t.Run("newest first, filtered by kind", func(t *testing.T) {
		s := openTestStore(t)
		for i, e := range []HostEvent{
			{Kind: HostEventBoot, At: opT0, Detail: `{}`, Key: "boot|1"},
			{Kind: HostEventVersion, At: opT0.Add(time.Hour), Detail: `{"from":"0.14.0","to":"0.15.0"}`, Key: "version|0.15.0"},
			{Kind: HostEventOSPackages, At: opT0.Add(time.Minute), Detail: `{"packages":14}`, Key: "os|x"},
		} {
			if ok, err := s.AppendHostEvent(ctx, e); err != nil || !ok {
				t.Fatalf("event %d: AppendHostEvent() = %v, %v; want true, nil", i, ok, err)
			}
		}
		got, err := s.HostEvents(ctx, "", 10)
		if err != nil || len(got) != 3 {
			t.Fatalf("HostEvents() = %v, %v; want 3", got, err)
		}
		if got[0].Kind != HostEventVersion || got[1].Kind != HostEventOSPackages || got[2].Kind != HostEventBoot {
			t.Errorf("order = %s, %s, %s; want version, os_packages, boot", got[0].Kind, got[1].Kind, got[2].Kind)
		}
		if got[0].Detail != `{"from":"0.14.0","to":"0.15.0"}` || got[0].Key != "version|0.15.0" || !got[0].At.Equal(opT0.Add(time.Hour)) {
			t.Errorf("version event = %+v, lost a field", got[0])
		}
		if only, _ := s.HostEvents(ctx, HostEventBoot, 10); len(only) != 1 || only[0].Kind != HostEventBoot {
			t.Errorf("HostEvents(boot) = %v, want the one boot", only)
		}
		if one, _ := s.HostEvents(ctx, "", 1); len(one) != 1 || one[0].Kind != HostEventVersion {
			t.Errorf("HostEvents(limit 1) = %v, want the newest", one)
		}
	})

	t.Run("the same key is recorded once", func(t *testing.T) {
		s := openTestStore(t)
		e := HostEvent{Kind: HostEventBoot, At: opT0, Detail: `{}`, Key: "boot|1"}
		if ok, err := s.AppendHostEvent(ctx, e); err != nil || !ok {
			t.Fatalf("first = %v, %v", ok, err)
		}
		e.Detail = `{"changed":true}`
		if ok, err := s.AppendHostEvent(ctx, e); err != nil || ok {
			t.Errorf("second = %v, %v; want false, nil", ok, err)
		}
		got, _ := s.HostEvents(ctx, "", 10)
		if len(got) != 1 || got[0].Detail != `{}` {
			t.Errorf("events = %+v, want the first one untouched", got)
		}
	})

	t.Run("events without a key are all kept", func(t *testing.T) {
		s := openTestStore(t)
		for range 2 {
			if ok, err := s.AppendHostEvent(ctx, HostEvent{Kind: HostEventBoot, At: opT0, Detail: `{}`}); err != nil || !ok {
				t.Fatalf("AppendHostEvent() = %v, %v", ok, err)
			}
		}
		if got, _ := s.HostEvents(ctx, "", 10); len(got) != 2 {
			t.Errorf("events = %+v, want 2", got)
		}
	})

	t.Run("a kind it does not know is refused", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.AppendHostEvent(ctx, HostEvent{Kind: "reboot", At: opT0, Detail: `{}`, Key: "k"}); err == nil {
			t.Error("AppendHostEvent accepted an unknown kind")
		}
	})

	t.Run("the newest 200 are kept", func(t *testing.T) {
		s := openTestStore(t)
		for i := range 205 {
			e := HostEvent{Kind: HostEventOSPackages, At: opT0.Add(time.Duration(i) * time.Minute), Detail: `{}`, Key: fmt.Sprintf("os|%03d", i)}
			if _, err := s.AppendHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.HostEvents(ctx, "", 500)
		if err != nil || len(got) != 200 {
			t.Fatalf("kept %d, %v; want 200", len(got), err)
		}
		if got[0].Key != "os|204" || got[199].Key != "os|005" {
			t.Errorf("kept %s down to %s, want os|204 down to os|005", got[0].Key, got[199].Key)
		}
		// A pruned key can be recorded again: the dedup is for what is kept.
		if ok, err := s.AppendHostEvent(ctx, HostEvent{Kind: HostEventOSPackages, At: opT0.Add(time.Hour * 24 * 365), Detail: `{}`, Key: "os|000"}); err != nil || !ok {
			t.Errorf("a pruned key = %v, %v; want recorded", ok, err)
		}
	})
}

func TestReleaseCheck(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	got, err := s.ReleaseCheck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Error("a new store has the release check off, want on")
	}
	if got.LatestTag != "" || got.URL != "" || got.Body != "" || got.ETag != "" || got.Error != "" || got.NotifiedTag != "" ||
		!got.PublishedAt.IsZero() || !got.CheckedAt.IsZero() {
		t.Errorf("a new store's row = %+v, want everything else empty", got)
	}

	want := ReleaseCheck{
		Enabled: false, LatestTag: "v0.16.0", PublishedAt: opT0, URL: "https://example.test/r/v0.16.0",
		Body: "- one\n- two", NomadPin: "2.0.1", TraefikPin: "v3.5.0", ETag: `W/"abc"`,
		CheckedAt: opT0.Add(time.Hour), Error: "GitHub didn't answer", NotifiedTag: "v0.15.9",
	}
	if err := s.SaveReleaseCheck(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = s.ReleaseCheck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled != want.Enabled || got.LatestTag != want.LatestTag || !got.PublishedAt.Equal(want.PublishedAt) ||
		got.URL != want.URL || got.Body != want.Body || got.NomadPin != want.NomadPin || got.TraefikPin != want.TraefikPin ||
		got.ETag != want.ETag || !got.CheckedAt.Equal(want.CheckedAt) || got.Error != want.Error || got.NotifiedTag != want.NotifiedTag {
		t.Errorf("ReleaseCheck() = %+v, want %+v", got, want)
	}

	// Saving again keeps one row, and clearing a field empties it.
	want.Error, want.Enabled, want.CheckedAt = "", true, time.Time{}
	if err := s.SaveReleaseCheck(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ReleaseCheck(ctx)
	if got.Error != "" || !got.Enabled || !got.CheckedAt.IsZero() {
		t.Errorf("after clearing = %+v", got)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM release_check").Scan(&n); err != nil || n != 1 {
		t.Errorf("release_check has %d rows, %v; want 1", n, err)
	}
}

func TestHostMarks(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if _, ok, err := s.HostMark(ctx, MarkSecuritySince); err != nil || ok {
		t.Fatalf("HostMark() on a new store = %v, %v; want none", ok, err)
	}
	if err := s.SetHostMark(ctx, MarkSecuritySince, opT0); err != nil {
		t.Fatal(err)
	}
	if at, ok, err := s.HostMark(ctx, MarkSecuritySince); err != nil || !ok || !at.Equal(opT0) {
		t.Errorf("HostMark() = %v, %v, %v; want %v", at, ok, err, opT0)
	}
	// Setting it again moves it.
	later := opT0.Add(time.Hour)
	if err := s.SetHostMark(ctx, MarkSecuritySince, later); err != nil {
		t.Fatal(err)
	}
	if at, _, _ := s.HostMark(ctx, MarkSecuritySince); !at.Equal(later) {
		t.Errorf("after a second set = %v, want %v", at, later)
	}
	if err := s.DeleteHostMark(ctx, MarkSecuritySince); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.HostMark(ctx, MarkSecuritySince); ok {
		t.Error("the mark is still there after DeleteHostMark")
	}
	if err := s.DeleteHostMark(ctx, MarkSecuritySince); err != nil {
		t.Errorf("DeleteHostMark of a missing mark = %v, want nil", err)
	}
}
