# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches; nothing is half-done. No mission is active (`/retriage` would set one).
- Candidates for next: #114 (bug: the older tailnet dial path uses the bare box name, which a
  local host can shadow; #115 already fixed the key-free fallback), #116 (idea: state on a small
  volume, repos on the VM's local disk), and the unchecked live items on #95 (key-free `megh ssh`
  from a tailnet member, `--type` launches, fixed hydrate on a fresh volume, Hetzner end to end).
- This run: merged #112, #115, #119, #120, #124, #125 and #126; closed #111, #113, #117, #118,
  #121, #122 and #123; filed #114 and #116; recorded the 2026-10-08 live results on #95.

## Across threads

- The repo stays public until #98 is worked, so the `curl .../install.sh | sh` one-liner and
  existing bootstrap notes keep working; install.sh already prefers gh when it is logged in.
- A deployed meghplane (SETUP.md §7) only picks up changes to `internal/serve`,
  `internal/lifecycle`, `internal/providers`, `cmd/meghplane`, or the `serve:`/`extra_pubkeys`
  parts of `megh.yaml` on the next `gcloud app deploy`.
- A box runs the `megh` baked into its image, which lags `main`; anything fixed since (hydrate on
  a fresh volume, #125) reaches a running box only through `install.sh` with
  `MEGH_INSTALL_DIR=/usr/local/bin`.
