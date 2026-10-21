"use strict";

// caravana's own script for the console. Everything else on the page is
// plain HTML and htmx (vendored beside this file). This file only does what
// HTML can't:
//
//   - tell the live indicator what the page's event stream is doing,
//   - let htmx show a form the server refused (422, 502),
//   - draw the Logs tab's lines as they arrive, with pause and wrapping,
//   - copy a value to the clipboard,
//   - keep a delete button off until the app's name has been typed.
//
// Why it looks like this, for a reader new to browser JavaScript:
//
//   - The whole file is one function that calls itself at once (an IIFE), so
//     the names inside it can't clash with names from other scripts.
//   - Instead of attaching a handler to every button and indicator, one
//     handler is attached to `document`. Events "bubble" up from the element
//     they happen on, so the handler sees them all, including ones from
//     elements htmx adds to the page later.
//   - The Content-Security-Policy forbids inline scripts, so there is no
//     onclick="..." anywhere. Markup says what it is with data-* attributes,
//     and this file reacts to them.
(function () {
  // How long "Copied" stays on a copy button before the label comes back.
  const COPIED_FOR_MS = 2000;

  // ---- The live indicator ------------------------------------------------
  //
  // The page's <body> opens a server-sent events stream (htmx's SSE
  // extension). The extension reports on it with three events, which bubble
  // up to `document`:
  //
  //   htmx:sseOpen   the stream connected (also after every reconnect)
  //   htmx:sseError  the connection dropped; the browser retries by itself
  //   htmx:sseClose  the stream was closed. detail.type is "message" when
  //                  the server's own `ended` event closed it (the session
  //                  ended); any other type is just the page going away.
  //
  // The indicator is the element marked data-live-indicator. CSS shows it
  // only once it has a data-state, and draws a different shape for each
  // state, so the status never depends on color alone.
  function setLive(state, text, linkText) {
    const indicator = document.querySelector("[data-live-indicator]");
    if (!indicator) {
      return;
    }
    const label = indicator.querySelector("[data-live-text]");
    indicator.dataset.state = state;
    // textContent (never innerHTML) puts the words in as text, not markup.
    label.textContent = text;
    if (linkText) {
      // "Signed out. " followed by a link back to the sign-in page, which
      // returns to the page the admin was on.
      const link = document.createElement("a");
      link.href = "/login?next=" + encodeURIComponent(location.pathname + location.search);
      link.textContent = linkText;
      label.append(link);
    }
  }

  document.addEventListener("htmx:sseOpen", function () {
    setLive("live", "live");
  });

  document.addEventListener("htmx:sseError", function () {
    setLive("reconnecting", "Reconnecting…");
  });

  document.addEventListener("htmx:sseClose", function (event) {
    if (event.detail && event.detail.type === "message") {
      setLive("signed-out", "Signed out. ", "Sign in again.");
    }
  });

  // ---- Refused forms -----------------------------------------------------
  //
  // htmx throws away an answer whose status is 4xx or 5xx, which would leave
  // a refused form looking as if nothing happened. The server answers a
  // refused form with the form again, errors under their fields, and one of
  // these three statuses (422: something typed is wrong; 409: the app changed
  // since the form was opened; 502: Nomad or the registry couldn't be used).
  // Swap those in.
  document.addEventListener("htmx:beforeSwap", function (event) {
    const status = event.detail.xhr.status;
    if (status === 422 || status === 409 || status === 502) {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
    }
  });

  // ---- The log view ------------------------------------------------------
  //
  // The Logs tab draws its tail on the server, so it can be read without this
  // file. With it, the page's event stream also carries the log lines: a
  // "logs-reset" event (clear the list; the tail follows) and a "log" event
  // per line whose data is JSON {copy, service, stream, text, time?}. The
  // markup says where they go:
  //
  //   [data-log]          the panel
  //   [data-log-list]     the list of lines
  //   [data-log-sink]     the element that listens for the events
  //   [data-log-status]   "Paused. 12 new lines."
  //   [data-log-dropped]  "Older lines were dropped."
  //   [data-log-empty]    "No output yet."
  //   [data-log-pause], [data-log-wrap]   the buttons, inside [data-log-tools]
  //
  // Every piece of a line is set with textContent (or a property that is only
  // ever text), never innerHTML: a line is whatever the app printed, and must
  // not become markup.

  const MAX_LINES = 5000; // the most lines the list keeps; older ones go
  const FLUSH_MS = 50; // new lines are drawn together this often
  const STATUS_MS = 250; // the paused counter is updated this often
  const NEAR_BOTTOM_PX = 24; // within this of the bottom counts as "at the bottom"

  let logView = null; // the one log view of the page, once set up

  function setUpLogView() {
    if (logView) {
      return logView;
    }
    const panel = document.querySelector("[data-log]");
    if (!panel) {
      return null;
    }
    logView = {
      list: panel.querySelector("[data-log-list]"),
      status: panel.querySelector("[data-log-status]"),
      dropped: panel.querySelector("[data-log-dropped]"),
      empty: panel.querySelector("[data-log-empty]"),
      pauseButton: document.querySelector("[data-log-pause]"),
      wrapButton: document.querySelector("[data-log-wrap]"),
      incoming: [], // lines received and not drawn yet, oldest first
      lost: false, // lines were thrown away before they were drawn
      paused: false,
      arrived: 0, // lines that arrived while paused
      flushTimer: null,
      statusTimer: null,
    };
    // The buttons do nothing without this file, so they start hidden.
    const tools = document.querySelector("[data-log-tools]");
    if (tools) {
      tools.hidden = false;
    }
    scrollToEnd(logView);
    return logView;
  }

  function scrollToEnd(view) {
    view.list.scrollTop = view.list.scrollHeight;
  }

  function nearEnd(view) {
    return view.list.scrollHeight - view.list.scrollTop - view.list.clientHeight <= NEAR_BOTTOM_PX;
  }

  // lineElement builds one line: the copy's short ID, a mark for stderr, the text.
  function lineElement(entry) {
    const row = document.createElement("div");
    row.className = "cv-log-line";

    const copy = document.createElement("span");
    copy.className = "cv-log-copy cv-data";
    copy.title = entry.copy;
    copy.textContent = entry.copy.slice(0, 8);

    const stream = document.createElement("span");
    if (entry.stream === "stderr") {
      stream.className = "cv-log-stream";
      stream.textContent = "stderr";
    } else {
      stream.className = "cv-log-stream";
      stream.setAttribute("aria-hidden", "true");
    }

    const text = document.createElement("span");
    text.className = "cv-log-text";
    text.textContent = entry.text;

    row.append(copy, stream, text);
    return row;
  }

  // flush draws the lines that have arrived, then keeps the list within
  // MAX_LINES and stays at the bottom if the reader was there.
  function flush(view) {
    view.flushTimer = null;
    if (view.paused || view.incoming.length === 0) {
      return;
    }
    const follow = nearEnd(view);
    let entries = view.incoming;
    view.incoming = [];
    if (entries.length > MAX_LINES) {
      entries = entries.slice(entries.length - MAX_LINES);
      view.lost = true;
    }
    const rows = document.createDocumentFragment();
    for (const entry of entries) {
      rows.append(lineElement(entry));
    }
    view.list.append(rows);
    while (view.list.childElementCount > MAX_LINES) {
      view.list.firstElementChild.remove();
      view.lost = true;
    }
    if (view.lost && view.dropped) {
      view.dropped.hidden = false;
    }
    if (view.empty) {
      view.empty.hidden = true;
    }
    if (follow) {
      scrollToEnd(view);
    }
  }

  function scheduleFlush(view) {
    if (view.flushTimer === null) {
      view.flushTimer = window.setTimeout(function () {
        flush(view);
      }, FLUSH_MS);
    }
  }

  function showStatus(view) {
    view.statusTimer = null;
    if (!view.status) {
      return;
    }
    if (!view.paused) {
      view.status.hidden = true;
      view.status.textContent = "";
      return;
    }
    const n = view.arrived;
    view.status.textContent = n === 0 ? "Paused." : "Paused. " + n + " new line" + (n === 1 ? "" : "s") + ".";
    view.status.hidden = false;
  }

  function scheduleStatus(view) {
    if (view.statusTimer === null) {
      view.statusTimer = window.setTimeout(function () {
        showStatus(view);
      }, STATUS_MS);
    }
  }

  function resetLog(view) {
    view.incoming = [];
    view.lost = false;
    view.arrived = 0;
    view.list.textContent = "";
    if (view.dropped) {
      view.dropped.hidden = true;
    }
    if (view.empty) {
      view.empty.hidden = false;
    }
    showStatus(view);
  }

  function receiveLine(view, entry) {
    view.incoming.push(entry);
    if (view.paused) {
      view.arrived += 1;
      // Hold no more than the list could show; the rest would be thrown away anyway.
      if (view.incoming.length > 2 * MAX_LINES) {
        view.incoming.splice(0, view.incoming.length - MAX_LINES);
        view.lost = true;
      }
      scheduleStatus(view);
    } else {
      scheduleFlush(view);
    }
  }

  // The SSE extension fires htmx:sseBeforeMessage on the sink before it swaps
  // the message in. The message is the event's detail (a MessageEvent). Cancel
  // the swap: the lines are drawn here, not by htmx.
  document.addEventListener("htmx:sseBeforeMessage", function (event) {
    const sink = event.target instanceof Element ? event.target.closest("[data-log-sink]") : null;
    if (!sink) {
      return;
    }
    event.preventDefault();
    const view = setUpLogView();
    const message = event.detail;
    if (!view || !message) {
      return;
    }
    if (message.type === "logs-reset") {
      resetLog(view);
      return;
    }
    if (message.type !== "log") {
      return;
    }
    let entry;
    try {
      entry = JSON.parse(message.data);
    } catch (err) {
      return; // not a line we know; the stream goes on
    }
    if (!entry || typeof entry.copy !== "string" || typeof entry.text !== "string") {
      return;
    }
    receiveLine(view, entry);
  });

  document.addEventListener("click", function (event) {
    if (!(event.target instanceof Element)) {
      return;
    }
    const pause = event.target.closest("[data-log-pause]");
    const wrap = event.target.closest("[data-log-wrap]");
    if (!pause && !wrap) {
      return;
    }
    const view = setUpLogView();
    if (!view) {
      return;
    }
    if (pause) {
      view.paused = !view.paused;
      pause.setAttribute("aria-pressed", String(view.paused));
      pause.textContent = view.paused ? "Resume" : "Pause";
      if (view.paused) {
        view.arrived = 0;
      } else {
        // Draw what came in while paused, and stay put if the reader scrolled up meanwhile.
        flush(view);
        view.arrived = 0;
      }
      showStatus(view);
    } else {
      const wrapped = view.list.classList.toggle("cv-log-wrap");
      wrap.setAttribute("aria-pressed", String(wrapped));
    }
  });

  // The server drew the tail already: show the buttons and start at the bottom.
  setUpLogView();

  // ---- Copy buttons ------------------------------------------------------
  //
  // <button data-copy="token-value"> copies the text of the element whose
  // id is token-value, then says "Copied" for a moment.

  // One pending "put the label back" timer per button, so clicking twice in
  // a row doesn't restore the label too early.
  const restoreTimers = new WeakMap();

  function say(button, text) {
    if (button.dataset.label === undefined) {
      button.dataset.label = button.textContent;
    }
    button.textContent = text;
    window.clearTimeout(restoreTimers.get(button));
    restoreTimers.set(
      button,
      window.setTimeout(function () {
        button.textContent = button.dataset.label;
        delete button.dataset.label;
      }, COPIED_FOR_MS)
    );
  }

  // Select the element's text, so Ctrl+C or Cmd+C finishes the job when the
  // clipboard can't be written (for instance on plain http:// to another
  // host, where browsers hide navigator.clipboard).
  function selectText(element) {
    const range = document.createRange();
    range.selectNodeContents(element);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
  }

  document.addEventListener("click", function (event) {
    const button = event.target.closest("[data-copy]");
    if (!button) {
      return;
    }
    const source = document.getElementById(button.dataset.copy);
    if (!source) {
      return;
    }
    const text = source.textContent.trim();

    // navigator.clipboard.writeText returns a promise: .then runs when it
    // worked, .catch when the browser refused.
    const write = navigator.clipboard
      ? navigator.clipboard.writeText(text)
      : Promise.reject(new Error("clipboard unavailable"));
    write.then(
      function () {
        say(button, "Copied");
      },
      function () {
        selectText(source);
        say(button, "Selected. Copy it now.");
      }
    );
  });

  // ---- Delete needs the name typed ---------------------------------------
  //
  // <input data-confirm-name="blog"> sits in a form with a submit button. The
  // button starts enabled in the HTML, so the form works without this file
  // (the server checks the name too, and is the real guard); here it is
  // disabled until the input holds exactly the name. The button's own text
  // doesn't change, and the input is labeled, so nothing relies on seeing
  // the button's state.

  function confirmButton(input) {
    const form = input.form;
    return form ? form.querySelector('button[type="submit"]') : null;
  }

  function matches(input) {
    return input.value === input.dataset.confirmName;
  }

  function syncConfirm(input) {
    const button = confirmButton(input);
    if (button) {
      button.disabled = !matches(input);
    }
  }

  function syncAllConfirms() {
    document.querySelectorAll("input[data-confirm-name]").forEach(syncConfirm);
  }

  // "input" fires on every keystroke, paste and autofill. One handler on
  // `document` covers the field wherever it is.
  document.addEventListener("input", function (event) {
    if (event.target.matches && event.target.matches("input[data-confirm-name]")) {
      syncConfirm(event.target);
    }
  });

  // A submit the button state didn't stop (Enter in the field, a script
  // race) still has to carry the name. The server checks it again.
  document.addEventListener("submit", function (event) {
    const input = event.target.querySelector && event.target.querySelector("input[data-confirm-name]");
    if (input && !matches(input)) {
      event.preventDefault();
      input.focus();
    }
  });

  // ---- Buttons that only work with this file ------------------------------
  //
  // A copy button does nothing without script, so the server sends it
  // hidden, and it appears here.
  function revealScriptButtons() {
    document.querySelectorAll("[data-copy][hidden]").forEach(function (button) {
      button.hidden = false;
    });
  }

  function start() {
    revealScriptButtons();
    syncAllConfirms();
  }

  // The script is loaded with `defer`, so the page is already parsed.
  start();
  // Coming back to a page from the back/forward cache keeps what was typed.
  window.addEventListener("pageshow", syncAllConfirms);
})();
