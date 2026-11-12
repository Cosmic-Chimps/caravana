// Passkeys in the console (feature 033). Ours, no library. Loaded only by the sign-in page and
// Settings › Sign-in.
//
// A block of the page that uses passkeys is marked with one of
//   data-passkey-signin   sign in on /login            (navigator.credentials.get)
//   data-passkey-confirm  "Confirm it's you"            (navigator.credentials.get)
//   data-passkey-add      add a passkey                 (navigator.credentials.create)
// and carries data-options (the address that makes the ceremony) and data-available ("true" when
// the server says this address can use passkeys). Inside it are a button [data-passkey-button], a
// status line [data-passkey-status] and a form [data-passkey-form] (the block itself, for add)
// with empty hidden fields `ceremony` and `credential`. The blocks start hidden: they are shown
// only here, and only when the browser supports passkeys and the server said they work here, so a
// browser without script sees the password path alone.
//
// A click asks the server for the ceremony (POST, same origin, JSON), calls the browser's
// credentials API with the decoded options, writes the ceremony's ID and the authenticator's answer
// (the same JSON a PublicKeyCredential has, binary fields in base64url) into the form's hidden
// fields and submits the form, so the server answers with its usual redirect or page. Nothing is
// logged or kept here.
(function () {
  "use strict";

  const supported = Boolean(window.PublicKeyCredential && window.isSecureContext && navigator.credentials);

  // ---- base64url <-> ArrayBuffer ---------------------------------------------------------
  // Written out rather than relying on parseRequestOptionsFromJSON and toJSON, which older
  // Safari doesn't have.
  function toBuffer(text) {
    let b64 = text.replace(/-/g, "+").replace(/_/g, "/");
    while (b64.length % 4) {
      b64 += "=";
    }
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) {
      bytes[i] = bin.charCodeAt(i);
    }
    return bytes.buffer;
  }

  function toText(buffer) {
    const bytes = new Uint8Array(buffer);
    let bin = "";
    for (let i = 0; i < bytes.length; i++) {
      bin += String.fromCharCode(bytes[i]);
    }
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  // ---- options from the server -----------------------------------------------------------
  // The server's publicKey object has its binary members as base64url text; the browser wants
  // buffers: challenge, user.id and the id of each allowed or excluded credential.
  function decodeOptions(publicKey) {
    const options = Object.assign({}, publicKey);
    options.challenge = toBuffer(publicKey.challenge);
    if (publicKey.user) {
      options.user = Object.assign({}, publicKey.user, { id: toBuffer(publicKey.user.id) });
    }
    ["excludeCredentials", "allowCredentials"].forEach(function (key) {
      if (Array.isArray(publicKey[key])) {
        options[key] = publicKey[key].map(function (c) {
          return Object.assign({}, c, { id: toBuffer(c.id) });
        });
      }
    });
    return options;
  }

  // ---- the answer, as the JSON the server parses -----------------------------------------
  function encodeCreated(cred) {
    const r = cred.response;
    const answer = {
      id: cred.id,
      rawId: toText(cred.rawId),
      type: cred.type,
      response: {
        clientDataJSON: toText(r.clientDataJSON),
        attestationObject: toText(r.attestationObject),
        transports: typeof r.getTransports === "function" ? r.getTransports() : [],
      },
      clientExtensionResults: typeof cred.getClientExtensionResults === "function" ? cred.getClientExtensionResults() : {},
    };
    if (cred.authenticatorAttachment) {
      answer.authenticatorAttachment = cred.authenticatorAttachment;
    }
    return answer;
  }

  function encodeAsserted(cred) {
    const r = cred.response;
    const answer = {
      id: cred.id,
      rawId: toText(cred.rawId),
      type: cred.type,
      response: {
        clientDataJSON: toText(r.clientDataJSON),
        authenticatorData: toText(r.authenticatorData),
        signature: toText(r.signature),
      },
      clientExtensionResults: typeof cred.getClientExtensionResults === "function" ? cred.getClientExtensionResults() : {},
    };
    if (r.userHandle && r.userHandle.byteLength > 0) {
      answer.response.userHandle = toText(r.userHandle);
    }
    if (cred.authenticatorAttachment) {
      answer.authenticatorAttachment = cred.authenticatorAttachment;
    }
    return answer;
  }

  // ---- talking to the server -------------------------------------------------------------
  // A refusal the server explains: its message is shown as it is.
  class ServerError extends Error {}

  async function fetchOptions(url) {
    let response;
    try {
      response = await fetch(url, {
        method: "POST",
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      });
    } catch (err) {
      throw new ServerError("Couldn't reach caravana. Try again.");
    }
    let body = null;
    try {
      body = await response.json();
    } catch (err) {
      body = null;
    }
    if (!response.ok) {
      const message = body && body.error && body.error.message;
      throw new ServerError(message || "caravana refused that. Reload the page and try again.");
    }
    if (!body || !body.ceremony || !body.publicKey) {
      throw new ServerError("caravana's answer wasn't what a passkey needs. Reload the page and try again.");
    }
    return body;
  }

  // ---- one block -------------------------------------------------------------------------
  // errorWords is what the status line says for what went wrong in the browser.
  function errorWords(err) {
    if (err instanceof ServerError) {
      return err.message;
    }
    switch (err && err.name) {
      case "NotAllowedError":
      case "AbortError":
        return "Cancelled.";
      case "InvalidStateError":
        return "That device already has a passkey here.";
      default:
        return "This browser couldn't use a passkey here.";
    }
  }

  function setup(block) {
    const creating = block.hasAttribute("data-passkey-add");
    const form = block.matches("form") ? block : block.querySelector("[data-passkey-form]");
    const button = block.querySelector("[data-passkey-button]");
    const status = block.querySelector("[data-passkey-status]");
    if (!form || !button) {
      return;
    }
    const say = function (text) {
      if (status) {
        status.textContent = text;
      }
    };

    async function run() {
      // The form is submitted by script, which skips the browser's own checks: the name of a new
      // passkey is required.
      if (creating && !form.reportValidity()) {
        return;
      }
      button.disabled = true;
      say("Waiting for your passkey…");
      try {
        const ceremony = await fetchOptions(block.getAttribute("data-options"));
        const options = decodeOptions(ceremony.publicKey);
        let answer;
        if (creating) {
          answer = encodeCreated(await navigator.credentials.create({ publicKey: options }));
        } else {
          answer = encodeAsserted(await navigator.credentials.get({ publicKey: options }));
        }
        form.elements.ceremony.value = ceremony.ceremony;
        form.elements.credential.value = JSON.stringify(answer);
        say("Checking…");
        form.submit();
      } catch (err) {
        say(errorWords(err));
        button.disabled = false;
      }
    }

    button.addEventListener("click", run);
    // Back to the page from the back/forward cache: a button left disabled by a submit works again.
    window.addEventListener("pageshow", function () {
      button.disabled = false;
      say("");
    });
    block.hidden = false;
  }

  function start() {
    document.querySelectorAll("[data-passkey-signin], [data-passkey-confirm], [data-passkey-add]").forEach(function (block) {
      if (supported && block.getAttribute("data-available") === "true") {
        setup(block);
      }
    });
    // A sentence for a browser that can't, shown only where the page offered something.
    document.querySelectorAll("[data-passkey-unsupported]").forEach(function (el) {
      el.hidden = supported;
    });
  }

  // The script is loaded with `defer`, so the page is already parsed.
  start();
})();
