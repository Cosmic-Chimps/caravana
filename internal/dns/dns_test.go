package dns

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The package is pure of Caravana (feature 048, D1): it imports none of the
// module's packages, only the standard library.
func TestImportsNothingOfCaravana(t *testing.T) {
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." {
				return filepath.SkipDir // dnstest has its own role
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(path, "caravana") {
				t.Errorf("%s imports %s", p, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestZoneOf(t *testing.T) {
	zones := []string{"example.com", "dev.example.com", "other.org"}
	tests := []struct {
		host string
		want string
		ok   bool
	}{
		{"example.com", "example.com", true},
		{"web.example.com", "example.com", true},
		{"WEB.Example.com.", "example.com", true},
		{"a.b.dev.example.com", "dev.example.com", true},
		{"dev.example.com", "dev.example.com", true},
		{"notexample.com", "", false},
		{"example.com.evil.org", "", false},
		{"nothing.net", "", false},
	}
	for _, tc := range tests {
		got, ok := ZoneOf(tc.host, zones)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ZoneOf(%q) = %q, %v; want %q, %v", tc.host, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := ZoneOf("a.com", nil); ok {
		t.Error("ZoneOf with no zones = true")
	}
}
