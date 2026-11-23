package deploy

import (
	"context"
	"errors"
	"testing"
)

func TestConsoleRouteSupported(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if _, err := d.ConsoleRouteSupported(ctx); !errors.Is(err, ErrNoProxy) {
		t.Fatalf("no job: err = %v, want ErrNoProxy", err)
	}
	// A job from before the feature: Traefik runs, but no template reads the hosts.
	nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	if got, err := d.ConsoleRouteSupported(ctx); err != nil || got {
		t.Errorf("older job = %v, %v; want false", got, err)
	}
	nomad.SetTraefikConsoleRoute("traefik:v3.7.13", "--accesslog=true")
	if got, err := d.ConsoleRouteSupported(ctx); err != nil || !got {
		t.Errorf("job with the template = %v, %v; want true", got, err)
	}

	nomad.Down()
	defer nomad.Up()
	if _, err := d.ConsoleRouteSupported(ctx); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("Nomad down: err = %v, want ErrNomadUnavailable", err)
	}
}

func TestSetConsoleHosts(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if err := d.SetConsoleHosts(ctx, []string{"console.example.com", "old.example.com"}); err != nil {
		t.Fatalf("SetConsoleHosts: %v", err)
	}
	got := nomad.Variable(ConsoleHostsPath)
	if len(got) != 1 || got["hosts"] != "console.example.com old.example.com" {
		t.Errorf("variable %s = %v, want one item hosts with both names", ConsoleHostsPath, got)
	}
	if !nomadPath.MatchString(ConsoleHostsPath) {
		t.Errorf("%q is not a legal Nomad variable path", ConsoleHostsPath)
	}
	// The Team-only users' variable is another path and is left alone.
	nomad.SetVariable(TraefikVariablePath, map[string]string{"x-auth": "team:hash"})
	if err := d.SetConsoleHosts(ctx, []string{"console.example.com"}); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable(TraefikVariablePath); v["x-auth"] != "team:hash" {
		t.Errorf("the team variable = %v, want it untouched", v)
	}
	if got := nomad.Variable(ConsoleHostsPath); got["hosts"] != "console.example.com" {
		t.Errorf("after a second write the variable = %v, want the one host", got)
	}

	for _, bad := range [][]string{{"a b.test"}, {"a.test\nb.test"}, {""}, {"a`)||Host(`b.test"}, {"a.test:8443"}, {"A.test"}, {"a\"b.test"}, {"a.test/x"}, {"-a.test"}} {
		if err := d.SetConsoleHosts(ctx, bad); err == nil {
			t.Errorf("SetConsoleHosts(%q) was accepted", bad)
		}
	}
	if got := nomad.Variable(ConsoleHostsPath); got["hosts"] != "console.example.com" {
		t.Errorf("a refused list changed the variable to %v", got)
	}

	// No hosts: the variable goes, and the route falls back to the job's own domain. Deleting
	// what isn't there is not an error.
	if err := d.SetConsoleHosts(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := nomad.Variable(ConsoleHostsPath); got != nil {
		t.Errorf("after an empty list the variable = %v, want none", got)
	}
	if err := d.SetConsoleHosts(ctx, nil); err != nil {
		t.Errorf("deleting twice: %v", err)
	}

	nomad.Down()
	defer nomad.Up()
	if err := d.SetConsoleHosts(ctx, []string{"x.example.com"}); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("Nomad down: err = %v, want ErrNomadUnavailable", err)
	}
}
