package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 025: getMetrics, getAppMetrics and getHost over rows written the
// way the sampler writes them, and the needs-you list's disk item. Every body
// is checked against the contract.

// monEnv is an environment whose sampler never ticks (its gap is an hour), so
// the rows a test writes are the only ones.
func monEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWith(t, "", func(s *service.Service) { s.SetMetricsSampleGap(time.Hour) })
}

// monNow is the minute the seeded rows end at. The service's clock is the real
// one in these tests, so every row sits just before now.
func monNow() time.Time { return time.Now().UTC().Truncate(time.Minute) }

// seedMinutes writes minute rows for app whoami's web service: n minutes
// ending at end, each 60 requests (3 of them 5xx) in the 50-100 ms bucket,
// 100 MB used of a 256 MB limit, 150 MHz of 200 reserved, one copy.
func seedMinutes(t *testing.T, e *testEnv, appID string, end time.Time, n int, memUsed int64) {
	t.Helper()
	ctx := context.Background()
	layout, err := e.Store.EnsureLayout(ctx, metrics.TraefikBounds)
	if err != nil {
		t.Fatal(err)
	}
	series, err := e.Store.EnsureSeries(ctx, appID, "web")
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.MetricsSample
	for i := range n {
		ts := end.Add(-time.Duration(n-1-i) * time.Minute)
		secs, reqs, errs := int64(60), int64(60), int64(3)
		buckets := "0,0,0,0,60"
		rows = append(rows, store.MetricsSample{
			Series: series, TS: ts, Minutes: 1, HTTPSecs: &secs, Requests: &reqs, Errors: &errs, Layout: &layout, Buckets: &buckets,
			CPUMHz: new(int64(150)), CPUReservedMHz: new(int64(200)),
			MemoryMB: new(memUsed), MemoryLimitMB: new(int64(256)), MemoryReservedMB: new(int64(128)), Copies: new(int64(1)),
			Restarts: new(int64(0)),
		})
	}
	if err := e.Store.WriteMetricsTick(ctx, store.MetricsTick{At: end.Add(-time.Duration(n) * time.Minute), Minute: store.MetricsRows{Samples: rows}}); err != nil {
		t.Fatal(err)
	}
}

// seedHost writes a host sample and two filesystems at end.
func seedHost(t *testing.T, e *testEnv, end time.Time, rootUsedMB int64) {
	t.Helper()
	rows := store.MetricsRows{
		Hosts: []store.HostSample{{
			Node: "n1", TS: end, MemoryUsedMB: new(int64(1946)), MemoryReservedMB: new(int64(1312)), MemoryTotalMB: new(int64(3891)),
			CPUUsedMHz: new(int64(1210)), CPUReservedMHz: new(int64(1730)), CPUTotalMHz: new(int64(4000)), CPUCores: new(int64(4)),
		}},
		Disks: []store.DiskSample{
			{Node: "n1", TS: end, Mount: "/", Holds: "nomad,docker", UsedMB: rootUsedMB, TotalMB: 40960, FreeMB: 40960 - rootUsedMB},
			{Node: "n1", TS: end, Mount: "/data", Holds: "nomad", UsedMB: 1000, TotalMB: 10000, FreeMB: 9000},
		},
	}
	if err := e.Store.WriteMetricsTick(context.Background(), store.MetricsTick{Minute: rows}); err != nil {
		t.Fatal(err)
	}
}

func getJSON(t *testing.T, e *testEnv, path, schema string) map[string]any {
	t.Helper()
	status, header, raw := call(t, http.MethodGet, e.URL+path, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, status, raw)
	}
	if header.Get("Content-Type") != "application/json" {
		t.Errorf("GET %s Content-Type = %q", path, header.Get("Content-Type"))
	}
	if cc := header.Get("Cache-Control"); cc != "" {
		t.Errorf("GET %s Cache-Control = %q, want none (nothing here is a secret)", path, cc)
	}
	checkSchema(t, loadContract(t), raw, schema)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func obj(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s = %v (%T), want an object", what, v, v)
	}
	return m
}

func list(t *testing.T, v any, what string) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("%s = %v (%T), want a list", what, v, v)
	}
	return l
}

func TestGetMetricsOfAnEnvironment(t *testing.T) {
	e := monEnv(t)
	seedHealthy(t, e, "whoami", 1, 7)
	end := monNow()
	seedMinutes(t, e, "whoami", end, 30, 100)

	body := getJSON(t, e, "/v1/metrics?range=1h", "Metrics")
	if body["range"] != "1h" || body["step"] != 60.0 {
		t.Errorf("range %v step %v, want 1h and 60", body["range"], body["step"])
	}
	points := list(t, body["points"], "points")
	if len(points) != 60 {
		t.Fatalf("points = %d, want 60", len(points))
	}
	series := obj(t, body["series"], "series")
	if series["hasHttp"] != true {
		t.Errorf("hasHttp = %v, want true (whoami has a domain)", series["hasHttp"])
	}
	for _, name := range []string{"requests", "errors", "p95", "memory"} {
		s := obj(t, series[name], name)
		if got := len(list(t, s["points"], name+".points")); got != 60 {
			t.Errorf("%s has %d points, want 60", name, got)
		}
	}
	if got := len(list(t, series["memoryLimit"], "memoryLimit")); got != 60 {
		t.Errorf("memoryLimit has %d entries, want 60", got)
	}

	// The newest point, field by field: 60 requests in 60 s is 1 per second, 3 of 60 is 5 percent,
	// the bucket (50 ms, 100 ms] gives a slowest 5 % inside it, memory is 100 MB. The sampler
	// has run for under a day, so there is no band.
	pt := func(name string, i int) map[string]any {
		return obj(t, list(t, obj(t, series[name], name)["points"], name)[i], name+" point")
	}
	last := pt("requests", 59)
	if last["value"] != 1.0 || last["days"] != 0.0 {
		t.Errorf("requests point = %v, want value 1 and days 0", last)
	}
	for _, f := range []string{"low", "high", "usual"} {
		if v, ok := last[f]; !ok || v != nil {
			t.Errorf("requests point %s = %v (present %v), want null while the baseline is building", f, v, ok)
		}
	}
	if v := pt("errors", 59)["value"]; v != 5.0 {
		t.Errorf("errors value = %v, want 5", v)
	}
	if v, _ := pt("p95", 59)["value"].(float64); v <= 50 || v > 100 {
		t.Errorf("p95 value = %v, want inside (50, 100]", v)
	}
	if v := pt("memory", 59)["value"]; v != 100.0 {
		t.Errorf("memory value = %v, want 100", v)
	}
	if v := list(t, series["memoryLimit"], "memoryLimit")[59]; v != 256.0 {
		t.Errorf("memoryLimit newest = %v, want 256", v)
	}
	// Before the first row the points are gaps: null, never 0.
	if v, ok := pt("requests", 0)["value"]; !ok || v != nil {
		t.Errorf("first requests point = %v (present %v), want null", v, ok)
	}
	if v := list(t, series["memoryLimit"], "memoryLimit")[0]; v != nil {
		t.Errorf("first memoryLimit = %v, want null", v)
	}
	if now := obj(t, series["requests"], "requests"); now["now"] != 1.0 || now["over"] != false || now["usual"] != nil || now["usualDays"] != 0.0 {
		t.Errorf("requests summary = %v", now)
	}

	// The service row.
	services := list(t, body["services"], "services")
	if len(services) != 1 {
		t.Fatalf("services = %v", services)
	}
	sv := obj(t, services[0], "service")
	want := map[string]any{
		"appId": "whoami", "appName": "whoami", "service": "web", "state": "healthy", "level": nil,
		"hasDomain": true, "requests": 1.0, "errors": 5.0, "p95Over": false, "usualP95": nil,
		"cpuReservedMhz": 200.0, "memoryMb": 100.0, "memoryLimitMb": 256.0, "restarts": 0.0,
		"unusual": false, "distance": nil, "httpShared": nil,
	}
	for k, w := range want {
		if got, ok := sv[k]; !ok || got != w {
			t.Errorf("service %s = %v (present %v), want %v", k, got, ok, w)
		}
	}
	if cpu := list(t, sv["cpu"], "cpu"); len(cpu) != 60 || cpu[59] != 150.0 || cpu[0] != nil {
		t.Errorf("cpu = %d entries, newest %v, first %v; want 60, 150 and null", len(cpu), cpu[len(cpu)-1], cpu[0])
	}
	if p95, _ := sv["p95"].(float64); p95 <= 50 || p95 > 100 {
		t.Errorf("service p95 = %v, want inside (50, 100]", sv["p95"])
	}

	// The baseline and the sources: the sampler's first row was 30 minutes ago and it has never ticked.
	baseline := obj(t, body["baseline"], "baseline")
	if baseline["state"] != "building" || baseline["fullDays"] != 0.0 || baseline["since"] == nil {
		t.Errorf("baseline = %v", baseline)
	}
	sources := obj(t, body["sources"], "sources")
	for _, name := range []string{"traefik", "nomad"} {
		s := obj(t, sources[name], name)
		if s["state"] != "unknown" || s["at"] != nil {
			t.Errorf("source %s = %v, want unknown with a null time before the first tick", name, s)
		}
	}
	if issues := list(t, body["issues"], "issues"); len(issues) != 0 {
		t.Errorf("issues = %v, want none", issues)
	}
	if deploys := list(t, body["deploys"], "deploys"); len(deploys) != 1 {
		t.Errorf("deploys = %v, want the seeded healthy deployment", deploys)
	} else if d := obj(t, deploys[0], "deploy"); d["appId"] != "whoami" || d["deploymentId"] != "whoami-d1" || d["version"] != 7.0 || d["appName"] != "whoami" {
		t.Errorf("deploy = %v", d)
	}
}

func TestGetMetricsRangesAndSorts(t *testing.T) {
	e := monEnv(t)
	seedHealthy(t, e, "whoami", 1, 7)
	seedHealthy(t, e, "blog", 1, 3)
	end := monNow()
	seedMinutes(t, e, "whoami", end, 10, 100)
	seedMinutes(t, e, "blog", end, 10, 200)

	for name, step := range map[string]float64{"1h": 60, "6h": 300, "24h": 600, "7d": 3600} {
		body := getJSON(t, e, "/v1/metrics?range="+name, "Metrics")
		if body["range"] != name || body["step"] != step {
			t.Errorf("range %s: answered %v with step %v, want step %v", name, body["range"], body["step"], step)
		}
	}
	if body := getJSON(t, e, "/v1/metrics", "Metrics"); body["range"] != "6h" {
		t.Errorf("default range = %v, want 6h", body["range"])
	}
	order := func(path string) []string {
		var ids []string
		for _, s := range list(t, getJSON(t, e, path, "Metrics")["services"], "services") {
			ids = append(ids, obj(t, s, "service")["appId"].(string))
		}
		return ids
	}
	if got := order("/v1/metrics?sort=memory"); !slices.Equal(got, []string{"blog", "whoami"}) {
		t.Errorf("sort=memory = %v, want blog (200 MB) first", got)
	}
	if got := order("/v1/metrics?sort=unusual"); !slices.Equal(got, []string{"blog", "whoami"}) {
		t.Errorf("sort=unusual = %v, want by name when nothing is unusual", got)
	}
	if got := order("/v1/metrics?sort=traffic&range=1h"); len(got) != 2 {
		t.Errorf("sort=traffic = %v", got)
	}
}

// An app above 90 % of its memory limit is an issue: its facts, its row's mark,
// and the app's own read agrees with the environment's.
func TestGetMetricsIssue(t *testing.T) {
	e := monEnv(t)
	seedHealthy(t, e, "whoami", 1, 7)
	seedHealthy(t, e, "blog", 1, 3)
	end := monNow()
	seedMinutes(t, e, "whoami", end, 15, 100)
	seedMinutes(t, e, "blog", end, 15, 250) // 250 of 256 MB: 97.7 %

	body := getJSON(t, e, "/v1/metrics?range=1h", "Metrics")
	issues := list(t, body["issues"], "issues")
	if len(issues) != 1 {
		t.Fatalf("issues = %v, want blog's memory", issues)
	}
	is := obj(t, issues[0], "issue")
	for k, w := range map[string]any{"appId": "blog", "appName": "blog", "service": "web", "kind": "memory", "level": "warning",
		"limitMb": 256.0, "usual": nil, "sinceCapped": false} {
		if got, ok := is[k]; !ok || got != w {
			t.Errorf("issue %s = %v (present %v), want %v", k, got, ok, w)
		}
	}
	if v, _ := is["value"].(float64); v < 97 || v > 98 {
		t.Errorf("issue value = %v, want the percent of the limit", is["value"])
	}
	if d, _ := is["distance"].(float64); d < 1 {
		t.Errorf("issue distance = %v, want past 1", is["distance"])
	}
	win := obj(t, is["window"], "window")
	if win["requests"] != 600.0 || win["errors"] != 30.0 || win["requestsPerSecond"] != 1.0 {
		t.Errorf("window = %v", win)
	}
	// The seeded deployments ended just now, after the issue began 15 minutes ago: the first one that
	// ended after it, named by relation "before", which has no minutes. The `since` is in the range.
	near := obj(t, is["deployNear"], "deployNear")
	if near["appId"] != "whoami" && near["appId"] != "blog" || near["relation"] != "before" || near["deploymentId"] == nil || near["version"] == nil || near["endedAt"] == nil {
		t.Errorf("deployNear = %v", near)
	}
	if _, has := near["minutes"]; has {
		t.Errorf("deployNear has minutes = %v; only relation after has them", near["minutes"])
	}
	if since, _ := is["since"].(string); since == "" || since >= end.Format(time.RFC3339) {
		t.Errorf("since = %v, want before the last sample %s", is["since"], end.Format(time.RFC3339))
	}
	services := list(t, body["services"], "services")
	first := obj(t, services[0], "service")
	if first["appId"] != "blog" || first["state"] != "unusual" || first["level"] != "warning" || first["unusual"] != true || first["distance"] == nil {
		t.Errorf("first service = %v, want blog unusual first", first)
	}

	app := getJSON(t, e, "/v1/apps/blog/metrics?range=1h", "Metrics")
	if got := list(t, app["services"], "services"); len(got) != 1 || obj(t, got[0], "service")["appId"] != "blog" {
		t.Errorf("app services = %v, want blog's only", got)
	}
	if got := list(t, app["issues"], "issues"); len(got) != 1 {
		t.Errorf("app issues = %v", got)
	}
	// The app's series are its own totals: blog's memory, not whoami's.
	mem := obj(t, obj(t, app["series"], "series")["memory"], "memory")
	if mem["now"] != 250.0 {
		t.Errorf("app memory now = %v, want 250", mem["now"])
	}
	envMem := obj(t, obj(t, body["series"], "series")["memory"], "memory")
	if envMem["now"] != 350.0 {
		t.Errorf("environment memory now = %v, want 350 (100 + 250)", envMem["now"])
	}
}

func TestGetMetricsNothingSampled(t *testing.T) {
	e := monEnv(t)
	seedHealthy(t, e, "whoami", 1, 7)
	body := getJSON(t, e, "/v1/metrics?range=1h", "Metrics")
	series := obj(t, body["series"], "series")
	for _, name := range []string{"requests", "errors", "p95", "memory"} {
		s := obj(t, series[name], name)
		if s["now"] != nil || s["unusualSince"] != nil {
			t.Errorf("%s = now %v since %v, want null", name, s["now"], s["unusualSince"])
		}
		for i, p := range list(t, s["points"], name) {
			if v := obj(t, p, "point")["value"]; v != nil {
				t.Fatalf("%s point %d = %v, want a gap (null)", name, i, v)
			}
		}
	}
	baseline := obj(t, body["baseline"], "baseline")
	if baseline["since"] != nil || baseline["state"] != "building" {
		t.Errorf("baseline = %v, want never sampled", baseline)
	}
	sv := obj(t, list(t, body["services"], "services")[0], "service")
	for _, k := range []string{"requests", "errors", "p95", "memoryMb", "memoryLimitMb", "cpuReservedMhz"} {
		if v, ok := sv[k]; !ok || v != nil {
			t.Errorf("service %s = %v (present %v), want null", k, v, ok)
		}
	}
}

func TestGetMetricsErrors(t *testing.T) {
	e := monEnv(t)
	seedHealthy(t, e, "whoami", 1, 7)
	get := func(path string) (int, []byte) {
		status, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		return status, raw
	}
	for _, tc := range []struct {
		path   string
		status int
		code   string
		field  string
	}{
		{"/v1/metrics?project=nope", http.StatusNotFound, "project_not_found", "project"},
		{"/v1/metrics?project=default&environment=nope", http.StatusNotFound, "environment_not_found", "environment"},
		{"/v1/metrics?range=2h", http.StatusUnprocessableEntity, "invalid_field", "range"},
		{"/v1/metrics?sort=name", http.StatusUnprocessableEntity, "invalid_field", "sort"},
		{"/v1/apps/nope/metrics", http.StatusNotFound, "app_not_found", "app"},
		{"/v1/apps/whoami/metrics?range=1d", http.StatusUnprocessableEntity, "invalid_field", "range"},
		{"/v1/apps/whoami/metrics?sort=hot", http.StatusUnprocessableEntity, "invalid_field", "sort"},
	} {
		status, raw := get(tc.path)
		wantError(t, "GET "+tc.path, status, raw, tc.status, tc.code, tc.field)
	}
	// The place's default is Default > production.
	if status, raw := get("/v1/metrics?project=default&environment=production&range=1h"); status != http.StatusOK {
		t.Errorf("explicit default place = %d %s", status, raw)
	}
	// No credentials: the uniform 401.
	status, _, raw := call(t, http.MethodGet, e.RawURL+"/v1/metrics", "", "")
	wantError(t, "GET /v1/metrics without a token", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
	status, _, raw = call(t, http.MethodGet, e.RawURL+"/v1/host", "", "")
	wantError(t, "GET /v1/host without a token", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
}

func TestGetHost(t *testing.T) {
	e := monEnv(t)
	seedHealthy(t, e, "whoami", 1, 7)
	end := monNow()
	seedMinutes(t, e, "whoami", end, 5, 100)
	seedHost(t, e, end, 18432)

	body := getJSON(t, e, "/v1/host", "Host")
	mem := obj(t, body["memory"], "memory")
	if mem["used"] != 1946.0 || mem["reserved"] != 1312.0 || mem["total"] != 3891.0 {
		t.Errorf("memory = %v", mem)
	}
	cpu := obj(t, body["cpu"], "cpu")
	if cpu["used"] != 1210.0 || cpu["reserved"] != 1730.0 || cpu["total"] != 4000.0 || cpu["cores"] != 4.0 {
		t.Errorf("cpu = %v", cpu)
	}
	disks := list(t, body["disks"], "disks")
	if len(disks) != 2 {
		t.Fatalf("disks = %v", disks)
	}
	root := obj(t, disks[0], "disk")
	if disks[0].(map[string]any)["mount"] != "/" {
		root = obj(t, disks[1], "disk")
	}
	for k, w := range map[string]any{"mount": "/", "usedMb": 18432.0, "totalMb": 40960.0, "freeMb": 22528.0, "usedPercent": 45.0, "state": "ok"} {
		if root[k] != w {
			t.Errorf("disk %s = %v, want %v", k, root[k], w)
		}
	}
	if holds := list(t, root["holds"], "holds"); len(holds) != 2 || holds[0] != "nomad" || holds[1] != "docker" {
		t.Errorf("holds = %v", holds)
	}
	room := obj(t, body["room"], "room")
	// 3891-1312 = 2579 MB free; whoami reserves 128 MB: 20 fit by memory, and CPU leaves 2270 / 200 = 11.
	for k, w := range map[string]any{"count": 11.0, "likeApp": "whoami", "likeAppName": "whoami", "memoryMb": 128.0,
		"cpuMhz": 200.0, "limitedBy": "cpu", "freeMemoryMb": 2579.0} {
		if room[k] != w {
			t.Errorf("room %s = %v, want %v", k, room[k], w)
		}
	}
	hist := obj(t, body["history"], "history")
	if n := len(list(t, hist["points"], "points")); n != 168 {
		t.Errorf("history has %d points, want 168", n)
	}
	if n := len(list(t, hist["memoryUsed"], "memoryUsed")); n != 168 {
		t.Errorf("memoryUsed has %d entries, want 168", n)
	}
	if body["sampledAt"] != end.Format(time.RFC3339) || body["stale"] != false {
		t.Errorf("sampledAt %v stale %v, want %s and false", body["sampledAt"], body["stale"], end.Format(time.RFC3339))
	}
	if b := obj(t, body["baseline"], "baseline"); b["state"] != "building" {
		t.Errorf("baseline = %v", b)
	}

	// A sample older than 5 minutes is the last known: stale.
	old := monNow().Add(-20 * time.Minute)
	e2 := monEnv(t)
	seedHost(t, e2, old, 1000)
	if b := getJSON(t, e2, "/v1/host", "Host"); b["stale"] != true || b["sampledAt"] != old.Format(time.RFC3339) {
		t.Errorf("stale host = sampledAt %v stale %v", b["sampledAt"], b["stale"])
	}
}

func TestGetHostNeverSampled(t *testing.T) {
	e := monEnv(t)
	body := getJSON(t, e, "/v1/host", "Host")
	if body["sampledAt"] != nil || body["stale"] != false {
		t.Errorf("sampledAt %v stale %v", body["sampledAt"], body["stale"])
	}
	for _, name := range []string{"memory", "cpu"} {
		for k, v := range obj(t, body[name], name) {
			if v != nil {
				t.Errorf("%s.%s = %v, want null", name, k, v)
			}
		}
	}
	if disks := list(t, body["disks"], "disks"); len(disks) != 0 {
		t.Errorf("disks = %v", disks)
	}
	for k, v := range obj(t, body["room"], "room") {
		if v != nil {
			t.Errorf("room.%s = %v, want null", k, v)
		}
	}
	hist := obj(t, body["history"], "history")
	for _, v := range list(t, hist["memoryUsed"], "memoryUsed") {
		if v != nil {
			t.Fatalf("memoryUsed has %v, want only gaps", v)
		}
	}
}

// No yardstick (nothing is running): the count is null and what is free is
// still said.
func TestGetHostRoomWithNothingRunning(t *testing.T) {
	e := monEnv(t)
	seedHost(t, e, monNow(), 1000)
	room := obj(t, getJSON(t, e, "/v1/host", "Host")["room"], "room")
	if room["count"] != nil || room["likeApp"] != nil || room["limitedBy"] != nil || room["freeMemoryMb"] != 2579.0 {
		t.Errorf("room = %v, want a null count and 2579 MB free", room)
	}
}

// A deploy token may not read any of it (default deny), not even for its own app.
func TestMonitoringDeniedToDeployTokens(t *testing.T) {
	e := monEnv(t)
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	for _, path := range []string{"/v1/metrics", "/v1/host", "/v1/apps/shop--staging--web/metrics", "/v1/apps/web/metrics"} {
		status, _, raw := callWith(t, auth, http.MethodGet, e.RawURL+path, "", "")
		wantError(t, "GET "+path+" with a deploy token", status, raw, http.StatusForbidden, codeTokenScope, "")
	}
}

// The disk item (feature 025): host-level, with the facts of the filesystem.
func TestAttentionDiskNearlyFull(t *testing.T) {
	e := monEnv(t)
	end := monNow()
	seedHost(t, e, end, 36864) // 90 % of 40960 MB used... of used+free, as df says

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/attention", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /v1/attention = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "Attention")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["disksUnknown"] != false {
		t.Errorf("disksUnknown = %v, want false: the host was sampled a moment ago", body["disksUnknown"])
	}
	var disk map[string]any
	for _, it := range list(t, body["items"], "items") {
		if m := obj(t, it, "item"); m["kind"] == "disk_nearly_full" {
			disk = m
		}
	}
	if disk == nil {
		t.Fatalf("no disk_nearly_full item in %s", raw)
	}
	if disk["mount"] != "/" || disk["level"] != "warning" || disk["usedPercent"] != 90.0 || disk["freeBytes"] != float64(4096<<20) {
		t.Errorf("disk item = %v", disk)
	}
	if disk["detail"] != "The disk at / is 90% full: 4.0 GB left." {
		t.Errorf("detail = %v, want the one sentence the console and the notifications use", disk["detail"])
	}
	if holds := list(t, disk["holds"], "holds"); len(holds) != 2 || holds[0] != "nomad" || holds[1] != "docker" {
		t.Errorf("holds = %v", holds)
	}
	for _, k := range []string{"appId", "project", "environment", "appName", "service"} {
		if _, has := disk[k]; has {
			t.Errorf("disk item has %s = %v; it is about the host", k, disk[k])
		}
	}
	if !strings.HasPrefix(disk["at"].(string), end.Format("2006-01-02T15:04")) {
		t.Errorf("at = %v, want the sample's minute %s", disk["at"], end.Format(time.RFC3339))
	}

	// Not nearly full: no item. Never sampled: disksUnknown.
	e2 := monEnv(t)
	seedHost(t, e2, monNow(), 18432)
	b2 := getJSON(t, e2, "/v1/attention", "Attention")
	if items := list(t, b2["items"], "items"); len(items) != 0 || b2["disksUnknown"] != false {
		t.Errorf("a 45 %% disk gave %v, disksUnknown %v", items, b2["disksUnknown"])
	}
	b3 := getJSON(t, monEnv(t), "/v1/attention", "Attention")
	if b3["disksUnknown"] != true {
		t.Errorf("disksUnknown = %v with no host sample, want true", b3["disksUnknown"])
	}
}
