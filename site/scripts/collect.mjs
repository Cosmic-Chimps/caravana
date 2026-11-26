// Generates site/src/content/docs/ from the repository's Markdown.
//
// The Markdown in the repository is the source of truth: docs/ and CHANGELOG.md
// read well on GitHub as they are, and this script turns them into the pages of
// the site at build time, so no copy of a page is ever committed and nothing
// under site/ is edited to change a page. One source file is one page; pages.mjs
// is the table. It writes only inside site/: the pages, the fonts and their
// licence, and the images the pages show (docs/images/ is where they live, so
// that a page that loads one asks nothing of another host).
//
// It is strict on purpose. A link in a source that would not work on the site, a
// page missing from the sidebar, a screenshot in a shape it does not know: each
// is an error that names the file and line, and the build stops, so a mistake in
// the documentation never reaches the published site in silence.
//
// `node scripts/collect.mjs --allow-missing` is for local work while pages are
// still being written (`npm run build:draft`): a page whose source does not
// exist yet is skipped, with its sidebar entry, a link to it goes to GitHub, and
// an image that does not exist yet becomes a one-pixel placeholder. Each is
// printed. The normal build, and CI, never use it.
//
// The link scan reads each source line by line (outside fenced code and code
// spans; inline links and images, reference definitions and HTML href/src/srcset),
// and slug() follows GitHub's rule, so an anchor that works on GitHub works here.
// Like any line-by-line scan, a code span that wraps onto a second line is not
// blanked. A link-like text in one usually fails loudly, as a link that does not
// exist; the quiet case would be a wrapped span holding `](path)` to a file that
// exists, whose path would be rewritten inside the code. Keep such a span on one line.

import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { base, branch, repository } from '../site.config.mjs';
import { pages as pageTable } from '../pages.mjs';
import { sidebarSlugs } from '../sidebar.mjs';

const ALLOW_MISSING = process.argv.includes('--allow-missing');

const SITE_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const ROOT = path.resolve(SITE_DIR, '..');
const OUT = path.join(SITE_DIR, 'src', 'content', 'docs');
// The console's own fonts: IBM Plex Sans and Mono, and their licence.
const FONTS_IN = path.join(ROOT, 'internal', 'ui', 'static', 'fonts');
const FONTS_OUT = path.join(SITE_DIR, 'src', 'fonts');
const LICENSES_OUT = path.join(SITE_DIR, 'public', 'licenses', 'fonts');
// The images the pages show are copied from here to public/images/ (generated, ignored).
const IMAGES_IN = 'docs/images/';
const IMAGES_OUT = path.join(SITE_DIR, 'public', 'images');
// The slugs a draft build skipped, which astro.config.mjs reads to leave them out of the sidebar.
const SKIPPED_FILE = path.join(SITE_DIR, '.draft-skipped.json');
// What stands for an image that does not exist yet in a draft build: a transparent pixel.
const PLACEHOLDER_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
  'base64',
);

const errors = [];
const notes = []; // what a draft build (--allow-missing) skipped or replaced, printed at the end
const fail = (where, message) => errors.push(`${where}: ${message}`);

// ---------------------------------------------------------------------------
// Reading Markdown: the same rules as check-links.py.

const FENCE = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const CODE_SPAN = /(?<!`)(`+)(?!`)(.+?)(?<!`)\1(?!`)/g;
const INLINE_LINK = /\]\(\s*(?:<([^>]*)>|((?:[^\s()]|\([^\s()]*\))*))/dg;
const REF_DEF = /^[ >]{0,3}\[[^\]]+\]:\s*(?:<([^>]*)>|(\S+))/dg;
const HTML_ATTR = /\b(href|srcset|src)\s*=\s*(?:"([^"]*)"|'([^']*)')/dg;
// No SVG: one served from the site's own address runs the script it holds when opened directly.
const IMAGE_FILE = /\.(?:png|webp|jpe?g|gif|avif)$/i;
const ATX = /^ {0,3}(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$/;
const SETEXT = /^ {0,3}(?:=+|-+)[ \t]*$/;
const SCHEME = /^[a-zA-Z][a-zA-Z0-9+.-]*:/;

/** The lines of a file: number, raw text, whether it is fenced code, and the text with code spans blanked. */
function scanLines(text) {
  const lines = text.split(/\r?\n/);
  if (lines.at(-1) === '') lines.pop();
  const out = [];
  let fence = null;
  lines.forEach((raw, i) => {
    const m = FENCE.exec(raw);
    let code = true;
    if (fence) {
      if (m && m[1][0] === fence[0] && m[1].length >= fence.length && !m[2].trim()) fence = null;
    } else if (m && !(m[1][0] === '`' && m[2].includes('`'))) {
      fence = m[1];
    } else {
      code = false;
    }
    out.push({
      n: i + 1,
      raw,
      code,
      blanked: code ? raw : raw.replace(CODE_SPAN, (s) => ' '.repeat(s.length)),
    });
  });
  return out;
}

/** GitHub's anchor for a heading: rendered text, lowercased, punctuation dropped, spaces to hyphens. */
function slug(heading) {
  const parts = [];
  heading.split(CODE_SPAN).forEach((piece, i) => {
    if (i % 3 === 2) {
      parts.push(piece); // the content of a code span: kept as written
      return;
    }
    if (i % 3 === 1) return; // the backtick run
    piece = piece.replace(/!?\[([^\]]*)\]\([^)]*\)|\[([^\]]*)\]\[[^\]]*\]/g, (_, a, b) => a || b || '');
    piece = piece.replace(/<[^>]+>|\*/g, '');
    parts.push(piece.replace(/(?<![\p{L}\p{N}_])_+(?=\S)|(?<=\S)_+(?![\p{L}\p{N}_])/gu, ''));
  });
  const text = parts.join('').trim().toLowerCase();
  return [...text]
    .filter((c) => /[\p{L}\p{N}\p{M}]/u.test(c) || c === ' ' || c === '-' || c === '_')
    .join('')
    .replaceAll(' ', '-');
}

/** A heading's id given the ids already used, with GitHub's -1, -2 rule for repeats. */
function uniqueId(heading, seen) {
  const id = slug(heading);
  const count = seen.get(id) ?? 0;
  seen.set(id, count + 1);
  return count === 0 ? id : `${id}-${count}`;
}

/** A title with its inline Markdown removed: code spans, emphasis and links keep their text only. */
function plainText(text) {
  return text
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/[*]/g, '')
    .trim();
}

const sources = new Map();

/** A source file: its lines and its headings, with the ids GitHub gives them. */
function load(rel) {
  if (sources.has(rel)) return sources.get(rel);
  const abs = path.join(ROOT, rel);
  const lines = scanLines(readFileSync(abs, 'utf8'));
  const headings = [];
  const setext = [];
  const seen = new Map();
  let prev = null;
  for (const line of lines) {
    if (line.code) {
      prev = null;
      continue;
    }
    const m = ATX.exec(line.raw);
    if (m) {
      headings.push({ n: line.n, level: m[1].length, text: m[2], id: uniqueId(m[2], seen) });
    } else if (
      prev &&
      SETEXT.test(line.raw) &&
      !/^\s*([#>|*+-]|\d+[.)])/.test(prev.raw)
    ) {
      setext.push(line.n); // only a problem in a source that becomes a page
    }
    prev = line.raw.trim() ? line : null;
  }
  const src = { rel, abs, lines, headings, setext };
  sources.set(rel, src);
  return src;
}

/** The first line must be `# Title`; docs/ carries no frontmatter. */
function titleOf(src) {
  const first = src.lines[0]?.raw ?? '';
  if (first.startsWith('---')) {
    fail(`${src.rel}:1`, 'starts with frontmatter; the sources carry none, the site writes it');
    return '';
  }
  const m = /^# +(.*?)\s*$/.exec(first);
  if (!m) {
    fail(`${src.rel}:1`, `does not start with a "# Title" line`);
    return '';
  }
  return plainText(m[1]);
}

// ---------------------------------------------------------------------------
// The pages. A page is one source file; its body is the source without its `# ` title.

const pages = [];
const skipped = []; // slugs of the pages whose source is missing, in a draft build
const pageSources = new Set(pageTable.map(([, source]) => source));

/** The lines of a segment: [source, from line, to line (inclusive)]. */
function segment(src, from, to) {
  return { src, lines: src.lines.filter((l) => l.n >= from && l.n <= to) };
}

function buildPages() {
  const slugs = new Set();
  const sourcesSeen = new Set();
  for (const [slug, rel, options] of pageTable) {
    if (slugs.has(slug)) fail('site/pages.mjs', `the slug "${slug}" is listed twice`);
    if (sourcesSeen.has(rel)) fail('site/pages.mjs', `the source ${rel} is the page of two slugs`);
    slugs.add(slug);
    sourcesSeen.add(rel);
    if (!rel.endsWith('.md')) {
      fail('site/pages.mjs', `${slug}: the source ${rel} is not a Markdown file`);
      continue;
    }
    if (!existsSync(path.join(ROOT, rel))) {
      if (ALLOW_MISSING) {
        skipped.push(slug);
        notes.push(`skipped the page "${slug}": ${rel} does not exist yet`);
      } else {
        fail('site/pages.mjs', `the page "${slug}" has no source: ${rel} does not exist`);
      }
      continue;
    }
    const src = load(rel);
    const title = titleOf(src);
    pages.push({
      route: slug === 'index' ? '' : slug,
      slug,
      title: options?.title ?? title,
      edit: rel,
      // Starlight writes "<page> | <site>" in the browser tab: the home page gets a tab title of its own.
      tabTitle: slug === 'index' ? 'Caravana documentation' : undefined,
      segments: [segment(src, 2, src.lines.length)],
    });
  }
}

buildPages();

// Which page holds each source file, and the id each heading has on its page.
const pageOfSource = new Map();
const headingIds = new Map(); // `${rel}:${line}` -> id on the page
for (const page of pages) {
  const seen = new Map();
  for (const seg of page.segments) {
    if (!pageOfSource.has(seg.src.rel)) {
      for (const n of seg.src.setext) {
        fail(`${seg.src.rel}:${n}`, 'a Markdown heading underlined with = or - is not supported here; write it with #');
      }
    }
    pageOfSource.set(seg.src.rel, page);
    for (const line of seg.lines) {
      if (line.code) continue;
      const m = ATX.exec(line.raw);
      if (!m) continue;
      const level = m[1].length;
      if (level === 1) fail(`${seg.src.rel}:${line.n}`, 'a "# " heading inside a page: a page has one title, which the site writes');
      headingIds.set(`${seg.src.rel}:${line.n}`, uniqueId(m[2], seen));
    }
  }
}

// ---------------------------------------------------------------------------
// Links.

const stats = { toPages: 0, toRepository: 0 };
const images = new Set(); // the files of docs/images/ that a page references, as paths from the repository root
const pageHref = (page) => (page.route ? `${base}/${page.route}/` : `${base}/`);
const encodePath = (rel) => rel.split('/').map(encodeURIComponent).join('/');

/** The link to a file or directory of the repository on GitHub. */
function repositoryHref(rel, isDir, anchor) {
  stats.toRepository++;
  const kind = isDir ? 'tree' : 'blob';
  const where = rel ? `${repository}/${kind}/${branch}/${encodePath(rel)}` : `${repository}/${kind}/${branch}`;
  return anchor ? `${where}#${anchor}` : where;
}

/** The link to a heading on a page: `#id` on the page itself, or the page's address and `#id`. */
function pageLink(page, here, key) {
  stats.toPages++;
  const id = key ? headingIds.get(key) : undefined;
  if (page === here) return id ? `#${id}` : pageHref(page);
  return id ? `${pageHref(page)}#${id}` : pageHref(page);
}

/** The address on the site of an image of docs/images/, which is copied there. */
function imageHref(rel, anchor) {
  images.add(rel);
  const href = `${base}/images/${encodePath(rel.slice(IMAGES_IN.length))}`;
  return anchor ? `${href}#${anchor}` : href;
}

/**
 * The new target of a link, null to leave it alone, or an Error. `embedded` is an image the page
 * shows (`![]()`, src, srcset); `srcset` is the attribute of that name.
 */
function resolve(src, here, target, { embedded = false, srcset = false } = {}) {
  if (!target || SCHEME.test(target)) return null;
  if (srcset && /[,\s]/.test(target)) {
    return new Error('a srcset here holds one address and no descriptor (no comma, no 2x or 640w): one image for each <source>');
  }
  const hash = target.indexOf('#');
  const rawPath = hash < 0 ? target : target.slice(0, hash);
  const anchorRaw = hash < 0 ? '' : target.slice(hash + 1);
  let filePart = rawPath;
  let anchor = anchorRaw;
  try {
    filePart = decodeURIComponent(rawPath);
    anchor = decodeURIComponent(anchorRaw);
  } catch {
    /* keep it as written */
  }
  anchor = anchor.toLowerCase();

  const dest = !filePart
    ? src.abs
    : path.normalize(filePart.startsWith('/') ? path.join(ROOT, filePart) : path.join(path.dirname(src.abs), filePart));
  const rel = path.relative(ROOT, dest).split(path.sep).join('/');
  if (rel.startsWith('..')) return new Error(`outside the repository`);
  if (!existsSync(dest)) {
    if (ALLOW_MISSING && rel.startsWith(IMAGES_IN) && IMAGE_FILE.test(rel)) {
      return imageHref(rel, anchorRaw); // a placeholder is written for it, and noted, at the end
    }
    if (ALLOW_MISSING && pageSources.has(rel)) {
      // A page that is not written yet: GitHub is the only place it could be.
      notes.push(`${src.rel}: a link to ${rel}, which does not exist yet, goes to GitHub`);
      return repositoryHref(rel, false, anchorRaw);
    }
    return new Error(`no such file ${rel}`);
  }
  const isDir = statSync(dest).isDirectory();
  // An image: shown from the site's own copy. One that is not under docs/images/ is not copied, so a
  // page cannot show it; a plain link to one goes to GitHub, like any other file.
  if (!isDir && IMAGE_FILE.test(rel) && rel.startsWith(IMAGES_IN)) return imageHref(rel, anchorRaw);
  if (embedded) {
    return new Error(`${rel} is shown by a page, and the images a page shows are PNG, WebP, JPEG, GIF or AVIF files under ${IMAGES_IN}`);
  }
  const isMarkdown = !isDir && dest.endsWith('.md');
  // A Markdown file's headings are read only when the link has an anchor to check.
  const target_ = isMarkdown && anchor ? load(rel) : null;
  const heading = anchor && target_ ? target_.headings.find((h) => h.id === anchor) : null;
  if (anchor && target_ && !heading) return new Error(`no heading for #${anchorRaw} in ${rel}`);

  // A page's source.
  const page = pageOfSource.get(rel);
  if (page) return pageLink(page, here, heading ? `${rel}:${heading.n}` : null);

  // Anything else that exists: GitHub shows it.
  return repositoryHref(rel, isDir, anchorRaw);
}

/** Whether the `](` that starts at `close` ends the alt text of an image: the `[` that opens it follows a `!`. */
function endsImageAlt(text, close) {
  let depth = 0;
  for (let i = close - 1; i >= 0; i--) {
    if (text[i] === ']') depth++;
    else if (text[i] === '[') {
      if (depth === 0) return text[i - 1] === '!';
      depth--;
    }
  }
  return false; // the alt text wraps onto another line: read as a link, like check-links.py
}

/** The place of each link target in a line, from the same patterns as check-links.py, and how the target is used. */
function linkTargets(line) {
  const found = [];
  // The first group that took part holds the target (the `<...>` form or the plain one); an HTML attribute's name comes first.
  for (const [pattern, first] of [[INLINE_LINK, 1], [REF_DEF, 1], [HTML_ATTR, 2]]) {
    for (const m of line.blanked.matchAll(pattern)) {
      const group = m.indices.findIndex((range, i) => i >= first && range && m[i]);
      if (group < 1) continue;
      const attr = pattern === HTML_ATTR ? m[1] : '';
      const embedded = pattern === INLINE_LINK ? endsImageAlt(line.blanked, m.index) : attr === 'src' || attr === 'srcset';
      found.push({ start: m.indices[group][0], end: m.indices[group][1], target: m[group], use: { embedded, srcset: attr === 'srcset' } });
    }
  }
  return found.sort((a, b) => b.start - a.start);
}

const PICTURE_START = /<picture\b/i;
const PICTURE_END = /<\/picture>/i;
const PICTURE_SHAPE = /^(\s*)<picture>\s*<source\s([^>]*?)\s*\/?>\s*<img\s([^>]*?)\s*\/?>\s*<\/picture>\s*$/i;
const ATTRIBUTE = /([a-zA-Z][\w:-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')/g;

/** The attributes of a tag's inside as a Map, or null when something other than name="value" pairs is in it. */
function attributesOf(inside) {
  const found = new Map();
  const rest = inside.replace(ATTRIBUTE, (_, name, a, b) => {
    found.set(name.toLowerCase(), a ?? b);
    return '';
  });
  return rest.trim() === '' ? found : null;
}

/**
 * A screenshot in the sources is, for GitHub's sake,
 *   <picture><source media="(prefers-color-scheme: dark)" srcset="…-dark.png"><img alt="…" src="…-light.png" width="760"></picture>
 * On the site it follows Starlight's own theme choice rather than the system's, so it becomes two
 * images that the stylesheet shows one at a time (.cv-shot-light, .cv-shot-dark). The alt text is on
 * both: the one that is hidden is not read, so a screen reader hears it once. Any other shape of
 * <picture> is an error rather than something that quietly shows the wrong image.
 */
function rewritePicture(src, page, from, block) {
  const where = `${src.rel}:${from}`;
  const bad = (what) => {
    fail(where, `<picture>: ${what}`);
    return null;
  };
  const m = PICTURE_SHAPE.exec(block.map((l) => l.raw).join(' '));
  if (!m) {
    return bad('must be exactly one <source media="(prefers-color-scheme: dark)" srcset="…"> and one <img alt src width>, nothing else, and nothing else on its lines');
  }
  const source = attributesOf(m[2]);
  const img = attributesOf(m[3]);
  if (!source || ![...source.keys()].every((k) => k === 'media' || k === 'srcset')) {
    return bad('the <source> may have only media and srcset');
  }
  if (!img || ![...img.keys()].every((k) => k === 'alt' || k === 'src' || k === 'width')) {
    return bad('the <img> may have only alt, src and width');
  }
  if ((source.get('media') ?? '').replace(/\s+/g, '') !== '(prefers-color-scheme:dark)') {
    return bad('the <source> must have media="(prefers-color-scheme: dark)"');
  }
  if (!source.get('srcset')) return bad('the <source> has no srcset');
  if (!img.get('src')) return bad('the <img> has no src');
  if (!img.get('alt')?.trim()) return bad('the <img> has no alt text: say what the picture shows');
  if (!/^\d{2,4}$/.test(img.get('width') ?? '')) return bad('the <img> needs a width in pixels, such as width="760"');
  const dark = resolve(src, page, source.get('srcset'), { embedded: true, srcset: true });
  const light = resolve(src, page, img.get('src'), { embedded: true });
  for (const [what, value, result] of [['srcset', source.get('srcset'), dark], ['src', img.get('src'), light]]) {
    if (result instanceof Error) return bad(`${what} ${value}: ${result.message}`);
    if (result === null) return bad(`${what} is not a file of ${IMAGES_IN}`);
  }
  const alt = img.get('alt').replaceAll('"', '&quot;');
  const width = img.get('width');
  const tag = (theme, href) =>
    `<img class="cv-shot-${theme}" alt="${alt}" src="${href}" width="${width}" loading="lazy">`;
  return [`${m[1]}<div class="cv-shot">`, `${m[1]}${tag('light', light)}`, `${m[1]}${tag('dark', dark)}`, `${m[1]}</div>`].join('\n');
}

function renderBody(page) {
  const out = [];
  for (const seg of page.segments) {
    const body = [];
    for (let i = 0; i < seg.lines.length; i++) {
      const line = seg.lines[i];
      if (!line.code && PICTURE_START.test(line.blanked)) {
        let j = i;
        while (j < seg.lines.length && !PICTURE_END.test(seg.lines[j].blanked)) j++;
        if (j === seg.lines.length) {
          fail(`${seg.src.rel}:${line.n}`, 'a <picture> that is not closed');
          break;
        }
        const replaced = rewritePicture(seg.src, page, line.n, seg.lines.slice(i, j + 1));
        if (replaced !== null) body.push(replaced);
        i = j;
        continue;
      }
      let text = line.raw;
      if (!line.code) {
        for (const { start, end, target, use } of linkTargets(line)) {
          const next = resolve(seg.src, page, target, use);
          if (next instanceof Error) {
            fail(`${seg.src.rel}:${line.n}`, `${target}: ${next.message}`);
          } else if (next !== null) {
            text = text.slice(0, start) + next + text.slice(end);
          }
        }
      }
      body.push(text);
    }
    out.push(body.join('\n').replace(/^\n+|\n+$/g, ''));
  }
  return out.join('\n\n');
}

// ---------------------------------------------------------------------------
// The sidebar names every page, and only pages.

const generated = new Set(pages.map((p) => p.slug));
const named = new Set(sidebarSlugs());
for (const slugName of generated) {
  if (!named.has(slugName)) fail('site/sidebar.mjs', `the page "${slugName}" is not in the sidebar`);
}
for (const slugName of named) {
  if (!generated.has(slugName) && !skipped.includes(slugName)) fail('site/sidebar.mjs', `names the page "${slugName}", which was not generated`);
}

// ---------------------------------------------------------------------------
// Write.

const rendered = pages.map((page) => ({ page, body: renderBody(page) }));

if (errors.length > 0) {
  for (const line of errors) console.error(line);
  console.error(`collect: ${errors.length} problem${errors.length === 1 ? '' : 's'}, nothing written`);
  process.exit(1);
}

rmSync(OUT, { recursive: true, force: true });
for (const { page, body } of rendered) {
  const file = path.join(OUT, `${page.slug}.md`);
  mkdirSync(path.dirname(file), { recursive: true });
  const front = [
    '---',
    `title: ${JSON.stringify(page.title)}`,
    `editUrl: ${JSON.stringify(`${repository}/edit/${branch}/${page.edit}`)}`,
    ...(page.tabTitle ? ['head:', '  - tag: title', `    content: ${JSON.stringify(page.tabTitle)}`] : []),
    '---',
    '',
  ];
  writeFileSync(file, `${front.join('\n')}\n${body}\n`);
}

// The fonts the stylesheet names, and their licence, which travels with them.
rmSync(FONTS_OUT, { recursive: true, force: true });
rmSync(path.join(SITE_DIR, 'public', 'licenses'), { recursive: true, force: true });
mkdirSync(FONTS_OUT, { recursive: true });
mkdirSync(LICENSES_OUT, { recursive: true });
let fonts = 0;
for (const name of readdirSync(FONTS_IN)) {
  if (name.endsWith('.woff2')) {
    copyFileSync(path.join(FONTS_IN, name), path.join(FONTS_OUT, name));
    fonts++;
  } else if (/^OFL.*\.txt$/.test(name)) {
    copyFileSync(path.join(FONTS_IN, name), path.join(LICENSES_OUT, name));
  }
}

// The images the pages reference, and only those. public/ also holds committed files, so only images/ is cleared.
rmSync(IMAGES_OUT, { recursive: true, force: true });
for (const rel of images) {
  const copy = path.join(IMAGES_OUT, rel.slice(IMAGES_IN.length));
  mkdirSync(path.dirname(copy), { recursive: true });
  if (existsSync(path.join(ROOT, rel))) {
    copyFileSync(path.join(ROOT, rel), copy);
  } else {
    // Only a draft build gets here: resolve() lets a missing image through only with --allow-missing.
    writeFileSync(copy, PLACEHOLDER_PNG);
    notes.push(`the image ${rel} does not exist yet: a one-pixel placeholder stands for it`);
  }
}

// What a draft build left out, for astro.config.mjs; a normal build leaves no such file.
rmSync(SKIPPED_FILE, { force: true });
if (ALLOW_MISSING) writeFileSync(SKIPPED_FILE, `${JSON.stringify(skipped)}\n`);

for (const note of [...new Set(notes)]) console.warn(`collect: draft: ${note}`);
console.log(
  `collect: ${pages.length} pages written to src/content/docs/` +
    (ALLOW_MISSING ? ` (${skipped.length} skipped as missing: this is a draft)` : '') +
    `, ${stats.toPages} links to pages and ${stats.toRepository} to the repository on GitHub, ${fonts} fonts and ${images.size} images copied`,
);
