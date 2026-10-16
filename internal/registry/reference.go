// Package registry pins container images: it turns an image reference with
// a tag ("traefik/whoami:latest") into one pinned by digest
// ("traefik/whoami@sha256:…"), so every deploy runs exactly the image it
// resolved. It speaks the registry v2 API to any registry: anonymously, or
// with the credentials a Client is given for a private one.
package registry

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Reference is a parsed image reference.
type Reference struct {
	// Name is the image as written, without tag or digest: "traefik/whoami",
	// "nginx", "ghcr.io/acme/shop", "registry.example.com:5000/acme/shop".
	// The pinned image keeps it, host and port included. A host is written in
	// lowercase: that is how Docker matches it to the credentials for it.
	Name string
	// Registry is the canonical registry host, lowercased, with its port when
	// it has one: "docker.io", "ghcr.io", "registry.example.com:5000".
	// "index.docker.io" is "docker.io".
	Registry string
	// Repository is the path on that registry: "traefik/whoami",
	// "library/nginx", "acme/shop".
	Repository string
	// Tag is the tag to resolve; "latest" when none was given. Empty when
	// Digest is set.
	Tag string
	// Digest is "sha256:<64 hex>" when the reference is already pinned.
	Digest string
	// IgnoredTag is the tag when both a tag and a digest were given: the
	// digest wins, and the deploy warns that the tag has no effect.
	IgnoredTag string
}

var (
	// pathRE is a repository path: lowercase components separated by "/".
	pathRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRE  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	// digestRE is the only digest form Caravana pins: sha256.
	digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	// hostRE is a registry host in lowercase: a DNS name or a bracketed IPv6
	// address, then an optional port.
	hostRE = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*|\[[0-9a-f:.]+\])(?::[0-9]{1,5})?$`)
)

// Parse parses an image reference: [host[:port]/]path[:tag][@sha256:<64 hex>].
// Docker Hub is the default. A first part of the path is a registry host when
// it has a "." or a ":", or is "localhost", as Docker reads it. Errors
// describe the problem without naming the field, so callers can put them
// under the field they came from.
func Parse(image string) (Reference, error) {
	if image == "" {
		return Reference{}, errors.New("is required")
	}

	rest := image
	var digest, tag string
	if name, d, ok := strings.Cut(rest, "@"); ok {
		if !digestRE.MatchString(d) {
			return Reference{}, fmt.Errorf("%q: the digest must be sha256:<64 lowercase hex>", image)
		}
		rest, digest = name, d
	}
	// A ':' after the last '/' starts the tag; one before it is a host's port.
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		rest, tag = rest[:i], rest[i+1:]
		if !tagRE.MatchString(tag) {
			return Reference{}, fmt.Errorf("%q: %q is not a valid tag", image, tag)
		}
	}

	name := rest
	host, path := "", rest
	if first, after, ok := strings.Cut(rest, "/"); ok && (strings.ContainsAny(first, ".:") || strings.EqualFold(first, "localhost")) {
		host, path = strings.ToLower(first), after
		if !hostRE.MatchString(host) {
			return Reference{}, fmt.Errorf("%q is not a valid registry host (a name or an address, then an optional :port)", first)
		}
		name = host + "/" + path
	}
	registry := host
	if host == "" || host == "index.docker.io" {
		registry = "docker.io"
	}
	if !pathRE.MatchString(path) {
		return Reference{}, fmt.Errorf("%q is not a valid image name (lowercase letters, digits, '.', '_' and '-', in parts separated by '/')", name)
	}
	// Docker Hub's official images live under library/.
	if registry == "docker.io" && !strings.Contains(path, "/") {
		path = "library/" + path
	}

	ref := Reference{Name: name, Registry: registry, Repository: path, Tag: tag, Digest: digest}
	switch {
	case digest != "":
		ref.Tag, ref.IgnoredTag = "", tag
	case tag == "":
		ref.Tag = "latest"
	}
	return ref, nil
}

// Endpoint returns the base URL of host's registry API. Docker Hub's API is
// not on docker.io itself. A loopback host speaks plain HTTP, because
// Docker's daemon treats those as insecure registries by default, so the
// node pulls the way Caravana looks the digest up. Every other host must
// serve HTTPS: credentials never cross the network in the clear.
func Endpoint(host string) string {
	switch {
	case host == "docker.io":
		return "https://registry-1.docker.io"
	case isLoopback(host):
		return "http://" + host
	}
	return "https://" + host
}

// isLoopback reports whether host, with or without a port, is localhost or a
// loopback address (127.0.0.0/8, ::1).
func isLoopback(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.Trim(name, "[]")
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// Pinned returns the image pinned to digest: Name + "@" + digest.
func (r Reference) Pinned(digest string) string {
	return r.Name + "@" + digest
}

// String is the reference as resolved: Name:Tag, or Name@Digest.
func (r Reference) String() string {
	if r.Digest != "" {
		return r.Pinned(r.Digest)
	}
	return r.Name + ":" + r.Tag
}
