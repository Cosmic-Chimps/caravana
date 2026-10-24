package deploy

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestTraefikVariablePath(t *testing.T) {
	if !nomadPath.MatchString(TraefikVariablePath) {
		t.Errorf("%q is not a legal Nomad variable path", TraefikVariablePath)
	}
}

func TestTeamPasswordHash(t *testing.T) {
	const pw = "s3cret-sentinel-023-password"
	h, err := TeamPasswordHash(pw)
	if err != nil {
		t.Fatalf("TeamPasswordHash() = %v", err)
	}
	if strings.Contains(h, pw) {
		t.Error("the hash contains the password")
	}
	if !strings.HasPrefix(h, "$2a$10$") && !strings.HasPrefix(h, "$2b$10$") {
		t.Errorf("hash %q is not bcrypt at cost 10", h)
	}
	if !CheckTeamPassword(h, pw) {
		t.Error("CheckTeamPassword() = false for the right password")
	}
	if CheckTeamPassword(h, pw+"x") || CheckTeamPassword(h, "") || CheckTeamPassword("not a hash", pw) {
		t.Error("CheckTeamPassword() = true for a wrong password or a bad hash")
	}
	h2, _ := TeamPasswordHash(pw)
	if h == h2 {
		t.Error("two hashes of one password are equal; bcrypt must salt")
	}
	if got := TeamLine(h); got != "team:"+h {
		t.Errorf("TeamLine() = %q", got)
	}
}

func TestSyncTeamPasswords(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	t.Run("no entries leaves a marker", func(t *testing.T) {
		if err := d.SyncTeamPasswords(ctx, nil); err != nil {
			t.Fatalf("SyncTeamPasswords(nil) = %v", err)
		}
		got := nomad.Variable(TraefikVariablePath)
		if !reflect.DeepEqual(got, map[string]string{"CARAVANA_TEAM": "none"}) {
			t.Errorf("variable = %v, want the marker only", got)
		}
	})

	t.Run("one item per middleware", func(t *testing.T) {
		entries := map[string]string{"shop-web-1a2b3c4d-auth": "team:$2a$10$aaa", "shop-web-5e6f7a8b-auth": "team:$2a$10$bbb"}
		if err := d.SyncTeamPasswords(ctx, entries); err != nil {
			t.Fatal(err)
		}
		if got := nomad.Variable(TraefikVariablePath); !reflect.DeepEqual(got, entries) {
			t.Errorf("variable = %v, want %v", got, entries)
		}
	})

	t.Run("a second sync replaces, it does not merge", func(t *testing.T) {
		want := map[string]string{"shop-web-5e6f7a8b-auth": "team:$2a$10$ccc"}
		if err := d.SyncTeamPasswords(ctx, want); err != nil {
			t.Fatal(err)
		}
		if got := nomad.Variable(TraefikVariablePath); !reflect.DeepEqual(got, want) {
			t.Errorf("variable = %v, want %v", got, want)
		}
		if err := d.SyncTeamPasswords(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if got := nomad.Variable(TraefikVariablePath); len(got) != 1 || got["CARAVANA_TEAM"] == "" {
			t.Errorf("variable = %v, want the marker again", got)
		}
	})

	t.Run("a Nomad refusal is an error", func(t *testing.T) {
		nomad.FailVariables(403)
		defer nomad.FailVariables(0)
		if err := d.SyncTeamPasswords(ctx, map[string]string{"x-auth": "team:y"}); err == nil {
			t.Error("SyncTeamPasswords() = nil with variables refused")
		}
	})
}
