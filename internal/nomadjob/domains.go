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
// middleware "<router>-auth@file". Every router of a service ends with the
// service's retry middleware, RetryMiddleware.

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

// RetryMiddleware is the retry middleware of the service whose base router is
// router: "<router>-retry". Every router of the service uses it, last.
//
// Nomad registers a copy's service before its task starts and its
// registrations carry no health, so Traefik's Nomad provider routes to a copy
// that a scale, a move or a reschedule placed (none of them hides it behind
// canary tags, as a deploy does) up to a few seconds before it listens, and
// each request it gets meanwhile is a 502 (lab, observed: the registration
// came 0.7 to 2.6 s before the task's Started event). With the middleware, a
// request whose connection to a copy fails goes to the next copy instead.
// Traefik retries only when nothing of the request reached the copy (a refused
// or failed dial), so a POST is never sent twice; any answer the copy gives,
// 5xx included, goes to the visitor as it is.
func RetryMiddleware(router string) string {
	return router + "-retry"
}

// retryAttempts is how many copies a request may try: with no wait between
// tries, Traefik's round robin gives each try the next copy, so a request
// reaches a listening copy even when a step placed two new ones at once.
const retryAttempts = 3

// retryTags defines the retry middleware named mw.
func retryTags(mw string) []string {
	return []string{fmt.Sprintf("traefik.http.middlewares.%s.retry.attempts=%d", mw, retryAttempts)}
}

// routerTags builds the Traefik routers of a service, with TLS always on, and
// its load balancer's health check (healthCheckTags).
// Without a certificate resolver, Traefik serves its default certificate (the
// local lab); with one, it gets a real certificate. The base router (named
// router) takes every domain without options in one rule; each domain with
// options has its own router. When every domain has options the base router is
// not written at all: a router with an empty rule would never match.
func routerTags(appID, router string, svc app.Service, s Settings) []string {
	domains := svc.Domains
	tags := []string{"traefik.enable=true"}
	retry := RetryMiddleware(router)

	var plain []string
	for _, d := range domains {
		if !d.HasOptions() {
			plain = append(plain, fmt.Sprintf("Host(`%s`)", d.Host))
		}
	}
	if len(plain) > 0 {
		tags = append(tags, routerBase(router, strings.Join(plain, " || "), s)...)
		tags = append(tags, routerMiddlewares(router, []string{retry}))
	}

	for _, d := range domains {
		if d.HasOptions() {
			tags = append(tags, optionRouterTags(DomainRouterName(appID, svc.Name, d.Host), d, retry, s)...)
		}
	}
	tags = append(tags, retryTags(retry)...)
	return append(tags, healthCheckTags(router, svc)...)
}

// routerMiddlewares is the tag that puts middlewares, in order, on a router.
func routerMiddlewares(router string, middlewares []string) string {
	return "traefik.http.routers." + router + ".middlewares=" + strings.Join(middlewares, ",")
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
//     everything, so nothing is redirected before the password is asked;
//   - the service's retry middleware, retry, always comes last.
//
// The redirect's regex writes dots as [.] and its replacement uses $1 and $2,
// never ${1}: Nomad interpolates ${...} in tags. Traefik matches the regex
// against the URL built from the Host header, which carries the port when the
// visitor's URL has one (the lab's :8443, or a client sending :443); the regex
// accepts an optional port and the replacement keeps it, or the redirect
// would silently not happen and the pair would be served as the app (lab,
// observed 2026-10-24). Traefik answers 301 to a GET and 308 to anything else.
//
// In the rule, && binds tighter than ||, so with a path the pair host is
// routed on every path and redirected to the same path on this host.
func optionRouterTags(router string, d app.Domain, retry string, s Settings) []string {
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
	tags = append(tags, routerMiddlewares(router, append(middlewares, retry)))
	if pair != "" {
		mw := "traefik.http.middlewares." + router + "-www.redirectregex."
		tags = append(tags,
			mw+"regex=^https://"+strings.ReplaceAll(pair, ".", "[.]")+"(:[0-9]+)?/(.*)",
			mw+"replacement=https://"+d.Host+"$1/$2",
			mw+"permanent=true",
		)
	}
	return tags
}
