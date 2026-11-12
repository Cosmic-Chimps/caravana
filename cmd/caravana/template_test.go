package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/templates"
)

// writeSource makes a catalog source folder with one template, "web". compose
// is its compose.yaml.
func writeSource(t *testing.T, compose string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"catalog.json": `{"format": 1, "version": "2026.11.12"}`,
		"templates/web/template.json": `{
  "name": "web", "title": "Web", "version": "1.0.0-1", "description": "A web page.",
  "upstream": "https://example.com", "appName": "web",
  "inputs": [{"name": "domain", "label": "Domain", "type": "domain", "example": "web.example.com"}],
  "services": {"web": {"domains": [{"host": "{{domain}}", "port": 80}], "health": {"path": "/"}}}
}`,
		"templates/web/compose.yaml": compose,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const goodCompose = "services:\n  web:\n    image: nginx:1.27\n    expose: [\"80\"]\n"

func templateRun(args ...string) (int, string, string) {
	t0 := context.Background()
	var stdout, stderr bytes.Buffer
	code := run(t0, append([]string{"template"}, args...), func(string) string { return "" }, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestTemplateCheck(t *testing.T) {
	t.Run("a good catalog", func(t *testing.T) {
		code, stdout, stderr := templateRun("check", writeSource(t, goodCompose))
		if code != exitOK || !strings.Contains(stdout, "ok   web 1.0.0-1") || stderr != "" {
			t.Errorf("code %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	})
	t.Run("a warning fails it", func(t *testing.T) {
		compose := goodCompose + "    depends_on: [db]\n  db:\n    image: postgres:17\n"
		code, _, stderr := templateRun("check", writeSource(t, compose))
		if code != exitFailed || !strings.Contains(stderr, "FAIL web: services.web.depends_on") {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
	})
	t.Run("an unreadable source", func(t *testing.T) {
		code, _, stderr := templateRun("check", t.TempDir())
		if code != exitFailed || !strings.Contains(stderr, "catalog.json") {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
	})
	t.Run("an invalid manifest", func(t *testing.T) {
		code, _, stderr := templateRun("check", writeSource(t, "# {{nope}}\n"))
		if code != exitFailed || !strings.Contains(stderr, "web: compose:") {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
	})
}

func TestTemplateBundle(t *testing.T) {
	src := writeSource(t, goodCompose)
	out := filepath.Join(t.TempDir(), "out")
	code, stdout, stderr := templateRun("bundle", "--version", "2026.12.01", src, out)
	if code != exitOK || !strings.Contains(stdout, "1 templates") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	bundle, err := os.ReadFile(filepath.Join(out, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if err := templates.VerifySums(bundle, sums); err != nil {
		t.Errorf("the sums don't match the bundle: %v", err)
	}
	cat, err := templates.ParseCatalog(bundle)
	if err != nil || cat.Version != "2026.12.01" || len(cat.Templates) != 1 {
		t.Errorf("catalog = %+v, %v", cat, err)
	}

	t.Run("nothing is written for a failing catalog", func(t *testing.T) {
		bad := writeSource(t, goodCompose+"    depends_on: [db]\n  db:\n    image: postgres:17\n")
		out := filepath.Join(t.TempDir(), "out")
		code, _, stderr := templateRun("bundle", bad, out)
		if code != exitFailed || !strings.Contains(stderr, "nothing written") {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("the output folder was made")
		}
	})
}

func TestTemplateUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"check"}, {"check", "a", "b"}, {"bundle", "a"}, {"bundle", "--nope", "a", "b"}, {"frobnicate"}} {
		if code, _, stderr := templateRun(args...); code != exitUsage || !strings.Contains(stderr, "usage: caravana template") {
			t.Errorf("%v: code %d, stderr %q", args, code, stderr)
		}
	}
	if code, stdout, _ := templateRun("--help"); code != exitOK || !strings.Contains(stdout, "usage: caravana template") {
		t.Errorf("--help: code %d, stdout %q", code, stdout)
	}
	if _, stdout, _ := templateRun(); stdout != "" {
		t.Errorf("no arguments wrote to stdout: %q", stdout)
	}
}
