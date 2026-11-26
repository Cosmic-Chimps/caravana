# The documentation site

This folder builds <https://cosmic-chimps.github.io/caravana/>, the documentation of Caravana, with
[Astro Starlight](https://starlight.astro.build/). The pages are not written here. They come from the
Markdown in the repository (`docs/`, listed in `pages.mjs`, and `CHANGELOG.md`), which reads the same on
GitHub, and `scripts/collect.mjs` turns it into `src/content/docs/` at build time. That folder is
generated and ignored, so to change a page you edit its source Markdown, and nothing here.

One source file is one page. A source has no frontmatter and starts with one `# Title`, which is the
page's title; the sidebar label may be shorter.

## Build and preview

You need the Node version in `.nvmrc` (22.23.4; Astro asks for that or later). The workflow reads
the same file, so CI and a local build run the same Node.

```sh
cd site
npm ci
npm run build         # collect, then astro build, then the check of dist/
npm run dev           # collect, then the dev server (restart it to pick up a changed source)
npm run collect       # the collect script alone
npm run preview       # serve dist/ at http://localhost:4321/caravana/
```

### Drafts

While pages or images are still being written, `npm run build:draft` (and `npm run dev:draft`,
`npm run collect:draft`) runs `node scripts/collect.mjs --allow-missing`. With that flag:

- a page whose source file does not exist yet is skipped, with its sidebar entry;
- a link to such a source goes to GitHub instead of failing;
- an image under `docs/images/` that does not exist yet becomes a one-pixel placeholder;
- everything else is as strict as the real build, and each skip is printed (`collect: draft: …`).

The flag is for local work. `npm run build` never uses it, and neither does CI, so a missing page or
image fails there.

## Where things are

| Path | What it is |
| --- | --- |
| `site.config.mjs` | The address of the site and of the repository. The first two lines are the only ones to change if the site moves to a domain of its own. |
| `pages.mjs` | The table of pages: each slug and the source file it comes from. |
| `sidebar.mjs` | The sidebar: groups, labels, and the one link that is not a page (the API description on GitHub). It must name every page of `pages.mjs` and nothing else. |
| `scripts/collect.mjs` | Reads each source, writes its frontmatter, rewrites its links and screenshots, and copies the images the pages show from `docs/images/` into `public/images/`, and the fonts from `internal/ui/static/fonts/`. |
| `astro.config.mjs` | Starlight's settings, the code blocks' frame, and the site's `head` entries: the Content-Security-Policy and the link preview (both below). |
| `scripts/check-dist.mjs` | Reads the built `dist/` and fails if it asks another host for anything, if an image of the site is not in `dist/`, or if a page is without the policy. |
| `src/styles/caravana.css` | The look: the console's colours and fonts, through Starlight's custom properties. The one stylesheet. |
| `src/assets/`, `public/favicon.svg`, `public/favicon-32.png`, `public/apple-touch-icon.png` | The mark for the dark and the light theme, and the icons. |
| `social/card.html`, `public/og.png` | The link preview picture, and its source. See below. |
| `scripts/screenshots/` | The script that makes the screenshots in `docs/images/`. See below. |

The fonts (IBM Plex Sans and Mono, the console's own files) are copied by the collect script into
`src/fonts/`, and their licence into `public/licenses/fonts/`. The images are the ones the pages
reference, copied from `docs/images/` into `public/images/`. All of these are generated and ignored;
the other files in `public/` are committed.

## Screenshots in the sources

A page shows a console screen with a `<picture>` element, which is what works on GitHub:

```html
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="../images/apps-home-dark.png">
  <img alt="The Apps page: three apps of the shop project, all healthy." src="../images/apps-home-light.png" width="760">
</picture>
```

The paths are relative to the Markdown file and point into `docs/images/`. On the site the reader's
choice in Starlight's theme select, not the system's, decides which picture shows, so `collect.mjs`
turns each `<picture>` into two images in a `<div class="cv-shot">`: `cv-shot-light` and `cv-shot-dark`
(`src/styles/caravana.css` shows one of them). The alt text goes on both, since the hidden one is not
read. A `<picture>` that is not exactly one dark `<source>` and one `<img>` with `alt`, `src` and a
`width`, both under `docs/images/`, fails the build.

## The link preview

A chat or social app that shows a link to the site draws a picture (`og:image`) and an icon. It does
not draw an SVG, so both are PNG files, committed in `public/`, and `astro.config.mjs` names them in
the page's `head` (meta and link elements only, no script). The picture's source is `social/card.html`
(the layout is the designer's: the lockup, the title, one sentence and a footer, in the dark theme's
colours and the console's fonts).

To make the picture again after changing the card, with any Chrome or Chromium, from `site/` after
`npm run collect` (the card reads the fonts from `src/fonts/`):

```sh
chrome --headless --hide-scrollbars --virtual-time-budget=2000 --window-size=1200,630 \
  --screenshot=public/og.png social/card.html
```

The build does not run it: the PNG is committed. An app keeps a preview for a while after it first
fetched a link, so a changed picture shows only later, or at once on an address it has not seen
(add `?v=2`).

## The screenshots

The PNG files in `docs/images/` are made by a script, never by hand. That script, how to run it and
what it captures are described by its own author in `scripts/screenshots/`; this README does not
repeat them.

## What fails the build

- A page of `pages.mjs` whose source file does not exist (not with `--allow-missing`).
- A link in a source that does not become a working link on the site: a file that does not exist, an
  anchor that is not a heading. The message is `source:line: target: reason`. A link to a file that is
  not a page (`docs/vision.md`, a spec, `deploy/install/install.sh`) becomes a link to it on GitHub.
- An image a page shows (`![alt](path)`, `src`, `srcset`) that is not a PNG, WebP, JPEG, GIF or AVIF
  file under `docs/images/` (no SVG: served from the site's own address, it can run script).
  Those images are copied to the site and the page shows the copy, so nothing is loaded from GitHub.
  A `srcset` holds one address with no descriptor (no comma, no `2x`).
- A `<picture>` in a shape the site does not know (see above).
- A page that is not in `sidebar.mjs`, or an entry of it for a page that is not in `pages.mjs`.
- A source that does not start with a `# ` title, or that carries frontmatter, or a `# ` heading
  inside a page.
- A link the built pages do not resolve (`starlight-links-validator`, a second check).
- A link preview picture (`og:image`, `twitter:image`) that is not on the site's own host, or that
  `dist/` does not hold at that address.
- An `<img src>` or `<source srcset>` on the site's own host that `dist/` does not hold (both images
  of a screenshot are checked).
- Anything in `dist/` that asks another host for a file: an external script, stylesheet, font or
  image, a new URL in the JavaScript that `check-dist.mjs` has not been told about, or a host it
  does not know named in a script, a stylesheet or an SVG file.
- A page without the Content-Security-Policy element, one where a script or a stylesheet comes
  before it, or a policy that allows a host, a scheme other than `data:`, or a keyword other than
  `'self'`, `'none'`, `'unsafe-inline'` and `'wasm-unsafe-eval'`. A hash or a nonce is accepted.

## The Content-Security-Policy

The site asks no other host for anything. `check-dist.mjs` proves that of the built files; the
policy has the browser refuse it while a page runs, which also covers what a scan of files cannot
see, such as a URL a script puts together at run time.

GitHub Pages sends no header of ours, so the policy is a `<meta http-equiv="Content-Security-Policy">`
in every page. It is the first `head` entry in `astro.config.mjs`. The other `head` entries are the
link preview's, which are meta and link elements only. Another entry needs as good a reason, and a
script or a stylesheet from another host would be refused by this one anyway.

```text
default-src 'self';
script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval';
style-src 'self' 'unsafe-inline';
img-src 'self' data:;
base-uri 'none';
form-action 'self'
```

Each exception to `default-src 'self'` is something a browser refused without it:

| Source | What needs it |
| --- | --- |
| `'unsafe-inline'` in `script-src` | Starlight's inline scripts, such as the one that sets the theme before the first paint and the ones that restore the sidebar's state. |
| `'wasm-unsafe-eval'` in `script-src` | Pagefind, the search, is WebAssembly. It runs in a worker; when the worker cannot start, it runs on the page, and the search finds nothing without this. |
| `'unsafe-inline'` in `style-src` | The `style` attributes Starlight and the code blocks write (icon sizes, token colours), and the `<style>` elements of the dev server. |
| `data:` in `img-src` | The icons the stylesheets hold as `data:` URLs. |

`base-uri` and `form-action` are the two directives `default-src` does not cover.

What the policy does not do:

- It does not stop an inline script from running: `'unsafe-inline'` is there. It stops what such a
  script would load or send to another host.
- A worker takes its policy from its own response, not from the page, and Pages sends none. Firefox
  and WebKit follow that, so Pagefind's worker is outside the policy there; Chrome applies the page's
  policy to what a worker fetches. `check-dist.mjs` reads the worker's file like any other.
- A `meta` element cannot carry `frame-ancestors`, `sandbox` or a report address. Those need a header.

After a change to the policy, or an update of Starlight, Astro or Pagefind, build, run
`npm run preview` and look at the site in a browser with the console open: every page in the dark
and the light theme, the theme select, the menu at a narrow width, and a search. The console must
show no violation. Do the same once with `npm run dev`.

## The look in numbers

The contrast ratios of the look's text and background pairs are written in the designer's ruling, which
the stylesheet follows; they are not repeated here.

## One-time setting on GitHub

In the repository's Settings, Pages, set Source to GitHub Actions. Until then only the deploy job of
`.github/workflows/pages.yml` fails; the build job still proves the site on every run.
