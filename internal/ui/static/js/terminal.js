"use strict";

// caravana's script for the terminal page (feature 020.6). xterm.js (vendored
// beside this file) draws a terminal; this file connects it to the API's
// websocket, openTerminal, which bridges to a shell in one copy. The protocol
// is in specs/020.6-terminal/contracts/terminal.md:
//
//   browser -> caravana  text, JSON: {"type":"input","data":"..."}
//                                    {"type":"resize","cols":N,"rows":M}
//   caravana -> browser  binary: what the shell printed, as bytes
//                        text, JSON, once, last: {"type":"exit","code":N}
//                        or {"type":"exit","reason":"idle|limit|error","message":"..."}
//
// The page has no inline script (its CSP allows only this file), so everything
// comes from data- attributes. Nothing typed or printed is stored anywhere
// here: keystrokes go straight to the socket and output straight to the screen.
(function () {
  const root = document.querySelector("[data-terminal]");
  if (!root || typeof Terminal === "undefined" || typeof FitAddon === "undefined") {
    return;
  }
  const screen = root.querySelector("[data-terminal-screen]");
  const controls = root.querySelector("[data-terminal-controls]");
  const picker = root.querySelector("[data-terminal-shell]");
  const statusLine = root.querySelector("[data-terminal-status]");
  const newButton = root.querySelector("[data-terminal-new]");
  const closeLink = root.querySelector("[data-terminal-close]");
  const socketPath = root.dataset.socket;

  // The controls only work with this script, so they are hidden until it runs.
  controls.hidden = false;

  // DESIGN.md's dark colors as hex, since the terminal takes no oklch(): the
  // page ground, Readout White text, a Control Edge selection, and the four
  // status hues for the ANSI colors.
  const term = new Terminal({
    fontFamily: '"IBM Plex Mono", ui-monospace, "SF Mono", Menlo, monospace',
    fontSize: 13,
    cursorBlink: true,
    scrollback: 5000,
    theme: {
      background: "#0d0e11",
      foreground: "#eef0f3",
      cursor: "#eef0f3",
      selectionBackground: "#35383d",
      black: "#0d0e11", brightBlack: "#5f6469",
      red: "#ec6d3d", brightRed: "#ec6d3d",
      green: "#6bb9f8", brightGreen: "#6bb9f8",
      yellow: "#ffd972", brightYellow: "#ffd972",
      blue: "#6bb9f8", brightBlue: "#6bb9f8",
      magenta: "#d3befd", brightMagenta: "#d3befd",
      cyan: "#6bb9f8", brightCyan: "#6bb9f8",
      white: "#bdc1c6", brightWhite: "#eef0f3",
    },
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(screen);
  fit.fit();
  term.focus();

  // One socket at a time. `current` is the live one; a handler of an older
  // socket (one we closed ourselves) checks it is still current before it acts,
  // so a closed session never writes to the new one's screen or status line.
  let current = null;
  let finished = false; // the shell ended or the server closed it: no more input

  function setStatus(text) {
    statusLine.textContent = text;
  }

  function send(socket, message) {
    if (socket === current && socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify(message));
    }
  }

  // The words for the exit message, as the contract's two shapes.
  function exitText(message) {
    if (typeof message.code === "number") {
      return "Shell ended (exit " + message.code + ")";
    }
    return "Closed: " + (message.message || message.reason || "ended");
  }

  function connect() {
    if (current) {
      const old = current;
      current = null;
      old.close(1000);
    }
    finished = false;
    term.reset();
    fit.fit();

    const shell = picker.value;
    const query = new URLSearchParams({ shell: shell, cols: term.cols, rows: term.rows });
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(scheme + "//" + location.host + socketPath + "?" + query);
    socket.binaryType = "arraybuffer";
    current = socket;
    let opened = false;
    setStatus("Connecting…");

    socket.onopen = function () {
      if (socket !== current) return;
      opened = true;
      setStatus("Connected — " + (shell === "auto" ? "automatic shell" : shell));
      term.focus();
    };
    socket.onmessage = function (event) {
      if (socket !== current) return;
      if (typeof event.data === "string") {
        // The only text message is the last one, "exit".
        let message;
        try {
          message = JSON.parse(event.data);
        } catch (e) {
          return;
        }
        if (message.type === "exit") {
          finished = true;
          setStatus(exitText(message));
        }
        return;
      }
      term.write(new Uint8Array(event.data));
    };
    socket.onclose = function () {
      if (socket !== current) return;
      current = null;
      if (finished) return; // the exit message already said why
      finished = true;
      setStatus(opened
        ? "Connection lost"
        : "Couldn't connect: the copy may have stopped, the app may be stopped, or five terminals are already open");
    };
    // An error is always followed by a close, which says it.
    socket.onerror = function () {};
  }

  // Keystrokes and pastes go to the shell as they are.
  term.onData(function (data) {
    if (!finished && current) {
      send(current, { type: "input", data: data });
    }
  });

  // The shell's window size follows the terminal's, a moment after it settles
  // (dragging a window edge fires many resizes).
  let resizeTimer = null;
  term.onResize(function (size) {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(function () {
      if (current) send(current, { type: "resize", cols: size.cols, rows: size.rows });
    }, 150);
  });
  new ResizeObserver(function () {
    fit.fit();
  }).observe(screen);

  picker.addEventListener("change", connect);
  newButton.addEventListener("click", connect);
  // Leaving the page closes the socket, which ends the shell; Close says so first.
  closeLink.addEventListener("click", function () {
    if (current) current.close(1000);
  });
  window.addEventListener("pagehide", function () {
    if (current) current.close(1000);
  });

  connect();
})();
