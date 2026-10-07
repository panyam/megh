# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. Next up is #104 (Hetzner and Vultr on meghplane): the plan is agreed except
  one choice, whether the launch provider is derived from the chosen volume (recommended) or
  picked from an explicit menu.
- #95 now lists live checks for Hetzner and Vultr; both providers have only run against fakes.
- This run: #100–#103 and #105 merged since the last checkpoint; closed #51 (delivered) and #52
  (superseded by approach B); #98 (go private) and #77 (docker in a box) unchanged.

## Across threads

- The repo stays public until #98 is worked, so the `curl .../install.sh | sh` one-liner and
  existing bootstrap notes keep working; install.sh already prefers gh when it is logged in.
- A deployed meghplane (SETUP.md §7) only picks up changes to `internal/serve`,
  `internal/lifecycle`, `internal/providers`, `cmd/meghplane`, or the `serve:`/`extra_pubkeys`
  parts of `megh.yaml` on the next `gcloud app deploy`.
