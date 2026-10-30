# Box-first desk

## Box mirror

The development workspace is the SURF Research Cloud box, reached with `ssh -F ~/.config/claude-ssh/config desk`; its checkout is `/home/dmccool/ddp-comments`, kept in step with this one by `scripts/box-sync.sh push|pull` (rsync, excluding `.git`, `.superpowers`, `dist` and `.cache`). Commits are made locally only, after reading the diff, and GitHub is reached only from the local checkout, never from the box. `scripts/box-sync.sh` is the rsync path; a sandboxed agent that cannot run rsync over ssh uses tar, scp and ssh instead, as three separate bare commands (bare ssh and scp run outside the sandbox, so the archive goes at the literal scratchpad path, not `$TMPDIR`): run from the checkout root, `find . \( -path ./.git -o -path ./.superpowers -o -path ./dist -o -path ./.cache -o -path ./.venv -o -name __pycache__ \) -prune -o -type f -print0 | tar czf /tmp/claude-1000/-home-dmccool-src-researchcloud-ddp-comments--claude-worktrees-desk-core/a907afb4-52c6-4fd5-9308-8900bc708eb2/scratchpad/ddp-sync.tgz --null -T -` (regular files only, so the sandbox's device-file masks are not archived), then `scp -F /home/dmccool/.config/claude-ssh/config <that path> desk:/tmp/ddp-sync.tgz`, then `ssh -F /home/dmccool/.config/claude-ssh/config desk 'rm -rf /home/dmccool/ddp-comments && mkdir -p /home/dmccool/ddp-comments && tar xzf /tmp/ddp-sync.tgz -C /home/dmccool/ddp-comments'`.

## Box-first verification

Verification runs on the box. There is no Go toolchain there, so Go tests run in a container: `sudo docker run --rm -v /home/dmccool/ddp-comments:/src -w /src -v ddp-gomod:/go/pkg/mod golang:1.26 go test ./...`, and locally the same way with the local path when `go` is absent. `scripts/check.sh shell shape` runs on the box; the `adr` layer needs `adg`, which the box does not have, so it is a desktop and CI layer.

## ADRs

Decisions live in `docs/decisions/` as lean ADRs. Before editing any governed file, run `adg lean brief --model docs/decisions <paths>` and obey what it prints. Run `scripts/check.sh` before every commit; it runs the layers `adr`, `shell`, `go`, `compose` and `shape`, and a missing tool is a failure, not a skip.

## Commits

Every commit subject starts with a conventional-commit prefix such as `fix(facebook):` or `chore(check):`, and its body carries a `Plan:` line naming the plan that the work belongs to, then the attribution lines. Stage and commit by pathspec (`git commit -m "..." -- <paths>`), never `git add -A` or `git commit -a`, because the sandbox shows unrelated files at the repo root.
