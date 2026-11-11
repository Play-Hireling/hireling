// Console check for the owner panel. Prints one JSON line:
// {"ok":true,"errors":[],"csp":[]}
// or ok:false with the messages. Exit 0 only when both are empty.
import { pathToFileURL } from 'node:url';

const root = process.env.PLAYWRIGHT_CORE || '/tmp/panel-pw/node_modules';
const { chromium } = await import(pathToFileURL(root + '/playwright-core/index.mjs').href);

const [url, token, expectText] = process.argv.slice(2);
if (!url || !token || !expectText) {
  console.error('usage: panel_console.mjs <url> <token> <text>');
  process.exit(2);
}

const chrome = process.env.CHROME_PATH || '/usr/local/bin/google-chrome';
const browser = await chromium.launch({
  executablePath: chrome,
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu'],
});

const errors = [];
const csp = [];
const viewports = [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
];

try {
  for (const viewport of viewports) {
    const page = await browser.newPage({ viewport });
    const label = viewport.width + 'x' + viewport.height;
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(label + ' console: ' + msg.text());
    });
    page.on('pageerror', (err) => {
      errors.push(label + ' pageerror: ' + err.message);
    });
    page.on('requestfailed', (req) => {
      errors.push(label + ' requestfailed: ' + req.url() + ' ' + (req.failure() ? req.failure().errorText : ''));
    });
    page.on('request', (req) => {
      const u = req.url();
      if (u.includes('/api/quest') || u.includes('token=')) {
        errors.push(label + ' bad request: ' + u.split('?')[0]);
      }
    });
    await page.addInitScript((t) => {
      sessionStorage.setItem('tavernToken', t);
      try { localStorage.removeItem('tavernToken'); } catch (e) { /* ignore */ }
      window.__csp = [];
      document.addEventListener('securitypolicyviolation', (e) => {
        window.__csp.push(e.violatedDirective + ' ' + e.blockedURI + ' ' + e.sample);
      });
    }, token);
    await page.goto(url + '/', { waitUntil: 'networkidle' });
    await page.waitForTimeout(5500);
    const body = await page.locator('body').innerText();
    if (!body.includes(expectText)) {
      errors.push(label + ' missing ' + JSON.stringify(expectText));
    }
    const found = await page.evaluate(() => window.__csp || []);
    for (const v of found) csp.push(label + ' ' + v);
    await page.close();
  }
} finally {
  await browser.close();
}

const ok = errors.length === 0 && csp.length === 0;
console.log(JSON.stringify({ ok, errors, csp, expect: expectText }));
process.exit(ok ? 0 : 1);
