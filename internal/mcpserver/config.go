// Package mcpserver is the Model Context Protocol server behind `caravana mcp`
// (feature 026.5). It runs where the agent is, on the owner's machine, and
// turns each tool call into a call of Caravana's public API with the owner's
// token. It is an API client like any other: it must not import the service,
// store, deploy or Nomad packages (a test checks that).
package mcpserver

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Config is what `caravana mcp` needs to start.
type Config struct {
	// URL is the console's address, checked and without a trailing "/".
	URL string
	// Token is the API token. It is never logged, never put in a URL and never
	// shown in an answer.
	Token string
	// AllowActions also offers the tools that change things.
	AllowActions bool
	// Version is the binary's version, sent as the User-Agent and as the
	// server's version.
	Version string
}

// UsageError is a start-up mistake the owner can fix: a missing or bad
// setting. `caravana mcp` exits with status 2 for it.
type UsageError struct {
	Message string
}

func (e *UsageError) Error() string { return e.Message }

func usagef(format string, args ...any) error {
	return &UsageError{Message: fmt.Sprintf(format, args...)}
}

// maxTokenLength is a generous bound: Caravana's tokens are 4 + 43 characters.
const maxTokenLength = 256

// tokenRE is the alphabet of a Caravana token (cvat_ and base64url). A token
// with anything else is refused at start, so it can never break a header.
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ConfigFromEnv reads the settings from the environment (getenv) and the
// command line (args, after "mcp"). readFile reads CARAVANA_TOKEN_FILE. The
// token has no flag on purpose: a process list would show it. Every mistake
// the owner can fix is a *UsageError; flag.ErrHelp is returned for -h.
func ConfigFromEnv(getenv func(string) string, readFile func(string) ([]byte, error), args []string) (Config, error) {
	fs := flag.NewFlagSet("caravana mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the caller prints the usage, once
	allow := fs.Bool("allow-actions", false, "also offer the tools that change things")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, err
		}
		return Config{}, usagef("%v", err)
	}
	if fs.NArg() > 0 {
		return Config{}, usagef("unexpected argument %q", fs.Arg(0))
	}

	address, err := checkURL(getenv("CARAVANA_URL"))
	if err != nil {
		return Config{}, err
	}
	token, err := readToken(getenv, readFile)
	if err != nil {
		return Config{}, err
	}
	return Config{URL: address, Token: token, AllowActions: *allow}, nil
}

// checkURL validates CARAVANA_URL: https, or http for this machine only, since
// the token travels with every request. The error never echoes the value,
// which might hold a password someone put in the address.
func checkURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", usagef("CARAVANA_URL is required: the console's address, like https://caravana.example.com")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", usagef("CARAVANA_URL is not an address like https://caravana.example.com")
	}
	if u.User != nil {
		return "", usagef("CARAVANA_URL must not hold a user name or password; the token has its own setting")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", usagef("CARAVANA_URL must be the console's address only, without a query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", usagef("CARAVANA_URL uses http:// for a host that is not this machine, so the token would travel in clear: use https://")
		}
	default:
		return "", usagef("CARAVANA_URL must start with https:// (http:// is only for localhost)")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// isLoopback says whether host is this machine: localhost, 127.0.0.0/8 or ::1.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// readToken takes the token from CARAVANA_TOKEN or from the file named by
// CARAVANA_TOKEN_FILE (read once, surrounding whitespace dropped). Exactly one
// must be set.
func readToken(getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
	token := strings.TrimSpace(getenv("CARAVANA_TOKEN"))
	file := strings.TrimSpace(getenv("CARAVANA_TOKEN_FILE"))
	switch {
	case token != "" && file != "":
		return "", usagef("set only one of CARAVANA_TOKEN and CARAVANA_TOKEN_FILE")
	case token == "" && file == "":
		return "", usagef("CARAVANA_TOKEN or CARAVANA_TOKEN_FILE is required: make a token in the console's Settings › API tokens")
	}
	if file != "" {
		data, err := readFile(file)
		if err != nil {
			return "", usagef("CARAVANA_TOKEN_FILE: %v", err)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return "", usagef("CARAVANA_TOKEN_FILE: the file is empty")
		}
	}
	if len(token) > maxTokenLength || !tokenRE.MatchString(token) {
		// Say nothing about the value itself.
		return "", usagef("the token has characters or a length a Caravana token never has: check CARAVANA_TOKEN (or the file), it should be one cvat_… value")
	}
	return token, nil
}
