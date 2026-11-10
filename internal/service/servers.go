package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Servers (feature 031): the machines of the cluster, how one joins and what the node agents tell
// Caravana. The shapes of the calls are in specs/031-add-a-server/contracts/api.md.
//
// A join token (cvjn_…) and a node token (cvnd_…) are shown once and stored as SHA-256 hashes, as
// API tokens are. No token, key, CSR or certificate private key is logged, stored, put in an error,
// a notice or a list answer (FR-015).

// sentence is an error whose text is a sentence for the owner, as the contract words it: capitalized
// and ended with a full stop, which errors.New strings are not by convention. Two are equal when
// their texts are, so errors.Is works on the variables below.
type sentence string

func (s sentence) Error() string { return string(s) }

// Errors of the servers calls. The API maps each to its status and code.
var (
	// ErrClusterNotReady: no first server row, or the cluster's CA isn't readable.
	ErrClusterNotReady error = sentence("Run the installer again on this server first: it sets up the private network and Nomad's certificates.")
	// ErrServerExists: a server or a Nomad node already has the name asked for.
	ErrServerExists = errors.New("a server with that name already exists")
	// ErrServerNotFound: no server has that name or ID.
	ErrServerNotFound = errors.New("no such server")
	// ErrServerRunning: the server is still running, so it can't be removed.
	ErrServerRunning = errors.New("the server is running")
	// ErrFirstServer: the server Caravana runs on can't be removed.
	ErrFirstServer = errors.New("this is the server Caravana runs on: it can't be removed")
	// ErrJoinToken is every refusal of a join token: used, expired or never made.
	ErrJoinToken error = sentence("This join token isn't valid: it was used, expired, or never existed. Make a new command in the console.")
	// ErrNodeToken is every refusal of a node token.
	ErrNodeToken error = sentence("This server isn't known to the cluster: it was removed, or its token is wrong.")
	// ErrClusterFull: every server index is taken.
	ErrClusterFull = store.ErrClusterFull
)

// ServerRunningError is ErrServerRunning with the server's name, in the words the owner reads.
type ServerRunningError struct{ Name string }

func (e *ServerRunningError) Error() string {
	return e.Name + " is running. Stop it, or run the installer with --leave on it, then remove it."
}
func (e *ServerRunningError) Unwrap() error { return ErrServerRunning }

const (
	joinTokenPrefix = "cvjn_"
	nodeTokenPrefix = "cvnd_"
	serverTokenLen  = 32 // random bytes of either token
	// joinTTL is how long a join command works.
	joinTTL = time.Hour
	// renewWithin is how close to its end a server's certificate is told to renew.
	renewWithin = 30 * 24 * time.Hour
	// certWarnWithin is how close to its end a certificate becomes a needs-you item.
	certWarnWithin = 14 * 24 * time.Hour
	// maxFactLen caps every text a server tells about itself.
	maxFactLen = 200
	// maxCSRLen caps a certificate request.
	maxCSRLen = 8 << 10
)

// NodeFacts are what a join tells about the machine (api.md §4). The sizes are checked; none of it is trusted for
// more than display.
type NodeFacts struct {
	Hostname    string `json:"hostname"`
	Arch        string `json:"arch"`
	MemoryMB    int64  `json:"memoryMb"`
	DiskFreeMB  int64  `json:"diskFreeMb"`
	DiskTotalMB int64  `json:"diskTotalMb"`
	Kernel      string `json:"kernel"`
	Docker      string `json:"docker"`
	Nomad       string `json:"nomad"`
	Agent       string `json:"agent"`
}

// NodeReach is one peer a server pinged.
type NodeReach struct {
	Index int  `json:"index"`
	OK    bool `json:"ok"`
}

// NodeReport is what a sync tells Caravana about a server (api.md §4).
type NodeReport struct {
	Agent               string      `json:"agent"`
	Nomad               string      `json:"nomad"`
	Docker              string      `json:"docker"`
	DockerRunning       bool        `json:"dockerRunning"`
	DiskFreeMB          int64       `json:"diskFreeMb"`
	DiskTotalMB         int64       `json:"diskTotalMb"`
	MemoryMB            int64       `json:"memoryMb"`
	Kernel              string      `json:"kernel"`
	Role                string      `json:"role"`
	NomadLeader         bool        `json:"nomadLeader"`
	ServerRunning       bool        `json:"serverRunning"`
	CertificateNotAfter string      `json:"certificateNotAfter,omitempty"`
	Reach               []NodeReach `json:"reach"`
}

// JoinRequest is the body of POST /v1/join.
type JoinRequest struct {
	Token     string
	PublicKey string
	CSR       string
	Address   string // an IP literal the other servers reach this one at; "" to use where the call came from
	Facts     NodeFacts
}

// NodePeer is another server as a server needs to know it. The agent reads index, publicKey and
// endpoint (mesh.Peer); the name is for a person reading the answer.
type NodePeer struct {
	Index     int
	Name      string
	PublicKey string
	Endpoint  string
}

// NodeAnswer is what a join, a sync and a certificate call answer (api.md §4). Fields a call
// doesn't answer stay empty.
type NodeAnswer struct {
	Name        string
	Index       int
	Pool        string
	Role        string // "client" or "server"
	Servers     []int
	Peers       []NodePeer
	CA          string
	Certificate string
	NodeToken   string
	GossipKey   string
	Renew       bool
	State       string
	Checks      []ServerCheck
}

// ServerJoin is a join command that was just made (addServer's answer). Token is the only place the
// join token ever is.
type ServerJoin struct {
	Server      ServerView
	Token       string
	Command     string
	CommandNote string
	ExpiresAt   time.Time
}

// ServerAgent is what a server's agent told about the machine.
type ServerAgent struct{ Caravana, Nomad, Docker, Kernel string }

// ServerMemory and ServerCPU are a server's numbers from the newest stored sample: memory in MB,
// CPU in MHz. nil in a ServerView means no sample yet.
type ServerMemory struct{ Used, Reserved, Total int64 }

// ServerCPU is ServerMemory for the CPU, with its cores.
type ServerCPU struct{ Used, Reserved, Total, Cores int64 }

// ServerView is one server as the API and the console show it.
type ServerView struct {
	ID    string
	Name  string
	Index int // 0 for a Nomad node Caravana doesn't manage
	Pool  string
	// State is "waiting", "joining", "ready" or "down" (a ready server whose Nomad node is down,
	// disconnected or gone).
	State                                                  string
	Managed                                                bool // false: a Nomad node with no row (the lab; a host not rerun)
	First, Ingress, ControlPlane, NomadServer, AcceptsWork bool
	Address                                                string // where other servers reach it, without the port
	PrivateAddress                                         string // its address on the private network
	CreatedAt, JoinedAt, ReadyAt, SeenAt, ExpiresAt        time.Time
	Checks                                                 []ServerCheck
	Agent                                                  *ServerAgent
	CertificateExpiresAt                                   time.Time
	Memory                                                 *ServerMemory
	CPU                                                    *ServerCPU
	Disks                                                  []HostDisk // nil: no sample yet
	Copies                                                 int
	Unreachable                                            []string // servers its last report couldn't reach
	// NodeID is Nomad's ID for the server's node, "" when Nomad has none.
	NodeID string
}

// ClusterView is the cluster as a whole.
type ClusterView struct {
	Servers, Ready, NomadServers int
	SurvivesLoss                 bool
	MemoryMB, CPUCores           int64
	CanAdd                       bool
	CannotAddReason              string
	// Nomad is "ok" or "unreachable".
	Nomad string
}

// ServersView is what listServers answers.
type ServersView struct {
	Cluster ClusterView
	Servers []ServerView
}

// newServerToken returns a prefix and 32 random bytes in URL-safe base64 without padding.
func newServerToken(prefix string) (string, error) {
	b := make([]byte, serverTokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a token: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// clusterCA reads the CA, turning a missing one into ErrClusterNotReady.
func (s *Service) clusterCA(ctx context.Context) (cert, key []byte, err error) {
	cert, key, err = s.deployer.ClusterCA(ctx)
	if errors.Is(err, deploy.ErrNoCluster) {
		return nil, nil, ErrClusterNotReady
	}
	return cert, key, err
}

// ClusterStatus says whether servers can be added: a first server row exists and the CA is
// readable. Otherwise it is ErrClusterNotReady.
func (s *Service) ClusterStatus(ctx context.Context) error {
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("cluster status: %w", err)
	}
	if !slices.ContainsFunc(rows, func(r store.Server) bool { return r.First }) {
		return ErrClusterNotReady
	}
	if _, _, err := s.clusterCA(ctx); err != nil {
		return err
	}
	return nil
}

// CreateJoin makes a join command for a server that doesn't exist yet. name may be empty (the
// machine's host name is used) and pool too ("default"). fallbackURL is the console's address as the
// request reached it, used when CARAVANA_PUBLIC_URL isn't set.
func (s *Service) CreateJoin(ctx context.Context, name, pool, fallbackURL string) (ServerJoin, error) {
	if err := s.ClusterStatus(ctx); err != nil {
		return ServerJoin{}, err
	}
	if name != "" && !mesh.ValidName(name) {
		return ServerJoin{}, app.NewFieldError("name", "must be lowercase letters, digits and dashes, 63 characters at most")
	}
	if mesh.ReservedName(name) {
		return ServerJoin{}, app.NewFieldError("name", "can't be %q: the console uses that word in its addresses", name)
	}
	if pool == "" {
		pool = "default"
	}
	if !mesh.ValidName(pool) {
		return ServerJoin{}, app.NewFieldError("pool", "must be lowercase letters, digits and dashes, 63 characters at most")
	}
	now := s.clock.Now().UTC()
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	if _, err := s.store.DeleteExpiredJoins(ctx, now); err != nil {
		return ServerJoin{}, fmt.Errorf("create join: %w", err)
	}
	if name != "" {
		if err := s.nameFree(ctx, name); err != nil {
			return ServerJoin{}, err
		}
	}
	token, err := newServerToken(joinTokenPrefix)
	if err != nil {
		return ServerJoin{}, err
	}
	expires := now.Add(joinTTL)
	row, err := s.store.InsertServer(ctx, store.Server{
		ID: newTokenID(), Name: name, Pool: pool, State: store.ServerWaiting,
		JoinHash: hashToken(token), JoinExpires: expires, CreatedAt: now,
	})
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		return ServerJoin{}, &RefusalError{Field: "name", Err: ErrServerExists}
	case err != nil:
		return ServerJoin{}, fmt.Errorf("create join: %w", err)
	}
	s.publish(Change{What: ChangeServers})
	command, note := s.joinCommand(token, fallbackURL)
	return ServerJoin{Server: waitingView(row), Token: token, Command: command, CommandNote: note, ExpiresAt: expires}, nil
}

// nameFree refuses a name a server or a Nomad node has. A Nomad that can't be read is not a reason
// to refuse: the join will meet the name again.
func (s *Service) nameFree(ctx context.Context, name string) error {
	if _, err := s.store.ServerByName(ctx, name); err == nil {
		return &RefusalError{Field: "name", Err: ErrServerExists}
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("check the server's name: %w", err)
	}
	if nodes, err := s.deployer.Nodes(ctx); err == nil {
		if slices.ContainsFunc(nodes, func(n deploy.Node) bool { return n.Name == name }) {
			return &RefusalError{Field: "name", Err: ErrServerExists}
		}
	}
	return nil
}

// joinCommand is the line the owner runs on the new server, and a note when it needs more than that.
func (s *Service) joinCommand(token, fallbackURL string) (command, note string) {
	url := s.PublicURL()
	if url == "" {
		url = strings.TrimRight(fallbackURL, "/")
	}
	insecure := ""
	if mode, _ := s.ACME(); mode == "none" {
		insecure = " --insecure"
	}
	if hostos.IsReleaseVersion(s.runningVersion) {
		v := strings.TrimPrefix(s.runningVersion, "v")
		return fmt.Sprintf("curl -fsSL %s/v%s/install.sh | sudo bash -s -- --join %s --url %s%s",
			s.downloadBase(), v, token, url, insecure), ""
	}
	return fmt.Sprintf("sudo bash install.sh --from <the release folder> --join %s --url %s%s", token, url, insecure),
		"This is a development build: copy its install.sh and release files to the new server first, then run this command there."
}

// waitingView is the view of a row that hasn't joined.
func waitingView(r store.Server) ServerView {
	v := ServerView{ID: r.ID, Name: r.Name, Index: r.Index, Pool: r.Pool, State: store.ServerWaiting, Managed: true,
		CreatedAt: r.CreatedAt, ExpiresAt: r.JoinExpires}
	if addrs, err := mesh.Of(r.Index); err == nil {
		v.PrivateAddress = addrs.Mesh.String()
	}
	return v
}

// validateFacts checks the sizes of what a server says about itself.
func validateFacts(f NodeFacts) error {
	for _, c := range []struct {
		field, value string
	}{{"facts.hostname", f.Hostname}, {"facts.arch", f.Arch}, {"facts.kernel", f.Kernel},
		{"facts.docker", f.Docker}, {"facts.nomad", f.Nomad}, {"facts.agent", f.Agent}} {
		if err := checkText(c.field, c.value); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		field string
		value int64
	}{{"facts.memoryMb", f.MemoryMB}, {"facts.diskFreeMb", f.DiskFreeMB}, {"facts.diskTotalMb", f.DiskTotalMB}} {
		if c.value < 0 || c.value > 1<<40 {
			return app.NewFieldError(c.field, "is out of range")
		}
	}
	return nil
}

// checkText refuses a text that is long or holds a control character.
func checkText(field, v string) error {
	if len(v) > maxFactLen {
		return app.NewFieldError(field, "is longer than %d characters", maxFactLen)
	}
	for _, r := range v {
		if r < ' ' || r == 0x7f {
			return app.NewFieldError(field, "has a control character")
		}
	}
	return nil
}

// Join answers a server's first call (api.md §4, POST /v1/join). remoteAddr is where the call came
// from, an IP address or "" when unknown. Everything that can fail on the request's content fails
// before the token is spent, so a mistake leaves the command usable until it ends.
func (s *Service) Join(ctx context.Context, req JoinRequest, remoteAddr string) (NodeAnswer, error) {
	caCert, caKey, err := s.clusterCA(ctx)
	if err != nil {
		return NodeAnswer{}, err
	}
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	now := s.clock.Now().UTC()

	row, err := s.store.ServerByJoinHash(ctx, hashToken(req.Token))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return NodeAnswer{}, ErrJoinToken
	case err != nil:
		return NodeAnswer{}, fmt.Errorf("join: %w", err)
	}
	if row.State != store.ServerWaiting || !row.JoinExpires.After(now) {
		return NodeAnswer{}, ErrJoinToken
	}

	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return NodeAnswer{}, fmt.Errorf("join: %w", err)
	}
	if !mesh.ValidPublicKey(req.PublicKey) {
		return NodeAnswer{}, app.NewFieldError("publicKey", "must be a WireGuard public key: 44 characters of base64")
	}
	if slices.ContainsFunc(rows, func(r store.Server) bool { return r.PublicKey == req.PublicKey }) {
		return NodeAnswer{}, app.NewFieldError("publicKey", "is another server's key")
	}
	if len(req.CSR) > maxCSRLen {
		return NodeAnswer{}, app.NewFieldError("csr", "is larger than %d bytes", maxCSRLen)
	}
	if err := validateFacts(req.Facts); err != nil {
		return NodeAnswer{}, err
	}
	endpoint, err := joinEndpoint(req.Address, remoteAddr)
	if err != nil {
		return NodeAnswer{}, err
	}
	addrs, err := mesh.Of(row.Index)
	if err != nil {
		return NodeAnswer{}, fmt.Errorf("join: %w", err)
	}
	cert, err := clusterca.Sign(caCert, caKey, []byte(req.CSR), clusterca.Client, addrs.Mesh, now)
	if err != nil {
		// The error names what is wrong with the request, never a key.
		return NodeAnswer{}, app.NewFieldError("csr", "%s", err.Error())
	}
	tokenHash, nodeToken, err := newNodeToken()
	if err != nil {
		return NodeAnswer{}, err
	}

	name := row.Name
	generated := name == ""
	if generated {
		name = freeName(mesh.NameOfHost(req.Facts.Hostname, row.Index), rows)
	}
	joined, err := s.store.ConsumeJoin(ctx, hashToken(req.Token), name, req.PublicKey, endpoint, tokenHash, now)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return NodeAnswer{}, ErrJoinToken
	case errors.Is(err, store.ErrAlreadyExists):
		return NodeAnswer{}, &RefusalError{Field: "name", Err: ErrServerExists}
	case err != nil:
		return NodeAnswer{}, fmt.Errorf("join: %w", err)
	}
	// The facts are the first report: the page shows the machine at once. A sync replaces it.
	first := NodeReport{Agent: req.Facts.Agent, Nomad: req.Facts.Nomad, Docker: req.Facts.Docker,
		DockerRunning: req.Facts.Docker != "", DiskFreeMB: req.Facts.DiskFreeMB, DiskTotalMB: req.Facts.DiskTotalMB,
		MemoryMB: req.Facts.MemoryMB, Kernel: req.Facts.Kernel, Role: "client", Reach: []NodeReach{}}
	if b, err := json.Marshal(first); err == nil {
		// Not fatal: the first sync writes it again.
		if err := s.store.SaveServerReport(ctx, joined.Index, string(b), now, certEnd(cert)); err != nil {
			s.log.Warn("couldn't store a joining server's facts", "server", joined.Name, "error", err)
		}
	}
	s.publish(Change{What: ChangeServers})

	rows, err = s.store.ListServers(ctx)
	if err != nil {
		return NodeAnswer{}, fmt.Errorf("join: %w", err)
	}
	return NodeAnswer{
		Name: joined.Name, Index: joined.Index, Pool: joined.Pool, Role: "client",
		Servers: nomadServerIndexes(rows), Peers: peersOf(rows, joined.Index),
		CA: string(caCert), Certificate: string(cert), NodeToken: nodeToken,
	}, nil
}

// newNodeToken makes a node token and the hash that is stored.
func newNodeToken() (hash, token string, err error) {
	token, err = newServerToken(nodeTokenPrefix)
	if err != nil {
		return "", "", err
	}
	return hashToken(token), token, nil
}

// joinEndpoint is "ip:51820": the address the server gave, else where the call came from.
func joinEndpoint(given, remote string) (string, error) {
	field, text := "address", given
	if text == "" {
		field, text = "address", remote
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(text))
	if err != nil {
		if given == "" {
			return "", app.NewFieldError(field, "couldn't be read from the request: give the address other servers reach this one at (--address)")
		}
		return "", app.NewFieldError(field, "must be an IP address, not a name")
	}
	ap, err := mesh.ParseEndpoint(netip.AddrPortFrom(addr.Unmap(), mesh.Port).String())
	if err != nil {
		return "", app.NewFieldError(field, "isn't an address other servers can reach this one at")
	}
	return ap.String(), nil
}

// freeName is base, or base-2, base-3… while a server has the name.
func freeName(base string, rows []store.Server) string {
	taken := func(n string) bool {
		return slices.ContainsFunc(rows, func(r store.Server) bool { return r.Name == n })
	}
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		cand := strings.TrimRight(cutTo(base, 63-len(suffix)), "-") + suffix
		if !taken(cand) {
			return cand
		}
	}
}

func cutTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func certEnd(certPEM []byte) time.Time {
	t, err := clusterca.NotAfter(certPEM)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// nomadServerIndexes are the indexes of the servers Caravana wants as Nomad servers that have joined.
func nomadServerIndexes(rows []store.Server) []int {
	var out []int
	for _, r := range rows {
		if (r.NomadServer || r.First) && r.State != store.ServerWaiting {
			out = append(out, r.Index)
		}
	}
	slices.Sort(out)
	if len(out) == 0 {
		out = []int{1}
	}
	return out
}

// peersOf are every other server that is joining or ready and has a key and an address.
func peersOf(rows []store.Server, own int) []NodePeer {
	peers := []NodePeer{}
	for _, r := range rows {
		if r.Index == own || r.State == store.ServerWaiting || r.PublicKey == "" || r.Endpoint == "" {
			continue
		}
		peers = append(peers, NodePeer{Index: r.Index, Name: r.Name, PublicKey: r.PublicKey, Endpoint: r.Endpoint})
	}
	return peers
}

// parseReport reads a stored report; ok is false when there is none.
func parseReport(text string) (NodeReport, bool) {
	var r NodeReport
	if text == "" || json.Unmarshal([]byte(text), &r) != nil {
		return NodeReport{}, false
	}
	return r, true
}

// nodeOf finds Nomad's node at a server's app address.
func nodeOf(nodes []deploy.Node, index int) *deploy.Node {
	for i := range nodes {
		addr, err := netip.ParseAddr(nodes[i].AppAddress)
		if err != nil {
			continue
		}
		if idx, ok := mesh.IndexOfApp(addr); ok && idx == index {
			return &nodes[i]
		}
	}
	return nil
}

// othersOf are the joining and ready servers but index.
func othersOf(rows []store.Server, index int) []otherServer {
	var out []otherServer
	for _, r := range rows {
		if r.Index != index && r.State != store.ServerWaiting {
			out = append(out, otherServer{Index: r.Index, Name: r.Name})
		}
	}
	return out
}

// authNode finds the server a node token belongs to.
func (s *Service) authNode(ctx context.Context, token string) (store.Server, error) {
	if !strings.HasPrefix(token, nodeTokenPrefix) {
		return store.Server{}, ErrNodeToken
	}
	row, err := s.store.ServerByTokenHash(ctx, hashToken(token))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.Server{}, ErrNodeToken
	case err != nil:
		return store.Server{}, fmt.Errorf("node token: %w", err)
	}
	return row, nil
}

// validateReport checks the sizes of a sync's report.
func validateReport(r NodeReport) error {
	for _, c := range []struct{ field, value string }{
		{"agent", r.Agent}, {"nomad", r.Nomad}, {"docker", r.Docker}, {"kernel", r.Kernel},
		{"role", r.Role}, {"certificateNotAfter", r.CertificateNotAfter}} {
		if err := checkText(c.field, c.value); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		field string
		value int64
	}{{"diskFreeMb", r.DiskFreeMB}, {"diskTotalMb", r.DiskTotalMB}, {"memoryMb", r.MemoryMB}} {
		if c.value < 0 || c.value > 1<<40 {
			return app.NewFieldError(c.field, "is out of range")
		}
	}
	if len(r.Reach) > mesh.MaxIndex {
		return app.NewFieldError("reach", "has more than %d entries", mesh.MaxIndex)
	}
	for _, x := range r.Reach {
		if x.Index < 1 || x.Index > mesh.MaxIndex {
			return app.NewFieldError("reach", "has an index outside 1 to %d", mesh.MaxIndex)
		}
	}
	return nil
}

// NodeSync is a server's periodic call (api.md §4, POST /v1/node/sync): it stores the report,
// evaluates the checks, marks the server ready when they pass and answers what the server should be.
func (s *Service) NodeSync(ctx context.Context, nodeToken string, report NodeReport) (NodeAnswer, error) {
	row, err := s.authNode(ctx, nodeToken)
	if err != nil {
		return NodeAnswer{}, err
	}
	if err := validateReport(report); err != nil {
		return NodeAnswer{}, err
	}
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	now := s.clock.Now().UTC()

	var certEndAt time.Time
	if report.CertificateNotAfter != "" {
		if t, err := time.Parse(time.RFC3339, report.CertificateNotAfter); err == nil {
			certEndAt = t.UTC()
		}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return NodeAnswer{}, fmt.Errorf("sync: %w", err)
	}
	if err := s.store.SaveServerReport(ctx, row.Index, string(body), now, certEndAt); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NodeAnswer{}, ErrNodeToken // removed since the token was read
		}
		return NodeAnswer{}, fmt.Errorf("sync: %w", err)
	}

	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return NodeAnswer{}, fmt.Errorf("sync: %w", err)
	}
	nodes, nodesErr := s.deployer.Nodes(ctx)
	checks := serverChecks(checkInput{Report: &report, Others: othersOf(rows, row.Index),
		NomadKnown: nodesErr == nil, Node: nodeOf(nodes, row.Index)})
	state := row.State
	if state == store.ServerJoining && allPass(checks) {
		if err := s.store.SetServerReady(ctx, row.Index, now); err != nil {
			return NodeAnswer{}, fmt.Errorf("sync: %w", err)
		}
		state = store.ServerReady
	}
	if err := s.reconcileRoles(ctx); err != nil {
		return NodeAnswer{}, err
	}
	s.publish(Change{What: ChangeServers})

	// Read again: the roles may have changed.
	rows, err = s.store.ListServers(ctx)
	if err != nil {
		return NodeAnswer{}, fmt.Errorf("sync: %w", err)
	}
	cur := rows[slices.IndexFunc(rows, func(r store.Server) bool { return r.Index == row.Index })]
	ans := NodeAnswer{
		Name: cur.Name, Index: cur.Index, Pool: cur.Pool, Role: "client",
		Servers: nomadServerIndexes(rows), Peers: peersOf(rows, cur.Index),
		Renew: !certEndAt.IsZero() && certEndAt.Sub(now) < renewWithin, State: state, Checks: checks,
	}
	if cur.NomadServer || cur.First {
		ans.Role = "server"
		key, err := s.deployer.GossipKey(ctx)
		if errors.Is(err, deploy.ErrNoCluster) {
			return NodeAnswer{}, ErrClusterNotReady
		} else if err != nil {
			return NodeAnswer{}, err
		}
		ans.GossipKey = key
	}
	return ans, nil
}

// NodeCertificate signs a certificate for a server's new key: of the kind the server should be now,
// for its mesh address.
func (s *Service) NodeCertificate(ctx context.Context, nodeToken, csrPEM string) (certPEM, caPEM string, err error) {
	row, err := s.authNode(ctx, nodeToken)
	if err != nil {
		return "", "", err
	}
	if len(csrPEM) > maxCSRLen {
		return "", "", app.NewFieldError("csr", "is larger than %d bytes", maxCSRLen)
	}
	caCert, caKey, err := s.clusterCA(ctx)
	if err != nil {
		return "", "", err
	}
	addrs, err := mesh.Of(row.Index)
	if err != nil {
		return "", "", fmt.Errorf("certificate: %w", err)
	}
	kind := clusterca.Client
	if row.NomadServer || row.First {
		kind = clusterca.Server
	}
	cert, err := clusterca.Sign(caCert, caKey, []byte(csrPEM), kind, addrs.Mesh, s.clock.Now().UTC())
	if err != nil {
		return "", "", app.NewFieldError("csr", "%s", err.Error())
	}
	return string(cert), string(caCert), nil
}

// ListServers lists every server: the stored ones, joined with Nomad's nodes by app address, and
// the Nomad nodes that have no row.
func (s *Service) ListServers(ctx context.Context) (ServersView, error) {
	views, cluster, err := s.serverViews(ctx)
	if err != nil {
		return ServersView{}, err
	}
	return ServersView{Cluster: cluster, Servers: views}, nil
}

// Server reads one server by name.
func (s *Service) Server(ctx context.Context, name string) (ServerView, error) {
	views, _, err := s.serverViews(ctx)
	if err != nil {
		return ServerView{}, err
	}
	for _, v := range views {
		if v.Name == name && name != "" {
			return v, nil
		}
	}
	return ServerView{}, &RefusalError{Field: "name", Err: ErrServerNotFound}
}

// ServerByID reads one server by its ID: a join's handle, which exists before the name does.
func (s *Service) ServerByID(ctx context.Context, id string) (ServerView, error) {
	views, _, err := s.serverViews(ctx)
	if err != nil {
		return ServerView{}, err
	}
	for _, v := range views {
		if v.ID == id && id != "" {
			return v, nil
		}
	}
	return ServerView{}, &RefusalError{Field: "id", Err: ErrServerNotFound}
}

// serverViews builds every server's view and the cluster's.
func (s *Service) serverViews(ctx context.Context) ([]ServerView, ClusterView, error) {
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, ClusterView{}, fmt.Errorf("list servers: %w", err)
	}
	nodes, nodesErr := s.deployer.Nodes(ctx)
	nomadOK := nodesErr == nil
	if nodesErr != nil {
		s.log.Warn("can't read Nomad's nodes; the servers are listed without them", "error", nodesErr)
	}
	copies := map[string]int{} // by the node's name
	if nomadOK {
		if all, err := s.deployer.AllCopies(ctx); err == nil {
			for _, list := range all {
				for _, c := range list {
					if !c.Terminal {
						copies[c.Node]++
					}
				}
			}
		}
	}
	names := map[int]string{}
	for _, r := range rows {
		names[r.Index] = r.Name
	}

	var views []ServerView
	used := map[string]bool{} // Nomad node IDs that belong to a row
	for _, r := range rows {
		v := s.rowView(ctx, r, rows, nodes, nomadOK, names)
		if v.NodeID != "" {
			used[v.NodeID] = true
			v.Copies = copies[nodeNameOf(nodes, v.NodeID)]
		}
		views = append(views, v)
	}
	for _, n := range nodes {
		if used[n.ID] {
			continue
		}
		v := ServerView{ID: n.ID, Name: n.Name, Pool: n.Pool, State: "ready", Managed: false, NodeID: n.ID,
			AcceptsWork: n.Eligible && !n.Draining, Copies: copies[n.Name]}
		if n.Down() {
			v.State, v.AcceptsWork = "down", false
		}
		s.fillNumbers(ctx, &v)
		views = append(views, v)
	}
	return views, s.clusterView(ctx, rows, views, nodes, nomadOK), nil
}

func nodeNameOf(nodes []deploy.Node, id string) string {
	for _, n := range nodes {
		if n.ID == id {
			return n.Name
		}
	}
	return ""
}

// rowView is the view of a stored server.
func (s *Service) rowView(ctx context.Context, r store.Server, rows []store.Server, nodes []deploy.Node, nomadOK bool, names map[int]string) ServerView {
	v := waitingView(r)
	if r.State == store.ServerWaiting {
		return v
	}
	v.State, v.First, v.Ingress, v.ControlPlane, v.NomadServer = r.State, r.First, r.First, r.First, r.NomadServer || r.First
	v.JoinedAt, v.ReadyAt, v.SeenAt, v.CertificateExpiresAt, v.ExpiresAt = r.JoinedAt, r.ReadyAt, r.SeenAt, r.CertNotAfter, time.Time{}
	if ap, err := netip.ParseAddrPort(r.Endpoint); err == nil {
		v.Address = ap.Addr().String()
	}
	report, hasReport := parseReport(r.Report)
	if hasReport {
		v.Agent = &ServerAgent{Caravana: report.Agent, Nomad: report.Nomad, Docker: report.Docker, Kernel: report.Kernel}
		for _, x := range report.Reach {
			if !x.OK && names[x.Index] != "" {
				v.Unreachable = append(v.Unreachable, names[x.Index])
			}
		}
	}
	node := nodeOf(nodes, r.Index)
	if node != nil {
		v.NodeID = node.ID
		v.AcceptsWork = r.State == store.ServerReady && node.Eligible && !node.Draining && !node.Down()
	}
	if r.State == store.ServerReady && nomadOK && (node == nil || node.Down()) {
		v.State = "down"
	}
	if r.State == store.ServerReady && !nomadOK {
		v.AcceptsWork = true // unknown; not judged
	}
	in := checkInput{NomadKnown: nomadOK, Node: node, Others: othersOf(rows, r.Index)}
	if hasReport {
		in.Report = &report
	}
	v.Checks = serverChecks(in)
	s.fillNumbers(ctx, &v)
	return v
}

// fillNumbers sets the server's memory, CPU and disks from the newest stored sample of its Nomad
// node; they stay nil when it has none.
func (s *Service) fillNumbers(ctx context.Context, v *ServerView) {
	if v.NodeID == "" {
		return
	}
	h, err := s.store.NewestHostOf(ctx, v.NodeID)
	if err != nil {
		return
	}
	if h.MemoryTotalMB != nil {
		m := ServerMemory{Total: *h.MemoryTotalMB}
		if h.MemoryUsedMB != nil {
			m.Used = *h.MemoryUsedMB
		}
		if h.MemoryReservedMB != nil {
			m.Reserved = *h.MemoryReservedMB
		}
		v.Memory = &m
	}
	if h.CPUTotalMHz != nil {
		c := ServerCPU{Total: *h.CPUTotalMHz}
		for _, p := range []struct {
			from *int64
			to   *int64
		}{{h.CPUUsedMHz, &c.Used}, {h.CPUReservedMHz, &c.Reserved}, {h.CPUCores, &c.Cores}} {
			if p.from != nil {
				*p.to = *p.from
			}
		}
		v.CPU = &c
	}
	disks, err := s.store.DiskRows(ctx, store.MinuteRes, h.TS, h.TS.Add(time.Second))
	if err != nil {
		return
	}
	for _, d := range disks {
		if d.Node != v.NodeID {
			continue
		}
		pct := metrics.UsedPercent(d.UsedMB, d.FreeMB)
		v.Disks = append(v.Disks, HostDisk{Mount: d.Mount, Holds: strings.Split(d.Holds, ","),
			UsedMB: d.UsedMB, TotalMB: d.TotalMB, FreeMB: d.FreeMB, UsedPercent: pct,
			State: metrics.DiskState(pct, false).State})
	}
}

// clusterView sums the cluster up.
func (s *Service) clusterView(ctx context.Context, rows []store.Server, views []ServerView, nodes []deploy.Node, nomadOK bool) ClusterView {
	c := ClusterView{Servers: len(views), Nomad: "ok"}
	if !nomadOK {
		c.Nomad = "unreachable"
	}
	wanted, running := 0, true
	for _, r := range rows {
		if r.NomadServer || r.First {
			wanted++
			if rep, ok := parseReport(r.Report); !ok || !rep.ServerRunning {
				running = false
			}
		}
	}
	c.NomadServers = wanted
	c.SurvivesLoss = wanted >= maxNomadServers && running
	for _, v := range views {
		if v.State == "ready" {
			c.Ready++
		}
	}
	for _, n := range nodes {
		if !n.Down() {
			c.MemoryMB += int64(n.MemoryMB)
			c.CPUCores += int64(n.Cores)
		}
	}
	c.CanAdd = true
	if err := s.ClusterStatus(ctx); err != nil {
		c.CanAdd, c.CannotAddReason = false, err.Error()
	} else if len(rows) >= mesh.MaxIndex {
		c.CanAdd, c.CannotAddReason = false, "The cluster has the most servers it can have."
	}
	return c
}

// RemoveServer takes a server out of the cluster. A waiting join can always be cancelled; a joining
// or ready server only when Nomad has no node for it or says it is down. The first server is never
// removed.
func (s *Service) RemoveServer(ctx context.Context, nameOrID string) error {
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	row, err := s.store.ServerByName(ctx, nameOrID)
	if errors.Is(err, store.ErrNotFound) {
		row, err = s.store.ServerByID(ctx, nameOrID)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &RefusalError{Field: "name", Err: ErrServerNotFound}
	case err != nil:
		return fmt.Errorf("remove server: %w", err)
	}
	if row.First {
		return ErrFirstServer
	}
	if row.State != store.ServerWaiting {
		nodes, err := s.deployer.Nodes(ctx)
		if err != nil {
			return err // without Nomad it can't be told whether the server is running
		}
		if node := nodeOf(nodes, row.Index); node != nil && !node.Down() {
			return &ServerRunningError{Name: row.Name}
		}
	}
	if err := s.store.DeleteServer(ctx, row.Index); err != nil {
		return fmt.Errorf("remove server: %w", err)
	}
	if err := s.reconcileRoles(ctx); err != nil {
		return err
	}
	s.publish(Change{What: ChangeServers})
	return nil
}

// AdoptFirst registers the server Caravana runs on as server 1 (the CLI's `caravana server
// adopt-first`): a first server row when there is none, or a new node token, key and endpoint for
// the one there is. It returns the node token, the only time it exists.
func (s *Service) AdoptFirst(ctx context.Context, name, publicKey, address string) (string, error) {
	return AdoptFirstServer(ctx, s.store, s.clock.Now, name, publicKey, address)
}

// AdoptFirstServer is AdoptFirst for a caller that has only a store: the CLI, which opens it beside
// the running server.
func AdoptFirstServer(ctx context.Context, st *store.Store, now func() time.Time, name, publicKey, address string) (string, error) {
	if !mesh.ValidName(name) {
		return "", app.NewFieldError("name", "must be lowercase letters, digits and dashes, 63 characters at most")
	}
	if !mesh.ValidPublicKey(publicKey) {
		return "", app.NewFieldError("publicKey", "must be a WireGuard public key: 44 characters of base64")
	}
	endpoint, err := joinEndpoint(address, "")
	if err != nil {
		return "", err
	}
	tokenHash, token, err := newNodeToken()
	if err != nil {
		return "", err
	}
	rows, err := st.ListServers(ctx)
	if err != nil {
		return "", fmt.Errorf("adopt first server: %w", err)
	}
	if slices.ContainsFunc(rows, func(r store.Server) bool { return r.First }) {
		if _, err := st.RotateFirstServer(ctx, tokenHash, publicKey, endpoint); err != nil {
			return "", fmt.Errorf("adopt first server: %w", err)
		}
		return token, nil
	}
	t := now().UTC()
	v, err := st.InsertServer(ctx, store.Server{
		ID: newTokenID(), Name: name, Pool: "default", State: store.ServerReady, First: true, NomadServer: true,
		PublicKey: publicKey, Endpoint: endpoint, TokenHash: tokenHash, CreatedAt: t, JoinedAt: t, ReadyAt: t,
	})
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		return "", &RefusalError{Field: "name", Err: ErrServerExists}
	case err != nil:
		return "", fmt.Errorf("adopt first server: %w", err)
	case v.Index != 1:
		// Index 1 belongs to the first server alone. Roll the row back rather than leave a first
		// server that every address derivation would get wrong.
		_ = st.DeleteServer(ctx, v.Index)
		return "", fmt.Errorf("adopt first server: index 1 is taken by a server that isn't the first")
	}
	return token, nil
}
