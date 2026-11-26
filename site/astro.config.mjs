import { existsSync, readFileSync } from 'node:fs';
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import { base, repository, site } from './site.config.mjs';
import { visibleSidebar } from './sidebar.mjs';

// No custom components, no layouts, no head scripts: Starlight's defaults plus
// one stylesheet and the `head` entries below: the policy, then the link preview
// (meta and link elements only). The site asks no other host for anything:
// scripts/check-dist.mjs proves it after every build, and the policy has the
// browser refuse it.

// GitHub Pages sends no header of ours, so the Content-Security-Policy travels in
// each page, as a meta element. Everything comes from the site itself; each
// exception is something a browser refused under a bare default-src 'self'.
const contentSecurityPolicy = [
  "default-src 'self'",
  // 'unsafe-inline': Starlight's inline scripts (the theme before first paint, the
  // sidebar's state). 'wasm-unsafe-eval': Pagefind's WebAssembly, when its worker
  // cannot start and the search runs on the page.
  "script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'",
  // The style attributes Starlight and the code blocks write (icon sizes, token colours).
  "style-src 'self' 'unsafe-inline'",
  // The icons the stylesheets hold as data: URLs.
  "img-src 'self' data:",
  // The two that default-src does not cover: no <base>, and no form that posts elsewhere.
  "base-uri 'none'",
  "form-action 'self'",
].join('; ');

// What a chat or social app shows beside a link to the site. Starlight writes
// the title and the description but no picture, and its favicon is an SVG,
// which those apps do not draw: without these the preview has an empty box
// where the picture goes and no icon. The picture's address must be absolute.
const linkPreview = [
  { tag: 'meta', attrs: { property: 'og:image', content: `${site}${base}/og.png` } },
  { tag: 'meta', attrs: { property: 'og:image:type', content: 'image/png' } },
  { tag: 'meta', attrs: { property: 'og:image:width', content: '1200' } },
  { tag: 'meta', attrs: { property: 'og:image:height', content: '630' } },
  { tag: 'meta', attrs: { property: 'og:image:alt', content: 'Caravana documentation: run apps on your own servers' } },
  { tag: 'meta', attrs: { name: 'twitter:image', content: `${site}${base}/og.png` } },
  { tag: 'link', attrs: { rel: 'icon', type: 'image/png', sizes: '32x32', href: `${base}/favicon-32.png` } },
  { tag: 'link', attrs: { rel: 'apple-touch-icon', sizes: '180x180', href: `${base}/apple-touch-icon.png` } },
];
// A draft build (`node scripts/collect.mjs --allow-missing`) leaves out the pages that are not
// written yet, and with them their sidebar entries. A normal build leaves no such file.
const skippedFile = new URL('./.draft-skipped.json', import.meta.url);
const skipped = existsSync(skippedFile) ? JSON.parse(readFileSync(skippedFile, 'utf8')) : [];

export default defineConfig({
  site,
  base,
  trailingSlash: 'always',
  integrations: [
    starlight({
      title: 'Caravana',
      description:
        'Documentation for Caravana, a self-hosted platform for running apps on your own servers.',
      // The mark alone; the site title draws the wordmark beside it (src/styles/caravana.css).
      logo: {
        light: './src/assets/mark-on-light.svg',
        dark: './src/assets/mark-on-dark.svg',
        alt: '',
        replacesTitle: false,
      },
      favicon: '/favicon.svg',
      // The policy first. Starlight puts an http-equiv meta right after the charset
      // and the viewport, before the first script and stylesheet, which is where a
      // policy has to be. One `head` key: a second would silently replace this one.
      head: [{ tag: 'meta', attrs: { 'http-equiv': 'Content-Security-Policy', content: contentSecurityPolicy } }, ...linkPreview],
      customCss: ['./src/styles/caravana.css'],
      social: [{ icon: 'github', label: 'GitHub', href: repository }],
      sidebar: visibleSidebar(skipped),
      plugins: [starlightLinksValidator()],
      // Code is content, so it keeps syntax colour, framed like a console panel: no shadow, since in
      // the console a shadow only means "floating above the page".
      expressiveCode: {
        themes: ['github-dark-dimmed', 'github-light'],
        styleOverrides: {
          borderRadius: '6px',
          borderColor: 'var(--sl-color-hairline)',
          codeFontFamily: "'IBM Plex Mono', ui-monospace, monospace",
          uiFontFamily: "'IBM Plex Sans', ui-sans-serif, sans-serif",
          codeBackground: ({ theme }) => (theme.type === 'dark' ? '#131518' : '#fcfdff'),
          frames: { shadowColor: 'transparent' },
        },
      },
      lastUpdated: false,
      credits: false,
      pagefind: true,
    }),
  ],
});
