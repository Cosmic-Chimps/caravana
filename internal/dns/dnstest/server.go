// Package dnstest has the fakes of package dns: Server, a fake Cloudflare over
// httptest for the client's own tests and the lab, and Provider, an
// in-memory dns.Provider for the service's tests.
package dnstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/dns"
)

// Server is a fake Cloudflare: zones and records in memory, a bearer token to
// check, a counter of the calls it got.
type Server struct {
	*httptest.Server

	mu      sync.Mutex
	token   string
	zones   map[string]string // name → ID
	records map[string][]dns.Record
	calls   []string // "METHOD path"
	next    int
	status  int // when not 0, every call answers it (a broken provider)
}

// NewServer starts a fake that accepts only token, and stops it when t ends.
func NewServer(t testing.TB, token string) *Server {
	t.Helper()
	s := &Server{token: token, zones: map[string]string{}, records: map[string][]dns.Record{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// AddZone makes a zone and returns its ID.
func (s *Server) AddZone(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := fmt.Sprintf("zone%d", s.next)
	s.zones[name] = id
	return id
}

// Seed adds a hand-made record to a zone and returns it with its ID.
func (s *Server) Seed(zone string, r dns.Record) dns.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	r.ID = fmt.Sprintf("rec%d", s.next)
	if r.TTL == 0 {
		r.TTL = 1
	}
	id := s.zones[zone]
	s.records[id] = append(s.records[id], r)
	return r
}

// Records returns the zone's records now.
func (s *Server) Records(zone string) []dns.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records[s.zones[zone]])
}

// SetToken changes the token the fake accepts.
func (s *Server) SetToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

// SetStatus makes every call answer this HTTP status (0 restores normal).
func (s *Server) SetStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
}

// Calls is how many calls the fake got, and Log says which ("GET /zones").
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// Log lists the calls got, oldest first.
func (s *Server) Log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// ResetCalls forgets the calls counted so far.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

func reply(w http.ResponseWriter, code int, result any, info map[string]int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	out := map[string]any{"success": code < 400, "result": result}
	if info != nil {
		out["result_info"] = info
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
	if s.status != 0 {
		reply(w, s.status, nil, nil)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		reply(w, http.StatusForbidden, nil, nil)
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	switch {
	case path == "zones" && r.Method == http.MethodGet:
		name := r.URL.Query().Get("name")
		found := []map[string]string{}
		for z, id := range s.zones {
			if name == "" || z == name {
				found = append(found, map[string]string{"id": id, "name": z})
			}
		}
		reply(w, http.StatusOK, found, nil)
	case len(parts) == 3 && parts[0] == "zones" && parts[2] == "dns_records":
		s.records3(w, r, parts[1])
	case len(parts) == 4 && parts[0] == "zones" && parts[2] == "dns_records":
		s.record4(w, r, parts[1], parts[3])
	default:
		reply(w, http.StatusNotFound, nil, nil)
	}
}

func (s *Server) zoneKnown(id string) bool {
	for _, z := range s.zones {
		if z == id {
			return true
		}
	}
	return false
}

func (s *Server) records3(w http.ResponseWriter, r *http.Request, zoneID string) {
	if !s.zoneKnown(zoneID) {
		reply(w, http.StatusNotFound, nil, nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		perPage, _ := strconv.Atoi(q.Get("per_page"))
		page, _ := strconv.Atoi(q.Get("page"))
		if perPage <= 0 {
			perPage = 100
		}
		if page <= 0 {
			page = 1
		}
		var match []dns.Record
		for _, rec := range s.records[zoneID] {
			if name := q.Get("name"); name == "" || rec.Name == name {
				match = append(match, rec)
			}
		}
		total := (len(match) + perPage - 1) / perPage
		lo := min((page-1)*perPage, len(match))
		hi := min(lo+perPage, len(match))
		out := []map[string]any{}
		for _, rec := range match[lo:hi] {
			out = append(out, recJSON(rec))
		}
		reply(w, http.StatusOK, out, map[string]int{"page": page, "total_pages": total})
	case http.MethodPost:
		var in struct {
			Type, Name, Content, Comment string
			Proxied                      bool
			TTL                          int
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Type == "" || in.Name == "" || in.Content == "" {
			reply(w, http.StatusBadRequest, nil, nil)
			return
		}
		s.next++
		rec := dns.Record{ID: fmt.Sprintf("rec%d", s.next), Type: in.Type, Name: in.Name, Content: in.Content, Proxied: in.Proxied, Comment: in.Comment, TTL: in.TTL}
		s.records[zoneID] = append(s.records[zoneID], rec)
		reply(w, http.StatusOK, recJSON(rec), nil)
	default:
		reply(w, http.StatusMethodNotAllowed, nil, nil)
	}
}

func (s *Server) record4(w http.ResponseWriter, r *http.Request, zoneID, id string) {
	if !s.zoneKnown(zoneID) {
		reply(w, http.StatusNotFound, nil, nil)
		return
	}
	idx := slices.IndexFunc(s.records[zoneID], func(x dns.Record) bool { return x.ID == id })
	if idx < 0 {
		reply(w, http.StatusNotFound, nil, nil)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var in struct{ Proxied bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			reply(w, http.StatusBadRequest, nil, nil)
			return
		}
		s.records[zoneID][idx].Proxied = in.Proxied
		reply(w, http.StatusOK, recJSON(s.records[zoneID][idx]), nil)
	case http.MethodDelete:
		s.records[zoneID] = slices.Delete(s.records[zoneID], idx, idx+1)
		reply(w, http.StatusOK, map[string]string{"id": id}, nil)
	default:
		reply(w, http.StatusMethodNotAllowed, nil, nil)
	}
}

func recJSON(r dns.Record) map[string]any {
	return map[string]any{"id": r.ID, "type": r.Type, "name": r.Name, "content": r.Content, "proxied": r.Proxied, "comment": r.Comment, "ttl": r.TTL}
}
