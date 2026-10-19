package app

import (
	"strings"
	"testing"
)

func TestOriginValidate(t *testing.T) {
	good := Origin{Commit: "abc1234", Ref: "refs/heads/main", Actor: "ana", Run: "https://github.com/o/r/actions/runs/1", Repository: "o/r"}
	tests := []struct {
		name      string
		edit      func(o *Origin)
		wantField string // "" means valid
	}{
		{"all set", func(o *Origin) {}, ""},
		{"nothing set", func(o *Origin) { *o = Origin{} }, ""},
		{"commit 6 digits", func(o *Origin) { o.Commit = "abc123" }, HeaderOriginCommit},
		{"commit 7 digits", func(o *Origin) { o.Commit = "abc1234" }, ""},
		{"commit 64 digits", func(o *Origin) { o.Commit = strings.Repeat("a", 64) }, ""},
		{"commit 65 digits", func(o *Origin) { o.Commit = strings.Repeat("a", 65) }, HeaderOriginCommit},
		{"commit not hex", func(o *Origin) { o.Commit = "abc123g" }, HeaderOriginCommit},
		{"ref 255", func(o *Origin) { o.Ref = strings.Repeat("r", 255) }, ""},
		{"ref 256", func(o *Origin) { o.Ref = strings.Repeat("r", 256) }, HeaderOriginRef},
		{"ref with a newline", func(o *Origin) { o.Ref = "main\nx" }, HeaderOriginRef},
		{"actor 101", func(o *Origin) { o.Actor = strings.Repeat("a", 101) }, HeaderOriginActor},
		{"actor with a tab", func(o *Origin) { o.Actor = "a\tb" }, HeaderOriginActor},
		{"actor not UTF-8", func(o *Origin) { o.Actor = "a\xffb" }, HeaderOriginActor},
		{"actor with accents", func(o *Origin) { o.Actor = "Zoë" }, ""},
		{"run over http", func(o *Origin) { o.Run = "http://github.com/o/r" }, HeaderOriginRun},
		{"run javascript", func(o *Origin) { o.Run = "javascript:alert(1)" }, HeaderOriginRun},
		{"run without host", func(o *Origin) { o.Run = "https://" }, HeaderOriginRun},
		{"run with a space", func(o *Origin) { o.Run = "https://github.com/a b" }, HeaderOriginRun},
		{"run 501", func(o *Origin) { o.Run = "https://x.io/" + strings.Repeat("a", 500) }, HeaderOriginRun},
		{"repository 201", func(o *Origin) { o.Repository = strings.Repeat("a", 201) }, HeaderOriginRepository},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := good
			tt.edit(&o)
			err := o.Validate()
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			fe, ok := err.(*FieldError)
			if !ok || fe.Field != tt.wantField {
				t.Fatalf("Validate() = %v, want a FieldError on %s", err, tt.wantField)
			}
		})
	}
}

func TestOriginHelpers(t *testing.T) {
	o := Origin{Commit: "ABC1234567", Ref: "refs/heads/feature/x"}.Normalized()
	if o.Commit != "abc1234567" || o.ShortCommit() != "abc1234" || o.BranchOrTag() != "feature/x" {
		t.Errorf("Normalized/ShortCommit/BranchOrTag = %+v %q %q", o, o.ShortCommit(), o.BranchOrTag())
	}
	if got := (Origin{Ref: "refs/tags/v1"}).BranchOrTag(); got != "v1" {
		t.Errorf("BranchOrTag(tag) = %q", got)
	}
	if !(Origin{}).IsZero() || o.IsZero() {
		t.Error("IsZero is wrong")
	}
}
