package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// AttentionKinds is what a test walks to check every kind: it must hold every constant of the type.
func TestAttentionKindsListsEveryKind(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "attention.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []AttentionKind
	for _, d := range file.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "AttentionKind" {
				continue
			}
			for _, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok {
					declared = append(declared, AttentionKind(strings.Trim(lit.Value, `"`)))
				}
			}
		}
	}
	if len(declared) < 14 {
		t.Fatalf("read %d kinds from attention.go, want at least 14", len(declared))
	}
	for _, k := range declared {
		if !slices.Contains(AttentionKinds, k) {
			t.Errorf("AttentionKinds misses %q", k)
		}
	}
	if len(declared) != len(AttentionKinds) {
		t.Errorf("AttentionKinds has %d kinds, attention.go declares %d", len(AttentionKinds), len(declared))
	}
}

// Every kind has words of its own: none falls through to the "needs you (kind)" fallback.
func TestEveryKindHasItsOwnWords(t *testing.T) {
	for _, k := range AttentionKinds {
		it := AttentionItem{Kind: k, AppID: "blog", AppName: "blog", Service: "web", Copies: []string{"c1"}, Mount: "/var/lib/docker",
			Domain: "bi.example.com", Schedule: "nightly", Detail: "x"}
		got := plainText(ProblemParts(it, func(s string) string { return s }))
		if strings.Contains(got, "needs you (") {
			t.Errorf("%s has no wording of its own: %q", k, got)
		}
	}
}

// A deploy that never reached Nomad says why, as far as it is known: the one wording of the needs-you
// list and of the app's summary.
func TestDeployFailedPartsSayTheCause(t *testing.T) {
	display := func(s string) string { return s }
	three := uint64(3)
	for _, tc := range []struct {
		name string
		it   AttentionItem
		want string
	}{
		{"image not found", AttentionItem{Kind: AttentionDeployFailed, AppID: "blog", DeploymentStatus: store.StatusFailed, Detail: "ghcr.io: x:1: image not found"},
			"blog: This deploy didn't start: the image wasn't found."},
		{"unknown cause", AttentionItem{Kind: AttentionDeployFailed, AppID: "blog", DeploymentStatus: store.StatusFailed, Detail: "something odd"},
			"blog: This deploy didn't start."},
		{"a version never says a cause", AttentionItem{Kind: AttentionDeployFailed, AppID: "blog", Version: &three, DeploymentStatus: store.StatusFailed, Detail: "image not found"},
			"blog: v3 failed during its rollout."},
	} {
		if got := plainText(ProblemParts(tc.it, display)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
