package app

import (
	"errors"
	"testing"
)

func TestPreviewNumber(t *testing.T) {
	tests := []struct {
		name string
		n    int
		ok   bool
	}{
		{"pr-1", 1, true},
		{"pr-12", 12, true},
		{"pr-999999999", 999999999, true},
		{"pr-1000000000", 0, false}, // ten digits
		{"pr-0", 0, false},
		{"pr-07", 0, false},
		{"pr-", 0, false},
		{"pr-12a", 0, false},
		{"pr--1", 0, false},
		{"pr-+1", 0, false},
		{"PR-12", 0, false},
		{"pr12", 0, false},
		{"staging", 0, false},
		{"", 0, false},
		{"xpr-12", 0, false},
	}
	for _, tt := range tests {
		n, ok := PreviewNumber(tt.name)
		if n != tt.n || ok != tt.ok {
			t.Errorf("PreviewNumber(%q) = %d, %v; want %d, %v", tt.name, n, ok, tt.n, tt.ok)
		}
		if IsPreviewEnvironment(tt.name) != tt.ok {
			t.Errorf("IsPreviewEnvironment(%q) = %v, want %v", tt.name, !tt.ok, tt.ok)
		}
	}
	if got := PreviewName(12); got != "pr-12" {
		t.Errorf("PreviewName(12) = %q", got)
	}
}

func TestValidEnvironmentName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"staging", false},
		{"pr-12", false},
		{"pr-12-old", true},
		{"pr-07", true},
		{"pr-", true},
		{"pr-x", true},
		{"pr", false},
		{"pre-1", false},
		{"Staging", true},   // not a label
		{"a--b", true},      // the ID separator
		{"my-pr-12", false}, // the prefix only counts at the start
	}
	for _, tt := range tests {
		err := ValidEnvironmentName("name", tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidEnvironmentName(%q) = %v, wantErr %v", tt.name, err, tt.wantErr)
			continue
		}
		var fe *FieldError
		if err != nil && (!errors.As(err, &fe) || fe.Field != "name") {
			t.Errorf("ValidEnvironmentName(%q) = %v, want a *FieldError on name", tt.name, err)
		}
	}
}
