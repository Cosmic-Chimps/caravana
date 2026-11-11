package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// Reach is one peer, pinged over the mesh.
type Reach struct {
	Index int  `json:"index"`
	OK    bool `json:"ok"`
}

// Report is what a sync tells the console about this server (api.md §4, POST /v1/node/sync).
type Report struct {
	Agent               string  `json:"agent"`
	Nomad               string  `json:"nomad"`
	Docker              string  `json:"docker"`
	DockerRunning       bool    `json:"dockerRunning"`
	DiskFreeMB          int64   `json:"diskFreeMb"`
	DiskTotalMB         int64   `json:"diskTotalMb"`
	MemoryMB            int64   `json:"memoryMb"`
	Kernel              string  `json:"kernel"`
	Role                string  `json:"role"`
	NomadLeader         bool    `json:"nomadLeader"`
	ServerRunning       bool    `json:"serverRunning"`
	CertificateNotAfter string  `json:"certificateNotAfter,omitempty"`
	Reach               []Reach `json:"reach"`
}

// Facts are what a join tells the console about a new server (api.md §4, POST /v1/join).
type Facts struct {
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

// pingParallel is how many peers are pinged at once.
const pingParallel = 8

// facts gathers what is true of this host. Anything it can't read is left empty or zero.
func (a *Agent) facts(ctx context.Context) (f Facts, dockerRunning bool) {
	if out, err := a.run(ctx, "uname", "-n"); err == nil {
		f.Hostname = strings.TrimSpace(string(out))
	}
	if out, err := a.run(ctx, "uname", "-r"); err == nil {
		f.Kernel = strings.TrimSpace(string(out))
	}
	f.Arch = runtime.GOARCH
	f.MemoryMB = a.memoryMB()
	f.DiskFreeMB, f.DiskTotalMB = a.disk(ctx)
	if out, err := a.run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err == nil {
		f.Docker = strings.TrimSpace(string(out))
		dockerRunning = true
	}
	if out, err := a.run(ctx, "nomad", "version"); err == nil {
		f.Nomad = parseNomadVersion(string(out))
	}
	f.Agent = a.Version
	return f, dockerRunning
}

// Report gathers the report of this sync: the facts, the role, whether the local Nomad knows a
// leader, for a server whether it is among Nomad's peers, and per peer one ping.
func (a *Agent) Report(ctx context.Context, st State) Report {
	f, dockerRunning := a.facts(ctx)
	r := Report{
		Agent: f.Agent, Nomad: f.Nomad, Docker: f.Docker, DockerRunning: dockerRunning,
		DiskFreeMB: f.DiskFreeMB, DiskTotalMB: f.DiskTotalMB, MemoryMB: f.MemoryMB, Kernel: f.Kernel,
		Role: st.Role, Reach: []Reach{},
	}
	if b, ok, err := readIfExists(a.path(agentCert)); err == nil && ok {
		if end, err := clusterca.NotAfter(b); err == nil {
			r.CertificateNotAfter = end.UTC().Format(time.RFC3339)
		}
	}
	var leader string
	if a.nomadGet(ctx, "/v1/status/leader", &leader) == nil {
		r.NomadLeader = leader != ""
	}
	if st.Role == RoleServer {
		var peers []string
		if addrs, err := mesh.Of(st.Index); err == nil && a.nomadGet(ctx, "/v1/status/peers", &peers) == nil {
			own := fmt.Sprintf("%s:%d", addrs.Mesh, nomadRPCPort)
			for _, p := range peers {
				if p == own {
					r.ServerRunning = true
				}
			}
		}
	}
	r.Reach = a.reach(ctx, st.Peers)
	return r
}

// reach pings every peer's mesh address once, eight at a time, and returns the results by index.
func (a *Agent) reach(ctx context.Context, peers []mesh.Peer) []Reach {
	out := make([]Reach, len(peers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, pingParallel)
	for i, p := range peers {
		out[i] = Reach{Index: p.Index}
		addrs, err := mesh.Of(p.Index)
		if err != nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			_, err := a.run(ctx, "ping", "-c", "1", "-W", "2", addrs.Mesh.String())
			out[i].OK = err == nil
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// nomadGet reads a JSON answer of the local Nomad's HTTP API, which needs no token for /v1/status.
func (a *Agent) nomadGet(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.nomadAddr()+path, nil)
	if err != nil {
		return err
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("nomad answered HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// memoryMB reads MemTotal from /proc/meminfo; 0 where there is none.
func (a *Agent) memoryMB() int64 {
	b, err := os.ReadFile(a.path("/proc/meminfo"))
	if err != nil {
		return 0
	}
	return parseMemTotalMB(string(b))
}

func parseMemTotalMB(meminfo string) int64 {
	for _, line := range strings.Split(meminfo, "\n") {
		rest, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}

// disk asks df about the disk under Nomad's data directory, or the nearest folder above it that
// exists (Nomad may not have made its own yet).
func (a *Agent) disk(ctx context.Context) (freeMB, totalMB int64) {
	dir := a.path(nomadDataDir)
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return 0, 0
		}
		dir = parent
	}
	out, err := a.run(ctx, "df", "-Pm", dir)
	if err != nil {
		return 0, 0
	}
	return parseDF(string(out))
}

// parseDF reads `df -Pm`'s one data row: filesystem, 1M-blocks, used, available, capacity, mount.
func parseDF(out string) (freeMB, totalMB int64) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 6 {
		return 0, 0
	}
	total, err1 := strconv.ParseInt(f[1], 10, 64)
	free, err2 := strconv.ParseInt(f[3], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return free, total
}

// parseNomadVersion reads "Nomad v2.0.7" (and the lines after it) as "2.0.7".
func parseNomadVersion(out string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	v := strings.TrimPrefix(strings.TrimSpace(first), "Nomad")
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, " ")
	v, _, _ = strings.Cut(v, "+")
	return v
}
