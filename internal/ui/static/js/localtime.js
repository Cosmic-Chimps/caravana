"use strict";

// caravana's own script for the viewer's local time (feature 022). Every clock
// time the console draws is UTC, named: <time class="cv-clock"
// datetime="2026-10-23T14:15:00Z" data-local="time">14:15 UTC</time>. This
// file adds the same moment in the browser's own zone after it, quieter:
// "14:15 UTC · 16:15 local". Without it the UTC time stands alone, and is
// right; nothing on a page needs it.
//
// What it does, and where the markup says so:
//
//   time.cv-clock          adds " · 16:15 local" after the element, in the
//                          shape its data-local names: time (16:15), time-s
//                          (16:15:02), weekday (Sun 16:15) or date
//                          (2026-10-23 16:15). A "time" whose local day isn't
//                          its UTC day gets the weekday, so it can't mislead.
//                          data-local="none" gets nothing (a page's title).
//   [data-local-line]      a table cell whose times take the local time on a
//                          line of its own, "Mon 03:00 local" under
//                          "in 2 d · Mon 09:00 UTC", with no separator, so the
//                          pair never wraps in the middle of a phrase.
//   [data-clock-group]     a list of times (the next three runs): one
//                          " · 16:15 · 16:30 · 16:45 local" after the list.
//   [data-local-lede]      the sentence that says "Times are UTC." gains
//                          " Your local time is shown beside them."
//   [data-local-hint=ID]   the hint under the field ID says what the time of
//                          day typed there is locally: "03:00 UTC is 05:00
//                          local", kept up to date while typing.
//
// It runs when the page loads and again after htmx settles a swapped region.
// Each element is done once (data-local-done), so a second pass changes
// nothing. Text goes in with textContent, never as markup. It names no zone:
// the word is "local".
(function () {
  // The parts of a moment in the browser's zone. en-US only names the
  // weekday; the numbers are put together here, so every browser gives the
  // same shape ("16:15", not "4:15 PM" or "16.15").
  const LOCAL = new Intl.DateTimeFormat("en-US", {
    weekday: "short", year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
  });

  function partsOf(date) {
    const out = {};
    LOCAL.formatToParts(date).forEach(function (p) {
      out[p.type] = p.value;
    });
    // Some engines still say "24" for midnight with h23.
    if (out.hour === "24") {
      out.hour = "00";
    }
    return out;
  }

  // The UTC date of a moment as YYYY-MM-DD, to compare with the local one.
  function utcDay(date) {
    return date.toISOString().slice(0, 10);
  }

  // localText is the moment in the browser's zone, in the given shape.
  function localText(date, shape) {
    const p = partsOf(date);
    let text = p.hour + ":" + p.minute;
    if (shape === "time-s") {
      text += ":" + p.second;
    }
    const day = p.year + "-" + p.month + "-" + p.day;
    if (shape === "date") {
      return day + " " + text;
    }
    if (shape === "weekday" || day !== utcDay(date)) {
      return p.weekday + " " + text;
    }
    return text;
  }

  function dateOf(time) {
    const date = new Date(time.getAttribute("datetime"));
    return isNaN(date.getTime()) ? null : date;
  }

  function addAfter(element, text, ownLine) {
    const span = document.createElement("span");
    span.className = ownLine ? "cv-clock-local cv-clock-local-line" : "cv-clock-local";
    span.textContent = text;
    element.after(span);
  }

  function doTimes(root) {
    root.querySelectorAll("time.cv-clock:not([data-local-done])").forEach(function (time) {
      if (time.closest("[data-clock-group]")) {
        return;
      }
      time.dataset.localDone = "1";
      if (time.dataset.local === "none") {
        return;
      }
      const date = dateOf(time);
      if (date) {
        if (time.closest("[data-local-line]")) {
          addAfter(time, localText(date, time.dataset.local) + " local", true);
        } else {
          addAfter(time, " · " + localText(date, time.dataset.local) + " local");
        }
      }
    });
  }

  function doGroups(root) {
    root.querySelectorAll("[data-clock-group]:not([data-local-done])").forEach(function (group) {
      group.dataset.localDone = "1";
      const texts = [];
      group.querySelectorAll("time.cv-clock").forEach(function (time) {
        const date = dateOf(time);
        if (date) {
          texts.push(localText(date, time.dataset.local));
        }
      });
      if (texts.length > 0) {
        addAfter(group, " · " + texts.join(" · ") + " local");
      }
    });
  }

  function doLedes(root) {
    root.querySelectorAll("[data-local-lede]:not([data-local-done])").forEach(function (lede) {
      lede.dataset.localDone = "1";
      lede.append(" Your local time is shown beside them.");
    });
  }

  // A time of day in what was typed ("every day at 03:00", "weekdays at
  // 9:30"), as the UTC hours and minutes; null for none.
  const TIME_OF_DAY = /(?:^|\D)(\d{1,2}):(\d{2})(?!\d)/;

  function hintText(value) {
    const m = TIME_OF_DAY.exec(value);
    if (!m) {
      return "";
    }
    const h = Number(m[1]);
    const min = Number(m[2]);
    if (h > 23 || min > 59) {
      return "";
    }
    const now = new Date();
    const date = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), h, min));
    const utc = String(h).padStart(2, "0") + ":" + m[2];
    const p = partsOf(date);
    let text = " " + utc + " UTC is " + p.hour + ":" + p.minute + " local";
    const day = p.year + "-" + p.month + "-" + p.day;
    if (day > utcDay(date)) {
      text += ", the next day";
    } else if (day < utcDay(date)) {
      text += ", the day before";
    }
    return text + ".";
  }

  function doHints(root) {
    root.querySelectorAll("[data-local-hint]:not([data-local-done])").forEach(function (hint) {
      const field = document.getElementById(hint.dataset.localHint);
      if (!field) {
        return;
      }
      hint.dataset.localDone = "1";
      const span = document.createElement("span");
      span.className = "cv-clock-local";
      hint.append(span);
      const update = function () {
        span.textContent = hintText(field.value);
      };
      field.addEventListener("input", update);
      update();
    });
  }

  function run(root) {
    doTimes(root);
    doGroups(root);
    doLedes(root);
    doHints(root);
  }

  // The script is loaded with `defer`, so the page is already parsed.
  run(document);
  document.addEventListener("htmx:afterSettle", function (event) {
    run(event.target instanceof Element ? event.target : document);
  });
})();
