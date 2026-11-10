// Package nodeagent is the root agent that runs on every Caravana server: `caravana node <command>`
// (feature 031, contracts/node.md). It keeps the server's WireGuard interfaces, its Nomad
// configuration and certificate in line with what Caravana wants, and it only ever applies values,
// never commands or paths: nothing the console answers becomes a program, a file name or an address
// (the keys and endpoints it does take are validated first).
//
// All host access goes through the Agent struct, so tests replace it: a root folder for every path,
// a Runner for the programs (ip, wg, systemctl, ping, df, docker, nomad, uname), an HTTP client and
// a clock. Nothing here needs Linux-only system calls, so the binary also builds for macOS.
package nodeagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Where things are on a server, under the Agent's Root (contracts/node.md §2).
const (
	agentDir  = "/etc/caravana-node"
	stateFile = agentDir + "/state.json"
	meshKey   = agentDir + "/mesh.key"
	nodeEnv   = agentDir + "/node.env"
	gossipKey = agentDir + "/gossip.key"
	caKeyFile = agentDir + "/ca-key.pem"

	tlsDir    = "/etc/nomad.d/tls"
	caCert    = tlsDir + "/ca.pem"
	agentCert = tlsDir + "/agent.pem"
	agentKey  = tlsDir + "/agent-key.pem"
	nomadConf = "/etc/nomad.d/nomad.hcl"

	nomadDataDir = "/opt/nomad/data"
)

// Runner runs a program and returns what it wrote on standard output. No shell is involved.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout []byte, err error)
}

// Agent is the host the commands act on.
type Agent struct {
	// Root is the folder every path is under: "/" on a server, a temporary folder in tests.
	Root string
	// Runner runs ip, wg, systemctl, ping, df, docker, nomad and uname.
	Runner Runner
	// HTTP calls the console. It must not follow redirects.
	HTTP *http.Client
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Sleep waits between checks of something that takes time; nil means time.Sleep. Tests set it.
	Sleep func(time.Duration)
	// Version is this agent's version, sent in the facts and the report.
	Version string
	// NomadAddr is the local Nomad's HTTP address; empty means http://127.0.0.1:4646.
	NomadAddr string
	// Stdout and Stderr are where the commands print; nil means nothing is printed.
	Stdout, Stderr io.Writer
}

func (a *Agent) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Agent) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return NewHTTPClient(false)
}

func (a *Agent) nomadAddr() string {
	if a.NomadAddr != "" {
		return strings.TrimRight(a.NomadAddr, "/")
	}
	return "http://127.0.0.1:4646"
}

func (a *Agent) path(name string) string { return filepath.Join(a.Root, name) }

func (a *Agent) out(format string, args ...any) {
	if a.Stdout != nil {
		fmt.Fprintf(a.Stdout, format+"\n", args...)
	}
}

func (a *Agent) warn(format string, args ...any) {
	if a.Stderr != nil {
		fmt.Fprintf(a.Stderr, format+"\n", args...)
	}
}

func (a *Agent) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return a.Runner.Run(ctx, name, args...)
}

// ---- files ----------------------------------------------------------------------------------

// ensureDir makes a folder with this mode, and sets the mode when it already exists.
func ensureDir(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// ensureAgentDir makes /etc/caravana-node, root only.
func (a *Agent) ensureAgentDir() error {
	return ensureDir(a.path(agentDir), 0o700)
}

// writeFile writes a file whole: to a temporary file in the same folder with its final mode, then
// renamed over the target, so a reader never sees half of it and a crash leaves the old one.
func writeFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// writeIfChanged writes the file unless it already holds exactly this, with this mode. It says
// whether it wrote.
func writeIfChanged(path string, data []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() == mode {
			return false, nil
		}
	}
	return true, writeFile(path, data, mode)
}

func readIfExists(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// clean makes text from the console safe to print: no control characters, at most 300 bytes.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// ---- the real host ----------------------------------------------------------------------------

// RunTimeout is how long one program may run.
const RunTimeout = 30 * time.Second

// ExecRunner runs programs on this machine, with a timeout and no shell.
type ExecRunner struct{ Timeout time.Duration }

// Run implements Runner. The error holds the program's name and arguments and, except for wg, the
// start of its standard error. wg's is left out: when it can't read a config it prints the line it
// choked on, and that line may be the private key.
func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = RunTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := ""
		if name != "wg" {
			detail = ": " + clean(stderr.String())
		}
		return stdout.Bytes(), fmt.Errorf("%s %s failed: %v%s", name, firstWord(args), err, detail)
	}
	return stdout.Bytes(), nil
}

// firstWord names the subcommand in an error without repeating arguments (which may be paths).
func firstWord(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// NewHTTPClient is the console client: 20 s timeout, redirects are not followed, and the console's
// certificate is not verified only when insecure is true (a console on Traefik's own certificate).
func NewHTTPClient(insecure bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // only with --insecure, which the owner's join command says
	}
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
