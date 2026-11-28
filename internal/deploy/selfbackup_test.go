package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/selfbackup"
)

func TestExportVariablesKeepsOnlyWhatTheRuleSays(t *testing.T) {
	d, _, n := newTestDeployer(t)
	ctx := context.Background()
	for path, items := range map[string]map[string]string{
		"caravana/acme":                     {"email": "a@b.c"},
		"caravana/registries/ghcr~io":       {"username": "u", "password": "p"},
		"caravana/cluster/ca":               {"key": "cluster-secret"},
		"nomad/jobs/web/web":                {"PW": "s3cret"},
		"nomad/jobs/web/web/web":            {"CARAVANA_REGISTRY_PASSWORD": "x"},
		"nomad/jobs/web/caravana/previous":  {"PW": "old"},
		"nomad/jobs/web_run-1":              {"PW": "run"},
		"nomad/jobs/traefik/traefik":        {"hosts": "x"},
		"nomad/jobs/shop--staging--api/api": {"K": "v"},
		"other/thing":                       {"a": "b"},
	} {
		n.SetVariable(path, items)
	}
	got, err := d.ExportVariables(ctx, selfbackup.Keep)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, v := range got {
		paths = append(paths, v.Path)
	}
	want := "caravana/acme,caravana/registries/ghcr~io,nomad/jobs/shop--staging--api/api,nomad/jobs/web/web"
	if strings.Join(paths, ",") != want {
		t.Errorf("paths = %v, want %s", paths, want)
	}
	if got[3].Items["PW"] != "s3cret" || got[1].Items["password"] != "p" {
		t.Errorf("items = %+v", got)
	}
}

func TestImportVariablesWritesAndRefuses(t *testing.T) {
	d, _, n := newTestDeployer(t)
	ctx := context.Background()
	n.SetVariable("nomad/jobs/web/web", map[string]string{"OLD": "gone"})
	vars := []selfbackup.Variable{
		{Path: "caravana/acme", Items: map[string]string{"email": "a@b.c"}},
		{Path: "nomad/jobs/web/web", Items: map[string]string{"PW": "s3cret"}},
	}
	if c, err := d.ImportVariables(ctx, vars); err != nil || c != 2 {
		t.Fatalf("ImportVariables = %d, %v", c, err)
	}
	if got := n.Variable("nomad/jobs/web/web"); len(got) != 1 || got["PW"] != "s3cret" {
		t.Errorf("the variable is %v: the import must replace it", got)
	}
	// A bad entry stops everything before anything is written.
	bad := []selfbackup.Variable{
		{Path: "caravana/other", Items: map[string]string{"k": "v"}},
		{Path: "nomad/jobs/traefik/traefik", Items: map[string]string{"k": "v"}},
	}
	if c, err := d.ImportVariables(ctx, bad); err == nil || c != 0 {
		t.Errorf("a path outside the rule: %d, %v", c, err)
	}
	if n.Variable("caravana/other") != nil {
		t.Error("the entry before the bad one was written")
	}
	if _, err := d.ImportVariables(ctx, []selfbackup.Variable{{Path: "caravana/x"}}); err == nil {
		t.Error("a variable with no items was accepted")
	}
}
