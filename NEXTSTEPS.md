# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- No open branches. #82 (meghplane) is live and closes after one launch-and-terminate from the
  page (CLAUDE.md "Live-validation debt"); #87 holds the web UI's missing verbs.
- This run: nothing to prune (no threads on main); #78–#81, #84–#86, #88–#90 merged since #83.

## Across threads

- meghplane is deployed at https://meghplane.appspot.com behind IAP (SETUP.md §7). Changes to
  `internal/serve`, `internal/lifecycle`, `internal/providers` or `cmd/meghplane`, and to the
  `serve:`/`extra_pubkeys` parts of `megh.yaml`, only reach it on the next `gcloud app deploy`.
- Still owed on the console side, not tracked in an issue: splitting meghplane's build service
  account from its runtime one before `serve.secret` holds a real key (SETUP.md §7.1).
