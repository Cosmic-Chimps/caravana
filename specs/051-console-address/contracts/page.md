# Page contract — Operator › Console address

No designer pass: the existing parts and `DESIGN.md`'s rules (as 050). Forms work without JavaScript.

- Sidebar: Operator group, "Console address" after Updates (`navOpsConsoleAddress`), route `GET /ops/console-address`.
- **Now**: the address as a link, "Issuer <issuer>" when set, old domains still redirecting with "until <clock>".
- **Depends on it** (sentences, counts from `dependents`): passkeys ("N passkeys belong to <domain>: they won't work at
  the new address. Password sign-in stays on there until you add a passkey."), "You sign in again at the new address.",
  webhooks and tokens ("Their senders (CI's CARAVANA_URL, Git hosts' webhook URLs) still hold <domain>: update them
  before the old address stops answering."), the issuer ("Anything that trusts the issuer <issuer>, such as Bella's
  Nomad attestor, needs the new one."), "Notification links and joined servers follow by themselves."
- **Change form** (`POST /ops/console-address/changes`): New domain (text, `autocomplete=off`), "Keep the old
  address for" select (0 "Don't keep it", 7, 30 (selected), 90 days), Check. Hint: "This server's sslip.io name:
  <hint>". A refusal is drawn at the field with 400/409/422 status; a `domain_not_here` with a zone offers a second
  button "Create the record in <zone> and check" (`createDNSRecord=1`). When `canChange` is false the form is
  replaced by the sentence.
- **Change panel** (`id="console-change"`, region `/parts/ops/console-address`, polled every 3 s while checking or
  switching, answering 286 once settled): one row per check (state word, message), the status sentence, and the
  actions: Switch to <domain> (`POST /ops/console-address/changes/{id}/switch`) when ready, Cancel
  (`POST /ops/console-address/changes/{id}/cancel`) until switching, and for `switching` "Waiting for the server
  agent (up to a minute). Caravana restarts at https://<domain>/; sign in there." with a link. A `noscript` meta
  refresh while open. Settled: the outcome sentence and its reason.
- Posts answer 303 to `/ops/console-address`; refusals draw the page with their status.
