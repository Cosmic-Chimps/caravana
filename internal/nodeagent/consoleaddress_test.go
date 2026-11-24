package nodeagent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	consoleChangeID  = "0123456789abcdef0123456789abcdef"
	consoleChangeID2 = "fedcba9876543210fedcba9876543210"

	oldEnvFile = "# Caravana's settings\nNOMAD_ADDR=http://127.0.0.1:4646\nCARAVANA_PUBLIC_URL=https://old.example.com\n\nCARAVANA_TRUSTED_PROXIES=127.0.0.1,::1\nCARAVANA_ACME=production\n"
	oldInstall = "CARAVANA_ROLE=first\nCARAVANA_ADMIN_EMAIL=ana@example.com\nCARAVANA_DOMAIN=old.example.com\nCARAVANA_ACME=production\n"
)

// routeToConsole makes the agent's calls to http://127.0.0.1:7070 (the console's loopback listener, which
// is where a first server's node.env points) reach the fake console instead.
func routeToConsole(a *testAgent) {
	target := a.console.srv.Listener.Addr().String()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	a.HTTP = &http.Client{Timeout: 5 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// consoleServer is an agent that runs beside the console: node.env at the loopback listener, Caravana's
// env file and the installer's record in place (mode 0640 and 0600, as the installer makes them), a
// health check that answers as the test says, and no waiting.
type consoleServer struct {
	*testAgent
	healthy  bool
	healthOK func() bool // when set, decides instead of healthy
	checks   int
	// onHealth runs on every health check: tests look at the files and the state at that moment.
	onHealth func()
}

func newConsoleServer(t *testing.T) *consoleServer {
	t.Helper()
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	routeToConsole(a)
	if err := a.SaveNodeEnv(NodeEnv{URL: localConsoleURL, Token: sentinelNodeToken}); err != nil {
		t.Fatal(err)
	}
	a.writeFiles(t, map[string]string{"etc/caravana/caravana.env": oldEnvFile, "var/lib/caravana-host/install.env": oldInstall})
	if err := os.Chmod(a.file("etc/caravana/caravana.env"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(a.file("var/lib/caravana-host/install.env"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &consoleServer{testAgent: a, healthy: true}
	a.Sleep = func(time.Duration) {}
	a.Health = func(context.Context) bool {
		s.checks++
		if s.onHealth != nil {
			s.onHealth()
		}
		if s.healthOK != nil {
			return s.healthOK()
		}
		return s.healthy
	}
	return s
}

// answers makes the console answer syncs with a request for the change (nil: none).
func (s *consoleServer) answers(req map[string]any) {
	s.console.on("/v1/node/sync", s.syncAnswer(func(m map[string]any) {
		if req != nil {
			m["consoleAddress"] = req
		}
	}))
}

func changeRequest(id, domain string) map[string]any {
	return map[string]any{"id": id, "domain": domain}
}

func (s *consoleServer) sync(t *testing.T) {
	t.Helper()
	if err := s.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatalf("Sync: %v\n%s", err, s.stderr.String())
	}
}

// restarts counts `systemctl restart caravana` calls.
func (s *consoleServer) restarts() int {
	n := 0
	for _, c := range s.runner.commands() {
		if c == "systemctl restart caravana" {
			n++
		}
	}
	return n
}

// untouched is a snapshot of every file but state.json.
func (s *consoleServer) untouched(t *testing.T) map[string]string {
	t.Helper()
	snap := s.snapshot(t)
	delete(snap, "/etc/caravana-node/state.json")
	return snap
}

func (s *consoleServer) outcome(t *testing.T) *ConsoleOutcome {
	t.Helper()
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return st.ConsoleAddress
}

// reportedOutcome is the consoleAddress member of the last sync's report.
func (s *consoleServer) reportedOutcome(t *testing.T) map[string]any {
	t.Helper()
	reqs := s.console.calls("/v1/node/sync")
	out, _ := reqs[len(reqs)-1].body["consoleAddress"].(map[string]any)
	return out
}

func TestRunsConsole(t *testing.T) {
	s := newConsoleServer(t)
	s.answers(nil)
	s.sync(t)
	if got := s.console.calls("/v1/node/sync")[0].body["runsConsole"]; got != true {
		t.Errorf("runsConsole = %v on the server that runs the console", got)
	}
	if got := s.console.calls("/v1/node/sync")[0].body["consoleAddress"]; got != nil {
		t.Errorf("consoleAddress = %v before any request", got)
	}

	// No Caravana env file here: this agent doesn't run the console, whatever node.env says.
	if err := os.Remove(s.file("etc/caravana/caravana.env")); err != nil {
		t.Fatal(err)
	}
	s.sync(t)
	if _, there := s.console.calls("/v1/node/sync")[1].body["runsConsole"]; there {
		t.Error("runsConsole is reported by an agent with no Caravana env file")
	}

	// A joined server: node.env points at the console's address.
	c := newConsole(t)
	j := newTestAgent(t, c)
	j.joined(t)
	c.on("/v1/node/sync", j.syncAnswer(nil))
	if err := j.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, there := c.calls("/v1/node/sync")[0].body["runsConsole"]; there {
		t.Error("runsConsole is reported by a joined server")
	}
}

func TestApplyConsoleChange(t *testing.T) {
	s := newConsoleServer(t)
	s.answers(changeRequest(consoleChangeID, "new.example.com"))
	s.onHealth = func() {
		// While Caravana is being waited for, both files already say the new address and the state says running.
		if !strings.Contains(s.read(t, "etc/caravana/caravana.env"), "CARAVANA_PUBLIC_URL=https://new.example.com\n") ||
			!strings.Contains(s.read(t, "var/lib/caravana-host/install.env"), "CARAVANA_DOMAIN=new.example.com\n") {
			t.Error("a file wasn't written before the health check")
		}
		if o := s.outcome(t); o == nil || o.State != ConsoleRunning || o.ID != consoleChangeID {
			t.Errorf("state at the health check = %+v, want running", o)
		}
	}
	s.sync(t)

	wantEnv := "# Caravana's settings\nNOMAD_ADDR=http://127.0.0.1:4646\nCARAVANA_PUBLIC_URL=https://new.example.com\n\nCARAVANA_TRUSTED_PROXIES=127.0.0.1,::1\nCARAVANA_ACME=production\n"
	wantInstall := "CARAVANA_ROLE=first\nCARAVANA_ADMIN_EMAIL=ana@example.com\nCARAVANA_DOMAIN=new.example.com\nCARAVANA_ACME=production\n"
	if got := s.read(t, "etc/caravana/caravana.env"); got != wantEnv {
		t.Errorf("caravana.env = %q, want %q", got, wantEnv)
	}
	if got := s.read(t, "var/lib/caravana-host/install.env"); got != wantInstall {
		t.Errorf("install.env = %q, want %q", got, wantInstall)
	}
	if m := s.mode(t, "etc/caravana/caravana.env"); m != 0o640 {
		t.Errorf("caravana.env mode = %v, want 0640", m)
	}
	if m := s.mode(t, "var/lib/caravana-host/install.env"); m != 0o600 {
		t.Errorf("install.env mode = %v, want 0600", m)
	}
	if got := s.restarts(); got != 1 {
		t.Errorf("systemctl restart caravana ran %d times, want 1", got)
	}
	for _, c := range s.runner.commands() {
		if strings.HasPrefix(c, "systemctl") && c != "systemctl restart caravana" {
			t.Errorf("an unexpected unit command: %s", c)
		}
	}
	if o := s.outcome(t); o == nil || o.ID != consoleChangeID || o.State != ConsoleApplied || o.Reason != "" || o.Domain != "new.example.com" {
		t.Errorf("outcome = %+v, want applied", o)
	}
	if left, _ := os.ReadDir(s.file("etc/caravana")); len(left) != 1 {
		t.Errorf("etc/caravana holds %d entries, want only caravana.env (no temporary file left)", len(left))
	}

	// The next sync reports it, without the domain, and doesn't apply it again.
	s.runner.reset()
	s.sync(t)
	rep := s.reportedOutcome(t)
	if rep["id"] != consoleChangeID || rep["state"] != "applied" || rep["domain"] != nil {
		t.Errorf("reported outcome = %v", rep)
	}
	if s.restarts() != 0 {
		t.Error("a request with an ID that was applied ran again")
	}
}

func TestApplyConsoleChangeAppendsTheDomainLine(t *testing.T) {
	for name, install := range map[string]string{
		"with a trailing newline":    "CARAVANA_ROLE=first\n",
		"without a trailing newline": "CARAVANA_ROLE=first",
	} {
		t.Run(name, func(t *testing.T) {
			s := newConsoleServer(t)
			s.writeFiles(t, map[string]string{"var/lib/caravana-host/install.env": install})
			s.answers(changeRequest(consoleChangeID, "new.example.com"))
			s.sync(t)
			if got, want := s.read(t, "var/lib/caravana-host/install.env"), "CARAVANA_ROLE=first\nCARAVANA_DOMAIN=new.example.com\n"; got != want {
				t.Errorf("install.env = %q, want %q", got, want)
			}
			if o := s.outcome(t); o == nil || o.State != ConsoleApplied {
				t.Errorf("outcome = %+v", o)
			}
		})
	}
}

func TestApplyConsoleChangeKeepsLineEndings(t *testing.T) {
	s := newConsoleServer(t)
	s.writeFiles(t, map[string]string{
		"etc/caravana/caravana.env":         "A=1\r\nCARAVANA_PUBLIC_URL=https://old.example.com\r\nB=2",
		"var/lib/caravana-host/install.env": "CARAVANA_DOMAIN=old.example.com\r\nX=y\r\n",
	})
	s.answers(changeRequest(consoleChangeID, "new.example.com"))
	s.sync(t)
	if got, want := s.read(t, "etc/caravana/caravana.env"), "A=1\r\nCARAVANA_PUBLIC_URL=https://new.example.com\r\nB=2"; got != want {
		t.Errorf("caravana.env = %q, want %q", got, want)
	}
	if got, want := s.read(t, "var/lib/caravana-host/install.env"), "CARAVANA_DOMAIN=new.example.com\r\nX=y\r\n"; got != want {
		t.Errorf("install.env = %q, want %q", got, want)
	}
}

// A request the agent can't trust changes nothing: no file, no unit, no health check; only a `refused`
// outcome naming nothing of what was asked.
func TestApplyConsoleRefusesACraftedDomain(t *testing.T) {
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 58) + ".com" // 254 bytes
	if len(long) != 254 {
		t.Fatalf("the long domain is %d bytes", len(long))
	}
	for name, domain := range map[string]string{
		"a newline":        "a.example.com\nCARAVANA_ACME=none",
		"a trailing break": "a.example.com\n",
		"a space":          "a b.example.com",
		"a quote":          `a"b.example.com`,
		"a single quote":   "a'b.example.com",
		"a command":        "a$(touch x).example.com",
		"a backtick":       "a`id`.example.com",
		"a slash":          "a.example.com/x",
		"a colon":          "a.example.com:",
		"a port":           "a.example.com:8443",
		"a scheme":         "https://a.example.com",
		"an IPv4 address":  "203.0.113.5",
		"an IPv6 address":  "2001:db8::1",
		"uppercase":        "Console.Example.com",
		"a trailing dot":   "console.example.com.",
		"one label":        "console",
		"localhost":        "localhost",
		"254 bytes":        long,
		"empty":            "",
		"a NUL":            "a\x00.example.com",
		"a semicolon":      "a.example.com;reboot",
		"an equals sign":   "a=b.example.com",
		"a backslash":      `a\.example.com`,
		"an underscore":    "a_b.example.com",
		"non-ASCII":        "é.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			s := newConsoleServer(t)
			s.answers(changeRequest(consoleChangeID, domain))
			before := s.untouched(t)
			s.sync(t)
			if !sameSnapshot(before, s.untouched(t)) {
				t.Error("a file changed")
			}
			if len(s.runner.commands()) != 0 {
				t.Errorf("programs ran: %v", s.runner.commands())
			}
			if s.checks != 0 {
				t.Error("Caravana's health was checked")
			}
			o := s.outcome(t)
			if o == nil || o.ID != consoleChangeID || o.State != ConsoleRefused || o.Reason == "" || o.Domain != "" {
				t.Errorf("outcome = %+v, want refused with no domain", o)
			}
			if o != nil && (strings.Contains(o.Reason, "$(") || strings.Contains(o.Reason, "\n")) {
				t.Errorf("the reason echoes the request: %q", o.Reason)
			}
			s.sync(t)
			if rep := s.reportedOutcome(t); rep["state"] != "refused" {
				t.Errorf("reported outcome = %v", rep)
			}
		})
	}
}

// An ID that isn't 32 lowercase hex digits can't be named to the console: nothing is recorded.
func TestApplyConsoleIgnoresABadID(t *testing.T) {
	for name, id := range map[string]string{
		"empty":     "",
		"short":     "abc",
		"33 digits": consoleChangeID + "0",
		"31 digits": consoleChangeID[:31],
		"uppercase": strings.ToUpper(consoleChangeID),
		"not hex":   strings.Repeat("g", 32),
		"a newline": consoleChangeID[:31] + "\n",
		"a path":    "../../../../etc/passwd" + strings.Repeat("0", 10),
	} {
		t.Run(name, func(t *testing.T) {
			s := newConsoleServer(t)
			s.answers(changeRequest(id, "new.example.com"))
			before := s.untouched(t)
			s.sync(t)
			if !sameSnapshot(before, s.untouched(t)) || len(s.runner.commands()) != 0 || s.checks != 0 {
				t.Error("something changed")
			}
			if o := s.outcome(t); o != nil {
				t.Errorf("an outcome was recorded: %+v", o)
			}
			if n := strings.Count(s.stderr.String(), "Ignored."); n != 1 {
				t.Errorf("stderr = %q, want one line saying it was ignored", s.stderr.String())
			}
			s.sync(t)
			if _, there := s.console.calls("/v1/node/sync")[1].body["consoleAddress"]; there {
				t.Error("the report names an ID that was never accepted")
			}
		})
	}
}

func TestApplyConsoleRefusesWhereItDoesNotRunTheConsole(t *testing.T) {
	t.Run("no Caravana env file", func(t *testing.T) {
		s := newConsoleServer(t)
		if err := os.Remove(s.file("etc/caravana/caravana.env")); err != nil {
			t.Fatal(err)
		}
		s.answers(changeRequest(consoleChangeID, "new.example.com"))
		before := s.untouched(t)
		s.sync(t)
		if o := s.outcome(t); o == nil || o.State != ConsoleRefused || !strings.Contains(o.Reason, "doesn't run") {
			t.Errorf("outcome = %+v", o)
		}
		if !sameSnapshot(before, s.untouched(t)) || len(s.runner.commands()) != 0 {
			t.Error("something changed")
		}
	})
	t.Run("node.env points at the console's address", func(t *testing.T) {
		s := newConsoleServer(t)
		if err := s.SaveNodeEnv(NodeEnv{URL: s.console.srv.URL, Token: sentinelNodeToken}); err != nil {
			t.Fatal(err)
		}
		s.HTTP = NewHTTPClient(false)
		s.answers(changeRequest(consoleChangeID, "new.example.com"))
		before := s.untouched(t)
		s.sync(t)
		if o := s.outcome(t); o == nil || o.State != ConsoleRefused {
			t.Errorf("outcome = %+v", o)
		}
		if !sameSnapshot(before, s.untouched(t)) || len(s.runner.commands()) != 0 {
			t.Error("something changed")
		}
	})
}

func TestApplyConsoleFilesThatCannotBeChanged(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		remove    string
		wantState string
	}{
		{"no public URL line", map[string]string{"etc/caravana/caravana.env": "A=1\n"}, "", ConsoleRefused},
		{"two public URL lines", map[string]string{"etc/caravana/caravana.env": oldEnvFile + "CARAVANA_PUBLIC_URL=https://other.example.com\n"}, "", ConsoleRefused},
		{"Caravana's env file is missing", nil, "etc/caravana/caravana.env", ConsoleRefused}, // not this agent's console any more
		{"the installer's record is missing", nil, "var/lib/caravana-host/install.env", ConsoleFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newConsoleServer(t)
			s.writeFiles(t, tt.files)
			if tt.remove != "" {
				if err := os.Remove(s.file(tt.remove)); err != nil {
					t.Fatal(err)
				}
			}
			s.answers(changeRequest(consoleChangeID, "new.example.com"))
			before := s.untouched(t)
			s.sync(t)
			if !sameSnapshot(before, s.untouched(t)) {
				t.Error("a file changed")
			}
			if len(s.runner.commands()) != 0 || s.checks != 0 {
				t.Errorf("programs ran: %v", s.runner.commands())
			}
			if o := s.outcome(t); o == nil || o.State != tt.wantState || o.Reason == "" {
				t.Errorf("outcome = %+v, want %s", o, tt.wantState)
			}
		})
	}
}

func TestApplyConsoleChangeNotHealthyPutsTheFilesBack(t *testing.T) {
	s := newConsoleServer(t)
	s.healthy = false
	s.answers(changeRequest(consoleChangeID, "new.example.com"))
	before := s.untouched(t)
	s.sync(t)

	if !sameSnapshot(before, s.untouched(t)) {
		t.Errorf("the files weren't put back byte for byte, mode included:\nbefore %v\nafter  %v", before, s.untouched(t))
	}
	if got := s.restarts(); got != 2 {
		t.Errorf("Caravana was restarted %d times, want 2 (the change, then the old address back)", got)
	}
	o := s.outcome(t)
	if o == nil || o.State != ConsoleFailed || o.Reason != "Caravana didn't answer at https://new.example.com within 60 s; the old address is back." {
		t.Errorf("outcome = %+v", o)
	}
	if s.checks != 120 { // 60 looks after each of the two restarts
		t.Errorf("health was checked %d times, want 120", s.checks)
	}
	// The failed outcome keeps being reported and isn't tried again.
	s.runner.reset()
	s.healthy = true
	s.sync(t)
	if s.restarts() != 0 || s.reportedOutcome(t)["state"] != "failed" {
		t.Errorf("after the failure: %d restarts, reported %v", s.restarts(), s.reportedOutcome(t))
	}
}

func TestApplyConsoleChangeWhenTheRestartFails(t *testing.T) {
	s := newConsoleServer(t)
	s.runner.failOn["systemctl restart"] = errors.New("unit not found")
	s.answers(changeRequest(consoleChangeID, "new.example.com"))
	before := s.untouched(t)
	s.sync(t)
	if !sameSnapshot(before, s.untouched(t)) {
		t.Error("the files weren't put back")
	}
	if o := s.outcome(t); o == nil || o.State != ConsoleFailed {
		t.Errorf("outcome = %+v, want failed", o)
	}
}

func TestApplyConsoleChangeOnceAndThenAnother(t *testing.T) {
	s := newConsoleServer(t)
	s.answers(changeRequest(consoleChangeID, "new.example.com"))
	s.sync(t)
	s.sync(t)
	s.sync(t)
	if got := s.restarts(); got != 1 {
		t.Errorf("one ID was applied %d times", got)
	}
	// A new change is a new ID: it is applied, over the first.
	s.answers(changeRequest(consoleChangeID2, "third.example.com"))
	s.sync(t)
	if got := s.restarts(); got != 2 {
		t.Errorf("restarts = %d after a second change, want 2", got)
	}
	if !strings.Contains(s.read(t, "etc/caravana/caravana.env"), "CARAVANA_PUBLIC_URL=https://third.example.com\n") {
		t.Errorf("caravana.env = %q", s.read(t, "etc/caravana/caravana.env"))
	}
	if o := s.outcome(t); o.ID != consoleChangeID2 || o.State != ConsoleApplied {
		t.Errorf("outcome = %+v", o)
	}
}

func TestApplyConsoleChangeNeedsTheStateWritten(t *testing.T) {
	s := newConsoleServer(t)
	// state.json can't be replaced: a folder where the file is.
	if err := os.Remove(s.file("etc/caravana-node/state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.file("etc/caravana-node/state.json/x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := s.applyConsole(ctx(), NodeEnv{URL: localConsoleURL}, State{}, &ConsoleRequest{ID: consoleChangeID, Domain: "new.example.com"})
	if got.ConsoleAddress != nil {
		t.Errorf("an outcome was returned: %+v", got.ConsoleAddress)
	}
	if len(s.runner.commands()) != 0 || s.checks != 0 {
		t.Errorf("something ran with no way to record it: %v", s.runner.commands())
	}
	if s.read(t, "etc/caravana/caravana.env") != oldEnvFile || s.read(t, "var/lib/caravana-host/install.env") != oldInstall {
		t.Error("a file changed with no way to record it")
	}
}

// A failed change never keeps the other steps of the sync from running.
func TestConsoleChangeDoesNotBlockTheOtherSteps(t *testing.T) {
	s := newConsoleServer(t)
	s.healthy = false
	d := newFakeDocker(t)
	withDocker(s.testAgent, d)
	s.console.on("/v1/node/sync", s.syncAnswer(func(m map[string]any) {
		m["consoleAddress"] = changeRequest(consoleChangeID, "new.example.com")
		m["clean"] = map[string]any{"id": cleanIDA, "keep": []string{}, "dockerVolumes": false}
	}))
	s.sync(t)
	if o := s.outcome(t); o == nil || o.State != ConsoleFailed {
		t.Fatalf("outcome = %+v", o)
	}
	if len(d.changing()) == 0 {
		t.Error("the clean-up didn't run after a console change that failed")
	}
	// The clean-up's save didn't lose the console's outcome.
	if st, _ := s.LoadState(); st.LastClean == nil || st.ConsoleAddress == nil {
		t.Errorf("state lost something: clean %+v, console %+v", st.LastClean, st.ConsoleAddress)
	}
}

func TestInterruptedConsoleChange(t *testing.T) {
	running := func(t *testing.T, s *consoleServer, domain string) {
		t.Helper()
		st, err := s.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		st.ConsoleAddress = &ConsoleOutcome{ID: consoleChangeID, State: ConsoleRunning, Domain: domain}
		if err := s.SaveState(st); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the env file was changed: the rest is finished", func(t *testing.T) {
		s := newConsoleServer(t)
		s.writeFiles(t, map[string]string{"etc/caravana/caravana.env": strings.Replace(oldEnvFile, "old.example.com", "new.example.com", 1)})
		running(t, s, "new.example.com")
		s.answers(changeRequest(consoleChangeID, "new.example.com")) // the console still asks: not applied twice
		s.sync(t)
		if got := s.read(t, "var/lib/caravana-host/install.env"); !strings.Contains(got, "CARAVANA_DOMAIN=new.example.com\n") {
			t.Errorf("install.env = %q, want the domain finished", got)
		}
		if got := s.restarts(); got != 1 {
			t.Errorf("restarts = %d, want 1 (to be sure Caravana runs under the new address)", got)
		}
		if o := s.outcome(t); o == nil || o.State != ConsoleApplied || o.ID != consoleChangeID {
			t.Errorf("outcome = %+v, want applied", o)
		}
	})

	t.Run("the env file was changed and Caravana doesn't answer", func(t *testing.T) {
		s := newConsoleServer(t)
		s.healthy = false
		s.writeFiles(t, map[string]string{"etc/caravana/caravana.env": strings.Replace(oldEnvFile, "old.example.com", "new.example.com", 1)})
		running(t, s, "new.example.com")
		s.answers(nil)
		s.sync(t)
		if o := s.outcome(t); o == nil || o.State != ConsoleFailed || !strings.Contains(o.Reason, "caravana.env") {
			t.Errorf("outcome = %+v, want failed pointing at the env file", o)
		}
	})

	t.Run("nothing was changed yet", func(t *testing.T) {
		s := newConsoleServer(t)
		running(t, s, "new.example.com")
		s.answers(changeRequest(consoleChangeID, "new.example.com"))
		before := s.untouched(t)
		s.sync(t)
		if !sameSnapshot(before, s.untouched(t)) || len(s.runner.commands()) != 0 {
			t.Error("something changed")
		}
		if o := s.outcome(t); o == nil || o.State != ConsoleFailed || o.Reason != "The agent stopped during the change." {
			t.Errorf("outcome = %+v", o)
		}
		if s.restarts() != 0 {
			t.Error("the failed request was applied again")
		}
	})

	t.Run("an old state.json with a running outcome and no domain", func(t *testing.T) {
		s := newConsoleServer(t)
		running(t, s, "")
		s.answers(nil)
		s.sync(t)
		if o := s.outcome(t); o == nil || o.State != ConsoleFailed {
			t.Errorf("outcome = %+v", o)
		}
	})
}

func TestStateValidatesTheConsoleOutcome(t *testing.T) {
	for name, o := range map[string]*ConsoleOutcome{
		"a bad ID":     {ID: "abc", State: ConsoleApplied},
		"a bad state":  {ID: consoleChangeID, State: "done"},
		"a bad domain": {ID: consoleChangeID, State: ConsoleApplied, Domain: "a b.test"},
	} {
		if err := o.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, o := range []*ConsoleOutcome{nil, {ID: consoleChangeID, State: ConsoleRunning, Domain: "new.example.com"}, {ID: consoleChangeID, State: ConsoleRefused}} {
		if err := o.validate(); err != nil {
			t.Errorf("%+v refused: %v", o, err)
		}
	}
}

func TestReplaceLines(t *testing.T) {
	got, n := replaceLines([]byte("A=1\nCARAVANA_DOMAIN=x\nCARAVANA_DOMAIN_OTHER=1\nCARAVANA_DOMAIN=y"), "CARAVANA_DOMAIN=", "z.test")
	if n != 2 || string(got) != "A=1\nCARAVANA_DOMAIN=z.test\nCARAVANA_DOMAIN_OTHER=1\nCARAVANA_DOMAIN=z.test" {
		t.Errorf("replaceLines = %q, %d", got, n)
	}
	if got, n := replaceLines(nil, "K=", "v"); n != 0 || len(got) != 0 {
		t.Errorf("empty = %q, %d", got, n)
	}
}

// The real health check goes to the console's loopback listener.
func TestConsoleHealthCheckAsksTheLoopbackListener(t *testing.T) {
	s := newConsoleServer(t)
	s.Health = nil
	hits := 0
	s.console.on("/v1/health", func(map[string]any, string) (int, any) { hits++; return 200, `{"status":"ok"}` })
	s.answers(changeRequest(consoleChangeID, "new.example.com"))
	s.sync(t)
	if hits != 1 {
		t.Errorf("the health check was asked %d times, want 1", hits)
	}
	if o := s.outcome(t); o == nil || o.State != ConsoleApplied {
		t.Errorf("outcome = %+v", o)
	}

	// 503 until the 60th look, then 200: within the minute is applied; never is failed.
	s2 := newConsoleServer(t)
	s2.Health = nil
	n := 0
	s2.console.on("/v1/health", func(map[string]any, string) (int, any) {
		n++
		if n < 60 {
			return 503, nil
		}
		return 200, `{"status":"ok"}`
	})
	s2.answers(changeRequest(consoleChangeID, "new.example.com"))
	s2.sync(t)
	if o := s2.outcome(t); o == nil || o.State != ConsoleApplied || n != 60 {
		t.Errorf("outcome %+v after %d looks, want applied at the 60th", o, n)
	}
}

// ---- joined servers follow -------------------------------------------------------------------------

// followFixture is a joined server whose console is the fake one, and a second console, the new
// address, that serves https.
type followFixture struct {
	*testAgent
	newConsole *httptest.Server
	health     int // what the new address answers
	asked      int
}

func newFollowFixture(t *testing.T, insecure bool) *followFixture {
	t.Helper()
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	f := &followFixture{testAgent: a, health: 200}
	f.newConsole = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.asked++
		if r.URL.Path != "/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(f.health)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(f.newConsole.Close)
	a.HTTP = NewHTTPClient(insecure)
	// node.env records the insecure setting: followed unchanged.
	if err := a.SaveNodeEnv(NodeEnv{URL: c.srv.URL, Token: sentinelNodeToken, Insecure: insecure}); err != nil {
		t.Fatal(err)
	}
	return f
}

// url is the new console's address as the answer names it: a host name, since an IP isn't one.
func (f *followFixture) url() string {
	_, port, _ := net.SplitHostPort(f.newConsole.Listener.Addr().String())
	return "https://localhost:" + port
}

func (f *followFixture) answer(consoleURL string) {
	f.console.on("/v1/node/sync", f.syncAnswer(func(m map[string]any) { m["consoleURL"] = consoleURL }))
}

func TestJoinedServerFollowsTheConsole(t *testing.T) {
	t.Run("a new address that answers", func(t *testing.T) {
		f := newFollowFixture(t, true)
		f.answer(f.url())
		before := f.snapshot(t)
		if err := f.Sync(ctx(), SyncOptions{}); err != nil {
			t.Fatal(err)
		}
		env, _, _ := f.LoadNodeEnv()
		if env.URL != f.url() || env.Token != sentinelNodeToken || !env.Insecure {
			t.Errorf("node.env = %+v, want the new URL with the token and insecure as they were", env)
		}
		if m := f.mode(t, "etc/caravana-node/node.env"); m != 0o600 {
			t.Errorf("node.env mode = %v", m)
		}
		if f.asked != 1 {
			t.Errorf("the new address was asked %d times", f.asked)
		}
		if !strings.Contains(f.stdout.String(), f.url()) {
			t.Errorf("stdout = %q, want one line naming the new address", f.stdout.String())
		}
		after := f.snapshot(t)
		delete(before, "/etc/caravana-node/node.env")
		delete(after, "/etc/caravana-node/node.env")
		delete(before, "/etc/caravana-node/state.json")
		delete(after, "/etc/caravana-node/state.json")
		if !sameSnapshot(before, after) {
			t.Error("another file changed")
		}
	})

	t.Run("a new address that doesn't answer yet", func(t *testing.T) {
		f := newFollowFixture(t, true)
		f.health = 502
		f.answer(f.url())
		if err := f.Sync(ctx(), SyncOptions{}); err != nil {
			t.Fatal(err)
		}
		if env, _, _ := f.LoadNodeEnv(); env.URL != f.console.srv.URL {
			t.Errorf("node.env URL = %q, want it kept", env.URL)
		}
		if !strings.Contains(f.stderr.String(), "doesn't answer yet") {
			t.Errorf("stderr = %q", f.stderr.String())
		}
	})

	t.Run("a new address that can't be reached", func(t *testing.T) {
		f := newFollowFixture(t, true)
		url := f.url()
		f.newConsole.Close()
		f.answer(url)
		if err := f.Sync(ctx(), SyncOptions{}); err != nil {
			t.Fatal(err)
		}
		if env, _, _ := f.LoadNodeEnv(); env.URL != f.console.srv.URL {
			t.Errorf("node.env URL = %q, want it kept", env.URL)
		}
	})

	t.Run("the agent's TLS setting is honoured", func(t *testing.T) {
		f := newFollowFixture(t, false) // verifies certificates: the test server's isn't trusted
		f.answer(f.url())
		if err := f.Sync(ctx(), SyncOptions{}); err != nil {
			t.Fatal(err)
		}
		if env, _, _ := f.LoadNodeEnv(); env.URL != f.console.srv.URL || env.Insecure {
			t.Errorf("node.env = %+v, want it kept", env)
		}
	})

	t.Run("an http URL is refused whole, even the console's own", func(t *testing.T) {
		f := newFollowFixture(t, true)
		f.answer(f.console.srv.URL + "/")
		if err := f.Sync(ctx(), SyncOptions{}); err == nil {
			t.Error("an http consoleURL was accepted")
		}
		if f.asked != 0 {
			t.Error("the new address was asked")
		}
		if env, _, _ := f.LoadNodeEnv(); env.URL != f.console.srv.URL {
			t.Errorf("node.env URL = %q", env.URL)
		}
	})

	t.Run("the server that runs the console never follows", func(t *testing.T) {
		s := newConsoleServer(t)
		f := &followFixture{testAgent: s.testAgent, health: 200}
		f.newConsole = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.asked++ }))
		t.Cleanup(f.newConsole.Close)
		s.console.on("/v1/node/sync", s.syncAnswer(func(m map[string]any) { m["consoleURL"] = f.url() }))
		before := s.untouched(t)
		s.sync(t)
		if f.asked != 0 || !sameSnapshot(before, s.untouched(t)) {
			t.Errorf("a loopback server followed: asked %d", f.asked)
		}
		if env, _, _ := s.LoadNodeEnv(); env.URL != localConsoleURL {
			t.Errorf("node.env URL = %q", env.URL)
		}
	})

	t.Run("an answer with a URL that isn't an https host is refused whole", func(t *testing.T) {
		for _, bad := range []string{"http://console.example.com", "https://console.example.com/path", "https://u:p@console.example.com", "https://203.0.113.5",
			"console.example.com", "https://console.example.com\nx", "https://a b.example.com", "ftp://console.example.com"} {
			f := newFollowFixture(t, true)
			f.answer(bad)
			before := f.snapshot(t)
			if err := f.Sync(ctx(), SyncOptions{}); err == nil {
				t.Errorf("%q: accepted", bad)
			}
			if !sameSnapshot(before, f.snapshot(t)) {
				t.Errorf("%q: a file changed", bad)
			}
		}
	})

	t.Run("no URL in the answer: nothing happens", func(t *testing.T) {
		f := newFollowFixture(t, true)
		f.console.on("/v1/node/sync", f.syncAnswer(nil))
		before := f.snapshot(t)
		if err := f.Sync(ctx(), SyncOptions{}); err != nil {
			t.Fatal(err)
		}
		if !sameSnapshot(before, f.snapshot(t)) || f.asked != 0 {
			t.Error("something changed")
		}
	})
}

// A joined server never handles a console address request: it doesn't run the console.
func TestJoinedServerRefusesAConsoleRequest(t *testing.T) {
	f := newFollowFixture(t, true)
	f.console.on("/v1/node/sync", f.syncAnswer(func(m map[string]any) {
		m["consoleAddress"] = changeRequest(consoleChangeID, "new.example.com")
	}))
	if err := f.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := f.LoadState()
	if st.ConsoleAddress == nil || st.ConsoleAddress.State != ConsoleRefused {
		t.Errorf("outcome = %+v, want refused", st.ConsoleAddress)
	}
	if len(f.runner.commands()) != 0 {
		t.Errorf("programs ran: %v", f.runner.commands())
	}
}
