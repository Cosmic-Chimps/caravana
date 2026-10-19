package deploytest

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/nomad/api"
)

// The fake interactive shell (feature 020.6) answers a terminal exec: it reads
// the client's stdin and terminal-size frames the way a Nomad server passes
// them on, echoes what is typed as a terminal does, and runs a few scripted
// commands at each line:
//
//	echo <text>      prints <text>; "echo $((6*7))" prints 42
//	stty size        prints the terminal's current "rows cols" (24 80 until resized)
//	exit [N]         ends with exit code N (0 without one)
//
// Anything else prints nothing. A "$ " prompt is printed first and after each
// line. Closing stdin ends the shell with exit code 0, as end of input does.

// runShell serves one terminal exec on conn until the shell exits or the client
// goes away. It is the only writer of conn, which gorilla requires.
func runShell(conn *websocket.Conn) {
	rows, cols := 24, 80
	write := func(s string) bool {
		return conn.WriteJSON(api.ExecStreamingOutput{
			Stdout: &api.ExecStreamingIOOperation{Data: []byte(s)}}) == nil
	}
	exit := func(code int) {
		_ = conn.WriteJSON(api.ExecStreamingOutput{Exited: true, Result: &api.ExecStreamingExitResult{ExitCode: code}})
		// Give the client the frame before the connection closes: wait until it
		// closes first, or a second passes.
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}

	if !write("$ ") {
		return
	}
	var line strings.Builder
	for {
		var in api.ExecStreamingInput
		if err := conn.ReadJSON(&in); err != nil {
			return
		}
		if in.TTYSize != nil {
			rows, cols = in.TTYSize.Height, in.TTYSize.Width
		}
		if in.Stdin == nil {
			continue // a heartbeat
		}
		for _, b := range in.Stdin.Data {
			if b != '\r' && b != '\n' {
				line.WriteByte(b)
				if !write(string(b)) {
					return
				}
				continue
			}
			if !write("\r\n") {
				return
			}
			text := strings.TrimSpace(line.String())
			line.Reset()
			out, code, done := runShellLine(text, rows, cols)
			if out != "" && !write(out) {
				return
			}
			if done {
				exit(code)
				return
			}
			if !write("$ ") {
				return
			}
		}
		if in.Stdin.Close {
			exit(0)
			return
		}
	}
}

// runShellLine is what one typed line does: the text to print, and whether the
// shell exits with code.
func runShellLine(text string, rows, cols int) (out string, code int, done bool) {
	switch {
	case text == "echo $((6*7))":
		return "42\r\n", 0, false
	case text == "echo" || strings.HasPrefix(text, "echo "):
		return strings.TrimPrefix(strings.TrimPrefix(text, "echo"), " ") + "\r\n", 0, false
	case text == "stty size":
		return fmt.Sprintf("%d %d\r\n", rows, cols), 0, false
	case text == "exit" || strings.HasPrefix(text, "exit "):
		n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(text, "exit")))
		return "", n, true
	}
	return "", 0, false
}
