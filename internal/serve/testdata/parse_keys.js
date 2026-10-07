// Runs the page's real parseKeys (from web/app.js) against pasted-note shapes.
// Invoked by TestPageKeyParser; exits non-zero on the first mismatch.
"use strict";
const fs = require("fs");
const vm = require("vm");

const ctx = { document: { addEventListener() {} } };
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(process.argv[2], "utf8"), ctx);

const R = "megh.runpod", ID = "megh.tsid", SEC = "megh.tssecret";
const cases = [
  ["one per line", "RUNPOD_API_KEY=rpa_1\nMEGH_TAILSCALE_CLIENT_ID=id1", { [R]: "rpa_1", [ID]: "id1" }],
  ["export prefix", "export RUNPOD_API_KEY=rpa_1", { [R]: "rpa_1" }],
  ["export with tabs", "export\t\tRUNPOD_API_KEY=rpa_1", { [R]: "rpa_1" }],
  ["several on a line", "export RUNPOD_API_KEY=rpa_1 MEGH_TAILSCALE_CLIENT_ID=id1 MEGH_TAILSCALE_CLIENT_SECRET=s1", { [R]: "rpa_1", [ID]: "id1", [SEC]: "s1" }],
  ["semicolons", "RUNPOD_API_KEY=rpa_1; export MEGH_TAILSCALE_CLIENT_ID=id1;", { [R]: "rpa_1", [ID]: "id1" }],
  ["double quotes", 'RUNPOD_API_KEY="rpa_1"', { [R]: "rpa_1" }],
  ["single quotes", "RUNPOD_API_KEY='rpa_1'", { [R]: "rpa_1" }],
  ["quoted space and hash", 'RUNPOD_API_KEY="a b#c"', { [R]: "a b#c" }],
  ["trailing comment", "RUNPOD_API_KEY=rpa_1 # runpod, rotated monthly", { [R]: "rpa_1" }],
  ["comment line", "# RUNPOD_API_KEY=old\nRUNPOD_API_KEY=new", { [R]: "new" }],
  ["hash inside a value", "RUNPOD_API_KEY=rpa#1", { [R]: "rpa#1" }],
  ["unknown names ignored", "OPENAI_API_KEY=x\nRUNPOD_API_KEY=rpa_1", { [R]: "rpa_1" }],
  ["CRLF", "RUNPOD_API_KEY=rpa_1\r\nMEGH_TAILSCALE_CLIENT_ID=id1\r\n", { [R]: "rpa_1", [ID]: "id1" }],
  ["blank and junk", "\n  \nexport\nfoo bar\n", {}],
];

let failed = 0;
for (const [name, input, want] of cases) {
  const got = ctx.parseKeys(input);
  const g = JSON.stringify(got, Object.keys(got).sort());
  const w = JSON.stringify(want, Object.keys(want).sort());
  if (g !== w) {
    console.error(`${name}: got ${g}, want ${w}`);
    failed++;
  }
}
process.exit(failed ? 1 : 0);
