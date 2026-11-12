# Contract: console

Follows `DESIGN.md` (tokens only, light and dark, one 32px select, mono for names and versions) and the
existing New app page. Every form works without JavaScript. No new script is needed.

## New app › From a template — `GET /apps/new?from=template[&q=…][&tag=…][&place=…]`

A fourth tab beside Image, Compose and From a GitHub repository, labelled **From a template**.

- A GET form: a search input `q` (placeholder "Search templates") and a Search button; under it the tags
  of the offered templates as links (`?from=template&tag=<t>`, the chosen one marked `aria-current`, plus
  "All").
- The grid: one card per offered template (`available` only), each a link to
  `/apps/new/template/<name>` (keeping `place`): the logo (`<img src="/templates/<name>/logo.svg" alt="">`,
  40px) or the monogram tile (first letter of the title, the template's hue), the title, the version in mono,
  the description, the tags as small chips. Empty result: "No template matches." with a link to clear.
- Footer, small text: "Catalog <version>, built in." or "Catalog <version>, fetched <date clock>." plus,
  when the last check failed, its words; a POST form **Check now** (`/templates/check`) when checking is
  on; and a POST form with one button: **Stop checking for new templates** / **Check for new templates
  daily** (`/templates/settings`). Both answer 303 back to the page (`&checked=1` / `&saved=1` notice). A
  refusal (too soon) draws the page again with the words and 429.

## One template — `GET /apps/new/template/<name>[?place=…]`

- Header: logo or monogram, title, version (mono), the description, a link "Upstream" (rel="noopener
  noreferrer", `target` not set), the tags.
- "What it makes": one line per service: name (mono), image (mono), its domains as placeholders
  rendered as "your domain" words, "backed up (postgres|volume|sqlite)" when it has a backup.
- "Caravana makes N passwords for it" listing the secret **names** that hold generated values, with
  "They go to the app's secret store and are never shown." (Omitted when none.)
- The notes, rendered with placeholders shown as `<your domain>`-style words before install (no value
  is known yet), in a quiet panel.
- The form (`POST /apps/new/template/<name>`): **Name** (default `appName`), **Place** (the
  `place` select of New app, shown only when there is more than one environment), then each input in
  order: `domain` → `type=text` `inputmode=url` `autocomplete=off` `spellcheck=false`; `email` →
  `type=email`; `text` → `type=text` with `pattern` not set (the server checks); `secret` → `type=password`
  `autocomplete=new-password`. Label, help under it, the example as placeholder (`e.g. status.example.com`).
  Errors at their input in the existing field-error style; a template defect at the top. Secret inputs are
  never written back into the form (`value` empty) and get the line "Type it again: secrets aren't kept
  in the form."
- **No backup destination (T011a).** When the template has a backup and no destination is set, the form starts
  with a notice, "This template backs up <services>. No backup destination is set yet.", that links to Settings ›
  Backup storage, and a checkbox "Install without backups for now" (`name=withoutBackups`). Once a destination is
  set, or when the template has no backup, neither shows. Create and Preview post the checkbox; the install then
  carries one warning per backup left out.
- One submit button **Create** (and a quiet **Preview** button, `name=action value=preview`, which draws
  the page again with the rendered app's services and warnings, generating nothing).
- Success: 303 to `/apps/<id>/deployments?created=template&v_<input>=<answer>` (the plain answers only, never a
  secret; the page re-checks each before it uses it) where the existing created notice says
  "Installed from the <title> template." and, when the template has notes, shows them rendered with the
  inputs (plain values only).

## App pages

Configure's facts (the short Configure of 021) gain one row when the app has a template:
"Template · <title> <version>" (title from the catalog in use, else the name). Nothing else changes.

## Logo route

`GET /templates/<name>/logo.svg` on the console listener (signed-in only, like every page), headers of
research R8.

## Not on the console

No install of a template marked unavailable (its page answers 404 with "This template isn't offered: …").
