package nomadjob

import "testing"

// A group with more than one copy spreads over the servers; one copy has
// nothing to spread, so its job doesn't change (feature 031, research L8).
func TestTranslateSpreads(t *testing.T) {
	tests := []struct {
		name     string
		replicas *int
		want     bool
	}{
		{"no replicas set is one copy", nil, false},
		{"one copy", new(1), false},
		{"two copies", new(2), true},
		{"five copies", new(5), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := whoamiApp()
			a.Services[0].Replicas = tt.replicas
			res, err := Translate(a, whoamiDeployment(), labSettings(), nil)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			got := res.Job.TaskGroups[0].Spreads
			if !tt.want {
				if len(got) != 0 {
					t.Fatalf("Spreads = %v, want none", got)
				}
				return
			}
			if len(got) != 1 || got[0].Attribute != "${node.unique.id}" || got[0].Weight == nil || *got[0].Weight != 100 {
				t.Fatalf("Spreads = %+v, want one spread over ${node.unique.id} with weight 100", got)
			}
		})
	}
}
