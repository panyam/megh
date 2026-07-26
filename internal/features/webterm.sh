#!/usr/bin/env bash
# Feature: webterm — a mobile/tablet-optimized web terminal on :7682.
#
# The stock ttyd on :7681 stays untouched (the "full" page). This adds a SECOND
# page: a second ttyd instance whose --index is a custom xterm.js client with an
# on-screen key bar built for phones/tablets — Esc/Ctrl/Alt/Tab, arrows, the
# symbols buried three taps deep on soft keyboards, one-tap Ctrl-combos, a tmux
# row, paste, and a voice-dictation mic. It disables autocorrect/autocapitalize
# on the input so the keyboard stops fighting you.
#
# Both ttyd instances attach the SAME tmux session ('main'), so :7681 and :7682
# are two views of one shell. The page + its WebSocket are same-origin (one port,
# no reverse proxy), which is what keeps it robust over `tailscale serve` and SSH
# tunnels. xterm.js/css + the fit addon are INLINED into the page (the @@...@@
# markers below are replaced with the vendored bytes by features.Script before
# this script runs), so the page has zero CDN/network dependency — neither the box
# nor the client needs internet. Idempotent — re-running refreshes and restarts.
set -uo pipefail
log() { echo "[megh-webterm] $*"; }

PORT="${MEGH_WEBTERM_PORT:-7682}"
SESSION="${MEGH_TMUX_SESSION:-main}"
DIR="${MEGH_WEBTERM_DIR:-/opt/megh/webterm}"
mkdir -p "$DIR"

# --- the page (self-contained; ttyd --index serves ONLY this file at /) --------
cat > "$DIR/term.html" <<'HTML'
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no, viewport-fit=cover">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="mobile-web-app-capable" content="yes">
<title>megh · terminal</title>
<style>@@XTERM_CSS@@</style>
<style>
  :root { --bar-bg:#11151c; --key-bg:#1c232e; --key-fg:#c8d0da; --key-active:#3b82f6; --edge:#2a3340; }
  * { box-sizing:border-box; -webkit-tap-highlight-color:transparent; }
  html,body { margin:0; padding:0; height:100%; background:#0b0e14; color:#c5c8c6;
    font-family:ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,sans-serif;
    overscroll-behavior:none; }
  #app { display:flex; flex-direction:column; height:var(--vh,100dvh); }
  #term { flex:1 1 auto; min-height:0; padding:4px 4px 0; }
  #term .xterm { height:100%; }
  #keybar { flex:0 0 auto; background:var(--bar-bg); border-top:1px solid var(--edge);
    padding:4px 2px calc(4px + env(safe-area-inset-bottom)); user-select:none; }
  .krow { display:flex; gap:5px; overflow-x:auto; overflow-y:hidden; padding:3px 4px;
    scrollbar-width:none; -webkit-overflow-scrolling:touch; }
  .krow::-webkit-scrollbar { display:none; }
  .key { flex:0 0 auto; min-width:34px; height:38px; padding:0 9px; border:1px solid var(--edge);
    border-radius:8px; background:var(--key-bg); color:var(--key-fg); font-size:15px;
    font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"DejaVu Sans Mono",monospace;
    display:inline-flex; align-items:center; justify-content:center; touch-action:manipulation; }
  .key:active { background:#2a3340; }
  .key.active { background:var(--key-active); color:#fff; border-color:var(--key-active); }
  .key.rec { background:#dc2626; color:#fff; border-color:#dc2626; }
  #keybar.min .krow:not(:last-child) { display:none; }
  #overlay { position:fixed; inset:0; display:none; align-items:center; justify-content:center;
    background:rgba(11,14,20,.92); color:#c5c8c6; font-size:16px; text-align:center; z-index:10; }
  #overlay.show { display:flex; }
  #toast { position:fixed; left:50%; bottom:64px; transform:translateX(-50%); background:#1c232e;
    color:#e5e9f0; padding:8px 14px; border-radius:10px; border:1px solid var(--edge);
    font-size:14px; opacity:0; transition:opacity .2s; z-index:11; pointer-events:none; }
  #toast.show { opacity:1; }
</style>
</head>
<body>
<div id="app">
  <div id="term"></div>
  <div id="keybar"></div>
</div>
<div id="overlay" onclick="location.reload()">disconnected — tap to reconnect</div>
<div id="toast"></div>
<script>@@XTERM_JS@@</script>
<script>@@FIT_JS@@</script>
<script>
(function () {
  var enc = new TextEncoder(), dec = new TextDecoder();
  var term = new Terminal({
    cursorBlink: true, scrollback: 8000, fontSize: 14, allowProposedApi: true,
    fontFamily: 'ui-monospace,SFMono-Regular,Menlo,Consolas,"DejaVu Sans Mono",monospace',
    theme: { background: '#0b0e14', foreground: '#c5c8c6', cursor: '#c5c8c6' }
  });
  var fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(document.getElementById('term'));

  // Stop the mobile keyboard from "helping": no autocorrect/autocapitalize/spellcheck.
  var ta = term.textarea;
  if (ta) {
    ta.setAttribute('autocorrect', 'off');
    ta.setAttribute('autocapitalize', 'none');
    ta.setAttribute('autocomplete', 'off');
    ta.setAttribute('spellcheck', 'false');
  }
  document.getElementById('term').addEventListener('mouseup', function () { term.focus(); });

  // ---- viewport: keep the terminal sized above the soft keyboard --------------
  function layout() {
    var vh = (window.visualViewport ? window.visualViewport.height : window.innerHeight);
    document.documentElement.style.setProperty('--vh', vh + 'px');
    try { fit.fit(); } catch (e) {}
    sendResize();
  }
  if (window.visualViewport) window.visualViewport.addEventListener('resize', layout);
  window.addEventListener('resize', layout);
  window.addEventListener('orientationchange', function () { setTimeout(layout, 200); });

  // ---- ttyd WebSocket protocol (subprotocol 'tty') ----------------------------
  // client->server: '0'+data = input, '1'+json = resize, first frame = init json.
  // server->client: '0'+data = output, '1'+text = title, '2'+json = prefs (ignored)
  var ws = null, token = '';
  function connect() {
    var proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(proto + '://' + location.host + '/ws', ['tty']);
    ws.binaryType = 'arraybuffer';
    ws.onopen = function () {
      overlay(false);
      ws.send(enc.encode(JSON.stringify({ AuthToken: token, columns: term.cols, rows: term.rows })));
      setTimeout(layout, 60);
    };
    ws.onmessage = function (ev) {
      var d = new Uint8Array(ev.data);
      if (!d.length) return;
      var cmd = d[0], p = d.subarray(1);
      if (cmd === 48) term.write(p);                                   // '0' output
      else if (cmd === 49) { try { document.title = dec.decode(p); } catch (e) {} } // '1' title
    };
    ws.onclose = function () { overlay(true); };
    ws.onerror = function () {};
  }
  function rawSend(str) {
    if (!ws || ws.readyState !== 1 || !str) return;
    var body = enc.encode(str), buf = new Uint8Array(body.length + 1);
    buf[0] = 48; buf.set(body, 1);
    ws.send(buf);
    term.focus();
  }
  function sendResize() {
    if (!ws || ws.readyState !== 1) return;
    ws.send(enc.encode('1' + JSON.stringify({ columns: term.cols, rows: term.rows })));
  }

  // ---- sticky modifiers (Ctrl / Alt apply to the NEXT key) --------------------
  var mods = { ctrl: false, alt: false };
  function toggleMod(m) { mods[m] = !mods[m]; updateModUI(); term.focus(); }
  function updateModUI() {
    var bs = document.querySelectorAll('.key[data-mod]');
    for (var i = 0; i < bs.length; i++) bs[i].classList.toggle('active', !!mods[bs[i].dataset.mod]);
  }
  function applyMods(str) {
    if (!str) return str;
    var out = str, changed = false;
    if (mods.ctrl) { out = String.fromCharCode(out.charCodeAt(0) & 0x1f) + out.slice(1); mods.ctrl = false; changed = true; }
    if (mods.alt) { out = '\x1b' + out; mods.alt = false; changed = true; }
    if (changed) updateModUI();
    return out;
  }
  // Real keyboard input flows through the sticky modifiers too.
  term.onData(function (d) { rawSend(applyMods(d)); });
  function sendChar(c) { rawSend(applyMods(c)); }

  // ---- key bar ----------------------------------------------------------------
  var ROWS = [
    [
      { l: 'Esc', s: '\x1b' }, { l: 'Ctrl', mod: 'ctrl' }, { l: 'Alt', mod: 'alt' }, { l: 'Tab', s: '\t' },
      { l: '←', s: '\x1b[D' }, { l: '↓', s: '\x1b[B' }, { l: '↑', s: '\x1b[A' }, { l: '→', s: '\x1b[C' },
      { l: '^C', s: '\x03' }, { l: '^D', s: '\x04' }, { l: '^Z', s: '\x1a' }, { l: '^R', s: '\x12' }, { l: '^L', s: '\x0c' }
    ],
    ['|', '~', '/', '\\', '-', '_', '=', '+', '$', '*', '&', '^', '%', '#', '@', '!', '?', ':', ';', '"', "'", '`', '(', ')', '{', '}', '[', ']', '<', '>']
      .map(function (c) { return { l: c, c: c }; }),
    [
      { l: 'tmux', s: '\x02' }, { l: 'new', s: '\x02c' }, { l: '◀', s: '\x02p' }, { l: '▶', s: '\x02n' },
      { l: '─', s: '\x02"' }, { l: '│', s: '\x02%' }, { l: '⛶', s: '\x02z' },
      { l: 'Home', s: '\x1b[H' }, { l: 'End', s: '\x1b[F' }, { l: 'PgUp', s: '\x1b[5~' }, { l: 'PgDn', s: '\x1b[6~' },
      { l: '📋', act: 'paste' }, { l: '🎤', act: 'voice' }, { l: '⌨', act: 'toggle' }
    ]
  ];
  var bar = document.getElementById('keybar'), voiceBtn = null;
  ROWS.forEach(function (row) {
    var r = document.createElement('div'); r.className = 'krow';
    row.forEach(function (k) {
      var b = document.createElement('button'); b.className = 'key'; b.textContent = k.l;
      if (k.mod) b.dataset.mod = k.mod;
      if (k.act === 'voice') voiceBtn = b;
      var act = function () {
        if (k.mod) return toggleMod(k.mod);
        if (k.act === 'paste') return doPaste();
        if (k.act === 'voice') return toggleVoice();
        if (k.act === 'toggle') return toggleBar();
        if (k.c != null) return sendChar(k.c);
        if (k.s != null) return rawSend(k.s);
      };
      // preventDefault keeps focus on the terminal so the soft keyboard stays up.
      b.addEventListener('touchstart', function (e) { e.preventDefault(); act(); }, { passive: false });
      b.addEventListener('mousedown', function (e) { e.preventDefault(); act(); });
      r.appendChild(b);
    });
    bar.appendChild(r);
  });
  function toggleBar() { bar.classList.toggle('min'); setTimeout(layout, 60); }

  // ---- paste + voice ----------------------------------------------------------
  function doPaste() {
    var fallback = function () { var t = prompt('Paste text:'); if (t) rawSend(t); };
    if (navigator.clipboard && navigator.clipboard.readText) {
      navigator.clipboard.readText().then(function (t) { if (t) rawSend(t); }).catch(fallback);
    } else fallback();
  }
  var recog = null, recognizing = false;
  function toggleVoice() {
    var SR = window.SpeechRecognition || window.webkitSpeechRecognition;
    if (!SR) return toast('voice input not supported in this browser');
    if (recognizing) { if (recog) recog.stop(); return; }
    recog = new SR();
    recog.lang = navigator.language || 'en-US';
    recog.interimResults = false; recog.continuous = false;
    recog.onresult = function (e) { rawSend(e.results[0][0].transcript); };
    recog.onerror = function () { toast('voice error'); };
    recog.onend = function () { recognizing = false; if (voiceBtn) voiceBtn.classList.remove('rec'); };
    try { recog.start(); recognizing = true; if (voiceBtn) voiceBtn.classList.add('rec'); toast('listening…'); }
    catch (e) { recognizing = false; }
  }

  // ---- small ui helpers -------------------------------------------------------
  var overlayEl = document.getElementById('overlay');
  function overlay(show) { overlayEl.classList.toggle('show', show); }
  var toastEl = document.getElementById('toast'), toastT = null;
  function toast(msg) {
    toastEl.textContent = msg; toastEl.classList.add('show');
    if (toastT) clearTimeout(toastT);
    toastT = setTimeout(function () { toastEl.classList.remove('show'); }, 1800);
  }

  // ---- go ---------------------------------------------------------------------
  layout();
  fetch('token', { cache: 'no-store' })
    .then(function (r) { return r.ok ? r.json() : {}; })
    .then(function (j) { token = (j && j.token) || ''; })
    .catch(function () {})
    .then(function () { connect(); term.focus(); });
})();
</script>
</body>
</html>
HTML

log "wrote ${DIR}/term.html (self-contained; xterm.js inlined)"

# Emit-only: used at IMAGE BUILD time to bake the page into the image. Writes the
# page and stops here — no ttyd, no tailscale. The entrypoint then serves it
# directly, so the page is a first-class surface, not a boot-time generation.
if [ "${MEGH_WEBTERM_EMIT_ONLY:-0}" = "1" ]; then
  log "emit-only; not starting ttyd"
  exit 0
fi

if ! command -v ttyd >/dev/null 2>&1; then
  log "ttyd not found (it is baked into every megh image); cannot start webterm"
  exit 1
fi

# --- (re)start the second ttyd on its own port, same tmux session --------------
# Kill any prior webterm ttyd so an updated page is always picked up.
pkill -f "ttyd.*-p ${PORT}" 2>/dev/null && sleep 0.3 || true
ttyd -i 127.0.0.1 -p "${PORT}" -W -t titleFixed=megh-webterm \
  --index "${DIR}/term.html" tmux new -A -s "${SESSION}" >/tmp/ttyd-webterm.log 2>&1 &
log "webterm up on 127.0.0.1:${PORT} (mobile key bar; tmux session '${SESSION}')"

# --- serve on the tailnet if it is up (skipped when the entrypoint owns serve) -
if [ "${MEGH_WEBTERM_NO_SERVE:-0}" = "1" ]; then
  log "MEGH_WEBTERM_NO_SERVE set; leaving tailscale serve to the caller"
elif tailscale ip -4 >/dev/null 2>&1; then
  tailscale serve --bg --http="${PORT}" "http://127.0.0.1:${PORT}" >/tmp/ts-serve-webterm.log 2>&1 \
    && log "served on the tailnet at :${PORT} (open http://<box-name>:${PORT})" \
    || log "tailscale serve ${PORT} failed (see /tmp/ts-serve-webterm.log)"
else
  log "tailscale not up; reach via 'megh browse ${PORT}' (SSH tunnel)"
fi
