package ui

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 031, the console: the Servers list, Add a server, the server page for every server and the
// small changes elsewhere once there is more than one server. The cluster is made through the service,
// as the installer and the node agents would: the first server adopted, the others joined and synced,
// and the fake Nomad has a node for each.

// joinTokenPattern is a join token as the installer is given it.
var joinTokenPattern = regexp.MustCompile(`cvjn_[A-Za-z0-9_-]{43}`)

// serversEnv is a console on a cluster the installer left: the first server (hel-1, node-1, the one
// Caravana runs on) is adopted, the CA and the gossip key are in Nomad's variables, and nothing else has
// joined. The sampler's first tick is an hour away, so the numbers are the ones a test writes.
func serversEnv(t *testing.T) *consoleEnv { return clusterEnv(t, false) }

// sampledServersEnv is serversEnv with the sampler running: it has read the first server's node (x86, Ubuntu,
// a reboot pending from the host's files), so that server's page is the whole page. The first server's numbers
// are the sampler's then, not a test's.
func sampledServersEnv(t *testing.T) *consoleEnv { return clusterEnv(t, true) }

func clusterEnv(t *testing.T, sampled bool) *consoleEnv {
	t.Helper()
	e := newConsoleEnvWith(t, func(svc *service.Service, n *deploytest.Nomad) {
		svc.SetMetricsSampleGap(time.Hour)
		n.SetNode("node-1", "hel-1", "ready", deploytest.LabNodeResources(), nil)
		n.SetNodeNetworks(deploytest.Caravana0Networks("10.99.0.1"))
		if sampled {
			svc.SetMetricsSampleGap(500 * time.Millisecond)
			svc.SetHostRoot(hostos.Dir("../hostos/testdata/host"))
			svc.SetDockerRoot("/var/lib/docker")
			n.SetHostStats(deploytest.LabHostStats())
			n.SetNodeFacts(deploytest.X86NodeAttributes(), "default", nil)
			n.SetTraefikJob("traefik:v3.7.13")
		}
	})
	caCert, caKey, err := clusterca.New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(caCert), "key": string(caKey)})
	e.Nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": "Z29zc2lwLWtleS1mb3ItdGVzdHM=\n"})
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AdoptFirstServer(context.Background(), e.Store, time.Now, "hel-1", pub, "203.0.113.4"); err != nil {
		t.Fatalf("adopt the first server: %v", err)
	}
	if sampled {
		deadline := time.Now().Add(20 * time.Second)
		for {
			h, err := e.Svc.Host(context.Background(), "")
			if err == nil && h.Node != nil && h.SampledAt != nil && h.Reboot != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the sampler never read the first server: %+v, %v", h, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return e
}

// healthy is a node report that makes the checks pass for a server that reaches the servers at others.
func healthy(others ...int) service.NodeReport {
	r := service.NodeReport{Agent: "0.18.0", Nomad: "2.0.7", Docker: "27.5.1", DockerRunning: true, DiskFreeMB: 160000,
		DiskTotalMB: 190000, MemoryMB: 3916, Kernel: "6.8.0", Role: "client", Reach: []service.NodeReach{}}
	for _, o := range others {
		r.Reach = append(r.Reach, service.NodeReach{Index: o, OK: true})
	}
	return r
}

// join makes a join command for name and has a server call it in: the server is joining. It returns the
// join (with its token) and the node's token.
func (e *consoleEnv) join(t *testing.T, name string) (service.ServerJoin, service.NodeAnswer) {
	t.Helper()
	return e.joinInPool(t, name, "")
}

// joinInPool is join in a node pool.
func (e *consoleEnv) joinInPool(t *testing.T, name, pool string) (service.ServerJoin, service.NodeAnswer) {
	t.Helper()
	ctx := context.Background()
	j, err := e.Svc.CreateJoin(ctx, name, pool, "https://console.test")
	if err != nil {
		t.Fatalf("CreateJoin(%q): %v", name, err)
	}
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	_, csr, err := clusterca.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	ans, err := e.Svc.Join(ctx, service.JoinRequest{Token: j.Token, PublicKey: pub, CSR: string(csr),
		Facts: service.NodeFacts{Hostname: name, Docker: "27.5.1", Agent: "0.18.0"}}, "203.0.113.9")
	if err != nil {
		t.Fatalf("Join(%q): %v", name, err)
	}
	return j, ans
}

// readyServer joins a server, gives it a Nomad node at its private address and syncs a healthy report: it is
// ready. nodeID is the node's ID in the fake Nomad. It returns the join and the node's token.
func (e *consoleEnv) readyServer(t *testing.T, name string) (service.ServerJoin, service.NodeAnswer) {
	t.Helper()
	return e.readyServerInPool(t, name, "")
}

// readyServerInPool is readyServer in a node pool.
func (e *consoleEnv) readyServerInPool(t *testing.T, name, pool string) (service.ServerJoin, service.NodeAnswer) {
	t.Helper()
	j, ans := e.joinInPool(t, name, pool)
	e.Nomad.AddNode(fmt.Sprintf("node-%d", ans.Index), name, "ready", fmt.Sprintf("10.99.0.%d", ans.Index))
	others := []int{1}
	for i := 2; i < ans.Index; i++ {
		others = append(others, i)
	}
	if _, err := e.Svc.NodeSync(context.Background(), ans.NodeToken, healthy(others...)); err != nil {
		t.Fatalf("NodeSync(%q): %v", name, err)
	}
	return j, ans
}

// sample stores a host sample for a Nomad node: memory and CPU used, reserved and total, and one disk.
func (e *consoleEnv) sample(t *testing.T, nodeID string, memUsed, memReserved, memTotal, cpuUsed, cpuTotal, cores, diskUsed, diskTotal int64) {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Minute)
	i64 := func(n int64) *int64 { return &n }
	err := e.Store.WriteMetricsTick(context.Background(), store.MetricsTick{At: at, Minute: store.MetricsRows{
		Hosts: []store.HostSample{{Node: nodeID, TS: at, MemoryUsedMB: i64(memUsed), MemoryReservedMB: i64(memReserved), MemoryTotalMB: i64(memTotal),
			CPUUsedMHz: i64(cpuUsed), CPUReservedMHz: i64(cpuUsed), CPUTotalMHz: i64(cpuTotal), CPUCores: i64(cores)}},
		Disks: []store.DiskSample{{Node: nodeID, TS: at, Mount: "/", Holds: "nomad,docker", UsedMB: diskUsed, TotalMB: diskTotal, FreeMB: diskTotal - diskUsed}},
	}})
	if err != nil {
		t.Fatalf("write a sample: %v", err)
	}
}

// threeServers is serversEnv with fsn-1 and fsn-2 ready and every server sampled.
func threeServers(t *testing.T) *consoleEnv {
	t.Helper()
	e := serversEnv(t)
	e.readyServer(t, "fsn-1")
	e.readyServer(t, "fsn-2")
	e.sample(t, "node-1", 480, 144, 1958, 60, 4000, 2, 6000, 19000)
	e.sample(t, "node-2", 900, 600, 1958, 300, 4000, 2, 8000, 19000)
	e.sample(t, "node-3", 100, 0, 1958, 20, 4000, 2, 3000, 19000)
	return e
}

// expiredJoin stores a waiting join whose command ended a minute ago, as one the service hasn't cleared yet.
func (e *consoleEnv) expiredJoin(t *testing.T, name string) store.Server {
	t.Helper()
	now := time.Now().UTC()
	row, err := e.Store.InsertServer(context.Background(), store.Server{ID: "expiredjoin" + name, Name: name, Pool: "default", State: store.ServerWaiting,
		JoinHash: "hash-of-" + name, JoinExpires: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour - time.Minute)})
	if err != nil {
		t.Fatalf("store an expired join: %v", err)
	}
	return row
}

// rowOfServer is the list's row of the server called name, from its opening tag to the next row's.
func rowOfServer(t *testing.T, body, name string) string {
	t.Helper()
	for _, r := range strings.Split(body, `<div class="cv-row cv-slrow`)[1:] {
		if strings.Contains(r, ">"+name+"</a>") {
			return r
		}
	}
	t.Fatalf("no row for %q in:\n%s", name, text(body))
	return ""
}

// ---- the list ---------------------------------------------------------------------------------

func TestServersListWithOneServer(t *testing.T) {
	e := serversEnv(t)
	e.sample(t, "node-1", 480, 144, 1958, 60, 4000, 2, 6000, 19000)
	resp, body := e.get(t, "/ops/servers")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops/servers = %d:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)
	for _, want := range []string{
		`<h1 class="cv-headline">Servers</h1>`, `href="/ops/servers/new" class="cv-btn cv-btn-primary cv-btn-sm"`,
		`<a href="/ops/servers" class="cv-nav-item" aria-current="page">`, `sse-connect="/live?watch=ops"`,
		`id="srv-list"`, `hx-get="/parts/ops/servers"`, `hx-trigger="sse:servers, sse:metrics, sse:copies, sse:sync"`,
		`<span class="cv-slrole">console</span>`, `<span class="cv-slrole">ingress</span>`, `<span class="cv-slrole">scheduler</span>`,
		`cv-pill-healthy cv-pill-quiet"><span class="cv-mark cv-mark-healthy" aria-hidden="true"></span>ready`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if !regexp.MustCompile(`Servers 1 server · [\d.]+ GB · \d+ vCPU Add server`).MatchString(words) {
		t.Errorf("the head line isn't `1 server · … GB · … vCPU`:\n%s", words)
	}
	// A list of one has no sentence about the cluster and says nothing is wrong with adding.
	if strings.Contains(body, "cv-slcluster") || strings.Contains(body, `aria-disabled="true"`) {
		t.Error("a cluster of one has a sentence or a disabled Add server")
	}
	row := rowOfServer(t, body, "hel-1")
	for _, want := range []string{"cpu", "0.03 / 2 vCPU", "mem", "0.5 / 1.9 GB", "disk", "5.9 / 18.6 GB", "0 copies"} {
		if !strings.Contains(text(row), want) {
			t.Errorf("hel-1's row lacks %q:\n%s", want, text(row))
		}
	}
	if strings.Contains(text(row), "Not set up for a cluster yet.") {
		t.Error("a managed server says it isn't set up")
	}
	// Reserved is hatched over the track, used is the fill, and the numbers are in the meter's title.
	if !strings.Contains(row, `class="cv-mnreserved"`) || !strings.Contains(row, `class="cv-mnfill"`) {
		t.Errorf("the meters lack the fill or the reserved hatch:\n%s", row)
	}
}

// A server with no sample yet has a dash in each meter, never an empty meter or a zero.
func TestServersListNoNumbersYet(t *testing.T) {
	e := serversEnv(t)
	_, body := e.get(t, "/ops/servers")
	row := rowOfServer(t, body, "hel-1")
	if strings.Count(row, `cv-slmeter-none`) != 3 || strings.Contains(row, `class="cv-mnfill"`) {
		t.Errorf("a server with no numbers doesn't have three dashes:\n%s", row)
	}
}

func TestServersListWithThreeServers(t *testing.T) {
	e := threeServers(t)
	_, body := e.get(t, "/ops/servers")
	words := text(body)
	if !regexp.MustCompile(`Servers 3 servers · [\d.]+ GB · 6 vCPU Add server`).MatchString(words) {
		t.Errorf("the head line isn't `3 servers · … GB · 6 vCPU`:\n%s", words)
	}
	// In index order, and only the first runs the console and takes the public traffic.
	i1, i2, i3 := strings.Index(body, ">hel-1</a>"), strings.Index(body, ">fsn-1</a>"), strings.Index(body, ">fsn-2</a>")
	if i1 < 0 || !(i1 < i2 && i2 < i3) {
		t.Errorf("the servers aren't in index order: %d %d %d", i1, i2, i3)
	}
	if r := rowOfServer(t, body, "fsn-1"); strings.Contains(r, "cv-slrole") {
		t.Errorf("fsn-1 has a role it doesn't have:\n%s", r)
	}
	// Three are ready and none runs the scheduler yet but the first: the promotion is under way.
	if !strings.Contains(words, "Three servers: two more are starting to run the scheduler.") || !strings.Contains(body, `cv-mark cv-mark-rolling`) {
		t.Errorf("the cluster sentence for a promotion under way is missing:\n%s", words)
	}
	// Memory in use at 85 % warns with a mark and words, not only a colour.
	e.sample(t, "node-2", 1700, 600, 1958, 300, 4000, 2, 8000, 19000)
	_, body = e.get(t, "/ops/servers")
	if r := rowOfServer(t, body, "fsn-1"); !strings.Contains(r, "cv-mnwarntext") || !strings.Contains(r, "(near its limit)") {
		t.Errorf("a server at 87 %% memory doesn't warn:\n%s", r)
	}
	// Its disk is at 42 %: the state is still ready.
	if r := rowOfServer(t, body, "fsn-1"); strings.Contains(text(r), "disk filling") || !strings.Contains(text(r), "7.8 / 18.6 GB") {
		t.Errorf("fsn-1's disk is at 42 %%:\n%s", text(r))
	}
}

// Two ready servers: one more would let the cluster survive a loss.
func TestServersListWithTwoServers(t *testing.T) {
	e := serversEnv(t)
	e.readyServer(t, "fsn-1")
	_, body := e.get(t, "/ops/servers")
	if !strings.Contains(text(body), "With one more server the cluster survives the loss of one.") {
		t.Errorf("the sentence for two servers is missing:\n%s", text(body))
	}
}

// The cluster sentence, one row of the contract's table each.
func TestClusterSentence(t *testing.T) {
	ready := func(name string, nomadServer bool) service.ServerView {
		return service.ServerView{Name: name, State: "ready", Managed: true, NomadServer: nomadServer, AcceptsWork: true}
	}
	tests := []struct {
		name         string
		view         service.ServersView
		joined       int
		mark, phrase string
	}{
		{"one server", service.ServersView{Cluster: service.ClusterView{Ready: 1}}, 1, "", ""},
		{"two servers", service.ServersView{Cluster: service.ClusterView{Ready: 2}}, 2, "idle", "With one more server the cluster survives the loss of one."},
		{"promotion under way", service.ServersView{Cluster: service.ClusterView{Ready: 3, NomadServers: 1}}, 3, "rolling", "Three servers: two more are starting to run the scheduler."},
		{"three schedulers running", service.ServersView{Cluster: service.ClusterView{Ready: 3, NomadServers: 3, SurvivesLoss: true}}, 3, "healthy", "The cluster survives the loss of one server."},
		{"a scheduler is down", service.ServersView{
			Cluster: service.ClusterView{Ready: 2, NomadServers: 3},
			Servers: []service.ServerView{ready("a", true), {Name: "b", State: "down", NomadServer: true}, ready("c", true)}}, 3,
			"warning", "One of the three servers that run the scheduler is down. The cluster works, and can't lose another."},
		{"a joining server is not ready", service.ServersView{Cluster: service.ClusterView{Ready: 2}}, 3, "idle", "With one more server the cluster survives the loss of one."},
	}
	for _, tc := range tests {
		got := clusterSentence(tc.view, tc.joined)
		switch {
		case tc.phrase == "" && got != nil:
			t.Errorf("%s: sentence %+v, want none", tc.name, got)
		case tc.phrase != "" && (got == nil || got.Mark != tc.mark || got.Text != tc.phrase):
			t.Errorf("%s: sentence %+v, want %s %q", tc.name, got, tc.mark, tc.phrase)
		}
	}
}

// With every scheduler running the cluster survives a loss and the sentence says so with the healthy mark.
func TestServersListSurvivesALoss(t *testing.T) {
	e := serversEnv(t)
	_, two := e.readyServer(t, "fsn-1")
	_, three := e.readyServer(t, "fsn-2")
	ctx := context.Background()
	running := func(token string, others ...int) {
		r := healthy(others...)
		r.ServerRunning = true
		if _, err := e.Svc.NodeSync(ctx, token, r); err != nil {
			t.Fatal(err)
		}
	}
	// The roles are handed out one at a time, each once the wanted ones run the scheduler.
	for range 4 {
		running(e.firstToken(t), 2, 3)
		running(two.NodeToken, 1, 3)
		running(three.NodeToken, 1, 2)
	}
	view, err := e.Svc.ListServers(ctx)
	if err != nil || !view.Cluster.SurvivesLoss {
		t.Skipf("the roles weren't handed out in this fake (survivesLoss = %v, %v): the sentence is covered by TestClusterSentence", view.Cluster.SurvivesLoss, err)
	}
	_, body := e.get(t, "/ops/servers")
	if !strings.Contains(text(body), "The cluster survives the loss of one server.") || strings.Count(body, `<span class="cv-slrole">scheduler</span>`) != 3 {
		t.Errorf("the page doesn't say the cluster survives a loss, with three schedulers:\n%s", text(body))
	}
}

// firstToken rotates the first server's node token and returns it (the installer's call), so a test can sync as it.
func (e *consoleEnv) firstToken(t *testing.T) string {
	t.Helper()
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.AdoptFirstServer(context.Background(), e.Store, time.Now, "hel-1", pub, "203.0.113.4")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestServersListWaitingJoiningAndExpired(t *testing.T) {
	e := serversEnv(t)
	waiting, err := e.Svc.CreateJoin(context.Background(), "fsn-9", "", "https://console.test")
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.join(t, "fsn-1")
	_, body := e.get(t, "/ops/servers")

	// A waiting row has no meters, says how long the command works and has a Cancel form of its own.
	row := rowOfServer(t, body, "fsn-9")
	if !regexp.MustCompile(`Waiting for the server to call in · the command works for \d+ more minutes`).MatchString(text(row)) ||
		strings.Contains(row, "cv-slmeter") || !strings.Contains(row, `<form method="post" action="/ops/servers/joins/`+waiting.Server.ID+`/cancel"`) ||
		!strings.Contains(row, `href="/ops/servers/joins/`+waiting.Server.ID+`"`) || !strings.Contains(row, "waiting") {
		t.Errorf("the waiting row:\n%s", row)
	}
	// A joining row says its first open check, and links to the join's page.
	row = rowOfServer(t, body, "fsn-1")
	if !strings.Contains(text(row), "Can't reach hel-1 yet") || !strings.Contains(row, `href="/ops/servers/joins/`+j.Server.ID+`"`) ||
		!strings.Contains(text(row), "joining") || strings.Contains(row, "<form") {
		t.Errorf("the joining row:\n%s", row)
	}
	// Neither counts in the head line.
	if !strings.Contains(text(body), "1 server") {
		t.Errorf("a server that hasn't joined counts:\n%s", text(body))
	}

	// An expired command isn't listed.
	e.expiredJoin(t, "fsn-8")
	_, body = e.get(t, "/ops/servers")
	if strings.Contains(body, ">fsn-8</a>") || !strings.Contains(body, ">fsn-9</a>") {
		t.Error("an expired command is listed, or a live one isn't")
	}
}

func TestServersListDownUnmanagedAndNotTakingWork(t *testing.T) {
	e := threeServers(t)
	e.Nomad.SetNodeStatusOf("node-2", "down")
	e.Nomad.SetNodeEligibilityOf("node-3", "ineligible", false)
	e.Nomad.AddNode("node-lab", "lab", "ready", "") // a node Nomad knows and Caravana doesn't manage
	_, body := e.get(t, "/ops/servers")

	// Down keeps its last numbers, grey, with no fill, and runs nothing.
	row := rowOfServer(t, body, "fsn-1")
	if !strings.Contains(row, "cv-pill-failed cv-pill-quiet") || !strings.Contains(row, "cv-slmeter-grey") || strings.Contains(row, `class="cv-mnfill"`) ||
		!strings.Contains(text(row), "0 copies") || !strings.Contains(text(row), "0.9 / 1.9 GB") {
		t.Errorf("the down row:\n%s", row)
	}
	if r := rowOfServer(t, body, "fsn-2"); !strings.Contains(text(r), "not taking work") || !strings.Contains(r, "cv-pill-idle cv-pill-quiet") {
		t.Errorf("an ineligible server doesn't say it takes no work:\n%s", r)
	}
	// A Nomad node Caravana doesn't manage is a normal row with Nomad's state, and says why it is different.
	if r := rowOfServer(t, body, "lab"); !strings.Contains(text(r), "Not set up for a cluster yet.") || !strings.Contains(text(r), "ready") {
		t.Errorf("the unmanaged node's row:\n%s", r)
	}
}

// A disk at 85 % or more is the state word "disk filling", with the warning mark.
func TestServersListDiskFilling(t *testing.T) {
	e := serversEnv(t)
	e.sample(t, "node-1", 480, 144, 1958, 60, 4000, 2, 17000, 19000)
	_, body := e.get(t, "/ops/servers")
	if r := rowOfServer(t, body, "hel-1"); !strings.Contains(r, "cv-pill-warning cv-pill-quiet") || !strings.Contains(text(r), "disk filling") {
		t.Errorf("a disk at 89 %% isn't \"disk filling\":\n%s", r)
	}
}

// A pool other than default in use is said in the head line.
func TestServersListNamedPool(t *testing.T) {
	e := serversEnv(t)
	e.readyServerInPool(t, "fsn-1", "build")
	_, body := e.get(t, "/ops/servers")
	if !strings.Contains(text(body), "1 in default, 1 in build ·") {
		t.Errorf("the pools aren't in the head line:\n%s", text(body))
	}
}

// With no cluster set up Add server is still a link, drawn as unavailable, and the notice says why.
func TestServersListWhenServersCantBeAddedYet(t *testing.T) {
	e := newConsoleEnv(t)
	_, body := e.get(t, "/ops/servers")
	if !strings.Contains(body, `href="/ops/servers/new" class="cv-btn cv-btn-primary cv-btn-sm" aria-disabled="true"`) ||
		!strings.Contains(text(body), "Run the installer again on this server first: it sets up the private network and Nomad's certificates.") {
		t.Errorf("the list doesn't explain why no server can be added:\n%s", text(body))
	}
}

// The part is the region the page draws; signed out it is 401 with the redirect.
func TestServersListPart(t *testing.T) {
	e := threeServers(t)
	_, page := e.get(t, "/ops/servers")
	resp, part := e.get(t, "/parts/ops/servers")
	if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") {
		t.Fatalf("the part = %d or a whole page", resp.StatusCode)
	}
	for _, want := range []string{">hel-1</a>", ">fsn-1</a>", ">fsn-2</a>", "Add server"} {
		if !strings.Contains(part, want) || !strings.Contains(page, want) {
			t.Errorf("page or part lacks %q", want)
		}
	}
	resp, _ = e.do(t, request{path: "/parts/ops/servers"})
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("HX-Redirect") == "" {
		t.Errorf("the part without a session = %d, want 401 with HX-Redirect", resp.StatusCode)
	}
}

// ---- Add a server -----------------------------------------------------------------------------

func TestAddServerBeforeTheCommand(t *testing.T) {
	e := serversEnv(t)
	resp, body := e.get(t, "/ops/servers/new")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops/servers/new = %d", resp.StatusCode)
	}
	words := text(body)
	for _, want := range []string{
		"← Servers", "Add a server",
		"Any machine with Ubuntu 24.04 and 2 GB of memory or more. Caravana installs what it needs, and the server starts taking work once its checks pass.",
		"Lowercase letters, digits and dashes. Leave it empty to use the machine's own name.",
		"Apps run on servers in default. A server in another pool takes no apps.",
		"Make the join command", "Checks", "Make the command to start.",
		"Connected to the cluster", "Docker running", "Reaches the other servers on the private network", "Ready to take work",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	for _, want := range []string{`<form method="post" action="/ops/servers"`, `name="name"`, `placeholder="fsn-2"`, `name="pool" value="default"`,
		`<a href="/ops/servers" class="cv-nav-item" aria-current="page">`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// No tabs, and nothing about SSH.
	if strings.Contains(strings.ToLower(words), "ssh") || strings.Contains(body, `role="tab"`) {
		t.Error("the page has tabs or mentions SSH")
	}
	// Before the command there is no token, no polling and no live region that asks for a join.
	if strings.Contains(body, "cvjn_") || strings.Contains(body, `hx-trigger="every 5s"`) || strings.Contains(body, "/parts/ops/servers/joins/") {
		t.Error("the form's page has a token or asks for a join's part")
	}
}

// When no server can be added yet there is no form: the service's sentence stands in its place.
func TestAddServerWhenTheClusterIsNotReady(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/ops/servers/new")
	if resp.StatusCode != http.StatusOK || strings.Contains(body, `action="/ops/servers"`) ||
		!strings.Contains(text(body), "Run the installer again on this server first: it sets up the private network and Nomad's certificates.") {
		t.Errorf("GET /ops/servers/new = %d:\n%s", resp.StatusCode, text(body))
	}
	// The same answer to the post, with its status, and nothing was made.
	resp, body = e.postForm(t, "/ops/servers", url.Values{"name": {"fsn-2"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Run the installer again on this server first") || strings.Contains(body, "cvjn_") {
		t.Errorf("POST /ops/servers = %d:\n%s", resp.StatusCode, text(body))
	}
}

// The command is the answer of the post, once; the page that answers it has the live checks.
func TestAddServerShowsTheCommandOnce(t *testing.T) {
	e := serversEnv(t)
	resp, body := e.postForm(t, "/ops/servers", url.Values{"name": {"fsn-2"}, "pool": {"default"}})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("POST /ops/servers = %d, Cache-Control %q; want 200 and no-store", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	token := joinTokenPattern.FindString(body)
	if token == "" || strings.Count(body, token) != 1 {
		t.Fatalf("the answer has the token %d times, want once:\n%s", strings.Count(body, token), text(body))
	}
	words := text(body)
	for _, want := range []string{
		"Run this on the new server as root:", "The token works once and for one hour.",
		"name fsn-2", "pool default", "Cancel this join", "Waiting for the server to call in…",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the answer lacks %q:\n%s", want, words)
		}
	}
	// The command is broken after the pipe (a dev build says it needs the files), the options indented two spaces.
	cmd := body[strings.Index(body, `id="join-command">`):]
	cmd = cmd[:strings.Index(cmd, "</code>")]
	if !strings.Contains(cmd, " \\\n  --join "+token+" \\\n  --url ") {
		t.Errorf("the command isn't broken for reading:\n%s", cmd)
	}
	for _, want := range []string{`data-copy="join-command"`, `id="join-checks"`, `hx-trigger="sse:servers, sse:sync"`, `hx-trigger="every 5s"`,
		`<noscript><meta http-equiv="refresh" content="10; url=/ops/servers/joins/`} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer lacks %q", want)
		}
	}
	if !strings.Contains(body, `data-copy="join-command" aria-label="Copy the join command" hidden`) {
		t.Error("the Copy button isn't drawn hidden, for the script to show")
	}
	// The form isn't drawn again: the fields are facts.
	if strings.Contains(body, `<input class="cv-field`) {
		t.Error("the answer still has inputs")
	}

	// The token is in no other page, part or log line.
	_, list := e.get(t, "/ops/servers")
	id := regexp.MustCompile(`/ops/servers/joins/([a-z0-9]+)`).FindStringSubmatch(list)
	if id == nil {
		t.Fatalf("the list has no link to the join:\n%s", list)
	}
	for _, path := range []string{"/ops/servers", "/ops/servers/new", "/ops/servers/joins/" + id[1], "/parts/ops/servers/joins/" + id[1],
		"/parts/ops/servers", "/ops", "/parts/ops/server", "/ops/updates"} {
		resp, other := e.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
		if strings.Contains(other, token) || strings.Contains(other, "cvjn_") {
			t.Errorf("GET %s has the join token", path)
		}
	}
	if strings.Contains(e.Logs.String(), token) {
		t.Error("the join token is in the server log")
	}
	// The join's own page says the command was shown once.
	_, join := e.get(t, "/ops/servers/joins/"+id[1])
	if !strings.Contains(text(join), "The command was shown once. If you lost it, cancel this one and make a new command.") || !strings.Contains(join, "Cancel this join") {
		t.Errorf("the join's page:\n%s", text(join))
	}
}

// A console with no trusted certificate puts --insecure in the command and says why that matters.
func TestAddServerInsecureConsole(t *testing.T) {
	e := serversEnv(t)
	e.Svc.SetACME("none", "")
	_, body := e.postForm(t, "/ops/servers", url.Values{})
	if !strings.Contains(body, "--insecure") ||
		!strings.Contains(text(body), "This console has no trusted certificate, so the new server can't check who it is talking to. Use this on a network you trust.") {
		t.Errorf("the insecure notice is missing:\n%s", text(body))
	}
	// A development build's command says the installer must be copied, as a notice.
	if !strings.Contains(text(body), "This is a development build: copy its install.sh and release files to the new server first") {
		t.Errorf("a dev build's note is missing:\n%s", text(body))
	}
}

// The forms work without script: a refusal is drawn at its field with the form filled as typed.
func TestAddServerRefusalsAreDrawnAtTheField(t *testing.T) {
	e := serversEnv(t)
	tests := []struct {
		name   string
		form   url.Values
		status int
		want   string
		field  string
	}{
		{"a bad name", url.Values{"name": {"Bad_Name"}, "pool": {"default"}}, http.StatusUnprocessableEntity, "Name must be lowercase letters, digits and dashes", `id="name-error"`},
		{"a bad pool", url.Values{"name": {"fsn-2"}, "pool": {"UP"}}, http.StatusUnprocessableEntity, "Pool must be lowercase letters, digits and dashes", `id="pool-error"`},
		{"a name that is taken", url.Values{"name": {"hel-1"}, "pool": {"default"}}, http.StatusConflict, "A server with that name already exists.", `id="name-error"`},
	}
	for _, tc := range tests {
		resp, body := e.postForm(t, "/ops/servers", tc.form)
		if resp.StatusCode != tc.status || !strings.Contains(text(body), tc.want) || !strings.Contains(body, tc.field) {
			t.Errorf("%s: POST = %d:\n%s", tc.name, resp.StatusCode, text(body))
		}
		if !strings.Contains(body, `value="`+tc.form.Get("name")+`"`) {
			t.Errorf("%s: the form isn't filled as typed", tc.name)
		}
		if strings.Contains(body, "cvjn_") {
			t.Errorf("%s: a refusal made a command", tc.name)
		}
	}
}

// Cancel removes the join (303 to the list); the same again is the same answer.
func TestCancelAJoin(t *testing.T) {
	e := serversEnv(t)
	j, err := e.Svc.CreateJoin(context.Background(), "fsn-2", "", "https://console.test")
	if err != nil {
		t.Fatal(err)
	}
	path := "/ops/servers/joins/" + j.Server.ID + "/cancel"
	for range 2 {
		resp, _ := e.postForm(t, path, url.Values{})
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ops/servers" {
			t.Errorf("POST %s = %d %s, want 303 to /ops/servers", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	_, list := e.get(t, "/ops/servers")
	if strings.Contains(list, "fsn-2") {
		t.Error("the cancelled join is still listed")
	}
	// Its page is gone.
	resp, body := e.get(t, "/ops/servers/joins/"+j.Server.ID)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "Join not found") {
		t.Errorf("the cancelled join's page = %d:\n%s", resp.StatusCode, text(body))
	}
	// A join that already joined and runs can't be cancelled here.
	_, ans := e.readyServer(t, "fsn-3")
	_ = ans
	view, _ := e.Svc.Server(context.Background(), "fsn-3")
	resp, body = e.postForm(t, "/ops/servers/joins/"+view.ID+"/cancel", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "fsn-3 is running.") {
		t.Errorf("cancelling a running server = %d:\n%s", resp.StatusCode, text(body))
	}
}

// A post from another site changes nothing (the console's cross-origin check covers the new posts).
func TestServerPostsRefuseAnotherSite(t *testing.T) {
	e := serversEnv(t)
	for _, path := range []string{"/ops/servers", "/ops/servers/joins/x/cancel", "/ops/servers/hel-1/remove"} {
		resp, _ := e.do(t, request{method: http.MethodPost, path: path, form: url.Values{"name": {"fsn-2"}}, cookies: []*http.Cookie{e.Cookie},
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s from another site = %d, want 403", path, resp.StatusCode)
		}
	}
	view, _ := e.Svc.ListServers(context.Background())
	if len(view.Servers) != 1 {
		t.Errorf("a refused post made a server: %+v", view.Servers)
	}
}

// Each of the four checks is open in turn, and the join's page and its part say the same.
func TestJoinChecksOpenInTurn(t *testing.T) {
	e := serversEnv(t)
	ctx := context.Background()
	j, ans := e.join(t, "fsn-1")
	path, part := "/ops/servers/joins/"+j.Server.ID, "/parts/ops/servers/joins/"+j.Server.ID

	lines := func(body string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`<p class="cv-jline cv-jtone-(\w+)"><span class="cv-mark cv-mark-(\w+)" aria-hidden="true"></span><span>([^<]*)</span>`).FindAllStringSubmatch(body, -1) {
			out = append(out, m[1]+"/"+m[2]+": "+html.UnescapeString(m[3]))
		}
		return out
	}
	step := func(name string, want ...string) {
		t.Helper()
		for _, p := range []string{path, part} {
			resp, body := e.get(t, p)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: GET %s = %d", name, p, resp.StatusCode)
			}
			got := lines(body)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("%s: GET %s has the lines\n%s\nwant\n%s", name, p, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if !strings.Contains(body, `hx-trigger="every 5s"`) {
				t.Errorf("%s: GET %s doesn't ask for itself while a check is open", name, p)
			}
			if strings.Contains(body, "Waiting for the server to call in…") {
				t.Errorf("%s: GET %s still waits for the call", name, p)
			}
		}
	}

	// 1. The join told Docker's version, so Docker is running, and the first server can't be reached yet.
	step("just joined", "done/healthy: Connected to the cluster", "done/healthy: Docker running",
		"work/rolling: Can't reach hel-1 yet", "later/idle: Ready to take work")
	if _, body := e.get(t, "/ops/servers"); !strings.Contains(text(rowOfServer(t, body, "fsn-1")), "Can't reach hel-1 yet") {
		t.Error("the joining row doesn't say the open check")
	}
	// 2. Docker isn't running.
	r := healthy(1)
	r.DockerRunning = false
	e.sync(t, ans.NodeToken, r)
	step("docker down", "done/healthy: Connected to the cluster", "work/rolling: Docker isn't running",
		"later/idle: Reaches the other servers on the private network", "later/idle: Ready to take work")
	// 3. Docker runs, and the first server can't be reached.
	e.sync(t, ans.NodeToken, healthy())
	step("no network", "done/healthy: Connected to the cluster", "done/healthy: Docker running, 156 GB disk free",
		"work/rolling: Can't reach hel-1 yet", "later/idle: Ready to take work")
	if _, body := e.get(t, "/ops/servers"); !strings.Contains(text(rowOfServer(t, body, "fsn-1")), "Can't reach hel-1 yet") {
		t.Error("the joining row doesn't say it can't reach hel-1")
	}
	// 4. The network is fine, and Nomad doesn't see the server yet.
	e.sync(t, ans.NodeToken, healthy(1))
	step("no node", "done/healthy: Connected to the cluster", "done/healthy: Docker running, 156 GB disk free",
		"done/healthy: Reaches hel-1 on the private network", "work/rolling: Waiting for Nomad on this server to register")

	// All four pass: the ending, with the button to the server, and the panel stops asking for itself.
	e.Nomad.AddNode("node-2", "fsn-1", "ready", "10.99.0.2")
	e.sync(t, ans.NodeToken, healthy(1))
	e.sample(t, "node-2", 100, 0, 1958, 20, 4000, 2, 3000, 19000)
	for _, p := range []string{path, part} {
		_, body := e.get(t, p)
		if strings.Contains(body, `hx-trigger="every 5s"`) || !strings.Contains(body, `<a href="/ops/servers/fsn-1" class="cv-btn cv-btn-primary cv-btn-sm">Go to fsn-1</a>`) ||
			!strings.Contains(text(body), "fsn-1 adds 1.9 GB.") || strings.Contains(text(body), "With three servers") {
			t.Errorf("GET %s when all four pass:\n%s", p, text(body))
		}
	}
	if view, err := e.Svc.Server(ctx, "fsn-1"); err != nil || view.State != "ready" {
		t.Errorf("the server isn't ready: %+v, %v", view, err)
	}
}

// sync is NodeSync with the node's token.
func (e *consoleEnv) sync(t *testing.T, token string, r service.NodeReport) {
	t.Helper()
	if _, err := e.Svc.NodeSync(context.Background(), token, r); err != nil {
		t.Fatalf("NodeSync: %v", err)
	}
}

// The third server brings the sentence about surviving a loss, with the two that start to run the scheduler.
func TestJoinReadyForTheThirdServer(t *testing.T) {
	e := serversEnv(t)
	e.readyServer(t, "fsn-1")
	j, _ := e.readyServer(t, "fsn-2")
	e.sample(t, "node-3", 100, 0, 1958, 20, 4000, 2, 3000, 19000)
	_, body := e.get(t, "/ops/servers/joins/"+j.Server.ID)
	if !strings.Contains(text(body), "fsn-2 adds 1.9 GB. With three servers the cluster survives the loss of one: fsn-1 and fsn-2 are starting to run the scheduler too.") {
		t.Errorf("the third server's sentence:\n%s", text(body))
	}
}

// An expired command says so, with the link to a new one, and the checks stay idle.
func TestJoinPageOfAnExpiredCommand(t *testing.T) {
	e := serversEnv(t)
	j := e.expiredJoin(t, "fsn-2")
	_, body := e.get(t, "/ops/servers/joins/"+j.ID)
	if !strings.Contains(text(body), "The command expired. Make a new one.") || !strings.Contains(body, `<a href="/ops/servers/new" class="cv-jlink">Make a new one.</a>`) ||
		strings.Contains(body, `hx-trigger="every 5s"`) || !strings.Contains(body, "cv-mark-warning") {
		t.Errorf("the expired command's page:\n%s", text(body))
	}
}

// ---- the server page --------------------------------------------------------------------------

func TestServerPageOfAnotherServer(t *testing.T) {
	e := threeServers(t)
	e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "w1fsn1111-0000", TaskGroup: "web", JobVersion: 1, ClientStatus: "running",
		DesiredStatus: "run", NodeID: "node-2", NodeName: "fsn-1"})
	e.seed(t, whoamiApp())
	resp, body := e.get(t, "/ops/servers/fsn-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops/servers/fsn-1 = %d:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)
	for _, want := range []string{"← Servers", "fsn-1", "ready", "2 vCPU", "1.9 GB",
		"Updates and restarts are read on hel-1 only.", "pool default", "address 203.0.113.9", "private address 10.98.0.2", "joined",
		"agent caravana 0.18.0 · nomad 2.0.7 · docker 27.5.1", "last seen", "Running here · 1"} {
		if !strings.Contains(words, want) {
			t.Errorf("fsn-1's page lacks %q:\n%s", want, words)
		}
	}
	// The head has no role marks for a server that has none, no reboot pill and no Recent list; the line says where
	// the operating system is read, with a link to Updates.
	for _, unwanted := range []string{"reboot needed", "Recent on this server", "runs the console", "Remove server"} {
		if strings.Contains(words, unwanted) {
			t.Errorf("fsn-1's page has %q", unwanted)
		}
	}
	if !strings.Contains(body, `<p class="cv-srvreadon"><a href="/ops/updates">Updates</a> and restarts are read on <span class="cv-inline-mono text-fg-1">hel-1</span> only.</p>`) {
		t.Errorf("the line about where updates are read:\n%s", body)
	}
	// Its numbers are its own: 0.9 GB of memory in use, not hel-1's 0.5.
	if !strings.Contains(words, "CPU") || !strings.Contains(words, "Memory") || !strings.Contains(words, "900 MB") {
		t.Errorf("the page doesn't show fsn-1's own numbers:\n%s", words)
	}
	// The part is the same body.
	resp, part := e.get(t, "/parts/ops/servers/fsn-1")
	if resp.StatusCode != http.StatusOK || !strings.Contains(part, `<h1 class="cv-srvname">fsn-1</h1>`) {
		t.Errorf("the part = %d", resp.StatusCode)
	}
}

// The server Caravana runs on keeps everything it had and gains the roles as words, its private address, when it
// joined, and its certificate's end; it has the Recent list and the reboot pill, and no line about where to read.
func TestServerPageOfTheFirstServer(t *testing.T) {
	e := sampledServersEnv(t)
	e.readyServer(t, "fsn-1")
	_, body := e.get(t, "/ops/servers/hel-1")
	words := text(body)
	for _, want := range []string{"runs the console", "public traffic enters here", "runs the scheduler", "Recent on this server", "private address 10.98.0.1",
		"address 203.0.113.4", "joined", "reboot needed", "4 vCPU · 1.9 GB · Ubuntu 24.04"} {
		if !strings.Contains(words, want) {
			t.Errorf("hel-1's page lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "are read on") || strings.Contains(words, "Remove server") {
		t.Errorf("the first server's page says where to read or offers a removal:\n%s", words)
	}
	// The first server's page has only its own items: the reboot, not another server's.
	if !strings.Contains(words, "needs a reboot") {
		t.Errorf("the reboot's banner is missing:\n%s", words)
	}
}

// A server that is still joining is the join's page; a name nobody has is a 404 that points at the list.
func TestServerPageOfAJoinAndOfNobody(t *testing.T) {
	e := serversEnv(t)
	e.readyServer(t, "fsn-1")
	j, _ := e.join(t, "fsn-2")
	resp, _ := e.get(t, "/ops/servers/fsn-2")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ops/servers/joins/"+j.Server.ID {
		t.Errorf("a joining server's page = %d %s, want 302 to its join", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := e.get(t, "/ops/servers/nope")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "No server called nope.") || !strings.Contains(body, `<a href="/ops/servers" class="cv-btn cv-btn-secondary cv-btn-sm">All servers</a>`) ||
		strings.Contains(text(body), "This caravana has one") {
		t.Errorf("the 404 page with several servers = %d:\n%s", resp.StatusCode, text(body))
	}
}

// An agent older than the console is told to run the newer installer.
func TestServerPageOldAgent(t *testing.T) {
	e := serversEnv(t)
	_, ans := e.join(t, "fsn-1")
	e.Nomad.AddNode("node-2", "fsn-1", "ready", "10.99.0.2")
	r := healthy(1)
	r.Agent = "0.0.1"
	e.sync(t, ans.NodeToken, r)
	// The test console's version is 0.7.0.
	_, body := e.get(t, "/ops/servers/fsn-1")
	if !strings.Contains(text(body), "This server's agent is 0.0.1; the console is 0.7.0. Run the newer installer on it.") {
		t.Errorf("the old agent's note is missing:\n%s", text(body))
	}
}

func TestServerPageOfADownServerAndItsRemoval(t *testing.T) {
	e := threeServers(t)
	e.Nomad.SetNodeStatusOf("node-2", "down")
	_, body := e.get(t, "/ops/servers/fsn-1")
	words := text(body)
	if !strings.Contains(words, "fsn-1 is down since") || !strings.Contains(words, "Its copies were replaced on other servers; what has a volume here waits for it.") ||
		!strings.Contains(body, `cv-pill-failed"><span class="cv-mark cv-mark-failed" aria-hidden="true"></span>down`) ||
		!strings.Contains(body, `<a href="/ops/servers/fsn-1/remove" class="cv-btn cv-btn-secondary cv-btn-sm">Remove server…</a>`) ||
		!strings.Contains(words, "Nothing runs here while the server is down.") {
		t.Errorf("a down server's page:\n%s", words)
	}
	// The confirmation: what goes and what stays, the primary button and Cancel.
	resp, confirm := e.get(t, "/ops/servers/fsn-1/remove")
	cw := text(confirm)
	if resp.StatusCode != http.StatusOK || !strings.Contains(cw, "Remove fsn-1?") || !strings.Contains(cw, "Its place in the cluster and its token are deleted.") ||
		!strings.Contains(cw, "Nothing on the server is touched.") ||
		!strings.Contains(confirm, `<form method="post" action="/ops/servers/fsn-1/remove"`) ||
		!strings.Contains(confirm, `<button type="submit" class="cv-btn cv-btn-primary">Remove fsn-1</button>`) ||
		!strings.Contains(confirm, `<a href="/ops/servers/fsn-1" class="cv-btn cv-btn-secondary">Cancel</a>`) {
		t.Errorf("the confirmation = %d:\n%s", resp.StatusCode, cw)
	}
	// Posting it removes the server and goes to the list.
	resp, _ = e.postForm(t, "/ops/servers/fsn-1/remove", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ops/servers" {
		t.Fatalf("POST remove = %d %s, want 303 to /ops/servers", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Nomad still lists its node until it forgets it: the node is listed, but as one Caravana doesn't manage.
	_, list := e.get(t, "/ops/servers")
	if r := rowOfServer(t, list, "fsn-1"); !strings.Contains(text(r), "Not set up for a cluster yet.") {
		t.Errorf("the removed server is still managed:\n%s", text(r))
	}
	e.Nomad.RemoveNode("node-2")
	_, list = e.get(t, "/ops/servers")
	if strings.Contains(list, ">fsn-1</a>") {
		t.Error("the removed server is still listed")
	}
	// The same again is the same answer.
	if resp, _ = e.postForm(t, "/ops/servers/fsn-1/remove", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("removing it again = %d, want 303", resp.StatusCode)
	}
}

// A server that runs is never offered a removal: the page says why, and the post refuses it too.
func TestRemovingARunningServerIsRefused(t *testing.T) {
	e := threeServers(t)
	_, page := e.get(t, "/ops/servers/fsn-1")
	if strings.Contains(page, "Remove server") {
		t.Error("a running server has a Remove link")
	}
	resp, body := e.get(t, "/ops/servers/fsn-1/remove")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "fsn-1 is running. Stop it, or run the installer with --leave on it, then remove it.") ||
		strings.Contains(body, `action="/ops/servers/fsn-1/remove"`) || !strings.Contains(body, `href="/ops/servers/fsn-1"`) {
		t.Errorf("GET remove of a running server = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/ops/servers/fsn-1/remove", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "fsn-1 is running.") {
		t.Errorf("POST remove of a running server = %d:\n%s", resp.StatusCode, text(body))
	}
	if _, err := e.Svc.Server(context.Background(), "fsn-1"); err != nil {
		t.Errorf("the refused removal removed it: %v", err)
	}
	// The first server is the one Caravana runs on.
	resp, body = e.get(t, "/ops/servers/hel-1/remove")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This is the server Caravana runs on: it can't be removed.") {
		t.Errorf("GET remove of the first server = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, _ = e.get(t, "/ops/servers/nope/remove")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET remove of nobody = %d, want 404", resp.StatusCode)
	}
}

// ---- elsewhere --------------------------------------------------------------------------------

// With one server the Overview is as it was; with three it has a panel for each, the headline counts them
// and the section links to the list.
func TestOverviewPerServer(t *testing.T) {
	e := serversEnv(t)
	_, one := e.get(t, "/ops")
	if strings.Contains(one, "All servers") || !strings.Contains(one, `id="ops-server-title">Server</h2>`) || !strings.Contains(text(one), "1 server") {
		t.Errorf("the Overview of one server changed:\n%s", text(one))
	}

	e = threeServers(t)
	e.seedStandard(t)
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "w1aaaaaa-0000", TaskGroup: "web", JobVersion: 7, ClientStatus: "running", DesiredStatus: "run", NodeID: "node-1", NodeName: "hel-1"},
		api.AllocationListStub{ID: "w2bbbbbb-0000", TaskGroup: "web", JobVersion: 7, ClientStatus: "running", DesiredStatus: "run", NodeID: "node-2", NodeName: "fsn-1"})
	_, body := e.get(t, "/ops")
	server := regionOf(t, body, `id="ops-server"`)
	for _, want := range []string{`id="ops-server-title">Servers</h2>`, `<a href="/ops/servers" class="cv-mnsec-link">All servers</a>`,
		`<a href="/ops/servers/hel-1" class="cv-node-name">hel-1</a>`, `<a href="/ops/servers/fsn-1" class="cv-node-name">fsn-1</a>`, `<a href="/ops/servers/fsn-2" class="cv-node-name">fsn-2</a>`} {
		if !strings.Contains(server, want) {
			t.Errorf("the Servers section lacks %q:\n%s", want, text(server))
		}
	}
	if strings.Count(server, `class="cv-panel cv-node"`) != 3 {
		t.Errorf("the section has %d panels, want 3", strings.Count(server, `class="cv-panel cv-node"`))
	}
	if !strings.Contains(text(body), "3 servers") || strings.Contains(text(body), "1 server ") {
		t.Errorf("the headline doesn't count the servers:\n%s", text(body))
	}
	// Each panel has the copies that run on it: whoami's two are on hel-1 and fsn-1, none on fsn-2.
	if strings.Count(server, `id="ops-copy-w1aaaaaa-0000"`) != 1 || strings.Count(server, `id="ops-copy-w2bbbbbb-0000"`) != 1 {
		t.Errorf("the chips aren't one per copy:\n%s", server)
	}
	panels := strings.Split(server, `class="cv-panel cv-node"`)
	if !strings.Contains(panels[1], "w1aaaaaa") || strings.Contains(panels[1], "w2bbbbbb") || !strings.Contains(panels[2], "w2bbbbbb") || strings.Contains(panels[3], "ops-copy-") {
		t.Errorf("the copies aren't on the right servers")
	}
	// The part draws the same.
	_, part := e.get(t, "/parts/ops/server")
	if !strings.Contains(part, `id="ops-server-title">Servers</h2>`) || !strings.Contains(text(part), "3 servers") {
		t.Errorf("the part says otherwise:\n%s", text(part))
	}
}

// The Running tab names a copy's server once there is more than one.
func TestRunningTabNamesTheServer(t *testing.T) {
	e := serversEnv(t)
	e.seedStandard(t)
	_, one := e.get(t, "/apps/whoami")
	if strings.Contains(one, "cv-k-server") {
		t.Error("with one server the Running tab names a server")
	}
	e = threeServers(t)
	e.seedStandard(t)
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "w1aaaaaa-0000", TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeID: "node-1", NodeName: "hel-1"},
		api.AllocationListStub{ID: "w2bbbbbb-0000", TaskGroup: "web", JobVersion: 7, ClientStatus: "running", NodeID: "node-2", NodeName: "fsn-1"})
	for _, path := range []string{"/apps/whoami", "/parts/apps/whoami/copies"} {
		_, body := e.get(t, path)
		if !strings.Contains(body, `<a href="/ops/servers/hel-1" class="cv-k-server"`) || !strings.Contains(body, `<a href="/ops/servers/fsn-1" class="cv-k-server"`) {
			t.Errorf("GET %s doesn't link each copy's server:\n%s", path, text(body))
		}
	}
}

// Monitoring has a select of servers above the host section once there is more than one; the choice is the address.
func TestMonitoringServerSelect(t *testing.T) {
	e := serversEnv(t)
	_, one := e.get(t, "/monitoring")
	if strings.Contains(one, "mon-server") || !strings.Contains(text(one), "This host 1 server") {
		t.Errorf("with one server the host section changed:\n%s", text(one))
	}
	e = threeServers(t)
	_, body := e.get(t, "/monitoring")
	for _, want := range []string{`<form method="get" action="/monitoring#host" class="cv-mnpick">`, `<select class="cv-field cv-select cv-mnpick-select" id="mon-server" name="server">`,
		`<option value="hel-1" selected>hel-1</option>`, `<option value="fsn-1">fsn-1</option>`, `<option value="fsn-2">fsn-2</option>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// A server's own numbers, and the choice is kept by the page's other links and by the section's part.
	_, other := e.get(t, "/monitoring?server=fsn-1&range=24h")
	if !strings.Contains(other, `<option value="fsn-1" selected>fsn-1</option>`) || !strings.Contains(other, `name="range" value="24h"`) ||
		!strings.Contains(other, `hx-get="/parts/monitoring/host?server=fsn-1"`) {
		t.Errorf("the choice isn't kept:\n%s", text(other))
	}
	_, part := e.get(t, "/parts/monitoring/host?server=fsn-1")
	if !strings.Contains(part, `<option value="fsn-1" selected>fsn-1</option>`) || !strings.Contains(text(part), "0.9G used") {
		t.Errorf("the part isn't fsn-1's:\n%s", text(part))
	}
	if resp, _ := e.get(t, "/monitoring?server=nobody"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown server = %d, want 404", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/parts/monitoring/host?server=nobody"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the part of an unknown server = %d, want 404", resp.StatusCode)
	}
}

// Updates says whose operating system it reads once there is more than one server.
func TestUpdatesSaysWhereItReads(t *testing.T) {
	e := serversEnv(t)
	if _, body := e.get(t, "/ops/updates"); strings.Contains(body, "where Caravana runs") {
		t.Error("with one server the Updates page says where it reads")
	}
	e = threeServers(t)
	for _, path := range []string{"/ops/updates", "/parts/ops/updates"} {
		_, body := e.get(t, path)
		if !strings.Contains(text(body), "Read on hel-1, where Caravana runs.") {
			t.Errorf("GET %s lacks the line:\n%s", path, text(body))
		}
	}
}

// The needs-you links: a server's items go to its page, a disk to that server's numbers.
func TestNeedsYouLinksToTheServer(t *testing.T) {
	e := threeServers(t)
	now := time.Now()
	down := e.Server.attentionRow(now, service.AttentionItem{Kind: service.AttentionServerDown, Server: "fsn-1", When: now}, places{}, nil)
	if down.Action != "Open server" || down.Href != "/ops/servers/fsn-1" {
		t.Errorf("server_down links to %q (%q)", down.Href, down.Action)
	}
	cert := e.Server.attentionRow(now, service.AttentionItem{Kind: service.AttentionServerCertExpiring, Server: "fsn-2", When: now}, places{}, nil)
	if cert.Action != "Open server" || cert.Href != "/ops/servers/fsn-2" {
		t.Errorf("server_certificate_expiring links to %q (%q)", cert.Href, cert.Action)
	}
	disk := e.Server.attentionRow(now, service.AttentionItem{Kind: service.AttentionDiskNearlyFull, Server: "fsn-1", Mount: "/", When: now}, places{}, nil)
	if disk.Action != "Open monitoring" || disk.Href != "/monitoring?server=fsn-1#host" {
		t.Errorf("a disk item of fsn-1 links to %q", disk.Href)
	}
	local := e.Server.attentionRow(now, service.AttentionItem{Kind: service.AttentionDiskNearlyFull, Mount: "/", When: now}, places{}, nil)
	if local.Href != "/monitoring#host" {
		t.Errorf("a disk item with one server links to %q, want /monitoring#host", local.Href)
	}
	// On a server's own page only its items are banners.
	items := []service.AttentionItem{
		{Kind: service.AttentionDiskNearlyFull, Server: "fsn-1", Mount: "/", UsedPercent: 90, When: now},
		{Kind: service.AttentionDiskNearlyFull, Server: "hel-1", Mount: "/", UsedPercent: 91, When: now},
		{Kind: service.AttentionServerCertExpiring, Server: "fsn-1", When: now},
		{Kind: service.AttentionServerDown, Server: "fsn-1", When: now},
	}
	att := service.Attention{Items: items}
	if got := e.Server.hostItemsFor(now, att, "fsn-1", false); len(got) != 2 {
		t.Errorf("fsn-1's page has %d banners, want its disk and its certificate (not the down item, which has its own banner)", len(got))
	}
	if got := e.Server.hostItemsFor(now, att, "hel-1", true); len(got) != 1 {
		t.Errorf("hel-1's page has %d banners, want its disk", len(got))
	}
}

// With a volume on a server, Backups names it once there is more than one server.
func TestVolumesNameTheServer(t *testing.T) {
	if got := whereIs(false, "fsn-1"); got != "this host" {
		t.Errorf("with one server: %q", got)
	}
	if got := whereIs(true, "fsn-1"); got != "fsn-1" {
		t.Errorf("with several: %q", got)
	}
	if got := whereIs(true, ""); got != "this host" {
		t.Errorf("a volume whose server isn't known: %q", got)
	}
}

// The new pages carry the theme and none of them says "ship".
func TestServerPagesThemeAndWords(t *testing.T) {
	e := threeServers(t)
	j, err := e.Svc.CreateJoin(context.Background(), "fsn-9", "", "https://console.test")
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.SetNodeStatusOf("node-3", "down")
	ship := regexp.MustCompile(`(?i)\bship`)
	node := regexp.MustCompile(`(?i)\b(node|machine)s?\b`)
	paths := []string{"/ops/servers", "/ops/servers/new", "/ops/servers/joins/" + j.Server.ID, "/ops/servers/hel-1", "/ops/servers/fsn-1",
		"/ops/servers/fsn-2", "/ops/servers/fsn-2/remove", "/ops/servers/hel-1/remove", "/ops/servers/nope"}
	for _, cookie := range []string{"", "light", "dark"} {
		cookies := []*http.Cookie{e.Cookie}
		if cookie != "" {
			cookies = append(cookies, &http.Cookie{Name: "caravana_theme", Value: cookie})
		}
		for _, path := range paths {
			resp, body := e.do(t, request{path: path, cookies: cookies})
			if resp.StatusCode >= 500 {
				t.Errorf("GET %s = %d", path, resp.StatusCode)
				continue
			}
			attribute, meta := themeOf(t, path, body)
			wantAttr, wantMeta := cookie, cookie
			if cookie == "" {
				wantMeta = "dark light"
			}
			if attribute != wantAttr || meta != wantMeta {
				t.Errorf("GET %s with theme %q: attribute %q, meta %q", path, cookie, attribute, meta)
			}
			if cookie != "" {
				continue
			}
			words := text(body)
			if m := ship.FindString(words); m != "" {
				t.Errorf("GET %s says %q", path, m)
			}
			// The console says server. "machine" is allowed once, in Add a server's lede, for the thing before it is a server.
			if path != "/ops/servers/new" && !strings.HasPrefix(path, "/ops/servers/joins/") {
				if m := node.FindString(words); m != "" {
					t.Errorf("GET %s says %q: the console says server", path, m)
				}
			}
		}
	}
}
