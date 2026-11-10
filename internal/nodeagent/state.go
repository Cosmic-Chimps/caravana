package nodeagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// Roles of a server in the Nomad cluster.
const (
	RoleClient = "client"
	RoleServer = "server"
)

// stateVersion is the format of state.json.
const stateVersion = 1

// State is what the agent last applied (state.json, contracts/node.md §2).
type State struct {
	Version         int         `json:"version"`
	Index           int         `json:"index"`
	Name            string      `json:"name"`
	Pool            string      `json:"pool"`
	First           bool        `json:"first"`
	Role            string      `json:"role"`
	Servers         []int       `json:"servers"` // indexes of the Nomad servers
	CPUTotalCompute int         `json:"cpuTotalCompute"`
	Peers           []mesh.Peer `json:"peers"`
}

// Validate says a state read from disk is one this agent can act on.
func (s State) Validate() error {
	switch {
	case s.Version != stateVersion:
		return fmt.Errorf("state.json: version %d isn't one this agent knows", s.Version)
	case s.Index < 1 || s.Index > mesh.MaxIndex:
		return fmt.Errorf("state.json: index %d is outside 1 to %d", s.Index, mesh.MaxIndex)
	case !ValidName(s.Name):
		return errors.New("state.json: name isn't a valid name")
	case !ValidName(s.Pool):
		return errors.New("state.json: pool isn't a valid name")
	case s.Role != RoleClient && s.Role != RoleServer:
		return errors.New("state.json: role must be client or server")
	}
	return nil
}

// LoadState reads state.json. A missing file is an error that satisfies errors.Is(err, fs.ErrNotExist).
func (a *Agent) LoadState() (State, error) {
	b, err := os.ReadFile(a.path(stateFile))
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, errors.New("state.json isn't valid JSON")
	}
	if err := s.Validate(); err != nil {
		return State{}, err
	}
	return s, nil
}

// SaveState writes state.json (0600), through a temporary file and a rename.
func (a *Agent) SaveState(s State) error {
	s.Version = stateVersion
	if s.Servers == nil {
		s.Servers = []int{}
	}
	if s.Peers == nil {
		s.Peers = []mesh.Peer{}
	}
	sort.Ints(s.Servers)
	sort.SliceStable(s.Peers, func(i, j int) bool { return s.Peers[i].Index < s.Peers[j].Index })
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	if err := a.ensureAgentDir(); err != nil {
		return err
	}
	return writeFile(a.path(stateFile), append(b, '\n'), 0o600)
}

// NodeEnv is node.env: where the console is and the token this server calls it with.
type NodeEnv struct {
	URL      string
	Token    string
	Insecure bool
}

// LoadNodeEnv reads node.env as text, the installer's env_value rule: a line `NAME=value`, one
// leading and one trailing single quote removed, never sourced. The bool says the file exists.
func (a *Agent) LoadNodeEnv() (NodeEnv, bool, error) {
	b, ok, err := readIfExists(a.path(nodeEnv))
	if err != nil || !ok {
		return NodeEnv{}, false, err
	}
	s := string(b)
	return NodeEnv{
		URL:      envValue(s, "CARAVANA_URL"),
		Token:    envValue(s, "CARAVANA_NODE_TOKEN"),
		Insecure: envValue(s, "CARAVANA_INSECURE") == "1",
	}, true, nil
}

// SaveNodeEnv writes node.env (0600).
func (a *Agent) SaveNodeEnv(e NodeEnv) error {
	insecure := "0"
	if e.Insecure {
		insecure = "1"
	}
	var b bytes.Buffer
	for _, kv := range [][2]string{
		{"CARAVANA_URL", e.URL},
		{"CARAVANA_NODE_TOKEN", e.Token},
		{"CARAVANA_INSECURE", insecure},
	} {
		q, err := envQuote(kv[1])
		if err != nil {
			return fmt.Errorf("node.env: %s: %w", kv[0], err)
		}
		fmt.Fprintf(&b, "%s=%s\n", kv[0], q)
	}
	if err := a.ensureAgentDir(); err != nil {
		return err
	}
	return writeFile(a.path(nodeEnv), b.Bytes(), 0o600)
}

var envPlain = regexp.MustCompile(`^[A-Za-z0-9._/:@,+=-]*$`)

// envQuote writes a value as the installer's envq does: plain when every character is safe, else in
// single quotes; a value with a single quote can't be written either way.
func envQuote(v string) (string, error) {
	switch {
	case envPlain.MatchString(v):
		return v, nil
	case strings.ContainsAny(v, "'\n\r"):
		return "", errors.New("the value can't be written to an env file")
	}
	return "'" + v + "'", nil
}

// envValue is the installer's env_value: the first line `NAME=...`, one leading and one trailing
// single quote removed.
func envValue(content, name string) string {
	for _, line := range strings.Split(content, "\n") {
		v, ok := strings.CutPrefix(line, name+"=")
		if !ok {
			continue
		}
		v = strings.TrimSuffix(v, "\r")
		v = strings.TrimPrefix(v, "'")
		return strings.TrimSuffix(v, "'")
	}
	return ""
}

// ValidName says s is a server or pool name: a lowercase DNS label, at most 63 characters
// (mesh.ValidName; the console applies the same rule).
func ValidName(s string) bool { return mesh.ValidName(s) }

// NameOfHost makes a valid name from a host name (mesh.NameOfHost).
func NameOfHost(host string, index int) string { return mesh.NameOfHost(host, index) }
