"use strict";

// caravana's own script for New app › From a template (feature 058). The page is complete without it:
// every input is drawn, under a heading that says which answer it is for ("Only for GitHub"), and the
// server reads only the inputs the chosen answers ask for. This file hides the groups of inputs a choice
// doesn't ask for, and turns their required attributes on only while they are shown, so the browser
// never asks for a field that won't be used. A hidden group is also disabled: nothing in it is sent.
//
// A group is a <fieldset data-when='{"signin":["github"]}'>: it is shown when every named choice has
// one of the listed values. A choice inside a hidden group counts as empty, so groups that depend on
// each other follow in document order. No inline script or style is used.
(function () {
  const form = document.querySelector("form[data-template-form]");
  if (!form) {
    return;
  }
  const groups = Array.from(form.querySelectorAll("fieldset[data-when]"));

  // valueOf is the answer of the choice input called name: "" when it has none, or when it sits in a
  // group that is hidden.
  function valueOf(name) {
    const field = form.querySelector('[name="in_' + name + '"]');
    if (!field) {
      return "";
    }
    const group = field.closest("fieldset[data-when]");
    if (group && group.hidden) {
      return "";
    }
    const control = form.elements["in_" + name];
    return control && control.value ? control.value : "";
  }

  function holds(condition) {
    return Object.keys(condition).every(function (name) {
      const value = valueOf(name);
      return value !== "" && condition[name].indexOf(value) >= 0;
    });
  }

  function update() {
    groups.forEach(function (group) {
      let condition;
      try {
        condition = JSON.parse(group.dataset.when);
      } catch (e) {
        return; // a group the script can't read stays shown, as without script
      }
      const on = holds(condition);
      group.hidden = !on;
      group.disabled = !on;
      group.querySelectorAll("[data-required]").forEach(function (el) {
        el.required = on;
      });
    });
  }

  form.addEventListener("change", update);
  form.addEventListener("input", update);
  update();
})();
