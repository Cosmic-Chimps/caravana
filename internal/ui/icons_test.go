package ui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestIcon(t *testing.T) {
	got := string(icon("house"))
	for _, want := range []string{`<svg `, `viewBox="0 0 24 24"`, `fill="none"`, `stroke="currentColor"`, `stroke-width="1.5"`,
		`stroke-linecap="round"`, `stroke-linejoin="round"`, `aria-hidden="true"`, `width="16" height="16"`, `</svg>`} {
		if !strings.Contains(got, want) {
			t.Errorf("icon(house) = %s, want %q in it", got, want)
		}
	}
	if got := string(icon("plus", 14)); !strings.Contains(got, `width="14" height="14"`) {
		t.Errorf("icon(plus, 14) = %s, want 14px", got)
	}
	if got := icon("no-such-icon"); got != "" {
		t.Errorf("an unknown icon drew %q, want nothing", got)
	}
	// Each icon is closed markup: as many openers as closers or self-closings.
	for name, shapes := range icons {
		if strings.Count(shapes, "<") != strings.Count(shapes, "/>") {
			t.Errorf("icon %q has shapes that don't self-close: %s", name, shapes)
		}
	}
}

// Every icon a template asks for exists, so a typo fails here instead of
// drawing a blank.
func TestTemplatesUseKnownIcons(t *testing.T) {
	use := regexp.MustCompile(`\{\{-?\s*icon "([^"]*)"`)
	found := 0
	err := fs.WalkDir(templatesFS, "templates", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(templatesFS, name)
		if err != nil {
			return err
		}
		for _, m := range use.FindAllStringSubmatch(string(b), -1) {
			found++
			if _, ok := icons[m[1]]; !ok {
				t.Errorf("%s uses the icon %q, which icons.go doesn't have", name, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Error("no template draws an icon: the pattern in this test no longer matches how they are written")
	}
}
