"use strict";

// caravana's own script for the console. Everything else on the page is
// plain HTML and htmx (vendored beside this file). This file only does what
// HTML can't:
//
//   - tell the live indicator what the page's event stream is doing,
//   - let htmx show a form the server refused (422, 502),
//   - copy a value to the clipboard,
//   - keep a live region's open rows open when it is drawn again,
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

  // ---- Rows that stay open ------------------------------------------------
  //
  // A live region (the Schedules list) is drawn again by the server on a
  // notice, and its rows are <details> elements with stable ids. Before htmx
  // swaps a region in, this remembers which of them are open and which are
  // closed; after, it puts them back. So a refresh never closes the row being
  // read, or opens again one the reader closed. A row that is new keeps what
  // the server drew.
  let openRows = null;

  document.addEventListener("htmx:beforeSwap", function (event) {
    const target = event.detail.target;
    openRows = null;
    if (!target || !event.detail.shouldSwap) {
      return;
    }
    openRows = new Map();
    target.querySelectorAll("details[id]").forEach(function (row) {
      openRows.set(row.id, row.open);
    });
  });

  document.addEventListener("htmx:afterSwap", function (event) {
    const target = event.detail.target;
    const state = openRows;
    openRows = null;
    if (!target || !state) {
      return;
    }
    target.querySelectorAll("details[id]").forEach(function (row) {
      if (state.has(row.id)) {
        row.open = state.get(row.id);
      }
    });
  });

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
