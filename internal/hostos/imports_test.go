package hostos_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The package is pure: it reads through an fs.FS. Only real.go may touch the
// operating system, and nothing in it runs a program or opens the network
// (constitution I and VI).
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
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || p == "real.go" {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case path == "net/http", path == "os/exec", path == "net":
				t.Errorf("%s imports %s: only real.go may touch the host", p, path)
			case strings.Contains(path, "nomad"),
				strings.HasSuffix(path, "/internal/service"),
				strings.HasSuffix(path, "/internal/store"),
				strings.HasSuffix(path, "/internal/deploy"):
				t.Errorf("%s imports %s", p, path)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" {
				switch sel.Sel.Name {
				case "Open", "OpenFile", "ReadFile", "ReadDir", "Stat", "Create":
					t.Errorf("%s calls os.%s: read through the fs.FS", p, sel.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
