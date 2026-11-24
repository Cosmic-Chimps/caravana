package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/cosmic-chimps/caravana/internal/mesh"
)

// Changing the console's address (feature 051, contracts/agent.md). The console never gets root:
// it hands this agent a change in its sync answer (an ID and a domain, nothing else), and the agent,
// which is root beside Caravana on the first server, checks both and rewrites exactly two lines of
// two files and restarts exactly one unit. A joined server only follows the console's new URL.

// Where the two files are, under the Agent's Root, and the console's own listener.
const (
	consoleEnvFile = "/etc/caravana/caravana.env"
	installEnvFile = "/var/lib/caravana-host/install.env"

	publicURLKey = "CARAVANA_PUBLIC_URL="
	domainKey    = "CARAVANA_DOMAIN="

	localConsoleURL = "http://127.0.0.1:7070"
	// healthAttempts one second apart make the 60 s the console is given to answer after the restart.
	healthAttempts = 60
)

// The states of a console address outcome.
const (
	ConsoleApplied = "applied"
	ConsoleFailed  = "failed"
	ConsoleRefused = "refused"
	ConsoleRunning = "running"
)

// ConsoleRequest is the answer's consoleAddress block: switch the console to Domain. ID is the
// change's, 32 lowercase hex digits. Neither is trusted until applyConsole has checked both.
type ConsoleRequest struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
}

// ConsoleOutcome is how the last console address request this agent handled went. It is kept in
// state.json and reported to the console, so the console settles the change. Domain is the request's
// domain and is only kept (never reported): a request found `running` after a crash needs it.
type ConsoleOutcome struct {
	ID     string `json:"id"`
	State  string `json:"state"` // applied, failed, refused or running
	Reason string `json:"reason,omitempty"`
	Domain string `json:"domain,omitempty"`
}

// reported is the outcome as the report carries it: without the domain.
func (o *ConsoleOutcome) reported() *ConsoleOutcome {
	if o == nil {
		return nil
	}
	return &ConsoleOutcome{ID: o.ID, State: o.State, Reason: o.Reason}
}

var consoleIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// validate says a stored outcome is one this agent wrote.
func (o *ConsoleOutcome) validate() error {
	if o == nil {
		return nil
	}
	switch {
	case !consoleIDRE.MatchString(o.ID):
		return errors.New("state.json: consoleAddress.id isn't 32 lowercase hex characters")
	case o.State != ConsoleApplied && o.State != ConsoleFailed && o.State != ConsoleRefused && o.State != ConsoleRunning:
		return errors.New("state.json: consoleAddress.state isn't applied, failed, refused or running")
	case o.Domain != "" && !mesh.ValidConsoleDomain(o.Domain):
		return errors.New("state.json: consoleAddress.domain isn't a host name")
	}
	return nil
}

// loopbackConsole says this agent's node.env URL is the console's own listener: the agent runs beside
// Caravana, and the console's address is a file on this server.
func loopbackConsole(env NodeEnv) bool {
	return strings.TrimRight(env.URL, "/") == localConsoleURL
}

// RunsConsole says this agent runs on the server that holds the console: its node.env URL is the
// loopback listener and Caravana's env file is here. Only such an agent is handed a change of the
// console's address, and it says so in its report.
func (a *Agent) RunsConsole(env NodeEnv) bool {
	if !loopbackConsole(env) {
		return false
	}
	_, err := os.Stat(a.path(consoleEnvFile))
	return err == nil
}

// applyConsole is the sync's step for the console's address, and returns the state with what it
// recorded (the caller carries it on, so a later save doesn't lose it). First a request this agent
// found `running` (it was stopped in the middle) is settled. Then req, when there is one:
//   - an ID that isn't 32 lowercase hex digits can't be told to the console: nothing is recorded and
//     one line is logged;
//   - an ID this agent already has an outcome for is not applied again (the outcome keeps being
//     reported);
//   - a domain that isn't a host name, or an agent that doesn't run the console, is `refused`;
//   - anything else is applied (applyConsoleChange).
//
// It never returns an error: what goes wrong is `failed` or `refused` in the outcome or a line on
// standard error, so the other steps of the sync go on. When state.json can't be written nothing is
// changed.
func (a *Agent) applyConsole(ctx context.Context, env NodeEnv, st State, req *ConsoleRequest) State {
	if st.ConsoleAddress != nil && st.ConsoleAddress.State == ConsoleRunning {
		st = a.settleInterruptedConsole(ctx, st)
	}
	if req == nil {
		return st
	}
	if !consoleIDRE.MatchString(req.ID) {
		a.warn("caravana node sync: the console asked for a change of its address with an ID that isn't 32 hex digits. Ignored.")
		return st
	}
	if st.ConsoleAddress != nil && st.ConsoleAddress.ID == req.ID {
		return st
	}
	switch {
	case !mesh.ValidConsoleDomain(req.Domain):
		return a.recordConsole(st, ConsoleOutcome{ID: req.ID, State: ConsoleRefused, Reason: "The domain isn't a host name this agent accepts."})
	case !a.RunsConsole(env):
		return a.recordConsole(st, ConsoleOutcome{ID: req.ID, State: ConsoleRefused, Reason: "This server's agent doesn't run beside Caravana's console."})
	}
	return a.applyConsoleChange(ctx, st, req)
}

// recordConsole saves an outcome in state.json and returns the state with it. When the save fails the
// state is returned as it was: the request is seen again at the next sync.
func (a *Agent) recordConsole(st State, o ConsoleOutcome) State {
	next := st
	next.ConsoleAddress = &o
	if err := a.SaveState(next); err != nil {
		a.warn("caravana node sync: the outcome of the console address change wasn't saved: %v", err)
		return st
	}
	return next
}

// fileEdit is one of the two files with what it will say and what it said.
type fileEdit struct {
	path     string // under Root
	original []byte
	updated  []byte
}

// applyConsoleChange writes the two files, restarts Caravana and waits for it. The running outcome is
// saved first, so a crash leaves a mark the next sync reads. On any failure after a file was written
// both files are put back and Caravana is restarted again.
func (a *Agent) applyConsoleChange(ctx context.Context, st State, req *ConsoleRequest) State {
	envEdit, installEdit, outcome := a.planConsoleFiles(req)
	if outcome != nil {
		return a.recordConsole(st, *outcome)
	}
	running := a.recordConsole(st, ConsoleOutcome{ID: req.ID, State: ConsoleRunning, Domain: req.Domain})
	if running.ConsoleAddress == st.ConsoleAddress {
		return st // state.json can't be written: nothing is changed
	}
	st = running
	fail := func(reason string) State {
		return a.recordConsole(st, ConsoleOutcome{ID: req.ID, State: ConsoleFailed, Reason: reason, Domain: req.Domain})
	}

	// The env file first: Caravana's address is what the change is about, and a crash after it is the
	// case the next sync knows how to finish (settleInterruptedConsole).
	if err := rewriteFile(envEdit.path, envEdit.updated); err != nil {
		return fail("Caravana's env file couldn't be written; nothing was changed.")
	}
	if err := rewriteFile(installEdit.path, installEdit.updated); err != nil {
		_ = rewriteFile(envEdit.path, envEdit.original)
		return fail("The installer's record couldn't be written; nothing was changed.")
	}
	healthy := a.restartConsole(ctx)
	if !healthy {
		_ = rewriteFile(envEdit.path, envEdit.original)
		_ = rewriteFile(installEdit.path, installEdit.original)
		a.restartConsole(ctx) // the old address comes back; whether it answers is the next sync's report
		return fail(fmt.Sprintf("Caravana didn't answer at https://%s within 60 s; the old address is back.", req.Domain))
	}
	a.out("Console address changed to %s.", req.Domain)
	return a.recordConsole(st, ConsoleOutcome{ID: req.ID, State: ConsoleApplied, Domain: req.Domain})
}

// planConsoleFiles reads both files and says what each will hold. A file that can't be changed safely
// gives the outcome instead (nothing has been written yet).
func (a *Agent) planConsoleFiles(req *ConsoleRequest) (envEdit, installEdit fileEdit, outcome *ConsoleOutcome) {
	failed := func(state, reason string) *ConsoleOutcome {
		return &ConsoleOutcome{ID: req.ID, State: state, Reason: reason, Domain: req.Domain}
	}
	envEdit = fileEdit{path: a.path(consoleEnvFile)}
	installEdit = fileEdit{path: a.path(installEnvFile)}
	var err error
	if envEdit.original, err = os.ReadFile(envEdit.path); err != nil {
		return fileEdit{}, fileEdit{}, failed(ConsoleFailed, "Caravana's env file couldn't be read; nothing was changed.")
	}
	if installEdit.original, err = os.ReadFile(installEdit.path); err != nil {
		return fileEdit{}, fileEdit{}, failed(ConsoleFailed, "The installer's record couldn't be read; nothing was changed.")
	}
	var n int
	envEdit.updated, n = replaceLines(envEdit.original, publicURLKey, "https://"+req.Domain)
	if n != 1 {
		return fileEdit{}, fileEdit{}, failed(ConsoleRefused, "Caravana's env file must have exactly one CARAVANA_PUBLIC_URL line; nothing was changed.")
	}
	installEdit.updated, n = replaceLines(installEdit.original, domainKey, req.Domain)
	if n == 0 {
		installEdit.updated = appendLine(installEdit.original, domainKey+req.Domain)
	}
	return envEdit, installEdit, nil
}

// replaceLines sets the value of every line that starts with key, keeping everything else byte for
// byte (a line's own line ending included), and says how many lines it changed.
func replaceLines(content []byte, key, value string) ([]byte, int) {
	lines := bytes.SplitAfter(content, []byte("\n"))
	n := 0
	for i, l := range lines {
		if !bytes.HasPrefix(l, []byte(key)) {
			continue
		}
		ending := ""
		switch {
		case bytes.HasSuffix(l, []byte("\r\n")):
			ending = "\r\n"
		case bytes.HasSuffix(l, []byte("\n")):
			ending = "\n"
		}
		lines[i] = []byte(key + value + ending)
		n++
	}
	return bytes.Join(lines, nil), n
}

// hasLine says some line of a file is exactly line (a line ending isn't part of it).
func hasLine(content []byte, line string) bool {
	for _, l := range bytes.Split(content, []byte("\n")) {
		if string(bytes.TrimSuffix(l, []byte("\r"))) == line {
			return true
		}
	}
	return false
}

// appendLine adds a line at the end of a file, after a line ending when the last line has none.
func appendLine(content []byte, line string) []byte {
	out := append([]byte(nil), content...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, line+"\n"...)
}

// rewriteFile writes a file whole through a temporary file in the same folder and a rename, with the
// original's mode, owner and group, so a reader never sees half of it and systemd still reads it.
func rewriteFile(path string, data []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
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
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if sys, isStat := fi.Sys().(*syscall.Stat_t); isStat {
		if err := tmp.Chown(int(sys.Uid), int(sys.Gid)); err != nil {
			tmp.Close()
			return err
		}
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

// restartConsole restarts Caravana and waits up to 60 s for it to answer its health check. It says
// whether it did.
func (a *Agent) restartConsole(ctx context.Context) bool {
	if _, err := a.run(ctx, "systemctl", "restart", "caravana"); err != nil {
		a.warn("caravana node sync: systemctl restart caravana failed: %v", err)
		return false
	}
	for i := 0; i < healthAttempts; i++ {
		if a.consoleHealthy(ctx) {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if a.Sleep != nil {
			a.Sleep(time.Second)
		} else {
			time.Sleep(time.Second)
		}
	}
	return false
}

// consoleHealthy asks Caravana's own listener for its health check: HTTP 200. Tests replace it with
// the Health field.
func (a *Agent) consoleHealthy(ctx context.Context) bool {
	if a.Health != nil {
		return a.Health(ctx)
	}
	return a.getOK(ctx, localConsoleURL+"/v1/health")
}

// getOK is a GET that says whether the answer is 200. Nothing of the answer is read.
func (a *Agent) getOK(ctx context.Context, rawURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// settleInterruptedConsole settles a request the state says is `running`: the agent was stopped in the
// middle of it. The env file is written first, so one that doesn't name the domain was never changed
// (and the installer's record wasn't either): `failed`. One that does is finished: the installer's
// record gets the domain, Caravana is restarted, and the outcome is `applied` when it answers and
// `failed` when it doesn't (the files can't be put back: the old address isn't known any more).
func (a *Agent) settleInterruptedConsole(ctx context.Context, st State) State {
	o := *st.ConsoleAddress
	fail := func(reason string) State {
		return a.recordConsole(st, ConsoleOutcome{ID: o.ID, State: ConsoleFailed, Reason: reason, Domain: o.Domain})
	}
	const stopped = "The agent stopped during the change."
	if o.Domain == "" || !mesh.ValidConsoleDomain(o.Domain) {
		return fail(stopped)
	}
	envFile, err := os.ReadFile(a.path(consoleEnvFile))
	if err != nil || !hasLine(envFile, publicURLKey+"https://"+o.Domain) {
		return fail(stopped)
	}
	record, err := os.ReadFile(a.path(installEnvFile))
	if err != nil {
		return fail(stopped)
	}
	updated, n := replaceLines(record, domainKey, o.Domain)
	if n == 0 {
		updated = appendLine(record, domainKey+o.Domain)
	}
	if !bytes.Equal(updated, record) {
		if err := rewriteFile(a.path(installEnvFile), updated); err != nil {
			return fail(stopped)
		}
	}
	if !a.restartConsole(ctx) {
		return fail(fmt.Sprintf("The agent stopped during the change, and Caravana didn't answer at https://%s within 60 s: look at %s.", o.Domain, consoleEnvFile))
	}
	return a.recordConsole(st, ConsoleOutcome{ID: o.ID, State: ConsoleApplied, Domain: o.Domain})
}

// followConsoleURL makes a joined server call the console at its new address (contracts/agent.md
// §Follow). It acts only when node.env's URL isn't the loopback listener and differs from consoleURL,
// which must be an address ValidConsoleURL accepts; the new address must answer its health check with
// the agent's usual TLS setting before node.env is rewritten (token and insecure as they were). Any
// failure keeps the URL: the next sync tries again. The server that runs the console (node.env at the
// loopback listener) never follows: its address is the file the change above rewrites.
func (a *Agent) followConsoleURL(ctx context.Context, env NodeEnv, consoleURL string) {
	consoleURL = strings.TrimRight(consoleURL, "/")
	if consoleURL == "" || loopbackConsole(env) || consoleURL == strings.TrimRight(env.URL, "/") {
		return
	}
	if ValidConsoleURL(consoleURL) != nil {
		return
	}
	if !a.getOK(ctx, consoleURL+"/v1/health") {
		a.warn("caravana node sync: the console's new address %s doesn't answer yet; this server keeps calling %s.", consoleURL, strings.TrimRight(env.URL, "/"))
		return
	}
	next := env
	next.URL = consoleURL
	if err := a.SaveNodeEnv(next); err != nil {
		a.warn("caravana node sync: node.env couldn't be updated to the console's new address: %v", err)
		return
	}
	a.out("The console is at %s now; this server calls it there.", consoleURL)
}
