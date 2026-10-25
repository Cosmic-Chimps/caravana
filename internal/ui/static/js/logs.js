"use strict";

// caravana's own script for the Logs page (feature 024). The page is plain HTML and works
// without it: the stream, the filters, Live and Paused are links and a form. This file adds
// two small things, and only to what the server drew for them:
//
//   fresh rows     While the page follows (Live), htmx draws the stream region again on each
//                  logs notice. After each such swap this marks the rows the previous draw
//                  did not have with .cv-row-fresh, whose 5% veil fades out in 600 ms (the
//                  stylesheet drops it under reduced motion).
//
//   new lines      A paused page keeps the page's event stream open, but nothing is swapped:
//                  the head holds <span data-logs-feed hidden>. On each logs notice (at most
//                  one read every few seconds) this reads the stream's newest lines from
//                  data-count-href, counts the rows the page doesn't have, writes the number
//                  into #logs-paused-n and shows the span: "paused · 3 new lines since Show".
//                  Show is a plain link that turns Live on.
//
// A notice carries nothing, and a line is whatever the app printed: the lines read for the
// count are never put on the page. They are parsed (DOMParser runs no script), their row ids
// compared, and dropped. The numbers go in with textContent.
(function () {
  const ROWS = ".cv-lrow[id]";
  const REGION = "logs-live";
  const MIN_GAP_MS = 3000; // between two reads for the count

  function rowIds(root) {
    return Array.from(root.querySelectorAll(ROWS), function (row) {
      return row.id;
    });
  }

  // ---- Fresh rows ---------------------------------------------------------

  let before = null; // the ids the region had before the swap in progress

  document.addEventListener("htmx:beforeSwap", function (event) {
    const target = event.detail && event.detail.target;
    before = target && target.id === REGION ? new Set(rowIds(target)) : null;
  });

  document.addEventListener("htmx:afterSwap", function (event) {
    const target = event.detail && event.detail.target;
    const known = before;
    before = null;
    if (!known || !target || target.id !== REGION) {
      return;
    }
    target.querySelectorAll(ROWS).forEach(function (row) {
      if (!known.has(row.id)) {
        row.classList.add("cv-row-fresh");
      }
    });
  });

  // ---- New lines since the page was paused --------------------------------

  let timer = null; // a read is waiting its turn
  let reading = false;
  let lastRead = 0;

  function show(feed, n, more) {
    const number = feed.querySelector("#logs-paused-n");
    const unit = feed.querySelector("#logs-paused-unit");
    if (!number || !unit) {
      return;
    }
    number.textContent = String(n) + (more ? "+" : "");
    unit.textContent = n === 1 && !more ? "line" : "lines";
    feed.hidden = false;
  }

  function count(feed) {
    timer = null;
    const href = feed.dataset.countHref;
    if (reading || !href || !document.body.contains(feed)) {
      return;
    }
    reading = true;
    lastRead = Date.now();
    fetch(href, { credentials: "same-origin", headers: { "HX-Request": "true" } })
      .then(function (response) {
        return response.ok ? response.text() : null;
      })
      .then(function (body) {
        if (body === null) {
          return;
        }
        const newest = rowIds(new DOMParser().parseFromString(body, "text/html"));
        const have = new Set(rowIds(document));
        const fresh = newest.filter(function (id) {
          return !have.has(id);
        });
        if (fresh.length > 0) {
          // Every row of the read is new: there may be more than it holds.
          show(feed, fresh.length, fresh.length === newest.length);
        }
      })
      .catch(function () {
        // A read that failed leaves the count as it was; the next notice tries again.
      })
      .finally(function () {
        reading = false;
      });
  }

  document.addEventListener("sse:logs", function (event) {
    const feed = event.target instanceof Element ? event.target.closest("[data-logs-feed]") : null;
    if (!feed || timer !== null) {
      return;
    }
    timer = window.setTimeout(function () {
      count(feed);
    }, Math.max(0, lastRead + MIN_GAP_MS - Date.now()));
  });
})();
