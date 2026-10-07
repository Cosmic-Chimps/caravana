package secretstest

import (
	"context"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/secrets"
)

// TestProvider checks that p follows the secrets.Provider contract: whole
// sets per service, and the kept copy's ownership rules. Every Provider's own
// tests run it, so the Nomad store and this one can't drift apart. p must
// start empty for the app IDs "c-*".
func TestProvider(t *testing.T, p secrets.Provider) {
	ctx := context.Background()
	v1 := secrets.Sets{"web": {"DATABASE_URL": "db1", "API_KEY": "k1"}, "worker": {"DATABASE_URL": "wdb"}}
	v2 := secrets.Sets{"web": {"DATABASE_URL": "db1", "API_KEY": "k2"}}
	v3 := secrets.Sets{"web": {"API_KEY": "k3"}}

	mustRead := func(t *testing.T, appID string, want secrets.Sets) {
		t.Helper()
		got, err := p.Read(ctx, appID)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !got.Equal(want) {
			t.Fatalf("Read = %v, want %v", got, want)
		}
	}
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("an app without secrets reads empty", func(t *testing.T) {
		mustRead(t, "c-empty", secrets.Sets{})
	})

	t.Run("replace sets, replaces and removes whole sets", func(t *testing.T) {
		must(t, p.Replace(ctx, "c-replace", "d1", v1))
		mustRead(t, "c-replace", v1)
		must(t, p.Replace(ctx, "c-replace", "d2", v2))
		mustRead(t, "c-replace", v2) // worker's set and nothing else is gone
		must(t, p.Replace(ctx, "c-replace", "d3", secrets.Sets{}))
		mustRead(t, "c-replace", secrets.Sets{})
	})

	t.Run("restore puts the previous sets back", func(t *testing.T) {
		must(t, p.Replace(ctx, "c-restore", "d1", v1))
		must(t, p.Commit(ctx, "c-restore", "d1"))
		must(t, p.Replace(ctx, "c-restore", "d2", v2))
		must(t, p.Restore(ctx, "c-restore", "d2"))
		mustRead(t, "c-restore", v1)
		// The copy is gone: a second Restore changes nothing.
		must(t, p.Replace(ctx, "c-restore", "d3", v1))
		must(t, p.Restore(ctx, "c-restore", "d2"))
		mustRead(t, "c-restore", v1)
	})

	t.Run("commit forgets the previous sets", func(t *testing.T) {
		must(t, p.Replace(ctx, "c-commit", "d1", v1))
		must(t, p.Commit(ctx, "c-commit", "d1"))
		must(t, p.Replace(ctx, "c-commit", "d2", v2))
		must(t, p.Commit(ctx, "c-commit", "d2"))
		must(t, p.Restore(ctx, "c-commit", "d2"))
		mustRead(t, "c-commit", v2)
	})

	t.Run("a newer deploy takes the copy over", func(t *testing.T) {
		must(t, p.Replace(ctx, "c-takeover", "d1", v1))
		must(t, p.Commit(ctx, "c-takeover", "d1")) // v1 runs
		must(t, p.Replace(ctx, "c-takeover", "d2", v2))
		must(t, p.Replace(ctx, "c-takeover", "d3", v3)) // d2 never got healthy
		must(t, p.Restore(ctx, "c-takeover", "d2"))     // superseded: not the owner
		mustRead(t, "c-takeover", v3)
		must(t, p.Restore(ctx, "c-takeover", "d3"))
		mustRead(t, "c-takeover", v1)
	})

	t.Run("a deploy that changes nothing still takes the copy over", func(t *testing.T) {
		must(t, p.Replace(ctx, "c-same", "d1", v1))
		must(t, p.Commit(ctx, "c-same", "d1"))
		must(t, p.Replace(ctx, "c-same", "d2", v2))
		must(t, p.Replace(ctx, "c-same", "d3", v2))
		must(t, p.Restore(ctx, "c-same", "d3"))
		mustRead(t, "c-same", v1)
	})

	t.Run("delete removes live and kept sets", func(t *testing.T) {
		must(t, p.Replace(ctx, "c-delete", "d1", v1))
		must(t, p.Commit(ctx, "c-delete", "d1"))
		must(t, p.Replace(ctx, "c-delete", "d2", v2))
		must(t, p.Delete(ctx, "c-delete"))
		mustRead(t, "c-delete", secrets.Sets{})
		must(t, p.Restore(ctx, "c-delete", "d2"))
		mustRead(t, "c-delete", secrets.Sets{})
		must(t, p.Delete(ctx, "c-delete")) // twice is fine
	})

	t.Run("apps don't share secrets", func(t *testing.T) {
		must(t, p.Replace(ctx, "c-one", "d1", v1))
		must(t, p.Replace(ctx, "c-one-two", "d2", v3))
		must(t, p.Delete(ctx, "c-one"))
		mustRead(t, "c-one-two", v3)
	})

	t.Run("values arrive byte for byte", func(t *testing.T) {
		tricky := secrets.Sets{"web": {"A": "line1\nline2 \"quoted\" $HOME {{x}} é \\ \t", "EMPTY": ""}}
		must(t, p.Replace(ctx, "c-bytes", "d1", tricky))
		mustRead(t, "c-bytes", tricky)
	})
}
