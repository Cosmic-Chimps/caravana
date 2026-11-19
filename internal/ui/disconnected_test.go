package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// A disconnected copy (its server stopped answering, task ctbb) is idle, not failed, offers no command
// or terminal, and a disconnected node says so in the warning colour with its own words.
func TestDisconnectedWords(t *testing.T) {
	if got := copyStatusView(deploy.CopyDisconnected); got.Kind != "idle" || got.Word != "disconnected" {
		t.Errorf("copy status = %+v, want idle disconnected", got)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	e := copyEntryOf(now, service.Copy{ID: "abcdef0123", Service: "web", Status: deploy.CopyDisconnected})
	if e.Live || e.Stopping || e.Status.Word != "disconnected" {
		t.Errorf("entry = %+v, want a copy that isn't live (no Run a command form)", e)
	}
	if e := copyEntryOf(now, service.Copy{ID: "abcdef0123", Service: "web", Status: deploy.CopyHealthy}); !e.Live {
		t.Error("a healthy copy isn't live")
	}
	if got := serverState("disconnected"); got.Kind != "warning" || got.Word != "disconnected" {
		t.Errorf("serverState = %+v", got)
	}
	if nodeMark("disconnected") != "warning" {
		t.Errorf("nodeMark = %q, want warning", nodeMark("disconnected"))
	}
	if got := serverRowState(service.ServerView{Managed: true, State: "disconnected"}); got.Kind != "warning" || got.Word != "disconnected" {
		t.Errorf("serverRowState = %+v", got)
	}

	node := &service.HostNode{NodeFacts: deploy.NodeFacts{State: "disconnected", StatusAt: now.Add(-3 * time.Minute)}}
	f, ok := lastSeenFact(now, node, now, time.Minute)
	if !ok || f.Mark != "warning" || f.Value != "disconnected 3 minutes ago" {
		t.Errorf("last seen = %+v, %v", f, ok)
	}
	banners := (&server{}).nodeBanners(now, node)
	if len(banners) != 1 || banners[0].Mark != "warning" || !strings.Contains(banners[0].Line.String(), "Nomad stopped hearing this server 3 minutes ago") {
		t.Errorf("banners = %+v", banners)
	}

	b := disconnectedBanner(now, service.ServerView{Name: "fsn-2", DisconnectedSince: now.Add(-time.Minute), KeptCopies: 1})
	if b.Mark != "warning" || b.Line.String() != "fsn-2 disconnected since" || b.Clock == nil || b.After.String() != ", 1 copy kept running there." {
		t.Errorf("banner = %+v", b)
	}
}

// removeRefusal allows a disconnected server, as the service does.
func TestRemoveOfADisconnectedServer(t *testing.T) {
	if err := removeRefusal(service.ServerView{Managed: true, State: "disconnected"}); err != nil {
		t.Errorf("removeRefusal = %v, want none", err)
	}
	if err := removeRefusal(service.ServerView{Managed: true, State: "ready"}); err == nil {
		t.Error("a ready server is removable")
	}
}
