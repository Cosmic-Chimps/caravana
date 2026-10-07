package secretstest

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/secrets"
)

func TestStore(t *testing.T) {
	TestProvider(t, New())
}

func TestStoreUnavailable(t *testing.T) {
	s := New()
	s.SetUnavailable(true)
	ctx := context.Background()
	calls := map[string]error{
		"Read":    func() error { _, err := s.Read(ctx, "a"); return err }(),
		"Replace": s.Replace(ctx, "a", "d", secrets.Sets{}),
		"Commit":  s.Commit(ctx, "a", "d"),
		"Restore": s.Restore(ctx, "a", "d"),
		"Delete":  s.Delete(ctx, "a"),
	}
	for name, err := range calls {
		if !errors.Is(err, secrets.ErrUnavailable) {
			t.Errorf("%s = %v, want ErrUnavailable", name, err)
		}
	}
}

func TestKeptOwner(t *testing.T) {
	s := New()
	ctx := context.Background()
	if err := s.Replace(ctx, "a", "d1", secrets.Sets{"web": {"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if owner, sets := s.Kept("a"); owner != "d1" || len(sets) != 0 {
		t.Errorf("Kept = %q, %v; want d1 owning the empty previous sets", owner, sets)
	}
}
