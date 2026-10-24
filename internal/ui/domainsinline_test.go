package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// The critique's ruling 8: the confirmations open inline, the banner stacks, the locked switch is
// drawn in fg-3, and a redirect row sorts under the host it redirects to.

func TestRedirectsUnderTargets(t *testing.T) {
	v := func(host, of string) service.DomainView { return service.DomainView{Host: host, RedirectOf: of} }
	tests := []struct {
		name string
		in   []service.DomainView
		want string
	}{
		{"apex under its www", []service.DomainView{v("a.example.com", ""), v("example.com", "www.example.com"), v("www.example.com", "")},
			"a.example.com www.example.com example.com"},
		{"www under its apex", []service.DomainView{v("b.example.com", ""), v("example.com", ""), v("www.example.com", "example.com")},
			"b.example.com example.com www.example.com"},
		{"a problem target keeps its place at the top", []service.DomainView{v("www.shop.com", ""), v("a.example.com", ""), v("shop.com", "www.shop.com")},
			"www.shop.com shop.com a.example.com"},
		{"a target not in the list", []service.DomainView{v("example.com", "www.example.com"), v("z.example.com", "")},
			"example.com z.example.com"},
		{"no redirects", []service.DomainView{v("b", ""), v("a", "")}, "b a"},
	}
	for _, tc := range tests {
		var got []string
		for _, r := range redirectsUnderTargets(tc.in) {
			got = append(got, r.Host)
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, got, tc.want)
		}
	}
}

func TestDomainsConfirmationsInline(t *testing.T) {
	e := newUIEnv(t, true, "")
	_, css := e.do(t, request{path: "/static/app.css"})
	flat := strings.Join(strings.Fields(css), " ")
	// Remove…, New password and Settings' Edit open in the flow: no absolute body, no overlay shadow.
	for _, sel := range []string{`\.cv-dmbtns \.cv-confirm-body`, `\.cv-certedit \.cv-confirm-body`} {
		rule := regexp.MustCompile(sel + `\{[^}]*\}`).FindString(flat)
		if rule == "" {
			t.Errorf("no rule for %s", sel)
			continue
		}
		if strings.Contains(rule, "absolute") || strings.Contains(rule, "shadow") {
			t.Errorf("%s still overlays: %s", sel, rule)
		}
	}
	if !strings.Contains(flat, ".cv-dmbtns>.cv-confirm[open]{flex-basis:100%") {
		t.Error("an open confirmation doesn't take its line")
	}
	// The locked switch is fg-3, knob and ground; the real switch's on hue is still healthy.
	for _, want := range []string{".cv-switch:disabled:checked{background:", ".cv-switch:disabled:checked:before{background:var(--color-fg-3)",
		".cv-switch:checked{background:var(--color-healthy-bg);border-color:var(--color-healthy-line)"} {
		if !strings.Contains(strings.ReplaceAll(flat, "::before", ":before"), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	// The banner stacks below 640px: Check again under the text.
	if !strings.Contains(flat, ".cv-dmbanner>.cv-dmcheckform{grid-column:2") {
		t.Error("the banner doesn't stack its button below 640px")
	}
}
