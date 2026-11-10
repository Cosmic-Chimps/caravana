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

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	sentinelCAKey   = "sentinel-ca-key-0031"
	sentinelGossip  = "c2VudGluZWwtZ29zc2lwLTAwMzEhISEhISEhISE=" // base64 of 32 bytes, ends the variable with a newline below
	firstServerName = "hel-1"
)

// clusterEnv is an env with a cluster set up as the installer leaves it: the CA and the gossip key in
// Nomad Variables and the first server adopted (index 1, name hel-1, app address 10.99.0.1).
type clusterEnv struct {
	*env
	caCert, caKey []byte
	firstToken    string
}

func newClusterEnv(t *testing.T) *clusterEnv {
	t.Helper()
	e := &clusterEnv{env: newEnv(t)}
	var err error
	e.caCert, e.caKey, err = clusterca.New(e.svc.clock.Now())
	if err != nil {
		t.Fatalf("make a CA: %v", err)
	}
	e.nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(e.caCert), "key": string(e.caKey)})
	e.nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": sentinelGossip + "\n"})
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	e.firstToken, err = e.svc.AdoptFirst(context.Background(), firstServerName, pub, "203.0.113.4")
	if err != nil {
		t.Fatalf("AdoptFirst: %v", err)
	}
	e.nomad.AddNode("node-1", firstServerName, "ready", "10.99.0.1")
	return e
}

// newJoin makes a join and returns its token.
func (e *clusterEnv) newJoin(t *testing.T, name string) ServerJoin {
	t.Helper()
	j, err := e.svc.CreateJoin(context.Background(), name, "", "https://console.test")
	if err != nil {
		t.Fatalf("CreateJoin(%q): %v", name, err)
	}
	return j
}

// joinRequest is a valid join with a fresh key pair and CSR.
func joinRequest(t *testing.T, token, hostname string) JoinRequest {
	t.Helper()
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	_, csr, err := clusterca.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	return JoinRequest{Token: token, PublicKey: pub, CSR: string(csr), Facts: NodeFacts{Hostname: hostname, Docker: "27.5.1", Agent: "0.18.0"}}
}

func (e *clusterEnv) join(t *testing.T, name, hostname string) (ServerJoin, NodeAnswer) {
	t.Helper()
	j := e.newJoin(t, name)
	ans, err := e.svc.Join(context.Background(), joinRequest(t, j.Token, hostname), "203.0.113.9")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	return j, ans
}

// healthyReport is a report that makes every check pass for a server that has peers at others.
func healthyReport(others ...int) NodeReport {
	r := NodeReport{Agent: "0.18.0", Nomad: "2.0.7", Docker: "27.5.1", DockerRunning: true, DiskFreeMB: 160000, DiskTotalMB: 190000,
		MemoryMB: 3916, Kernel: "6.8.0", Role: "client", Reach: []NodeReach{}}
	for _, o := range others {
		r.Reach = append(r.Reach, NodeReach{Index: o, OK: true})
	}
	return r
}

func TestClusterStatus(t *testing.T) {
	ctx := context.Background()
	t.Run("a host that wasn't turned into a first server isn't ready", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.ClusterStatus(ctx); !errors.Is(err, ErrClusterNotReady) {
			t.Errorf("ClusterStatus = %v, want ErrClusterNotReady", err)
		}
		if _, err := e.svc.CreateJoin(ctx, "", "", ""); !errors.Is(err, ErrClusterNotReady) {
			t.Errorf("CreateJoin = %v, want ErrClusterNotReady", err)
		}
		if !strings.HasPrefix(ErrClusterNotReady.Error(), "Run the installer again on this server first:") {
			t.Errorf("the sentence changed: %q", ErrClusterNotReady)
		}
	})
	t.Run("a first server without the CA isn't ready either", func(t *testing.T) {
		e := newClusterEnv(t)
		e.nomad.SetVariable("caravana/cluster/ca", nil)
		if err := e.svc.ClusterStatus(ctx); !errors.Is(err, ErrClusterNotReady) {
			t.Errorf("ClusterStatus = %v, want ErrClusterNotReady", err)
		}
	})
	t.Run("ready", func(t *testing.T) {
		e := newClusterEnv(t)
		if err := e.svc.ClusterStatus(ctx); err != nil {
			t.Errorf("ClusterStatus = %v", err)
		}
	})
}

func TestCreateJoin(t *testing.T) {
	ctx := context.Background()

	t.Run("the command, the token's hash and the hour", func(t *testing.T) {
		e := newClusterEnv(t)
		e.svc.SetVersion("0.18.0")
		j := e.newJoin(t, "fsn-2")
		if !strings.HasPrefix(j.Token, "cvjn_") || len(j.Token) != 5+43 {
			t.Errorf("token = %q", j.Token)
		}
		want := "curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/download/v0.18.0/install.sh | sudo bash -s -- --join " + j.Token + " --url https://console.test"
		if j.Command != want || j.CommandNote != "" {
			t.Errorf("command = %q (note %q), want %q", j.Command, j.CommandNote, want)
		}
		if got := j.ExpiresAt.Sub(e.svc.clock.Now()); got != time.Hour {
			t.Errorf("expires in %v, want 1h", got)
		}
		row, err := e.store.ServerByID(ctx, j.Server.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.JoinHash != hashToken(j.Token) || row.State != store.ServerWaiting || row.Name != "fsn-2" || row.Pool != "default" {
			t.Errorf("row = %+v", row)
		}
		if strings.Contains(row.JoinHash+row.TokenHash+row.Report, j.Token) {
			t.Error("the token is stored")
		}
		if j.Server.State != "waiting" || j.Server.Index != 2 || j.Server.ExpiresAt.IsZero() {
			t.Errorf("server = %+v", j.Server)
		}
	})
	t.Run("the public URL wins, and insecure follows a console with no certificates", func(t *testing.T) {
		e := newClusterEnv(t)
		e.svc.SetVersion("v0.18.0")
		e.svc.SetPublicURL("https://caravana.example.com/")
		e.svc.SetACME("none", "")
		j := e.newJoin(t, "")
		if !strings.HasSuffix(j.Command, "--url https://caravana.example.com --insecure") || !strings.Contains(j.Command, "/v0.18.0/install.sh") {
			t.Errorf("command = %q", j.Command)
		}
	})
	t.Run("a development build has no download", func(t *testing.T) {
		e := newClusterEnv(t)
		e.svc.SetVersion("dev")
		j := e.newJoin(t, "")
		if !strings.HasPrefix(j.Command, "sudo bash install.sh --from <the release folder> --join cvjn_") || j.CommandNote == "" {
			t.Errorf("command = %q, note = %q", j.Command, j.CommandNote)
		}
	})
	t.Run("names", func(t *testing.T) {
		e := newClusterEnv(t)
		for _, bad := range []string{"Fsn 2", "-x", strings.Repeat("a", 64), "new", "joins"} {
			var fe *app.FieldError
			if _, err := e.svc.CreateJoin(ctx, bad, "", ""); !errors.As(err, &fe) || fe.Field != "name" {
				t.Errorf("CreateJoin(%q) = %v, want a field error on name", bad, err)
			}
		}
		var fe *app.FieldError
		if _, err := e.svc.CreateJoin(ctx, "ok", "Bad Pool", ""); !errors.As(err, &fe) || fe.Field != "pool" {
			t.Errorf("a bad pool = %v", err)
		}
		e.newJoin(t, "fsn-2")
		for _, taken := range []string{"fsn-2", firstServerName} {
			var re *RefusalError
			if _, err := e.svc.CreateJoin(ctx, taken, "", ""); !errors.Is(err, ErrServerExists) || !errors.As(err, &re) || re.Field != "name" {
				t.Errorf("CreateJoin(%q) = %v, want ErrServerExists on name", taken, err)
			}
		}
		// A Nomad node's name is taken too.
		e.nomad.AddNode("node-x", "lab-node", "ready", "")
		if _, err := e.svc.CreateJoin(ctx, "lab-node", "", ""); !errors.Is(err, ErrServerExists) {
			t.Errorf("a Nomad node's name = %v, want ErrServerExists", err)
		}
	})
	t.Run("expired joins are cleaned up first", func(t *testing.T) {
		e := newClusterEnv(t)
		old := e.newJoin(t, "old")
		e.svc.clock.(*fakeClock).Set(e.svc.clock.Now().Add(2 * time.Hour))
		e.newJoin(t, "fresh")
		if _, err := e.store.ServerByID(ctx, old.Server.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("the expired join is still stored: %v", err)
		}
	})
	t.Run("a full cluster", func(t *testing.T) {
		e := newClusterEnv(t)
		for i := 2; i <= 63; i++ {
			e.newJoin(t, "")
		}
		if _, err := e.svc.CreateJoin(ctx, "", "", ""); !errors.Is(err, ErrClusterFull) {
			t.Errorf("the 64th = %v, want ErrClusterFull", err)
		}
	})
}

func TestJoin(t *testing.T) {
	ctx := context.Background()

	t.Run("a join gives the server its place in the cluster", func(t *testing.T) {
		e := newClusterEnv(t)
		j := e.newJoin(t, "")
		req := joinRequest(t, j.Token, "FSN-2.example.com")
		ans, err := e.svc.Join(ctx, req, "203.0.113.9")
		if err != nil {
			t.Fatalf("Join: %v", err)
		}
		if ans.Name != "fsn-2" || ans.Index != 2 || ans.Pool != "default" || ans.Role != "client" {
			t.Errorf("answer = %+v", ans)
		}
		if !slices.Equal(ans.Servers, []int{1}) || len(ans.Peers) != 1 || ans.Peers[0].Index != 1 || ans.Peers[0].Endpoint != "203.0.113.4:51820" {
			t.Errorf("servers %v, peers %+v", ans.Servers, ans.Peers)
		}
		if !strings.HasPrefix(ans.NodeToken, "cvnd_") || len(ans.NodeToken) != 48 {
			t.Errorf("node token = %q", ans.NodeToken)
		}
		if !clusterca.Equal([]byte(ans.CA), e.caCert) {
			t.Error("the answer's CA isn't the cluster's")
		}
		cert, err := clusterca.ParseCertificate([]byte(ans.Certificate))
		if err != nil || cert.DNSNames[0] != "client.global.nomad" || cert.IPAddresses[1].String() != "10.98.0.2" {
			t.Errorf("certificate = %+v, %v", cert, err)
		}
		row, _ := e.store.ServerByName(ctx, "fsn-2")
		if row.State != store.ServerJoining || row.Endpoint != "203.0.113.9:51820" || row.PublicKey != req.PublicKey ||
			row.TokenHash != hashToken(ans.NodeToken) || row.JoinHash != "" {
			t.Errorf("row = %+v", row)
		}
		if strings.Contains(row.Report+row.TokenHash+row.JoinHash, ans.NodeToken) || strings.Contains(row.Report, req.CSR) {
			t.Error("a token or the CSR is stored")
		}
	})
	t.Run("the token works once", func(t *testing.T) {
		e := newClusterEnv(t)
		j, _ := e.join(t, "two", "")
		if _, err := e.svc.Join(ctx, joinRequest(t, j.Token, ""), "203.0.113.10"); !errors.Is(err, ErrJoinToken) {
			t.Errorf("second use = %v, want ErrJoinToken", err)
		}
		if !strings.HasPrefix(ErrJoinToken.Error(), "This join token isn't valid: it was used, expired, or never existed.") {
			t.Errorf("the sentence changed: %q", ErrJoinToken)
		}
	})
	t.Run("two racing joins with one token: one wins", func(t *testing.T) {
		e := newClusterEnv(t)
		j := e.newJoin(t, "")
		var wins, refused int
		var mu sync.Mutex
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			req := joinRequest(t, j.Token, "race")
			go func() {
				defer wg.Done()
				_, err := e.svc.Join(ctx, req, "203.0.113.9")
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					wins++
				} else if errors.Is(err, ErrJoinToken) {
					refused++
				} else {
					t.Errorf("Join: %v", err)
				}
			}()
		}
		wg.Wait()
		if wins != 1 || refused != 5 {
			t.Errorf("wins = %d, refused = %d; want 1 and 5", wins, refused)
		}
	})
	t.Run("an unknown, expired or malformed token", func(t *testing.T) {
		e := newClusterEnv(t)
		for _, tok := range []string{"", "cvjn_" + strings.Repeat("x", 43), "not a token"} {
			if _, err := e.svc.Join(ctx, joinRequest(t, tok, ""), "203.0.113.9"); !errors.Is(err, ErrJoinToken) {
				t.Errorf("Join(%q) = %v, want ErrJoinToken", tok, err)
			}
		}
		j := e.newJoin(t, "late")
		e.svc.clock.(*fakeClock).Set(e.svc.clock.Now().Add(61 * time.Minute))
		if _, err := e.svc.Join(ctx, joinRequest(t, j.Token, ""), "203.0.113.9"); !errors.Is(err, ErrJoinToken) {
			t.Errorf("an expired token = %v, want ErrJoinToken", err)
		}
	})
	t.Run("a mistake in the request leaves the token usable", func(t *testing.T) {
		e := newClusterEnv(t)
		j := e.newJoin(t, "")
		first, _ := e.store.ServerByName(ctx, firstServerName)
		cases := map[string]func(*JoinRequest){
			"publicKey":         func(r *JoinRequest) { r.PublicKey = "short" },
			"publicKey (taken)": func(r *JoinRequest) { r.PublicKey = first.PublicKey },
			"csr": func(r *JoinRequest) {
				r.CSR = "-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n"
			},
			"facts.hostname":     func(r *JoinRequest) { r.Facts.Hostname = strings.Repeat("h", 300) },
			"facts.memoryMb":     func(r *JoinRequest) { r.Facts.MemoryMB = -1 },
			"address":            func(r *JoinRequest) { r.Address = "evil.example.com" },
			"address (loopback)": func(r *JoinRequest) { r.Address = "127.0.0.1" },
		}
		for name, mutate := range cases {
			req := joinRequest(t, j.Token, "x")
			mutate(&req)
			var fe *app.FieldError
			_, err := e.svc.Join(ctx, req, "203.0.113.9")
			want, _, _ := strings.Cut(name, " ")
			if !errors.As(err, &fe) || fe.Field != want {
				t.Errorf("%s: Join = %v, want a field error on %s", name, err, want)
			}
		}
		if _, err := e.svc.Join(ctx, joinRequest(t, j.Token, "x"), "203.0.113.9"); err != nil {
			t.Errorf("the token after the mistakes: %v", err)
		}
	})
	t.Run("the address: given, else where the call came from, never a name", func(t *testing.T) {
		e := newClusterEnv(t)
		j := e.newJoin(t, "a")
		req := joinRequest(t, j.Token, "")
		req.Address = "198.51.100.7"
		if _, err := e.svc.Join(ctx, req, "203.0.113.9"); err != nil {
			t.Fatal(err)
		}
		if row, _ := e.store.ServerByName(ctx, "a"); row.Endpoint != "198.51.100.7:51820" {
			t.Errorf("endpoint = %q", row.Endpoint)
		}
		j = e.newJoin(t, "b")
		if _, err := e.svc.Join(ctx, joinRequest(t, j.Token, ""), "2001:db8::5"); err != nil {
			t.Fatal(err)
		}
		if row, _ := e.store.ServerByName(ctx, "b"); row.Endpoint != "[2001:db8::5]:51820" {
			t.Errorf("endpoint = %q", row.Endpoint)
		}
		j = e.newJoin(t, "c")
		var fe *app.FieldError
		if _, err := e.svc.Join(ctx, joinRequest(t, j.Token, ""), "2001:db8::/64"); !errors.As(err, &fe) || fe.Field != "address" {
			t.Errorf("an address that isn't one = %v", err)
		}
	})
	t.Run("a name left to the machine takes -2 while taken", func(t *testing.T) {
		e := newClusterEnv(t)
		_, a := e.join(t, "", "box.example.com")
		_, b := e.join(t, "", "box")
		_, c := e.join(t, "", "")
		if a.Name != "box" || b.Name != "box-2" || c.Name != "server-4" {
			t.Errorf("names = %q, %q, %q", a.Name, b.Name, c.Name)
		}
	})
	t.Run("without a CA nothing is spent", func(t *testing.T) {
		e := newClusterEnv(t)
		j := e.newJoin(t, "")
		e.nomad.SetVariable("caravana/cluster/ca", nil)
		if _, err := e.svc.Join(ctx, joinRequest(t, j.Token, ""), "203.0.113.9"); !errors.Is(err, ErrClusterNotReady) {
			t.Errorf("Join = %v, want ErrClusterNotReady", err)
		}
		e.nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(e.caCert), "key": string(e.caKey)})
		if _, err := e.svc.Join(ctx, joinRequest(t, j.Token, ""), "203.0.113.9"); err != nil {
			t.Errorf("Join after the CA is back = %v", err)
		}
	})
}

func TestNodeSyncGetsAServerReady(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	_, ans := e.join(t, "two", "")
	tok := ans.NodeToken

	t.Run("a token that isn't a server's", func(t *testing.T) {
		for _, bad := range []string{"", "cvnd_" + strings.Repeat("x", 43), e.firstToken + "x", "cvat_whatever", "cvjn_" + strings.Repeat("x", 43)} {
			if _, err := e.svc.NodeSync(ctx, bad, healthyReport()); !errors.Is(err, ErrNodeToken) {
				t.Errorf("NodeSync(%q) = %v, want ErrNodeToken", bad, err)
			}
		}
	})
	t.Run("the first sync leaves it joining until all four checks pass", func(t *testing.T) {
		out, err := e.svc.NodeSync(ctx, tok, healthyReport()) // reaches nobody, no Nomad node
		if err != nil {
			t.Fatal(err)
		}
		if out.State != "joining" || len(out.Checks) != 4 || out.Checks[0].OK != true || out.Checks[2].OK {
			t.Errorf("answer = %+v", out)
		}
		if out.Role != "client" || out.GossipKey != "" || out.Renew {
			t.Errorf("role %q, gossip %q, renew %v", out.Role, out.GossipKey, out.Renew)
		}
		out, _ = e.svc.NodeSync(ctx, tok, healthyReport(1))
		if out.State != "joining" || out.Checks[2].OK != true || out.Checks[3].OK {
			t.Errorf("with the network reached but no Nomad node: %+v", out.Checks)
		}
	})
	t.Run("ready once Nomad has its node", func(t *testing.T) {
		e.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
		out, err := e.svc.NodeSync(ctx, tok, healthyReport(1))
		if err != nil {
			t.Fatal(err)
		}
		if out.State != "ready" || !allPass(out.Checks) {
			t.Errorf("answer = %+v", out)
		}
		row, _ := e.store.ServerByName(ctx, "two")
		if row.State != store.ServerReady || row.ReadyAt.IsZero() || row.SeenAt.IsZero() {
			t.Errorf("row = %+v", row)
		}
		if rep, ok := parseReport(row.Report); !ok || rep.Docker != "27.5.1" {
			t.Errorf("the report wasn't kept: %q", row.Report)
		}
	})
	t.Run("a removed server's token stops working", func(t *testing.T) {
		e.nomad.SetNodeStatusOf("node-2", "down")
		if err := e.svc.RemoveServer(ctx, "two"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.NodeSync(ctx, tok, healthyReport(1)); !errors.Is(err, ErrNodeToken) {
			t.Errorf("NodeSync after removal = %v, want ErrNodeToken", err)
		}
	})
}

func TestNodeSyncRenewAndPeers(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	_, two := e.join(t, "two", "")
	_, three := e.join(t, "three", "")
	now := e.svc.clock.Now().UTC()

	r := healthyReport(1)
	r.CertificateNotAfter = now.Add(10 * 24 * time.Hour).Format(time.RFC3339)
	out, err := e.svc.NodeSync(ctx, two.NodeToken, r)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Renew {
		t.Error("a certificate that ends in 10 days isn't told to renew")
	}
	r.CertificateNotAfter = now.Add(60 * 24 * time.Hour).Format(time.RFC3339)
	if out, _ = e.svc.NodeSync(ctx, two.NodeToken, r); out.Renew {
		t.Error("a certificate that ends in 60 days is told to renew")
	}
	row, _ := e.store.ServerByName(ctx, "two")
	if !row.CertNotAfter.Equal(now.Add(60 * 24 * time.Hour)) {
		t.Errorf("certificate end = %v", row.CertNotAfter)
	}
	// Peers are every other joining or ready server.
	if len(out.Peers) != 2 || out.Peers[0].Index != 1 || out.Peers[1].Index != 3 {
		t.Errorf("peers of two = %+v", out.Peers)
	}
	_ = three
}

func TestRolesAreHandedOutOneAtATime(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	// The first server reports its Nomad server running.
	first := healthyReport()
	first.Role, first.ServerRunning = "server", true
	sync := func(token string, r NodeReport) NodeAnswer {
		t.Helper()
		out, err := e.svc.NodeSync(ctx, token, r)
		if err != nil {
			t.Fatalf("NodeSync: %v", err)
		}
		return out
	}
	sync(e.firstToken, first)

	_, two := e.join(t, "two", "")
	_, three := e.join(t, "three", "")
	e.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	e.nomad.AddNode("node-3", "three", "ready", "10.99.0.3")
	if out := sync(two.NodeToken, healthyReport(1, 3)); out.State != "ready" || out.Role != "client" {
		t.Fatalf("two: %+v", out)
	}
	// Two ready servers: nothing is added. Three becoming ready makes three: the oldest that joined
	// (two) is promoted, and only it.
	if out := sync(three.NodeToken, healthyReport(1, 2)); out.State != "ready" || out.Role != "client" {
		t.Fatalf("three: %+v", out)
	}
	rows, _ := e.store.ListServers(ctx)
	if wanted := nomadServerIndexes(rows); !slices.Equal(wanted, []int{1, 2}) {
		t.Fatalf("Nomad servers = %v, want [1 2]", wanted)
	}
	// Two learns it is a server: it gets the gossip key. Until it reports its Nomad server running,
	// three isn't promoted.
	out := sync(two.NodeToken, healthyReport(1, 3))
	if out.Role != "server" || out.GossipKey != sentinelGossip || !slices.Equal(out.Servers, []int{1, 2}) {
		t.Fatalf("two after its promotion: role %q, gossip %q, servers %v", out.Role, out.GossipKey, out.Servers)
	}
	if out := sync(three.NodeToken, healthyReport(1, 2)); out.Role != "client" {
		t.Fatalf("three before two runs its server: %+v", out)
	}
	running := healthyReport(1, 3)
	running.Role, running.ServerRunning = "server", true
	sync(two.NodeToken, running)
	out = sync(three.NodeToken, healthyReport(1, 2))
	if out.Role != "server" || out.GossipKey != sentinelGossip || !slices.Equal(out.Servers, []int{1, 2, 3}) {
		t.Fatalf("three after two runs: role %q, servers %v", out.Role, out.Servers)
	}
	// And the certificate a server asks for is a server's.
	_, csr, _ := clusterca.NewKeyAndCSR()
	cert, ca, err := e.svc.NodeCertificate(ctx, three.NodeToken, string(csr))
	if err != nil || !clusterca.Equal([]byte(ca), e.caCert) {
		t.Fatalf("NodeCertificate = %v", err)
	}
	if c, _ := clusterca.ParseCertificate([]byte(cert)); c == nil || c.DNSNames[0] != "server.global.nomad" {
		t.Errorf("certificate for a Nomad server = %+v", c)
	}
	cl, _, err := e.svc.NodeCertificate(ctx, e.firstToken, string(csr))
	if c, _ := clusterca.ParseCertificate([]byte(cl)); err != nil || c == nil || c.IPAddresses[1].String() != "10.98.0.1" {
		t.Errorf("the first server's certificate = %v, %v", c, err)
	}
	if _, _, err := e.svc.NodeCertificate(ctx, "cvnd_"+strings.Repeat("y", 43), string(csr)); !errors.Is(err, ErrNodeToken) {
		t.Errorf("NodeCertificate with a wrong token = %v", err)
	}
	var fe *app.FieldError
	if _, _, err := e.svc.NodeCertificate(ctx, three.NodeToken, "garbage"); !errors.As(err, &fe) || fe.Field != "csr" {
		t.Errorf("NodeCertificate with a bad CSR = %v", err)
	}
}

func TestRemoveServer(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	_, two := e.join(t, "two", "")
	e.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	if _, err := e.svc.NodeSync(ctx, two.NodeToken, healthyReport(1)); err != nil {
		t.Fatal(err)
	}

	var re *ServerRunningError
	if err := e.svc.RemoveServer(ctx, "two"); !errors.As(err, &re) || !errors.Is(err, ErrServerRunning) ||
		err.Error() != "two is running. Stop it, or run the installer with --leave on it, then remove it." {
		t.Errorf("RemoveServer of a running server = %v", err)
	}
	if err := e.svc.RemoveServer(ctx, firstServerName); !errors.Is(err, ErrFirstServer) {
		t.Errorf("RemoveServer of the first = %v, want ErrFirstServer", err)
	}
	if err := e.svc.RemoveServer(ctx, "nobody"); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("RemoveServer of nobody = %v, want ErrServerNotFound", err)
	}
	// A waiting join is cancelled by its ID, running or not.
	waiting := e.newJoin(t, "")
	if err := e.svc.RemoveServer(ctx, waiting.Server.ID); err != nil {
		t.Errorf("cancel a waiting join: %v", err)
	}
	// Nomad down for the server: removed, and its token stops working.
	e.nomad.SetNodeStatusOf("node-2", "down")
	if err := e.svc.RemoveServer(ctx, "two"); err != nil {
		t.Errorf("RemoveServer of a down server: %v", err)
	}
	if _, err := e.store.ServerByName(ctx, "two"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the row is still there: %v", err)
	}
	// A server Nomad has no node for can go too; its index is free again.
	_, three := e.join(t, "three", "")
	if three.Index != 2 {
		t.Errorf("the freed index wasn't reused: %d", three.Index)
	}
	if _, err := e.svc.NodeSync(ctx, three.NodeToken, healthyReport(1)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveServer(ctx, "three"); err != nil {
		t.Errorf("RemoveServer with no Nomad node: %v", err)
	}
	// Nomad that can't be read can't say the server isn't running.
	_, four := e.join(t, "four", "")
	_ = four
	e.nomad.Down()
	if err := e.svc.RemoveServer(ctx, "four"); err == nil || errors.Is(err, ErrServerRunning) {
		t.Errorf("RemoveServer with Nomad down = %v, want Nomad's error", err)
	}
}

func TestListServers(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	_, two := e.join(t, "two", "")
	waiting := e.newJoin(t, "later")
	e.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	e.nomad.AddNode("node-lab", "lab", "ready", "") // a Nomad node with no row
	if _, err := e.svc.NodeSync(ctx, two.NodeToken, healthyReport(1)); err != nil {
		t.Fatal(err)
	}

	byName := func(v ServersView) map[string]ServerView {
		m := map[string]ServerView{}
		for _, s := range v.Servers {
			m[s.Name] = s
		}
		return m
	}
	view, err := e.svc.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := byName(view)
	if len(view.Servers) != 4 {
		t.Fatalf("servers = %d, want 4 (first, two, a waiting join, the lab's node)", len(view.Servers))
	}
	if f := m[firstServerName]; !f.First || !f.Ingress || !f.ControlPlane || !f.NomadServer || f.State != "ready" || f.Address != "203.0.113.4" || f.PrivateAddress != "10.98.0.1" || !f.Managed || f.Index != 1 {
		t.Errorf("first = %+v", f)
	}
	if v := m["two"]; v.State != "ready" || v.Address != "203.0.113.9" || v.PrivateAddress != "10.98.0.2" || !v.AcceptsWork || len(v.Checks) != 4 ||
		v.Agent == nil || v.Agent.Docker != "27.5.1" || v.NomadServer || v.Memory != nil || v.CPU != nil || v.Disks != nil {
		t.Errorf("two = %+v", v)
	}
	if v := m["lab"]; v.Managed || v.State != "ready" || v.Index != 0 {
		t.Errorf("an unmanaged node = %+v", v)
	}
	var w ServerView
	for _, s := range view.Servers {
		if s.State == "waiting" {
			w = s
		}
	}
	if w.ID != waiting.Server.ID || w.Name != "later" || w.ExpiresAt.IsZero() || w.Checks != nil {
		t.Errorf("waiting = %+v", w)
	}
	if c := view.Cluster; c.Servers != 4 || c.Ready != 3 || c.NomadServers != 1 || c.SurvivesLoss || !c.CanAdd || c.Nomad != "ok" || c.MemoryMB != 3*4096 {
		t.Errorf("cluster = %+v", c)
	}

	// A server whose node is down, then one Nomad has lost.
	e.nomad.SetNodeStatusOf("node-2", "down")
	view, _ = e.svc.ListServers(ctx)
	if got := byName(view)["two"]; got.State != "down" || got.AcceptsWork {
		t.Errorf("a down server = %+v", got)
	}
	e.nomad.RemoveNode("node-2")
	view, _ = e.svc.ListServers(ctx)
	if got := byName(view)["two"]; got.State != "down" {
		t.Errorf("a server Nomad lost = %+v", got)
	}
	// Ineligible: still ready, takes no work.
	e.nomad.AddNode("node-2", "two", "ready", "10.99.0.2")
	e.nomad.SetNodeEligibilityOf("node-2", "ineligible", false)
	view, _ = e.svc.ListServers(ctx)
	if got := byName(view)["two"]; got.State != "ready" || got.AcceptsWork {
		t.Errorf("an ineligible server = %+v", got)
	}

	// Nomad unreachable: the rows are listed, nothing is judged down.
	e.nomad.Down()
	view, err = e.svc.ListServers(ctx)
	if err != nil {
		t.Fatalf("ListServers with Nomad down: %v", err)
	}
	if view.Cluster.Nomad != "unreachable" || byName(view)["two"].State != "ready" || byName(view)[firstServerName].State != "ready" {
		t.Errorf("with Nomad down: cluster %+v, two %+v", view.Cluster, byName(view)["two"])
	}
	e.nomad.Up()

	if got, err := e.svc.Server(ctx, "two"); err != nil || got.Name != "two" {
		t.Errorf("Server(two) = %+v, %v", got, err)
	}
	if got, err := e.svc.ServerByID(ctx, waiting.Server.ID); err != nil || got.State != "waiting" {
		t.Errorf("ServerByID(waiting) = %+v, %v", got, err)
	}
	if _, err := e.svc.Server(ctx, "nobody"); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("Server(nobody) = %v", err)
	}
}

func TestServerNumbersComeFromTheNewestSample(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	at := e.svc.clock.Now().UTC().Truncate(time.Minute)
	i64 := func(n int64) *int64 { return &n }
	err := e.store.WriteMetricsTick(ctx, store.MetricsTick{At: at, Minute: store.MetricsRows{
		Hosts: []store.HostSample{{Node: "node-1", TS: at, MemoryUsedMB: i64(480), MemoryReservedMB: i64(144), MemoryTotalMB: i64(1958),
			CPUUsedMHz: i64(60), CPUReservedMHz: i64(220), CPUTotalMHz: i64(4000), CPUCores: i64(2)}},
		Disks: []store.DiskSample{{Node: "node-1", TS: at, Mount: "/", Holds: "nomad,docker", UsedMB: 6000, TotalMB: 19000, FreeMB: 13000}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Server(ctx, firstServerName)
	if err != nil {
		t.Fatal(err)
	}
	if v.Memory == nil || *v.Memory != (ServerMemory{Used: 480, Reserved: 144, Total: 1958}) ||
		v.CPU == nil || *v.CPU != (ServerCPU{Used: 60, Reserved: 220, Total: 4000, Cores: 2}) ||
		len(v.Disks) != 1 || v.Disks[0].Mount != "/" || v.Disks[0].FreeMB != 13000 {
		t.Errorf("numbers = %+v %+v %+v", v.Memory, v.CPU, v.Disks)
	}
}

func TestAdoptFirst(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, pub, _ := mesh.NewKey()
	_, pub2, _ := mesh.NewKey()

	var fe *app.FieldError
	if _, err := e.svc.AdoptFirst(ctx, "Bad Name", pub, "203.0.113.4"); !errors.As(err, &fe) || fe.Field != "name" {
		t.Errorf("a bad name = %v", err)
	}
	if _, err := e.svc.AdoptFirst(ctx, "hel-1", "short", "203.0.113.4"); !errors.As(err, &fe) || fe.Field != "publicKey" {
		t.Errorf("a bad key = %v", err)
	}
	if _, err := e.svc.AdoptFirst(ctx, "hel-1", pub, "not-an-ip"); !errors.As(err, &fe) || fe.Field != "address" {
		t.Errorf("a bad address = %v", err)
	}
	tok1, err := e.svc.AdoptFirst(ctx, "hel-1", pub, "203.0.113.4")
	if err != nil || !strings.HasPrefix(tok1, "cvnd_") {
		t.Fatalf("AdoptFirst = %q, %v", tok1, err)
	}
	row, _ := e.store.ServerByIndex(ctx, 1)
	if !row.First || !row.NomadServer || row.State != store.ServerReady || row.Name != "hel-1" || row.Endpoint != "203.0.113.4:51820" || row.TokenHash != hashToken(tok1) {
		t.Errorf("row = %+v", row)
	}
	// Again: the same server, a new token, key and endpoint; the old token stops.
	tok2, err := e.svc.AdoptFirst(ctx, "hel-1", pub2, "203.0.113.5")
	if err != nil || tok2 == tok1 {
		t.Fatalf("second AdoptFirst = %q, %v", tok2, err)
	}
	rows, _ := e.store.ListServers(ctx)
	if len(rows) != 1 || rows[0].PublicKey != pub2 || rows[0].Endpoint != "203.0.113.5:51820" || rows[0].TokenHash != hashToken(tok2) {
		t.Errorf("rows after the second = %+v", rows)
	}
	if _, err := e.svc.authNode(ctx, tok1); !errors.Is(err, ErrNodeToken) {
		t.Errorf("the old token = %v", err)
	}
}

// T018 (FR-015): no error of the servers calls quotes what it was given, whether a key, a certificate
// request or a token; and the CA's key and the gossip key reach no answer of the service.
func TestServerErrorsNeverQuoteTheirInput(t *testing.T) {
	ctx := context.Background()
	e := newClusterEnv(t)
	const sentinel = "s3cret-sentinel-0031"
	j := e.newJoin(t, "")
	_, two := e.join(t, "two", "")

	var errs []error
	add := func(_ any, err error) { errs = append(errs, err) }
	for _, mutate := range []func(*JoinRequest){
		func(r *JoinRequest) { r.PublicKey = sentinel },
		func(r *JoinRequest) {
			r.CSR = "-----BEGIN CERTIFICATE REQUEST-----\n" + sentinel + "\n-----END CERTIFICATE REQUEST-----\n"
		},
		func(r *JoinRequest) { r.Address = sentinel },
		func(r *JoinRequest) { r.Facts.Kernel = sentinel + strings.Repeat("k", 300) },
	} {
		req := joinRequest(t, j.Token, "x")
		mutate(&req)
		add(e.svc.Join(ctx, req, "203.0.113.9"))
	}
	add(e.svc.Join(ctx, joinRequest(t, sentinel, "x"), "203.0.113.9"))
	add(e.svc.NodeSync(ctx, sentinel, healthyReport()))
	bad := healthyReport()
	bad.Kernel = sentinel + strings.Repeat("k", 300)
	add(e.svc.NodeSync(ctx, two.NodeToken, bad))
	_, _, err := e.svc.NodeCertificate(ctx, two.NodeToken, "-----BEGIN CERTIFICATE REQUEST-----\n"+sentinel+"\n-----END CERTIFICATE REQUEST-----\n")
	errs = append(errs, err)
	errs = append(errs, e.svc.RemoveServer(ctx, sentinel))
	_, err = e.svc.CreateJoin(ctx, strings.ToUpper(sentinel), "", "")
	errs = append(errs, err)

	for i, err := range errs {
		if err == nil {
			t.Errorf("call %d wasn't refused", i)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), sentinel) {
			t.Errorf("call %d's error quotes its input: %v", i, err)
		}
	}

	// What the service answers about servers never holds the CA's key or the gossip key.
	view, _ := e.svc.ListServers(ctx)
	att, _ := e.svc.Attention(ctx)
	dump := fmt.Sprintf("%+v %+v", view, att)
	for what, secret := range map[string]string{"the CA's key": string(e.caKey), "the gossip key": sentinelGossip, "the join token": j.Token, "the node token": two.NodeToken} {
		if strings.Contains(dump, secret) {
			t.Errorf("a list answer holds %s", what)
		}
	}
}
