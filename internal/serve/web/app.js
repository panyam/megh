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

function haveKeys() { return get("megh.runpod") !== ""; }

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
    ul.append(li);
  }
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
    const r = await call("POST", "/api/up", { name, flavor: $("flavor").value });
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
  $("forget").hidden = !ok;
  if (ok) refresh();
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
  showState();
});
