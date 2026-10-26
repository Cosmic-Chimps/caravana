"use strict";

// caravana's own script for the Monitoring page (feature 025). The page is complete without it:
// the charts are drawn by the server as SVG, the filters are links, and a chart's head says "last
// 10 min". This file adds one thing, the hover readout. While the pointer is over a chart's plot it
// draws a 1px crosshair at the nearest point and writes that point's time, number and "usually" into
// the chart's head ("13:40 UTC · 07:40 local", the value, "usually 120"), and puts the head back
// when the pointer leaves.
//
// The markup says what to read: a plot is <div data-mon-plot data-t0 data-step data-values
// [data-usuals] data-unit [data-date]>, with its crosshair <line data-mon-cross>, inside a chart
// panel whose head has data-mon-now, data-mon-value, data-mon-unit and [data-mon-usually]. The
// values and the usuals are the server's own words, one per point (a point with no reading, or no
// band, is empty), written for display. Nothing is read from the network and
// nothing is put on the page as markup: the text goes in with textContent, and the crosshair moves
// with setAttribute (a style attribute would be refused by the page's policy).
//
// One handler on `document` serves every plot, including the ones htmx draws again each minute.
(function () {
  const PLOT = "[data-mon-plot]";
  const WIDTH = 300; // the plot's SVG box, in its own units

  // The browser's own zone, in the shape of localtime.js: "07:40", with the weekday on a dated range.
  const LOCAL = new Intl.DateTimeFormat("en-US", {
    weekday: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23",
  });
  const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

  function pad(n) {
    return String(n).padStart(2, "0");
  }

  function timeText(date, dated) {
    let utc = pad(date.getUTCHours()) + ":" + pad(date.getUTCMinutes());
    const parts = {};
    LOCAL.formatToParts(date).forEach(function (p) {
      parts[p.type] = p.value;
    });
    let local = (parts.hour === "24" ? "00" : parts.hour) + ":" + parts.minute;
    if (dated) {
      utc = WEEKDAYS[date.getUTCDay()] + " " + utc;
      local = parts.weekday + " " + local;
    }
    return utc + " UTC · " + local + " local";
  }

  const saved = new WeakMap(); // a chart's head as the server drew it, while it is being hovered

  function parts(plot) {
    const chart = plot.closest(".cv-mnchart");
    if (!chart) {
      return null;
    }
    const now = chart.querySelector("[data-mon-now]");
    const value = chart.querySelector("[data-mon-value]");
    const unit = chart.querySelector("[data-mon-unit]");
    const usually = chart.querySelector("[data-mon-usually]"); // absent on a chart with no band (Memory)
    return now && value && unit ? { chart: chart, now: now, value: value, unit: unit, usually: usually } : null;
  }

  function show(plot, clientX) {
    const values = (plot.dataset.values || "").split(",");
    const head = parts(plot);
    const rect = plot.getBoundingClientRect();
    if (!head || values.length < 2 || rect.width <= 0) {
      return;
    }
    const fraction = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
    const i = Math.round(fraction * (values.length - 1));
    const start = Date.parse(plot.dataset.t0);
    const step = Number(plot.dataset.step) * 1000;
    if (isNaN(start) || isNaN(step)) {
      return;
    }
    const cross = plot.querySelector("[data-mon-cross]");
    if (cross) {
      const x = String((i / (values.length - 1)) * WIDTH);
      cross.setAttribute("x1", x);
      cross.setAttribute("x2", x);
      cross.classList.add("cv-on");
    }
    if (!saved.has(head.chart)) {
      saved.set(head.chart, {
        now: head.now.textContent, value: head.value.textContent, unit: head.unit.textContent,
        usually: head.usually ? head.usually.textContent : "",
      });
    }
    head.now.textContent = timeText(new Date(start + i * step), plot.hasAttribute("data-date"));
    const value = values[i];
    head.value.textContent = value === "" ? "—" : value;
    head.unit.textContent = value === "" ? "no reading" : plot.dataset.unit || "";
    if (head.usually) {
      // "usually" follows the hovered point: its own band's median, or nothing where there is no band.
      head.usually.textContent = (plot.dataset.usuals || "").split(",")[i] || "";
    }
  }

  function hide(plot) {
    const cross = plot.querySelector("[data-mon-cross]");
    if (cross) {
      cross.classList.remove("cv-on");
    }
    const head = parts(plot);
    const before = head ? saved.get(head.chart) : null;
    if (head && before) {
      head.now.textContent = before.now;
      head.value.textContent = before.value;
      head.unit.textContent = before.unit;
      if (head.usually) {
        head.usually.textContent = before.usually;
      }
      saved.delete(head.chart);
    }
  }

  // The minute's refresh swaps each chart's content by id (htmx's out-of-band swaps). A chart with the
  // pointer over it keeps what it shows until the pointer leaves, so an open hover doesn't blink away.
  document.addEventListener("htmx:oobBeforeSwap", function (event) {
    const target = event.detail && event.detail.target;
    if (target instanceof Element && target.classList.contains("cv-mnchart") && saved.has(target)) {
      event.preventDefault();
    }
  });

  function plotOf(target) {
    return target instanceof Element ? target.closest(PLOT) : null;
  }

  document.addEventListener("pointermove", function (event) {
    const plot = plotOf(event.target);
    if (plot) {
      show(plot, event.clientX);
    }
  });

  document.addEventListener("pointerout", function (event) {
    const plot = plotOf(event.target);
    if (plot && !(event.relatedTarget instanceof Node && plot.contains(event.relatedTarget))) {
      hide(plot);
    }
  });
})();
