# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- chore/retire-sessions-collect — PR #81 — next: review and merge
- docs/checkpoint-2026-10-06 — next: re-paste the phone bootstrap note now that #80's build is out
- This run: first NEXTSTEPS.md; #78, #79, #80 merged this session; filed #82 (megh serve)

## Across threads

- The phone is the only launcher (CONSTRAINTS.md C3 "Current practice"). No box holds provider,
  tailnet or profile keys; other machines get in via `extra_pubkeys` + Bitwarden's SSH agent
  (SETUP.md §6.7). Don't propose re-elevating a box.

## chore/retire-sessions-collect

- **Last touched**: 2026-10-06 23:41 (dev:/workspace/repos/newstack/megh worktree)
- **Ticket**: PR #81
- **Why**: transcripts stay on the volume; context is kept by checkpointing into the repo.
- **Where it stopped**: code + docs done, `make test` green, PR open.
- **Next action**: merge #81, then drop the `sessions:` block from `~/dotfiles/megh/megh.yaml`
  (harmless if left: the parser ignores unknown keys).
- **Open questions**: archive `panyam/megh-sessions` (leaning yes, don't delete: its pushed
  history is the only off-volume copy).

## docs/checkpoint-2026-10-06

- **Last touched**: 2026-10-06 23:41 (dev:/workspace/repos/newstack/megh worktree)
- **Why**: get the phone working as the launcher and credentials off boxes.
- **Where it stopped**: the first phone bootstrap hit the Termux argv bug (fixed in #80); nothing
  after `install.sh` ran (no profile, no `secrets.env`).
- **Next action**: re-paste the Bitwarden bootstrap note in Termux, `megh up` a box, confirm two
  lines in its `authorized_keys` (CLAUDE.md live-validation debt).
- **User-side cleanup still pending**: `gh ssh-key delete 163221592` + `rm -rf
  /workspace/state/megh/profiles/boxdev` (blocked for the agent); recreate `dev` from the Mac to
  drop the stale `/root/personal-control` mount; delete lines 3–10 of `~/dotfiles/shared/zshenv`.
- **Open questions**: #82 (web control plane) vs. phone-only — decide after living with the phone.
