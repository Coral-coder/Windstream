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

function renderDash(s) {
  lastState = s;
  $('#who').textContent = s.user;
  const n = s.network || {};
  const link = $('#link');
  link.textContent = s.link || 'Working out your address…';
  link.href = s.link || '#';
  $('#link-qr').classList.toggle('hidden', !s.link_qr);
  if (s.link_qr) $('#link-qr').src = s.link_qr;
  $('#lan-line').textContent = n.lan_url && n.lan_url !== s.link ? `On your home Wi-Fi you can also use ${n.lan_url} (your browser will show a certificate warning for this one).` : '';
  const warn = $('#net-warn');
  let w = '';
  if (n.cgnat) w = "Your internet provider shares your public IP with other customers (CGNAT), so this PC can't be reached directly from outside. It works on your home network; for outside access ask your ISP for a public IP.";
  else if (n.upnp && n.upnp !== 'mapped' && n.upnp !== 'disabled') w = 'Automatic router setup failed. Add the port forwards shown under "Internet access" so the link works outside your home.';
  else if (!n.public_url && !s.dev) w = n.checked ? "Couldn't detect your public address. The home-network link still works." : 'Still detecting your public address…';
  warn.textContent = w; warn.classList.toggle('hidden', !w);
  const crash = $('#crash-note');
  crash.textContent = s.last_crash ? `Windstream hit a problem and restarted itself automatically (${s.last_crash}). Use "Download logs" under Troubleshooting to send the details.` : '';
  crash.classList.toggle('hidden', !s.last_crash);

  renderDeps(s.deps || []);
  const st = s.stream || {};
  const err = (s.https_error && `HTTPS server: ${s.https_error}`) || st.error || (st.stats && st.stats.error);
  $('#engine').textContent = err ? err : st.running ? `Ready (${st.mode === 'virtual' ? 'virtual screen, on while streaming' : st.mode === 'test' ? 'test pattern' : 'monitor'})` : 'Starting…';
  $('#clients').textContent = st.stats ? String(st.stats.clients) : '0';
  $('#encoder').textContent = (st.stats && st.stats.encoder) || 'chosen when someone connects';
  const aerr = st.stats && st.stats.audio_error;
  $('#audio').textContent = !s.settings || !s.settings.audio ? 'off'
    : aerr ? 'no audio device found on this PC' : st.running && st.stats && st.stats.clients ? 'on' : 'on when streaming';
  $('#audio').title = aerr || '';
  $('#upnp').textContent = { mapped: 'Ports opened automatically ✓', disabled: 'Automatic setup off', unavailable: 'No UPnP router found', error: 'Router refused' }[n.upnp] || '…';
  $('#pubip').textContent = n.public_ip || (n.checked ? "Couldn't detect" : '…');
  $('#forward-why').textContent = n.upnp === 'disabled'
    ? 'Automatic router setup is off. To use the link outside your home, add these port forwards in your router:'
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
