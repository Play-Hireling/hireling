// A13 proof for the owner panel's live turn stream.
//
//   node panel_stream.mjs <origin> <owner-token> <agent-token>
//
// 1. Fast path: a turn `step` reported while the SSE stream is open reaches
//    the panel before the turn ends and before the next 5 s poll.
// 2. Fallback: a stream that opens and closes with no events is tried once,
//    warned about, and the panel keeps painting from GET /api/status on the
//    5 s poll.
// 3. The panel opens with and without a token at the five QA resolutions with
//    no console error, page error or CSP violation.
//
// Prints one JSON line: {"ok":true,"checks":[...],"errors":[],"warnings":[]}
// Exit 0 only when `errors` is empty.
import { pathToFileURL } from 'node:url';

const root = process.env.PLAYWRIGHT_CORE || '/tmp/panel-pw/node_modules';
const { chromium } = await import(pathToFileURL(root + '/playwright-core/index.mjs').href);

const [origin, ownerToken, agentToken] = process.argv.slice(2);
if (!origin || !ownerToken || !agentToken) {
  console.error('usage: panel_stream.mjs <origin> <owner-token> <agent-token>');
  process.exit(2);
}
const chrome = process.env.CHROME_PATH || '/usr/local/bin/google-chrome';
const browser = await chromium.launch({
  executablePath: chrome,
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu'],
});

const checks = [];
const errors = [];
const warnings = [];
const ok = (message) => { checks.push({ ok: true, message }); };
const fail = (message) => { checks.push({ ok: false, message }); errors.push(message); };

const views = [
  { name: '1440x900', width: 1440, height: 900, touch: false },
  { name: '1280x800', width: 1280, height: 800, touch: false },
  { name: '1024x768', width: 1024, height: 768, touch: false },
  { name: '390x844', width: 390, height: 844, touch: true },
  { name: '360x640', width: 360, height: 640, touch: true },
];

function pageLog(page, state) {
  page.on('console', (msg) => {
    if (msg.type() === 'error') state.errors.push('console: ' + msg.text());
    if (msg.type() === 'warning') state.warnings.push(msg.text());
  });
  page.on('pageerror', (err) => state.errors.push('pageerror: ' + err.message));
  page.on('requestfailed', (req) => state.failed.push(req.url() + ' ' + (req.failure() ? req.failure().errorText : '')));
  page.on('request', (req) => {
    const path = new URL(req.url()).pathname;
    if (path === '/api/turn/stream') state.streamHits.push(Date.now());
    if (path === '/api/status') state.statusHits.push(Date.now());
  });
  page.on('response', (res) => {
    if (new URL(res.url()).pathname === '/api/turn/stream') state.streamStatuses.push(res.status());
  });
}

async function openPage(view, token, routeStream) {
  const state = { errors: [], warnings: [], failed: [], streamHits: [], streamStatuses: [], statusHits: [] };
  const context = await browser.newContext({
    viewport: { width: view.width, height: view.height },
    deviceScaleFactor: 1,
    hasTouch: view.touch,
    isMobile: view.touch,
  });
  const page = await context.newPage();
  pageLog(page, state);
  await page.addInitScript(({ token }) => {
    if (token) sessionStorage.setItem('tavernToken', token);
    else sessionStorage.removeItem('tavernToken');
    window.__csp = [];
    document.addEventListener('securitypolicyviolation', (event) => {
      window.__csp.push(event.violatedDirective + ' ' + (event.blockedURI || ''));
    });
  }, { token });
  if (routeStream) await page.route('**/api/turn/stream', routeStream);
  return { context, page, state };
}

async function csp(page, state) {
  for (const violation of await page.evaluate(() => window.__csp || [])) state.errors.push('csp: ' + violation);
}

async function waitText(page, text, timeout) {
  await page.waitForFunction((want) => (document.body.textContent || '').includes(want), text, { timeout });
}

async function postTurn(body) {
  const res = await fetch(origin + '/api/turn', {
    method: 'POST',
    headers: { Authorization: 'Bearer ' + agentToken, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error('POST /api/turn ' + res.status);
}

async function until(fn, timeout, label) {
  const start = Date.now();
  while (Date.now() - start < timeout) {
    if (fn()) return Date.now() - start;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error('timeout: ' + label);
}

try {
  // 1. The streamed step reaches the balloon before the turn's end and before
  // the next poll. The post happens right after the first paint, so the next
  // /api/status is ~5 s away; the 2 s bound is the stream, not luck.
  {
    const { context, page, state } = await openPage(views[0], ownerToken, null);
    try {
      await page.goto(origin + '/panel/', { waitUntil: 'domcontentloaded' });
      await page.locator('#party .agent').waitFor({ timeout: 10000 });
      const stamp = Date.now();
      const step = 'STREAM-EARLY-' + stamp;
      await postTurn({ agent: 'test', phase: 'start', topic: 'Stream proof' });
      const started = Date.now();
      await postTurn({ agent: 'test', phase: 'step', step });
      await waitText(page, step, 2000);
      const elapsed = Date.now() - started;
      const pollAfterPost = state.statusHits.filter((at) => at >= started - 50);
      if (elapsed >= 2000) fail(`stream step took ${elapsed} ms (>= 2000 ms)`);
      else if (pollAfterPost.length) fail(`stream step arrived after a poll (${pollAfterPost.length} /api/status after the post)`);
      else ok(`stream step on screen in ${elapsed} ms, before the turn ended and before the next poll`);

      const end = 'STREAM-END-' + stamp;
      await postTurn({ agent: 'test', phase: 'end', result: end });
      await waitText(page, end, 2000);
      ok('stream end result replaced the balloon');
      await csp(page, state);
      state.errors.forEach((message) => fail('fast path ' + message));
    } catch (err) {
      fail('fast path: ' + err.message);
    } finally {
      await context.close();
    }
  }

  // 2. A stream that opens and closes with no events is tried once, warned
  // about, and the poll keeps the panel alive. A /api/status after the post is
  // what paints the step.
  {
    const { context, page, state } = await openPage(views[0], ownerToken, (route) =>
      route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }));
    try {
      await page.goto(origin + '/panel/', { waitUntil: 'domcontentloaded' });
      await page.locator('#party .agent').waitFor({ timeout: 10000 });
      await until(() => state.warnings.some((w) => w.includes('live turn stream off')), 3000, 'stream warning');
      ok('an empty stream is reported and the panel falls back');

      const step = 'POLL-FALLBACK-' + Date.now();
      const started = Date.now();
      await postTurn({ agent: 'test', phase: 'step', step });
      await waitText(page, step, 8000);
      const elapsed = Date.now() - started;
      const polled = state.statusHits.some((at) => at >= started - 50);
      if (!polled) fail('fallback text did not arrive on a /api/status poll');
      else if (elapsed > 7500) fail(`fallback poll took ${elapsed} ms`);
      else ok(`poll painted the step in ${elapsed} ms with the stream off`);

      await page.waitForTimeout(2000);
      if (state.streamHits.length !== 1) fail(`stream retried ${state.streamHits.length} times (want 1, no hot loop)`);
      else ok('stream is not retried in a hot loop');
      await csp(page, state);
      state.errors.forEach((message) => fail('fallback ' + message));
    } catch (err) {
      fail('fallback: ' + err.message);
    } finally {
      await context.close();
    }
  }

  // 3. Five resolutions, with and without the owner token.
  for (const view of views) {
    for (const withToken of [true, false]) {
      const label = `${view.name} ${withToken ? 'token' : 'no token'}`;
      const { context, page, state } = await openPage(view, withToken ? ownerToken : '', null);
      try {
        await page.goto(origin + '/panel/', { waitUntil: 'domcontentloaded' });
        if (withToken) {
          await page.locator('#party .agent').waitFor({ timeout: 10000 });
          await until(() => state.streamStatuses.length > 0, 5000, 'stream response');
        } else {
          await waitText(page, 'paste the owner token above', 5000);
        }
        await page.waitForTimeout(600);
        await csp(page, state);
        if (state.errors.length) state.errors.forEach((message) => fail(label + ' ' + message));
        else if (state.failed.length) state.failed.forEach((message) => fail(label + ' requestfailed ' + message));
        else if (withToken && state.streamStatuses.some((status) => status !== 200)) fail(label + ' stream status ' + state.streamStatuses.join(','));
        else if (withToken && state.streamHits.length !== 1) fail(label + ' stream hits ' + state.streamHits.length);
        else if (!withToken && state.streamHits.length !== 0) fail(label + ' opened a stream without a token');
        else ok(`${label}: opens clean`);
      } catch (err) {
        fail(`${label}: ${err.message}`);
      } finally {
        await context.close();
      }
    }
  }
} finally {
  await browser.close();
}

for (const check of checks) console.log((check.ok ? 'ok   ' : 'FAIL ') + check.message);
console.log(JSON.stringify({ ok: errors.length === 0, checks, errors, warnings }, null, 1));
process.exit(errors.length === 0 ? 0 : 1);
