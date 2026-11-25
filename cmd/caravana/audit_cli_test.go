package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// What the host's CLI changes is noted in the audit log (feature 057, phase C), done by the host.
func TestCLIChangesAreNotedAsTheHost(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := admin(t, dir, cliPassword+"\n", "init", "--email", "you@example.com")
	if code != 0 {
		t.Fatalf("init = %d %s", code, stderr)
	}
	code, token, stderr := admin(t, dir, "", "create-token", "--name", "ci")
	token = strings.TrimSpace(token)
	if code != 0 || token == "" {
		t.Fatalf("create-token = %d %q %s", code, token, stderr)
	}
	if code, _, stderr := admin(t, dir, "", "revoke-token", "--name", "ci"); code != 0 {
		t.Fatalf("revoke-token = %d %s", code, stderr)
	}
	addCLIPasskey(t, dir, "console.example.com", "MacBook") // signs in with the first password
	if code, _, stderr := admin(t, dir, "a brand new password\n", "reset-password"); code != 0 {
		t.Fatalf("reset-password = %d %s", code, stderr)
	}
	_, stdout, _ := admin(t, dir, "", "passkeys", "list")
	id := strings.Fields(strings.Split(strings.TrimSpace(stdout), "\n")[1])[0]
	if code, _, stderr := admin(t, dir, "", "passkeys", "remove", "--id", id); code != 0 {
		t.Fatalf("passkeys remove = %d %s", code, stderr)
	}
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := serverCmd(t, dir, "adopt-first", "--name", "hel-1", "--public-key", pub, "--address", "203.0.113.4"); code != 0 {
		t.Fatalf("adopt-first = %d %s", code, stderr)
	}

	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, _, err := st.ListAuditEvents(context.Background(), store.AuditFilter{Limit: store.MaxAuditLimit})
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		action audit.Action
		kind   audit.Kind
	}
	seen := map[key]bool{}
	for _, r := range rows {
		seen[key{r.Action, r.Resource.Kind}] = true
		if r.Action == audit.ActionSignedIn || r.Action == audit.ActionCreated && r.Resource.Kind == audit.KindPasskey {
			continue // addCLIPasskey stands in for the console here: a person signs in and adds it
		}
		if r.Actor.Kind != audit.ActorHost || r.Door != audit.DoorHost || r.Fallback {
			t.Errorf("%s %s: actor %+v door %s fallback %v, want the host", r.Action, r.Resource.Kind, r.Actor, r.Door, r.Fallback)
		}
	}
	for _, want := range []key{
		{audit.ActionCreated, audit.KindPerson}, {audit.ActionCreated, audit.KindToken}, {audit.ActionRevoked, audit.KindToken},
		{audit.ActionUpdated, audit.KindAccount}, {audit.ActionRemoved, audit.KindPasskey}, {audit.ActionCreated, audit.KindServer},
	} {
		if !seen[want] {
			t.Errorf("no %s %s entry; got %v", want.action, want.kind, seen)
		}
	}
	raw, _ := json.Marshal(rows)
	for _, secret := range []string{token, cliPassword, "a brand new password"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("an entry holds %q", secret)
		}
	}
}
