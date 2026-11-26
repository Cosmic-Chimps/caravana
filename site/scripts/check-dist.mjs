// Proves, after every build, that the built site asks no other host for anything.
//
// Caravana's own console loads nothing from third parties, and its documentation
// follows suit: no analytics, no CDN script, no external font, no remote image.
// Starlight's defaults already behave that way. This script is what keeps it
// true: a dependency update, a stylesheet edit or a Markdown page that starts
// reaching for another host fails the build here, instead of reaching a reader.
//
// Own host is the host of `site` in site.config.mjs. A link a reader may follow
// (the href of an <a>) or text may name any host; nothing the browser fetches on
// its own may.
//
// The browser enforces the same thing at run time, through the
// Content-Security-Policy that astro.config.mjs puts in every page. This script
// checks that each page carries it, before anything it loads, and that it allows
// no other host.

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { base, site } from '../site.config.mjs';

const DIST = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist');
const OWN_HOST = new URL(site).host;

// URL strings that appear in the built JavaScript and are not requests. Each was
// found by reading what the build produced; anything not listed fails the build,
// so a dependency that starts calling home is noticed. A new entry needs a reason.
const ALLOWED_JS_URLS = new Map([
  ['http://www.w3.org/2000/svg', 'the SVG XML namespace, the argument of createElementNS, never fetched'],
  ['https://p', 'a dummy origin that new URL(path, base) needs to resolve a pathname, never fetched'],
  ['https://example.com', 'a dummy origin in pagefind to parse a relative URL, never fetched'],
  ['https://markjs.io/', 'the homepage in the licence comment of mark.js, in pagefind-highlight.js'],
  ['https://git.io/vwTVl', 'a link in a comment of mark.js, in pagefind-highlight.js'],
  // The credits of the translations of Pagefind's search interface (a string beside
  // each language), in ui-core and pagefind-ui and pagefind-component-ui.
  ['https://github.com/pvillaverde', 'translator credit in a Pagefind UI language string'],
  ['https://github.com/dallyh', 'translator credit in a Pagefind UI language string'],
  ['https://github.com/diomed', 'translator credit in a Pagefind UI language string'],
  ['https://github.com/seokho-son', 'translator credit in a Pagefind UI language string'],
  ['https://yekta.dev', 'translator credit in a Pagefind UI language string'],
  ['https://goudarzjafari.com', 'translator credit in a Pagefind UI language string'],
  ['https://harrymkt.github.io', 'translator credit in a Pagefind UI language string'],
]);

// Hosts that may be named in the built scripts, stylesheets and SVG files. The
// exact list above only sees a full http(s) URL in JavaScript; this one also
// sees what it cannot: a protocol-relative "//host/x", a URL in an SVG file,
// and one in a CSS function other than url(), such as image-set(). Every host
// that follows a quote, a bracket, an equals sign or a space, with or without
// a scheme, must be here, each with the reason it is not a request.
const ALLOWED_HOSTS = new Map([
  ['www.w3.org', 'XML namespaces (SVG, XHTML), in scripts, in SVG files and in data: URIs of the stylesheets'],
  ['bugzilla.mozilla.org', 'a link to a browser bug in a comment of Starlight\'s stylesheet'],
  ['bugs.webkit.org', 'a link to a browser bug in a comment of Starlight\'s stylesheet'],
  ['markjs.io', 'the homepage in the licence comment of mark.js, in pagefind-highlight.js'],
  ['git.io', 'a link in a comment of mark.js, in pagefind-highlight.js'],
  ['example.com', 'a dummy origin in pagefind to parse a relative URL, never fetched'],
]);
const HOST_AFTER_DELIMITER = /["'`(=\s](?:https?:)?\/\/([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)/g;

const problems = [];
const problem = (file, message) => problems.push(`${path.relative(DIST, file)}: ${message}`);

const seenHosts = new Set();

/** Every host named after a delimiter in a script, a stylesheet or an SVG file must be the site's own or allowed. */
function checkHosts(file, text) {
  const found = new Set();
  for (const m of text.matchAll(HOST_AFTER_DELIMITER)) found.add(m[1].toLowerCase());
  for (const host of found) {
    seenHosts.add(host);
    if (host === OWN_HOST || ALLOWED_HOSTS.has(host)) continue;
    problem(file, `names the host ${host}, which is not in the allowed hosts of scripts/check-dist.mjs`);
  }
}

function walk(dir, found = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(full, found);
    else found.push(full);
  }
  return found;
}

/** The host of a URL that is not the site's own, or null when it is relative, own, or not a URL. */
function otherHost(value) {
  const v = value.trim();
  if (!/^(https?:)?\/\//i.test(v)) return null;
  try {
    const host = new URL(v, 'https://relative.invalid').host;
    return host === OWN_HOST ? null : host;
  } catch {
    return v;
  }
}

/** The external URLs a piece of CSS fetches: url(...) and @import. data: and relative URLs are fine. */
function cssUrls(css) {
  const urls = [];
  for (const m of css.matchAll(/url\(\s*(['"]?)(.*?)\1\s*\)/gi)) urls.push(m[2]);
  for (const m of css.matchAll(/@import\s+(?:url\(\s*)?['"]?([^'")\s;]+)/gi)) urls.push(m[1]);
  return urls.filter((u) => otherHost(u) !== null);
}

const TAG = /<([a-zA-Z][\w:-]*)((?:\s+[^\s"'<>\/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'<>=`]+))?)*)\s*\/?>/g;
const ATTR = /([^\s"'<>\/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'<>=`]+)))?/g;
// Attributes the browser fetches from, on whatever element carries them.
const FETCHED = new Set(['src', 'srcset', 'poster', 'data', 'action', 'xlink:href']);

const externalLinks = new Map(); // host -> count, for <a href>

// The picture of a link preview. A chat or social app fetches it from the
// address in the page, so it must be the site's own, absolute, and a file that
// this build really holds at that path: a changed base or a renamed file
// would otherwise bring back the empty box, with no build noticing.
const PREVIEW_IMAGE = new Set(['og:image', 'twitter:image']);
const BASE_PATH = base.replace(/\/+$/, '');

function checkPreviewImage(file, which, value) {
  let url;
  try {
    url = new URL(value);
  } catch {
    problem(file, `${which} is "${value}", which is not an absolute URL`);
    return;
  }
  if (url.host !== OWN_HOST) {
    problem(file, `${which} points at ${value}, which is not on ${OWN_HOST}`);
  } else if (BASE_PATH && !url.pathname.startsWith(`${BASE_PATH}/`)) {
    problem(file, `${which} is ${value}, which is not under ${BASE_PATH}/`);
  } else if (!existsSync(path.join(DIST, decodeURIComponent(url.pathname.slice(BASE_PATH.length))))) {
    problem(file, `${which} is ${value}, and dist/ holds no such file`);
  }
}

// An image a page shows from the site's own host (an <img src> or a <source srcset>,
// relative to the page or absolute) must be a file this build holds, under the base
// path: the pages show the images of docs/images/, which collect.mjs copies, and a
// missing copy would otherwise be an empty box that no build noticed.
function checkOwnImage(file, tag, attr, value) {
  let url;
  try {
    url = new URL(value.trim(), new URL(path.relative(DIST, file).split(path.sep).join('/'), `https://${OWN_HOST}${BASE_PATH}/`));
  } catch {
    problem(file, `<${tag} ${attr}> is "${value}", which is not an address`);
    return;
  }
  if (url.host !== OWN_HOST || !/^https?:$/.test(url.protocol)) return; // another host is judged elsewhere, data: needs no file
  if (BASE_PATH && !url.pathname.startsWith(`${BASE_PATH}/`)) {
    problem(file, `<${tag} ${attr}> is ${value}, which is not under ${BASE_PATH}/`);
  } else if (!existsSync(path.join(DIST, decodeURIComponent(url.pathname.slice(BASE_PATH.length))))) {
    problem(file, `<${tag} ${attr}> is ${value}, and dist/ holds no such file`);
  }
}

// What a source of the Content-Security-Policy may be: one of these keywords, a
// hash, a nonce, or data:. Everything else is refused by name: a host or another
// scheme would let the browser ask another host, and so would a keyword such as
// 'strict-dynamic', which lets a trusted script load others from anywhere.
const POLICY_SOURCE = /^(?:'self'|'none'|'unsafe-inline'|'wasm-unsafe-eval'|'sha(?:256|384|512)-[A-Za-z0-9+\/_-]+={0,2}'|'nonce-[A-Za-z0-9+\/_-]+={0,2}'|data:)$/;

/** The page's policy must exist, name no other host, and come before anything the page loads. */
function checkPolicy(file, policyAt, policy, firstLoadAt) {
  if (policy === null) {
    problem(file, 'has no <meta http-equiv="Content-Security-Policy">: the browser would not refuse a request to another host');
    return;
  }
  if (firstLoadAt !== -1 && firstLoadAt < policyAt) {
    problem(file, 'a script or a stylesheet comes before the Content-Security-Policy element, so the policy does not cover it');
  }
  const directives = policy.split(';').map((d) => d.trim().split(/\s+/)).filter((d) => d[0] !== '');
  if (!directives.some(([name, ...sources]) => name === 'default-src' && sources.join(' ') === "'self'")) {
    problem(file, "the Content-Security-Policy does not start from default-src 'self'");
  }
  for (const [name, ...sources] of directives) {
    for (const source of sources) {
      if (!POLICY_SOURCE.test(source)) problem(file, `the Content-Security-Policy allows ${source} in ${name}`);
    }
  }
}

function checkHtml(file, html) {
  const scripts = [];
  const styles = [];
  const rest = html
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi, (_, attrs, body) => {
      scripts.push({ attrs, body });
      return `<script${attrs}></script>`;
    })
    .replace(/<style\b[^>]*>([\s\S]*?)<\/style>/gi, (all, body) => {
      styles.push(body);
      return '<style></style>';
    });

  for (const { body } of scripts) {
    for (const m of body.matchAll(/https?:\/\/[^\s"'`)<>\\]+/g)) {
      if (m[0].startsWith('http://www.w3.org/') || otherHost(m[0]) === null) continue;
      problem(file, `an inline <script> holds the URL ${m[0]}`);
    }
  }
  for (const css of styles) {
    for (const u of cssUrls(css)) problem(file, `a <style> element fetches ${u}`);
  }

  let policyAt = -1;
  let policy = null;
  let firstLoadAt = -1;
  for (const tag of rest.matchAll(TAG)) {
    const name = tag[1].toLowerCase();
    const attrs = new Map();
    for (const a of tag[2].matchAll(ATTR)) attrs.set(a[1].toLowerCase(), a[2] ?? a[3] ?? a[4] ?? '');
    if (name === 'meta' && /^content-security-policy$/i.test(attrs.get('http-equiv') ?? '')) {
      if (policy === null) {
        policyAt = tag.index;
        policy = attrs.get('content') ?? '';
      }
    } else if (firstLoadAt === -1 && (name === 'script' || name === 'style' || (name === 'link' && /\bstylesheet\b/i.test(attrs.get('rel') ?? '')))) {
      firstLoadAt = tag.index;
    }
    for (const [attr, value] of attrs) {
      if (attr === 'xmlns' || attr.startsWith('xmlns:')) continue;
      if (attr === 'href') {
        const host = otherHost(value);
        if (host === null) continue;
        if (name === 'a') externalLinks.set(host, (externalLinks.get(host) ?? 0) + 1);
        else problem(file, `<${name} href> points at ${value}`);
      } else if (attr === 'srcset') {
        for (const candidate of value.split(',')) {
          const url = candidate.trim().split(/\s+/)[0];
          if (otherHost(url) !== null) problem(file, `<${name} srcset> holds ${url}`);
          else if (url && (name === 'source' || name === 'img')) checkOwnImage(file, name, 'srcset', url);
        }
      } else if (FETCHED.has(attr)) {
        if (otherHost(value) !== null) problem(file, `<${name} ${attr}> points at ${value}`);
        else if (attr === 'src' && name === 'img' && value) checkOwnImage(file, name, attr, value);
      } else if (attr === 'style') {
        for (const u of cssUrls(value)) problem(file, `an inline style on <${name}> fetches ${u}`);
      } else if (name === 'meta' && attr === 'content' && PREVIEW_IMAGE.has(attrs.get('property') ?? attrs.get('name') ?? '')) {
        checkPreviewImage(file, attrs.get('property') ?? attrs.get('name'), value);
      } else if (name === 'meta' && attr === 'content' && /^refresh$/i.test(attrs.get('http-equiv') ?? '')) {
        const target = /url\s*=\s*['"]?([^'";\s]+)/i.exec(value)?.[1];
        if (target && otherHost(target) !== null) problem(file, `<meta http-equiv="refresh"> goes to ${target}`);
      }
    }
  }
  checkPolicy(file, policyAt, policy, firstLoadAt);
}

function checkJs(file, js) {
  for (const url of new Set(js.match(/https?:\/\/[^\s"'`)<>\\$,;{}]+/g) ?? [])) {
    if (otherHost(url) === null || ALLOWED_JS_URLS.has(url)) continue;
    problem(file, `the script holds the URL ${url}, which is not in the allowed list of scripts/check-dist.mjs`);
  }
}

if (!existsSync(DIST) || !statSync(DIST).isDirectory()) {
  console.error('check-dist: there is no dist/; run astro build first');
  process.exit(1);
}
if (!existsSync(path.join(DIST, 'index.html'))) problems.push('dist/ has no index.html');
if (!existsSync(path.join(DIST, 'pagefind')) || !statSync(path.join(DIST, 'pagefind')).isDirectory()) {
  problems.push('dist/ has no pagefind/ directory: the search was not built');
}

const counts = { html: 0, css: 0, js: 0, svg: 0 };
const seenJsUrls = new Set();
for (const file of walk(DIST)) {
  const ext = path.extname(file).toLowerCase();
  if (ext === '.html') {
    counts.html++;
    checkHtml(file, readFileSync(file, 'utf8'));
  } else if (ext === '.css') {
    counts.css++;
    const css = readFileSync(file, 'utf8');
    for (const u of cssUrls(css)) problem(file, `the stylesheet fetches ${u}`);
    checkHosts(file, css);
  } else if (ext === '.js' || ext === '.mjs') {
    counts.js++;
    const js = readFileSync(file, 'utf8');
    checkJs(file, js);
    checkHosts(file, js);
    for (const url of js.match(/https?:\/\/[^\s"'`)<>\\$,;{}]+/g) ?? []) seenJsUrls.add(url);
  } else if (ext === '.svg') {
    // An SVG shown as an image cannot fetch, but one inlined or embedded later could.
    counts.svg++;
    checkHosts(file, readFileSync(file, 'utf8'));
  }
}

// An allowed URL that no longer appears is a stale entry: drop it, so the list stays honest.
for (const url of ALLOWED_JS_URLS.keys()) {
  if (!seenJsUrls.has(url)) console.warn(`check-dist: note: ${url} is in the allowed list but no script holds it any more`);
}
for (const host of ALLOWED_HOSTS.keys()) {
  if (!seenHosts.has(host)) console.warn(`check-dist: note: ${host} is in the allowed hosts but no file names it any more`);
}

if (problems.length > 0) {
  for (const line of problems) console.error(line);
  console.error(`check-dist: ${problems.length} problem${problems.length === 1 ? '' : 's'}: the site would ask another host for something, or the browser would not refuse it`);
  process.exit(1);
}

const total = [...externalLinks.values()].reduce((a, b) => a + b, 0);
const hosts = [...externalLinks].sort((a, b) => b[1] - a[1]).map(([host, n]) => `${host} ${n}`).join(', ');
console.log(
  `check-dist: ${counts.html + counts.css + counts.js + counts.svg} files read (${counts.html} html, ${counts.css} css, ${counts.js} js, ${counts.svg} svg), ` +
    `nothing requested from another host; ${total} external <a> links (${hosts || 'none'})`,
);
