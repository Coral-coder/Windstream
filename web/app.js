/* Windstream browser client: WebRTC playback + gamepad/keyboard/mouse forwarding. */
'use strict';

const $ = (sel) => document.querySelector(sel);
const MSG = { GAMEPAD_STATE: 0x01, GAMEPAD_CONNECT: 0x02, GAMEPAD_DISCONNECT: 0x03,
  KEY: 0x10, MOUSE_MOVE: 0x20, MOUSE_BUTTON: 0x21, MOUSE_WHEEL: 0x22, PING: 0x30, PONG: 0x31 };

const ui = {
  login: $('#login'), form: $('#login-form'), loginBtn: $('#login-btn'), loginError: $('#login-error'),
  totpRow: $('#totp-row'), stream: $('#stream'), stage: $('#stage'), video: $('#video'),
  status: $('#status'), stats: $('#stats'), pads: $('#pads'),
};

const state = {
  ws: null, pc: null, control: null, motion: null, caps: null,
  reconnectDelay: 1000, active: false, held: new Set(), pads: new Map(),
  rtt: null, lastStats: null, lastHeartbeat: 0, showStats: false, pointerLocked: false, fatalError: null,
};

const enc = new TextEncoder();

// ---------- helpers ----------
async function api(path, body) {
  const res = await fetch(path, {
    method: body ? 'POST' : 'GET',
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  });
  let data = null;
  try { data = await res.json(); } catch { /* no body */ }
  return { ok: res.ok, status: res.status, data };
}

function setStatus(text) {
  if (state.fatalError && text !== state.fatalError) return; // keep the fatal message on screen
  if (!text) { ui.status.classList.add('hidden'); return; }
  ui.status.textContent = text;
  ui.status.classList.remove('hidden');
}

function sendControl(buf) {
  if (state.control && state.control.readyState === 'open') state.control.send(buf);
}
function sendMotion(buf) {
  const ch = (state.motion && state.motion.readyState === 'open') ? state.motion : state.control;
  if (ch && ch.readyState === 'open' && ch.bufferedAmount < 16384) ch.send(buf);
}

// ---------- login ----------
ui.form.addEventListener('submit', async (ev) => {
  ev.preventDefault();
  ui.loginBtn.disabled = true;
  ui.loginError.textContent = '';
  const fd = new FormData(ui.form);
  const { ok, status, data } = await api('/api/login', {
    username: fd.get('username'), password: fd.get('password'), code: fd.get('code') || '',
  });
  ui.loginBtn.disabled = false;
  if (ok) { ui.form.reset(); showStream(); return; }
  const code = data && data.error;
  if (code === 'totp_required') {
    ui.totpRow.classList.remove('hidden');
    ui.totpRow.querySelector('input').focus();
    ui.loginError.textContent = 'Enter the code from your authenticator app.';
  } else if (status === 429) {
    ui.loginError.textContent = 'Too many attempts. Wait a minute and try again.';
  } else if (code === 'bad_origin') {
    ui.loginError.textContent = 'Request blocked: origin mismatch.';
  } else {
    ui.loginError.textContent = 'Invalid username, password or code.';
  }
});

async function boot() {
  const me = await api('/api/me');
  if (me.ok) showStream(); else showLogin();
}

function showLogin() {
  state.active = false;
  teardown();
  ui.stream.classList.add('hidden');
  ui.login.classList.remove('hidden');
}

function showStream() {
  ui.login.classList.add('hidden');
  ui.stream.classList.remove('hidden');
  state.active = true;
  state.reconnectDelay = 1000;
  connect();
}

// ---------- signaling + WebRTC ----------
function connect() {
  if (!state.active) return;
  state.fatalError = null;
  setStatus('Connecting…');
  const ws = new WebSocket(`wss://${location.host}/api/signal`);
  state.ws = ws;
  ws.onmessage = async (ev) => {
    let msg;
    try { msg = JSON.parse(ev.data); } catch { return; }
    try { await handleSignal(msg); } catch (e) { console.error('signal', msg.type, e); setStatus(`Error: ${e.message}`); }
  };
  ws.onclose = (ev) => {
    if (state.ws !== ws) return;
    state.ws = null;
    teardownPeer();
    if (ev.code === 1003) { // unsupported data: negotiation cannot succeed; do not retry
      setStatus(state.fatalError || 'Media negotiation failed.');
      return;
    }
    if (ev.code === 1008) { // policy violation: session expired or refused
      if (!state.active) return;
      setStatus(ev.reason === 'refused' ? 'Server is full. Retrying…' : 'Session expired. Please sign in again.');
      if (ev.reason !== 'refused') { showLogin(); return; }
    }
    if (!state.active) return;
    setStatus(`Disconnected. Reconnecting in ${Math.round(state.reconnectDelay / 1000)}s…`);
    setTimeout(connect, state.reconnectDelay);
    state.reconnectDelay = Math.min(state.reconnectDelay * 2, 15000);
  };
  ws.onerror = () => {};
}

function signal(msg) {
  if (state.ws && state.ws.readyState === WebSocket.OPEN) state.ws.send(JSON.stringify(msg));
}

async function handleSignal(msg) {
  switch (msg.type) {
    case 'config': {
      state.caps = msg.input || null;
      createPeer(msg.iceServers || []);
      break;
    }
    case 'offer': {
      if (!state.pc) createPeer([]);
      await state.pc.setRemoteDescription({ type: 'offer', sdp: msg.sdp });
      const answer = await state.pc.createAnswer();
      await state.pc.setLocalDescription(answer);
      signal({ type: 'answer', sdp: answer.sdp });
      break;
    }
    case 'candidate':
      if (state.pc && msg.candidate) await state.pc.addIceCandidate(msg.candidate).catch(() => {});
      break;
    case 'error':
      state.fatalError = msg.message;
      setStatus(msg.message);
      break;
  }
}

function createPeer(iceServers) {
  teardownPeer();
  const pc = new RTCPeerConnection({ iceServers, bundlePolicy: 'max-bundle', rtcpMuxPolicy: 'require' });
  state.pc = pc;
  const stream = new MediaStream();
  pc.ontrack = (ev) => {
    stream.addTrack(ev.track);
    ui.video.srcObject = stream;
    try { ev.receiver.playoutDelayHint = 0; } catch { /* unsupported */ }
    try { ev.receiver.jitterBufferTarget = 0; } catch { /* unsupported */ }
    ui.video.play().catch(() => {});
  };
  pc.onicecandidate = (ev) => { if (ev.candidate) signal({ type: 'candidate', candidate: ev.candidate.toJSON() }); };
  pc.ondatachannel = (ev) => {
    const ch = ev.channel;
    ch.binaryType = 'arraybuffer';
    if (ch.label === 'control') {
      state.control = ch;
      ch.onopen = () => { announcePads(); };
      ch.onmessage = (m) => onChannelMessage(m.data);
    } else if (ch.label === 'state') {
      state.motion = ch;
      ch.onmessage = (m) => onChannelMessage(m.data);
    }
  };
  pc.onconnectionstatechange = () => {
    const s = pc.connectionState;
    if (s === 'connected') { setStatus(''); state.reconnectDelay = 1000; }
    else if (s === 'connecting') setStatus('Negotiating media…');
    else if (s === 'failed') { setStatus('Connection failed. Retrying…'); if (state.ws) state.ws.close(); }
    else if (s === 'disconnected') setStatus('Connection interrupted…');
  };
}

function onChannelMessage(data) {
  const b = new Uint8Array(data);
  if (b.length >= 5 && b[0] === MSG.PONG) {
    const sent = new DataView(b.buffer, b.byteOffset).getUint32(1, true);
    state.rtt = (performance.now() & 0xffffffff) - sent;
    if (state.rtt < 0) state.rtt += 0x100000000;
  }
}

function teardownPeer() {
  if (state.pc) { try { state.pc.close(); } catch { /* ignore */ } }
  state.pc = null; state.control = null; state.motion = null;
  ui.video.srcObject = null;
}

function teardown() {
  teardownPeer();
  if (state.ws) { const ws = state.ws; state.ws = null; ws.close(); }
}

// ---------- gamepads ----------
function padState(gp) {
  let buttons = 0;
  const n = Math.min(gp.buttons.length, 17);
  for (let i = 0; i < n; i++) if (gp.buttons[i].pressed) buttons |= (1 << i);
  const axis = (v) => Math.max(-32768, Math.min(32767, Math.round((v || 0) * 32767)));
  const trig = (i) => Math.round(Math.max(0, Math.min(1, gp.buttons[i] ? gp.buttons[i].value : 0)) * 255);
  return { buttons, axes: [axis(gp.axes[0]), axis(gp.axes[1]), axis(gp.axes[2]), axis(gp.axes[3])], triggers: [trig(6), trig(7)] };
}

function sameState(a, b) {
  return a.buttons === b.buttons && a.triggers[0] === b.triggers[0] && a.triggers[1] === b.triggers[1]
    && a.axes.every((v, i) => v === b.axes[i]);
}

function encodePad(index, st) {
  const buf = new ArrayBuffer(16);
  const dv = new DataView(buf);
  dv.setUint8(0, MSG.GAMEPAD_STATE); dv.setUint8(1, index);
  dv.setUint32(2, st.buttons >>> 0, true);
  for (let i = 0; i < 4; i++) dv.setInt16(6 + 2 * i, st.axes[i], true);
  dv.setUint8(14, st.triggers[0]); dv.setUint8(15, st.triggers[1]);
  return buf;
}

function slotFor(gp) {
  // Map browser gamepad indices onto a dense 0..N-1 range the server expects.
  if (state.pads.has(gp.index)) return state.pads.get(gp.index).slot;
  const used = new Set([...state.pads.values()].map((p) => p.slot));
  let slot = 0;
  while (used.has(slot)) slot++;
  return slot;
}

function announcePads() {
  for (const [, p] of state.pads) {
    const name = enc.encode(p.id.slice(0, 48));
    const buf = new Uint8Array(2 + name.length);
    buf[0] = MSG.GAMEPAD_CONNECT; buf[1] = p.slot; buf.set(name, 2);
    sendControl(buf);
    p.last = null;
  }
  updatePadBadge();
}

function updatePadBadge() {
  ui.pads.textContent = `🎮 ${state.pads.size}`;
  ui.pads.title = [...state.pads.values()].map((p) => `${p.slot}: ${p.id}`).join('\n') || 'No controllers. Press a button on one to connect it.';
}

window.addEventListener('gamepadconnected', (ev) => {
  const gp = ev.gamepad;
  const max = state.caps ? state.caps.maxGamepads : 4;
  const slot = slotFor(gp);
  if (slot >= max) return;
  state.pads.set(gp.index, { slot, id: gp.id, last: null });
  const name = enc.encode(gp.id.slice(0, 48));
  const buf = new Uint8Array(2 + name.length);
  buf[0] = MSG.GAMEPAD_CONNECT; buf[1] = slot; buf.set(name, 2);
  sendControl(buf);
  updatePadBadge();
});

window.addEventListener('gamepaddisconnected', (ev) => {
  const p = state.pads.get(ev.gamepad.index);
  if (!p) return;
  state.pads.delete(ev.gamepad.index);
  sendControl(new Uint8Array([MSG.GAMEPAD_DISCONNECT, p.slot]));
  updatePadBadge();
});

function pollPads(now) {
  requestAnimationFrame(pollPads);
  if (!state.control || state.control.readyState !== 'open') return;
  const heartbeat = now - state.lastHeartbeat > 100;
  if (heartbeat) state.lastHeartbeat = now;
  for (const gp of navigator.getGamepads()) {
    if (!gp) continue;
    const p = state.pads.get(gp.index);
    if (!p) continue;
    const st = padState(gp);
    if (!p.last || heartbeat || !sameState(st, p.last)) {
      p.last = st;
      sendMotion(encodePad(p.slot, st));
    }
  }
}
requestAnimationFrame(pollPads);

// ---------- keyboard ----------
const PASSTHROUGH_KEYS = new Set(['F11']);
function keyEvent(ev, down) {
  if (!state.active || !state.caps || !state.caps.keyboard) return;
  if (document.activeElement && ['INPUT', 'TEXTAREA'].includes(document.activeElement.tagName)) return;
  if (PASSTHROUGH_KEYS.has(ev.code)) return;
  if (down && ev.repeat) { ev.preventDefault(); return; }
  if (ev.code === 'Escape' && !state.pointerLocked && !down) return; // let Escape leave fullscreen
  if (down) state.held.add(ev.code); else state.held.delete(ev.code);
  const code = enc.encode(ev.code);
  const buf = new Uint8Array(2 + code.length);
  buf[0] = MSG.KEY; buf[1] = down ? 1 : 0; buf.set(code, 2);
  sendControl(buf);
  ev.preventDefault();
}
window.addEventListener('keydown', (ev) => keyEvent(ev, true));
window.addEventListener('keyup', (ev) => keyEvent(ev, false));
window.addEventListener('blur', () => {
  for (const code of state.held) {
    const c = enc.encode(code);
    const buf = new Uint8Array(2 + c.length);
    buf[0] = MSG.KEY; buf[1] = 0; buf.set(c, 2);
    sendControl(buf);
  }
  state.held.clear();
});

// ---------- mouse ----------
ui.stage.addEventListener('click', () => {
  if (state.caps && state.caps.mouse && !state.pointerLocked && ui.stage.requestPointerLock) {
    ui.stage.requestPointerLock({ unadjustedMovement: true }).catch?.(() => ui.stage.requestPointerLock());
  }
});
document.addEventListener('pointerlockchange', () => {
  state.pointerLocked = document.pointerLockElement === ui.stage;
  ui.stage.classList.toggle('show-cursor', !state.pointerLocked);
});
ui.stage.classList.add('show-cursor');

let accX = 0, accY = 0, moveScheduled = false;
ui.stage.addEventListener('mousemove', (ev) => {
  if (!state.pointerLocked) return;
  accX += ev.movementX; accY += ev.movementY;
  if (!moveScheduled) {
    moveScheduled = true;
    requestAnimationFrame(() => {
      moveScheduled = false;
      const dx = Math.max(-32768, Math.min(32767, accX)), dy = Math.max(-32768, Math.min(32767, accY));
      accX = 0; accY = 0;
      if (dx === 0 && dy === 0) return;
      const dv = new DataView(new ArrayBuffer(5));
      dv.setUint8(0, MSG.MOUSE_MOVE); dv.setInt16(1, dx, true); dv.setInt16(3, dy, true);
      sendMotion(dv.buffer);
    });
  }
});
function mouseButton(ev, down) {
  if (!state.pointerLocked) return;
  ev.preventDefault();
  sendControl(new Uint8Array([MSG.MOUSE_BUTTON, ev.button, down ? 1 : 0]));
}
ui.stage.addEventListener('mousedown', (ev) => mouseButton(ev, true));
ui.stage.addEventListener('mouseup', (ev) => mouseButton(ev, false));
ui.stage.addEventListener('contextmenu', (ev) => ev.preventDefault());
ui.stage.addEventListener('wheel', (ev) => {
  if (!state.pointerLocked) return;
  ev.preventDefault();
  const dv = new DataView(new ArrayBuffer(5));
  dv.setUint8(0, MSG.MOUSE_WHEEL);
  dv.setInt16(1, Math.sign(ev.deltaX), true);
  dv.setInt16(3, -Math.sign(ev.deltaY), true);
  sendMotion(dv.buffer);
}, { passive: false });

// ---------- toolbar ----------
$('#btn-fullscreen').addEventListener('click', () => {
  if (document.fullscreenElement) document.exitFullscreen();
  else ui.stream.requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
});
$('#btn-mute').addEventListener('click', (ev) => {
  ui.video.muted = !ui.video.muted;
  ev.currentTarget.textContent = ui.video.muted ? '🔇' : '🔊';
  ui.video.play().catch(() => {});
});
$('#btn-stats').addEventListener('click', () => {
  state.showStats = !state.showStats;
  ui.stats.classList.toggle('hidden', !state.showStats);
});
$('#btn-logout').addEventListener('click', async () => {
  signal({ type: 'bye' });
  await api('/api/logout', {});
  showLogin();
});

// ---------- stats ----------
setInterval(async () => {
  if (!state.pc) return;
  const dv = new DataView(new ArrayBuffer(5));
  dv.setUint8(0, MSG.PING); dv.setUint32(1, performance.now() & 0xffffffff, true);
  sendControl(dv.buffer);
  if (!state.showStats) return;
  const report = await state.pc.getStats();
  let video = null, pair = null;
  report.forEach((s) => {
    if (s.type === 'inbound-rtp' && s.kind === 'video') video = s;
    if (s.type === 'candidate-pair' && s.nominated && s.state === 'succeeded') pair = s;
  });
  const prev = state.lastStats;
  state.lastStats = video ? { t: performance.now(), bytes: video.bytesReceived, frames: video.framesDecoded } : null;
  const lines = [];
  if (video && prev) {
    const dt = (performance.now() - prev.t) / 1000;
    lines.push(`video   ${video.frameWidth || 0}x${video.frameHeight || 0} ${((video.framesDecoded - prev.frames) / dt).toFixed(0)} fps`);
    lines.push(`bitrate ${(((video.bytesReceived - prev.bytes) * 8) / dt / 1e6).toFixed(1)} Mbps`);
    lines.push(`loss    ${video.packetsLost || 0} pkts  nack ${video.nackCount || 0}  pli ${video.pliCount || 0}`);
    if (video.jitterBufferEmittedCount) lines.push(`jitter  ${((video.jitterBufferDelay / video.jitterBufferEmittedCount) * 1000).toFixed(0)} ms buffer`);
    lines.push(`dropped ${video.framesDropped || 0} frames`);
  }
  if (pair && pair.currentRoundTripTime != null) lines.push(`rtt     ${(pair.currentRoundTripTime * 1000).toFixed(0)} ms (ice)`);
  if (state.rtt != null) lines.push(`rtt     ${state.rtt.toFixed(0)} ms (input)`);
  lines.push(`pads    ${state.pads.size}`);
  ui.stats.textContent = lines.join('\n') || 'collecting…';
}, 1000);

boot();
