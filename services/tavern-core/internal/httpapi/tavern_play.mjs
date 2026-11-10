// Loads /tavern/ under the tavern-core CSP and plays it: token screen, WASD,
// touch, one order, one decision. Exit 0 only with zero CSP violations, zero
// console errors and zero economy calls.
import { pathToFileURL } from 'node:url';

const root = process.env.PLAYWRIGHT_CORE || '/tmp/panel-pw/node_modules';
const { chromium } = await import(pathToFileURL(root + '/playwright-core/index.mjs').href);

const [url, token] = process.argv.slice(2);
if (!url || !token) {
  console.error('usage: tavern_play.mjs <origin> <owner-token>');
  process.exit(2);
}

const chrome = process.env.CHROME_PATH || '/usr/bin/google-chrome';
const browser = await chromium.launch({
  executablePath: chrome,
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu'],
});

const views = [
  { name: '1440', width: 1440, height: 900, touch: false },
  { name: '390', width: 390, height: 844, touch: true },
  { name: '2560x1080', width: 2560, height: 1080, touch: false },
  { name: '3440x1440', width: 3440, height: 1440, touch: false },
];

function leftPx(style) {
  const match = /left:\s*([0-9.]+)px/.exec(style || '');
  return match ? Number(match[1]) : 0;
}

const errors = [];
const csp = [];
try {
  for (const view of views) {
    const context = await browser.newContext({
      viewport: { width: view.width, height: view.height },
      hasTouch: view.touch,
      isMobile: view.touch,
    });
    const page = await context.newPage();
    const label = view.name;
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(label + ' console: ' + msg.text());
    });
    page.on('pageerror', (err) => errors.push(label + ' pageerror: ' + err.message));
    page.on('request', (req) => {
      const path = new URL(req.url()).pathname;
      if (/\/api\/(coins|treasury|usage)/.test(path)) errors.push(label + ' economy ' + path);
    });
    await page.addInitScript(() => {
      window.__csp = [];
      document.addEventListener('securitypolicyviolation', (e) => {
        window.__csp.push(e.violatedDirective + ' ' + (e.blockedURI || '') + ' ' + (e.sample || ''));
      });
    });
    try {
    await page.goto(url + '/tavern/', { waitUntil: 'networkidle' });
    await page.locator('#token-screen').waitFor({ state: 'visible' });
    if (await page.locator('input[type=checkbox]').count()) errors.push(label + ' remember checkbox');
    await page.fill('#token', token);
    await page.locator('#enter').click();
    await page.locator('#token-screen').waitFor({ state: 'hidden' });
    const stored = await page.evaluate(() => ({
      session: sessionStorage.getItem('tavernToken'),
      local: localStorage.getItem('tavernToken'),
    }));
    if (stored.session !== token || stored.local) errors.push(label + ' token storage ' + JSON.stringify(stored));
    const before = leftPx(await page.locator('#player').getAttribute('style'));
    if (view.touch) {
      const box = await page.locator('#stick').boundingBox();
      if (!box) errors.push(label + ' stick missing');
      else {
        const client = await context.newCDPSession(page);
        const x = box.x + box.width / 2;
        const y = box.y + box.height / 2;
        await client.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
        await client.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x + 30, y }] });
        await page.waitForTimeout(200);
        await client.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
        await page.locator('#stick').dispatchEvent('touchmove');
      }
    } else {
      await page.keyboard.down('d');
      await page.waitForTimeout(200);
      await page.keyboard.up('d');
    }
    const after = leftPx(await page.locator('#player').getAttribute('style'));
    if (!(after > before)) errors.push(label + ' did not walk ' + before + ' -> ' + after);
    await page.locator('body[data-status]').waitFor();
    const status = await page.locator('body').getAttribute('data-status');
    const decisions = await page.locator('body').getAttribute('data-decisions');
    if (status !== '200') errors.push(label + ' status ' + status);
    if (!decisions || decisions === '0') errors.push(label + ' decisions ' + decisions);
    await page.locator('body[data-decision]').waitFor();
    await page.locator('#order').click();
    await page.locator('body[data-ordered="1"]').waitFor();
    await page.locator('#answer').click();
    await page.locator('body[data-answered="1"]').waitFor();
    const found = await page.evaluate(() => window.__csp || []);
    for (const v of found) csp.push(label + ' ' + v);
    } catch (err) {
      errors.push(label + ' ' + (err && err.message ? err.message.split('\n')[0] : err));
    }
    await context.close();
  }
} finally {
  await browser.close();
}

const ok = errors.length === 0 && csp.length === 0;
console.log(JSON.stringify({ ok, errors, csp }));
process.exit(ok ? 0 : 1);
