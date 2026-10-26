package service

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/metrics/metricstest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// samplerLab is a service on the fakes with a Traefik source and a node, whose
// sampler ticks when the test says so (startServiceLogging gives it an hour's
// gap, so it never ticks on its own).
type samplerLab struct {
	*env
	traefik *metricstest.Source
}

// labBase is the whole minute the lab's ticks count from: tick n runs at
// labBase + n minutes + 2 s, and writes the row of the minute before it.
var labBase = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func newSamplerLab(t *testing.T, log *slog.Logger) *samplerLab {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.nomad.Succeed()
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets, log)
	e.nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	e.nomad.SetHostStats(deploytest.LabHostStats())

	l := &samplerLab{env: e, traefik: metricstest.New(shopText(1e9, 0, 0, nil))}
	e.svc.metricsGap = time.Minute
	e.svc.sampler.traefik = l.traefik
	e.svc.sampler.dockerRoot = "/var/lib/docker"
	return l
}

// tick runs the sampler's tick n: at labBase + n minutes + 2 s.
func (l *samplerLab) tick(t *testing.T, n int) {
	t.Helper()
	if err := l.svc.sampleTick(context.Background(), labBase.Add(time.Duration(n)*time.Minute+2*time.Second)); err != nil {
		t.Fatalf("tick %d: %v", n, err)
	}
}

// rowTS is the time of the row tick n writes.
func rowTS(n int) time.Time { return labBase.Add(time.Duration(n-1) * time.Minute) }

// minute is the stored minute row of a service at ts, which must exist.
func (l *samplerLab) minute(t *testing.T, appID, service string, ts time.Time) store.MetricsSample {
	t.Helper()
	id, err := l.store.EnsureSeries(context.Background(), appID, service)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := l.store.MinuteRows(context.Background(), id, ts, ts.Add(time.Second))
	if err != nil || len(rows) != 1 {
		t.Fatalf("minute rows of %s/%s at %s = %v, %v; want one", appID, service, ts.Format("15:04:05"), rows, err)
	}
	return rows[0]
}

// save stores an app made of whoami's web service (with its domain) under id.
func (l *samplerLab) save(t *testing.T, id, host string) {
	t.Helper()
	a := appWith(id, 1)
	a.Services[0].Domains = []app.Domain{{Host: host, Port: "http"}}
	saveApp(t, l.env, a)
}

var traefikBounds = strings.Split(metrics.TraefikBounds, ",")

// shopText is Traefik's numbers for the service shop-web: ok requests with the
// cumulative latency counts cum (one per bound, then +Inf; nil for none), and
// failed ones.
func shopText(start float64, ok, failed int, cum []int) string {
	return trafficText("shop-web@nomad", start, ok, failed, cum)
}

func trafficText(service string, start float64, ok, failed int, cum []int) string {
	return metricstest.Text(start, traefikBounds,
		metricstest.Counter{Service: service, Code: "200", Method: "GET", Protocol: "http", Requests: ok, Buckets: cum},
		metricstest.Counter{Service: service, Code: "500", Method: "GET", Protocol: "http", Requests: failed})
}

// samplerResources is a copy's reservation by task: MHz, MB, MB ceiling.
func samplerResources(tasks map[string][3]int) *api.AllocatedResources {
	out := &api.AllocatedResources{Tasks: map[string]*api.AllocatedTaskResources{}}
	for name, r := range tasks {
		out.Tasks[name] = &api.AllocatedTaskResources{
			Cpu:    api.AllocatedCpuResources{CpuShares: int64(r[0])},
			Memory: api.AllocatedMemoryResources{MemoryMB: int64(r[1]), MemoryMaxMB: int64(r[2])},
		}
	}
	return out
}

// samplerCopy is a running copy of shop's web that has restarted n times.
func samplerCopy(id string, restarts int, events ...*api.TaskEvent) api.AllocationListStub {
	return api.AllocationListStub{
		ID: id, TaskGroup: "web", JobVersion: 1, NodeID: "node-1", NodeName: "lab",
		ClientStatus: api.AllocClientStatusRunning, DesiredStatus: api.AllocDesiredStatusRun,
		TaskStates: map[string]*api.TaskState{"web": {
			State: "running", StartedAt: time.Now(), Restarts: uint64(restarts), Events: events}},
		AllocatedResources: samplerResources(map[string][3]int{"web": {100, 64, 256}}),
	}
}

func wantInt(t *testing.T, what string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %s, want %d", what, showInt(got), want)
	}
}

func wantNull(t *testing.T, what string, got *int64) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %d, want NULL", what, *got)
	}
}

func showInt(p *int64) string {
	if p == nil {
		return "NULL"
	}
	return strconv.FormatInt(*p, 10)
}

func TestSamplerTwoTicksGiveAMinute(t *testing.T) {
	l := newSamplerLab(t, discardLogger())
	l.save(t, "shop", "shop.example.com")
	l.save(t, "idle", "idle.example.com")
	l.nomad.SetCopies("shop", samplerCopy("c1", 1))
	l.nomad.SetCopies("idle", func() api.AllocationListStub {
		c := samplerCopy("i1", 0)
		c.TaskGroup = "web"
		return c
	}())
	l.nomad.SetStats("c1", statsFor("web", 12.4, 30))
	l.nomad.SetStats("i1", statsFor("web", 1, 5))
	if got := l.svc.SamplerSources(); got.Traefik != SamplerUnknown || got.Nomad != SamplerUnknown {
		t.Errorf("sources before a tick = %+v, want unknown", got)
	}
	notices, stop := l.svc.Subscribe("")
	defer stop()

	cum1 := []int{0, 0, 0, 0, 5, 8, 10, 10, 10, 10, 10, 10}
	l.traefik.Set(shopText(1e9, 10, 2, cum1))
	l.tick(t, 1)

	// The first minute after a start has CPU and memory, and no HTTP numbers
	// (nothing to subtract) and no restarts.
	first := l.minute(t, "shop", "web", rowTS(1))
	wantNull(t, "first http_secs", first.HTTPSecs)
	wantNull(t, "first requests", first.Requests)
	wantNull(t, "first errors", first.Errors)
	if first.Buckets != nil || first.Layout != nil {
		t.Errorf("first buckets = %v layout %v, want NULL", first.Buckets, first.Layout)
	}
	wantNull(t, "first restarts", first.Restarts)
	wantInt(t, "cpu", first.CPUMHz, 12)
	wantInt(t, "memory", first.MemoryMB, 30)
	wantInt(t, "cpu reserved", first.CPUReservedMHz, 100)
	wantInt(t, "memory reserved", first.MemoryReservedMB, 64)
	wantInt(t, "memory limit", first.MemoryLimitMB, 256)
	wantInt(t, "copies", first.Copies, 1)
	if first.Minutes != 1 {
		t.Errorf("minutes = %d, want 1", first.Minutes)
	}
	if got := l.svc.SamplerSources(); got.Traefik != SamplerOK || got.Nomad != SamplerOK || !got.At.Equal(labBase.Add(time.Minute+2*time.Second)) {
		t.Errorf("sources = %+v, want ok and ok at the tick", got)
	}

	// The host: the lab's numbers, reserved by Nomad's rule (the one copy).
	hosts, err := l.store.HostRows(context.Background(), store.MinuteRes, rowTS(1), rowTS(1).Add(time.Second))
	if err != nil || len(hosts) != 1 {
		t.Fatalf("host rows = %v, %v", hosts, err)
	}
	h := hosts[0]
	if h.Node != "node-1" {
		t.Errorf("host node = %q", h.Node)
	}
	wantInt(t, "host memory used", h.MemoryUsedMB, 1680)
	wantInt(t, "host memory total", h.MemoryTotalMB, 1949)
	wantInt(t, "host memory reserved", h.MemoryReservedMB, 128) // shop's copy and idle's, 64 each
	wantInt(t, "host cpu used", h.CPUUsedMHz, 485)
	wantInt(t, "host cpu total", h.CPUTotalMHz, 4000)
	wantInt(t, "host cpu reserved", h.CPUReservedMHz, 200)
	wantInt(t, "host cores", h.CPUCores, 2)
	disks, err := l.store.DiskRows(context.Background(), store.MinuteRes, rowTS(1), rowTS(1).Add(time.Second))
	if err != nil || len(disks) != 1 || disks[0].Mount != "/" || disks[0].Holds != "nomad,docker" {
		t.Fatalf("disk rows = %+v, %v; want / holding both", disks, err)
	}

	// The second tick, a minute later: 30 more answered requests, 3 more 5xx,
	// and their latency; one restart nobody asked for (one of the two asked).
	requested := &api.TaskEvent{Type: api.TaskRestartSignal, DisplayMessage: "User requested all tasks to restart"}
	l.nomad.SetCopies("shop", samplerCopy("c1", 3, requested))
	l.nomad.SetStats("c1", statsFor("web", 20, 40))
	cum2 := []int{0, 0, 0, 0, 15, 33, 40, 40, 40, 40, 40, 40}
	l.traefik.Set(shopText(1e9, 40, 5, cum2))
	l.tick(t, 2)

	second := l.minute(t, "shop", "web", rowTS(2))
	wantInt(t, "http_secs", second.HTTPSecs, 60)
	wantInt(t, "requests", second.Requests, 33)
	wantInt(t, "errors", second.Errors, 3)
	if second.Buckets == nil || *second.Buckets != "0,0,0,0,10,15,5" {
		t.Errorf("buckets = %v, want 0,0,0,0,10,15,5", second.Buckets)
	}
	if second.Layout == nil {
		t.Fatal("layout is NULL")
	}
	layouts, err := l.store.MetricsLayouts(context.Background())
	if err != nil || layouts[*second.Layout] != metrics.TraefikBounds {
		t.Errorf("layout %d = %q, %v; want the Traefik bounds", *second.Layout, layouts[*second.Layout], err)
	}
	wantInt(t, "restarts", second.Restarts, 1)
	wantInt(t, "memory", second.MemoryMB, 40)

	// A service with a domain that Traefik didn't list had no request: zeros.
	idle := l.minute(t, "idle", "web", rowTS(2))
	wantInt(t, "idle requests", idle.Requests, 0)
	wantInt(t, "idle errors", idle.Errors, 0)
	wantInt(t, "idle http_secs", idle.HTTPSecs, 60)
	if idle.Buckets != nil {
		t.Errorf("idle buckets = %v, want NULL", *idle.Buckets)
	}

	// Each tick told the open pages, with no data in the notice.
	var told int
	for _, c := range drain(notices) {
		if c.What == ChangeMetrics && c.AppID == "" {
			told++
		}
	}
	if told != 2 {
		t.Errorf("metrics notices = %d, want 2", told)
	}
}

func TestSamplerServiceWithoutDomainOrCopies(t *testing.T) {
	l := newSamplerLab(t, discardLogger())
	a := appWith("shop", 1, 1) // web has a domain, worker has none
	saveApp(t, l.env, a)
	l.nomad.SetCopies("shop", samplerCopy("c1", 0))
	l.nomad.SetStats("c1", statsFor("web", 10, 30))
	l.traefik.Set(shopText(1e9, 10, 0, nil))
	l.tick(t, 1)
	l.traefik.Set(shopText(1e9, 20, 0, nil))
	l.tick(t, 2)

	w := l.minute(t, "shop", "worker", rowTS(2))
	wantNull(t, "worker http_secs", w.HTTPSecs)
	wantNull(t, "worker requests", w.Requests)
	// Nothing runs: no copy, nothing reserved, and nothing was measured.
	wantInt(t, "worker copies", w.Copies, 0)
	wantInt(t, "worker cpu reserved", w.CPUReservedMHz, 0)
	wantInt(t, "worker memory reserved", w.MemoryReservedMB, 0)
	wantNull(t, "worker memory limit", w.MemoryLimitMB)
	wantNull(t, "worker cpu", w.CPUMHz)
	wantNull(t, "worker memory", w.MemoryMB)
	wantInt(t, "worker restarts", w.Restarts, 0)
	web := l.minute(t, "shop", "web", rowTS(2))
	wantInt(t, "web requests", web.Requests, 10)
}

func TestSamplerSourcesThatFail(t *testing.T) {
	t.Run("Traefik answering 404 is off, and the rest is written", func(t *testing.T) {
		l := newSamplerLab(t, discardLogger())
		l.save(t, "shop", "shop.example.com")
		l.nomad.SetCopies("shop", samplerCopy("c1", 0))
		l.nomad.SetStats("c1", statsFor("web", 10, 30))
		l.traefik.Fail(metrics.ErrOff)
		l.tick(t, 1)
		if got := l.svc.SamplerSources(); got.Traefik != SamplerOff || got.Nomad != SamplerOK {
			t.Errorf("sources = %+v, want off and ok", got)
		}
		r := l.minute(t, "shop", "web", rowTS(1))
		wantNull(t, "requests", r.Requests)
		wantInt(t, "memory", r.MemoryMB, 30)
	})

	t.Run("Traefik unreachable is a gap, and the next good scrape only re-baselines", func(t *testing.T) {
		l := newSamplerLab(t, discardLogger())
		l.save(t, "shop", "shop.example.com")
		l.nomad.SetCopies("shop", samplerCopy("c1", 0))
		l.nomad.SetStats("c1", statsFor("web", 10, 30))
		l.traefik.Set(shopText(1e9, 10, 0, nil))
		l.tick(t, 1)
		l.traefik.Fail(metrics.ErrUnreachable)
		l.tick(t, 2)
		if got := l.svc.SamplerSources().Traefik; got != SamplerUnreachable {
			t.Errorf("Traefik = %s, want unreachable", got)
		}
		wantNull(t, "gap requests", l.minute(t, "shop", "web", rowTS(2)).Requests)
		wantInt(t, "gap memory", l.minute(t, "shop", "web", rowTS(2)).MemoryMB, 30)

		l.traefik.Set(shopText(1e9, 50, 0, nil))
		l.tick(t, 3) // two minutes after the last good scrape: only a new baseline
		wantNull(t, "re-baseline requests", l.minute(t, "shop", "web", rowTS(3)).Requests)
		l.traefik.Set(shopText(1e9, 57, 0, nil))
		l.tick(t, 4)
		wantInt(t, "after requests", l.minute(t, "shop", "web", rowTS(4)).Requests, 7)
	})

	t.Run("no URL is disabled", func(t *testing.T) {
		l := newSamplerLab(t, discardLogger())
		l.save(t, "shop", "shop.example.com")
		l.svc.SetTraefikMetricsURL("")
		l.tick(t, 1)
		if got := l.svc.SamplerSources().Traefik; got != SamplerDisabled {
			t.Errorf("Traefik = %s, want disabled", got)
		}
		wantNull(t, "requests", l.minute(t, "shop", "web", rowTS(1)).Requests)
	})

	t.Run("a 403 on the node reads is denied, with the rest still written", func(t *testing.T) {
		l := newSamplerLab(t, discardLogger())
		l.save(t, "shop", "shop.example.com")
		l.nomad.SetCopies("shop", samplerCopy("c1", 0))
		l.nomad.SetStats("c1", statsFor("web", 10, 30))
		l.nomad.FailNodeReads(403)
		l.tick(t, 1)
		if got := l.svc.SamplerSources(); got.Nomad != SamplerDenied || got.Traefik != SamplerOK {
			t.Errorf("sources = %+v, want Nomad denied, Traefik ok", got)
		}
		r := l.minute(t, "shop", "web", rowTS(1))
		wantInt(t, "memory", r.MemoryMB, 30)
		wantInt(t, "copies", r.Copies, 1)
		wantInt(t, "memory reserved", r.MemoryReservedMB, 64)
		hosts, err := l.store.HostRows(context.Background(), store.MinuteRes, labBase.Add(-time.Hour), labBase.Add(time.Hour))
		if err != nil || len(hosts) != 0 {
			t.Errorf("host rows = %v, %v; want none", hosts, err)
		}
		disks, err := l.store.DiskRows(context.Background(), store.MinuteRes, labBase.Add(-time.Hour), labBase.Add(time.Hour))
		if err != nil || len(disks) != 0 {
			t.Errorf("disk rows = %v, %v; want none", disks, err)
		}
	})

	t.Run("a copy with no stats makes the service's memory a gap", func(t *testing.T) {
		l := newSamplerLab(t, discardLogger())
		l.save(t, "shop", "shop.example.com")
		l.nomad.SetCopies("shop", samplerCopy("c1", 0), samplerCopy("c2", 0))
		l.nomad.SetStats("c1", statsFor("web", 10, 30)) // c2 has none: 404
		l.tick(t, 1)
		r := l.minute(t, "shop", "web", rowTS(1))
		wantNull(t, "cpu", r.CPUMHz)
		wantNull(t, "memory", r.MemoryMB) // not 30: a sum of the copies that answered
		wantInt(t, "copies", r.Copies, 2)
		wantInt(t, "memory reserved", r.MemoryReservedMB, 128)
	})

	t.Run("a copy that isn't running reads no value, not 0 MB", func(t *testing.T) {
		l := newSamplerLab(t, discardLogger())
		l.save(t, "shop", "shop.example.com")
		l.nomad.SetCopies("shop", samplerCopy("c1", 0))
		l.nomad.SetStats("c1", deploytest.DeadAllocUsage("web"))
		l.tick(t, 1)
		r := l.minute(t, "shop", "web", rowTS(1))
		wantNull(t, "memory", r.MemoryMB)
		wantNull(t, "cpu", r.CPUMHz)
		wantInt(t, "copies", r.Copies, 1)
	})

	t.Run("copies add up", func(t *testing.T) {
		l := newSamplerLab(t, discardLogger())
		l.save(t, "shop", "shop.example.com")
		l.nomad.SetCopies("shop", samplerCopy("c1", 0), samplerCopy("c2", 0))
		l.nomad.SetStats("c1", statsFor("web", 12, 30))
		l.nomad.SetStats("c2", statsFor("web", 8, 40))
		l.tick(t, 1)
		r := l.minute(t, "shop", "web", rowTS(1))
		wantInt(t, "cpu", r.CPUMHz, 20)
		wantInt(t, "memory", r.MemoryMB, 70)
		wantInt(t, "memory limit", r.MemoryLimitMB, 512)
	})
}

func TestSamplerCounterResets(t *testing.T) {
	// A restart of Traefik that leaves every counter above its old value is
	// seen by the start time moving by more than 10 s; the drift of one
	// process (a few seconds) is not.
	for _, tc := range []struct {
		name  string
		start float64
		want  int64
	}{
		{"the start time moved by 359 s: counted from zero", 1000 + 359, 150},
		{"the start time drifted by 3 s: the same process", 1000 - 3, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newSamplerLab(t, discardLogger())
			l.save(t, "shop", "shop.example.com")
			l.traefik.Set(shopText(1000, 100, 0, nil))
			l.tick(t, 1)
			l.traefik.Set(shopText(tc.start, 150, 0, nil))
			l.tick(t, 2)
			wantInt(t, "requests", l.minute(t, "shop", "web", rowTS(2)).Requests, tc.want)
		})
	}

	t.Run("a counter that went down restarted at zero", func(t *testing.T) {
		l := newSamplerLab(t, discardLogger())
		l.save(t, "shop", "shop.example.com")
		l.traefik.Set(shopText(1000, 100, 0, nil))
		l.tick(t, 1)
		l.traefik.Set(shopText(1000, 7, 0, nil))
		l.tick(t, 2)
		wantInt(t, "requests", l.minute(t, "shop", "web", rowTS(2)).Requests, 7)
	})
}

func TestSamplerMissedMinuteOnlyRebaselines(t *testing.T) {
	l := newSamplerLab(t, discardLogger())
	l.save(t, "shop", "shop.example.com")
	l.traefik.Set(shopText(1000, 10, 0, nil))
	l.tick(t, 1)
	l.traefik.Set(shopText(1000, 110, 0, nil))
	l.tick(t, 6) // five minutes later: those counts are not one minute's
	if r := l.minute(t, "shop", "web", rowTS(6)); r.Requests != nil || r.HTTPSecs != nil {
		t.Errorf("after a missed minute: requests %v, http_secs %v; want NULL", r.Requests, r.HTTPSecs)
	}
	l.traefik.Set(shopText(1000, 125, 0, nil))
	l.tick(t, 7)
	r := l.minute(t, "shop", "web", rowTS(7))
	wantInt(t, "requests", r.Requests, 15)
	wantInt(t, "http_secs", r.HTTPSecs, 60)
}

func TestSamplerRestarts(t *testing.T) {
	l := newSamplerLab(t, discardLogger())
	l.save(t, "shop", "shop.example.com")
	requested := &api.TaskEvent{Type: api.TaskRestartSignal, DisplayMessage: "User requested all tasks to restart"}

	l.nomad.SetCopies("shop", samplerCopy("c1", 2))
	l.tick(t, 1)
	wantNull(t, "first minute", l.minute(t, "shop", "web", rowTS(1)).Restarts)

	// Restarts 2 -> 5, two of the three new ones asked for by Caravana.
	l.nomad.SetCopies("shop", samplerCopy("c1", 5, requested, requested))
	l.tick(t, 2)
	wantInt(t, "one crash among three restarts", l.minute(t, "shop", "web", rowTS(2)).Restarts, 1)

	// Nothing new.
	l.tick(t, 3)
	wantInt(t, "no new restarts", l.minute(t, "shop", "web", rowTS(3)).Restarts, 0)

	// A second copy has failed since the last tick: one, once.
	failed := samplerCopy("c2", 0)
	failed.ClientStatus = api.AllocClientStatusFailed
	l.nomad.SetCopies("shop", samplerCopy("c1", 5, requested, requested), failed)
	l.tick(t, 4)
	wantInt(t, "a newly failed copy", l.minute(t, "shop", "web", rowTS(4)).Restarts, 1)
	l.tick(t, 5)
	wantInt(t, "the same failed copy again", l.minute(t, "shop", "web", rowTS(5)).Restarts, 0)
}

func TestSamplerCollisionIsAttributedToNeitherAndLoggedOnce(t *testing.T) {
	logs := &syncBuffer{}
	l := newSamplerLab(t, slog.New(slog.NewTextHandler(logs, nil)))
	// "a-b" with service "c" and "a" with service "b-c" are both "a-b-c@nomad".
	one := appWith("a-b", 1)
	one.Services[0].Name = "c"
	two := appWith("a", 1)
	two.Services[0].Name = "b-c"
	saveApp(t, l.env, one)
	saveApp(t, l.env, two)
	l.save(t, "shop", "shop.example.com")

	text := func(n int) string {
		return metricstest.Text(1000, traefikBounds,
			metricstest.Counter{Service: "a-b-c@nomad", Code: "200", Method: "GET", Protocol: "http", Requests: 10 * n},
			metricstest.Counter{Service: "shop-web@nomad", Code: "200", Method: "GET", Protocol: "http", Requests: 10 * n},
			metricstest.Counter{Service: "ghost-web@nomad", Code: "200", Method: "GET", Protocol: "http", Requests: 10 * n}) // not stored
	}
	l.traefik.Set(text(1))
	l.tick(t, 1)
	l.traefik.Set(text(2))
	l.tick(t, 2)

	wantNull(t, "a-b/c requests", l.minute(t, "a-b", "c", rowTS(2)).Requests)
	wantNull(t, "a/b-c requests", l.minute(t, "a", "b-c", rowTS(2)).Requests)
	wantInt(t, "shop requests", l.minute(t, "shop", "web", rowTS(2)).Requests, 10)
	if n := strings.Count(logs.String(), "attributed to neither"); n != 1 {
		t.Errorf("collision logged %d times, want once:\n%s", n, logs.String())
	}
	series, err := l.store.ListMetricsSeries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range series {
		if s.AppID == "ghost" {
			t.Errorf("a Traefik service Caravana doesn't know got a series: %+v", s)
		}
	}
}

func TestSamplerFoldsASlotWhenItEnds(t *testing.T) {
	l := newSamplerLab(t, discardLogger())
	l.save(t, "shop", "shop.example.com")
	cum := func(n int) []int {
		return []int{0, 0, 0, 0, 5 * n, 8 * n, 10 * n, 10 * n, 10 * n, 10 * n, 10 * n, 10 * n}
	}
	l.nomad.SetCopies("shop", samplerCopy("c1", 0))

	// Ticks 1..10 write the rows of 12:00..12:09, the slot [12:00, 12:10): it
	// is folded by the last of them, which is the first at or past 12:10.
	for n := 1; n <= 10; n++ {
		l.nomad.SetStats("c1", statsFor("web", float64(n), uint64(30+n)))
		l.traefik.Set(shopText(1000, 10*n, n, cum(n)))
		l.tick(t, n)
		id, _ := l.store.EnsureSeries(context.Background(), "shop", "web")
		slots, err := l.store.SlotRows(context.Background(), id, labBase.Add(-time.Hour), labBase.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if (n < 10) != (len(slots) == 0) {
			t.Fatalf("after tick %d: %d slot rows", n, len(slots))
		}
	}
	id, _ := l.store.EnsureSeries(context.Background(), "shop", "web")
	slots, _ := l.store.SlotRows(context.Background(), id, labBase.Add(-time.Hour), labBase.Add(time.Hour))
	if len(slots) != 1 {
		t.Fatalf("slot rows = %+v", slots)
	}
	s := slots[0]
	if !s.TS.Equal(labBase) || s.Minutes != 10 {
		t.Errorf("slot = %s, %d minutes; want 12:00, 10", s.TS.Format("15:04"), s.Minutes)
	}
	// Rows 12:01..12:09 have deltas (the first scrape had none): 9 minutes.
	wantInt(t, "requests", s.Requests, 99)
	wantInt(t, "errors", s.Errors, 9)
	wantInt(t, "http_secs", s.HTTPSecs, 540)
	// Memory 31..40: the median of ten is the mean of 35 and 36, half up.
	wantInt(t, "memory", s.MemoryMB, 36)
	wantInt(t, "copies", s.Copies, 1)
	wantInt(t, "memory limit", s.MemoryLimitMB, 256)
	if s.Buckets == nil || *s.Buckets != "0,0,0,0,45,27,18" {
		t.Errorf("buckets = %v, want the nine deltas added: 0,0,0,0,45,27,18", s.Buckets)
	}
	hosts, err := l.store.HostRows(context.Background(), store.SlotRes, labBase.Add(-time.Hour), labBase.Add(time.Hour))
	if err != nil || len(hosts) != 1 || !hosts[0].TS.Equal(labBase) {
		t.Fatalf("host slot rows = %+v, %v", hosts, err)
	}
	wantInt(t, "host memory used", hosts[0].MemoryUsedMB, 1680)
	disks, err := l.store.DiskRows(context.Background(), store.SlotRes, labBase.Add(-time.Hour), labBase.Add(time.Hour))
	if err != nil || len(disks) != 1 || disks[0].Mount != "/" || !disks[0].TS.Equal(labBase) {
		t.Fatalf("disk slot rows = %+v, %v", disks, err)
	}

	// Later ticks of the same slot's successor don't fold it again, and the
	// next slot is folded when it ends.
	for n := 11; n <= 20; n++ {
		l.traefik.Set(shopText(1000, 10*n, n, cum(n)))
		l.tick(t, n)
	}
	slots, _ = l.store.SlotRows(context.Background(), id, labBase.Add(-time.Hour), labBase.Add(time.Hour))
	if len(slots) != 2 || !slots[1].TS.Equal(labBase.Add(10*time.Minute)) || slots[1].Minutes != 10 {
		t.Errorf("slot rows after 20 ticks = %+v, want 12:00 and 12:10", slots)
	}
}

func TestSamplerPrune(t *testing.T) {
	l := newSamplerLab(t, discardLogger())
	l.save(t, "shop", "shop.example.com")
	ctx := context.Background()
	id, err := l.store.EnsureSeries(ctx, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	old := func() {
		t.Helper()
		err := l.store.WriteMetricsTick(ctx, store.MetricsTick{
			Minute: store.MetricsRows{Samples: []store.MetricsSample{
				{Series: id, TS: labBase.Add(-49 * time.Hour), Minutes: 1, Copies: new(int64(1))},
				{Series: id, TS: labBase.Add(-time.Hour), Minutes: 1, Copies: new(int64(1))},
			}},
			Fold: store.MetricsRows{Samples: []store.MetricsSample{
				{Series: id, TS: labBase.Add(-9 * 24 * time.Hour), Minutes: 10, Copies: new(int64(1))},
				{Series: id, TS: labBase.Add(-24 * time.Hour), Minutes: 10, Copies: new(int64(1))},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func() (minutes, slots int) {
		from, to := labBase.Add(-30*24*time.Hour), labBase.Add(time.Hour)
		m, err := l.store.MinuteRows(ctx, id, from, to)
		if err != nil {
			t.Fatal(err)
		}
		s, err := l.store.SlotRows(ctx, id, from, to)
		if err != nil {
			t.Fatal(err)
		}
		return len(m), len(s)
	}

	old()
	l.tick(t, 1) // the first tick prunes
	// Minute rows: 11:00 (kept) and the tick's own 12:00; slot rows: yesterday's.
	if m, s := count(); m != 2 || s != 1 {
		t.Errorf("after the first tick: %d minute rows, %d slot rows; want 2, 1", m, s)
	}

	old()
	l.tick(t, 2) // under 10 minutes since the last prune
	// The 49 h row and the 9 d row are back, with the tick's own 12:01.
	if m, s := count(); m != 4 || s != 2 {
		t.Errorf("a tick soon after: %d minute rows, %d slot rows; want 4, 2 (nothing pruned)", m, s)
	}
	l.tick(t, 11) // ten minutes after the last prune; it also folds the slot 12:00
	// Minute rows 11:00, 12:00, 12:01, 12:10; slot rows yesterday's and 12:00's.
	if m, s := count(); m != 4 || s != 2 {
		t.Errorf("ten minutes later: %d minute rows, %d slot rows; want 4, 2 (the 49 h and 9 d rows gone, the fold of 12:00 added)", m, s)
	}
}

// Wait returns promptly once the context ends, with the sampler ticking fast
// (and so most likely in the middle of a tick).
func TestSamplerWaitReturnsAfterCancel(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := registrytest.New(t)
	nomad := deploytest.New(t)
	nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	nomad.SetHostStats(deploytest.LabHostStats())
	svc := New(st, newDeployer(t, reg, nomad), secretstest.New(), discardLogger())
	svc.SetMetricsSampleGap(20 * time.Millisecond)
	svc.sampler.offset = 0
	svc.sampler.traefik = metricstest.New(shopText(1000, 1, 0, nil))
	saveApp(t, &env{store: st}, appWith("shop", 1))

	runCtx, cancel := context.WithCancel(ctx)
	if err := svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sampler to write a row", func() bool {
		series, err := st.ListMetricsSeries(ctx)
		return err == nil && len(series) > 0
	})
	time.Sleep(50 * time.Millisecond)
	cancel()
	done := make(chan struct{})
	go func() { svc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the context ended")
	}
}
