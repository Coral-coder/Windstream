'use strict';
const $ = (s, el = document) => el.querySelector(s);
const views = ['setup', 'login', 'dash'];
let state = null;
let setupToken = null;
let addToken = null;
let settingsDirty = false;

let lastState = null; // latest /api/state, for the troubleshooting buttons

async function api(path, body) {
  const res = await fetch(path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  let data = {};
  try { data = await res.json(); } catch { /* empty */ }
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

function show(view) {
  for (const v of views) $(`#view-${v}`).classList.toggle('hidden', v !== view);
}

function formError(form, msg) { $('[data-error]', form).textContent = msg || ''; }

async function submitting(form, fn) {
  const btn = $('button[type=submit]', form);
  btn.disabled = true; formError(form, '');
  try { await fn(new FormData(form)); } catch (e) { formError(form, e.message); }
  finally { btn.disabled = false; }
}

// ---------- first run ----------
$('#form-setup-1').addEventListener('submit', (ev) => {
  ev.preventDefault();
  submitting(ev.target, async (fd) => {
    if (fd.get('password') !== fd.get('confirm')) throw new Error("Passwords don't match");
    const r = await api('/api/setup/begin', { username: fd.get('username'), password: fd.get('password') });
    setupToken = r.token;
    $('#setup-qr').src = r.qr;
    $('#setup-secret').textContent = r.secret.replace(/(.{4})/g, '$1 ').trim();
    $('#setup-1').classList.add('hidden');
    $('#setup-2').classList.remove('hidden');
    $('#form-setup-2 input').focus();
  });
});
$('#form-setup-2').addEventListener('submit', (ev) => {
  ev.preventDefault();
  submitting(ev.target, async (fd) => {
    await api('/api/setup/finish', { token: setupToken, code: fd.get('code').trim() });
    await refresh();
  });
});

// ---------- login ----------
$('#form-login').addEventListener('submit', (ev) => {
  ev.preventDefault();
  submitting(ev.target, async (fd) => {
    try {
      await api('/api/login', { username: fd.get('username'), password: fd.get('password'), code: fd.get('code') });
    } catch (e) {
      if (e.message === 'totp_required') throw new Error('Enter the code from your authenticator app');
      throw e;
    }
    ev.target.reset();
    await refresh();
  });
});
$('#btn-logout').addEventListener('click', async () => { await api('/api/logout', {}); await refresh(); });

// ---------- dashboard ----------
$('#btn-copy').addEventListener('click', async () => {
  const link = $('#link').href;
  try { await navigator.clipboard.writeText(link); $('#btn-copy').textContent = 'Copied ✓'; } catch { /* ignore */ }
  setTimeout(() => { $('#btn-copy').textContent = 'Copy'; }, 1500);
});
$('#btn-net').addEventListener('click', async () => { await api('/api/network/refresh', {}); });

const form = $('#form-settings');
form.addEventListener('input', () => { settingsDirty = true; $('#saved').classList.add('hidden'); syncSettingsUI(); });
form.addEventListener('submit', (ev) => {
  ev.preventDefault();
  submitting(form, async (fd) => {
    const s = {
      resolution: fd.get('resolution'), fps: +fd.get('fps'), refresh: Math.max(60, +fd.get('fps')),
      bitrate_mbps: +fd.get('bitrate_mbps'), encoder: fd.get('encoder'), codec: fd.get('codec'),
      display_mode: fd.get('display_mode'), monitor: fd.get('monitor') || '0',
      make_primary: !!fd.get('make_primary'), audio: !!fd.get('audio'), gamepads: !!fd.get('gamepads'),
      keyboard_mouse: !!fd.get('keyboard_mouse'), launch_steam: !!fd.get('launch_steam'),
      upnp: !!fd.get('upnp'), custom_domain: fd.get('custom_domain') || '', max_clients: +fd.get('max_clients') || 2,
      https_port: +fd.get('https_port') || 0, media_port: +fd.get('media_port') || 0,
    };
    await api('/api/settings', s);
    settingsDirty = false;
    $('#saved').classList.remove('hidden');
    await refresh();
  });
});

function syncSettingsUI() {
  $('#bitrate-out').textContent = `${form.bitrate_mbps.value} Mbps`;
  $('#monitor-row').classList.toggle('hidden', form.display_mode.value !== 'monitor');
}

function fillSettings(s) {
  if (settingsDirty) return;
  const res = form.resolution;
  if (![...res.options].some((o) => o.value === s.resolution)) res.add(new Option(s.resolution));
  res.value = s.resolution;
  form.fps.value = String(s.fps);
  form.bitrate_mbps.value = s.bitrate_mbps;
  form.encoder.value = s.encoder;
  form.codec.value = s.codec || 'auto';
  form.display_mode.value = s.display_mode === 'monitor' ? 'monitor' : 'virtual';
  for (const k of ['make_primary', 'audio', 'gamepads', 'keyboard_mouse', 'launch_steam', 'upnp']) form[k].checked = !!s[k];
  form.custom_domain.value = s.custom_domain || '';
  form.max_clients.value = s.max_clients;
  form.https_port.value = s.https_port;
  form.media_port.value = s.media_port;
  syncSettingsUI();
}

function fillMonitors(monitors, current) {
  const sel = form.monitor;
  if (settingsDirty) return;
  sel.innerHTML = '';
  for (const m of monitors || []) {
    sel.add(new Option(`${m.index}: ${m.monitor || m.name} (${m.size})${m.primary ? ' · main' : ''}`, String(m.index)));
  }
  if (!sel.options.length) sel.add(new Option('Primary monitor', '0'));
  sel.value = current || '0';
}

const depIcon = { ready: ['ready', '✓'], error: ['error', '!'], downloading: ['busy', ''], installing: ['busy', ''], pending: ['idle', ''], skipped: ['idle', ''] };

function renderDeps(deps) {
  const ul = $('#deps');
  ul.innerHTML = '';
  for (const d of deps) {
    const li = document.createElement('li');
    const [cls, glyph] = depIcon[d.state] || ['idle', ''];
    const dot = document.createElement('span'); dot.className = `dot ${cls}`; dot.textContent = glyph;
    const name = document.createElement('span'); name.textContent = d.name;
    li.append(dot, name);
    const sub = document.createElement('span'); sub.className = 'sub';
    sub.textContent = d.state === 'ready' ? d.purpose : d.state === 'error' ? d.detail : d.state === 'pending' ? 'Waiting…' : d.state === 'skipped' ? d.detail : (d.detail || d.state);
    li.append(sub);
    if (d.action === 'allow') {
      const b = document.createElement('button');
      b.className = 'ghost small-btn'; b.textContent = 'Allow…';
      b.addEventListener('click', async () => {
        try { await api('/api/permission', { id: d.id }); } catch (e) { alert(e.message); }
      });
      li.append(b);
    }
    if (d.state === 'downloading') {
      const bar = document.createElement('div'); bar.className = 'bar';
      const i = document.createElement('i'); i.style.width = `${Math.round(d.progress * 100)}%`;
      bar.append(i); li.append(bar);
    }
    ul.append(li);
  }
}

function renderUsers(users, me) {
  const ul = $('#users');
  ul.innerHTML = '';
  for (const u of users || []) {
    const li = document.createElement('li');
    const name = document.createElement('span');
    name.textContent = u.name + (u.name === me ? ' (you)' : '') + (u.totp ? '' : ' · no 2-step code');
    const btns = document.createElement('div');
    const out = document.createElement('button'); out.className = 'ghost'; out.textContent = 'Sign out devices';
    out.onclick = async () => { await api('/api/users/signout', { name: u.name }); out.textContent = 'Done ✓'; };
    btns.append(out);
    if (users.length > 1) {
      const del = document.createElement('button'); del.className = 'ghost'; del.textContent = 'Delete';
      del.onclick = async () => { if (confirm(`Delete ${u.name}?`)) { await api('/api/users/delete', { name: u.name }); refresh(); } };
      btns.append(del);
    }
    li.append(name, btns);
    ul.append(li);
  }
}

$('#form-add-1').addEventListener('submit', (ev) => {
  ev.preventDefault();
  submitting(ev.target, async (fd) => {
    const r = await api('/api/setup/begin', { username: fd.get('username'), password: fd.get('password') });
    addToken = r.token;
    $('#add-qr').src = r.qr;
    $('#add-secret').textContent = r.secret.replace(/(.{4})/g, '$1 ').trim();
    ev.target.classList.add('hidden');
    $('#form-add-2').classList.remove('hidden');
  });
});
$('#form-add-2').addEventListener('submit', (ev) => {
  ev.preventDefault();
  submitting(ev.target, async (fd) => {
    await api('/api/setup/finish', { token: addToken, code: fd.get('code').trim() });
    $('#form-add-1').reset(); ev.target.reset();
    $('#form-add-1').classList.remove('hidden'); ev.target.classList.add('hidden');
    $('#add-user').open = false;
    refresh();
  });
});

$('#btn-uninstall').addEventListener('click', async () => {
  if (!confirm('Uninstall Windstream from this PC?')) return;
  try {
    await api('/api/uninstall', { remove_data: $('#rm-data').checked });
    document.body.innerHTML = '<main class="narrow"><section class="card"><h1>Windstream is being removed</h1><p class="muted">You can close this tab.</p></section></main>';
  } catch (e) { alert(e.message); }
});

// On a Mac some settings do not apply (no virtual screen, controllers or
// AV1 encoder) and the encoder is Apple's.
let adaptedForMac = false;
function adaptForMac() {
  if (adaptedForMac) return;
  adaptedForMac = true;
  const f = $('#form-settings');
  f.encoder.innerHTML = '<option value="auto">Automatic (Apple media engine)</option>' +
    '<option value="videotoolbox">Apple VideoToolbox</option><option value="x264">Software (CPU)</option>';
  for (const o of [...f.codec.options]) if (o.value === 'av1') o.remove();
  for (const o of [...f.display_mode.options]) if (o.value === 'virtual') o.remove();
  for (const name of ['make_primary', 'gamepads']) f[name].closest('label').classList.add('hidden');
  $('#steam-row').classList.add('hidden');
  for (const el of document.querySelectorAll('h2')) if (el.textContent === 'This PC') el.textContent = 'This Mac';
  for (const el of document.querySelectorAll('label')) {
    for (const n of el.childNodes) if (n.nodeType === 3) n.textContent = n.textContent.replace('on this PC', 'on this Mac');
  }
}

function renderDash(s) {
  lastState = s;
  if (s.platform === 'darwin') adaptForMac();
  $('#who').textContent = s.user;
  const n = s.network || {};
  const link = $('#link');
  link.textContent = s.link || 'Working out your address…';
  link.href = s.link || '#';
  $('#link-qr').classList.toggle('hidden', !s.link_qr);
  if (s.link_qr) $('#link-qr').src = s.link_qr;
  let lanLine = '';
  if (n.public_url && s.link !== n.public_url) {
    lanLine = `This link works on your home network. Away from home, try ${n.public_url} on your phone with Wi-Fi off: this PC can't test that one from inside your home, but once it's used from outside, Windstream confirms it here.`;
  } else if (n.lan_url && n.lan_url !== s.link) {
    lanLine = n.seen_from_outside
      ? `✓ Confirmed working from outside your home. On your home Wi-Fi use ${n.lan_url} (your browser will show a certificate warning for this one).`
      : `On your home Wi-Fi you can also use ${n.lan_url} (your browser will show a certificate warning for this one).`;
  }
  $('#lan-line').textContent = lanLine;
  const warn = $('#net-warn');
  let w = '';
  if (n.cgnat) w = "Your internet provider shares your public IP with other customers (CGNAT), so this PC can't be reached directly from outside. It works on your home network; for outside access ask your ISP for a public IP.";
  else if (n.public_check === 'failed' && n.public_check_foreign && n.upnp === 'mapped') w = `When this PC opens its own internet link (${n.public_url}), your router answers itself instead (that's the "no-man zone" page). Many routers do this for devices at home, so the link may still work from outside: try it on your phone with Wi-Fi off.`;
  else if (n.public_check === 'failed' && n.public_check_foreign) w = `At your internet address (${n.public_url}) another device answers instead of this PC, usually the router's own page (for example a "no-man zone" error). Make sure the port forwards under "Internet access" point to this PC. Windstream re-checks every few minutes.`;
  else if (n.public_check === 'failed' && n.upnp === 'mapped') w = `Your router opened the ports, but this PC can't reach its own internet link (${n.public_url}). Many routers can't loop back to themselves, so it should still work from outside your home: try it on your phone with Wi-Fi off. At home, use ${n.lan_url}.`;
  else if (n.public_check === 'failed') w = `Your internet link (${n.public_url}) doesn't reach this PC yet: ${n.public_check_detail || 'something else answered'}. Until it does, the link below only works on your home network. Add the port forwards shown under "Internet access" (Windstream re-checks every few minutes).`;
  else if (n.upnp && n.upnp !== 'mapped' && n.upnp !== 'disabled') w = 'Automatic router setup failed. Add the port forwards shown under "Internet access" so the link works outside your home.';
  else if (!n.public_url && !s.dev) w = n.checked ? "Couldn't detect your public address. The home-network link still works." : 'Still detecting your public address…';
  warn.textContent = w; warn.classList.toggle('hidden', !w);
  const crash = $('#crash-note');
  crash.textContent = s.last_crash ? `Windstream hit a problem and restarted itself automatically (${s.last_crash}). Use "Download logs" under Troubleshooting to send the details.` : '';
  crash.classList.toggle('hidden', !s.last_crash);
  const cnote = $('#config-note');
  cnote.textContent = s.config_notice || '';
  cnote.classList.toggle('hidden', !s.config_notice);

  renderSignIns(s.sign_ins || []);
  renderUpdate(s.update);
  renderDeps(s.deps || []);
  const st = s.stream || {};
  const err = (s.https_error && `HTTPS server: ${s.https_error}`) || st.error || (st.stats && st.stats.error);
  $('#engine').textContent = err ? err : st.running ? `Ready (${st.mode === 'virtual' ? 'virtual screen, on while streaming' : st.mode === 'test' ? 'test pattern' : 'monitor'})` : 'Starting…';
  $('#clients').textContent = st.stats ? String(st.stats.clients) : '0';
  $('#encoder').textContent = (st.stats && st.stats.encoder) || 'chosen when someone connects';
  const aerr = st.stats && st.stats.audio_error;
  $('#audio').textContent = !s.settings || !s.settings.audio ? 'off'
    : st.audio_notice ? 'needs a loopback driver (hover for how)'
    : aerr ? 'no audio device found on this PC' : st.running && st.stats && st.stats.clients ? 'on' : 'on when streaming';
  $('#audio').title = st.audio_notice || aerr || '';
  $('#upnp').textContent = { mapped: 'Ports opened automatically ✓', disabled: 'Automatic setup off', unavailable: 'No UPnP router found', error: 'Router refused' }[n.upnp] || '…';
  $('#pubip').textContent = n.public_ip || (n.checked ? "Couldn't detect" : '…');
  $('#forward-why').textContent = n.upnp === 'disabled'
    ? 'Automatic router setup is off. To use the link outside your home, add these port forwards in your router:'
    : n.upnp === 'mapped'
      ? "If the link doesn't work from outside your home, check that your router has these port forwards (and that it is the only router between this PC and the internet):"
      : "Your router didn't accept automatic setup. Add these port forwards in its settings:";
  $('#hostname').textContent = n.hostname || '—';
  if (s.settings) $('#ports').textContent = `HTTPS ${s.settings.https_port}/TCP · media ${s.settings.media_port}/UDP`;
  $('#forward').classList.toggle('hidden', !(n.forward && n.forward.length));
  const fl = $('#forward-list'); fl.innerHTML = '';
  for (const f of n.forward || []) { const li = document.createElement('li'); li.textContent = f; fl.append(li); }

  $('#steam-row').classList.toggle('hidden', !s.steam_found);
  fillMonitors(s.monitors, s.settings && s.settings.monitor);
  if (s.settings) fillSettings(s.settings);
  renderUsers(s.users, s.user);
  const logs = $('#logs');
  const atBottom = logs.scrollTop + logs.clientHeight >= logs.scrollHeight - 4;
  logs.textContent = (s.logs || []).join('\n');
  if (atBottom) logs.scrollTop = logs.scrollHeight;
  $('#version').textContent = `Windstream ${s.version}`;
}

async function refresh() {
  try { state = await api('/api/state'); } catch { return; }
  $('#btn-logout').classList.toggle('hidden', !state.logged_in);
  if (state.setup_needed) { show('setup'); return; }
  if (!state.logged_in) { show('login'); $('#who').textContent = ''; return; }
  show('dash');
  renderDash(state);
}

refresh();
setInterval(() => { if (!document.hidden) refresh(); }, 2000);

// ---- troubleshooting ----
function logsNote(text, isError = false) {
  const n = $('#logs-note');
  n.textContent = text;
  n.classList.toggle('saved', !isError);
  n.classList.toggle('error', isError);
  n.classList.toggle('hidden', !text);
  if (text) setTimeout(() => { if (n.textContent === text) n.classList.add('hidden'); }, 6000);
}
$('#btn-logs-zip').addEventListener('click', () => {
  location.href = '/api/logs.zip'; // served as an attachment: the page stays put
  logsNote('Downloading… check your Downloads folder.');
});
$('#btn-logs-open').addEventListener('click', async () => {
  try {
    await api('/api/logs/open', {});
    logsNote('Opened in File Explorer ✓');
  } catch (e) { logsNote(e.message, true); }
});
$('#btn-logs-copy').addEventListener('click', async () => {
  const s = lastState || {};
  const text = [`Windstream ${s.version || ''}`, s.last_crash ? `Last crash: ${s.last_crash}` : '', ...(s.logs || []).slice(-200)]
    .filter(Boolean).join('\n');
  try {
    await navigator.clipboard.writeText(text);
    logsNote('Copied the last 200 log lines ✓');
  } catch {
    logsNote('Copy failed; use Download logs instead.', true);
  }
});

// Updates from the GitHub releases.
function renderUpdate(u) {
  const card = $('#update-card');
  card.classList.toggle('hidden', !u || !u.supported);
  if (!u || !u.supported) return;
  let line;
  if (u.state === 'downloading') line = `Downloading Windstream ${u.latest}… ${u.progress || 0}%`;
  else if (u.state === 'installing') line = `Installing Windstream ${u.latest}. Windstream restarts in a moment and this page reconnects by itself.`;
  else if (u.state === 'checking') line = 'Checking for updates…';
  else if (u.available) {
    line = `Windstream ${u.latest} is available (you have ${u.current}).`;
    if (u.unattended && u.auto) line += ' It installs by itself as soon as nobody is playing.';
  } else if (u.checked) line = `Windstream ${u.current} is up to date.`;
  else line = `Windstream ${u.current}. Windstream checks for updates a couple of minutes after it starts.`;
  $('#update-line').textContent = line;
  const busy = u.state === 'downloading' || u.state === 'installing';
  $('#btn-update').classList.toggle('hidden', !u.available || busy);
  $('#btn-update-check').disabled = busy || u.state === 'checking';
  $('#update-error').textContent = u.error || '';
  $('#update-auto-row').classList.toggle('hidden', !u.unattended);
  $('#update-auto').checked = !!u.auto;
  $('#update-mac-note').classList.toggle('hidden', u.unattended || !u.available);
}
$('#btn-update').addEventListener('click', async () => {
  try { await api('/api/update/install', {}); } catch (e) { $('#update-error').textContent = e.message; }
  refresh();
});
$('#btn-update-check').addEventListener('click', async () => {
  try { await api('/api/update/check', {}); } catch (e) { $('#update-error').textContent = e.message; }
  setTimeout(refresh, 1500);
});
$('#update-auto').addEventListener('change', async (ev) => {
  try { await api('/api/update/auto', { on: ev.target.checked }); } catch (e) { $('#update-error').textContent = e.message; }
  refresh();
});

// Who signed in (or tried to): newest first.
function renderSignIns(list) {
  const ul = $('#signins');
  ul.innerHTML = '';
  $('#signins-empty').classList.toggle('hidden', list.length > 0);
  for (const e of list.slice(0, 15)) {
    const li = document.createElement('li');
    const when = new Date(e.time);
    const what = e.result === 'ok' ? `${e.user} signed in`
      : e.result === 'rate_limited' ? 'Too many attempts, blocked for a minute'
      : `Wrong password or code${e.user ? ` for "${e.user}"` : ''}`;
    const times = e.count > 1 ? ` (${e.count} times)` : '';
    const where = isHomeIP(e.ip) ? 'from your home network' : `from ${e.ip}`;
    const span = document.createElement('span');
    span.className = e.result === 'ok' ? 'ok' : 'bad';
    span.textContent = `${what}${times} ${where}`;
    li.append(span);
    const t = document.createElement('time');
    t.dateTime = e.time;
    t.textContent = when.toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
    li.append(t);
    ul.append(li);
  }
}

function isHomeIP(ip) {
  return /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|127\.|::1$|fe80:|f[cd])/i.test(ip || '');
}
