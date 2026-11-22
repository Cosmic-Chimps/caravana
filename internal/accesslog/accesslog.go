// Package accesslog reads one line of Traefik's access log (feature 047).
//
// Traefik is told to write one JSON object per request with only the fields the
// Requests page shows, no header and no query string (deploy/install/
// traefik.nomad.hcl). This package is pure: it turns a line into a Request,
// says which app a Traefik service name belongs to, and names a status class.
// It reads nothing and keeps nothing; the service reads Nomad's files.
//
// A line is Traefik's text about an app's request and may hold a path a person
// typed: nothing here writes one to a log or an error (constitution VII).
package accesslog

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// maxLine is the longest line Parse reads; a longer one is not an access log
// line Traefik wrote with the allowlisted fields.
const maxLine = 64 << 10

// MaxPath is the longest path a Request keeps: a longer one is cut here, where
// a character begins.
const MaxPath = 2 << 10

// nomadSuffix is the provider suffix Traefik adds to a router and service it
// made from Nomad's service tags. Only this one is removed: a name from another
// provider ("caravana@file") keeps its suffix, so it can never be mistaken for
// an app's.
const nomadSuffix = "@nomad"

// Request is one request Traefik served.
type Request struct {
	Time     time.Time     // StartUTC: when Traefik received the request
	Router   string        // the router's name without "@nomad"; "" when no router took the request
	Service  string        // Traefik's service name without "@nomad": "<app ID>-<service>"; "" likewise
	Method   string        // GET, POST, ...
	Host     string        // the Host header
	Path     string        // the path alone, never the query string; cut at MaxPath bytes
	Status   int           // the status the client got; 0 when the client went away first
	Duration time.Duration // from receiving the request to the last byte written
	Bytes    int64         // the bytes of the answer's body
	Client   string        // the TCP peer, or X-Forwarded-For when a trusted proxy sent it
	TLS      string        // "1.3"; "" for plain HTTP
	Retries  int           // how many times Traefik retried the request on another copy
	// EntryPoint is "websecure" or "web".
	EntryPoint string
}

// wire is the line as Traefik writes it. Pointers say which fields are there.
type wire struct {
	StartUTC              *string `json:"StartUTC"`
	RouterName            string  `json:"RouterName"`
	ServiceName           string  `json:"ServiceName"`
	RequestMethod         string  `json:"RequestMethod"`
	RequestHost           string  `json:"RequestHost"`
	RequestPath           string  `json:"RequestPath"`
	DownstreamStatus      *int    `json:"DownstreamStatus"`
	Duration              int64   `json:"Duration"` // nanoseconds
	DownstreamContentSize int64   `json:"DownstreamContentSize"`
	ClientHost            string  `json:"ClientHost"`
	TLSVersion            string  `json:"TLSVersion"`
	RetryAttempts         int     `json:"RetryAttempts"`
	EntryPointName        string  `json:"entryPointName"`
}

// Parse reads one line of Traefik's stdout. A line that isn't an access log
// line (Traefik's own log if it ever lands there, a line cut by a rotated file,
// junk) is not a request: false. A request has a StartUTC and a
// DownstreamStatus; any other field may be missing.
func Parse(line string) (Request, bool) {
	if len(line) < 2 || len(line) > maxLine || line[0] != '{' {
		return Request{}, false
	}
	var w wire
	if err := json.Unmarshal([]byte(line), &w); err != nil {
		return Request{}, false
	}
	if w.StartUTC == nil || w.DownstreamStatus == nil {
		return Request{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, *w.StartUTC)
	if err != nil {
		return Request{}, false
	}
	return Request{
		Time:       t.UTC(),
		Router:     strings.TrimSuffix(w.RouterName, nomadSuffix),
		Service:    strings.TrimSuffix(w.ServiceName, nomadSuffix),
		Method:     w.RequestMethod,
		Host:       w.RequestHost,
		Path:       cutPath(w.RequestPath),
		Status:     *w.DownstreamStatus,
		Duration:   time.Duration(w.Duration),
		Bytes:      w.DownstreamContentSize,
		Client:     w.ClientHost,
		TLS:        w.TLSVersion,
		Retries:    w.RetryAttempts,
		EntryPoint: w.EntryPointName,
	}, true
}

// ServiceOf is the service name of a line (without "@nomad") and nothing else
// of it: what the live follower needs to find the app that was asked, without
// holding the rest of the line. "" and false when the line is no request or no
// router took it.
func ServiceOf(line string) (string, bool) {
	if len(line) < 2 || len(line) > maxLine || line[0] != '{' {
		return "", false
	}
	var w struct {
		ServiceName string `json:"ServiceName"`
	}
	if err := json.Unmarshal([]byte(line), &w); err != nil || w.ServiceName == "" {
		return "", false
	}
	return strings.TrimSuffix(w.ServiceName, nomadSuffix), true
}

// cutPath cuts a path longer than MaxPath where a character begins.
func cutPath(p string) string {
	if len(p) <= MaxPath {
		return p
	}
	cut := MaxPath
	for cut > 0 && !utf8.RuneStart(p[cut]) {
		cut--
	}
	return p[:cut]
}

// The status classes.
const (
	Class1xx   = "1xx"
	Class2xx   = "2xx"
	Class3xx   = "3xx"
	Class4xx   = "4xx"
	Class5xx   = "5xx"
	ClassOther = "other" // a status outside 100-599: a 0 for a client that went away
)

// Classes is every class, in the order the page shows them.
var Classes = []string{Class1xx, Class2xx, Class3xx, Class4xx, Class5xx, ClassOther}

// Class is the class of a status: "2xx" for 200-299 and so on, "other" for
// anything outside 100-599.
func Class(status int) string {
	switch {
	case status >= 100 && status < 200:
		return Class1xx
	case status >= 200 && status < 300:
		return Class2xx
	case status >= 300 && status < 400:
		return Class3xx
	case status >= 400 && status < 500:
		return Class4xx
	case status >= 500 && status < 600:
		return Class5xx
	}
	return ClassOther
}

// ValidClass says whether s is one of Classes.
func ValidClass(s string) bool {
	for _, c := range Classes {
		if s == c {
			return true
		}
	}
	return false
}
