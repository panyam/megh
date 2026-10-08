// meghplane page. Every string from the server goes into the DOM through
// textContent or an attribute, never innerHTML, and the CSP forbids inline
// script, so nothing a backend returns can run here.
"use strict";

const STORE = {
  RUNPOD_API_KEY: "megh.runpod",
  HCLOUD_TOKEN: "megh.hcloud",
  VULTR_API_KEY: "megh.vultr",
  MEGH_TAILSCALE_CLIENT_ID: "megh.tsid",
  MEGH_TAILSCALE_CLIENT_SECRET: "megh.tssecret",
};
const HEADERS = {
  "megh.runpod": "X-Megh-Runpod-Key",
  "megh.hcloud": "X-Megh-Hcloud-Token",
  "megh.vultr": "X-Megh-Vultr-Key",
  "megh.registry": "X-Megh-Registry-Token",
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

// registryEnv is the note's name for the registry pull token, which follows
// megh.yaml's registries[0].token_env; GET /api/keys says which name that is.
let registryEnv = "GH_MEGH_TOKEN";

// parseKeys accepts the note as pasted: KEY=value assignments, several to a
// line or one per line, with or without "export", quotes or comments. Unknown
// names are ignored.
function parseKeys(text, regEnv = registryEnv) {
  const out = {};
  for (const line of text.split(/\r?\n/)) {
    for (const word of shellWords(line)) {
      const eq = word.indexOf("=");
      if (eq < 1) continue; // "export", or any other bare word
      const name = word.slice(0, eq);
      if (name === regEnv) out["megh.registry"] = word.slice(eq + 1);
      else if (STORE[name]) out[STORE[name]] = word.slice(eq + 1);
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
let server = { runpod: false, hetzner: false, vultr: false, tailscale: false };

// PROVIDER_KEYS are the session keys that each open one backend. Any one is
// enough; the server builds only the backends it has a key for.
const PROVIDER_KEYS = ["megh.runpod", "megh.hcloud", "megh.vultr"];

// PROVIDER_STORE is the session key holding each provider's key.
const PROVIDER_STORE = { runpod: "megh.runpod", hetzner: "megh.hcloud", vultr: "megh.vultr" };

// allProviders is every provider the server can build, with the note variable
// that unlocks it (GET /api/keys). The page offers all of them, keyed or not.
let allProviders = [];

function serverHasProvider() { return server.runpod || server.hetzner || server.vultr; }
function haveKeys() { return serverHasProvider() || PROVIDER_KEYS.some((k) => get(k) !== ""); }
function haveLocalKeys() { return Object.keys(HEADERS).some((k) => get(k) !== ""); }

async function loadServerKeys() {
  try {
    const res = await fetch("/api/keys");
    const json = await res.json();
    server = json.server || server;
    if (json.registryEnv) registryEnv = json.registryEnv;
    allProviders = json.providers || allProviders;
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
    if (b.provider) head.append(el("span", b.provider, "muted"));
    if (b.dc) head.append(el("span", where(b.dc, b.place), "muted"));
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
// catalog is true when the chosen provider answers the region question from
// its price list (Hetzner, Vultr), so there is nothing to probe.
let catalog = false;

// regionPlaces is the selected provider's names for its location codes
// ("ord" -> "Chicago, US"), from GET /api/regions.
let regionPlaces = {};

// where labels a location code for a person: "Chicago, US (ord)", or the bare
// code when the provider gave no name for it.
function where(code, place) {
  if (!code) return "";
  return place ? place + " (" + code + ")" : code;
}

function money(perHr) { return "$" + perHr.toFixed(3) + "/hr"; }

async function loadAdmin() {
  try {
    const rv = await call("GET", "/api/volumes");
    const d = rv.data || {};
    keyed = d.providers || [];
    const prov = $("volprov");
    const keepProv = prov.value;
    prov.replaceChildren();
    const names = allProviders.length ? allProviders.map((p) => p.name) : keyed;
    for (const p of names) {
      const o = el("option", keyed.includes(p) ? p : p + " (no key)");
      o.value = p;
      prov.append(o);
    }
    prov.value = names.includes(keepProv) ? keepProv : (d.provider || "");

    const ul = $("volumes");
    ul.replaceChildren();
    for (const v of d.volumes || []) {
      const li = el("li", "", "row");
      li.append(el("strong", v.name), el("span", v.provider, "muted"), el("span", where(v.dc, v.place), "muted"), el("span", v.sizeGB + " GB", "muted"));
      if (v.id === d.default) li.append(el("span", "default", "status"));
      const del = el("button", "Delete", "danger");
      del.type = "button";
      del.addEventListener("click", () => deleteVolume(v));
      li.append(del);
      ul.append(li);
    }

    if (!keyed.includes(prov.value)) { showUnkeyed(prov.value); return; }
    $("volcreate").disabled = false;
    const q = new URLSearchParams({ provider: prov.value });
    if ($("allregions").checked) q.set("all", "1");
    if ($("size").value) q.set("vcpu", $("size").value);
    const rr = await call("GET", "/api/regions?" + q);
    const r = rr.data || {};
    regionList = r.dcs || [];
    regionPlaces = r.places || {};
    catalog = Array.isArray(r.offers);
    const dc = $("voldc");
    const keep = dc.value;
    dc.replaceChildren();
    const offerByDC = {};
    for (const offer of r.offers || []) offerByDC[offer.dc] = offer;
    for (const name of regionList) {
      const offer = offerByDC[name];
      const label = where(name, regionPlaces[name]);
      const o = el("option", offer ? label + " · " + offer.type + " · " + money(offer.perHr) : label);
      o.value = name;
      dc.append(o);
    }
    dc.value = regionList.includes(keep) ? keep : (r.default || regionList[0] || "");
    showRegions(r.offers);
  } catch (e) {
    showLog(e.message, true);
  }
}

// keyed is the providers this request holds a key for (GET /api/volumes).
let keyed = [];

function envFor(provider) {
  const p = allProviders.find((x) => x.name === provider);
  return p ? p.env : "its key";
}

// showUnkeyed is the Volumes and Regions sections for a provider with no key:
// nothing to list or create until one is added under Keys.
function showUnkeyed(provider) {
  regionList = [];
  catalog = false;
  $("voldc").replaceChildren();
  $("volcreate").disabled = true;
  $("probe-help").hidden = $("probe-row").hidden = $("offer-help").hidden = true;
  $("probes").replaceChildren(el("li", "No " + provider + " key yet. Add " + envFor(provider) + " under Keys (top of the page) to create volumes and launch boxes there.", "muted"));
}

// showKeyStatus lists where each key comes from: the server, this tab, or
// nowhere yet.
function showKeyStatus() {
  const ul = $("keystatus");
  ul.replaceChildren();
  const row = (label, onServer, inTab, env) => {
    const where = onServer ? "on the server" : inTab ? "in this tab" : "missing";
    ul.append(el("li", label + ": " + where + (onServer || inTab ? "" : " (" + env + ")"), onServer || inTab ? "" : "muted"));
  };
  for (const p of allProviders) row(p.name, server[p.name], get(PROVIDER_STORE[p.name]) !== "", p.env);
  row("tailscale", server.tailscale, get("megh.tsid") !== "" && get("megh.tssecret") !== "", "MEGH_TAILSCALE_CLIENT_ID and _SECRET");
  row("registry pull token", server.registry, get("megh.registry") !== "", registryEnv);
}

// showRegions switches the Regions section between RunPod's probe buttons and
// a catalog backend's price list, which needs no probing.
function showRegions(offers) {
  $("probe-help").hidden = $("probe-row").hidden = catalog;
  $("offer-help").hidden = !catalog;
  const ul = $("probes");
  ul.replaceChildren();
  for (const offer of offers || []) ul.append(el("li", where(offer.dc, regionPlaces[offer.dc]) + ": " + offer.type + ", " + money(offer.perHr)));
  if (catalog && !(offers || []).length) ul.append(el("li", "no location sells this size", "muted"));
}

async function createVolume(name, sizeGB, dc) {
  const r = await call("POST", "/api/volumes", { provider: $("volprov").value, name, sizeGB, dc });
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
    const label = where(dc, regionPlaces[dc]);
    const row = probeRow(label + " … probing", "muted");
    try {
      const r = await call("POST", "/api/regions/probe", { provider: $("volprov").value, dc, vcpu });
      const p = r.data;
      row.textContent = label + ": " + p.verdict;
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
      row.textContent = label + ": " + e.message;
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
    showLog("created " + v.name + " in " + where(v.dc, v.place || regionPlaces[v.dc]) + "; it is selected under Launch", false);
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
      const o = el("option", v.name + " · " + v.provider + " " + where(v.dc, v.place) + " · " + v.sizeGB + " GB" + (v.id === d.default ? " (default)" : ""));
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
    showLog(r.log || "", !!r.log);
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
  if (!ok) $("keys").open = true;
  $("app").hidden = !ok;
  showKeyStatus();
  $("forget").hidden = !haveLocalKeys();
  $("source").textContent = !ok ? "" :
    (serverHasProvider() && haveLocalKeys() ? "Using keys from the server and this tab" :
      serverHasProvider() ? "Using keys stored on the server" : "Using keys pasted into this tab") +
    (server.tailscale || (get("megh.tsid") && get("megh.tssecret")) ? "." : " (no Tailscale keys, so new boxes won't join the tailnet).");
  if (ok) { refresh(); loadVolumes(); loadAdmin(); }
}

document.addEventListener("DOMContentLoaded", () => {
  $("savekeys").addEventListener("click", () => {
    const keys = parseKeys($("keyblock").value);
    $("keyblock").value = "";
    if (!Object.keys(keys).length) {
      showLog("no recognized KEY=value line found", true);
      return;
    }
    // Adds to what this tab holds rather than replacing it, so a key the
    // server lacks can sit beside the ones it has.
    for (const k in keys) set(k, keys[k]);
    showLog("", false);
    $("keys").open = !haveKeys();
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
      showLog("created " + v.name + " in " + where(v.dc, v.place || regionPlaces[v.dc]), false);
    } catch (e) {
      showLog(e.message, true);
    }
  });
  $("probe").addEventListener("click", probe);
  $("place").addEventListener("click", place);
  $("allregions").addEventListener("change", loadAdmin);
  $("volprov").addEventListener("change", async () => {
    await loadAdmin();
    if (!keyed.includes($("volprov").value)) {
      $("keys").open = true;
      $("keyblock").focus();
    }
  });
  $("size").addEventListener("change", loadAdmin);
  loadServerKeys().then(showState);
});
