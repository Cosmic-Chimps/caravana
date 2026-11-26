# Contract: Configure in sections (task 4mvb)

Source: the maintainer's approved mockup (2026-11-26), five artboards: Configure › Overview, › Source (Compose
file), › Services, › Integrations, and Overview at 390 px. Files: `design/configure-mock/project/*.dc.html` in the
main checkout (git-ignored). This contract was drafted by the builder session from the mockup because no designer
session was open; the designer reviews it with the build (section 9).

The rule of the page: **one concern per page, one deploy per change.** Each section is its own address and its own
form with its own submit on the page's revision. Every form works without JavaScript.

## 1. Frame

- The app header and the app's tab row are unchanged; the Configure tab is current on every section.
- Under the tab row: `.cv-cfg`, a flex row that wraps (gap 28px, `align-items: flex-start`): the section nav, then the
  section's body (`flex: 999 1 560px; min-width: 0`, a column with gap 16px).
- **Section nav** (`nav aria-label="Configure"`): Settings' nav reused (`cv-snav-item`, `cv-snav-text`, `cv-snav-count`),
  with a modifier `cv-snav-inpage` so it is not full height and has no right border: `flex: 1 1 200px`, column, gap 2px,
  `position: sticky; top: 16px` (below the shell's top bar: use the same offset rule Settings uses, 52px + 16px if the bar
  is sticky). No title, no group labels, no icons (the mockup has none). Items, in this order:
  1. Overview → `/apps/{id}/configure`
  2. Source → `/apps/{id}/configure/source`
  3. Services → `/apps/{id}/configure/services`, count = number of services (none when 0)
  4. Variables & secrets → `/apps/{id}/variables`, count = variables + secrets of all services (none when 0)
  5. Domains → `/apps/{id}/domains`, count = domains (none when 0)
  6. Integrations → `/apps/{id}/configure/integrations`
  The current one has `aria-current="page"` (Settings' current style). Under 900px of viewport it is Settings' strip: a
  row that scrolls sideways, current first, fade at the right (reuse the Settings rules; they key on `.cv-snav`).
- Variables & secrets and Domains are today's pages, drawn inside the same frame with the nav.
- A section's notices (stopped app, copied app, a refusal) sit at the top of the body, above its label.
- Each section body opens with its Label head (`cv-label`, h2) and one `cv-note` sentence (mockup's `.intro`, 13px fg-3,
  max 760px). Overview has the sentence only: "What this app is made of. Each part is changed on its own page, and every
  change is a deploy."

## 2. Overview (`GET /apps/{id}/configure`)

Read-only, no inputs. A grid of panels (`repeat(auto-fit, minmax(320px, 1fr))`, gap 16px, `align-items: start`); a
panel is `cv-panel` 18px padding, a column with gap 12px: Label head, lines, then a foot row of secondary small buttons
(links). Panels in this order:

1. **Source**: the strong line says what the app is made from, one of: `Template · <title> <version>` (the template
   fact), `Compose file` (kept), `Compose file, not kept` (made from Compose before 015), `Git · <repository> <branch>`
   (a built app), `Image` (one service) / `Images` (several). A second line: "Made from a Compose file, kept as you typed
   it." / "Each service runs the image it names." / "Built from <branch> on each deploy." / for an app with no
   definition "Not defined yet." Button: "Edit the Compose file" / "Change the images" / "Change the build" /
   "Define the app" → Source.
2. **Variables & secrets**: strong line "N variables · M secrets"; a mono line of secret names per service (today's
   `secretsPanelOf` lines); muted "Values of secrets are never shown." Button "Manage variables and secrets".
3. **Services** (wide, `grid-column: 1/-1`): one row per service, a 4-column grid (110px name, image, copies and size,
   check and volumes), mono 13px, 1px dividers between rows: `server · <image> · 1 copy · 512 MB, up to 1 GB · GET
   /health/ready`; a service with volumes adds `volume <name>` to the last column; a scaled service says
   `1–3 copies`. Under 720px of room the row has two columns (the 110px name, then image, copies and check each on
   its own line beside it), and under 480px it stacks (task `qqrj`). Button "Change copies, size and checks".
4. **Domains**: one line per domain (status mark of its state when known, host in mono strong, `→ <service> :<port>`
   when the app has several services). None: "No domains. Copies are reachable only inside the app." Buttons
   "Manage domains", and "+ Add domain" when the person may add one.
5. **Backups**: one line per backed-up service: `<service>` mono strong ` · daily at 03:00 UTC, keeping 14` (the
   built-in schedule's words; "no automatic backups" for `never`), then a line where they go and the last backup
   when known. None: "No backups." Button "Open the Backups tab".
6. **Schedules**: today's schedule lines (mark, name, words) and note. Buttons "Manage schedules", "+ New schedule"
   (when the person may make one).
7. **Integrations**: "Webhook token made <date>." or "No webhook token." Muted "Export the app as a plain Nomad job."
   Button "Open integrations". Shown only to a person who may maintain the app; otherwise the panel says only the export line.
8. **Delete app** (wide), last: what goes (today's `whatGoes` sentence) and today's `<details>` confirmation with the
   name typed. **No Fault Ember edge on this panel** (the designer's ruling: colour is status only); the button stays
   `cv-btn-danger`. Not drawn for a person who may not configure the app.

An app with no definition draws Source ("Define the app"), Integrations and Delete only.

## 3. Source (`GET /apps/{id}/configure/source`)

Label "Compose file" (kept or not kept) / "Images" / "Build" / "Define the app". What it holds:

- **Kept Compose file**: the intro "This app is edited through its Compose file. Change the file, preview what changes,
  then deploy. Copies and memory can also be changed on Services, which writes them into this file. Health checks are
  on Services, secrets on Variables & secrets, domains on Domains and backups on the Backups tab: a deploy from here
  keeps them as they are." (The sentence on copies and memory came with task gqbf.) Then today's compose form with **only** the file (the field
  label names the template when there is one: "From template <title> <version>"), Preview, "Against the app today",
  "What changes in meaning", and Deploy. No per-service inputs at all: no domains (already gone), no health, no secret
  rows, no backup fieldset. Beside Deploy: "Secrets, domains, health checks and backups stay as they are."
  The CI note (`FromCI`) stays.
- **Not kept**: today's "paste it once" notice and the same form, then the image fields below (as for an image app).
- **Image app**: per service a form (`cv-panel`, the service name in mono as its head when several): Image, Container
  port (one-port service), "Domains go to" (several ports with domains). "Save and deploy <service>" (or "Save and
  deploy" for one service), beside it muted "<other> is left as it is." Under them a panel "Deploy from Compose":
  "Replace the app with a Compose file." with its button, as today.
- **Built app**: today's Build panel and Git access per built service; other services of the app get the image form.
- **No definition**: today's Define form.

## 4. Services (`GET /apps/{id}/configure/services`)

Label "Services", intro "Copies, size and health check of each service. Each service is saved on its own, and saving
is a deploy." (+ for a kept-file app: " For this app the health check is written into its Compose settings; the file
itself is changed on Source.")

One `form.cv-panel` per service (gap 18px): head = name (mono 500 15px) and, muted mono, the image (and `· volume <v>
→ <mount>` for volumes). Groups in a grid `repeat(auto-fit, minmax(300px, 1fr))`, gap 18px 28px; each group a Label
(h3) then three fields in a row (`cv-fields3`), 32px inputs:

- **Copies & size**: Copies, Reserve MB, Up to MB, and CPU MHz (four fields: keep today's four, `cv-fields4`). A scaled
  service's Copies reads "Set by scaling, 1–3". A service with a volume or a published port: Copies is read-only "1",
  muted "A service with a volume runs one copy." For a kept-file app the group was read-only text (e.g. "1 copy · 512 MB,
  up to 1 GB") with muted "Set in the Compose file." (deviation from the mockup, see plan D4); task gqbf made Copies,
  Reserve and Up to inputs written into the file, and keeps the text only for a file that can't take the edit.
- **Health check**: GET path, Every s, Timeout s; muted "New copies must pass it before they take traffic; if they
  don't, the deploy is reverted." An empty path: placeholder "TCP port", muted "Empty checks the TCP port." Not drawn for a
  service with no port (as today, `ShowHealth`).
- **Placement**: today's placement panel (form for an image app; read-only with "Set in the Compose file." for a
  kept-file app).
- **Scaling**: today's scale panel, same rule.
- **Identities**: read-only, when the service has some.
- **Backups**: read-only line, when the service has a policy: "Daily at 03:00 UTC, keeping 14." with "Changed on the
  Backups tab." (link).

Foot (`border-top` 1px, 14px padding-top): primary "Save and deploy <service>" (one service: "Save and deploy"), muted
"<others> is left as it is." A stopped app: no button, the fieldset disabled, the stopped notice at the top. A person
who may not configure: everything read-only, "Your role here is to look: changing how this app is set up is for
people who maintain it." at the top.

Placement and Scaling keep their own forms and routes (`/services/{svc}/placement`, `/scale`); they sit inside the
service's panel but outside its main `<form>` (forms can't nest): the main form holds Copies & size and Health check,
and its foot is at the bottom of the panel.

## 5. Integrations (`GET /apps/{id}/configure/integrations`)

Label "Integrations", intro "How other tools deploy and read this app. Tokens are shown once, when they are made."
Three panels, today's content: **Webhook** (with the call as a `cv-cmd` block when a token exists: `curl -X POST
<console>/v1/webhooks/<id> -H "Authorization: Bearer $CARAVANA_WEBHOOK_TOKEN"`), **Deploy token**, **Export**
("The app as a plain Nomad job, as deployed in <version>. It runs without Caravana." + "Download the jobspec").
Making a token answers this page (200, shown once); revoking redirects here.

## 6. Variables & secrets, Domains

Unchanged content, drawn in the frame with the nav. Their "Configure" links in the "define first" and "edit the file"
sentences point at Source.

## 7. Backups tab: Change the policy

Each backed-up service's `<details>` gets "Change the policy…" beside its other buttons: a confirmation form like
"Back up daily" with Engine (a select of the engines the service can use; today's `backupEngineFor` and the SQLite
choice with its Database file field), Schedule (Daily at 03:00 UTC / No automatic backups / the stored cron line as
"Keep <words>" when it is another), Keep (number). "Save and deploy". Restore schedule, destination and copies stay as
they are. A refusal is drawn at its field.

## 8. Words

"Save and deploy" on every submit that deploys; no "ship"; voice rules of 021. No text in fg-4.

## 9. Review (designer)

At 1440 and 390 px, dark and light: each section's address, the nav current and its counts, Overview panels, Source
for a kept file / an image app / a built app / an undefined app, Services for a kept-file app and an image app with two
services, Integrations after making a webhook token, a stopped app, a person with the view role. Then the impeccable
critique (no detector) and the last look.

### 9.1 What the review found (2026-11-26, fixed in 75d9f37)

Run on the docs demo console with three added apps (a kept-file Compose app made from a template, a two-service
image app, an app with a name only), at 1440 and 390 px in both themes, as the owner and as a person with the view
role.

- Overview: the Delete panel now says what goes before its button (the sentence sat inside the closed details);
  a backup line reads "In main" (was "Go to main."); an app with neither reads "No variables or secrets." (was
  "0 variables · 0 secrets"); sizes keep number and unit on one line; a long image name wraps inside its column
  instead of widening the grid; a person who may only look gets "See the Compose file", "See the images" or
  "See the build" instead of a button that promises a change.
- Services: the placement form waits behind "Change placement…" (the sentences stay in view); the scaling rule is
  one paragraph in the body size; the other groups sit two to a row with a rule above them; copies and size take
  two fields a row inside a half-width group (the CPU field's "default" was cut off); the health path field is
  labelled "Path" (the group's head already says Health check), and its hint about an empty path shows only while
  the path is empty.
- Source: the existing app's Compose form labels its field "The file" under the "Compose file" head (the label
  repeated the head).
- A row of buttons keeps them on the first line while one of its confirmations is open.

### 9.2 The critique's rulings (impeccable critique, no detector)

Taken (this commit):
- 1: the read-only placement and scaling notes of a kept-file app said "Set in the Compose overlay.", a word the
  console never shows. They now say "Set through the API, in the Compose settings."
- 2: the placement and scaling buttons say they deploy: "Save placement and deploy", "Save scaling and deploy"
  (the sentence under them that said so is gone; the button names its form, since Services has several).
- 4: an existing app's Compose form says beside Preview: "Preview checks the file and shows what changes. Nothing is
  deployed until you choose Deploy."
- 7: a link inside a note of a section (`.cv-cfg-body .cv-note a`) is underlined like New app's, so it reads as one.
- 8: the empty health path's placeholder is "empty: TCP check" (was "TCP port", which read like a port number).
- 9: Overview's Source panel for an app with a name only reads "Not defined yet" with "Give it an image or a
  Compose file on Source."; Integrations says "Export the app as a plain Nomad job." only when there is an export.

Left, with the reason:
- 3 and C (the Compose form's "Deploy" should read "Save and deploy"): "Deploy" already says it deploys, and
  `DESIGN.md` keeps "Save and deploy" for saves of stored fields.
- 5 (rename and widen copies and size): the fields keep the names `DESIGN.md` gave them in 021 (Copies, CPU, Memory,
  Ceiling); two a row instead of four is the half-width group's own rule.
- 6 (one typeface in the scaling sentence): the console puts names and numbers in mono inside sentences everywhere.
- 10 (a long image name widens Overview's grid): fixed by the review already; the name wraps only as a last resort.
- For the maintainer: A (copies and size read-only for a kept-file app) stays as plan D4, with follow-up task
  `gqbf`; B, "Also set" on Services, stays so nothing the old read-only Definition showed is lost.
