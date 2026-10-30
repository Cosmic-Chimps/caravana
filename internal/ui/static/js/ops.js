"use strict";

// caravana's own script for the Operator Overview (feature 027). The page is complete without
// it: the server draws the Live activity panel as a snapshot and htmx draws it again on the live
// notices. This file adds two small things to that panel (the server page's body, feature 027
// page 2, gets the second one too: its Recent list has times with the same attribute):
//
//   - a new row (one whose data-ev the panel didn't have before the swap) gets .cv-row-fresh once,
//     the 5% veil that fades out in 600 ms (the One Tick Rule). The first draw of the page has
//     none: only rows that arrived live are fresh;
//   - each time (<time data-ops-time>, "14:02:14", UTC) gets the viewer's own time in its title,
//     "2026-10-30 14:02:14 UTC · 16:02:14 local", so the column stays narrow and the local time is
//     one hover away.
//
// Text goes in with the title property (a string, never markup); the page's policy allows no
// inline script or style, and none is used.
(function () {
  const PANEL = "ops-activity";
  const ROWS = "[data-ev]";

  const LOCAL = new Intl.DateTimeFormat("en-US", {
    hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
  });

  function localText(date) {
    const parts = {};
    LOCAL.formatToParts(date).forEach(function (p) {
      parts[p.type] = p.value;
    });
    const hour = parts.hour === "24" ? "00" : parts.hour;
    return hour + ":" + parts.minute + ":" + parts.second;
  }

  function titles(root) {
    root.querySelectorAll("time[data-ops-time]").forEach(function (el) {
      if (el.dataset.localDone) {
        return;
      }
      const date = new Date(el.getAttribute("datetime"));
      if (isNaN(date.getTime())) {
        return;
      }
      el.dataset.localDone = "1";
      el.title = (el.title ? el.title + " · " : "") + localText(date) + " local";
    });
  }

  function ids(panel) {
    return Array.from(panel.querySelectorAll(ROWS), function (row) {
      return row.dataset.ev;
    });
  }

  let before = null; // the entries the panel had before the swap in progress

  document.addEventListener("htmx:beforeSwap", function (event) {
    const target = event.detail && event.detail.target;
    before = target && target.id === PANEL ? new Set(ids(target)) : null;
  });

  document.addEventListener("htmx:afterSwap", function (event) {
    const target = event.detail && event.detail.target;
    const known = before;
    before = null;
    if (!target) {
      return;
    }
    // Any swapped region may hold times: the server page's body has its Recent list in it.
    titles(target);
    if (target.id !== PANEL) {
      return;
    }
    if (!known) {
      return;
    }
    target.querySelectorAll(ROWS).forEach(function (row) {
      if (!known.has(row.dataset.ev)) {
        row.classList.add("cv-row-fresh");
      }
    });
  });

  function start() {
    titles(document);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
