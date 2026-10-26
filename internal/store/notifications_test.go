package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

var notifyT0 = time.Date(2026, 10, 16, 9, 0, 0, 123456789, time.UTC)

func newChannel(id string) NotificationChannel {
	return NotificationChannel{
		ID: id, Kind: "ntfy", Name: "phone", Address: "https://ntfy.sh/my-topic",
		Events: []string{"problems", "deploy_failed"}, CreatedAt: notifyT0, UpdatedAt: notifyT0,
	}
}

func TestNotificationChannels(t *testing.T) {
	ctx := context.Background()

	t.Run("a new store has none", func(t *testing.T) {
		s := openTestStore(t)
		got, err := s.ListNotificationChannels(ctx)
		if err != nil || len(got) != 0 {
			t.Errorf("ListNotificationChannels() = %v, %v; want none", got, err)
		}
		if _, err := s.GetNotificationChannel(ctx, "nch_x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetNotificationChannel() = %v, want ErrNotFound", err)
		}
	})

	t.Run("save and read back", func(t *testing.T) {
		s := openTestStore(t)
		want := newChannel("nch_aaaaaaaaaaaa")
		want.Paused = true
		if err := s.SaveNotificationChannel(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetNotificationChannel(ctx, want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("GetNotificationChannel() = %+v, %v; want %+v", got, err, want)
		}
	})

	t.Run("saving again keeps created_at and changes the rest", func(t *testing.T) {
		s := openTestStore(t)
		first := newChannel("nch_aaaaaaaaaaaa")
		if err := s.SaveNotificationChannel(ctx, first); err != nil {
			t.Fatal(err)
		}
		t1 := notifyT0.Add(time.Hour)
		next := first
		next.Name, next.Events, next.Paused, next.CreatedAt, next.UpdatedAt = "tablet", []string{"deploy_succeeded"}, true, t1, t1
		if err := s.SaveNotificationChannel(ctx, next); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetNotificationChannel(ctx, first.ID)
		want := next
		want.CreatedAt = notifyT0
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("GetNotificationChannel() = %+v, %v; want %+v", got, err, want)
		}
	})

	t.Run("an unknown kind is refused", func(t *testing.T) {
		s := openTestStore(t)
		c := newChannel("nch_aaaaaaaaaaaa")
		c.Kind = "carrier-pigeon"
		if err := s.SaveNotificationChannel(ctx, c); err == nil {
			t.Error("SaveNotificationChannel() accepted an unknown kind")
		}
	})

	t.Run("list is oldest first", func(t *testing.T) {
		s := openTestStore(t)
		for i, id := range []string{"nch_cc", "nch_aa", "nch_bb"} {
			c := newChannel(id)
			c.CreatedAt = notifyT0.Add(time.Duration(i) * time.Minute)
			if err := s.SaveNotificationChannel(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListNotificationChannels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		if want := []string{"nch_cc", "nch_aa", "nch_bb"}; !reflect.DeepEqual(ids, want) {
			t.Errorf("ids = %v, want %v", ids, want)
		}
	})

	t.Run("delete removes it with its deliveries, and a missing ID is ErrNotFound", func(t *testing.T) {
		s := openTestStore(t)
		for _, id := range []string{"nch_aa", "nch_bb"} {
			if err := s.SaveNotificationChannel(ctx, newChannel(id)); err != nil {
				t.Fatal(err)
			}
			if err := s.EnqueueDelivery(ctx, newDelivery("ndl_"+id, id, notifyT0)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteNotificationChannel(ctx, "nch_aa"); err != nil {
			t.Fatalf("DeleteNotificationChannel() = %v", err)
		}
		if got, err := s.ListDeliveries(ctx, "nch_aa", 10); err != nil || len(got) != 0 {
			t.Errorf("deliveries of the deleted channel = %v, %v; want none", got, err)
		}
		if got, err := s.ListDeliveries(ctx, "nch_bb", 10); err != nil || len(got) != 1 {
			t.Errorf("deliveries of the other channel = %v, %v; want 1", got, err)
		}
		if err := s.DeleteNotificationChannel(ctx, "nch_aa"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteNotificationChannel() of a missing ID = %v, want ErrNotFound", err)
		}
	})
}

func TestSentProblems(t *testing.T) {
	ctx := context.Background()
	p := func(key, app string, opened time.Time) SentProblem {
		return SentProblem{Key: key, Kind: "restarting", AppID: app, Service: "web", OpenedAt: opened, LastSeenAt: opened, Summary: "title " + key}
	}

	t.Run("upsert, list, and replace", func(t *testing.T) {
		s := openTestStore(t)
		a, b := p("a", "blog", notifyT0), p("b", "", notifyT0.Add(time.Minute))
		for _, x := range []SentProblem{b, a} {
			if err := s.UpsertSentProblem(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListSentProblems(ctx)
		if err != nil || !reflect.DeepEqual(got, []SentProblem{a, b}) {
			t.Fatalf("ListSentProblems() = %+v, %v; want %+v (oldest first)", got, err, []SentProblem{a, b})
		}
		a.Summary = "changed"
		if err := s.UpsertSentProblem(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, _ = s.ListSentProblems(ctx)
		if len(got) != 2 || got[0].Summary != "changed" {
			t.Errorf("after replacing: %+v", got)
		}
	})

	t.Run("touch moves last_seen_at only, and skips unknown keys", func(t *testing.T) {
		s := openTestStore(t)
		a := p("a", "blog", notifyT0)
		if err := s.UpsertSentProblem(ctx, a); err != nil {
			t.Fatal(err)
		}
		later := notifyT0.Add(10 * time.Minute)
		if err := s.TouchSentProblems(ctx, []string{"a", "unknown"}, later); err != nil {
			t.Fatal(err)
		}
		if err := s.TouchSentProblems(ctx, nil, later); err != nil {
			t.Fatal(err)
		}
		got, _ := s.ListSentProblems(ctx)
		want := a
		want.LastSeenAt = later
		if !reflect.DeepEqual(got, []SentProblem{want}) {
			t.Errorf("ListSentProblems() = %+v, want %+v", got, want)
		}
	})

	t.Run("delete one and delete by app", func(t *testing.T) {
		s := openTestStore(t)
		for _, x := range []SentProblem{p("a", "blog", notifyT0), p("b", "blog", notifyT0), p("c", "shop", notifyT0), p("d", "", notifyT0)} {
			if err := s.UpsertSentProblem(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DeleteSentProblem(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSentProblem(ctx, "missing"); err != nil {
			t.Errorf("DeleteSentProblem() of a missing key = %v, want nil", err)
		}
		if err := s.DeleteSentProblemsOfApp(ctx, "blog"); err != nil {
			t.Fatal(err)
		}
		got, _ := s.ListSentProblems(ctx)
		var keys []string
		for _, x := range got {
			keys = append(keys, x.Key)
		}
		if want := []string{"c", "d"}; !reflect.DeepEqual(keys, want) {
			t.Errorf("keys left = %v, want %v", keys, want)
		}
	})
}

func newDelivery(id, channel string, created time.Time) Delivery {
	return Delivery{
		ID: id, ChannelID: channel, EventType: "problem.opened", Title: "a title", Payload: `{"type":"problem.opened"}`,
		Status: DeliveryPending, NextAt: created, CreatedAt: created,
	}
}

func TestDeliveries(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) *Store {
		t.Helper()
		s := openTestStore(t)
		for _, id := range []string{"nch_aa", "nch_bb"} {
			if err := s.SaveNotificationChannel(ctx, newChannel(id)); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}

	t.Run("a delivery round-trips", func(t *testing.T) {
		s := setup(t)
		done := notifyT0.Add(time.Second)
		want := newDelivery("ndl_1", "nch_aa", notifyT0)
		want.Status, want.Attempts, want.Error, want.FinishedAt = DeliveryFailed, 3, "boom", &done
		if err := s.EnqueueDelivery(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListDeliveries(ctx, "nch_aa", 10)
		if err != nil || !reflect.DeepEqual(got, []Delivery{want}) {
			t.Errorf("ListDeliveries() = %+v, %v; want %+v", got, err, want)
		}
	})

	t.Run("a delivery for an unknown channel is refused", func(t *testing.T) {
		s := setup(t)
		if err := s.EnqueueDelivery(ctx, newDelivery("ndl_1", "nch_zz", notifyT0)); err == nil {
			t.Error("EnqueueDelivery() accepted an unknown channel")
		}
	})

	t.Run("due ordering by next_at, only pending, only due, limited", func(t *testing.T) {
		s := setup(t)
		now := notifyT0.Add(time.Hour)
		put := func(id string, next time.Time, status string) {
			d := newDelivery(id, "nch_aa", notifyT0)
			d.NextAt, d.Status = next, status
			if err := s.EnqueueDelivery(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		put("ndl_c", now.Add(-time.Minute), DeliveryPending)
		put("ndl_a", now.Add(-time.Hour), DeliveryPending)
		put("ndl_b", now, DeliveryPending) // due exactly now
		put("ndl_future", now.Add(time.Second), DeliveryPending)
		put("ndl_sent", now.Add(-2*time.Hour), DeliverySent)
		put("ndl_failed", now.Add(-2*time.Hour), DeliveryFailed)

		ids := func(ds []Delivery) []string {
			var out []string
			for _, d := range ds {
				out = append(out, d.ID)
			}
			return out
		}
		got, err := s.DueDeliveries(ctx, now, 10)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"ndl_a", "ndl_c", "ndl_b"}; !reflect.DeepEqual(ids(got), want) {
			t.Errorf("due = %v, want %v", ids(got), want)
		}
		got, _ = s.DueDeliveries(ctx, now, 2)
		if want := []string{"ndl_a", "ndl_c"}; !reflect.DeepEqual(ids(got), want) {
			t.Errorf("due with limit 2 = %v, want %v", ids(got), want)
		}
	})

	t.Run("retry keeps it pending with a later time, finish ends it", func(t *testing.T) {
		s := setup(t)
		if err := s.EnqueueDelivery(ctx, newDelivery("ndl_1", "nch_aa", notifyT0)); err != nil {
			t.Fatal(err)
		}
		next := notifyT0.Add(30 * time.Second)
		if err := s.RetryDelivery(ctx, "ndl_1", 1, next, "first failure"); err != nil {
			t.Fatal(err)
		}
		if due, _ := s.DueDeliveries(ctx, notifyT0, 10); len(due) != 0 {
			t.Errorf("due before the retry time = %v, want none", due)
		}
		due, _ := s.DueDeliveries(ctx, next, 10)
		if len(due) != 1 || due[0].Attempts != 1 || due[0].Error != "first failure" || !due[0].NextAt.Equal(next) {
			t.Fatalf("due at the retry time = %+v", due)
		}

		done := next.Add(time.Second)
		if err := s.FinishDelivery(ctx, "ndl_1", DeliverySent, 2, "", done); err != nil {
			t.Fatal(err)
		}
		got, _ := s.ListDeliveries(ctx, "nch_aa", 10)
		if len(got) != 1 || got[0].Status != DeliverySent || got[0].Attempts != 2 || got[0].Error != "" ||
			got[0].FinishedAt == nil || !got[0].FinishedAt.Equal(done) {
			t.Errorf("after finishing: %+v", got)
		}
		if due, _ := s.DueDeliveries(ctx, done.Add(time.Hour), 10); len(due) != 0 {
			t.Errorf("a finished delivery is still due: %v", due)
		}

		// A finished delivery can't be retried.
		if err := s.RetryDelivery(ctx, "ndl_1", 3, next, "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("RetryDelivery() of a finished delivery = %v, want ErrNotFound", err)
		}
		if err := s.FinishDelivery(ctx, "ndl_missing", DeliverySent, 1, "", done); !errors.Is(err, ErrNotFound) {
			t.Errorf("FinishDelivery() of a missing ID = %v, want ErrNotFound", err)
		}
	})

	t.Run("list is newest first, per channel, limited", func(t *testing.T) {
		s := setup(t)
		for i, id := range []string{"ndl_1", "ndl_2", "ndl_3"} {
			if err := s.EnqueueDelivery(ctx, newDelivery(id, "nch_aa", notifyT0.Add(time.Duration(i)*time.Minute))); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.EnqueueDelivery(ctx, newDelivery("ndl_other", "nch_bb", notifyT0)); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListDeliveries(ctx, "nch_aa", 2)
		if err != nil || len(got) != 2 || got[0].ID != "ndl_3" || got[1].ID != "ndl_2" {
			t.Errorf("ListDeliveries() = %+v, %v; want ndl_3, ndl_2", got, err)
		}
	})

	t.Run("prune keeps the newest 20 finished and never touches pending", func(t *testing.T) {
		s := setup(t)
		for i := 0; i < 25; i++ {
			d := newDelivery("ndl_f"+string(rune('a'+i)), "nch_aa", notifyT0.Add(time.Duration(i)*time.Minute))
			d.Status = DeliverySent
			if i%2 == 0 {
				d.Status = DeliveryFailed
			}
			if err := s.EnqueueDelivery(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		// Pending rows, older than all of them, and another channel's rows.
		for _, id := range []string{"ndl_p1", "ndl_p2"} {
			if err := s.EnqueueDelivery(ctx, newDelivery(id, "nch_aa", notifyT0.Add(-time.Hour))); err != nil {
				t.Fatal(err)
			}
		}
		other := newDelivery("ndl_other", "nch_bb", notifyT0)
		other.Status = DeliverySent
		if err := s.EnqueueDelivery(ctx, other); err != nil {
			t.Fatal(err)
		}

		if err := s.PruneDeliveries(ctx, "nch_aa", 20); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListDeliveries(ctx, "nch_aa", 100)
		if err != nil {
			t.Fatal(err)
		}
		finished, pending := 0, 0
		for _, d := range got {
			if d.Status == DeliveryPending {
				pending++
			} else {
				finished++
			}
		}
		if finished != 20 || pending != 2 {
			t.Errorf("after pruning: %d finished and %d pending, want 20 and 2", finished, pending)
		}
		// The oldest five finished ones went; the newest stayed.
		for _, d := range got {
			if d.ID == "ndl_fa" || d.ID == "ndl_fe" {
				t.Errorf("%s survived pruning", d.ID)
			}
		}
		if got[0].ID != "ndl_fy" {
			t.Errorf("newest = %s, want ndl_fy", got[0].ID)
		}
		if other, _ := s.ListDeliveries(ctx, "nch_bb", 10); len(other) != 1 {
			t.Errorf("another channel's deliveries were pruned: %v", other)
		}
	})
}

// One check's changes are applied together or not at all: a delivery that
// cannot be queued (its channel is gone) leaves the table of sent problems as
// it was.
func TestApplyProblemCheck(t *testing.T) {
	ctx := context.Background()
	problem := func(key string, seen time.Time) SentProblem {
		return SentProblem{Key: key, Kind: "backup_failed", AppID: "kuma", Service: "db", OpenedAt: notifyT0, LastSeenAt: seen, Summary: "s"}
	}
	setup := func(t *testing.T) *Store {
		t.Helper()
		s := openTestStore(t)
		if err := s.SaveNotificationChannel(ctx, newChannel("nch_aa")); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"old", "kept", "gone"} {
			if err := s.UpsertSentProblem(ctx, problem(key, notifyT0)); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	later := notifyT0.Add(time.Minute)

	t.Run("opened, seen, removed and queued together", func(t *testing.T) {
		s := setup(t)
		err := s.ApplyProblemCheck(ctx, ProblemCheck{
			Opened: []SentProblem{problem("new", later)}, Seen: []string{"kept"}, SeenAt: later,
			Removed: []string{"gone"}, Deliveries: []Delivery{newDelivery("ndl_a", "nch_aa", later)},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ListSentProblems(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]time.Time{}
		for _, p := range got {
			seen[p.Key] = p.LastSeenAt
		}
		if len(seen) != 3 || !seen["kept"].Equal(later) || !seen["new"].Equal(later) || !seen["old"].Equal(notifyT0) {
			t.Errorf("problems = %v, want old (untouched), kept (moved), new", seen)
		}
		if _, ok := seen["gone"]; ok {
			t.Error("a removed problem is still there")
		}
		if d, _ := s.ListDeliveries(ctx, "nch_aa", 10); len(d) != 1 || d[0].Status != DeliveryPending {
			t.Errorf("deliveries = %v, want one pending", d)
		}
	})

	t.Run("a failing delivery rolls back the table", func(t *testing.T) {
		s := setup(t)
		err := s.ApplyProblemCheck(ctx, ProblemCheck{
			Opened: []SentProblem{problem("new", later)}, Removed: []string{"gone"},
			Deliveries: []Delivery{newDelivery("ndl_a", "nch_missing", later)}, // no such channel
		})
		if err == nil {
			t.Fatal("ApplyProblemCheck succeeded for a delivery of a missing channel")
		}
		got, _ := s.ListSentProblems(ctx)
		if len(got) != 3 {
			t.Errorf("problems = %v, want the three from before", got)
		}
		for _, p := range got {
			if p.Key == "new" {
				t.Error("a problem opened by a failed check was kept")
			}
		}
	})

	t.Run("an empty check changes nothing", func(t *testing.T) {
		s := setup(t)
		if err := s.ApplyProblemCheck(ctx, ProblemCheck{}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.ListSentProblems(ctx); len(got) != 3 {
			t.Errorf("problems = %v", got)
		}
	})
}

// A sent problem keeps the level last sent, empty by default (a row from before the column, or a kind
// with none), and a check moves it with ProblemCheck.Levels.
func TestSentProblemLevel(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	plain := SentProblem{Key: "restarting|blog|web|", Kind: "restarting", AppID: "blog", Service: "web", OpenedAt: notifyT0, LastSeenAt: notifyT0, Summary: "t"}
	disk := SentProblem{Key: "disk_nearly_full|||/", Kind: "disk_nearly_full", OpenedAt: notifyT0.Add(time.Second), LastSeenAt: notifyT0, Summary: "d", Level: "warning"}
	if err := s.ApplyProblemCheck(ctx, ProblemCheck{Opened: []SentProblem{plain, disk}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListSentProblems(ctx)
	if err != nil || len(got) != 2 || got[0].Level != "" || got[1].Level != "warning" {
		t.Fatalf("ListSentProblems() = %+v, %v; want levels '' and warning", got, err)
	}
	if err := s.ApplyProblemCheck(ctx, ProblemCheck{Levels: map[string]string{disk.Key: "failed"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListSentProblems(ctx)
	if len(got) != 2 || got[1].Level != "failed" || got[0].Level != "" {
		t.Errorf("after the rise = %+v, want the disk failed and the other unchanged", got)
	}
	// A row written without a level keeps the default.
	old := SentProblem{Key: "k", Kind: "x", OpenedAt: notifyT0.Add(time.Minute), LastSeenAt: notifyT0, Summary: "s"}
	if err := s.UpsertSentProblem(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListSentProblems(ctx)
	if len(got) != 3 || got[2].Level != "" {
		t.Errorf("a problem saved without a level = %+v", got)
	}
}
