# Contract: Settings › Certificates and Domains (034)

Written by the building session from the existing page (023's `domains-screen.md`) and DESIGN.md; the
designer's review is still to come, as for 030 and 031. Everything works without JavaScript. Fields and
selects are the console's 32px ones; text is never fg-4; no new colour.

## Settings › Certificates, top to bottom

1. **Lede**, by issuer: Let's Encrypt as today; custom: "HTTPS certificates for every domain come from
   <issuerName> and renew 30 days before they expire."; with uploaded certificates one more sentence:
   "Hosts an uploaded certificate covers use it instead."
2. Banners and the **Domain certificates** table as today. The Issuer column says the handshake's issuer;
   for an uploaded certificate "uploaded: <subject>". Expiry for an uploaded one is "expires in 61d"
   (never "renews in").
3. **Uploaded certificates** (label, with the count):
   - one row per certificate: mark (healthy, warning when expiring, failed when expired), the names in mono
     (first three, then "+n"), issuer, "expires in 61d" / "expired" (title: the date), `keyType`, and
     "not trusted by browsers" when `trusted` is false;
   - under a row, when it applies: "Serves a.example.test, b.example.test." and
     "Still on their earlier certificate: c.example.test. Traefik keeps a certificate it already has: run the
     installer again on this server to drop it.";
   - Remove: a `details` confirmation ("Remove this certificate? Its hosts go back to <issuerName>." or
     "…to Traefik's own certificate."), post to `/settings/certificates/uploaded/{id}/remove`;
   - **Upload a certificate** (`details`, open after a refusal): two textareas, "Certificate and its
     intermediates (PEM)" and "Private key (PEM)", the hint "The key is stored in Nomad's variables and is
     never shown again. Nothing renews an uploaded certificate: caravana warns <n> days before it ends.",
     button "Check and upload". Post to `/settings/certificates/upload`. A refusal draws the page again
     (422) with the message at its field and the certificate text kept; the key is never drawn back.
   - empty: "No uploaded certificates. Upload one to use a certificate you bought, or your company's."
   - unavailable (`uploadUnavailable`): no form; "Run the installer again on this server: its Traefik job is
     older than uploaded certificates." (or "caravana couldn't ask Nomad.")
4. **How certificates are issued** as today, plus:
   - Issuer row: the name; note for custom: "<server>. Set here." and "EAB key ID <id>" when set;
   - **Change issuer** (`details`, beside Edit): radio "Let's Encrypt (what the installer set)" / "Another
     ACME issuer"; ACME directory URL with the preset links "Google Trust Services · ZeroSSL · DigiCert"
     (`/settings/certificates?preset=<name>#issuer` draws the form open with the URL filled); Contact email;
     EAB key ID; EAB HMAC key (`type=password`, `autocomplete=off`, never filled back; hint "Leave empty to
     keep the stored key." when one is stored); "The issuer's own CA certificate (PEM)" in a nested
     `details` ("Only for an issuer with a private CA"); the warning line "Saving restarts Traefik once: a few
     seconds without HTTP, this page included. Every domain then gets a new certificate from the new
     issuer."; button "Save and restart Traefik". Post to `/settings/certificates/issuer`.
   - saved: 303 to `/settings/certificates?issuer=saved`, notice "Saved. Traefik is restarting; domains get
     their certificates from <issuerName> as they are asked for."
   - a refusal draws the page again (422 or 409) with the form open, the message at its field, every value
     kept but the HMAC key.
   - unavailable (`issuerUnavailable`): no form; the note says why: "Installed without an issuer
     (`--acme none`): run the installer with `--acme production` to have one, then choose it here." /
     "Run the installer again on this server…".

## Domains (`/domains`, the app's Domains page, the Add panel)

- "Let's Encrypt" is no longer written into sentences: they name `issuerName` ("waiting for Google Trust
  Services", "<issuerName> renews 30 days before expiry…"). With issuer `none`/`unknown` the words are as
  today.
- A live row's certificate fact for an uploaded certificate: "uploaded: <subject>, expires <date>" (date as
  "Dec 2"; "expires in 5d" in the Certificate column), and its expiring sentence says "Nothing renews an
  uploaded certificate: upload its replacement in Settings › Certificates." with a link.
- The footer line says "Certificates come from <issuerName>…" and, with uploads, "or from the certificates
  uploaded in Settings".

## Needs you

`certificate_expiring`: "The uploaded certificate for `*.example.test` expires in 5 days (Dec 2). Upload its
replacement in Settings › Certificates." / "…expired on Dec 2." One source, `service.ProblemParts`; the link
goes to `/settings/certificates`.
