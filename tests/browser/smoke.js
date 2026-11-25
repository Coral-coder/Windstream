// Browser smoke test of the app: first-run setup, the control panel's
// password-only sign-in, signing in to stream (password + authenticator
// code), live video, and the stream resuming by itself after the server
// restarts (as after an update).
//
//   go build -o /tmp/ws ./cmd/windstream
//   WINDSTREAM=/tmp/ws CHROME=/path/to/chrome node tests/browser/smoke.js
//
// Needs Playwright (NODE_PATH pointing at a node_modules with it) and
// ffmpeg on PATH (the dev server streams a test pattern).
'use strict';
const { chromium } = require('playwright');
const crypto = require('crypto');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn } = require('child_process');

const exe = process.env.WINDSTREAM;
if (!exe) { console.error('set WINDSTREAM to a built windstream binary'); process.exit(2); }
const data = fs.mkdtempSync(path.join(os.tmpdir(), 'ws-smoke-'));
const problems = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) problems.push(what); };

function totp(secret, step = 0) {
  const a = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  for (const c of secret) bits += a.indexOf(c).toString(2).padStart(5, '0');
  const key = Buffer.alloc(Math.floor(bits.length / 8));
  for (let i = 0; i < key.length; i++) key[i] = parseInt(bits.substr(i * 8, 8), 2);
  const ctr = Buffer.alloc(8);
  ctr.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000) + step));
  const h = crypto.createHmac('sha1', key).update(ctr).digest();
  const o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, '0');
}
// A code may be used once: wait for the next 30 s window between uses.
const nextWindow = () => new Promise((r) => setTimeout(r, (30 - (Date.now() / 1000) % 30) * 1000 + 600));

let server;
function startServer() {
  server = spawn(exe, ['app', '-dev', '-data', data], { stdio: ['ignore', 'ignore', fs.openSync(path.join(data, 'server.log'), 'a')] });
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  startServer();
  await sleep(3000);
  const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ['--no-sandbox', '--autoplay-policy=no-user-gesture-required'] });
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1200, height: 1000 } });
  const errors = [];
  ctx.on('page', (p) => p.on('pageerror', (e) => errors.push(e.message)));
  try {
    // ---- first run: account with password + authenticator
    const dash = await ctx.newPage();
    await dash.goto('http://127.0.0.1:47333/');
    await dash.waitForSelector('#view-setup:not(.hidden)');
    await dash.fill('#form-setup-1 [name=username]', 'player');
    await dash.fill('#form-setup-1 [name=password]', 'correct-horse-battery');
    await dash.fill('#form-setup-1 [name=confirm]', 'correct-horse-battery');
    await dash.click('#form-setup-1 button');
    await dash.waitForSelector('#setup-2:not(.hidden)');
    const secret = (await dash.textContent('#setup-secret')).replace(/\s/g, '');
    await dash.fill('#form-setup-2 [name=code]', totp(secret));
    await dash.click('#form-setup-2 button');
    await dash.waitForSelector('#view-dash:not(.hidden)');
    check(true, 'setup done, control panel open');

    // ---- the control panel signs in with the password alone
    await dash.click('#btn-logout');
    await dash.waitForSelector('#view-login:not(.hidden)');
    check(await dash.$('#form-login [name=code]') === null, 'control panel sign-in has no authenticator code field');
    await dash.fill('#form-login [name=username]', 'PLAYER');
    await dash.fill('#form-login [name=password]', 'wrong-password-123');
    await dash.click('#form-login button');
    await dash.waitForFunction(() => document.querySelector('#form-login [data-error]').textContent !== '');
    check(await dash.isVisible('#view-login'), 'a wrong password is refused');
    await dash.fill('#form-login [name=password]', 'correct-horse-battery');
    await dash.click('#form-login button');
    await dash.waitForSelector('#view-dash:not(.hidden)', { timeout: 10000 });
    check(true, 'control panel signed in with only the password');
    await dash.waitForFunction(() => (document.querySelector('#engine').textContent || '').startsWith('Ready'), null, { timeout: 30000 });

    // ---- streaming sign-in still needs the authenticator code
    const sp = await ctx.newPage();
    await sp.goto('https://127.0.0.1:8443/');
    await nextWindow();
    await sp.fill('#login-form [name=username]', 'player');
    await sp.fill('#login-form [name=password]', 'correct-horse-battery');
    await sp.click('#login-form button');
    await sp.waitForSelector('#totp-row:not(.hidden)');
    check(true, 'streaming sign-in asks for the authenticator code');
    await sp.fill('#login-form [name=code]', totp(secret));
    await sp.click('#login-form button');
    await sp.waitForFunction(() => document.querySelector('#video').videoWidth > 0, null, { timeout: 30000 });
    check(true, 'video playing');

    // ---- restart (as an update does): the stream resumes without signing in
    server.kill();
    await sleep(2000);
    const down = Date.now();
    startServer();
    const frames = await sp.evaluate(() => document.querySelector('#video').getVideoPlaybackQuality().totalVideoFrames).catch(() => 0);
    const back = await sp.waitForFunction((f) => { const v = document.querySelector('#video'); return v && v.videoWidth > 0 && v.getVideoPlaybackQuality().totalVideoFrames > f + 30; }, frames, { timeout: 60000 }).then(() => true, () => false);
    check(back && await sp.isHidden('#login'), `stream resumed by itself after a restart (${((Date.now() - down) / 1000).toFixed(1)} s)`);
    check(errors.length === 0, 'no page errors ' + JSON.stringify(errors));
  } catch (e) {
    check(false, 'unexpected: ' + e.message);
  } finally {
    await browser.close();
    server.kill();
  }
  console.log(problems.length ? `FAILED (${problems.length}); server log in ${data}` : 'ALL PASS');
  process.exit(problems.length ? 1 : 0);
})();
