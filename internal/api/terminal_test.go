package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/httpx"
)

// termSentinel is typed in the tests that look for what a terminal must not
// keep; the fake shell echoes it back, as a real one does.
const termSentinel = "s3cret-sentinel-0206"

const terminalPath = "/v1/apps/whoami/copies/c1/terminal"

// wsURL is the websocket address of an http base URL and path.
func wsURL(base, path string) string { return "ws" + strings.TrimPrefix(base, "http") + path }

// termClient is a websocket client of the terminal. A goroutine reads every
// frame (gorilla can't read on after a timeout, so the tests wait on the
// client's state instead of reading with deadlines): output is kept as the
// screen, text frames and how the connection ended are kept for the test.
type termClient struct {
	t    *testing.T
	conn *websocket.Conn

	mu     sync.Mutex
	screen strings.Builder
	texts  []string
	err    error         // how the connection ended; nil while it is open
	ended  chan struct{} // closed when the reader stopped
}

func bearer(e *testEnv) http.Header { return http.Header{"Authorization": {"Bearer " + e.Token}} }

// newTermClient starts reading conn.
func newTermClient(t *testing.T, conn *websocket.Conn) *termClient {
	c := &termClient{t: t, conn: conn, ended: make(chan struct{})}
	go func() {
		defer close(c.ended)
		for {
			kind, data, err := conn.ReadMessage()
			c.mu.Lock()
			switch {
			case err != nil:
				c.err = err
			case kind == websocket.BinaryMessage:
				c.screen.Write(data)
			default:
				c.texts = append(c.texts, string(data))
			}
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return c
}

func (c *termClient) screenText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.screen.String()
}

// openTerm dials the terminal of a copy and fails the test when it can't.
func openTerm(t *testing.T, base, path string, header http.Header) *termClient {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(base, path), header)
	if err != nil {
		body := ""
		if resp != nil {
			raw, _ := io.ReadAll(resp.Body)
			body = resp.Status + " " + string(raw)
		}
		t.Fatalf("dial %s: %v %s", path, err, body)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("dial = %d, want 101", resp.StatusCode)
	}
	return newTermClient(t, conn)
}

// dialFails dials and expects the upgrade to be refused with a JSON error.
func dialFails(t *testing.T, base, path string, header http.Header) (int, errorBody) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(base, path), header)
	if err == nil {
		conn.Close()
		t.Fatalf("dial %s succeeded, want a refusal", path)
	}
	if resp == nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body errorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("dial %s refused with %d %q, not a JSON error", path, resp.StatusCode, raw)
	}
	return resp.StatusCode, body
}

func (c *termClient) input(s string) {
	c.t.Helper()
	if err := c.conn.WriteJSON(map[string]any{"type": "input", "data": s}); err != nil {
		c.t.Fatalf("send input: %v", err)
	}
}

func (c *termClient) resize(cols, rows int) {
	c.t.Helper()
	if err := c.conn.WriteJSON(map[string]any{"type": "resize", "cols": cols, "rows": rows}); err != nil {
		c.t.Fatalf("send resize: %v", err)
	}
}

// readUntil waits until the screen has want.
func (c *termClient) readUntil(want string) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(c.screenText(), want) {
		if time.Now().After(deadline) {
			c.t.Fatalf("the screen %q never showed %q", c.screenText(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitEnd waits for the connection to end and returns how.
func (c *termClient) waitEnd() error {
	c.t.Helper()
	select {
	case <-c.ended:
	case <-time.After(5 * time.Second):
		c.t.Fatalf("the connection did not end; the screen is %q", c.screenText())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// readExit waits for the exit message, which must be the only text frame and
// be followed by a normal close.
func (c *termClient) readExit() map[string]any {
	c.t.Helper()
	err := c.waitEnd()
	if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		c.t.Fatalf("the connection ended with %v, want a normal close", err)
	}
	c.mu.Lock()
	texts := append([]string(nil), c.texts...)
	c.mu.Unlock()
	if len(texts) != 1 {
		c.t.Fatalf("text frames = %q, want exactly the exit message", texts)
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(texts[0]), &msg); err != nil || msg["type"] != "exit" {
		c.t.Fatalf("text frame %q is not an exit message", texts[0])
	}
	return msg
}

// readClose waits for the connection to close and returns the close code.
func (c *termClient) readClose() int {
	c.t.Helper()
	err := c.waitEnd()
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		c.t.Fatalf("the connection did not end with a close frame: %v", err)
	}
	return ce.Code
}

// terminalServer serves the API with the terminal's timings changed.
func terminalServer(t *testing.T, e *testEnv, tweak func(*server)) string {
	t.Helper()
	s := newServer(Deps{Service: e.Service, Auth: e.Auth, Store: e.Store, Log: e.log})
	tweak(s)
	srv := httptest.NewServer(httpx.LogRequests(e.log, s.handler(s.routes())))
	t.Cleanup(srv.Close)
	return srv.URL
}

type actionRow struct {
	Action   string `json:"action"`
	Trigger  string `json:"trigger"`
	Status   string `json:"status"`
	CopyID   string `json:"copyId"`
	Service  string `json:"service"`
	Program  string `json:"program"`
	ExitCode *int   `json:"exitCode"`
	Error    string `json:"error"`
}

// terminalActions lists the app's terminal actions through the API, newest
// first, checking each against the contract.
func terminalActions(t *testing.T, e *testEnv) []actionRow {
	t.Helper()
	doc := loadContract(t)
	_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/actions", "", "")
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("actions = %s", raw)
	}
	var out []actionRow
	for _, item := range items {
		checkSchema(t, doc, item, "AppAction")
		var row actionRow
		if err := json.Unmarshal(item, &row); err != nil {
			t.Fatal(err)
		}
		if row.Action == "terminal" {
			out = append(out, row)
		}
	}
	return out
}

func waitTerminalAction(t *testing.T, e *testEnv, want func(actionRow) bool) actionRow {
	t.Helper()
	var last []actionRow
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		last = terminalActions(t, e)
		if len(last) > 0 && want(last[0]) {
			return last[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no terminal action as wanted; records = %+v", last)
	return actionRow{}
}

func TestTerminalSession(t *testing.T) {
	e := deployedWithCopies(t)

	c := openTerm(t, e.RawURL, terminalPath+"?shell=bash&cols=100&rows=30", bearer(e))
	c.input("echo $((6*7))\r")
	c.readUntil("42\r\n")

	c.input("stty size\r")
	c.readUntil("30 100\r\n")

	c.resize(120, 40)
	// The resize and the typing are sent by different goroutines in Nomad's
	// client, so ask until the new size shows.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(c.screenText(), "40 120\r\n") {
		if time.Now().After(deadline) {
			t.Fatalf("the screen %q never showed the new size", c.screenText())
		}
		c.input("stty size\r")
		time.Sleep(20 * time.Millisecond)
	}

	if got := e.Nomad.Execs(); len(got) != 1 || !got[0].TTY || got[0].Task != "web" || strings.Join(got[0].Command, "|") != "/bin/bash" {
		t.Errorf("exec = %+v, want /bin/bash with a terminal in the service's task", got)
	}

	rows := terminalActions(t, e)
	if len(rows) != 1 || rows[0].Status != "running" || rows[0].Program != "bash" || rows[0].CopyID != "c1" ||
		rows[0].Service != "web" || rows[0].Trigger != "manual" {
		t.Errorf("record while open = %+v", rows)
	}

	c.input("exit 3\r")
	msg := c.readExit()
	if msg["code"] != float64(3) || msg["reason"] != nil {
		t.Errorf("exit message = %v, want code 3", msg)
	}
	row := waitTerminalAction(t, e, func(a actionRow) bool { return a.Status != "running" })
	if row.Status != "succeeded" || row.ExitCode == nil || *row.ExitCode != 3 || row.Error != "" {
		t.Errorf("record after = %+v", row)
	}
}

func TestTerminalDefaultsToAutoAnd80x24(t *testing.T) {
	e := deployedWithCopies(t)
	c := openTerm(t, e.RawURL, terminalPath, bearer(e))
	c.input("stty size\r")
	c.readUntil("24 80\r\n")
	if got := e.Nomad.Execs(); len(got) != 1 || got[0].Command[0] != "/bin/sh" || got[0].Command[1] != "-c" {
		t.Errorf("exec = %+v, want the auto shell", got)
	}
}

func TestTerminalSessionCookie(t *testing.T) {
	e := deployedWithCopies(t)
	cookie := sessionCookie(t, e)
	host := strings.TrimPrefix(e.RawURL, "http://")
	header := func(origin string) http.Header {
		h := http.Header{"Cookie": {cookie.String()}}
		if origin != "" {
			h.Set("Origin", origin)
		}
		return h
	}

	t.Run("the console's own origin", func(t *testing.T) {
		c := openTerm(t, e.RawURL, terminalPath, header("http://"+host))
		c.input("echo hi\r")
		c.readUntil("hi\r\n")
		row := waitTerminalAction(t, e, func(a actionRow) bool { return true })
		if row.Trigger != "console" {
			t.Errorf("trigger = %q, want console for a session", row.Trigger)
		}
	})

	for name, origin := range map[string]string{
		"another site":            "https://evil.example",
		"the same host, https":    "https://" + host,
		"the same host, a port":   "http://" + host + "0",
		"null":                    "null",
		"no origin at all":        "",
		"a host that contains it": "http://" + host + ".evil.example",
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			before := len(e.Nomad.Execs())
			status, body := dialFails(t, e.RawURL, terminalPath, header(origin))
			if status != http.StatusForbidden || body.Error.Code != "cross_origin" {
				t.Errorf("got %d %+v, want 403 cross_origin", status, body.Error)
			}
			if got := len(e.Nomad.Execs()); got != before {
				t.Error("a shell was opened for a request that was refused")
			}
		})
	}

	t.Run("a token needs no origin, and a cookie sent with it doesn't change that", func(t *testing.T) {
		h := bearer(e)
		h.Set("Origin", "https://evil.example")
		openTerm(t, e.RawURL, terminalPath, h)
	})

	t.Run("no credentials", func(t *testing.T) {
		status, body := dialFails(t, e.RawURL, terminalPath, http.Header{"Origin": {"http://" + host}})
		if status != http.StatusUnauthorized || body.Error.Code != "unauthorized" {
			t.Errorf("got %d %+v, want 401", status, body.Error)
		}
	})
}

// Behind Traefik over HTTPS (the installed host), the upgrade arrives from the
// trusted proxy with X-Forwarded-Proto "wss" and the browser sends the
// __Host- cookie and an https Origin. It used to read as plain HTTP: the
// cookie wasn't found (401) and the Origin wouldn't have matched.
func TestTerminalBehindTraefikOverHTTPS(t *testing.T) {
	e := deployedWithCopies(t)
	proxies, err := httpx.ParseProxies("127.0.0.1,::1")
	if err != nil {
		t.Fatal(err)
	}
	base := terminalServer(t, e, func(s *server) { s.proxies = proxies })
	host := strings.TrimPrefix(base, "http://")
	session := sessionCookie(t, e)
	header := func(proto, cookieName, origin string) http.Header {
		return http.Header{
			"X-Forwarded-Proto": {proto},
			"Cookie":            {(&http.Cookie{Name: cookieName, Value: session.Value}).String()},
			"Origin":            {origin},
		}
	}

	c := openTerm(t, base, terminalPath, header("wss", "__Host-caravana_session", "https://"+host))
	c.input("echo $((6*7))\r")
	c.readUntil("42\r\n")

	// Over plain ws the console's cookie has no prefix, and an https Origin is
	// another site's.
	status, body := dialFails(t, base, terminalPath, header("ws", "caravana_session", "https://"+host))
	if status != http.StatusForbidden || body.Error.Code != "cross_origin" {
		t.Errorf("ws with an https origin = %d %+v, want 403 cross_origin", status, body.Error)
	}
}

func TestTerminalDeployTokenIsRefused(t *testing.T) {
	e := deployedWithCopies(t)
	auth := scopedToken(t, e, "default", "whoami") // the very app it may deploy
	status, body := dialFails(t, e.RawURL, terminalPath, http.Header{"Authorization": {auth}})
	if status != http.StatusForbidden || body.Error.Code != "token_scope" {
		t.Errorf("got %d %+v, want 403 token_scope", status, body.Error)
	}
	if len(e.Nomad.Execs()) != 0 {
		t.Error("a shell was opened for a deploy token")
	}
}

func TestTerminalRefusals(t *testing.T) {
	e := deployedWithCopies(t)

	t.Run("bad query", func(t *testing.T) {
		tests := []struct{ query, field string }{
			{"?shell=zsh", "shell"},
			{"?shell=", "shell"},
			{"?shell=/bin/bash", "shell"},
			{"?cols=0", "cols"},
			{"?cols=1001", "cols"},
			{"?cols=abc", "cols"},
			{"?cols=", "cols"},
			{"?rows=0", "rows"},
			{"?rows=1001", "rows"},
			{"?rows=-5", "rows"},
			{"?rows=2.5", "rows"},
		}
		for _, tt := range tests {
			status, body := dialFails(t, e.RawURL, terminalPath+tt.query, bearer(e))
			if status != http.StatusUnprocessableEntity || body.Error.Code != "invalid_field" || body.Error.Field != tt.field {
				t.Errorf("%s: got %d %+v, want 422 invalid_field on %s", tt.query, status, body.Error, tt.field)
			}
		}
		if len(e.Nomad.Execs()) != 0 {
			t.Error("a shell was opened for a bad query")
		}
	})

	t.Run("the extremes are fine", func(t *testing.T) {
		openTerm(t, e.RawURL, terminalPath+"?cols=1&rows=1000", bearer(e))
	})

	t.Run("unknown app and copy", func(t *testing.T) {
		for _, path := range []string{"/v1/apps/ghost/copies/c1/terminal", "/v1/apps/whoami/copies/other/terminal"} {
			status, body := dialFails(t, e.RawURL, path, bearer(e))
			if status != http.StatusNotFound || body.Error.Code != "not_found" {
				t.Errorf("%s: got %d %+v, want 404", path, status, body.Error)
			}
		}
	})

	t.Run("not a websocket request", func(t *testing.T) {
		status, _, raw := callWith(t, "Bearer "+e.Token, http.MethodGet, e.RawURL+terminalPath, "", "")
		var body errorBody
		if err := json.Unmarshal(raw, &body); err != nil || status != http.StatusBadRequest || body.Error.Code != "bad_request" {
			t.Errorf("got %d %s, want 400 bad_request", status, raw)
		}
	})

	t.Run("a copy that isn't running", func(t *testing.T) {
		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "complete", JobVersion: 0})
		status, body := dialFails(t, e.RawURL, terminalPath, bearer(e))
		if status != http.StatusConflict || body.Error.Code != "copy_not_running" {
			t.Errorf("got %d %+v, want 409 copy_not_running", status, body.Error)
		}
	})

	t.Run("a stopped app", func(t *testing.T) {
		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/stop", "", ""); status != http.StatusOK {
			t.Fatalf("stop = %d %s", status, raw)
		}
		status, body := dialFails(t, e.RawURL, terminalPath, bearer(e))
		if status != http.StatusConflict || body.Error.Code != "app_stopped" {
			t.Errorf("got %d %+v, want 409 app_stopped", status, body.Error)
		}
	})
}

func TestTerminalAtMostFiveOpen(t *testing.T) {
	e := deployedWithCopies(t)
	var open []*termClient
	for i := 0; i < 5; i++ {
		open = append(open, openTerm(t, e.RawURL, terminalPath, bearer(e)))
	}
	status, body := dialFails(t, e.RawURL, terminalPath, bearer(e))
	if status != http.StatusTooManyRequests || body.Error.Code != "too_many_terminals" {
		t.Fatalf("the sixth: got %d %+v, want 429 too_many_terminals", status, body.Error)
	}

	// Closing one makes room.
	open[0].conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(e.RawURL, terminalPath), bearer(e))
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no room after one was closed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTerminalIdleAndLimit(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		e := deployedWithCopies(t)
		e.Service.SetTerminalLimits(0, 150*time.Millisecond, 0)
		c := openTerm(t, e.RawURL, terminalPath, bearer(e))
		msg := c.readExit()
		if msg["reason"] != "idle" || msg["code"] != nil || msg["message"] != "idle for 150ms" {
			t.Errorf("exit message = %v, want reason idle", msg)
		}
		row := waitTerminalAction(t, e, func(a actionRow) bool { return a.Status != "running" })
		if row.Status != "failed" || row.Error != "idle for 150ms" {
			t.Errorf("record = %+v", row)
		}
	})
	t.Run("limit", func(t *testing.T) {
		e := deployedWithCopies(t)
		e.Service.SetTerminalLimits(0, time.Hour, 150*time.Millisecond)
		c := openTerm(t, e.RawURL, terminalPath, bearer(e))
		if msg := c.readExit(); msg["reason"] != "limit" || msg["message"] != "open for 150ms" {
			t.Errorf("exit message = %v, want reason limit", msg)
		}
	})
}

func TestTerminalNoShellInTheImage(t *testing.T) {
	e := deployedWithCopies(t)
	e.Nomad.OnExec(func(r deploytest.ExecRequest) deploytest.ExecReply {
		return deploytest.ExecReply{Fail: `exec: "/bin/bash": no such file`}
	})
	c := openTerm(t, e.RawURL, terminalPath+"?shell=bash", bearer(e))
	msg := c.readExit()
	if msg["reason"] != "error" || msg["message"] != "no shell found in this image" {
		t.Errorf("exit message = %v", msg)
	}
}

func TestTerminalProtocolViolations(t *testing.T) {
	e := deployedWithCopies(t)
	tests := []struct {
		name string
		send func(c *websocket.Conn) error
	}{
		{"a binary frame", func(c *websocket.Conn) error { return c.WriteMessage(websocket.BinaryMessage, []byte("ls\n")) }},
		{"not JSON", func(c *websocket.Conn) error { return c.WriteMessage(websocket.TextMessage, []byte("ls")) }},
		{"another type", func(c *websocket.Conn) error { return c.WriteJSON(map[string]any{"type": "hello"}) }},
		{"no type", func(c *websocket.Conn) error { return c.WriteJSON(map[string]any{"data": "ls"}) }},
		{"a resize with no size", func(c *websocket.Conn) error { return c.WriteJSON(map[string]any{"type": "resize"}) }},
		{"a resize too wide", func(c *websocket.Conn) error {
			return c.WriteJSON(map[string]any{"type": "resize", "cols": 1001, "rows": 10})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := openTerm(t, e.RawURL, terminalPath, bearer(e))
			if err := tt.send(c.conn); err != nil {
				t.Fatal(err)
			}
			if code := c.readClose(); code != websocket.CloseUnsupportedData {
				t.Errorf("close code = %d, want 1003", code)
			}
		})
	}

	t.Run("a frame over 64 KiB", func(t *testing.T) {
		c := openTerm(t, e.RawURL, terminalPath, bearer(e))
		big := strings.Repeat("x", 70<<10)
		_ = c.conn.WriteJSON(map[string]any{"type": "input", "data": big})
		if code := c.readClose(); code != websocket.CloseMessageTooBig {
			t.Errorf("close code = %d, want 1009", code)
		}
	})
}

func TestTerminalLostConnection(t *testing.T) {
	e := deployedWithCopies(t)
	url := terminalServer(t, e, func(s *server) { s.pingEvery, s.pongWait = 20*time.Millisecond, 150*time.Millisecond })
	// A client that never reads never answers a ping.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(url, terminalPath), bearer(e))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	row := waitTerminalAction(t, e, func(a actionRow) bool { return a.Status != "running" })
	if row.Status != "failed" || row.Error != "connection lost" || row.ExitCode != nil {
		t.Errorf("record = %+v, want failed, connection lost", row)
	}
}

func TestTerminalClosedByTheUser(t *testing.T) {
	e := deployedWithCopies(t)
	c := openTerm(t, e.RawURL, terminalPath, bearer(e))
	c.input("echo hi\r")
	c.readUntil("hi\r\n")
	if err := c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	row := waitTerminalAction(t, e, func(a actionRow) bool { return a.Status != "running" })
	if row.Status != "succeeded" || row.Error != "closed" {
		t.Errorf("record = %+v, want succeeded, closed", row)
	}
}

func TestTerminalEndsWhenTheTokenIsRevoked(t *testing.T) {
	e := deployedWithCopies(t)
	url := terminalServer(t, e, func(s *server) { s.recheck = 30 * time.Millisecond })
	made, err := e.Auth.CreateAPIToken(context.Background(), "terminal", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := openTerm(t, url, terminalPath, http.Header{"Authorization": {"Bearer " + made.Token}})
	c.input("echo up\r")
	c.readUntil("up\r\n")
	if err := e.Auth.RevokeAPIToken(context.Background(), made.Info.ID); err != nil {
		t.Fatal(err)
	}
	msg := c.readExit()
	if msg["reason"] != "error" || msg["message"] != "your sign-in ended" {
		t.Errorf("exit message = %v", msg)
	}
}

// What was typed and printed is in no log line and no answer of the API; the
// request log has the path, a 101 and nothing of the query's or the frames'.
func TestTerminalKeepsNoInputOrOutput(t *testing.T) {
	e := deployedWithCopies(t)
	c := openTerm(t, e.RawURL, terminalPath, bearer(e))
	c.input("echo " + termSentinel + "\r")
	c.readUntil(termSentinel + "\r\n")
	c.input("exit\r")
	c.readExit()
	waitTerminalAction(t, e, func(a actionRow) bool { return a.Status != "running" })

	var seen []string
	for _, p := range []string{"/v1/apps/whoami/actions", "/v1/apps/whoami", "/v1/apps", "/v1/apps/whoami/copies", "/v1/attention"} {
		_, _, raw := call(t, http.MethodGet, e.URL+p, "", "")
		seen = append(seen, string(raw))
	}
	seen = append(seen, e.Logs.String())
	for i, text := range seen {
		if strings.Contains(text, termSentinel) {
			t.Errorf("answer or log %d holds what was typed", i)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(e.Logs.String(), `"path":"`+terminalPath+`"`) {
		if time.Now().After(deadline) {
			t.Fatal("no request log line for the terminal")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var line map[string]any
	for _, l := range strings.Split(e.Logs.String(), "\n") {
		if strings.Contains(l, `"path":"`+terminalPath+`"`) {
			if err := json.Unmarshal([]byte(l), &line); err != nil {
				t.Fatal(err)
			}
		}
	}
	if line["status"] != float64(101) {
		t.Errorf("request log line = %v, want status 101", line)
	}
}
