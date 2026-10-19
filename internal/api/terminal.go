package api

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The terminal (feature 020.6): GET /v1/apps/{id}/copies/{copyId}/terminal
// upgrades to a websocket and bridges it to a shell in the copy
// (specs/020.6-terminal/contracts/terminal.md). Everything goes through
// service.OpenTerminal.
//
// A terminal is root-equivalent access to the app's container, so who may open
// one is narrow: a session cookie whose Origin is the console's own, or a full
// API token. A deploy token never reaches this file (the scope check refuses
// every route it doesn't list). What is typed and printed passes through here
// and is never logged, stored or put in an error; the request log has the
// method, path and status (101) only.

// The timings of a terminal's connection, and the largest frame the browser may
// send.
const (
	defaultPingEvery = 30 * time.Second // Caravana pings this often
	defaultPongWait  = 60 * time.Second // no pong, and no input, for this long ends the session
	terminalWrite    = 10 * time.Second // how long one write to the browser may take
	terminalMaxFrame = 64 << 10
	// terminalSettle is how long a closing session may take to be recorded
	// before the connection is dropped anyway.
	terminalSettle = 5 * time.Second
)

const codeTooManyTerminals = "too_many_terminals"

// terminalUpgrader turns the request into a websocket. The Origin was checked
// before (checkTerminalOrigin), and only for cookies, so the library's own
// check, which wants it equal to the Host for every request, is off. Its
// failures answer in our error body.
var terminalUpgrader = websocket.Upgrader{
	ReadBufferSize:  4 << 10,
	WriteBufferSize: 4 << 10,
	CheckOrigin:     func(*http.Request) bool { return true },
	Error: func(w http.ResponseWriter, _ *http.Request, status int, reason error) {
		code := codeBadRequest
		if status >= 500 {
			code = codeInternal
		}
		writeError(w, &Error{Status: status, Code: code, Message: "could not open the terminal: " + reason.Error()})
	},
}

// terminalInput is a frame from the browser: {"type":"input","data":"…"} or
// {"type":"resize","cols":N,"rows":M}.
type terminalInput struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// terminalExit is the last frame to the browser.
type terminalExit struct {
	Type    string `json:"type"`
	Code    *int   `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// openTerminal answers GET /v1/apps/{id}/copies/{copyId}/terminal: errors are
// the usual JSON ones until the upgrade; after it, the protocol of the
// contract, and the handler returns nothing (the response is the connection).
func (s *server) openTerminal(w http.ResponseWriter, r *http.Request) error {
	// Before anything else, so no answer of this operation can be stored.
	w.Header().Set("Cache-Control", "no-store")
	if err := s.checkTerminalOrigin(r); err != nil {
		return err
	}
	if !websocket.IsWebSocketUpgrade(r) {
		return &Error{Status: http.StatusBadRequest, Code: codeBadRequest,
			Message: "this endpoint speaks websocket: send a request with Upgrade: websocket"}
	}
	shell, size, err := terminalQuery(r)
	if err != nil {
		return err
	}
	trigger := app.TriggerManual
	if len(r.Header.Values("Authorization")) == 0 {
		trigger = app.TriggerConsole // a session cookie: the browser console
	}
	sess, err := s.svc.OpenTerminal(r.Context(), r.PathValue("id"), r.PathValue("copyId"), shell, size, trigger)
	if errors.Is(err, service.ErrTooManyTerminals) {
		return &Error{Status: http.StatusTooManyRequests, Code: codeTooManyTerminals, Message: err.Error()}
	}
	if err != nil {
		return err
	}

	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrader answered the client. The shell is already open: end it.
		sess.Close(service.EndGone)
		return nil
	}
	defer conn.Close()
	// The server set read and write deadlines for the request before this
	// handler ran; a terminal lives as long as its session says.
	_ = conn.NetConn().SetDeadline(time.Time{})

	pongWait := orDefault(s.pongWait, defaultPongWait)
	go readTerminal(conn, sess, pongWait)
	s.writeTerminal(r, conn, sess)
	return nil
}

// checkTerminalOrigin refuses a session cookie that doesn't come from the
// console itself. Browsers send no cookie-bound protection for websockets
// (same-origin policy doesn't apply, and http.CrossOriginProtection doesn't
// check GET), so the Origin header must equal the console's own: the scheme as
// the request arrived (after trusted proxies) and its Host. A missing Origin is
// refused too, since browsers always send it. A request with an Authorization
// header is a token's (authenticate has checked it) and no browser sends one
// across sites.
func (s *server) checkTerminalOrigin(r *http.Request) error {
	if len(r.Header.Values("Authorization")) > 0 {
		return nil
	}
	scheme := "http"
	if s.proxies.HTTPS(r) {
		scheme = "https"
	}
	if origins := r.Header.Values("Origin"); len(origins) == 1 && strings.EqualFold(origins[0], scheme+"://"+r.Host) {
		return nil
	}
	return &Error{Status: http.StatusForbidden, Code: codeCrossOrigin,
		Message: "this terminal was asked for by another site, so it was refused; nothing was opened"}
}

// terminalQuery reads shell, cols and rows. Each problem is 422 invalid_field
// naming the parameter.
func terminalQuery(r *http.Request) (string, service.TerminalSize, error) {
	q := r.URL.Query()
	shell := "auto"
	if q.Has("shell") {
		shell = q.Get("shell")
	}
	switch shell {
	case service.ShellAuto, service.ShellBash, service.ShellSh:
	default:
		return "", service.TerminalSize{}, invalidQuery("shell", "must be auto, bash or sh")
	}
	side := func(name string, def int) (int, error) {
		if !q.Has(name) {
			return def, nil
		}
		n, err := strconv.Atoi(q.Get(name))
		if err != nil || n < 1 || n > 1000 {
			return 0, invalidQuery(name, "must be a whole number from 1 to 1000")
		}
		return n, nil
	}
	cols, err := side("cols", 80)
	if err != nil {
		return "", service.TerminalSize{}, err
	}
	rows, err := side("rows", 24)
	if err != nil {
		return "", service.TerminalSize{}, err
	}
	return shell, service.TerminalSize{Cols: cols, Rows: rows}, nil
}

// invalidQuery is the 422 for a query parameter that breaks its rule.
func invalidQuery(field, message string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: field, Message: message}
}

// readTerminal passes the browser's frames on to the session until the
// connection ends, and tells the session why it ended: the user closed it, or
// the connection was lost. It is the only reader of conn.
func readTerminal(conn *websocket.Conn, sess *service.TerminalSession, pongWait time.Duration) {
	conn.SetReadLimit(terminalMaxFrame)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	// A pong, or any frame, shows the browser is there.
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })
	refuse := func(why string) {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseUnsupportedData, why), time.Now().Add(terminalWrite))
		sess.Close(service.EndClosed)
	}
	for {
		kind, frame, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				sess.Close(service.EndClosed)
			} else {
				sess.Close(service.EndGone)
			}
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		if kind != websocket.TextMessage {
			refuse("send text frames")
			return
		}
		var in terminalInput
		if err := json.Unmarshal(frame, &in); err != nil {
			refuse("not a message of this protocol")
			return
		}
		switch in.Type {
		case "input":
			if _, err := sess.Write([]byte(in.Data)); err != nil {
				return // the session has ended; the writer says so
			}
		case "resize":
			if in.Cols < 1 || in.Cols > 1000 || in.Rows < 1 || in.Rows > 1000 {
				refuse("cols and rows must be from 1 to 1000")
				return
			}
			sess.Resize(in.Cols, in.Rows)
		default:
			refuse("unknown message type")
			return
		}
	}
}

// writeTerminal sends the shell's output to the browser as binary frames, pings
// it, and when the session ends sends the exit message and closes. It is the
// only writer of conn apart from the close frames, which gorilla allows from
// anywhere. It returns when the session has ended.
func (s *server) writeTerminal(r *http.Request, conn *websocket.Conn, sess *service.TerminalSession) {
	ping := time.NewTicker(orDefault(s.pingEvery, defaultPingEvery))
	defer ping.Stop()
	recheck := time.NewTicker(orDefault(s.recheck, defaultRecheck))
	defer recheck.Stop()
	stillValid := s.credentialCheck(r)
	signedOut := false

	// lost ends the session because the browser can't be written to, waits for
	// the record to settle and drops the connection.
	lost := func() {
		sess.Close(service.EndGone)
		select {
		case <-sess.Done():
		case <-time.After(terminalSettle):
		}
	}
	send := func(kind int, data []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(terminalWrite))
		return conn.WriteMessage(kind, data) == nil
	}
	for {
		select {
		case chunk := <-sess.Output():
			if !send(websocket.BinaryMessage, chunk) {
				lost()
				return
			}
		case <-ping.C:
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(terminalWrite)) != nil {
				lost()
				return
			}
		case <-recheck.C:
			// A revoked token or an ended session ends the terminal too.
			if err := stillValid(context.WithoutCancel(r.Context())); err != nil && !signedOut {
				signedOut = true
				sess.Close(service.EndClosed)
			}
		case <-sess.Done():
			// What the shell printed last is still waiting; it goes first.
			for drained := false; !drained; {
				select {
				case chunk := <-sess.Output():
					if !send(websocket.BinaryMessage, chunk) {
						return
					}
				default:
					drained = true
				}
			}
			if msg, ok := exitMessage(sess.End(), signedOut); ok {
				frame, err := json.Marshal(msg)
				if err == nil {
					send(websocket.TextMessage, frame)
				}
			}
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(terminalWrite))
			return
		}
	}
}

// exitMessage is the last frame for a session's end, and false when the
// browser went away and there is nobody to tell. The shell exiting says its
// code (with a sentence when it never really started); idle, the time limit
// and failures say their reason in fixed words.
func exitMessage(end service.TerminalEnd, signedOut bool) (terminalExit, bool) {
	switch {
	case end.Code != nil:
		return terminalExit{Type: "exit", Code: end.Code, Message: end.Message}, true
	case end.Reason == service.EndIdle, end.Reason == service.EndLimit, end.Reason == service.EndError:
		return terminalExit{Type: "exit", Reason: end.Reason, Message: end.Message}, true
	case signedOut:
		return terminalExit{Type: "exit", Reason: service.EndError, Message: "your sign-in ended"}, true
	}
	return terminalExit{}, false
}
