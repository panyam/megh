// meghplane page. Every string from the server goes into the DOM through
// textContent or an attribute, never innerHTML, and the CSP forbids inline
// script, so nothing a backend returns can run here.
"use strict";

const STORE = {
  RUNPOD_API_KEY: "megh.runpod",
  MEGH_TAILSCALE_CLIENT_ID: "megh.tsid",
  MEGH_TAILSCALE_CLIENT_SECRET: "megh.tssecret",
};
const HEADERS = {
  "megh.runpod": "X-Megh-Runpod-Key",
  "megh.tsid": "X-Megh-Ts-Client-Id",
  "megh.tssecret": "X-Megh-Ts-Client-Secret",
};

const $ = (id) => document.getElementById(id);

function get(k) {
  try { return sessionStorage.getItem(k) || ""; } catch (e) { return ""; }
}

function set(k, v) {
  try { v ? sessionStorage.setItem(k, v) : sessionStorage.removeItem(k); } catch (e) {}
}

// parseKeys accepts the note as pasted: KEY=value assignments, several to a
// line or one per line, with or without "export", quotes or comments. Unknown
// names are ignored.
function parseKeys(text) {
  const out = {};
  for (const line of text.split(/\r?\n/)) {
    for (const word of shellWords(line)) {
      const eq = word.indexOf("=");
      if (eq < 1) continue; // "export", or any other bare word
      const name = word.slice(0, eq);
      if (STORE[name]) out[STORE[name]] = word.slice(eq + 1);
    }
  }
  return out;
}

// shellWords splits one line roughly as a shell would for simple assignments:
// whitespace and ";" separate words, '...' and "..." group (the quotes are
// dropped), and an unquoted "#" at the start of a word begins a comment. A "#"
// inside a word is kept, since a key may contain one.
function shellWords(line) {
  const words = [];
  let cur = "", inWord = false, quote = "";
  for (const c of line) {
    if (quote) {
      if (c === quote) quote = "";
      else cur += c;
      continue;
    }
    if (c === "'" || c === '"') { quote = c; inWord = true; continue; }
    if (c === "#" && !inWord) break;
    if (c === " " || c === "\t" || c === ";") {
      if (inWord) { words.push(cur); cur = ""; inWord = false; }
      continue;
    }
    cur += c;
    inWord = true;
  }
  if (inWord) words.push(cur);
  return words;
}

// server is what GET /api/keys reported the server holds (booleans only).
let server = { runpod: false, tailscale: false };

function haveKeys() { return server.runpod || get("megh.runpod") !== ""; }
function haveLocalKeys() { return Object.keys(HEADERS).some((k) => get(k) !== ""); }

async function loadServerKeys() {
  try {
    const res = await fetch("/api/keys");
    const json = await res.json();
    server = json.server || server;
    if (json.error) showLog(json.error, true);
  } catch (e) { /* no server keys; the page asks for them */ }
}

function showLog(text, isError) {
  const el = $("log");
  el.textContent = text;
  el.hidden = !text;
  el.className = isError ? "error" : "";
}

async function call(method, path, body) {
  const headers = {};
  for (const k in HEADERS) {
    const v = get(k);
    if (v) headers[HEADERS[k]] = v;
  }
  if (body) headers["Content-Type"] = "application/json";
  const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
  let json = {};
  try { json = await res.json(); } catch (e) { json = { error: res.status + " " + res.statusText }; }
  if (!res.ok) {
    const err = new Error(json.error || res.statusText);
    err.log = json.log || "";
    throw err;
  }
  return json;
}

function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function link(label, url) {
  const a = el("a", label);
  if (/^https?:\/\//.test(url)) a.href = url;
  a.rel = "noopener noreferrer";
  a.target = "_blank";
  return a;
}

function copyable(label, cmd) {
  const row = el("div", "", "cmd");
  row.append(el("span", label + " "), el("code", cmd));
  const b = el("button", "copy");
  b.type = "button";
  b.addEventListener("click", () => navigator.clipboard && navigator.clipboard.writeText(cmd));
  row.append(b);
  return row;
}

function render(boxes) {
  const ul = $("boxes");
  ul.replaceChildren();
  $("empty").hidden = boxes.length > 0;
  for (const b of boxes) {
    const li = el("li");
    const head = el("div", "", "row");
    head.append(el("strong", b.name), el("span", b.status, "status"));
    if (b.dc) head.append(el("span", b.dc, "muted"));
    if (b.costPerHr) head.append(el("span", "$" + b.costPerHr.toFixed(3) + "/hr", "muted"));
    const down = el("button", "Terminate", "danger");
    down.type = "button";
    down.addEventListener("click", () => terminate(b.name));
    head.append(down);
    li.append(head);
    const links = el("div", "", "links");
    for (const l of b.links || []) links.append(link(l.label, l.url));
    li.append(links);
    if (b.ssh) li.append(copyable("ssh", b.ssh));
    if (b.tunnel) li.append(copyable("webterm tunnel", b.tunnel));
    li.append(setupPanel());
    ul.append(li);
  }
}

// setupPanel is hydrate for the web: the server has no SSH into boxes, so it
// shows the in-box bootstrap (SETUP.md 7.2) to run in the box's terminal.
const SETUP_STEPS = [
  ["sign in to GitHub (once per volume)", "gh auth login -h github.com -p https -w"],
  ["fetch your megh.yaml", "megh config pull"],
  ["clone your repos", "megh hydrate"],
  ["reload the shell", "exec zsh"],
];
function setupPanel() {
  const d = el("details", "", "setup");
  d.append(el("summary", "Set up this box"));
  d.append(el("p", "Run these in the box's terminal (webterm, or Tailscale's browser SSH). Then paste your box env file to the path your files: entry maps it to.", "muted"));
  for (const [label, cmd] of SETUP_STEPS) d.append(copyable(label, cmd));
  return d;
}

// --- Volumes and regions -----------------------------------------------------

let regionList = [];

async function loadAdmin() {
  try {
    const all = $("allregions").checked ? "?all=1" : "";
    const [rv, rr] = await Promise.all([call("GET", "/api/volumes"), call("GET", "/api/regions" + all)]);
    const vols = (rv.data && rv.data.volumes) || [];
    const ul = $("volumes");
    ul.replaceChildren();
    for (const v of vols) {
      const li = el("li", "", "row");
      li.append(el("strong", v.name), el("span", v.dc, "muted"), el("span", v.sizeGB + " GB", "muted"));
      if (v.id === rv.data.default) li.append(el("span", "default", "status"));
      const del = el("button", "Delete", "danger");
      del.type = "button";
      del.addEventListener("click", () => deleteVolume(v));
      li.append(del);
      ul.append(li);
    }
    regionList = (rr.data && rr.data.dcs) || [];
    const dc = $("voldc");
    const keep = dc.value;
    dc.replaceChildren();
    for (const d of regionList) {
      const o = el("option", d);
      o.value = d;
      dc.append(o);
    }
    dc.value = keep || (rr.data && rr.data.default) || "";
  } catch (e) {
    showLog(e.message, true);
  }
}

async function createVolume(name, sizeGB, dc) {
  const r = await call("POST", "/api/volumes", { name, sizeGB, dc });
  await Promise.all([loadAdmin(), loadVolumes()]);
  return r.data;
}

async function deleteVolume(v) {
  const typed = prompt("Deleting a volume destroys everything on it and cannot be undone.\nType " + v.name + " to delete it.");
  if (typed === null) return;
  try {
    await call("POST", "/api/volumes/delete", { id: v.id, confirm: typed });
    showLog("deleted volume " + v.name, false);
    await Promise.all([loadAdmin(), loadVolumes()]);
  } catch (e) {
    showLog(e.message, true);
  }
}

function probeRow(text, cls) {
  const li = el("li", text, cls);
  $("probes").append(li);
  return li;
}

// sweep probes data centers one request at a time, so progress shows as it
// goes and no request is long. With stopAtFirst it returns the first rentable
// one, which is what placing a volume wants.
async function sweep(stopAtFirst) {
  $("probes").replaceChildren();
  const vcpu = Number($("size").value) || 0;
  for (const dc of regionList) {
    const row = probeRow(dc + " … probing", "muted");
    try {
      const r = await call("POST", "/api/regions/probe", { dc, vcpu });
      const p = r.data;
      row.textContent = p.dc + ": " + p.verdict;
      row.className = p.rentable ? "" : "muted";
      if (p.orphanId) {
        row.className = "error";
        const b = el("button", "Terminate probe box", "danger");
        b.type = "button";
        b.addEventListener("click", () => terminate(p.orphanId));
        row.append(" ", b);
      }
      if (p.rentable && stopAtFirst) return p.dc;
    } catch (e) {
      row.textContent = dc + ": " + e.message;
      row.className = "error";
    }
  }
  return "";
}

async function probe() {
  if (!confirm("Probe " + regionList.length + " data center(s)? Each rents a real box for about a second, a fraction of a cent.")) return;
  $("probe").disabled = $("place").disabled = true;
  try { await sweep(false); } finally { $("probe").disabled = $("place").disabled = false; }
}

async function place() {
  const name = $("volname").value.trim();
  if (!name) { showLog("name the new volume first (Volumes, above)", true); return; }
  const size = Number($("volsize").value);
  if (!confirm("Probe until a data center rents, then create a " + size + " GB volume \"" + name + "\" there? The volume bills monthly until you delete it.")) return;
  $("probe").disabled = $("place").disabled = true;
  try {
    const dc = await sweep(true);
    if (!dc) { showLog("no probed data center had capacity; try again later or include all regions", true); return; }
    const v = await createVolume(name, size, dc);
    $("where").value = v.id;
    showLog("created " + v.name + " in " + v.dc + "; it is selected under Launch", false);
  } catch (e) {
    showLog(e.message, true);
  } finally {
    $("probe").disabled = $("place").disabled = false;
  }
}

// loadVolumes fills the "where" menu. A box starts in its volume's data center,
// so this is also the region choice; the default entry keeps megh.yaml's pair.
async function loadVolumes() {
  try {
    const r = await call("GET", "/api/volumes");
    const d = r.data || {};
    const sel = $("where");
    const keep = sel.value;
    sel.replaceChildren();
    const first = el("option", "default volume");
    first.value = "";
    sel.append(first);
    for (const v of d.volumes || []) {
      const o = el("option", v.name + " · " + v.dc + " · " + v.sizeGB + " GB" + (v.id === d.default ? " (default)" : ""));
      o.value = v.id;
      sel.append(o);
    }
    sel.value = keep;
  } catch (e) { /* the default entry still works */ }
}

async function refresh() {
  try {
    const r = await call("GET", "/api/boxes");
    render(r.data || []);
    showLog("", false);
  } catch (e) {
    showLog(e.message + (e.log ? "\n" + e.log : ""), true);
  }
}

async function launch() {
  const name = $("name").value.trim();
  if (!name) return;
  $("up").disabled = true;
  showLog("launching " + name + " ...", false);
  try {
    const r = await call("POST", "/api/up", {
      name, flavor: $("flavor").value, vcpu: Number($("size").value) || 0, volume: $("where").value,
    });
    showLog((r.log || "") + (r.data ? r.data.summary : ""), false);
    $("name").value = "";
    await refresh();
  } catch (e) {
    showLog(e.message + (e.log ? "\n" + e.log : ""), true);
  } finally {
    $("up").disabled = false;
  }
}

async function terminate(name) {
  const typed = prompt("Type " + name + " to terminate it. The volume survives.");
  if (typed !== name) return;
  showLog("terminating " + name + " ...", false);
  try {
    const r = await call("POST", "/api/down", { name });
    showLog(r.log || ("terminated " + name), false);
    await refresh();
  } catch (e) {
    showLog(e.message + (e.log ? "\n" + e.log : ""), true);
  }
}

function showState() {
  const ok = haveKeys();
  $("keys").hidden = ok;
  $("app").hidden = !ok;
  $("forget").hidden = !haveLocalKeys();
  $("source").textContent = !ok ? "" :
    server.runpod ? "Using keys stored on the server" + (server.tailscale ? "." : " (no Tailscale keys there, so new boxes won't join the tailnet).") :
    "Using keys pasted into this tab.";
  if (ok) { refresh(); loadVolumes(); loadAdmin(); }
}

document.addEventListener("DOMContentLoaded", () => {
  $("savekeys").addEventListener("click", () => {
    const keys = parseKeys($("keyblock").value);
    $("keyblock").value = "";
    if (!keys["megh.runpod"]) {
      showLog("no RUNPOD_API_KEY= line found", true);
      return;
    }
    for (const k in HEADERS) set(k, keys[k] || "");
    showState();
  });
  $("forget").addEventListener("click", () => {
    for (const k in HEADERS) set(k, "");
    showLog("", false);
    showState();
  });
  // Sign out forgets the keys, then asks IAP to drop its session cookie so the
  // next visit signs in again. Locally (`megh serve`, no IAP) the query string
  // is ignored and this just reloads the page.
  $("signout").addEventListener("click", () => {
    for (const k in HEADERS) set(k, "");
    location.replace("/?gcp-iap-mode=CLEAR_LOGIN_COOKIE");
  });
  $("refresh").addEventListener("click", refresh);
  $("up").addEventListener("click", launch);
  $("volcreate").addEventListener("click", async () => {
    const name = $("volname").value.trim();
    const size = Number($("volsize").value);
    const dc = $("voldc").value;
    if (!name || !dc) { showLog("name the volume and pick a data center", true); return; }
    if (!confirm("Create a " + size + " GB volume \"" + name + "\" in " + dc + "? It bills monthly until you delete it.")) return;
    try {
      const v = await createVolume(name, size, dc);
      $("volname").value = "";
      showLog("created " + v.name + " in " + v.dc, false);
    } catch (e) {
      showLog(e.message, true);
    }
  });
  $("probe").addEventListener("click", probe);
  $("place").addEventListener("click", place);
  $("allregions").addEventListener("change", loadAdmin);
  loadServerKeys().then(showState);
});
