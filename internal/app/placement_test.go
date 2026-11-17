package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidatePlacement(t *testing.T) {
	many := func(n int) map[string]string {
		m := map[string]string{}
		for i := range n {
			m[fmt.Sprintf("l%02d", i)] = "v"
		}
		return m
	}
	prefs := func(n int) []Preference {
		var p []Preference
		for i := range n {
			p = append(p, Preference{Label: fmt.Sprintf("l%02d", i), Value: "v"})
		}
		return p
	}
	names := func(n int) []string {
		var s []string
		for i := range n {
			s = append(s, fmt.Sprintf("srv%02d", i))
		}
		return s
	}

	tests := []struct {
		name      string
		placement *Placement
		wantField string // empty means valid
	}{
		{"none", nil, ""},
		{"every rule", &Placement{
			Require: map[string]string{"ssd": "true", "region": "eu-west_1:a/b.c"},
			Prefer:  []Preference{{Label: "ssd", Value: "true", Weight: 80}, {Label: "disk", Value: "nvme"}},
			Avoid:   []string{"three", "two"},
		}, ""},
		{"weight 100", &Placement{Prefer: []Preference{{Label: "a", Value: "b", Weight: 100}}}, ""},
		{"empty object", &Placement{}, "services[1].placement"},
		{"empty maps and lists", &Placement{Require: map[string]string{}, Prefer: []Preference{}, Avoid: []string{}}, "services[1].placement"},

		{"require bad name, uppercase", &Placement{Require: map[string]string{"SSD": "true"}}, "services[1].placement.require"},
		{"require bad name, digit first", &Placement{Require: map[string]string{"1ssd": "true"}}, "services[1].placement.require"},
		{"require name too long", &Placement{Require: map[string]string{strings.Repeat("a", 33): "true"}}, "services[1].placement.require"},
		{"require name 32 ok", &Placement{Require: map[string]string{strings.Repeat("a", 32): "true"}}, ""},
		{"require empty value", &Placement{Require: map[string]string{"ssd": ""}}, "services[1].placement.require.ssd"},
		{"require value with a space", &Placement{Require: map[string]string{"ssd": "a b"}}, "services[1].placement.require.ssd"},
		{"require value too long", &Placement{Require: map[string]string{"ssd": strings.Repeat("a", 65)}}, "services[1].placement.require.ssd"},
		{"require value 64 ok", &Placement{Require: map[string]string{"ssd": strings.Repeat("a", 64)}}, ""},
		{"require eight", &Placement{Require: many(8)}, ""},
		{"require nine", &Placement{Require: many(9)}, "services[1].placement.require"},

		{"prefer bad label", &Placement{Prefer: []Preference{{Label: "Ssd", Value: "true"}}}, "services[1].placement.prefer[0].label"},
		{"prefer bad value", &Placement{Prefer: []Preference{{Label: "ssd", Value: "tr ue"}}}, "services[1].placement.prefer[0].value"},
		{"prefer empty value", &Placement{Prefer: []Preference{{Label: "ssd"}}}, "services[1].placement.prefer[0].value"},
		{"prefer weight negative", &Placement{Prefer: []Preference{{Label: "ssd", Value: "true", Weight: -1}}}, "services[1].placement.prefer[0].weight"},
		{"prefer weight 101", &Placement{Prefer: []Preference{{Label: "a", Value: "b"}, {Label: "ssd", Value: "true", Weight: 101}}}, "services[1].placement.prefer[1].weight"},
		{"prefer same label twice", &Placement{Prefer: []Preference{{Label: "ssd", Value: "true"}, {Label: "ssd", Value: "false"}}}, "services[1].placement.prefer[1].label"},
		{"prefer eight", &Placement{Prefer: prefs(8)}, ""},
		{"prefer nine", &Placement{Prefer: prefs(9)}, "services[1].placement.prefer"},

		{"avoid not a name", &Placement{Avoid: []string{"two", "Three"}}, "services[1].placement.avoid[1]"},
		{"avoid empty", &Placement{Avoid: []string{""}}, "services[1].placement.avoid[0]"},
		{"avoid reserved new", &Placement{Avoid: []string{"new"}}, "services[1].placement.avoid[0]"},
		{"avoid reserved joins", &Placement{Avoid: []string{"joins"}}, "services[1].placement.avoid[0]"},
		{"avoid twice", &Placement{Avoid: []string{"two", "two"}}, "services[1].placement.avoid[1]"},
		{"avoid sixteen", &Placement{Avoid: names(16)}, ""},
		{"avoid seventeen", &Placement{Avoid: names(17)}, "services[1].placement.avoid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := validApp()
			a.Services[1].Placement = tc.placement
			err := ValidateApp(a)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("ValidateApp() = %v, want nil", err)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("ValidateApp() = %v, want a *FieldError for %q", err, tc.wantField)
			}
			if fe.Field != tc.wantField {
				t.Errorf("Field = %q, want %q (error: %v)", fe.Field, tc.wantField, err)
			}
			if fe.Message == "" {
				t.Errorf("Message is empty for %q", fe.Field)
			}
		})
	}
}

func TestCheckLabels(t *testing.T) {
	many := map[string]string{}
	for i := range MaxServerLabels + 1 {
		many[fmt.Sprintf("l%02d", i)] = "v"
	}
	tests := []struct {
		name      string
		labels    map[string]string
		wantField string
	}{
		{"none", nil, ""},
		{"two", map[string]string{"ssd": "true", "region": "eu"}, ""},
		{"bad name", map[string]string{"Ssd": "true"}, "labels"},
		{"bad value", map[string]string{"ssd": "no way"}, "labels.ssd"},
		{"too many", many, "labels"},
	}
	for _, tc := range tests {
		err := CheckLabels(tc.labels, "labels")
		if tc.wantField == "" {
			if err != nil {
				t.Errorf("%s: CheckLabels() = %v, want nil", tc.name, err)
			}
			continue
		}
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != tc.wantField {
			t.Errorf("%s: CheckLabels() = %v, want a *FieldError for %q", tc.name, err, tc.wantField)
		}
	}
}

func TestPreferenceWeightOrDefault(t *testing.T) {
	if got := (Preference{}).WeightOrDefault(); got != DefaultPreferenceWeight {
		t.Errorf("default = %d, want %d", got, DefaultPreferenceWeight)
	}
	if got := (Preference{Weight: 80}).WeightOrDefault(); got != 80 {
		t.Errorf("weight = %d, want 80", got)
	}
}

// A service without placement marshals exactly as before feature 042, so no
// stored app's revision moves; one with placement writes only what is set.
func TestPlacementJSONUnchanged(t *testing.T) {
	b, err := json.Marshal(Service{Name: "web", Image: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"name":"web","image":"x"}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
	b, err = json.Marshal(Service{Name: "web", Image: "x", Placement: &Placement{
		Require: map[string]string{"ssd": "true"},
		Prefer:  []Preference{{Label: "disk", Value: "nvme"}, {Label: "ssd", Value: "true", Weight: 80}},
		Avoid:   []string{"three"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"web","image":"x","placement":{"require":{"ssd":"true"},"prefer":[{"label":"disk","value":"nvme"},{"label":"ssd","value":"true","weight":80}],"avoid":["three"]}}`
	if got := string(b); got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}
