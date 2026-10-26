package service

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A Service that was started with New (its default gaps, not the helpers'
// short ones) and cancelled mid-rollout must leave nothing behind once Wait
// returns: closing the store right after is final, and the data directory
// doesn't change afterwards. It once did: a write cut off by the cancellation
// is rolled back by database/sql in a goroutine of its own, which discards the
// store's only connection after Wait has returned. SQLite then checkpointed
// and deleted its -wal file a moment later, which made a test's temp dir fail
// to remove ("directory not empty"). Without the Ping at the end of Wait this
// test fails in its first rounds.
func TestWaitLeavesNothingWritingToTheStore(t *testing.T) {
	ctx := context.Background()
	reg := registrytest.New(t)
	reg.AddIndex("traefik/whoami", "latest", whoamiDigest)
	nomad := deploytest.New(t)
	nomad.Script("whoami", 0, stuckRollout)
	sec := secretstest.New()

	// Each round is a chance for the cancellation to cut a write.
	for round := range 10 {
		dir := t.TempDir()
		st, err := store.Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		svc := New(st, newDeployer(t, reg, nomad), sec, discardLogger())
		runCtx, stop := context.WithCancel(ctx)
		if err := svc.Start(runCtx); err != nil {
			t.Fatal(err)
		}
		a := whoami()
		a.ID = "wait" + string(rune('a'+round%26)) + string(rune('a'+round/26))
		if _, err := svc.Deploy(ctx, a, app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(round%5) * time.Millisecond) // let the watcher get into its updates
		stop()
		svc.Wait()
		if err := st.Close(); err != nil {
			t.Fatalf("round %d: close the store: %v", round, err)
		}

		before := dirState(t, dir)
		time.Sleep(10 * time.Millisecond)
		if after := dirState(t, dir); after != before {
			t.Fatalf("round %d: the data dir changed after Wait and Close:\nbefore: %s\nafter:  %s", round, before, after)
		}
	}
}

// Wait is safe to call when Start never ran, and again after a Wait.
func TestWaitWithoutStart(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, nil, nil, discardLogger())
	svc.Wait()
	svc.Wait()
}

// dirState lists a directory's files with their sizes, for comparing two moments.
func dirState(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			out = append(out, e.Name()+" (gone)") // removed between the listing and the stat
			continue
		}
		out = append(out, e.Name()+" "+time.Duration(info.Size()).String())
	}
	return strings.Join(out, ", ")
}
