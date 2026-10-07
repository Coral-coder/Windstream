/* Windstream browser client: WebRTC playback + gamepad/keyboard/mouse forwarding. */
'use strict';

const $ = (sel) => document.querySelector(sel);
const MSG = { GAMEPAD_STATE: 0x01, GAMEPAD_CONNECT: 0x02, GAMEPAD_DISCONNECT: 0x03,
  KEY: 0x10, MOUSE_MOVE: 0x20, MOUSE_BUTTON: 0x21, MOUSE_WHEEL: 0x22, PING: 0x30, PONG: 0x31 };

const ui = {
  login: $('#login'), form: $('#login-form'), loginBtn: $('#login-btn'), loginError: $('#login-error'),
  totpRow: $('#totp-row'), stream: $('#stream'), stage: $('#stage'), video: $('#video'), audio: $('#audio'),
  status: $('#status'), stats: $('#stats'), pads: $('#pads'),
};

const state = {
  ws: null, pc: null, control: null, motion: null, caps: null,
  reconnectDelay: 1000, active: false, held: new Set(), pads: new Map(),
  rtt: null, lastStats: null, lastHeartbeat: 0, showStats: false, pointerLocked: false, fatalError: null,
  codec: null, padTimer: null,
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

// ---------- video codecs ----------
// The server streams AV1, HEVC or H.264. Prefer whatever this device decodes
// in hardware (fast, low latency, low power), most efficient first; then
// software decoders, cheapest first. ?codec=h264 forces one for testing.
const CODEC_MIME = { av1: 'video/AV1', h265: 'video/H265', h264: 'video/H264' };
async function detectCodecs() {
  const forced = new URLSearchParams(location.search).get('codec');
  if (forced && CODEC_MIME[forced]) return [forced];
  let caps = [];
  try { caps = (RTCRtpReceiver.getCapabilities('video') || {}).codecs || []; } catch { /* old browser */ }
  if (!caps.length) return ['h264'];
  const decodable = Object.keys(CODEC_MIME).filter((k) => caps.some((c) => c.mimeType.toLowerCase() === CODEC_MIME[k].toLowerCase()));
  const hw = {};
  await Promise.all(decodable.map(async (k) => {
    try {
      const info = await navigator.mediaCapabilities.decodingInfo({ type: 'webrtc',
        video: { contentType: CODEC_MIME[k], width: 1920, height: 1080, bitrate: 20e6, framerate: 60 } });
      hw[k] = !!(info.supported && info.powerEfficient);
    } catch { hw[k] = false; }
  }));
  return [...['av1', 'h265', 'h264'].filter((k) => decodable.includes(k) && hw[k]),
    ...['h264', 'av1', 'h265'].filter((k) => decodable.includes(k) && !hw[k])];
}
const codecsReady = detectCodecs().catch(() => ['h264']);

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
  ws.onopen = async () => {
    const codecs = await codecsReady;
    if (state.ws === ws) signal({ type: 'hello', codecs });
  };
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
      if (msg.codec) state.codec = msg.codec;
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

// minimizeBuffering asks the browser to hold as little received media as
// possible. These hints reduce the receive-side latency a lot; support
// varies by browser, so they are re-applied periodically below.
function minimizeBuffering(receiver) {
  if (!receiver) return;
  try { receiver.playoutDelayHint = 0; } catch { /* unsupported */ }
  try { receiver.jitterBufferTarget = 0; } catch { /* unsupported */ }
}

function createPeer(iceServers) {
  teardownPeer();
  const pc = new RTCPeerConnection({ iceServers, bundlePolicy: 'max-bundle', rtcpMuxPolicy: 'require' });
  state.pc = pc;
  // Audio plays from its own element and stream so the browser never delays
  // video frames to lip-sync them with the (more buffered) audio.
  pc.ontrack = (ev) => {
    minimizeBuffering(ev.receiver);
    const el = ev.track.kind === 'audio' ? ui.audio : ui.video;
    el.srcObject = new MediaStream([ev.track]);
    el.play().catch(() => {});
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
  ui.video.srcObject = null; ui.audio.srcObject = null;
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
  startPadPolling();
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

// Poll every 4 ms (browsers sample controllers at up to 250 Hz) instead of
// once per animation frame, which would add up to a frame of input lag.
function startPadPolling() {
  if (!state.padTimer) state.padTimer = setInterval(() => pollPads(performance.now()), 4);
}

function pollPads(now) {
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

// Mouse motion is sent the moment it happens. pointerrawupdate (Chrome,
// Edge) delivers raw movement at the mouse's polling rate without waiting for
// the next animation frame; other browsers fall back to mousemove.
const RAW_POINTER = 'onpointerrawupdate' in window;
function sendMove(ev) {
  if (!state.pointerLocked) return;
  const dx = Math.max(-32768, Math.min(32767, Math.round(ev.movementX)));
  const dy = Math.max(-32768, Math.min(32767, Math.round(ev.movementY)));
  if (dx === 0 && dy === 0) return;
  const dv = new DataView(new ArrayBuffer(5));
  dv.setUint8(0, MSG.MOUSE_MOVE); dv.setInt16(1, dx, true); dv.setInt16(3, dy, true);
  sendMotion(dv.buffer);
}
ui.stage.addEventListener(RAW_POINTER ? 'pointerrawupdate' : 'mousemove', sendMove);
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
  ui.audio.muted = !ui.audio.muted;
  ev.currentTarget.textContent = ui.audio.muted ? '🔇' : '🔊';
  ui.audio.play().catch(() => {});
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
setInterval(() => { updateStats().catch((e) => console.warn('stats', e)); }, 1000);

async function updateStats() {
  if (!state.pc) return;
  const dv = new DataView(new ArrayBuffer(5));
  dv.setUint8(0, MSG.PING); dv.setUint32(1, performance.now() & 0xffffffff, true);
  sendControl(dv.buffer);
  for (const r of state.pc.getReceivers()) minimizeBuffering(r);
  if (!state.showStats) return;
  const pc = state.pc;
  const report = await pc.getStats();
  if (pc !== state.pc) return; // reconnected meanwhile
  let video = null, pair = null;
  report.forEach((s) => {
    if (s.type === 'inbound-rtp' && s.kind === 'video') video = s;
    if (s.type === 'candidate-pair' && s.nominated && s.state === 'succeeded') pair = s;
  });
  const prev = state.lastStats;
  state.lastStats = video ? { t: performance.now(), bytes: video.bytesReceived, frames: video.framesDecoded,
    decode: video.totalDecodeTime || 0, processing: video.totalProcessingDelay || 0 } : null;
  const lines = [];
  if (video && prev) {
    const dt = (performance.now() - prev.t) / 1000;
    const codec = video.codecId && report.get(video.codecId);
    const df = video.framesDecoded - prev.frames;
    lines.push(`video   ${video.frameWidth || 0}x${video.frameHeight || 0} ${(df / dt).toFixed(0)} fps`);
    lines.push(`codec   ${codec && codec.mimeType ? codec.mimeType.replace('video/', '') : state.codec || '?'} ${video.powerEfficientDecoder ? '(hw decode)' : '(sw decode)'}`);
    if (df > 0) lines.push(`decode  ${(((video.totalDecodeTime || 0) - prev.decode) / df * 1000).toFixed(1)} ms  recv→decoded ${(((video.totalProcessingDelay || 0) - prev.processing) / df * 1000).toFixed(1)} ms`);
    lines.push(`bitrate ${(((video.bytesReceived - prev.bytes) * 8) / dt / 1e6).toFixed(1)} Mbps`);
    lines.push(`loss    ${video.packetsLost || 0} pkts  nack ${video.nackCount || 0}  pli ${video.pliCount || 0}`);
    if (video.jitterBufferEmittedCount) lines.push(`jitter  ${((video.jitterBufferDelay / video.jitterBufferEmittedCount) * 1000).toFixed(0)} ms buffer`);
    lines.push(`dropped ${video.framesDropped || 0} frames`);
  }
  if (pair && pair.currentRoundTripTime != null) lines.push(`rtt     ${(pair.currentRoundTripTime * 1000).toFixed(0)} ms (ice)`);
  if (state.rtt != null) lines.push(`rtt     ${state.rtt.toFixed(0)} ms (input)`);
  lines.push(`pads    ${state.pads.size}`);
  ui.stats.textContent = lines.join('\n') || 'collecting…';
}

boot();
