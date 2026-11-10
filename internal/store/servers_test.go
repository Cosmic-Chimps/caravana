package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func newWaiting(id, name, hash string, expires time.Time) Server {
	return Server{ID: id, Name: name, State: ServerWaiting, JoinHash: hash, JoinExpires: expires}
}

func TestInsertServerTakesTheLowestFreeIndex(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	insert := func(n int) Server {
		t.Helper()
		v, err := s.InsertServer(ctx, newWaiting(fmt.Sprintf("id%d", n), "", fmt.Sprintf("hash%d", n), exp))
		if err != nil {
			t.Fatalf("InsertServer %d: %v", n, err)
		}
		return v
	}
	if got := insert(1).Index; got != 1 {
		t.Errorf("first index = %d, want 1", got)
	}
	if got := insert(2).Index; got != 2 {
		t.Errorf("second index = %d, want 2", got)
	}
	if got := insert(3).Index; got != 3 {
		t.Errorf("third index = %d, want 3", got)
	}
	// A deleted index is the next one used again.
	if err := s.DeleteServer(ctx, 2); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	if got := insert(4).Index; got != 2 {
		t.Errorf("index after a delete = %d, want 2 again", got)
	}
	if got := insert(5).Index; got != 4 {
		t.Errorf("index after the gap is filled = %d, want 4", got)
	}
	if err := s.DeleteServer(ctx, 1); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	if got := insert(6).Index; got != 1 {
		t.Errorf("index with 1 free = %d, want 1", got)
	}
	if err := s.DeleteServer(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteServer(99) = %v, want ErrNotFound", err)
	}
}

func TestInsertServerClusterFull(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for i := 1; i <= 63; i++ {
		if _, err := s.InsertServer(ctx, Server{ID: fmt.Sprintf("id%d", i), State: ServerWaiting}); err != nil {
			t.Fatalf("InsertServer %d: %v", i, err)
		}
	}
	if _, err := s.InsertServer(ctx, Server{ID: "id64", State: ServerWaiting}); !errors.Is(err, ErrClusterFull) {
		t.Errorf("the 64th server = %v, want ErrClusterFull", err)
	}
}

func TestServerNamesAreUnique(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.InsertServer(ctx, Server{ID: "a", Name: "fsn-1", State: ServerReady}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertServer(ctx, Server{ID: "b", Name: "fsn-1", State: ServerReady}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("a second fsn-1 = %v, want ErrAlreadyExists", err)
	}
	// Two waiting servers with no name yet are fine.
	for _, id := range []string{"c", "d"} {
		if _, err := s.InsertServer(ctx, Server{ID: id, State: ServerWaiting}); err != nil {
			t.Errorf("waiting server %s without a name: %v", id, err)
		}
	}
	if _, err := s.ServerByName(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("ServerByName(\"\") = %v, want ErrNotFound", err)
	}
}

func TestConsumeJoin(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for _, v := range []Server{
		newWaiting("live", "", "hash-live", now.Add(time.Hour)),
		newWaiting("expired", "", "hash-expired", now.Add(-time.Minute)),
	} {
		if _, err := s.InsertServer(ctx, v); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("an expired token misses", func(t *testing.T) {
		if _, err := s.ConsumeJoin(ctx, "hash-expired", "x", "key", "203.0.113.9:51820", "tok-x", now); !errors.Is(err, ErrNotFound) {
			t.Errorf("expired = %v, want ErrNotFound", err)
		}
	})
	t.Run("an unknown token misses", func(t *testing.T) {
		if _, err := s.ConsumeJoin(ctx, "nope", "x", "key", "e", "tok-y", now); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown = %v, want ErrNotFound", err)
		}
		if _, err := s.ConsumeJoin(ctx, "", "x", "key", "e", "tok-y", now); !errors.Is(err, ErrNotFound) {
			t.Errorf("empty = %v, want ErrNotFound", err)
		}
	})
	t.Run("one of two racing calls wins", func(t *testing.T) {
		var wins, misses int
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.ConsumeJoin(ctx, "hash-live", fmt.Sprintf("n%d", i), "key", "203.0.113.9:51820", fmt.Sprintf("tok%d", i), now)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins++
				case errors.Is(err, ErrNotFound):
					misses++
				default:
					t.Errorf("ConsumeJoin: %v", err)
				}
			}()
		}
		wg.Wait()
		if wins != 1 || misses != 7 {
			t.Errorf("wins = %d, misses = %d; want 1 and 7", wins, misses)
		}
	})
	t.Run("the row is joining and the hash is spent", func(t *testing.T) {
		v, err := s.ServerByID(ctx, "live")
		if err != nil {
			t.Fatal(err)
		}
		if v.State != ServerJoining || v.JoinHash != "" || v.PublicKey != "key" || v.TokenHash == "" || !v.JoinedAt.Equal(now) {
			t.Errorf("after the join the row is %+v", v)
		}
		if _, err := s.ServerByJoinHash(ctx, "hash-live"); !errors.Is(err, ErrNotFound) {
			t.Errorf("ServerByJoinHash after use = %v, want ErrNotFound", err)
		}
		if got, err := s.ServerByTokenHash(ctx, v.TokenHash); err != nil || got.ID != "live" {
			t.Errorf("ServerByTokenHash = %+v, %v", got, err)
		}
	})
}

func TestConsumeJoinTakenName(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.InsertServer(ctx, Server{ID: "a", Name: "taken", State: ServerReady}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertServer(ctx, newWaiting("b", "", "h", now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeJoin(ctx, "h", "taken", "k", "e", "t", now); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("ConsumeJoin with a taken name = %v, want ErrAlreadyExists", err)
	}
	// Nothing was spent: the token still works with another name.
	if _, err := s.ConsumeJoin(ctx, "h", "free", "k", "e", "t", now); err != nil {
		t.Errorf("ConsumeJoin again with a free name: %v", err)
	}
}

func TestServerReportsReadyRoleAndExpiredJoins(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.InsertServer(ctx, Server{ID: "a", Name: "one", State: ServerJoining, TokenHash: "ta"}); err != nil {
		t.Fatal(err)
	}
	cert := now.Add(90 * 24 * time.Hour)
	if err := s.SaveServerReport(ctx, 1, `{"agent":"0.18.0"}`, now, cert); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveServerReport(ctx, 1, `{"agent":"0.18.1"}`, now.Add(time.Minute), time.Time{}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.ServerByID(ctx, "a")
	if v.Report != `{"agent":"0.18.1"}` || !v.SeenAt.Equal(now.Add(time.Minute)) || !v.CertNotAfter.Equal(cert) {
		t.Errorf("after two reports: %+v (an unknown certificate end keeps the known one)", v)
	}
	if err := s.SetServerReady(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServerReady(ctx, 1, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNomadServer(ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	v, _ = s.ServerByID(ctx, "a")
	if v.State != ServerReady || !v.ReadyAt.Equal(now) || !v.NomadServer {
		t.Errorf("after ready and role: %+v (ready keeps its first time)", v)
	}
	if err := s.SetServerReady(ctx, 9, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetServerReady(9) = %v, want ErrNotFound", err)
	}

	for _, v := range []Server{
		newWaiting("old", "", "h-old", now.Add(-time.Hour)),
		newWaiting("new", "", "h-new", now.Add(time.Hour)),
	} {
		if _, err := s.InsertServer(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.DeleteExpiredJoins(ctx, now); err != nil || n != 1 {
		t.Fatalf("DeleteExpiredJoins = %d, %v; want 1", n, err)
	}
	if _, err := s.ServerByID(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the expired join is still there: %v", err)
	}
	if _, err := s.ServerByID(ctx, "new"); err != nil {
		t.Errorf("the live join is gone: %v", err)
	}
	if _, err := s.ServerByID(ctx, "a"); err != nil {
		t.Errorf("a joined server was deleted with the expired joins: %v", err)
	}
}

func TestRotateFirstServer(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.RotateFirstServer(ctx, "t", "k", "e"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RotateFirstServer with no first = %v, want ErrNotFound", err)
	}
	if _, err := s.InsertServer(ctx, Server{ID: "a", Name: "one", State: ServerReady, First: true, TokenHash: "old"}); err != nil {
		t.Fatal(err)
	}
	v, err := s.RotateFirstServer(ctx, "new", "key", "203.0.113.4:51820")
	if err != nil || v.TokenHash != "new" || v.PublicKey != "key" || v.Endpoint != "203.0.113.4:51820" || !v.First {
		t.Errorf("RotateFirstServer = %+v, %v", v, err)
	}
}
