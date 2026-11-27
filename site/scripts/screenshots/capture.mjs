// Captures the screens of the documentation from the demo console that run.sh starts
// (internal/ui/docsdemo_test.go), each in the light and the dark theme, into out/raw/.
// optimise.mjs runs after this one.
//
// How a screen is taken:
//   - The browser signs in through the real form, as the demo's owner (DEMO_EMAIL, DEMO_PASSWORD).
//   - The theme is the console's own choice cookie, caravana_theme (feature 028), set to light and
//     then to dark: the same state, the same page, drawn by the server in each theme. Nothing is
//     changed in the page's DOM.
//   - A screen is a path, an optional click-through (`steps`), a selector that says the page is
//     there (`ready`), and a frame: the whole viewport, or the union of the boxes of some
//     selectors plus 16 px of the page around them.
//   - The console's live streams are left to connect: a page is taken when its live indicator
//     says "live" (or has none), so "Reconnecting…" is never in a picture.
//   - localtime.js, the optional script that adds "· 16:15 local" after each UTC time, is not
//     loaded: the pictures show UTC only, and the machine's own zone is not in them.
//     LOCAL_ZONE=Europe/Madrid (an IANA zone) loads it with the browser in that zone, to check how
//     the local times sit in a page; pictures for docs/images are taken without it.
//
// Environment: DEMO_URL, DEMO_EMAIL, DEMO_PASSWORD (required); ONLY=name,name to take some screens;
// LOCAL_ZONE (see above).
// Exits non-zero if any screen failed, naming each, so a UI change that moves a selector is noticed.
import { chromium } from '@playwright/test';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';

const base = process.env.DEMO_URL;
const email = process.env.DEMO_EMAIL;
const password = process.env.DEMO_PASSWORD;
if (!base || !email || !password) throw new Error('DEMO_URL, DEMO_EMAIL and DEMO_PASSWORD must be set');
const only = (process.env.ONLY ?? '').split(',').map((s) => s.trim()).filter(Boolean);
const rawDir = process.env.RAW_DIR ?? 'out/raw';
rmSync(rawDir, { recursive: true, force: true });
mkdirSync(rawDir, { recursive: true });

const VIEWPORT = { width: 1280, height: 800 };
const SCALE = 2;
const THEMES = ['light', 'dark'];
const PAD = 16;

// The ids of the made-up apps.
const WEB = 'shop--production--web';
const WEB_STAGING = 'shop--staging--web';
const API = 'shop--production--api';
const DB = 'shop--production--db';
const BLOG = 'shop--staging--blog';
const SEARCH = 'shop--production--search';
const SHOP = 'project=shop&environment=production';

// ---------------------------------------------------------------------------
// The screens, in the order of the brief. Each entry:
//   path     where to go (the demo's own address is prepended)
//   ready    a selector that must be on the page before it is taken
//   steps    async (page) => {} that clicks through from `path`, then waits for its own result
//   clip     [selectors] the frame is the union of their boxes plus 16 px, or
//   h        the height of the viewport, which is the picture (default 800). The window is
//            made that tall before the page loads, so the sidebar fills it as on a tall screen.
//            FULL=1 sets it to the page's own height (to choose an h).
//   public   true: taken signed out
// ---------------------------------------------------------------------------
const SCREENS = {
  'sign-in': { path: '/login', ready: 'form', public: true, clip: ['main'] },
  'apps-home': { path: '/apps', ready: '.cv-apps', h: 1100 },
  'apps-home-security': {
    path: '/apps', ready: '#needs-security', h: 1100, clip: ['[aria-label="Needs you"]'],
    steps: async (page) => {
      await page.locator('#needs-security > summary .cv-need-text').click();
      await page.locator('#needs-security[open] .cv-need-app').first().waitFor();
    },
  },
  'new-app-image': { path: '/apps/new', ready: 'form', h: 1010 },
  'new-app-compose': { path: '/apps/new?from=compose', ready: 'form', h: 920 },
  'new-app-templates': { path: '/apps/new?from=template', ready: 'main' },
  'new-app-github': { path: '/apps/new?from=github', ready: 'form', h: 1030 },
  'new-app-git': { path: '/apps/new?from=git', ready: 'form', h: 1100 },
  'app-running': { path: `/apps/${WEB}`, ready: 'main', h: 960 },
  'app-deployments': { path: `/apps/${WEB}/deployments`, ready: 'main', h: 1180 },
  'app-deployments-security': { path: `/apps/${WEB}/deployments`, ready: '#security', h: 1180, clip: ['#security'] },
  'app-configure': { path: `/apps/${WEB}/configure`, ready: 'main', h: 1200 },
  'app-configure-source': { path: `/apps/${WEB}/configure/source`, ready: 'main', h: 900 },
  'app-configure-services': { path: `/apps/${WEB}/configure/services`, ready: 'main', h: 1000 },
  'app-configure-services-compose': { path: `/apps/${BLOG}/configure/services`, ready: 'main', h: 1300, clip: ['#service-web'] },
  'app-configure-services-limits': { path: `/apps/${SEARCH}/configure/services`, ready: 'main', h: 1500, clip: ['#service-search'] },
  'app-configure-integrations': { path: `/apps/${WEB}/configure/integrations`, ready: 'main', h: 900 },
  'app-variables': { path: `/apps/${API}/variables`, ready: 'main', h: 1000 },
  'app-domains': { path: `/apps/${WEB}/domains`, ready: 'main', h: 1000 },
  'app-backups': { path: `/apps/${DB}/backups`, ready: 'main', h: 1120 },
  'app-backups-policy': {
    path: `/apps/${SEARCH}/backups`, ready: 'main', h: 1560,
    steps: async (page) => {
      await page.locator('details.cv-confirm > summary:has-text("Change the policy")').first().click();
      await page.locator('form[action$="/backups/policy/change"]').first().waitFor();
    },
  },
  'run-command': {
    path: `/apps/${WEB}`, ready: 'form.cv-runform', clip: ['section[aria-labelledby="run-title"]'],
    steps: async (page) => {
      await page.locator('form.cv-runform input[name="command"]').first().fill('ls /app');
      await Promise.all([
        page.waitForResponse((r) => r.request().method() === 'POST'),
        page.locator('form.cv-runform button:has-text("Run")').first().click(),
      ]);
      await page.locator('[aria-label="Command result"]').waitFor();
    },
  },
  'app-terminal': {
    path: `/apps/${WEB}`, ready: 'form.cv-runform', clip: ['[data-terminal]'],
    steps: async (page) => {
      await Promise.all([page.waitForURL(/\/terminal$/), page.locator('form.cv-runform a:has-text("Terminal")').first().click()]);
      await page.locator('[data-terminal-status]', { hasText: 'Connected' }).waitFor({ timeout: 15000 });
      await page.locator('.xterm-rows', { hasText: '$' }).waitFor();
      await page.locator('.xterm').click();
      await page.keyboard.type('echo web is up', { delay: 20 });
      await page.keyboard.press('Enter');
      await page.locator('.xterm-rows', { hasText: 'web is up' }).waitFor();
    },
  },
  'app-logs': { path: `/apps/${WEB}/logs?range=1h`, ready: 'main', h: 900 },
  'app-requests': { path: `/apps/${WEB}/requests?range=1h`, ready: 'main', h: 1000 },
  'promote-preview': { path: `/apps/${WEB_STAGING}/promote?to=production`, ready: 'main' },
  projects: { path: '/projects', ready: 'main' },
  'project-page': { path: '/projects/shop', ready: 'main', h: 1100 },
  previews: { path: '/previews', ready: 'main' },
  schedules: { path: '/schedules', ready: 'main', h: 900 },
  'schedule-new': { path: '/schedules/new', ready: 'form', h: 1170 },
  domains: { path: '/domains', ready: 'main', h: 1000 },
  logs: { path: `/logs?${SHOP}&range=1h`, ready: 'main', h: 900 },
  monitoring: { path: `/monitoring?${SHOP}`, ready: 'main', h: 1280 },
  'settings-registries': { path: '/settings/registries', ready: 'main', h: 900 },
  'settings-backup-storage': { path: '/settings/backup-storage', ready: 'main', h: 1180 },
  'settings-notifications': { path: '/settings/notifications', ready: 'main', h: 1040 },
  'settings-tokens': { path: '/settings/tokens', ready: 'main', h: 1220 },
  'settings-people': { path: '/settings/people', ready: 'main' },
  'settings-sign-in': { path: '/settings/sign-in', ready: 'main', h: 870 },
  'settings-audit': { path: '/settings/audit', ready: 'main', h: 900 },
  'settings-secret-providers': { path: '/settings/secret-providers', ready: 'main', h: 1150 },
  'settings-certificates': { path: '/settings/certificates', ready: 'main', h: 850 },
  'ops-overview': { path: '/ops', ready: 'main', h: 1100 },
  'ops-servers': { path: '/ops/servers', ready: 'main' },
  'ops-server': { path: '/ops/servers/hel-1', ready: 'main', h: 1100 },
  'ops-updates': { path: '/ops/updates', ready: 'main', h: 1000 },
  'ops-security': { path: '/ops/security', ready: 'main', h: 1500 },
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
const baseUrl = new URL(base);
const log = (message) => console.log(message);

// Headless Chromium hints glyphs to whole pixels by default, which spaces the letters of a word
// unevenly. With hinting off it places them as a real screen does.
const browser = await chromium.launch({ args: ['--font-render-hinting=none'] });

const localZone = process.env.LOCAL_ZONE ?? '';

async function newContext() {
  const context = await browser.newContext({
    viewport: VIEWPORT, deviceScaleFactor: SCALE, locale: 'en-US', timezoneId: localZone || 'UTC',
  });
  if (!localZone) await context.route('**/static/js/localtime.js*', (route) => route.abort());
  return context;
}

async function setTheme(context, theme) {
  await context.addCookies([{ name: 'caravana_theme', value: theme, domain: baseUrl.hostname, path: '/' }]);
}

async function signIn(page) {
  await page.goto(`${base}/login`);
  await page.locator('input[name="email"]').fill(email);
  await page.locator('input[name="password"]').fill(password);
  await Promise.all([page.waitForURL(/\/apps/), page.getByRole('button', { name: 'Sign in', exact: true }).click()]);
}

// What must hold before a picture: the page's fonts and images are in and, when it has a live
// indicator, its stream has connected.
async function settle(page) {
  await page.waitForLoadState('load');
  if ((await page.locator('[data-live-indicator]').count()) > 0) {
    await page.waitForFunction(
      () => document.querySelector('[data-live-indicator]')?.getAttribute('data-state') === 'live',
      null, { timeout: 15000 },
    );
  }
  await page.evaluate(async () => {
    await document.fonts.ready;
    await Promise.all([...document.images].map((img) => (img.complete ? null : new Promise((done) => { img.onload = img.onerror = done; }))));
  });
  await page.waitForTimeout(500);
}

// The page's text must hold none of what a picture may never show.
const LEAKS = [
  [/cvat_[A-Za-z0-9_-]{8,}/, 'an API token'],
  [/cvwh_[A-Za-z0-9_-]{8,}/, 'a webhook token'],
  [/cvin_[A-Za-z0-9_-]{8,}/, 'an invite token'],
  [/cvjn_[A-Za-z0-9_-]{8,}/, 'a join token'],
  [/lacalabaza/i, 'a real project name'],
  [/Reconnecting/, 'a live stream that is not connected'],
];
async function guard(page, name) {
  const text = await page.locator('body').innerText();
  for (const [pattern, what] of LEAKS) {
    if (pattern.test(text)) throw new Error(`${name}: the page shows ${what}`);
  }
  const html = await page.content();
  for (const [pattern, what] of LEAKS.slice(0, 5)) {
    if (pattern.test(html)) throw new Error(`${name}: the page's HTML holds ${what}`);
  }
}

// The union of the boxes of the selectors, padded by 16 px, in page coordinates, clamped to the page.
async function clipOf(page, selectors) {
  const boxes = [];
  for (const selector of selectors) {
    const locator = page.locator(selector);
    if ((await locator.count()) === 0) throw new Error(`the clip selector matches nothing: ${selector}`);
    boxes.push(await locator.first().evaluate((el) => {
      const r = el.getBoundingClientRect();
      return { left: r.left + scrollX, top: r.top + scrollY, right: r.right + scrollX, bottom: r.bottom + scrollY };
    }));
  }
  const size = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, height: document.documentElement.scrollHeight }));
  const left = Math.max(0, Math.min(...boxes.map((b) => b.left)) - PAD);
  const top = Math.max(0, Math.min(...boxes.map((b) => b.top)) - PAD);
  const right = Math.min(size.width, Math.max(...boxes.map((b) => b.right)) + PAD);
  const bottom = Math.min(size.height, Math.max(...boxes.map((b) => b.bottom)) + PAD);
  return { x: left, y: top, width: right - left, height: bottom - top };
}

async function describe(page) {
  try {
    const text = (await page.locator('body').innerText()).replace(/\s+\n/g, '\n');
    return `url ${page.url()}\n${text.slice(0, 1200)}`;
  } catch (error) {
    return `(could not describe the page: ${error.message})`;
  }
}

async function take(context, page, name, screen, theme) {
  await setTheme(context, theme);
  await page.setViewportSize({ width: VIEWPORT.width, height: screen.h ?? VIEWPORT.height });
  const response = await page.goto(`${base}${screen.path}`);
  if (!response || response.status() !== 200) throw new Error(`${name}: ${screen.path} answered ${response?.status()}`);
  if (new URL(page.url()).pathname === '/login' && !screen.public) throw new Error(`${name}: sent to the sign-in page`);
  await page.locator(screen.ready).first().waitFor({ timeout: 15000 });
  if (screen.steps) await screen.steps(page);
  await settle(page);
  await guard(page, name);
  const options = { path: `${rawDir}/${name}-${theme}.png`, animations: 'disabled', caret: 'hide' };
  if (process.env.FULL) {
    // Looking for an h: as tall as the page, up to 3000 px.
    const height = await page.evaluate(() => document.documentElement.scrollHeight);
    await page.setViewportSize({ width: VIEWPORT.width, height: Math.min(Math.max(height, 800), 3000) });
    await page.waitForTimeout(300);
  } else if (screen.clip) {
    options.clip = await clipOf(page, screen.clip);
    options.fullPage = true;
  }
  await page.screenshot(options);
}

// ---------------------------------------------------------------------------
// The walk
// ---------------------------------------------------------------------------
const failures = [];
if (only.length === 0) writeFileSync(`${rawDir}/_names.txt`, `${Object.keys(SCREENS).join('\n')}\n`);
const owner = await newContext();
const visitor = await newContext();
const ownerPage = await owner.newPage();
const visitorPage = await visitor.newPage();
await signIn(ownerPage);

// Run a command and Terminal leave a record (and an entry of the audit log), so they are taken last:
// no other picture shows what the walk itself did.
const LAST = ['run-command', 'app-terminal'];
const walk = Object.entries(SCREENS).sort(([a], [b]) => LAST.includes(a) - LAST.includes(b));
for (const [name, screen] of walk) {
  if (only.length > 0 && !only.includes(name)) continue;
  const context = screen.public ? visitor : owner;
  const page = screen.public ? visitorPage : ownerPage;
  try {
    for (const theme of THEMES) await take(context, page, name, screen, theme);
    log(`captured ${name}`);
  } catch (error) {
    failures.push(name);
    console.error(`FAILED ${name}: ${error.message}\n${await describe(page)}\n`);
  }
}
await browser.close();

if (failures.length > 0) {
  console.error(`${failures.length} screen(s) failed: ${failures.join(', ')}`);
  process.exit(1);
}
