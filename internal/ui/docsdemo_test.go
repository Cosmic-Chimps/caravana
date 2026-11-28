package ui

// TestDocsDemo serves a throwaway console filled with made-up data, for the screenshots of the
// documentation site (site/scripts/screenshots/run.sh). It is skipped unless CARAVANA_DOCS_DEMO is
// set to a listen address:
//
//	CARAVANA_DOCS_DEMO=127.0.0.1:7099 CARAVANA_DOCS_DEMO_STOP=/tmp/stop \
//	  go test ./internal/ui -run '^TestDocsDemo$' -count=1 -v -timeout 30m
//
// It builds the same console the other tests build (a real service, a real SQLite store in a
// temporary directory, the fake Nomad and registry), seeds the installation of project "shop"
// (see seedDocsDemo), serves the pages on the address, prints one line
// "docs demo ready at http://<address>", and blocks until the file CARAVANA_DOCS_DEMO_STOP exists.
//
// Nothing here is production code and nothing leaves the process: no real Nomad, no network but
// the one listener, no data directory but the test's own. Everything the pages show is fictional:
// example.com names, made-up digests and logs, no secret value anywhere (secret names only).

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	caravanaapi "github.com/cosmic-chimps/caravana/internal/api"
	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/store/storetest"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

const (
	// The owner of the demo installation. The password protects nothing: the instance lives for
	// the length of one screenshot run, in memory and a temporary directory, on a loopback port.
	docsDemoEmail    = "ana@example.com"
	docsDemoPassword = "demo-password-for-screenshots"
	// docsDemoVersion and docsDemoHost are what the sidebar prints.
	docsDemoVersion = "0.24.0"
	docsDemoHost    = "caravana.example.com"
)

// docsDemo is the demo installation: the console env and the virtual "now" the seed counts back from.
type docsDemo struct {
	*consoleEnv
	now time.Time
	// The copies of the apps that have output to show.
	webIDs, apiIDs, stagingIDs, searchIDs []string
}

// ago is the time dur before the demo's now.
func (d *docsDemo) ago(dur time.Duration) time.Time { return d.now.Add(-dur) }

// at runs fn with the store's clock set to t: rows written inside it carry that time.
func (d *docsDemo) at(t time.Time, fn func()) {
	d.Store.SetClock(func() time.Time { return t })
	defer d.Store.SetClock(time.Now)
	fn()
}

func TestDocsDemo(t *testing.T) {
	addr := os.Getenv("CARAVANA_DOCS_DEMO")
	if addr == "" {
		t.Skip("set CARAVANA_DOCS_DEMO to a listen address (and CARAVANA_DOCS_DEMO_STOP to a file) to serve the screenshot demo")
	}
	stopFile := os.Getenv("CARAVANA_DOCS_DEMO_STOP")
	if stopFile == "" {
		t.Fatal("set CARAVANA_DOCS_DEMO_STOP to the file whose existence stops the demo")
	}

	d := newDocsDemo(t)

	// The console on the address the screenshots use. Its own address, where a page prints one, is
	// the fictional console domain; the browser still talks to the local listener.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s, err := newServer(Deps{Auth: d.Auth, Service: d.Svc, Log: log, Host: docsDemoHost, Version: docsDemoVersion, Now: time.Now})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen on %s: %v", addr, err)
	}
	// The pages, and the API under /v1 (the terminal's websocket is an API route), as cmd/caravana mounts them.
	apiHandler := caravanaapi.New(caravanaapi.Deps{Service: d.Svc, Auth: d.Auth, Store: d.Store, Log: log, Version: docsDemoVersion})
	pages := s.routes()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p == "/v1" || strings.HasPrefix(p, "/v1/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		pages.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	fmt.Printf("docs demo ready at http://%s\n", ln.Addr())
	for {
		if _, err := os.Stat(stopFile); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// newDocsDemo builds the console the other tests build (a real service and store, the fake Nomad, a
// fake registry), with the demo owner, one first server that its sampler has read, and the data of
// seedDocsDemo. Everything that could reach a network is pointed at a local stand-in or left off.
func newDocsDemo(t *testing.T) *docsDemo {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Dir(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Now().UTC().Truncate(time.Minute)
	uie := &uiEnv{Store: st, Clock: &fakeClock{t: now}, Logs: &syncBuffer{}}
	log := slog.New(slog.NewJSONHandler(uie.Logs, nil))
	uie.Auth = service.NewAuth(st, log, service.AuthOptions{Now: time.Now, PasswordIterations: 1000})
	if err := uie.Auth.InitAdmin(ctx, docsDemoEmail, docsDemoPassword); err != nil {
		t.Fatalf("create the owner: %v", err)
	}
	owner, err := uie.Auth.ResolvePerson(ctx, "")
	if err != nil {
		t.Fatalf("find the owner: %v", err)
	}
	uie.OwnerID = owner.ID

	nomad := deploytest.New(t)
	nomad.Succeed()
	reg := registrytest.New(t)
	client, err := deploy.NewNomadClient(nomad.Addr(), "")
	if err != nil {
		t.Fatalf("nomad client: %v", err)
	}
	deployer := deploy.New(client, &registry.Client{Endpoints: reg.Endpoints()},
		nomadjob.Settings{Entrypoint: "websecure", DefaultCPUMHz: 100, DefaultMemoryMB: 256, DefaultMemoryMaxMB: 1024})
	deployer.Timing = deploy.Timing{
		ImageTimeout: 5 * time.Second, PollGap: time.Millisecond,
		BackoffStart: time.Millisecond, BackoffMax: 5 * time.Millisecond,
		VersionRetries: []time.Duration{time.Millisecond, time.Millisecond},
	}
	svc := service.New(st, deployer, deploy.NewVariables(client), log)
	svc.SetAuth(uie.Auth)
	svc.SetDomainWatchGaps(time.Hour, time.Hour)
	svc.SetEdge(edgetest.FakeResolver{}, &edgetest.FakeDialer{})
	svc.SetPublicURL("https://" + docsDemoHost)
	svc.SetVersion(docsDemoVersion)

	// Nothing leaves the process. The release check asks a stand-in that says 0.25.0 is out; the
	// template catalog check asks one that has the built-in catalog's sums; Traefik's metrics are
	// a stand-in; the clocks of schedules and backups never come due.
	gh := docsRelease(t)
	svc.SetReleaseURL(gh.URL + "/repos/x/releases/latest")
	svc.SetReleasePinURLs(gh.URL+"/download", gh.URL+"/raw")
	svc.SetReleaseCheckGap(24 * time.Hour)
	updaterDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(updaterDir, "updater"), []byte("installed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.SetUpdaterDir(updaterDir)
	svc.SetUpdateRequestPath(filepath.Join(t.TempDir(), "update-request"))
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
			_, _ = w.Write(templates.EmbeddedSums())
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(catalog.Close)
	svc.SetTemplatesURL(catalog.URL)
	svc.SetTemplateCheckGap(24 * time.Hour)
	svc.SetTraefikMetricsURL(fakeTraefikMetrics(t).URL + "/metrics")
	svc.SetScheduleScanGap(24 * time.Hour)
	svc.SetBackupScanGap(24 * time.Hour)
	svc.SetPreviewReapGap(24 * time.Hour)
	svc.SetMetricsSampleGap(time.Minute)
	svc.SetHostRoot(hostos.Dir(docsHostRoot(t)))
	svc.SetDockerRoot("/var/lib/docker")

	// The first server: Nomad's node, its network and its host numbers.
	nomad.SetNode("node-1", "hel-1", "ready", api.NodeResources{
		Cpu: api.NodeCpuResources{CpuShares: 4800, TotalCpuCores: 2}, Memory: api.NodeMemoryResources{MemoryMB: 3916}}, nil)
	nomad.SetNodeNetworks(deploytest.Caravana0Networks("10.99.0.1"))
	stats := deploytest.LabHostStats()
	stats.Uptime = uint64((23*24*time.Hour + 6*time.Hour) / time.Second)
	stats.Memory = &api.HostMemoryStats{Total: 4106 << 20, Available: 2200 << 20, Used: 1900 << 20, Free: 300 << 20}
	stats.CPUTicksConsumed = 410
	root := api.HostDiskStats{Device: "/dev/sda1", Mountpoint: "/", Size: 80 << 30, Used: 22 << 30, Available: 58 << 30, UsedPercent: 27.5}
	alloc := root
	alloc.Device, alloc.Mountpoint = "", ""
	stats.DiskStats = []*api.HostDiskStats{&root}
	stats.AllocDirStats = &alloc
	nomad.SetHostStats(stats)
	attrs := deploytest.X86NodeAttributes()
	attrs["cpu.numcores"], attrs["cpu.totalcompute"] = "2", "4800"
	nomad.SetNodeFacts(attrs, "default", []*api.NodeEvent{{Subsystem: "Cluster", Message: "Node registered", Timestamp: now.Add(-23 * 24 * time.Hour)}})
	nomad.OnExec(func(r deploytest.ExecRequest) deploytest.ExecReply {
		if r.TTY {
			return deploytest.ExecReply{Shell: true}
		}
		return deploytest.ExecReply{Stdout: "dist\nnode_modules\npackage.json\npublic\n"}
	})

	d := &docsDemo{now: now}
	d.consoleEnv = &consoleEnv{uiEnv: uie, Svc: svc, Nomad: nomad, Reg: reg}
	seedDocsDemo(t, d)
	return d
}

// seedDocsDemo makes the installation of the documentation: see the README of
// site/scripts/screenshots. Each piece is its own function so a screen that needs more data adds one.
func seedDocsDemo(t *testing.T, d *docsDemo) {
	t.Helper()
	ctx := context.Background()
	seedDocsPlaces(t, d)
	seedDocsApps(t, d)
	d.seedDocsPreview(t)
	d.seedDocsDomains(t)
	d.seedDocsLogs(d.webIDs, d.apiIDs, d.stagingIDs)
	d.seedDocsFiles(d.webIDs[0])
	d.seedDocsRequests()
	d.seedDocsBackups(t)
	d.seedDocsSchedules(t)
	d.seedDocsSettings(t)
	d.seedDocsScanner(t)
	d.seedDocsMetrics(t)

	svcCtx, stop := context.WithCancel(ctx)
	if err := d.Svc.Start(svcCtx); err != nil {
		t.Fatalf("start the service: %v", err)
	}
	t.Cleanup(func() { stop(); d.Svc.Wait() })

	// The cluster the installer leaves, then two servers joined to it.
	caCert, caKey, err := clusterca.New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.Nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(caCert), "key": string(caKey)})
	d.Nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": "Z29zc2lwLWtleS1mb3ItdGVzdHM=\n"})
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AdoptFirstServer(ctx, d.Store, time.Now, "hel-1", pub, "203.0.113.4"); err != nil {
		t.Fatalf("adopt the first server: %v", err)
	}
	d.seedDocsServers(t)

	// The sampler ticks every minute: wait for its second tick, the first that has request numbers
	// (they are the difference of two readings of Traefik), so no page shows a gap at its right edge.
	series, err := d.Store.EnsureSeries(ctx, shopID("production", "web"), "web")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Minute)
	for {
		h, herr := d.Svc.Host(ctx, "")
		rows, _ := d.Store.MinuteRows(ctx, series, d.now, time.Now().Add(time.Hour))
		ready := herr == nil && h.Node != nil && h.SampledAt != nil
		for _, row := range rows {
			if ready && row.Requests != nil && row.TS.After(d.now) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sampler never read the first server and Traefik: %+v, %v, %d rows", h, herr, len(rows))
		}
		time.Sleep(500 * time.Millisecond)
	}
}
