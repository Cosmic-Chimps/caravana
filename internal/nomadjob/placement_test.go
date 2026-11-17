package nomadjob

import (
	"reflect"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func TestPlacementKeys(t *testing.T) {
	if got, want := LabelMetaKey("ssd"), "caravana.label.ssd"; got != want {
		t.Errorf("LabelMetaKey = %q, want %q", got, want)
	}
	if got, want := LabelAttribute("ssd"), "${meta.caravana.label.ssd}"; got != want {
		t.Errorf("LabelAttribute = %q, want %q", got, want)
	}
	if got, want := ServerAttribute, "${meta.caravana.server}"; got != want {
		t.Errorf("ServerAttribute = %q, want %q", got, want)
	}
}

func TestPlacementBlocks(t *testing.T) {
	c, a := placementBlocks(app.Service{})
	if c != nil || a != nil {
		t.Errorf("no placement: constraints %v, affinities %v, want none", c, a)
	}

	svc := app.Service{Placement: &app.Placement{
		Require: map[string]string{"ssd": "true", "region": "eu"},
		Prefer:  []app.Preference{{Label: "ssd", Value: "true", Weight: 80}, {Label: "disk", Value: "nvme"}},
		Avoid:   []string{"three", "two"},
	}}
	c, a = placementBlocks(svc)
	wantC := []*api.Constraint{
		{LTarget: "${meta.caravana.label.region}", RTarget: "eu", Operand: "="},
		{LTarget: "${meta.caravana.label.ssd}", RTarget: "true", Operand: "="},
		{LTarget: "${meta.caravana.server}", RTarget: "three", Operand: "!="},
		{LTarget: "${meta.caravana.server}", RTarget: "two", Operand: "!="},
	}
	if !reflect.DeepEqual(c, wantC) {
		t.Errorf("constraints = %+v, want %+v", c, wantC)
	}
	wantA := []*api.Affinity{
		{LTarget: "${meta.caravana.label.ssd}", RTarget: "true", Operand: "=", Weight: new(int8(80))},
		{LTarget: "${meta.caravana.label.disk}", RTarget: "nvme", Operand: "=", Weight: new(int8(50))},
	}
	if !reflect.DeepEqual(a, wantA) {
		t.Errorf("affinities = %+v, want %+v", a, wantA)
	}

	// The same service always gives the same blocks, whatever order avoid
	// was written in.
	svc.Placement.Avoid = []string{"two", "three"}
	c2, _ := placementBlocks(svc)
	if !reflect.DeepEqual(c2, wantC) {
		t.Errorf("avoid in another order: constraints = %+v, want %+v", c2, wantC)
	}
}

// A service without placement gets no constraint and no affinity, so no
// existing job changes.
func TestTranslateWithoutPlacement(t *testing.T) {
	res, err := Translate(whoamiApp(), whoamiDeployment(), labSettings(), nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	g := res.Job.TaskGroups[0]
	if len(g.Constraints) != 0 || len(g.Affinities) != 0 {
		t.Errorf("group has constraints %v, affinities %v, want none", g.Constraints, g.Affinities)
	}
}
