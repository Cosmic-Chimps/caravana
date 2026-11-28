// Package s3test is an in-memory S3 for the tests of every package that backs something up: a
// path-style server over httptest that checks each request's Signature Version 4 (by signing it again with
// the same keys), stores objects, answers ranged reads and paged listings, and can be told to fail.
package s3test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/s3"
)

// Server is a fake bucket. Make one with New; Close it when done (New registers no cleanup).
type Server struct {
	// URL is the endpoint to give the client.
	URL string
	// Bucket, AccessKeyID, SecretAccessKey and Region are what the server accepts.
	Bucket, AccessKeyID, SecretAccessKey, Region string

	srv *httptest.Server

	mu       sync.Mutex
	objects  map[string][]byte
	times    map[string]time.Time
	pageSize int
	failures map[string]int // "PUT", "GET", "LIST", "DELETE" -> status to answer, 0 for none
	requests int
}

// New starts a server for the bucket "backups" with a made-up key pair.
func New() *Server {
	s := &Server{Bucket: "backups", AccessKeyID: "AKIATESTKEY", SecretAccessKey: "test-secret-key-sentinel", Region: "us-east-1",
		objects: map[string][]byte{}, times: map[string]time.Time{}, pageSize: 1000, failures: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Config is what a client needs to reach the server.
func (s *Server) Config() s3.Config {
	return s3.Config{Endpoint: s.URL, Region: s.Region, Bucket: s.Bucket,
		Credentials: s3.Credentials{AccessKeyID: s.AccessKeyID, SecretAccessKey: s.SecretAccessKey, Region: s.Region}}
}

// Put stores an object, as if it had been uploaded now.
func (s *Server) Put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), data...)
	s.times[key] = time.Now().UTC()
}

// Object returns an object's bytes and whether it exists.
func (s *Server) Object(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[key]
	return append([]byte(nil), b...), ok
}

// Keys returns every key, sorted.
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Remove deletes an object behind the client's back.
func (s *Server) Remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	delete(s.times, key)
}

// SetPageSize makes listings answer at most n objects a page, to test continuation.
func (s *Server) SetPageSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageSize = n
}

// Fail makes every request of kind op ("PUT", "GET", "LIST" or "DELETE") answer status with an
// AccessDenied-style error body; 0 undoes it.
func (s *Server) Fail(op string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[op] = status
}

// Requests is how many requests the server has answered.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	s.mu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	if code := s.check(r, body); code != "" {
		s.fail(w, http.StatusForbidden, code)
		return
	}
	bucketPath := "/" + s.Bucket
	path := r.URL.Path
	switch {
	case path == bucketPath && r.Method == http.MethodGet:
		s.list(w, r)
	case strings.HasPrefix(path, bucketPath+"/"):
		key := strings.TrimPrefix(path, bucketPath+"/")
		s.object(w, r, key, body)
	default:
		s.fail(w, http.StatusNotFound, "NoSuchBucket")
	}
}

// check verifies the request's signature and payload hash; it returns the S3 error code to refuse
// with, "" when the request is right.
func (s *Server) check(r *http.Request, body []byte) string {
	sum := sha256.Sum256(body)
	if got := r.Header.Get("X-Amz-Content-Sha256"); got != hex.EncodeToString(sum[:]) {
		return "XAmzContentSHA256Mismatch"
	}
	got := r.Header.Get("Authorization")
	if got == "" {
		return "AccessDenied"
	}
	date := r.Header.Get("X-Amz-Date")
	at, err := time.Parse("20060102T150405Z", date)
	if err != nil {
		return "AccessDenied"
	}
	again, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	again.Host = r.Host
	if v := r.Header.Get("Range"); v != "" {
		again.Header.Set("Range", v)
	}
	s3.SignRequest(again, s3.Credentials{AccessKeyID: s.AccessKeyID, SecretAccessKey: s.SecretAccessKey, Region: s.Region},
		hex.EncodeToString(sum[:]), at)
	if again.Header.Get("Authorization") != got {
		return "SignatureDoesNotMatch"
	}
	return ""
}

func (s *Server) failing(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[op]
}

func (s *Server) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>a message the client must not repeat</Message></Error>`, code)
}

func (s *Server) object(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	switch r.Method {
	case http.MethodPut:
		if st := s.failing("PUT"); st != 0 {
			s.fail(w, st, "AccessDenied")
			return
		}
		s.Put(key, body)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if st := s.failing("GET"); st != 0 {
			s.fail(w, st, "AccessDenied")
			return
		}
		data, ok := s.Object(key)
		if !ok {
			s.fail(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		if rng := r.Header.Get("Range"); rng != "" {
			var from, to int
			if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &from, &to); err != nil || from != 0 {
				s.fail(w, http.StatusBadRequest, "InvalidRange")
				return
			}
			if len(data) == 0 {
				s.fail(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
				return
			}
			if to+1 < len(data) {
				data = data[:to+1]
			}
			w.WriteHeader(http.StatusPartialContent)
		}
		w.Write(data)
	case http.MethodDelete:
		if st := s.failing("DELETE"); st != 0 {
			s.fail(w, st, "AccessDenied")
			return
		}
		s.Remove(key)
		w.WriteHeader(http.StatusNoContent)
	default:
		s.fail(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if st := s.failing("LIST"); st != 0 {
		s.fail(w, st, "AccessDenied")
		return
	}
	q := r.URL.Query()
	prefix, after := q.Get("prefix"), q.Get("continuation-token")
	s.mu.Lock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	size := s.pageSize
	type content struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		Size         int    `xml:"Size"`
	}
	var out struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		IsTruncated           bool
		NextContinuationToken string `xml:",omitempty"`
		KeyCount              int
		Contents              []content
	}
	if len(keys) > size {
		keys = keys[:size]
		out.IsTruncated = true
		out.NextContinuationToken = keys[len(keys)-1] // the last key given: the next page is what sorts after it
	}
	for _, k := range keys {
		out.Contents = append(out.Contents, content{Key: k, LastModified: s.times[k].Format(time.RFC3339), Size: len(s.objects[k])})
	}
	s.mu.Unlock()
	out.KeyCount = len(out.Contents)
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(out)
}
