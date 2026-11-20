package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/accesslog"
	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	copyTraefik      = "7a1c9e22-0000-4000-8000-0000000000aa"
	sentinelRequests = "s3cret-sentinel-047"
)

// reqLine is one line of Traefik's access log as the lab writes it: start is
// when the request began (StartUTC), and the line's own "time" (seconds, what
// the walk reads) is when it ended. svc is Traefik's service name without
// "@nomad"; "" is a request no router took.
func reqLine(start time.Time, dur time.Duration, svc, method, path string, status int, client string) string {
	names := ""
	if svc != "" {
		names = fmt.Sprintf(`"RouterName":%q,"ServiceName":%q,`, svc+"@nomad", svc+"@nomad")
	}
	return fmt.Sprintf(`{"ClientHost":%q,"DownstreamContentSize":%d,"DownstreamStatus":%d,"Duration":%d,`+
		`"RequestHost":"x.example","RequestMethod":%q,"RequestPath":%q,"RetryAttempts":0,%s"StartUTC":%q,`+
		`"TLSVersion":"1.3","entryPointName":"websecure","level":"info","msg":"","time":%q}`,
		client, 100, status, dur.Nanoseconds(), method, path, names,
		start.UTC().Format(time.RFC3339Nano), start.Add(dur).UTC().Format(time.RFC3339))
}

// reqEnv is the logs' environment (blog: web and worker; docs: web) with a
// Traefik job that writes the access log and one running allocation, created
// at readStart. No request has been written yet.
func reqEnv(t *testing.T) *env {
	t.Helper()
	e := readEnv(t)
	e.nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true", "--accesslog.format=json")
	stub := copyStub(copyTraefik, "traefik", "running", readStart)
	stub.CreateTime = readStart.UnixNano()
	e.nomad.SetCopies("traefik", stub)
	return e
}

// at is a time on 2026-10-25 (readNow is 00:40).
func reqAt(clock string) time.Time { return wrote(clock) }

func mustReadRequests(t *testing.T, e *env, q RequestQuery) RequestRead {
	t.Helper()
	read, err := e.svc.ReadRequests(context.Background(), q)
	if err != nil {
		t.Fatalf("ReadRequests: %v", err)
	}
	return read
}

func paths(rows []RequestEntry) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Path
	}
	return out
}

// The file is in completion order; the read answers by start time, newest first,
// and keeps only the requests of the place's apps.
func TestReadRequestsOrderAndOwners(t *testing.T) {
	e := reqEnv(t)
	if _, err := e.svc.CreateProject(context.Background(), "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	saveApp(t, e, appWith("shop--production--web", 1))
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:30:00"), 5*time.Minute, "blog-web", "POST", "/slow", 200, "1.1.1.1"), // ends 00:35:00, starts first
		reqLine(reqAt("00:34:00"), time.Second, "blog-web", "GET", "/fast", 200, "1.1.1.1"),    // ends 00:34:01, written first by time but after /slow in the file
		reqLine(reqAt("00:36:00"), 20*time.Millisecond, "docs-web", "GET", "/docs", 404, "2.2.2.2"),
		reqLine(reqAt("00:36:30"), 20*time.Millisecond, "blog-worker", "GET", "/work", 200, "1.1.1.1"),
		reqLine(reqAt("00:37:00"), 20*time.Millisecond, "shop--production--web-web", "GET", "/other-place", 200, "1.1.1.1"),
		reqLine(reqAt("00:37:10"), 20*time.Millisecond, "caravana@file", "GET", "/console", 200, "1.1.1.1"),
		reqLine(reqAt("00:37:20"), 20*time.Millisecond, "gone-web", "GET", "/deleted", 200, "1.1.1.1"),
		reqLine(reqAt("00:37:30"), 20*time.Millisecond, "", "GET", "/no-router", 404, "1.1.1.1"),
		`{"level":"info","msg":"Starting provider *nomad.Provider","time":"2026-10-25T00:00:00Z"}`,
		`not json at all`,
	}, wrote("00:39:50"))

	read := mustReadRequests(t, e, RequestQuery{Range: time.Hour})
	if !read.AccessLog {
		t.Fatal("AccessLog = false")
	}
	if got, want := paths(read.Requests), []string{"/work", "/docs", "/fast", "/slow"}; !slices.Equal(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
	if read.Total != 4 {
		t.Errorf("Total = %d, want 4", read.Total)
	}
	first := read.Requests[0]
	want := RequestEntry{Time: reqAt("00:36:30"), App: "blog", Service: "worker", Router: "blog-worker", Method: "GET", Host: "x.example",
		Path: "/work", Status: 200, Class: "2xx", Duration: 20 * time.Millisecond, Bytes: 100, Client: "1.1.1.1", TLS: "1.3"}
	if first != want {
		t.Errorf("first = %+v, want %+v", first, want)
	}
	if got := read.Requests[3]; got.Duration != 5*time.Minute || got.Method != "POST" {
		t.Errorf("slow request = %+v", got)
	}

	// The rail counts every service of the place, whatever the list is cut to.
	counts := map[string]int{}
	for _, s := range read.Services {
		counts[s.App+"/"+s.Service] = s.Count
	}
	if wantCounts := map[string]int{"blog/web": 2, "blog/worker": 1, "docs/web": 1}; fmt.Sprint(counts) != fmt.Sprint(wantCounts) {
		t.Errorf("services = %v, want %v", counts, wantCounts)
	}
	if read.Services[0].Service != "web" || read.Services[0].Count != 2 {
		t.Errorf("rail isn't ordered by count: %+v", read.Services)
	}

	// The other place's app is read in its own place.
	other := mustReadRequests(t, e, RequestQuery{Project: "shop", Range: time.Hour})
	if got := paths(other.Requests); !slices.Equal(got, []string{"/other-place"}) {
		t.Errorf("shop's paths = %q", got)
	}
}

func TestReadRequestsFilters(t *testing.T) {
	e := reqEnv(t)
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:30:00"), 10*time.Millisecond, "blog-web", "GET", "/", 200, "1.1.1.1"),
		reqLine(reqAt("00:31:00"), 600*time.Millisecond, "blog-web", "POST", "/api/orders", 500, "2.2.2.2"),
		reqLine(reqAt("00:32:00"), 10*time.Millisecond, "blog-web", "GET", "/api/users", 404, "1.1.1.1"),
		reqLine(reqAt("00:33:00"), 2*time.Second, "docs-web", "GET", "/api/slow", 502, "3.3.3.3"),
		reqLine(reqAt("00:34:00"), 10*time.Millisecond, "docs-web", "GET", "/page", 301, "1.1.1.1"),
		reqLine(reqAt("00:35:00"), 10*time.Millisecond, "blog-worker", "PUT", "/api/orders/1", 204, "1.1.1.1"),
		reqLine(reqAt("00:36:00"), 10*time.Millisecond, "blog-web", "GET", "/ws", 101, "1.1.1.1"),
		reqLine(reqAt("00:37:00"), 10*time.Millisecond, "blog-web", "GET", "/gone", 0, "1.1.1.1"),
	}, wrote("00:39:50"))

	tests := []struct {
		name  string
		query RequestQuery
		want  []string // paths, newest first
	}{
		{"everything", RequestQuery{}, []string{"/gone", "/ws", "/api/orders/1", "/page", "/api/slow", "/api/users", "/api/orders", "/"}},
		{"5xx", RequestQuery{Statuses: []string{"5xx"}}, []string{"/api/slow", "/api/orders"}},
		{"4xx and 5xx", RequestQuery{Statuses: []string{"4xx", "5xx"}}, []string{"/api/slow", "/api/users", "/api/orders"}},
		{"1xx", RequestQuery{Statuses: []string{"1xx"}}, []string{"/ws"}},
		{"other", RequestQuery{Statuses: []string{"other"}}, []string{"/gone"}},
		{"method", RequestQuery{Method: "post"}, []string{"/api/orders"}},
		{"path prefix", RequestQuery{Path: "/api/orders"}, []string{"/api/orders/1", "/api/orders"}},
		{"path prefix is not a substring", RequestQuery{Path: "/orders"}, nil},
		{"minimum duration", RequestQuery{MinDuration: 500 * time.Millisecond}, []string{"/api/slow", "/api/orders"}},
		{"client", RequestQuery{Client: "2.2.2.2"}, []string{"/api/orders"}},
		{"app", RequestQuery{App: "docs"}, []string{"/page", "/api/slow"}},
		{"service", RequestQuery{Service: "blog/worker"}, []string{"/api/orders/1"}},
		{"5xx of an app with a path prefix", RequestQuery{App: "blog", Statuses: []string{"5xx"}, Path: "/api"}, []string{"/api/orders"}},
		{"lines", RequestQuery{Lines: 2}, []string{"/gone", "/ws"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.query.Range = time.Hour
			read := mustReadRequests(t, e, tt.query)
			if got := paths(read.Requests); !slices.Equal(got, tt.want) && !(len(got) == 0 && len(tt.want) == 0) {
				t.Errorf("paths = %q, want %q", got, tt.want)
			}
		})
	}

	// Total is what passed, before Lines cut the list.
	read := mustReadRequests(t, e, RequestQuery{Range: time.Hour, Lines: 3})
	if read.Total != 8 || len(read.Requests) != 3 {
		t.Errorf("Total = %d with %d requests, want 8 and 3", read.Total, len(read.Requests))
	}

	// The rail ignores the app and service cut but not the other filters.
	read = mustReadRequests(t, e, RequestQuery{Range: time.Hour, App: "docs", Statuses: []string{"5xx"}})
	counts := map[string]int{}
	for _, s := range read.Services {
		counts[s.App+"/"+s.Service] = s.Count
	}
	if fmt.Sprint(counts) != fmt.Sprint(map[string]int{"blog/web": 1, "docs/web": 1}) {
		t.Errorf("rail under 5xx = %v", counts)
	}
}

func TestReadRequestsRange(t *testing.T) {
	e := reqEnv(t)
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:00:00").Add(-30*time.Minute), time.Second, "blog-web", "GET", "/early", 200, "1.1.1.1"),
		reqLine(reqAt("00:20:00"), time.Second, "blog-web", "GET", "/mid", 200, "1.1.1.1"),
		reqLine(reqAt("00:38:00"), time.Second, "blog-web", "GET", "/late", 200, "1.1.1.1"),
	}, wrote("00:39:50"))

	if got := paths(mustReadRequests(t, e, RequestQuery{}).Requests); !slices.Equal(got, []string{"/late"}) {
		t.Errorf("default range (15m): %q", got)
	}
	if got := paths(mustReadRequests(t, e, RequestQuery{Range: time.Hour}).Requests); !slices.Equal(got, []string{"/late", "/mid"}) {
		t.Errorf("1h: %q", got)
	}
	read := mustReadRequests(t, e, RequestQuery{From: reqAt("00:10:00"), To: reqAt("00:30:00")})
	if got := paths(read.Requests); !slices.Equal(got, []string{"/mid"}) {
		t.Errorf("from/to: %q", got)
	}
	if !read.From.Equal(reqAt("00:10:00")) || !read.To.Equal(reqAt("00:30:00")) {
		t.Errorf("From, To = %v, %v", read.From, read.To)
	}
	// Show older: before cuts the newest.
	read = mustReadRequests(t, e, RequestQuery{Range: time.Hour, Before: reqAt("00:30:00")})
	if got := paths(read.Requests); !slices.Equal(got, []string{"/mid"}) {
		t.Errorf("before: %q", got)
	}
	// The walk stopped at the range's start: the read is complete.
	read = mustReadRequests(t, e, RequestQuery{Range: time.Hour})
	if !read.Coverage.Complete || !read.Coverage.From.Equal(read.From) {
		t.Errorf("coverage = %+v, want complete from %v", read.Coverage, read.From)
	}
}

// A Traefik allocation that began inside the range has nothing before it: the
// page says it read from the allocation's start. Files Nomad purged are dropped.
func TestReadRequestsCoverage(t *testing.T) {
	e := reqEnv(t)
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:30:00"), time.Second, "blog-web", "GET", "/a", 200, "1.1.1.1"),
	}, wrote("00:39:50"))

	// The allocation was created at 23:00 (readStart); a 24 h range starts before it.
	read := mustReadRequests(t, e, RequestQuery{Range: 24 * time.Hour})
	if read.Coverage.Complete || !read.Coverage.From.Equal(readStart) || read.Dropped {
		t.Errorf("coverage = %+v dropped %v, want incomplete from %v", read.Coverage, read.Dropped, readStart)
	}
	// A 15 m range starts after it.
	read = mustReadRequests(t, e, RequestQuery{})
	if !read.Coverage.Complete {
		t.Errorf("15m coverage = %+v, want complete", read.Coverage)
	}

	// Files before the first one were purged.
	e.nomad.AddLogFile(copyTraefik, "stdout", 3, []string{
		reqLine(reqAt("00:31:00"), time.Second, "blog-web", "GET", "/b", 200, "1.1.1.1"),
	}, wrote("00:39:50"))
	e.nomad.PurgeLogFile(copyTraefik, "stdout", 0)
	read = mustReadRequests(t, e, RequestQuery{Range: 24 * time.Hour})
	if !read.Dropped {
		t.Error("Dropped = false with the first files purged")
	}
	if got := paths(read.Requests); !slices.Equal(got, []string{"/b"}) {
		t.Errorf("paths = %q", got)
	}
}

// Two files: the walk reads into the older one for a long range, and stops
// before it for a short one.
func TestReadRequestsAcrossFiles(t *testing.T) {
	e := reqEnv(t)
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:10:00"), time.Second, "blog-web", "GET", "/old", 200, "1.1.1.1"),
	}, wrote("00:20:00"))
	e.nomad.AddLogFile(copyTraefik, "stdout", 1, []string{
		reqLine(reqAt("00:30:00"), time.Second, "blog-web", "GET", "/new", 200, "1.1.1.1"),
	}, wrote("00:39:50"))
	if got := paths(mustReadRequests(t, e, RequestQuery{Range: time.Hour}).Requests); !slices.Equal(got, []string{"/new", "/old"}) {
		t.Errorf("1h: %q", got)
	}
	if got := paths(mustReadRequests(t, e, RequestQuery{}).Requests); !slices.Equal(got, []string{"/new"}) {
		t.Errorf("15m: %q", got)
	}
}

func TestReadRequestsAccessLogOff(t *testing.T) {
	e := reqEnv(t)
	e.nomad.SetTraefikJob("traefik:v3.7.13") // the installer's job from before the feature: no access log
	read := mustReadRequests(t, e, RequestQuery{})
	if read.AccessLog || len(read.Requests) != 0 || read.Total != 0 {
		t.Errorf("read = %+v", read)
	}
	if read.From.IsZero() || read.To.IsZero() {
		t.Error("From and To are not resolved")
	}
	for _, r := range e.nomad.FileRequests() {
		t.Errorf("a file request was made without the access log: %+v", r)
	}
}

func TestReadRequestsNoTraefik(t *testing.T) {
	ctx := context.Background()
	e := readEnv(t) // no traefik job at all
	if _, err := e.svc.ReadRequests(ctx, RequestQuery{}); !errors.Is(err, ErrNoProxy) {
		t.Errorf("no job: err = %v, want ErrNoProxy", err)
	}
	e.nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true") // a job with no running allocation
	if _, err := e.svc.ReadRequests(ctx, RequestQuery{}); !errors.Is(err, ErrNoProxy) {
		t.Errorf("no allocation: err = %v, want ErrNoProxy", err)
	}
	stub := copyStub(copyTraefik, "traefik", "complete", readStart)
	e.nomad.SetCopies("traefik", stub)
	if _, err := e.svc.ReadRequests(ctx, RequestQuery{}); !errors.Is(err, ErrNoProxy) {
		t.Errorf("a finished allocation: err = %v, want ErrNoProxy", err)
	}
}

func TestReadRequestsRefusals(t *testing.T) {
	ctx := context.Background()
	e := reqEnv(t)
	long := strings.Repeat("a", 2000)
	tests := []struct {
		name  string
		query RequestQuery
		field string
	}{
		{"status", RequestQuery{Statuses: []string{"6xx"}}, "status"},
		{"empty status", RequestQuery{Statuses: []string{""}}, "status"},
		{"method", RequestQuery{Method: "G E T"}, "method"},
		{"method digits", RequestQuery{Method: "123"}, "method"},
		{"path without slash", RequestQuery{Path: "api"}, "path"},
		{"path too long", RequestQuery{Path: "/" + long}, "path"},
		{"duration", RequestQuery{MinDuration: -time.Millisecond}, "minDuration"},
		{"client too long", RequestQuery{Client: strings.Repeat("1", 65)}, "client"},
		{"range", RequestQuery{Range: 2 * time.Hour}, "range"},
		{"range with from", RequestQuery{Range: time.Hour, From: reqAt("00:00:00")}, "range"},
		{"from after to", RequestQuery{From: reqAt("00:30:00"), To: reqAt("00:10:00")}, "from"},
		{"lines too many", RequestQuery{Lines: 1001}, "lines"},
		{"lines negative", RequestQuery{Lines: -1}, "lines"},
		{"service form", RequestQuery{Service: "blog"}, "service"},
		{"service of another app", RequestQuery{App: "docs", Service: "blog/web"}, "service"},
		{"no such service", RequestQuery{Service: "blog/nope"}, "service"},
		{"before the range", RequestQuery{Range: time.Hour, Before: reqAt("00:00:00").Add(-2 * time.Hour)}, "before"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.ReadRequests(ctx, tt.query)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tt.field {
				t.Fatalf("err = %v, want a field error on %q", err, tt.field)
			}
		})
	}

	if _, err := e.svc.ReadRequests(ctx, RequestQuery{App: "nope"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app: err = %v, want store.ErrNotFound", err)
	}
	var pe *PlaceError
	if _, err := e.svc.ReadRequests(ctx, RequestQuery{Project: "nope"}); !errors.As(err, &pe) || pe.Field != "project" {
		t.Errorf("unknown project: err = %v, want a *PlaceError on project", err)
	}
	if _, err := e.svc.ReadRequests(ctx, RequestQuery{Environment: "nope"}); !errors.As(err, &pe) || pe.Field != "environment" {
		t.Errorf("unknown environment: err = %v, want a *PlaceError on environment", err)
	}
}

func TestReadRequestsNomadDown(t *testing.T) {
	e := reqEnv(t)
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:30:00"), time.Second, "blog-web", "GET", "/", 200, "1.1.1.1"),
	}, wrote("00:39:50"))
	e.nomad.FailFS(403) // a token without read-fs
	_, err := e.svc.ReadRequests(context.Background(), RequestQuery{})
	if !errors.Is(err, deploy.ErrNomadUnavailable) {
		t.Errorf("err = %v, want deploy.ErrNomadUnavailable", err)
	}
}

func TestRequestHistogram(t *testing.T) {
	e := reqEnv(t)
	// A 15 minute range ending 00:40 has 30-second bins: 00:25:00 is bin 0.
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:25:05"), 10*time.Millisecond, "blog-web", "GET", "/a", 200, "1.1.1.1"),
		reqLine(reqAt("00:25:10"), 10*time.Millisecond, "blog-web", "GET", "/b", 500, "1.1.1.1"),
		reqLine(reqAt("00:25:20"), 10*time.Millisecond, "blog-web", "GET", "/c", 500, "1.1.1.1"),
		reqLine(reqAt("00:39:45"), 10*time.Millisecond, "docs-web", "GET", "/d", 404, "1.1.1.1"),
	}, wrote("00:39:50"))
	h := mustReadRequests(t, e, RequestQuery{}).Histogram()
	if h.BucketSeconds != 30 {
		t.Errorf("BucketSeconds = %d, want 30", h.BucketSeconds)
	}
	if b := h.Bins[0]; b.Count != 3 || b.ClassCount("2xx") != 1 || b.ClassCount("5xx") != 2 || !b.Read {
		t.Errorf("bin 0 = %+v", b)
	}
	if b := h.Bins[requestBins-1]; b.Count != 1 || b.ClassCount("4xx") != 1 {
		t.Errorf("last bin = %+v", b)
	}
	total := 0
	for _, b := range h.Bins {
		total += b.Count
	}
	if total != 4 {
		t.Errorf("bins hold %d requests, want 4", total)
	}

	// With the lines cut to one, the histogram still counts every request.
	h = mustReadRequests(t, e, RequestQuery{Lines: 1}).Histogram()
	total = 0
	for _, b := range h.Bins {
		total += b.Count
	}
	if total != 4 {
		t.Errorf("with Lines 1 the bins hold %d requests, want 4", total)
	}

	// Bins before the coverage aren't zeros: the allocation began at 23:00 and a 24 h range starts before it.
	h = mustReadRequests(t, e, RequestQuery{Range: 24 * time.Hour}).Histogram()
	if h.Bins[0].Read || !h.Bins[requestBins-1].Read {
		t.Errorf("Read = %v first, %v last; want false, true", h.Bins[0].Read, h.Bins[requestBins-1].Read)
	}
}

func TestRequestQueryValidateMethodCase(t *testing.T) {
	// Validate wants capitals; ReadRequests upper-cases what it is given.
	if err := (RequestQuery{Method: "get"}).Validate(); err == nil {
		t.Error("Validate accepted a lower-case method")
	}
	if err := (RequestQuery{Method: "GET", Statuses: accesslog.Classes}).Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// The secrets rule (plan D9): a request line, the path above all, is never
// written to the server log, an error or a notice. The query string never
// reaches Caravana (Traefik drops it), but a line that somehow holds one is
// still only data.
func TestReadRequestsSentinel(t *testing.T) {
	e, logs := liveEnv(t)
	e.nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	stub := copyStub(copyTraefik, "traefik", "running", readStart)
	stub.CreateTime = readStart.UnixNano()
	e.nomad.SetCopies("traefik", stub)
	bad := "/x?token=" + sentinelRequests
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:30:00"), time.Second, "blog-web", "GET", bad, 200, "1.1.1.1"),
		reqLine(reqAt("00:31:00"), time.Second, "blog-web", "GET", "/x", 500, sentinelRequests),
		// A truncated line and a line of another shape holding the sentinel.
		`{"DownstreamStatus":200,"StartUTC":"2026-10-25T00:32:00Z","RequestPath":"` + sentinelRequests,
		sentinelRequests + " plain text",
	}, wrote("00:39:50"))

	ctx := context.Background()
	notices := collectRequestNotices(t, e)
	var errs []string
	for _, q := range []RequestQuery{
		{Range: time.Hour},
		{Range: time.Hour, Path: bad},
		{Range: time.Hour, Client: sentinelRequests},
		{Range: time.Hour, Client: strings.Repeat(sentinelRequests, 4)}, // too long: a refusal
		{Range: time.Hour, Path: sentinelRequests},                      // no leading slash: a refusal
		{Range: time.Hour, Method: sentinelRequests},                    // not a method: a refusal
		{Range: time.Hour, Service: sentinelRequests},                   // not a service: a refusal
	} {
		read, err := e.svc.ReadRequests(ctx, q)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		// The path is in the read as the text it is, and nowhere else.
		for _, r := range read.Requests {
			if strings.Contains(r.Path, "?") && r.Path != bad {
				t.Errorf("path = %q", r.Path)
			}
		}
	}
	e.nomad.FailFS(403)
	_, err := e.svc.ReadRequests(ctx, RequestQuery{Range: time.Hour})
	e.nomad.FailFS(0)
	if err == nil {
		t.Fatal("a failing file system gave no error")
	}
	errs = append(errs, err.Error())

	if len(errs) != 5 {
		t.Errorf("%d errors, want 4 refusals and the file system's: %q", len(errs), errs)
	}
	for _, msg := range errs {
		if strings.Contains(msg, sentinelRequests) {
			t.Errorf("an error holds the sentinel: %q", msg)
		}
	}
	if strings.Contains(logs.String(), sentinelRequests) {
		t.Errorf("the server's log holds the sentinel: %s", logs.String())
	}
	for _, n := range notices.snapshot() {
		if strings.Contains(n, sentinelRequests) {
			t.Errorf("a notice holds the sentinel: %q", n)
		}
	}
}

// requestNotices records the ChangeRequests notices as "<app>" strings.
type requestNotices struct {
	mu   sync.Mutex
	apps []string
	more []string // any other kind, as "<kind>:<app>"
}

func collectRequestNotices(t *testing.T, e *env) *requestNotices {
	t.Helper()
	ch, unsubscribe := e.svc.Subscribe("")
	t.Cleanup(unsubscribe)
	n := &requestNotices{}
	go func() {
		for c := range ch {
			n.mu.Lock()
			if c.What == ChangeRequests {
				n.apps = append(n.apps, c.AppID)
			} else {
				n.more = append(n.more, string(c.What)+":"+c.AppID)
			}
			n.mu.Unlock()
		}
	}()
	return n
}

func (n *requestNotices) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.apps)
}

func (n *requestNotices) wait(t *testing.T, count int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := n.snapshot(); len(got) >= count {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d requests notices within 5s, want %d", len(n.snapshot()), count)
	return nil
}

// A watched place follows Traefik's stdout and tells the pages which app was
// asked; nothing of the line is in the notice or the log, a request to another
// place or to nobody's service is no notice, and the follower ends with the watch.
func TestWatchRequestsNotice(t *testing.T) {
	e, logs := liveEnv(t)
	e.nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	e.nomad.SetCopies("traefik", copyStub(copyTraefik, "traefik", "running", readStart))
	if _, err := e.svc.CreateProject(context.Background(), "shop", ""); err != nil {
		t.Fatal(err)
	}
	saveApp(t, e, appWith("shop--production--web", 1))
	e.svc.SetLogWatchGaps(50*time.Millisecond, time.Hour)
	connected(t, e)
	notices := collectRequestNotices(t, e)

	done := e.svc.WatchRequests("", "")
	waitStreams(t, e, 1) // Traefik's stdout alone
	now := time.Now()

	e.nomad.WriteLog(copyTraefik, "stdout", reqLine(now, time.Millisecond, "blog-web", "GET", "/x?token="+sentinelRequests, 200, sentinelRequests)+"\n")
	if got := notices.wait(t, 1); got[0] != "blog" {
		t.Errorf("notice for %q, want blog", got[0])
	}
	// Another place's app, an unowned service, no router and noise: no notice.
	e.nomad.WriteLog(copyTraefik, "stdout",
		reqLine(now, time.Millisecond, "shop--production--web-web", "GET", "/", 200, "1.1.1.1")+"\n"+
			reqLine(now, time.Millisecond, "caravana@file", "GET", "/", 200, "1.1.1.1")+"\n"+
			reqLine(now, time.Millisecond, "", "GET", "/", 404, "1.1.1.1")+"\n"+
			sentinelRequests+" noise\n")
	e.nomad.WriteLog(copyTraefik, "stdout", reqLine(now, time.Millisecond, "docs-web", "GET", "/", 200, "1.1.1.1")+"\n")
	if got := notices.wait(t, 2); got[1] != "docs" {
		t.Errorf("second notice for %q, want docs", got[1])
	}
	time.Sleep(150 * time.Millisecond)
	if got := notices.snapshot(); len(got) != 2 {
		t.Errorf("notices = %v, want blog and docs only", got)
	}

	done()
	done() // safe to call again
	waitStreams(t, e, 0)
	e.nomad.WriteLog(copyTraefik, "stdout", reqLine(now, time.Millisecond, "blog-web", "GET", "/", 200, "1.1.1.1")+"\n")
	time.Sleep(200 * time.Millisecond)
	if n := len(notices.snapshot()); n != 2 {
		t.Errorf("%d notices after the watch ended, want none more (2 in all)", n)
	}
	if strings.Contains(logs.String(), sentinelRequests) {
		t.Errorf("the server's log holds the sentinel: %s", logs.String())
	}
	notices.mu.Lock()
	defer notices.mu.Unlock()
	for _, m := range notices.more {
		if strings.Contains(m, sentinelRequests) {
			t.Errorf("a notice holds the sentinel: %q", m)
		}
	}
}

// Two pages of one place share one follower, and a Traefik that restarts (a new
// allocation) is followed again.
func TestWatchRequestsSharedAndRestart(t *testing.T) {
	e, _ := liveEnv(t)
	e.nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	e.nomad.SetCopies("traefik", copyStub(copyTraefik, "traefik", "running", readStart))
	e.svc.SetLogWatchGaps(20*time.Millisecond, 50*time.Millisecond)
	notices := collectRequestNotices(t, e)

	d1 := e.svc.WatchRequests("", "")
	d2 := e.svc.WatchRequests("default", "production")
	waitStreams(t, e, 1)
	d1()
	time.Sleep(100 * time.Millisecond)
	waitStreams(t, e, 1) // one page is still open

	// Traefik restarts: the old allocation ends and a new one runs.
	const second = "8b2d0f33-0000-4000-8000-0000000000bb"
	next := copyStub(second, "traefik", "running", time.Now())
	next.CreateTime = time.Now().UnixNano()
	e.nomad.SetCopies("traefik", copyStub(copyTraefik, "traefik", "complete", readStart), next)
	waitFor(t, "the new allocation to be followed", func() bool {
		return e.nomad.OpenLogStreams() == 1 && followsCopy(e, second)
	})
	e.nomad.WriteLog(second, "stdout", reqLine(time.Now(), time.Millisecond, "blog-web", "GET", "/", 200, "1.1.1.1")+"\n")
	notices.wait(t, 1)

	d2()
	waitStreams(t, e, 0)
}

// followsCopy says whether a log request was made for the allocation.
func followsCopy(e *env, id string) bool {
	for _, r := range e.nomad.LogRequests() {
		if r.Copy == id {
			return true
		}
	}
	return false
}

// A read the deadline stops answers what it has and says it didn't finish.
func TestReadRequestsDeadline(t *testing.T) {
	e := reqEnv(t)
	e.nomad.AddLogFile(copyTraefik, "stdout", 0, []string{
		reqLine(reqAt("00:30:00"), time.Second, "blog-web", "GET", "/a", 200, "1.1.1.1"),
	}, wrote("00:39:50"))
	e.svc.logDeadline = time.Nanosecond
	read, err := e.svc.ReadRequests(context.Background(), RequestQuery{})
	if err != nil {
		t.Fatalf("ReadRequests: %v", err)
	}
	if read.Coverage.Complete || read.Coverage.Timed {
		t.Errorf("coverage = %+v, want incomplete and not timed", read.Coverage)
	}
}
