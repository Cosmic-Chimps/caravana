package mesh

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The package is pure: no files, no programs, no network (feature 031, node.md §1).
func TestImports(t *testing.T) {
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "testdata" {
				return filepath.SkipDir
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
			if path == "net/http" || path == "os" || path == "os/exec" || path == "net" {
				t.Errorf("%s imports %s", p, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
