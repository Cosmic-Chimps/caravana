package service

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// "Recent on this server" is this server's (feature 031, critique ruling 4): a problem about another server isn't
// on it. An item's key names its server: a remote disk carries the name where the service goes, a server kind has it
// as its subject; everything else is the server Caravana runs on.
func TestProblemServerOf(t *testing.T) {
	for key, want := range map[string]string{
		"disk_nearly_full|||/":                "",
		"disk_nearly_full||fsn-1|/":           "fsn-1",
		"server_down|||fsn-2":                 "fsn-2",
		"server_certificate_expiring|||fsn-2": "fsn-2",
		"os_reboot_required|||":               "",
		"restarting|blog|web|":                "",
		"garbage":                             "",
	} {
		if got := problemServerOf(key); got != want {
			t.Errorf("the server of %q = %q, want %q", key, got, want)
		}
	}
}

func TestRowIsOfServer(t *testing.T) {
	problem := func(key string) store.ActivityRow {
		return store.ActivityRow{Source: store.ActivityProblem, Program: key}
	}
	tests := []struct {
		name  string
		row   store.ActivityRow
		on    string
		local bool
		want  bool
	}{
		{"its own disk", problem("disk_nearly_full||fsn-1|/"), "fsn-1", false, true},
		{"another server's disk", problem("disk_nearly_full||fsn-1|/"), "hel-1", true, false},
		{"the local disk on the local server", problem("disk_nearly_full|||/"), "hel-1", true, true},
		{"the local disk on another server", problem("disk_nearly_full|||/"), "fsn-1", false, false},
		{"another server being down", problem("server_down|||fsn-2"), "hel-1", true, false},
		{"itself being down", problem("server_down|||fsn-2"), "fsn-2", false, true},
		{"a reboot is the local server's", problem("os_reboot_required|||"), "hel-1", true, true},
		{"a reboot isn't another's", problem("os_reboot_required|||"), "fsn-1", false, false},
		{"a boot is the local server's", store.ActivityRow{Source: store.ActivityHost}, "hel-1", true, true},
		{"a boot isn't another's", store.ActivityRow{Source: store.ActivityHost}, "fsn-1", false, false},
		{"an app's restart is anywhere", store.ActivityRow{Source: store.ActivityAction}, "fsn-1", false, true},
	}
	for _, tc := range tests {
		if got := rowIsOfServer(tc.row, tc.on, tc.local); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// With the problem events in the store, the server feed of one server has its own items and not another's.
func TestActivityForServer(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	now := e.svc.clock.Now().UTC()
	for i, p := range []struct{ kind, key, text string }{
		{"disk_nearly_full", "disk_nearly_full|||/", "The disk at / is 90% full."},
		{"disk_nearly_full", "disk_nearly_full||fsn-1|/", "The disk at / on fsn-1 is 91% full."},
		{"server_down", "server_down|||fsn-1", "fsn-1 is down."},
	} {
		at := now.Add(time.Duration(i) * time.Second)
		if err := e.store.ApplyProblemCheck(ctx, store.ProblemCheck{SeenAt: at,
			Opened: []store.SentProblem{{Key: p.key, Kind: p.kind, OpenedAt: at, LastSeenAt: at, Summary: p.text}}}); err != nil {
			t.Fatal(err)
		}
	}
	local, err := e.svc.Activity(ctx, ActivityQuery{Limit: 20, Server: true, ForServer: "hel-1", Local: true})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := e.svc.Activity(ctx, ActivityQuery{Limit: 20, Server: true, ForServer: "fsn-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range local {
		if en.Sentence == "fsn-1 is down." || en.Sentence == "The disk at / on fsn-1 is 91% full." {
			t.Errorf("the local server's feed has %q", en.Sentence)
		}
	}
	for _, en := range remote {
		if en.Sentence == "The disk at / is 90% full." {
			t.Errorf("fsn-1's feed has the local disk")
		}
	}
}

// The remote server's feed has its own item, and the local disk isn't in it.
func TestActivityForServerHasItsOwnRows(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	at := e.svc.clock.Now().UTC()
	for i, p := range []struct{ key, kind string }{{"disk_nearly_full|||/", "disk_nearly_full"}, {"server_down|||fsn-1", "server_down"}} {
		when := at.Add(time.Duration(i) * time.Second)
		if err := e.store.ApplyProblemCheck(ctx, store.ProblemCheck{SeenAt: when,
			Opened: []store.SentProblem{{Key: p.key, Kind: p.kind, OpenedAt: when, LastSeenAt: when, Summary: p.kind}}}); err != nil {
			t.Fatal(err)
		}
	}
	remote, err := e.svc.Activity(ctx, ActivityQuery{Limit: 20, Server: true, ForServer: "fsn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(remote) != 1 || remote[0].Sentence != "server_down" {
		t.Errorf("fsn-1's feed = %+v, want its one item", remote)
	}
}
