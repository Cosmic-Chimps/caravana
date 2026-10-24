package nomadjob

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func TestDomainRouterName(t *testing.T) {
	shape := regexp.MustCompile(`^shop-web-[0-9a-f]{8}$`)
	a := DomainRouterName("shop", "web", "shop.example.com")
	if !shape.MatchString(a) {
		t.Errorf("DomainRouterName() = %q, want shop-web- and 8 hex digits", a)
	}
	// Stable: the name is part of a Team-only password's middleware.
	if got := DomainRouterName("shop", "web", "shop.example.com"); got != a {
		t.Errorf("a second call gave %q, want %q", got, a)
	}
	// The hash part is of the host only; this is the one in the goldens.
	if a != "shop-web-951623a2" {
		t.Errorf("DomainRouterName() = %q, want shop-web-951623a2 (the first 8 hex of SHA-256 of the host)", a)
	}
	if b := DomainRouterName("shop", "web", "other.example.com"); b == a {
		t.Errorf("two hosts gave the same router %q", a)
	}
	// Another service or app keeps its own prefix with the same hash part.
	if got := DomainRouterName("blog", "api", "shop.example.com"); got != "blog-api-951623a2" {
		t.Errorf("DomainRouterName(blog, api) = %q", got)
	}
	if got := TeamAuthMiddleware(a); got != a+"-auth" {
		t.Errorf("TeamAuthMiddleware() = %q", got)
	}
}

// tagsOf translates one service with the given domains and returns the tags of
// its registration.
func tagsOf(t *testing.T, s Settings, domains ...app.Domain) []string {
	t.Helper()
	a := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{{
			Name:    "web",
			Image:   "traefik/whoami:v1.10",
			Ports:   []app.Port{{Name: "http", ContainerPort: 80}},
			Domains: domains,
			Health:  &app.HealthCheck{Path: "/health"},
		}},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"web": "traefik/whoami@sha256:" + strings.Repeat("a", 64),
	}}
	res, err := Translate(a, dep, s, nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	return res.Job.TaskGroups[0].Services[0].Tags
}

func TestRouterTagsOptions(t *testing.T) {
	r := DomainRouterName("shop", "web", "example.com")

	t.Run("every option on one domain: auth first, then the redirect", func(t *testing.T) {
		tags := tagsOf(t, labSettings(), app.Domain{Host: "example.com", Port: "http", Path: "/app", WWWRedirect: true, TeamOnly: true})
		for _, want := range []string{
			"traefik.http.routers." + r + ".rule=Host(`example.com`) && PathPrefix(`/app`) || Host(`www.example.com`)",
			"traefik.http.routers." + r + ".middlewares=" + r + "-auth@file," + r + "-www",
			"traefik.http.middlewares." + r + "-www.redirectregex.regex=^https://www[.]example[.]com(:[0-9]+)?/(.*)",
			"traefik.http.middlewares." + r + "-www.redirectregex.replacement=https://example.com$1/$2",
			"traefik.http.middlewares." + r + "-www.redirectregex.permanent=true",
		} {
			if !slices.Contains(tags, want) {
				t.Errorf("tags lack %q:\n%s", want, strings.Join(tags, "\n"))
			}
		}
	})

	t.Run("an apex target of a www host", func(t *testing.T) {
		w := DomainRouterName("shop", "web", "www.example.com")
		tags := tagsOf(t, labSettings(), app.Domain{Host: "www.example.com", Port: "http", WWWRedirect: true})
		for _, want := range []string{
			"traefik.http.routers." + w + ".rule=Host(`www.example.com`) || Host(`example.com`)",
			"traefik.http.middlewares." + w + "-www.redirectregex.regex=^https://example[.]com(:[0-9]+)?/(.*)",
			"traefik.http.middlewares." + w + "-www.redirectregex.replacement=https://www.example.com$1/$2",
		} {
			if !slices.Contains(tags, want) {
				t.Errorf("tags lack %q:\n%s", want, strings.Join(tags, "\n"))
			}
		}
	})

	t.Run("a path alone has no middlewares tag", func(t *testing.T) {
		tags := tagsOf(t, labSettings(), app.Domain{Host: "example.com", Port: "http", Path: "/api"})
		for _, tag := range tags {
			if strings.Contains(tag, "middlewares") {
				t.Errorf("unexpected tag %q", tag)
			}
		}
	})

	t.Run("no base router when every domain has options", func(t *testing.T) {
		tags := tagsOf(t, labSettings(), app.Domain{Host: "example.com", Port: "http", TeamOnly: true})
		for _, tag := range tags {
			if strings.HasPrefix(tag, "traefik.http.routers.shop-web.") {
				t.Errorf("the base router has a tag %q", tag)
			}
		}
		if tags[0] != "traefik.enable=true" {
			t.Errorf("tags[0] = %q, want traefik.enable=true", tags[0])
		}
	})

	t.Run("the base router keeps only the plain hosts", func(t *testing.T) {
		tags := tagsOf(t, labSettings(),
			app.Domain{Host: "a.example.com", Port: "http"},
			app.Domain{Host: "example.com", Port: "http", Path: "/api"},
			app.Domain{Host: "b.example.com", Port: "http"})
		if !slices.Contains(tags, "traefik.http.routers.shop-web.rule=Host(`a.example.com`) || Host(`b.example.com`)") {
			t.Errorf("base router rule wrong:\n%s", strings.Join(tags, "\n"))
		}
	})

	t.Run("the certificate resolver reaches every router", func(t *testing.T) {
		tags := tagsOf(t, letsEncryptSettings(),
			app.Domain{Host: "a.example.com", Port: "http"},
			app.Domain{Host: "example.com", Port: "http", Path: "/api"})
		for _, router := range []string{"shop-web", r} {
			want := "traefik.http.routers." + router + ".tls.certresolver=letsencrypt"
			if !slices.Contains(tags, want) {
				t.Errorf("tags lack %q", want)
			}
		}
	})

	t.Run("no tag holds a Nomad interpolation", func(t *testing.T) {
		tags := tagsOf(t, labSettings(), app.Domain{Host: "example.com", Port: "http", Path: "/x", WWWRedirect: true, TeamOnly: true})
		for _, tag := range tags {
			if strings.Contains(tag, "${") {
				t.Errorf("tag %q has ${, which Nomad would interpolate", tag)
			}
		}
	})
}
