package service

import (
	"fmt"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/notify"
)

// ServerCheck is one of the four checks of a joining server (contracts/api.md §3).
type ServerCheck struct {
	Name   string
	OK     bool
	Detail string
}

// otherServer is another server of the cluster that is joining or ready: one this server must reach.
type otherServer struct {
	Index int
	Name  string
}

// checkInput is what the four checks read.
type checkInput struct {
	// Report is the server's last sync; nil until its first.
	Report *NodeReport
	// Others are every other joining or ready server.
	Others []otherServer
	// NomadKnown says Nomad could be read; Node is its node at this server's app address, nil when
	// it has none.
	NomadKnown bool
	Node       *deploy.Node
}

const waitingForCheck = "Waiting for the check above."

// serverChecks evaluates the four checks in their order: connected, docker, network, ready. A check
// that follows an open one is open too. It is pure, so a table test calls it directly.
func serverChecks(in checkInput) []ServerCheck {
	checks := []ServerCheck{
		{Name: "connected", OK: true, Detail: "Connected to the cluster"},
		dockerCheck(in),
		networkCheck(in),
		readyCheck(in),
	}
	open := false
	for i := range checks {
		if open {
			checks[i].OK, checks[i].Detail = false, waitingForCheck
		}
		if !checks[i].OK {
			open = true
		}
	}
	return checks
}

func dockerCheck(in checkInput) ServerCheck {
	c := ServerCheck{Name: "docker"}
	switch {
	case in.Report == nil:
		c.Detail = "Waiting for the server's first report"
	case !in.Report.DockerRunning:
		c.Detail = "Docker isn't running"
	default:
		c.OK = true
		c.Detail = "Docker running"
		if in.Report.DiskFreeMB > 0 {
			c.Detail += ", " + notify.Size(in.Report.DiskFreeMB<<20) + " disk free"
		}
	}
	return c
}

func networkCheck(in checkInput) ServerCheck {
	c := ServerCheck{Name: "network"}
	if in.Report == nil {
		c.Detail = "Waiting for the server's first report"
		return c
	}
	reached := map[int]bool{}
	for _, r := range in.Report.Reach {
		if r.OK {
			reached[r.Index] = true
		}
	}
	var ok, open []string
	for _, o := range in.Others {
		if reached[o.Index] {
			ok = append(ok, o.Name)
		} else {
			open = append(open, o.Name)
		}
	}
	switch {
	case len(open) > 0:
		c.Detail = "Can't reach " + nameList(open) + " yet"
	case len(ok) == 0:
		c.OK, c.Detail = true, "No other server to reach"
	default:
		c.OK, c.Detail = true, "Reaches "+nameList(ok)+" on the private network"
	}
	return c
}

func readyCheck(in checkInput) ServerCheck {
	c := ServerCheck{Name: "ready"}
	switch {
	case !in.NomadKnown:
		c.Detail = "Nomad can't be read right now"
	case in.Node == nil:
		c.Detail = "Waiting for Nomad on this server to register"
	case in.Node.Down() || in.Node.Status != "ready":
		c.Detail = "Nomad doesn't see this server as ready yet"
	case !in.Node.Eligible || in.Node.Draining:
		c.Detail = "Nomad isn't placing work on this server"
	default:
		c.OK, c.Detail = true, "Ready to take work"
	}
	return c
}

// nameList writes names as "a", "a and b", "a, b and c".
func nameList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// allPass says every check is ok.
func allPass(checks []ServerCheck) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return len(checks) > 0
}

// String is for test failures.
func (c ServerCheck) String() string { return fmt.Sprintf("%s ok=%v %q", c.Name, c.OK, c.Detail) }
