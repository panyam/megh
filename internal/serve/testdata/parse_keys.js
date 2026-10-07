// Runs the page's real parseKeys (from web/app.js) against the shared cases in
// keyblock_cases.json, which the Go parser (ParseKeyBlock) is tested against
// too, so the page and the server read a pasted note the same way.
// Invoked by TestPageKeyParser; exits non-zero on the first mismatch.
"use strict";
const fs = require("fs");
const vm = require("vm");

const ctx = { document: { addEventListener() {} } };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.argv[2], "utf8"), ctx);
const cases = JSON.parse(fs.readFileSync(process.argv[3], "utf8"));

let failed = 0;
for (const c of cases) {
  const raw = ctx.parseKeys(c.in);
  const got = {};
  for (const k of Object.keys(raw)) got[k.replace(/^megh\./, "")] = raw[k];
  const g = JSON.stringify(got, Object.keys(got).sort());
  const w = JSON.stringify(c.want, Object.keys(c.want).sort());
  if (g !== w) {
    console.error(`${c.name}: got ${g}, want ${w}`);
    failed++;
  }
}
process.exit(failed ? 1 : 0);
