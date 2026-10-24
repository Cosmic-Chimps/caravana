package nomadjob

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Routers and domain options (feature 023). A service has one Traefik router,
// "<app>-<service>", for all of its domains without options, exactly as
// before. A domain with an option (a path, a www redirect, Team only) gets a
// router of its own on the same Nomad registration, named by DomainRouterName,
// with the middlewares its options need. Every router here is only tags: no
// secret is in them. The password of a Team-only domain lives in the Nomad
// Variable nomad/jobs/traefik, which Traefik's file provider renders as the
// middleware "<router>-auth@file".

// DomainRouterName is the Traefik router of the domain host of a service:
// "<app>-<service>-" and the first 8 hex digits of the SHA-256 of the host. A
// hash, not the host, because a router name sits inside dot-separated tag keys
// where a host's dots can't go. It is stable: the same host always gives the
// same name, so a Team-only password's middleware keeps its name across
// deploys.
func DomainRouterName(appID, service, host string) string {
	sum := sha256.Sum256([]byte(host))
	return appID + "-" + service + "-" + hex.EncodeToString(sum[:4])
}

// TeamAuthMiddleware is the file-provider middleware that guards a Team-only
// router: "<router>-auth". It is also the key of its item in the variable
// nomad/jobs/traefik.
func TeamAuthMiddleware(router string) string {
	return router + "-auth"
}

// routerTags builds the Traefik routers of a service, with TLS always on.
// Without a certificate resolver, Traefik serves its default certificate (the
// local lab); with one, it gets a real certificate. The base router (named
// router) takes every domain without options in one rule; each domain with
// options has its own router. When every domain has options the base router is
// not written at all: a router with an empty rule would never match.
func routerTags(appID, service, router string, domains []app.Domain, s Settings) []string {
	tags := []string{"traefik.enable=true"}

	var plain []string
	for _, d := range domains {
		if !d.HasOptions() {
			plain = append(plain, fmt.Sprintf("Host(`%s`)", d.Host))
		}
	}
	if len(plain) > 0 {
		tags = append(tags, routerBase(router, strings.Join(plain, " || "), s)...)
	}

	for _, d := range domains {
		if d.HasOptions() {
			tags = append(tags, optionRouterTags(DomainRouterName(appID, service, d.Host), d, s)...)
		}
	}
	return tags
}

// routerBase is a router's rule, entrypoint and TLS tags.
func routerBase(router, rule string, s Settings) []string {
	prefix := "traefik.http.routers." + router
	tags := []string{
		prefix + ".rule=" + rule,
		prefix + ".entrypoints=" + s.Entrypoint,
		prefix + ".tls=true",
	}
	if s.CertResolver != "" {
		tags = append(tags, prefix+".tls.certresolver="+s.CertResolver)
	}
	return tags
}

// optionRouterTags is the router of one domain with options, named router:
//
//   - a path adds && PathPrefix to the rule, and the prefix
//     is kept for the app (nothing is stripped);
//   - a www redirect adds the pair host to the rule, so one certificate covers
//     both names, and a redirect middleware "<router>-www" sends the pair to
//     this host;
//   - Team only puts the file middleware "<router>-auth@file" in front of
//     everything, so nothing is redirected before the password is asked.
//
// The redirect's regex writes dots as [.] and its replacement uses $1, never
// ${1}: Nomad interpolates ${...} in tags.
func optionRouterTags(router string, d app.Domain, s Settings) []string {
	rule := fmt.Sprintf("Host(`%s`)", d.Host)
	if d.Path != "" {
		rule += fmt.Sprintf(" && PathPrefix(`%s`)", d.Path)
	}
	pair := ""
	if d.WWWRedirect {
		pair = d.RedirectPair()
	}
	if pair != "" {
		rule += fmt.Sprintf(" || Host(`%s`)", pair)
	}
	tags := routerBase(router, rule, s)

	var middlewares []string
	if d.TeamOnly {
		middlewares = append(middlewares, TeamAuthMiddleware(router)+"@file")
	}
	if pair != "" {
		middlewares = append(middlewares, router+"-www")
	}
	if len(middlewares) > 0 {
		tags = append(tags, "traefik.http.routers."+router+".middlewares="+strings.Join(middlewares, ","))
	}
	if pair != "" {
		mw := "traefik.http.middlewares." + router + "-www.redirectregex."
		tags = append(tags,
			mw+"regex=^https://"+strings.ReplaceAll(pair, ".", "[.]")+"/(.*)",
			mw+"replacement=https://"+d.Host+"/$1",
			mw+"permanent=true",
		)
	}
	return tags
}
