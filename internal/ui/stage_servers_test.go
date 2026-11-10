package ui

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// TestStageServers is not a test: it stages the data of the Servers pages for a person to look at in a browser
// (feature 031's designer review), with no real servers. It runs the console in-process against the fake
// Nomad, builds a cluster through the service (the installer's first server, the others joined and synced as
// their agents would), prints the address and the sign-in, and serves until the timeout. It does nothing unless
// CARAVANA_STAGE_SERVERS is set:
//
//	CARAVANA_STAGE_SERVERS=1 go test ./internal/ui -run TestStageServers -v -timeout 3h
//
// What it shows: hel-1 (the server Caravana runs on, its node read by the sampler), fsn-1 (ready), fsn-2 (a disk at 89%,
// so "disk filling"), fsn-3 (down, its copies replaced), build-1 (ready, in the pool "build", not taking work),
// fsn-4 (joining: it can't reach hel-1 yet), a waiting command for fsn-9, and a Nomad node called lab that Caravana
// doesn't manage. The three standard apps run on them. Reload the pages to see them; the data doesn't change by itself.
func TestStageServers(t *testing.T) {
	if os.Getenv("CARAVANA_STAGE_SERVERS") == "" {
		t.Skip("set CARAVANA_STAGE_SERVERS=1 to stage the Servers pages")
	}
	e := sampledServersEnv(t)
	e.seedStandard(t)
	e.readyServer(t, "fsn-1")
	e.readyServer(t, "fsn-2")
	e.readyServer(t, "fsn-3")
	e.readyServerInPool(t, "build-1", "build")
	e.join(t, "fsn-4")
	if _, err := e.Svc.CreateJoin(t.Context(), "fsn-9", "", e.URL); err != nil {
		t.Fatal(err)
	}
	e.Nomad.AddNode("node-lab", "lab", "ready", "")
	e.sample(t, "node-2", 700, 300, 1958, 120, 4000, 2, 6000, 19000)
	e.sample(t, "node-3", 1500, 1200, 1958, 800, 4000, 2, 17000, 19000)
	e.sample(t, "node-4", 300, 200, 1958, 60, 4000, 2, 4000, 19000)
	e.sample(t, "node-5", 100, 0, 1958, 20, 4000, 2, 3000, 19000)
	e.Nomad.SetNodeStatusOf("node-4", "down")
	e.Nomad.SetNodeEligibilityOf("node-5", "ineligible", false)
	run := func(id, node, name string) api.AllocationListStub {
		return api.AllocationListStub{ID: id, TaskGroup: "web", JobVersion: 7, ClientStatus: "running", DesiredStatus: "run", NodeID: node, NodeName: name}
	}
	e.Nomad.SetCopies("whoami", run("w1aaaaaa-0000", "node-1", "hel-1"), run("w2bbbbbb-0000", "node-2", "fsn-1"))
	e.Nomad.SetCopies("blog", run("b1cccccc-0000", "node-3", "fsn-2"))
	e.Nomad.SetCopies("api", run("a1dddddd-0000", "node-2", "fsn-1"), run("a2eeeeee-0000", "node-1", "hel-1"))

	fmt.Printf("\nThe Servers pages are staged.\n  open     %s/ops/servers\n  sign in  %s / %s\n  (ctrl-c or the timeout ends it)\n\n", e.URL, testEmail, testPassword)
	wait := 3 * time.Hour
	if deadline, ok := t.Deadline(); ok {
		wait = time.Until(deadline) - 30*time.Second
	}
	time.Sleep(wait)
}
