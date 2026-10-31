package main

// The os-updates gate step (feature 027 page 3, quickstart §5), through the API with the gate's
// token. It is in its own file only because gate_test.go is long; runGate lists it after
// monitoring.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// osMessage is the part of a notification the step reads: the webhook body of a problem or of
// caravana.release.
type osMessage struct {
	Type    string `json:"type"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Release *struct {
		Current string `json:"current"`
		Latest  string `json:"latest"`
		URL     string `json:"url"`
	} `json:"release"`
}

// osMessages are the receiver's messages that match.
func osMessages(rx *notifyReceiver, match func(osMessage) bool) []osMessage {
	var out []osMessage
	for _, r := range rx.all() {
		var m osMessage
		if json.Unmarshal(r.Body, &m) == nil && match(m) {
			out = append(out, m)
		}
	}
	return out
}

// osUpdatesStep: what the host's OS says reaches the owner.
//
// On a real host (no host root to write into) the API's reads are checked: Updates reads the
// host's files (os.readable, Ubuntu) and the host read has the node's kernel, which proves the
// installed unit's sandbox lets Caravana read them.
//
// In the lab, a webhook receiver and a channel are set up, then:
//  1. A kernel is installed that the running one is behind, the reboot marker names it, and 14
//     updates wait (9 security): problem.opened for os_reboot_required arrives once, within the
//     sampler's tick (10 s) and the notifier's check (30 s).
//  2. The marker is removed: problem.resolved follows once the item has been gone for the resolve
//     delay (10 s in the lab).
//  3. The release stand-in announces 0.17.0: one caravana.release arrives (current 0.15.0, latest
//     0.17.0, with the release's link), still one after two more checks, and the release is on no
//     needs-you item.
//
// No message names a CVE.
func (g *gate) osUpdatesStep(t *testing.T) {
	root, rel := g.target.HostRoot, g.target.Release
	if root == "" || rel == nil {
		var up labUpdates
		g.getJSON(t, "/v1/updates", &up)
		if !up.OS.Readable || up.OS.Distro.ID != "ubuntu" {
			t.Errorf("GET /v1/updates: os.readable %v, distro %q; want a readable Ubuntu host (is the unit's sandbox in the way?)", up.OS.Readable, up.OS.Distro.ID)
		}
		if up.Caravana.Current == "" {
			t.Error("GET /v1/updates has no caravana.current")
		}
		var host struct {
			Node *struct {
				Kernel string `json:"kernel"`
			} `json:"node"`
		}
		waitFor(t, "the host read to have the node's kernel", 3*time.Minute, func() bool {
			g.getJSON(t, "/v1/host", &host)
			return host.Node != nil && host.Node.Kernel != ""
		})
		t.Log("this target has no host root to write files into: the reboot and release notifications are skipped")
		return
	}
	t.Cleanup(func() { logTail(t, g.target) })
	t.Cleanup(func() { rel.Set("v" + labCurrentVersion) })

	// The receiver and the channel (every problem event; caravana.release goes to every channel).
	rx := &notifyReceiver{}
	srv := httptest.NewServer(rx)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		if t.Failed() {
			for _, r := range rx.all() {
				t.Logf("the receiver got: %s", r.Body)
			}
		}
	})
	raw := g.sendWant(t, http.StatusCreated, http.MethodPost, "/v1/notifications/channels",
		`{"kind":"webhook","name":"os receiver","url":"`+srv.URL+`","events":["problems"]}`)
	var channel struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &channel); err != nil || channel.ID == "" {
		t.Fatalf("create the channel: %s", raw)
	}
	t.Cleanup(func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/notifications/channels/"+channel.ID, "", ""); err == nil {
			resp.Body.Close()
		}
	})
	isReboot := func(typ string) func(osMessage) bool {
		return func(m osMessage) bool { return m.Type == typ && m.Kind == "os_reboot_required" }
	}

	// 1. A pending kernel.
	fx := newHostFixture(t, root)
	fx.ubuntu()
	fx.rebootNeeded()
	waitFor(t, "problem.opened for the reboot to reach the receiver", 3*time.Minute, func() bool {
		return len(osMessages(rx, isReboot("problem.opened"))) > 0
	})
	up := g.updates(t)
	if !up.OS.Reboot.Required || up.OS.Reboot.InstalledKernel != labInstalledKernel || up.OS.Updates.Total != 14 || up.OS.Updates.Security != 9 {
		t.Errorf("GET /v1/updates = %+v, want the pending kernel and 14 updates (9 security)", up.OS)
	}
	if got := osMessages(rx, isReboot("problem.opened")); len(got) != 1 || !strings.Contains(strings.ToLower(got[0].Title), "reboot") {
		t.Errorf("problem.opened for the reboot = %+v, want one that says a reboot is needed", got)
	}

	// 2. The marker goes.
	fx.rebootCleared()
	waitFor(t, "problem.resolved for the reboot to reach the receiver", 3*time.Minute, func() bool {
		return len(osMessages(rx, isReboot("problem.resolved"))) > 0
	})
	if g.hasAttention(t, "os_reboot_required") {
		t.Error("the reboot item is still on the needs-you list after the marker went")
	}

	// 3. A newer release: one caravana.release, and no needs-you item for it.
	rel.Set("v0.17.0")
	isRelease := func(m osMessage) bool { return m.Type == "caravana.release" }
	waitFor(t, "caravana.release to reach the receiver", 3*time.Minute, func() bool { return len(osMessages(rx, isRelease)) > 0 })
	time.Sleep(2*labReleaseGap + 10*time.Second) // two more checks of the same release
	got := osMessages(rx, isRelease)
	if len(got) != 1 || got[0].Release == nil || got[0].Release.Current != labCurrentVersion || got[0].Release.Latest != "0.17.0" || got[0].Release.URL == "" {
		t.Errorf("caravana.release = %+v, want one: current %s, latest 0.17.0, with a link", got, labCurrentVersion)
	}
	for _, kind := range g.attentionKinds(t) {
		if strings.Contains(kind, "release") {
			t.Errorf("a newer release is a needs-you item: %s", kind)
		}
	}
	for _, r := range rx.all() {
		if strings.Contains(strings.ToLower(string(r.Body)), "cve") {
			t.Errorf("a message names a CVE: %s", r.Body)
		}
	}
}
