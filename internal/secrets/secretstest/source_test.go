package secretstest

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/secrets"
)

// The fake must behave as a secrets.Source does: absent paths are absent,
// not errors, and the two failures match their sentinels.
func TestSource(t *testing.T) {
	ctx := context.Background()
	var src secrets.Source = NewSource(map[string]string{"A": "1", "B": "2"})
	fake := src.(*Source)

	got, err := src.Read(ctx, []string{"A", "MISSING"})
	if err != nil || len(got) != 1 || got["A"] != "1" {
		t.Fatalf("Read = %v, %v; want only A", got, err)
	}
	if n, err := src.Probe(ctx); err != nil || n != 2 {
		t.Errorf("Probe = %d, %v; want 2", n, err)
	}
	fake.Set("C", "3")
	fake.Delete("A")
	if n, _ := src.Probe(ctx); n != 2 {
		t.Errorf("Probe after Set and Delete = %d, want 2", n)
	}

	fake.SetDown(true)
	if _, err := src.Read(ctx, []string{"B"}); !errors.Is(err, secrets.ErrSourceUnavailable) {
		t.Errorf("down Read error = %v, want ErrSourceUnavailable", err)
	}
	fake.SetDown(false)
	fake.SetRefused(true)
	if _, err := src.Probe(ctx); !errors.Is(err, secrets.ErrSourceRefused) {
		t.Errorf("refused Probe error = %v, want ErrSourceRefused", err)
	}
	fake.SetRefused(false)
	if fake.Reads() != 2 || fake.Probes() != 3 {
		t.Errorf("Reads, Probes = %d, %d; want 2, 3", fake.Reads(), fake.Probes())
	}
}
