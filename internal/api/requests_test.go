package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// Feature 047: listRequests over the fake Nomad's file system. Every body is
// checked against the contract.

const (
	traefikCopyID    = "7a1c9e22-0000-4000-8000-0000000000aa"
	requestsSentinel = "s3cret-sentinel-047"
)

// requestsFixture is app whoami (service web) with Traefik's job writing the
// access log, one running allocation and a file of requests just before now.
type requestsFixture struct {
	e   *testEnv
	now time.Time
}

func (f requestsFixture) at(ago time.Duration) time.Time { return f.now.Add(-ago) }

// line is one line of Traefik's access log; the line's own time is when it ended.
func (f requestsFixture) line(ago, dur time.Duration, svc, method, path string, status int, client string) string {
	names := ""
	if svc != "" {
		names = fmt.Sprintf(`"RouterName":%q,"ServiceName":%q,`, svc+"@nomad", svc+"@nomad")
	}
	start := f.at(ago)
	return fmt.Sprintf(`{"ClientHost":%q,"DownstreamContentSize":%d,"DownstreamStatus":%d,"Duration":%d,`+
		`"RequestHost":"whoami.example","RequestMethod":%q,"RequestPath":%q,"RetryAttempts":0,%s"StartUTC":%q,`+
		`"TLSVersion":"1.3","entryPointName":"websecure","level":"info","msg":"","time":%q}`,
		client, 398, status, dur.Nanoseconds(), method, path, names,
		start.Format(time.RFC3339Nano), start.Add(dur).Format(time.RFC3339))
}

func newRequestsFixture(t *testing.T) requestsFixture {
	t.Helper()
	e := newTestEnv(t, "")
	seedHealthy(t, e, "whoami", 1, 7)
	f := requestsFixture{e: e, now: time.Now().UTC().Truncate(time.Second)}
	e.Nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	e.Nomad.SetCopies("traefik", api.AllocationListStub{ID: traefikCopyID, TaskGroup: "traefik", JobVersion: 1, ClientStatus: "running",
		CreateTime: f.now.Add(-time.Hour).UnixNano(),
		TaskStates: map[string]*api.TaskState{"traefik": {StartedAt: f.now.Add(-time.Hour)}}})
	e.Nomad.AddLogFile(traefikCopyID, "stdout", 0, []string{
		f.line(9*time.Minute, 3*time.Millisecond, "whoami-web", "GET", "/", 200, "::1"),
		f.line(8*time.Minute, 600*time.Millisecond, "whoami-web", "POST", "/orders", 500, "203.0.113.7"),
		f.line(7*time.Minute, 5*time.Millisecond, "whoami-web", "GET", "/missing", 404, "203.0.113.7"),
		f.line(6*time.Minute, 5*time.Millisecond, "whoami-web", "GET", "/x?token="+requestsSentinel, 200, "203.0.113.8"),
		f.line(5*time.Minute, 5*time.Millisecond, "caravana@file", "GET", "/console", 200, "::1"),
		f.line(4*time.Minute, 5*time.Millisecond, "", "GET", "/nobody", 404, "::1"),
		`{"level":"info","msg":"Starting provider","time":"` + f.at(10*time.Minute).Format(time.RFC3339) + `"}`,
	}, f.at(3*time.Minute))
	return f
}

func (f requestsFixture) get(t *testing.T, path string) (int, http.Header, []byte) {
	t.Helper()
	return call(t, http.MethodGet, f.e.URL+path, "", "")
}

type requestReadBody struct {
	AccessLog bool `json:"accessLog"`
	Requests  []struct {
		Time        string  `json:"time"`
		App         string  `json:"app"`
		Service     string  `json:"service"`
		Router      string  `json:"router"`
		Method      string  `json:"method"`
		Host        string  `json:"host"`
		Path        string  `json:"path"`
		Status      int     `json:"status"`
		StatusClass string  `json:"statusClass"`
		DurationMs  float64 `json:"durationMs"`
		Bytes       int64   `json:"bytes"`
		Client      string  `json:"client"`
		TLS         string  `json:"tls"`
		Retries     int     `json:"retries"`
	} `json:"requests"`
	Total     int `json:"total"`
	Histogram struct {
		From          string `json:"from"`
		To            string `json:"to"`
		BucketSeconds int    `json:"bucketSeconds"`
		Buckets       []struct {
			From    string         `json:"from"`
			Count   int            `json:"count"`
			Classes map[string]int `json:"classes"`
			Read    bool           `json:"read"`
		} `json:"buckets"`
	} `json:"histogram"`
	Coverage struct {
		From     string `json:"from"`
		Complete bool   `json:"complete"`
	} `json:"coverage"`
	Dropped  bool `json:"dropped"`
	Services []struct {
		App     string `json:"app"`
		Service string `json:"service"`
		Count   int    `json:"count"`
	} `json:"services"`
}

func (r requestReadBody) paths() []string {
	out := make([]string, len(r.Requests))
	for i, q := range r.Requests {
		out[i] = q.Path
	}
	return out
}

func (f requestsFixture) readOK(t *testing.T, path string) requestReadBody {
	t.Helper()
	status, header, raw := f.get(t, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, status, raw)
	}
	if header.Get("Content-Type") != "application/json" || header.Get("Cache-Control") != "no-store" {
		t.Errorf("GET %s: Content-Type %q, Cache-Control %q, want application/json and no-store",
			path, header.Get("Content-Type"), header.Get("Cache-Control"))
	}
	checkSchema(t, loadContract(t), raw, "RequestRead")
	return decodeAs[requestReadBody](t, raw)
}

func TestListRequests(t *testing.T) {
	f := newRequestsFixture(t)

	t.Run("the default read: shape and order", func(t *testing.T) {
		got := f.readOK(t, "/v1/requests")
		if !got.AccessLog {
			t.Fatal("accessLog = false")
		}
		want := []string{"/x?token=" + requestsSentinel, "/missing", "/orders", "/"}
		if g := got.paths(); !slices.Equal(g, want) {
			t.Fatalf("paths = %q, want %q", g, want)
		}
		if got.Total != 4 {
			t.Errorf("total = %d, want 4", got.Total)
		}
		q := got.Requests[2]
		if q.App != "whoami" || q.Service != "web" || q.Router != "whoami-web" || q.Method != "POST" || q.Host != "whoami.example" ||
			q.Status != 500 || q.StatusClass != "5xx" || q.DurationMs != 600 || q.Bytes != 398 || q.Client != "203.0.113.7" ||
			q.TLS != "1.3" || q.Retries != 0 || q.Time != f.at(8*time.Minute).Format(time.RFC3339Nano) && q.Time != f.at(8*time.Minute).Format(time.RFC3339) {
			t.Errorf("the 500 = %+v", q)
		}
		if len(got.Services) != 1 || got.Services[0].App != "whoami" || got.Services[0].Service != "web" || got.Services[0].Count != 4 {
			t.Errorf("services = %+v", got.Services)
		}
	})

	t.Run("histogram and coverage", func(t *testing.T) {
		got := f.readOK(t, "/v1/requests")
		h := got.Histogram
		if len(h.Buckets) != 30 || h.BucketSeconds != 30 {
			t.Fatalf("histogram = %+v, want 30 buckets of 30 s", h)
		}
		total, fives := 0, 0
		for _, b := range h.Buckets {
			total += b.Count
			fives += b.Classes["5xx"]
			if !b.Read {
				t.Errorf("bucket %s not read", b.From)
			}
			if len(b.Classes) != 6 {
				t.Errorf("classes = %v, want the six", b.Classes)
			}
		}
		if total != 4 || fives != 1 {
			t.Errorf("histogram counts %d requests and %d 5xx, want 4 and 1", total, fives)
		}
		if !got.Coverage.Complete || got.Coverage.From == "" || got.Dropped {
			t.Errorf("coverage = %+v dropped %v", got.Coverage, got.Dropped)
		}
	})

	t.Run("filters", func(t *testing.T) {
		tests := []struct {
			query string
			want  int
		}{
			{"?status=5xx", 1},
			{"?status=4xx,5xx", 2},
			{"?status=2xx", 2},
			{"?method=post", 1},
			{"?path=" + url.QueryEscape("/m"), 1},
			{"?path=" + url.QueryEscape("/x"), 1},
			{"?minDuration=500", 1},
			{"?client=203.0.113.7", 2},
			{"?app=whoami&service=whoami/web", 4},
			{"?lines=2", 2},
			{"?range=1h", 4},
			{"?project=default&environment=production", 4},
			{"?from=" + url.QueryEscape(f.at(7*time.Minute+30*time.Second).Format(time.RFC3339)), 2},
			{"?before=" + url.QueryEscape(f.at(7*time.Minute+30*time.Second).Format(time.RFC3339)), 2},
		}
		for _, tc := range tests {
			if got := f.readOK(t, "/v1/requests"+tc.query); len(got.Requests) != tc.want {
				t.Errorf("requests%s = %d %q, want %d", tc.query, len(got.Requests), got.paths(), tc.want)
			}
		}
	})

	t.Run("no requests is an empty list, not null", func(t *testing.T) {
		status, _, raw := f.get(t, "/v1/requests?status=1xx")
		if status != http.StatusOK || strings.Contains(string(raw), "null") {
			t.Errorf("status %d body %s, want 200 and no null", status, raw)
		}
	})
}

func TestListRequestsRefusals(t *testing.T) {
	f := newRequestsFixture(t)
	now := f.now.Format(time.RFC3339)
	earlier := f.at(time.Hour).Format(time.RFC3339)
	tests := []struct {
		name, query string
		status      int
		code, field string
	}{
		{"unknown status class", "?status=6xx", 422, codeInvalidField, "status"},
		{"empty class in the list", "?status=5xx,", 422, codeInvalidField, "status"},
		{"method with spaces", "?method=G%20E%20T", 422, codeInvalidField, "method"},
		{"method of digits", "?method=123", 422, codeInvalidField, "method"},
		{"path without a slash", "?path=api", 422, codeInvalidField, "path"},
		{"path of 1025 bytes", "?path=/" + strings.Repeat("a", 1024), 422, codeInvalidField, "path"},
		{"minDuration not a number", "?minDuration=slow", 422, codeInvalidField, "minDuration"},
		{"minDuration negative", "?minDuration=-1", 422, codeInvalidField, "minDuration"},
		{"minDuration with a unit", "?minDuration=1s", 422, codeInvalidField, "minDuration"},
		{"client of 65 bytes", "?client=" + strings.Repeat("1", 65), 422, codeInvalidField, "client"},
		{"range not a preset", "?range=2h", 422, codeInvalidField, "range"},
		{"range with from", "?range=1h&from=" + url.QueryEscape(earlier), 422, codeInvalidField, "range"},
		{"from after to", "?from=" + url.QueryEscape(now) + "&to=" + url.QueryEscape(earlier), 422, codeInvalidField, "from"},
		{"from not a time", "?from=yesterday", 422, codeInvalidField, "from"},
		{"before not a time", "?before=soon", 422, codeInvalidField, "before"},
		{"lines zero", "?lines=0", 422, codeInvalidField, "lines"},
		{"lines too many", "?lines=1001", 422, codeInvalidField, "lines"},
		{"lines not a number", "?lines=many", 422, codeInvalidField, "lines"},
		{"service without its app", "?service=web", 422, codeInvalidField, "service"},
		{"service that isn't one", "?service=whoami/nope", 422, codeInvalidField, "service"},
		{"project not found", "?project=nope", 404, codeProjectNotFound, "project"},
		{"environment not found", "?project=default&environment=nope", 404, codeEnvironmentNotFound, "environment"},
		{"app not found", "?app=ghost", 404, codeAppNotFound, "app"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, header, raw := f.get(t, "/v1/requests"+tc.query)
			wantError(t, tc.query, status, raw, tc.status, tc.code, tc.field)
			if header.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q on an error, want no-store", header.Get("Cache-Control"))
			}
		})
	}

	t.Run("Nomad down is 502", func(t *testing.T) {
		f.e.Nomad.Down()
		defer f.e.Nomad.Up()
		status, _, raw := f.get(t, "/v1/requests")
		wantError(t, "Nomad down", status, raw, http.StatusBadGateway, codeNomadUnavailable, "")
	})
}

func TestListRequestsTraefik(t *testing.T) {
	t.Run("a job that doesn't write the access log is 200 with accessLog false", func(t *testing.T) {
		f := newRequestsFixture(t)
		f.e.Nomad.SetTraefikJob("traefik:v3.7.13")
		got := f.readOK(t, "/v1/requests")
		if got.AccessLog || len(got.Requests) != 0 || got.Total != 0 || len(got.Histogram.Buckets) != 30 {
			t.Errorf("read = %+v", got)
		}
	})
	t.Run("no Traefik is 503", func(t *testing.T) {
		e := newTestEnv(t, "")
		seedHealthy(t, e, "whoami", 1, 7)
		status, header, raw := call(t, http.MethodGet, e.URL+"/v1/requests", "", "")
		got := wantError(t, "no Traefik", status, raw, http.StatusServiceUnavailable, codeProxyUnavailable, "")
		if got.Error.Message != "Traefik isn't running" || header.Get("Cache-Control") != "no-store" {
			t.Errorf("message %q, Cache-Control %q", got.Error.Message, header.Get("Cache-Control"))
		}
	})
	t.Run("a job with no running allocation is 503", func(t *testing.T) {
		f := newRequestsFixture(t)
		f.e.Nomad.SetCopies("traefik")
		status, _, raw := f.get(t, "/v1/requests")
		wantError(t, "no allocation", status, raw, http.StatusServiceUnavailable, codeProxyUnavailable, "")
	})
}

// The secrets rule (plan D9): the path is in a 200 body as the text it is, and
// nowhere else: not in an error answer, not in the server's log. The query
// string never reaches Caravana (Traefik drops it); a line that somehow holds
// one is only data.
func TestListRequestsSentinel(t *testing.T) {
	f := newRequestsFixture(t)

	status, _, raw := f.get(t, "/v1/requests?path="+url.QueryEscape("/x?token="+requestsSentinel))
	if status != http.StatusOK || !strings.Contains(string(raw), requestsSentinel) {
		t.Fatalf("a read that asks for the path = %d %s", status, raw)
	}

	for _, query := range []string{
		"?client=" + strings.Repeat(requestsSentinel, 4), // too long
		"?path=" + requestsSentinel,                      // no slash
		"?method=" + requestsSentinel,                    // not a method
		"?service=" + requestsSentinel,                   // not a service
		"?status=" + requestsSentinel,                    // not a class
		"?minDuration=" + requestsSentinel,               // not a number
	} {
		status, _, raw := f.get(t, "/v1/requests"+query)
		if status < 400 {
			t.Errorf("GET %s = %d, want an error", query[:12], status)
		}
		if strings.Contains(string(raw), requestsSentinel) {
			t.Errorf("the error answer for %s… holds the value: %s", query[:12], raw)
		}
	}
	f.e.Nomad.FailFS(403)
	status, _, raw = f.get(t, "/v1/requests")
	f.e.Nomad.FailFS(0)
	if status != http.StatusBadGateway || strings.Contains(string(raw), requestsSentinel) {
		t.Errorf("a failing file system = %d %s", status, raw)
	}

	if strings.Contains(f.e.Logs.String(), requestsSentinel) {
		t.Errorf("the server log holds a path or a value: %s", f.e.Logs.String())
	}
}
