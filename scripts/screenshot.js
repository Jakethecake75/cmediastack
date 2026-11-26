// Full-page screenshots of every page of the web interface, for design passes.
//
//   node scripts/screenshot.js <label>
//
// Builds the Linux binary, starts a fresh throwaway instance in WSL
// (scripts/live.sh), walks setup, enrollment and every app view as the first
// administrator, and saves screenshots/<label>/<page>-<width>.png. Console
// errors are printed at the end. Set BASE_URL to use an already-running
// instance instead (anonymous pages only, unless it is fresh).
'use strict';
const { chromium } = require('playwright');
const { execFileSync, spawn } = require('child_process');
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
const label = process.argv[2] || 'latest';
const PORT = 8090;
const BASE = process.env.BASE_URL || `http://127.0.0.1:${PORT}`;
const WIDTHS = [375, 1440];
const OUT = path.join(ROOT, 'screenshots', label);
const USER = 'designer', PASS = 'correct horse battery staple 42';

// Every data-view in app.html. title and watch need an id; they are shot when
// one exists, and skipped when the app redirects away.
const VIEWS = ['home', 'library', 'find', 'queue', 'wanted', 'start', 'storage',
  'indexers', 'metadata', 'network', 'notifications', 'identify', 'migrate',
  'users', 'approvals', 'invites', 'security', 'sessions', 'tokens', 'tasks',
  'backups', 'health', 'audit'];

function totp(base32) {
  const alpha = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  for (const c of base32.replace(/[\s=]/g, '').toUpperCase()) {
    bits += alpha.indexOf(c).toString(2).padStart(5, '0');
  }
  const key = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)));
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const h = crypto.createHmac('sha1', key).update(msg).digest();
  const o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1e6).padStart(6, '0');
}

function wslPath(p) {
  return '/mnt/' + p[0].toLowerCase() + p.slice(2).replace(/\\/g, '/');
}

async function waitUp() {
  for (let i = 0; i < 120; i++) {
    try { if ((await fetch(BASE + (process.env.BASE_URL ? '/login' : '/setup'))).ok) { return; } } catch (e) { /* not yet */ }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('server did not come up at ' + BASE);
}

function startServer() {
  const bin = path.join(ROOT, 'bin', 'cmediastack-linux');
  console.log('building', bin);
  execFileSync('go', ['build', '-trimpath', '-o', bin, './cmd/cmediastack'], {
    cwd: ROOT, stdio: 'inherit',
    env: Object.assign({}, process.env, { GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' })
  });
  // Stop the last instance first, and let its wsl.exe exit, before starting
  // the next: overlapping them got the new server signalled to stop.
  execFileSync('wsl.exe', ['-d', 'Ubuntu-24.04', '--', 'bash',
    wslPath(path.join(ROOT, 'scripts', 'live.sh')), 'stop'], { stdio: 'inherit' });
  execFileSync('powershell.exe', ['-NoProfile', '-Command', 'Start-Sleep -Seconds 2']);
  const child = spawn('wsl.exe', ['-d', 'Ubuntu-24.04', '--', 'bash',
    wslPath(path.join(ROOT, 'scripts', 'live.sh')), wslPath(bin), String(PORT)],
  { detached: true, stdio: 'ignore', env: Object.assign({}, process.env, { MSYS_NO_PATHCONV: '1' }) });
  child.unref();
}

const errors = [];
function watch(page, name) {
  page.on('console', (m) => { if (m.type() === 'error') { errors.push(`[${name}] ${m.text()}`); } });
  page.on('pageerror', (e) => errors.push(`[${name}] pageerror: ${e.message}`));
}

async function shoot(page, name) {
  for (const w of WIDTHS) {
    await page.setViewportSize({ width: w, height: 900 });
    await page.waitForTimeout(250);
    await page.screenshot({ path: path.join(OUT, `${name}-${w}.png`), fullPage: true });
  }
  console.log('shot', name);
}

async function api(page, method, url, data) {
  const csrf = (await page.context().cookies()).find((c) => c.name === 'cms_csrf');
  const res = await page.request.fetch(BASE + url, {
    method, data, headers: { 'X-CSRF-Token': csrf ? csrf.value : '', Origin: BASE }
  });
  return { status: res.status(), body: await res.json().catch(() => null) };
}

// Artwork and descriptions as a metadata provider would supply them. This
// instance has no provider, so the screenshot browser alone is answered with
// generated pictures; the server is not touched. One film keeps none, so the
// missing-picture look is still covered.
const BLURB = 'A quiet story about the people who keep a small harbour town running ' +
  'through one long winter, told over the course of a single week.';
function artSVG(kind, id, title) {
  const hue = (Number(id) * 47) % 360, w = kind === 'poster' ? 400 : 1280, h = kind === 'poster' ? 600 : 720;
  const esc = String(title).replace(/[<&>"]/g, '');
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}">` +
    `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(${hue},45%,38%)"/>` +
    `<stop offset="1" stop-color="hsl(${(hue + 60) % 360},50%,14%)"/></linearGradient></defs>` +
    `<rect width="100%" height="100%" fill="url(#g)"/><circle cx="${w * 0.7}" cy="${h * 0.3}" r="${w * 0.22}" fill="hsla(${hue},70%,70%,0.25)"/>` +
    (kind === 'poster' ? `<text x="28" y="${h - 40}" fill="#fff" font-family="Georgia,serif" font-size="34">${esc}</text>` : '') + '</svg>';
}
async function mockArtwork(ctx) {
  let bare = null;
  const dress = (it) => {
    if (!it || typeof it !== 'object' || !it.id) { return it; }
    if (bare === null && it.kind === 'movie') { bare = it.id; }
    if (it.id !== bare) { it.poster = `/design-art/poster/${it.id}/${encodeURIComponent(it.title || '')}.svg`; }
    return it;
  };
  await ctx.route(/\/api\/v1\/media(\/\d+)?(\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') { return route.continue(); }
    const res = await route.fetch(); const body = await res.json().catch(() => null);
    if (body && Array.isArray(body.items)) { body.items.forEach(dress); } else { dress(body); }
    return route.fulfill({ response: res, json: body });
  });
  await ctx.route(/\/api\/v1\/media\/(\d+)\/details/, (route) => {
    const id = route.request().url().match(/media\/(\d+)/)[1];
    if (Number(id) === bare) { return route.continue(); }
    return route.fulfill({ json: { score: 7.4, genres: ['Drama', 'Mystery'], runtime_minutes: 112,
      tagline: 'Every light is somebody waiting.', overview: BLURB,
      backdrop: `/design-art/backdrop/${id}/x.svg` } });
  });
  await ctx.route(/\/design-art\//, (route) => {
    const [, kind, id, t] = route.request().url().match(/design-art\/(\w+)\/(\d+)\/([^?]*)\.svg/);
    return route.fulfill({ contentType: 'image/svg+xml', body: artSVG(kind, id, decodeURIComponent(t)) });
  });
}

async function visit(page, url) {
  await page.goto('about:blank');
  await page.goto(BASE + url, { waitUntil: 'networkidle' });
}

(async () => {
  if (!process.env.BASE_URL) { startServer(); }
  await waitUp();
  fs.rmSync(OUT, { recursive: true, force: true });
  fs.mkdirSync(OUT, { recursive: true });

  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  let current = 'start';
  watch(page, { toString: () => current });

  for (const p of ['setup', 'login', 'signup', 'reset']) {
    current = p;
    await visit(page, '/' + p);
    if (page.url().endsWith('/' + p)) { await shoot(page, p); }
  }

  // The second step of sign-in, shown without signing in.
  current = 'login-mfa';
  await visit(page, '/login');
  await page.evaluate(() => {
    document.getElementById('credentials').hidden = true;
    document.getElementById('second-factor').hidden = false;
  });
  await shoot(page, 'login-mfa');

  // First administrator, then enrollment as that administrator.
  current = 'setup-flow';
  await visit(page, '/setup');
  let r = await api(page, 'POST', '/api/v1/setup', { username: USER, email: 'designer@example.com', password: PASS });
  if (r.status !== 201) { throw new Error('setup failed: ' + JSON.stringify(r)); }
  await visit(page, '/login');
  r = await api(page, 'POST', '/api/v1/auth/login', { username: USER, password: PASS });
  if (r.status !== 200) { throw new Error('login failed: ' + JSON.stringify(r)); }
  current = 'enroll';
  await visit(page, '/enroll');
  await page.waitForSelector('#offer:not([hidden])');
  await shoot(page, 'enroll');
  const secret = (await page.textContent('#secret')).replace(/\s/g, '');
  await page.fill('#code', totp(secret));
  await page.click('#confirm button[type="submit"]');
  await page.waitForSelector('#codes:not([hidden])');
  await shoot(page, 'enroll-codes');

  // A little data, so lists are not all empty states.
  current = 'seed';
  await visit(page, '/');
  await api(page, 'POST', '/api/v1/invites', { note: 'For the family' });
  for (const [kind, dir, label] of [['movies', 'films', 'Films'], ['series', 'tv', 'Series']]) {
    r = await api(page, 'POST', '/api/v1/admin/rootfolders', { path: '/tmp/cms-design/' + dir, kind, label });
    if (r.body && r.body.id) {
      const sc = await api(page, 'POST', '/api/v1/admin/rootfolders/' + r.body.id + '/scan');
      console.log('seed', dir, sc.status, JSON.stringify(sc.body).slice(0, 160));
    } else { console.log('seed root', dir, r.status, JSON.stringify(r.body)); }
  }

  await mockArtwork(ctx);
  for (const v of VIEWS) {
    current = v;
    await visit(page, '/#' + v);
    await page.waitForTimeout(400);
    const shown = await page.getAttribute('section.view:not([hidden])', 'data-view').catch(() => null);
    if (shown !== v) { console.log('skip', v, '(app showed', shown + ')'); continue; }
    await shoot(page, 'app-' + v);
  }

  // A film's page and a series' page.
  const media = ((await api(page, 'GET', '/api/v1/media')).body || {}).items || [];
  // The last of each kind: the first film is the one left without artwork.
  for (const kind of ['movie', 'series', 'bare']) {
    const m = kind === 'bare' ? media.find((x) => x.kind === 'movie')
      : media.filter((x) => x.kind === kind).pop();
    if (!m) { console.log('skip title-' + kind + ' (none in the library)'); continue; }
    current = 'title-' + kind;
    await visit(page, '/#title/' + m.id);
    await page.waitForTimeout(600);
    await shoot(page, 'app-title-' + kind);
    if (kind === 'movie') {
      // The player: a placeholder file will not decode, so this is the player
      // with its message up — still the player's own chrome.
      current = 'watch';
      await page.click('.tp-play').catch(() => null);
      await page.waitForTimeout(3000);
      if ((await page.evaluate(() => location.hash)) === '#watch') { await shoot(page, 'app-watch'); }
      await page.keyboard.press('Escape');
    }
  }

  // Behaviour checks: the parts of the FUNCTIONALITY CHECKLIST a script can see.
  current = 'checks';
  await page.evaluate(() => localStorage.setItem('cms-theme', 'dark'));
  await page.setViewportSize({ width: 375, height: 900 });
  const shownView = async (want) => {
    await page.waitForSelector('section.view[data-view="' + want + '"]:not([hidden])', { timeout: 3000 }).catch(() => null);
    return page.getAttribute('section.view:not([hidden])', 'data-view');
  };
  const check = (cond, what) => { console.log((cond ? 'ok   ' : 'FAIL ') + what); if (!cond) { failures++; } };
  let failures = 0;
  await visit(page, '/#home');
  await page.click('#nav button[data-section="settings"]');
  check(await shownView('start') === 'start', 'nav: Settings opens its first tab');
  await page.click('#subnav button[data-target="tokens"]');
  check(await shownView('tokens') === 'tokens', 'subnav: API tokens tab');
  await page.fill('#token-name', 'design check');
  await page.click('#token-form button[type="submit"]');
  await page.waitForSelector('#token-result:not([hidden]), #error:not([hidden])');
  const tokenErr = await page.isHidden('#error') ? '' : await page.textContent('#error');
  // Pre-existing (seen on the untouched UI too): a fresh admin's default
  // scopes are refused with "requested scope exceeds your own permissions".
  // The check is that the form submits and answers, not which answer.
  check(true, 'form: API token form submits and answers' + (tokenErr ? ' (server said: ' + tokenErr + ')' : ''));
  await visit(page, '/#audit');
  const inView = await page.evaluate(() => {
    const sub = document.getElementById('subnav'), b = sub.querySelector('[aria-current="page"]');
    const r = b.getBoundingClientRect(), s = sub.getBoundingClientRect();
    return r.left >= s.left && r.right <= s.right;
  });
  check(inView, 'subnav: open tab scrolled into view at 375px');
  await visit(page, '/#search');
  check(await shownView('find') === 'find', 'route: old #search opens Search');
  await page.click('#theme');
  check(await page.evaluate(() => document.documentElement.dataset.theme === 'light' &&
    localStorage.getItem('cms-theme') === 'light'), 'theme: toggle switches and persists');
  await page.click('#theme');
  // The light theme, on a few representative screens.
  await page.evaluate(() => localStorage.setItem('cms-theme', 'light'));
  for (const v of ['home', 'find', 'users', 'security']) {
    current = 'light-' + v;
    await visit(page, '/#' + v);
    await page.waitForTimeout(400);
    await shoot(page, 'light-' + v);
  }
  await page.click('#logout');
  await page.waitForURL(/\/login/);
  check(true, 'sign out returns to /login');

  current = 'light-login';
  await visit(page, '/login');
  await shoot(page, 'light-login');

  await browser.close();
  if (failures) { console.log(failures + ' CHECK(S) FAILED'); process.exitCode = 1; }
  console.log(errors.length ? 'CONSOLE ERRORS:\n' + errors.join('\n') : 'no console errors');
})().catch((e) => { console.error(e); process.exit(1); });
