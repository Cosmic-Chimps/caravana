package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/deploy"
)

func okReport() *NodeReport {
	return &NodeReport{DockerRunning: true, DiskFreeMB: 160 << 10, Reach: []NodeReach{{Index: 1, OK: true}, {Index: 2, OK: true}}}
}

func readyNode() *deploy.Node {
	return &deploy.Node{ID: "n", Status: "ready", Eligible: true, AppAddress: "10.99.0.3"}
}

func TestServerChecks(t *testing.T) {
	others := []otherServer{{1, "hel-1"}, {2, "fsn-1"}}
	cases := []struct {
		name string
		in   checkInput
		ok   []bool   // each check's ok, in order
		want []string // a substring of each check's detail
	}{
		{"all pass", checkInput{Report: okReport(), Others: others, NomadKnown: true, Node: readyNode()},
			[]bool{true, true, true, true},
			[]string{"Connected to the cluster", "Docker running, 160 GB disk free", "Reaches hel-1 and fsn-1 on the private network", "Ready to take work"}},
		{"no report yet: everything after connected is open", checkInput{Others: others, NomadKnown: true, Node: readyNode()},
			[]bool{true, false, false, false}, []string{"Connected", "first report", waitingForCheck, waitingForCheck}},
		{"docker not running", checkInput{Report: &NodeReport{Reach: []NodeReach{{1, true}, {2, true}}}, Others: others, NomadKnown: true, Node: readyNode()},
			[]bool{true, false, false, false}, []string{"Connected", "Docker isn't running", waitingForCheck, waitingForCheck}},
		{"one peer not reached", checkInput{Report: &NodeReport{DockerRunning: true, Reach: []NodeReach{{1, true}, {2, false}}}, Others: others, NomadKnown: true, Node: readyNode()},
			[]bool{true, true, false, false}, []string{"Connected", "Docker running", "Can't reach fsn-1 yet", waitingForCheck}},
		{"a peer missing from the report counts as not reached", checkInput{Report: &NodeReport{DockerRunning: true, Reach: []NodeReach{{1, true}}}, Others: others, NomadKnown: true, Node: readyNode()},
			[]bool{true, true, false, false}, []string{"Connected", "Docker running", "Can't reach fsn-1 yet", waitingForCheck}},
		{"two peers not reached", checkInput{Report: &NodeReport{DockerRunning: true}, Others: others, NomadKnown: true, Node: readyNode()},
			[]bool{true, true, false, false}, []string{"Connected", "Docker running", "Can't reach hel-1 and fsn-1 yet", waitingForCheck}},
		{"no Nomad node yet", checkInput{Report: okReport(), Others: others, NomadKnown: true},
			[]bool{true, true, true, false}, []string{"Connected", "Docker", "Reaches", "Waiting for Nomad"}},
		{"Nomad can't be read", checkInput{Report: okReport(), Others: others},
			[]bool{true, true, true, false}, []string{"Connected", "Docker", "Reaches", "Nomad can't be read"}},
		{"an ineligible node is not ready", checkInput{Report: okReport(), Others: others, NomadKnown: true, Node: &deploy.Node{Status: "ready"}},
			[]bool{true, true, true, true}, nil},
		{"a lone joining server still reaches the first", checkInput{Report: &NodeReport{DockerRunning: true, Reach: []NodeReach{{1, true}}}, Others: others[:1], NomadKnown: true, Node: readyNode()},
			[]bool{true, true, true, true}, []string{"", "", "Reaches hel-1 on the private network", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := serverChecks(c.in)
			names := []string{"connected", "docker", "network", "ready"}
			if len(got) != 4 {
				t.Fatalf("checks = %v", got)
			}
			var oks []bool
			for i, g := range got {
				if g.Name != names[i] {
					t.Errorf("check %d is %q, want %q", i, g.Name, names[i])
				}
				oks = append(oks, g.OK)
			}
			if c.name == "an ineligible node is not ready" {
				// Eligible is the zero value false here on purpose.
				if got[3].OK {
					t.Errorf("an ineligible node passed: %v", got[3])
				}
				return
			}
			if !slices.Equal(oks, c.ok) {
				t.Errorf("ok = %v, want %v\n%v", oks, c.ok, got)
			}
			for i, w := range c.want {
				if !strings.Contains(got[i].Detail, w) {
					t.Errorf("check %s detail = %q, want it to contain %q", got[i].Name, got[i].Detail, w)
				}
			}
		})
	}
}

func TestNameList(t *testing.T) {
	for in, want := range map[string]string{"": "", "a": "a", "a,b": "a and b", "a,b,c": "a, b and c"} {
		var names []string
		if in != "" {
			names = strings.Split(in, ",")
		}
		if got := nameList(names); got != want {
			t.Errorf("nameList(%q) = %q, want %q", in, got, want)
		}
	}
}
