# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches.
- #95 (needs-live-test): the box launched from meghplane can tick several items now
  (`extra_pubkeys` in `authorized_keys`, the in-box bootstrap from #93, local `megh enable`).
- #82 closes once a page-launched box is reached and then terminated from the page.
- #98: going private is prepared but deliberately not done; #87: the web page's missing verbs.
- This run: #92–#94, #96, #97 merged since the last checkpoint; filed #95 and #98.

## Across threads

- The repo stays public until #98 is worked, so the `curl .../install.sh | sh` one-liner and
  existing bootstrap notes keep working; install.sh already prefers gh when it is logged in.
- A deployed meghplane (SETUP.md §7) only picks up changes to `internal/serve`,
  `internal/lifecycle`, `internal/providers`, `cmd/meghplane`, or the `serve:`/`extra_pubkeys`
  parts of `megh.yaml` on the next `gcloud app deploy`.
