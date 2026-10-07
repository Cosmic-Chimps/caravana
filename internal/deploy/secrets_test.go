package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
)

func newTestVariables(t *testing.T) (*Variables, *deploytest.Nomad) {
	t.Helper()
	nomad := deploytest.New(t)
	client, err := NewNomadClient(nomad.Addr(), "")
	if err != nil {
		t.Fatalf("NewNomadClient: %v", err)
	}
	return NewVariables(client), nomad
}

func TestVariablesContract(t *testing.T) {
	v, _ := newTestVariables(t)
	secretstest.TestProvider(t, v)
}

func TestVariablesLayout(t *testing.T) {
	v, nomad := newTestVariables(t)
	ctx := context.Background()
	sets := secrets.Sets{"web": {"API_KEY": "k1"}, "worker": {"API_KEY": "k2"}}
	if err := v.Replace(ctx, "blog", "d1", sets); err != nil {
		t.Fatal(err)
	}
	want := []string{"nomad/jobs/blog/caravana/previous", "nomad/jobs/blog/web", "nomad/jobs/blog/worker"}
	if got := nomad.VariablePaths(); !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	if got := nomad.Variable("nomad/jobs/blog/web"); len(got) != 1 || got["API_KEY"] != "k1" {
		t.Errorf("web's variable = %v, want only API_KEY=k1", got)
	}
	if got := nomad.Variable("nomad/jobs/blog/caravana/previous"); len(got) != 1 || got["deployment"] != "d1" {
		t.Errorf("kept copy = %v, want only its owner d1 (nothing was there before)", got)
	}

	if err := v.Commit(ctx, "blog", "d1"); err != nil {
		t.Fatal(err)
	}
	if err := v.Replace(ctx, "blog", "d2", secrets.Sets{"web": {"API_KEY": "k3"}}); err != nil {
		t.Fatal(err)
	}
	kept := nomad.Variable("nomad/jobs/blog/caravana/previous")
	if kept["deployment"] != "d2" || kept["web/API_KEY"] != "k1" || kept["worker/API_KEY"] != "k2" {
		t.Errorf("kept copy = %v, want d2 owning the previous sets", kept)
	}
	if nomad.Variable("nomad/jobs/blog/worker") != nil {
		t.Error("worker's variable is still there after its secrets were removed")
	}
}

func TestVariablesReadSkipsOtherPaths(t *testing.T) {
	v, nomad := newTestVariables(t)
	nomad.SetVariable("nomad/jobs/blog/web", map[string]string{"A": "1"})
	nomad.SetVariable("nomad/jobs/blog/web/web", map[string]string{"B": "2"}) // not ours
	nomad.SetVariable("nomad/jobs/blog2/web", map[string]string{"C": "3"})    // another app
	got, err := v.Read(context.Background(), "blog")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(secrets.Sets{"web": {"A": "1"}}) {
		t.Errorf("Read = %v, want only web's own variable", got)
	}
}

func TestVariablesUnavailable(t *testing.T) {
	ctx := context.Background()
	t.Run("unreachable", func(t *testing.T) {
		v, nomad := newTestVariables(t)
		nomad.Down()
		_, err := v.Read(ctx, "blog")
		if !errors.Is(err, secrets.ErrUnavailable) || !strings.Contains(err.Error(), "unreachable") {
			t.Errorf("Read = %v, want ErrUnavailable saying unreachable", err)
		}
	})
	t.Run("token refused", func(t *testing.T) {
		v, nomad := newTestVariables(t)
		nomad.RequireToken("another")
		err := v.Replace(ctx, "blog", "d1", secrets.Sets{"web": {"A": "s3cr3t-marker"}})
		if !errors.Is(err, secrets.ErrUnavailable) || !strings.Contains(err.Error(), "nomad/jobs/*") {
			t.Errorf("Replace = %v, want ErrUnavailable naming the policy", err)
		}
		if strings.Contains(err.Error(), "s3cr3t-marker") {
			t.Errorf("error %q contains the value", err)
		}
	})
	t.Run("a set Nomad refuses", func(t *testing.T) {
		v, _ := newTestVariables(t)
		err := v.Replace(ctx, "blog", "d1", secrets.Sets{"web": {"A": strings.Repeat("x", 70*1024)}})
		if !errors.Is(err, secrets.ErrUnavailable) || strings.Contains(err.Error(), "xxxx") {
			t.Errorf("Replace = %v, want ErrUnavailable without the value", err)
		}
	})
}
