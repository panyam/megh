# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. #87 holds the web control plane's missing verbs (volumes, region probing,
  hydrate).

## Across threads

- A deployed meghplane (SETUP.md §7) only picks up changes to `internal/serve`,
  `internal/lifecycle`, `internal/providers`, `cmd/meghplane`, or the `serve:`/`extra_pubkeys`
  parts of `megh.yaml` on the next `gcloud app deploy`.
