package metrics_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The package is pure: only real.go reaches the network, and nothing here
// imports Nomad, the service layer or the store (constitution VI and II).
func TestImports(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		isTest := strings.HasSuffix(path, "_test.go")
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case p == "net/http" && path != "real.go" && !isTest:
				t.Errorf("%s imports net/http: only real.go may", path)
			case strings.Contains(p, "nomad"),
				strings.HasSuffix(p, "/internal/service"),
				strings.HasSuffix(p, "/internal/store"),
				strings.HasSuffix(p, "/internal/deploy"):
				t.Errorf("%s imports %s", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("real.go"); err != nil {
		t.Fatal(err)
	}
}
