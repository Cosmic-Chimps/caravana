package app

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// Limits of labels and placement rules (feature 042).
const (
	// MaxServerLabels is how many labels one server may have.
	MaxServerLabels = 16
	// MaxRequire is how many labels one service may require.
	MaxRequire = 8
	// MaxPrefer is how many preferences one service may have.
	MaxPrefer = 8
	// MaxAvoid is how many servers one service may avoid.
	MaxAvoid = 16
	// DefaultPreferenceWeight is the weight of a preference that sets none.
	DefaultPreferenceWeight = 50
	// MaxPreferenceWeight is the heaviest a preference can be.
	MaxPreferenceWeight = 100
)

// labelNameRE: a server label's name is lowercase, starts with a letter and
// ends up in a Nomad node meta key (caravana.label.<name>).
var labelNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// labelValueRE: a label's value is 1 to 64 characters that are safe in a
// Nomad constraint and a Compose file.
var labelValueRE = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,64}$`)

// ValidLabelName reports whether s may name a server label: a lowercase
// letter, then up to 31 lowercase letters, digits and "-".
func ValidLabelName(s string) bool { return labelNameRE.MatchString(s) }

// ValidLabelValue reports whether s may be a server label's value: 1 to 64
// letters, digits and ". _ : / -".
func ValidLabelValue(s string) bool { return labelValueRE.MatchString(s) }

// Placement says where a service's copies may run: on which servers, by the
// labels the owner gave them and by the servers' names. nil means any server
// that takes work (feature 042).
type Placement struct {
	// Require lists labels a server must have, as name to value: a copy runs
	// only on a server that has every one. At most 8.
	Require map[string]string `json:"require,omitempty"`
	// Prefer lists labels the scheduler favours, in order. A copy still runs
	// elsewhere when no preferred server has room. At most 8.
	Prefer []Preference `json:"prefer,omitempty"`
	// Avoid lists server names the service never runs on. At most 16.
	Avoid []string `json:"avoid,omitempty"`
}

// Preference is a label the scheduler favours.
type Preference struct {
	// Label and Value name the server label that is preferred.
	Label string `json:"label"`
	Value string `json:"value"`
	// Weight is how much it counts, 1 to 100. 0 means
	// DefaultPreferenceWeight.
	Weight int `json:"weight,omitzero"`
}

// WeightOrDefault is the preference's weight, 50 when it sets none.
func (p Preference) WeightOrDefault() int {
	if p.Weight == 0 {
		return DefaultPreferenceWeight
	}
	return p.Weight
}

// Empty reports whether p has no rule.
func (p Placement) Empty() bool {
	return len(p.Require) == 0 && len(p.Prefer) == 0 && len(p.Avoid) == 0
}

// CheckLabels checks a server's label set: at most 16 labels, each with a
// valid name and value. field is the path its errors name ("labels"): a bad
// name or too many are reported on field, a bad value on field.<name>.
func CheckLabels(labels map[string]string, field string) error {
	return checkLabelSet(labels, field, MaxServerLabels)
}

// checkLabelSet is CheckLabels with the limit as an argument, so a service's
// require uses it with its own. Names are checked in order, so the error is
// the same every time.
func checkLabelSet(labels map[string]string, field string, limit int) error {
	if len(labels) > limit {
		return NewFieldError(field, "has %d labels; at most %d are allowed", len(labels), limit)
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !ValidLabelName(name) {
			return NewFieldError(field, "label name %q must start with a lowercase letter and have at most 32 lowercase letters, digits and \"-\"", name)
		}
		if !ValidLabelValue(labels[name]) {
			return NewFieldError(field+"."+name, "%q must be 1 to 64 letters, digits and \". _ : / -\"", labels[name])
		}
	}
	return nil
}

// validatePlacement checks a service's placement. field is the service's
// path, e.g. "services[0]".
func validatePlacement(field string, p *Placement) error {
	if p == nil {
		return nil
	}
	if p.Empty() {
		return NewFieldError(field+".placement", "has no rule: set require, prefer or avoid, or leave placement out")
	}
	if err := checkLabelSet(p.Require, field+".placement.require", MaxRequire); err != nil {
		return err
	}

	if len(p.Prefer) > MaxPrefer {
		return NewFieldError(field+".placement.prefer", "has %d preferences; at most %d are allowed", len(p.Prefer), MaxPrefer)
	}
	seen := map[string]bool{}
	for j, pref := range p.Prefer {
		f := fmt.Sprintf("%s.placement.prefer[%d]", field, j)
		switch {
		case !ValidLabelName(pref.Label):
			return NewFieldError(f+".label", "%q must start with a lowercase letter and have at most 32 lowercase letters, digits and \"-\"", pref.Label)
		case seen[pref.Label]:
			return NewFieldError(f+".label", "%q is preferred twice", pref.Label)
		case !ValidLabelValue(pref.Value):
			return NewFieldError(f+".value", "%q must be 1 to 64 letters, digits and \". _ : / -\"", pref.Value)
		case pref.Weight < 0 || pref.Weight > MaxPreferenceWeight:
			return NewFieldError(f+".weight", "is %d; it must be 1 to %d (leave it out for %d)", pref.Weight, MaxPreferenceWeight, DefaultPreferenceWeight)
		}
		seen[pref.Label] = true
	}

	if len(p.Avoid) > MaxAvoid {
		return NewFieldError(field+".placement.avoid", "has %d servers; at most %d are allowed", len(p.Avoid), MaxAvoid)
	}
	seen = map[string]bool{}
	for k, name := range p.Avoid {
		f := fmt.Sprintf("%s.placement.avoid[%d]", field, k)
		switch {
		case !mesh.ValidName(name) || mesh.ReservedName(name):
			return NewFieldError(f, "%q is not a server name (a lowercase DNS label, not \"new\" or \"joins\")", name)
		case seen[name]:
			return NewFieldError(f, "%q is listed twice", name)
		}
		seen[name] = true
	}
	return nil
}
