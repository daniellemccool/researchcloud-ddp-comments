# Monday handover chain plan

Spec: `docs/superpowers/specs/2026-10-02-monday-handover-chain-design.md` (this repo)
Regime: MIXED — ORG for `~/src/d3i-infra/researchcloud-ddp-transcribe` (implementer stages, Danielle commits); PERSONAL for `~/src/researchcloud-ddp-comments` and `~/src/ddp-transcribe` (agent may commit, same story-anchor structure).

## Goal
Deliver, before the launch day, the hand-fed handover chain: a correctly provisioned GPU workspace in OnlineHate, a ledger-based read-only feed export pushed to Yoda, a desk pull that builds disjoint per-session worklists, N generated Firefox+mitm sessions behind the SRAM gate on `checkjefeedzee`, the extended desk stub, the capture-addon fixes, and an executable runbook with the collector protocol.

## Approach
Three repos, in dependency order: deploy-repo fixes first so the GPU workspace can be launched while the desk code is written; then the `ddp-comments` scripts, each with a hermetic test runnable on the laptop; then Danielle's portal and on-box checklists, which are tasks with verification steps even though no agent runs them. Decisions D1–D14 in the spec are fixed; alternatives considered are recorded there and not reopened here.
- Alternative: one repo for everything — rejected, the deploy repo is org-owned and the feed script may not live in the pipeline repo (ADR-0041).
- Alternative: pure-shell worklist builder — rejected after the two-pane decision (D10); the CSV pool/split logic lives in a small Python helper beside the Python stub.

## Commit stories
Each story is one reviewable commit in the final history. Every task names its story.
1. **ytdlp-stack** (deploy repo, ORG): prefix `fix(ytdlp):`; scope `roles/ytdlp/`, `roles/verify/`, `roles/system_deps/`, `deploy-ddp-transcribe.yaml` defaults, `scripts/test-ytdlp-role.sh`, `scripts/test-verify-role.sh`, `scripts/fixtures/`.
2. **catalog-pins** (deploy repo, ORG): prefix `docs(catalog):`; scope `README.md`, `docs/catalog-item.md`, `docs/FOLLOWUPS.md`, `pipeline_git_ref` default.
3. **feed-followup** (ddp-transcribe, PERSONAL): prefix `docs(followups):`; scope `docs/FOLLOWUPS.md`, `docs/followups/production-run.md`.
4. **spike-import** (ddp-comments, PERSONAL): prefix `chore(repo):`; scope spike artifacts, README, test harness, `.gitignore`, study-folder pointer.
5. **feed** (ddp-comments): prefix `feat(feed):`; scope `gpu/feed-export.sh`, `desk/pull-feed.sh`, `desk/build_worklists.py`, their tests.
6. **capture** (ddp-comments): prefix `fix(capture):`; scope `desk/mitm/addons/capture_comments.py`, its tests, dev requirements.
7. **sessions** (ddp-comments): prefix `feat(desk):`; scope `desk/make-sessions.sh`, `desk/desk_stub.py`, `desk/desk-stub.service`, tests.
8. **runbook** (ddp-comments): prefix `docs(ops):`; scope `docs/day-one.md`, `docs/collector-sheet.md`, validation-log entries from Danielle's tasks.

## ADR impact
- ytdlp-stack, catalog-pins: none (the deploy repo has no `docs/decisions/`; the posture reversal is recorded in the role header and cites the ddp-transcribe incident record).
- feed-followup: none. Governed by ADR-0020 (the entry marks its hypothesis unverified) and references ADR-0041/0043; neither is revised.
- spike-import, feed, capture, sessions, runbook: none now. This repo has no ADR corpus; the spec's D6, D8, D12, D13, D14 and the §1 scope limit are candidates for records at Phase 1, when the repo moves into `d3i-infra` (spec §8.6). Nothing in week one creates an invariant that must be enforced by tooling yet.

## Ledger
- Resolves: none tracked (no ledger tool in these repos). The ddp-transcribe runbook's standing "fix the deploy repo's ytdlp role" paragraph is closed by story 1 and should be struck from `docs/operations/src-vm.md` in a later docs commit, out of scope here.
- Expects to park: a cron wrapper for the GPU cycle (after two clean manual runs); the dead-man census cron on the new VM; the Phase 1 session manager, store, 4CAT push, scoring, `feed` subcommand (spec §8); the `src-vm.md` update above; evidence for whether Deno is required (deploy FOLLOWUPS entry added in Task 3).

## Open questions
1. The OnlineHate Yoda collection path, and whether `inbox/` exists there yet (needed by Tasks 11–13).
2. N sessions for launch day (2 or 3) and the cap: rows per collector-hour × hours per shift (Task 12).
3. Operator on `checkjefeedzee`: join the `docker` group or keep `sudo` (Task 13).
4. Launch date: the spec says Monday 2026-10-05; this plan was written 2026-10-12. Confirm the day the collectors start.

---

# Monday Handover Chain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the hand-fed handover chain from Yoda inbox to Zeeschuimer sessions on `checkjefeedzee`, with tests, so collectors can start on launch day.

**Architecture:** A read-only export script on the GPU workspace writes capped `selection.csv` batches and syncs them to Yoda; a pull script on the desk workspace mirrors them, dedupes against a loaded-batch ledger and a pool, and splits outstanding rows round-robin into per-session worklists; a generator renders N isolated Firefox+mitmproxy pairs and the nginx locations; the Python desk stub serves one two-pane page per session and dispatches opens into that session's Firefox. The deploy repo's `ytdlp` role is fixed so a fresh GPU workspace can fetch at all.

**Tech Stack:** bash + sqlite3 + GoCommands (`gocmd`) on the GPU box; bash + Python 3.12 stdlib + Docker Compose + nginx on the desk box; Ansible roles (ansible-core 2.16 in the deploy repo's `.venv`); mitmproxy 12.2.3 addon (Python); hermetic bash test scripts with PATH shims, Python `unittest`.

**Spec:** `docs/superpowers/specs/2026-10-02-monday-handover-chain-design.md`

## Global Constraints

- yt-dlp on the GPU workspace is a **pre-release**: unpinned → `pipx install --pip-args=--pre yt-dlp[default]` at first provision, retained on re-runs; pinned → the **distribution** version string including `.dev0` (proven fallback: `2026.8.25.233329.dev0`). `curl_cffi` is injected. Deno 2.x is installed. (spec §4.1, D9)
- `pipeline_git_ref` is `v0.5.1`. No pipeline release, no schema migration (schema stays v7). (spec §4.1, D7)
- `feed-export.sh` opens `state.sqlite` with `?mode=ro`, creates and fills the temp ledger table **before** `PRAGMA query_only=1`, never holds a write lock. (spec §5.1)
- `gocmd sync SRC DEST` lands SRC as `DEST/basename(SRC)` when DEST exists: feed push targets `i:<collection>`, feed pull targets `~/desk`. (spec §5.1–5.2)
- Feed CSV header is exactly `video_id,bucket,note,succeeded_at,comment_count,donors`; the desk reads only the first three columns; batch dirs are `feed/<UTC %Y%m%dT%H%M%SZ>-<seq>/` and never reused. (spec §5.3)
- Each session pair `mitm-<i>` / `firefox-<i>` has its own Compose network with the proxy aliased `mitm`; container names `desk-mitm-<i>` / `desk-firefox-<i>`; noVNC bound to `127.0.0.1:580<i>`; state dirs owned by uid 1000. (spec §6.1)
- nginx: `/desk/` → `proxy_pass http://127.0.0.1:8110;` with **no trailing slash**; every location carries `auth_request /validate`; `/desk/` passes the `REMOTE_USER` header. (spec §6.1, §6.3)
- The Firefox `docker exec` invocation keeps `su-exec 1000:1000`, `DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base`, `--profile /config/profile`; never `docker exec -u`. (spec §6.3)
- Data flows GPU box → Yoda → desk only; nothing from the desk enters `state.sqlite`. (spec §5.3)
- Template profile holds the 4CAT login only, no SRAM session, empty Zeeschuimer items; it is never committed. (spec §6.2, D13)
- Regime: ORG tasks end with `git add` and a report; Danielle commits. PERSONAL tasks may commit with the story's prefix and the trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.

## Review Focus

1. A ledger file with CRLF line endings or trailing blank lines (edited on a laptop, scp'd back) must not produce malformed ids or a SQL error — Task 5 test "CRLF ledger".
2. A `note` field containing a comma or quotes in a hand-edited `selection.csv` must survive pool → session split intact — Task 6 test `test_quoted_note_round_trips`.
3. A request to the stub without a `REMOTE_USER` header (nginx misconfiguration or direct access to port 8110) must be refused, not logged as an unknown user — Task 9 test `test_missing_remote_user_is_403`.
4. A row click for a `video_id` that is not in that session's worklist must not exec anything (prevents cross-session or forged opens) — Task 9 test `test_foreign_id_is_404_without_exec`.
5. `--cap 0`, a non-numeric cap, or a missing `--collection` with push enabled must fail before touching the ledger — Task 5 tests "cap 0 is usage error", "cap abc is usage error", "missing --collection".

---

### Task 1: `ytdlp` role installs the pre-release stack (story ytdlp-stack)

**Files:**
- Modify: `~/src/d3i-infra/researchcloud-ddp-transcribe/roles/ytdlp/tasks/main.yaml` (replace whole file)
- Create: `~/src/d3i-infra/researchcloud-ddp-transcribe/roles/ytdlp/defaults/main.yaml`
- Modify: `~/src/d3i-infra/researchcloud-ddp-transcribe/deploy-ddp-transcribe.yaml` (vars block, after `pipeline_git_ref` near line 47: add `ytdlp_version: ""`)
- Create: `~/src/d3i-infra/researchcloud-ddp-transcribe/scripts/test-ytdlp-role.sh`
- Create: `~/src/d3i-infra/researchcloud-ddp-transcribe/scripts/fixtures/ytdlp-role-play.yaml`

**Interfaces:**
- Consumes: existing vars `pipeline_user`, `pipeline_home` (set by the playbook).
- Produces: role vars `ytdlp_version` (string, default `""`), `ytdlp_become` (bool, default `true`, test hook), `pipx_bin` (default `pipx`), `deno_version` (default `2.9.5`), `deno_release_base` (URL); yt-dlp at `{{ pipeline_home }}/.local/bin/yt-dlp`, Deno at `{{ pipeline_home }}/.local/bin/deno`.

- [ ] **Step 1: Write the test harness with fake `pipx` and `deno`**

Create `scripts/fixtures/ytdlp-role-play.yaml`:

```yaml
---
# Test play: runs the ytdlp role against localhost with PATH shims. Not for SRC.
- hosts: localhost
  connection: local
  gather_facts: false
  vars:
    pipeline_user: "{{ lookup('env', 'USER') }}"
    pipeline_home: "{{ lookup('env', 'TEST_HOME') }}"
    ytdlp_version: "{{ lookup('env', 'YTDLP_VERSION') | default('', true) }}"
    ytdlp_become: false
    pipx_bin: "{{ lookup('env', 'TEST_HOME') }}/bin/pipx"
    deno_release_base: "file://{{ lookup('env', 'TEST_HOME') }}/releases"
  roles:
    - role: ytdlp
```

Create `scripts/test-ytdlp-role.sh`:

```bash
#!/usr/bin/env bash
# test-ytdlp-role.sh — hermetic tests for roles/ytdlp (no network, no real pipx).
# A fake `pipx` records its argv and answers `list --json` from an env var; a fake
# `deno` answers --version; the Deno "release" is a local zip served over file://.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; REPO="$(dirname "$HERE")"
PLAYBOOK="${REPO}/.venv/bin/ansible-playbook"
FAIL=0
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then echo "ok: $d"; else echo "FAIL: $d"; FAIL=$((FAIL+1)); fi; }

run_role() { # run_role <installed-version-or-empty> <pinned-version-or-empty> <curl_cffi-present 0|1> <deno-major-or-empty>
  export TEST_HOME; TEST_HOME="$(mktemp -d)"
  mkdir -p "$TEST_HOME/bin" "$TEST_HOME/.local/bin" "$TEST_HOME/releases"
  export FAKE_LOG="$TEST_HOME/pipx.log"; : > "$FAKE_LOG"
  export FAKE_INSTALLED="$1" FAKE_CFFI="$3"
  cat > "$TEST_HOME/bin/pipx" <<'FAKE'
#!/usr/bin/env bash
echo "pipx $*" >> "$FAKE_LOG"
case "$1 $2" in
  "list --json") if [ -n "$FAKE_INSTALLED" ]; then
                   printf '{"venvs":{"yt-dlp":{"metadata":{"main_package":{"package":"yt-dlp","package_version":"%s"}}}}}\n' "$FAKE_INSTALLED"
                 else printf '{"venvs":{}}\n'; fi ;;
  "runpip yt-dlp") [ "$FAKE_CFFI" = 1 ] ;;          # `runpip yt-dlp show curl_cffi`
  "ensurepath ") echo "already";;
  *) exit 0 ;;
esac
FAKE
  chmod +x "$TEST_HOME/bin/pipx"
  if [ -n "$4" ]; then
    printf '#!/usr/bin/env bash\necho "deno %s.0.0 (stable, release, x86_64-unknown-linux-gnu)"\n' "$4" > "$TEST_HOME/.local/bin/deno"
    chmod +x "$TEST_HOME/.local/bin/deno"
  fi
  ( cd "$TEST_HOME/releases" \
    && printf '#!/usr/bin/env bash\necho "deno 2.9.5 (stable, release, x86_64-unknown-linux-gnu)"\n' > deno && chmod +x deno \
    && zip -q deno-x86_64-unknown-linux-gnu.zip deno \
    && sha256sum deno-x86_64-unknown-linux-gnu.zip > deno-x86_64-unknown-linux-gnu.zip.sha256sum && rm deno )
  YTDLP_VERSION="$2" "$PLAYBOOK" -i localhost, "$HERE/fixtures/ytdlp-role-play.yaml" >"$TEST_HOME/play.log" 2>&1
  echo $?
}

# 1. unpinned, nothing installed → latest pre-release install, inject, deno install
rc=$(run_role "" "" 0 "")
check "unpinned fresh: play succeeds" test "$rc" = 0
check "unpinned fresh: installs with --pre" grep -q "install --pip-args=--pre yt-dlp\[default\]" "$FAKE_LOG"
check "unpinned fresh: no --force" bash -c "! grep -q -- '--force' '$FAKE_LOG'"
check "unpinned fresh: injects curl_cffi" grep -q "inject yt-dlp curl_cffi" "$FAKE_LOG"
check "unpinned fresh: deno installed to ~/.local/bin" test -x "$TEST_HOME/.local/bin/deno"

# 2. unpinned, a pre-release already installed → retained; curl_cffi present → no inject; deno 2 kept
rc=$(run_role "2026.8.29.232711.dev0" "" 1 2)
check "unpinned rerun: play succeeds" test "$rc" = 0
check "unpinned rerun: no install call" bash -c "! grep -q ' install ' '$FAKE_LOG'"
check "unpinned rerun: no inject call" bash -c "! grep -q ' inject ' '$FAKE_LOG'"

# 3. pinned, mismatch → forced install of the exact distribution version
rc=$(run_role "2026.8.29.232711.dev0" "2026.8.25.233329.dev0" 1 2)
check "pinned mismatch: forced exact install" grep -q "install --force yt-dlp\[default\]==2026.8.25.233329.dev0" "$FAKE_LOG"

# 4. pinned, match → nothing installed
rc=$(run_role "2026.8.25.233329.dev0" "2026.8.25.233329.dev0" 1 2)
check "pinned match: no install call" bash -c "! grep -q ' install ' '$FAKE_LOG'"

# 5. deno 1.x present → replaced by the release
rc=$(run_role "2026.8.29.232711.dev0" "" 1 1)
check "deno 1.x: replaced by 2.x" bash -c "'$TEST_HOME/.local/bin/deno' --version | grep -q '^deno 2\.'"

echo; [ "$FAIL" -eq 0 ] && echo "all ytdlp-role checks passed" || { echo "$FAIL check(s) failed"; exit 1; }
```

- [ ] **Step 2: Run the test to verify it fails against the current role**

Run: `cd ~/src/d3i-infra/researchcloud-ddp-transcribe && chmod +x scripts/test-ytdlp-role.sh && scripts/test-ytdlp-role.sh`
Expected: FAIL lines for "installs with --pre", "injects curl_cffi", "deno installed" (the current role runs `pipx install 'yt-dlp[default,curl-cffi]'` with `creates:` and knows nothing of Deno). The play may error on the undefined `ytdlp_become`; that is expected too. `zip` and `unzip` must be installed on the laptop.

- [ ] **Step 3: Write the role defaults**

Create `roles/ytdlp/defaults/main.yaml`:

```yaml
---
# Empty = latest pre-release at first provision, retained on re-runs.
# A value is the DISTRIBUTION version (pipx/PyPI form, e.g. 2026.8.25.233329.dev0 —
# note the .dev0; the CLI banner prints 2026.08.25.233329 and does NOT match).
ytdlp_version: ""
ytdlp_become: true          # false only in scripts/test-ytdlp-role.sh (runs as the test user)
pipx_bin: pipx
deno_version: "2.9.5"       # the version running on the campaign VM since 2026-08-26
deno_release_base: "https://github.com/denoland/deno/releases/download/v{{ deno_version }}"
```

- [ ] **Step 4: Replace the role tasks**

Replace `roles/ytdlp/tasks/main.yaml` with:

```yaml
---
# yt-dlp fetch stack. Posture since 2026-08-26 (ddp-transcribe
# docs/operations/incident-2026-08-25-tiktok-challenge-requires-impersonation.md):
#   * a PRE-RELEASE yt-dlp — stable 2026.08.19 predates the challenge-flow fix
#     that TikTok forced on 2026-08-25;
#   * curl_cffi INJECTED — the 2026-08-25 incident REVERSED the earlier
#     "never pipx inject" rule (bake-findings 2026-05-13); yt-dlp warns when no
#     impersonation target is available and the challenge flow then fails;
#   * Deno 2.x — the challenge solver's JS runtime (necessity-today unverified,
#     kept deliberately; see the incident record and docs/FOLLOWUPS.md).
# Idempotence compares DISTRIBUTION versions from `pipx list --json`, never the
# CLI banner. ytdlp_version pins the distribution version; empty = latest
# pre-release at first provision, retained on re-runs.

- name: Read installed yt-dlp distribution version
  ansible.builtin.shell: >-
    {{ pipx_bin }} list --json | python3 -c
    'import json,sys; v=json.load(sys.stdin).get("venvs",{}).get("yt-dlp");
    print(v["metadata"]["main_package"]["package_version"] if v else "")'
  register: ytdlp_installed
  changed_when: false
  become: "{{ ytdlp_become }}"
  become_user: "{{ pipeline_user }}"

- name: Install latest pre-release yt-dlp (unpinned, first provision only)
  ansible.builtin.command: "{{ pipx_bin }} install --pip-args=--pre yt-dlp[default]"
  when: (ytdlp_version | length) == 0 and (ytdlp_installed.stdout | trim | length) == 0
  become: "{{ ytdlp_become }}"
  become_user: "{{ pipeline_user }}"

- name: Install pinned yt-dlp distribution version
  ansible.builtin.command: "{{ pipx_bin }} install --force yt-dlp[default]=={{ ytdlp_version }}"
  when: (ytdlp_version | length) > 0 and (ytdlp_installed.stdout | trim) != ytdlp_version
  become: "{{ ytdlp_become }}"
  become_user: "{{ pipeline_user }}"

- name: Check whether curl_cffi is already injected
  ansible.builtin.command: "{{ pipx_bin }} runpip yt-dlp show curl_cffi"
  register: curl_cffi_present
  changed_when: false
  failed_when: false
  become: "{{ ytdlp_become }}"
  become_user: "{{ pipeline_user }}"

- name: Inject curl_cffi into the yt-dlp venv
  ansible.builtin.command: "{{ pipx_bin }} inject yt-dlp curl_cffi"
  when: curl_cffi_present.rc != 0
  become: "{{ ytdlp_become }}"
  become_user: "{{ pipeline_user }}"

- name: Check Deno
  ansible.builtin.command: "{{ pipeline_home }}/.local/bin/deno --version"
  register: deno_check
  changed_when: false
  failed_when: false
  become: "{{ ytdlp_become }}"
  become_user: "{{ pipeline_user }}"

- name: Install Deno 2.x into ~/.local/bin
  when: deno_check.rc != 0 or not (deno_check.stdout is match('deno 2\\.'))
  become: "{{ ytdlp_become }}"
  become_user: "{{ pipeline_user }}"
  block:
    - name: Download Deno release (checksum from the release's .sha256sum asset)
      ansible.builtin.get_url:
        url: "{{ deno_release_base }}/deno-x86_64-unknown-linux-gnu.zip"
        dest: "{{ pipeline_home }}/.local/deno-{{ deno_version }}.zip"
        checksum: "sha256:{{ deno_release_base }}/deno-x86_64-unknown-linux-gnu.zip.sha256sum"
        mode: "0644"
    - name: Unpack Deno
      ansible.builtin.unarchive:
        src: "{{ pipeline_home }}/.local/deno-{{ deno_version }}.zip"
        dest: "{{ pipeline_home }}/.local/bin"
        remote_src: true
        mode: "0755"
    - name: Remove the Deno archive
      ansible.builtin.file:
        path: "{{ pipeline_home }}/.local/deno-{{ deno_version }}.zip"
        state: absent

- name: Ensure ~/.local/bin is on pipeline_user's PATH
  ansible.builtin.command: "{{ pipx_bin }} ensurepath"
  register: ensurepath
  changed_when: "'Added' in ensurepath.stdout"
  become: "{{ ytdlp_become }}"
  become_user: "{{ pipeline_user }}"
```

Add to the playbook's `vars:` block in `deploy-ddp-transcribe.yaml`, directly under `pipeline_git_ref`:

```yaml
    # yt-dlp distribution version pin (e.g. 2026.8.25.233329.dev0); empty = latest
    # pre-release at first provision. Overwritable at the catalog item.
    ytdlp_version: ""
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `scripts/test-ytdlp-role.sh`
Expected: every line `ok:`, then `all ytdlp-role checks passed`. `get_url` accepts a `file://` URL, so the test is offline.

- [ ] **Step 6: Lint**

Run: `cd ~/src/d3i-infra/researchcloud-ddp-transcribe && .venv/bin/yamllint roles/ytdlp scripts/fixtures && .venv/bin/ansible-playbook --syntax-check deploy-ddp-transcribe.yaml`
Expected: no yamllint output; `playbook: deploy-ddp-transcribe.yaml` from the syntax check. (Full `ansible-lint` cannot run in this venv under Python 3.14; FOLLOWUPS records that.)

- [ ] **Step 7: Handoff (ORG)**

Run: `git add roles/ytdlp deploy-ddp-transcribe.yaml scripts/test-ytdlp-role.sh scripts/fixtures/ytdlp-role-play.yaml && git status --short`
Report: story **ytdlp-stack**, prefix `fix(ytdlp):`; files staged; test output; note that Task 2 adds `unzip` to `system_deps`. Danielle commits Tasks 1–2 together.

---

### Task 2: verify role proves a usable impersonation target and Deno 2 (story ytdlp-stack)

**Files:**
- Modify: `~/src/d3i-infra/researchcloud-ddp-transcribe/roles/verify/tasks/main.yaml` (binary path variable; replace the impersonation task at lines 16–24; add a Deno task)
- Modify: `~/src/d3i-infra/researchcloud-ddp-transcribe/roles/system_deps/tasks/main.yaml` (add `unzip` to the apt package list)
- Create: `~/src/d3i-infra/researchcloud-ddp-transcribe/scripts/test-verify-role.sh`
- Create: `~/src/d3i-infra/researchcloud-ddp-transcribe/scripts/fixtures/verify-role-play.yaml`

**Interfaces:**
- Consumes: `pipeline_home`, `pipeline_user`, `cuda_build`, `run_smoke_test` (existing); new vars `verify_become` (default true) and `ddp_transcribe_bin` (default `/usr/local/bin/ddp-transcribe`), both test hooks.
- Produces: a provision that fails when no impersonation target row (a line containing `curl_cffi`) lacks `(unavailable)`, or when Deno is missing or not 2.x.

- [ ] **Step 1: Write the test**

Create `scripts/fixtures/verify-role-play.yaml`:

```yaml
---
- hosts: localhost
  connection: local
  gather_facts: false
  vars:
    pipeline_user: "{{ lookup('env', 'USER') }}"
    pipeline_home: "{{ lookup('env', 'TEST_HOME') }}"
    verify_become: false
    cuda_build: false
    run_smoke_test: false
    ddp_transcribe_bin: "{{ lookup('env', 'TEST_HOME') }}/bin/ddp-transcribe"
  roles:
    - role: verify
```

Create `scripts/test-verify-role.sh`:

```bash
#!/usr/bin/env bash
# test-verify-role.sh — the verify role's yt-dlp/Deno gates against fake tools.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; REPO="$(dirname "$HERE")"
PLAYBOOK="${REPO}/.venv/bin/ansible-playbook"
FAIL=0
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then echo "ok: $d"; else echo "FAIL: $d"; FAIL=$((FAIL+1)); fi; }

run_verify() { # run_verify <targets-listing> <deno-version-line-or-empty>
  export TEST_HOME; TEST_HOME="$(mktemp -d)"; mkdir -p "$TEST_HOME/bin" "$TEST_HOME/.local/bin"
  printf '#!/usr/bin/env bash\nexit 0\n' > "$TEST_HOME/bin/ddp-transcribe"; chmod +x "$TEST_HOME/bin/ddp-transcribe"
  printf '%s\n' "$1" > "$TEST_HOME/targets.txt"
  printf '#!/usr/bin/env bash\ncat "%s"\n' "$TEST_HOME/targets.txt" > "$TEST_HOME/.local/bin/yt-dlp"; chmod +x "$TEST_HOME/.local/bin/yt-dlp"
  if [ -n "$2" ]; then printf '#!/usr/bin/env bash\necho "%s"\n' "$2" > "$TEST_HOME/.local/bin/deno"; chmod +x "$TEST_HOME/.local/bin/deno"; fi
  "$PLAYBOOK" -i localhost, "$HERE/fixtures/verify-role-play.yaml" >"$TEST_HOME/play.log" 2>&1; echo $?
}

GOOD=$'[info] Available impersonate targets\nClient          OS           Source\n--------------------------------------\nChrome-133      Macos-15     curl_cffi\nSafari-17.2     Ios-17.2     curl_cffi'
ALL_UNAVAILABLE=$'[info] Available impersonate targets\nClient    OS   Source\n--------------------------------------------\nChrome    -    curl_cffi (unavailable)\nSafari    -    curl_cffi (unavailable)'
HEADER_ONLY=$'[info] Available impersonate targets\nClient    OS   Source\n--------------------------------------------'
DENO2="deno 2.9.5 (stable, release, x86_64-unknown-linux-gnu)"
DENO1="deno 1.46.3 (stable, release, x86_64-unknown-linux-gnu)"

check "usable target + deno 2 passes"  test "$(run_verify "$GOOD" "$DENO2")" = 0
check "all targets unavailable fails"  test "$(run_verify "$ALL_UNAVAILABLE" "$DENO2")" != 0
check "header-only listing fails"      test "$(run_verify "$HEADER_ONLY" "$DENO2")" != 0
check "deno missing fails"             test "$(run_verify "$GOOD" "")" != 0
check "deno 1.x fails"                 test "$(run_verify "$GOOD" "$DENO1")" != 0

echo; [ "$FAIL" -eq 0 ] && echo "all verify-role checks passed" || { echo "$FAIL check(s) failed"; exit 1; }
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `chmod +x scripts/test-verify-role.sh && scripts/test-verify-role.sh`
Expected: "all targets unavailable fails" FAILS (the current grep for `chrome` passes an unavailable Chrome row); "deno missing fails" and "deno 1.x fails" FAIL (no Deno task exists). The play may also error on the hard-coded `/usr/local/bin/ddp-transcribe`; the edit below introduces `ddp_transcribe_bin`.

- [ ] **Step 3: Edit the verify role**

In `roles/verify/tasks/main.yaml`, make the binary path a variable in the first two tasks and in the three smoke-test commands (`{{ ddp_transcribe_bin | default('/usr/local/bin/ddp-transcribe') }}`), then replace the impersonation task with these two and add the Deno task:

```yaml
- name: List yt-dlp impersonation targets
  ansible.builtin.command: "{{ pipeline_home }}/.local/bin/yt-dlp --list-impersonate-targets"
  register: impersonate_targets
  changed_when: false
  failed_when: false
  become: "{{ verify_become | default(true) }}"
  become_user: "{{ pipeline_user }}"

- name: Verify at least one impersonation target is USABLE
  # A target row is a line containing `curl_cffi` (yt-dlp's handler name; header and
  # separator lines never contain it). A row ending in "(unavailable)" is a
  # listed-but-unusable target — exactly the state the 2026-08-25 incident produced.
  ansible.builtin.assert:
    that:
      - impersonate_targets.rc == 0
      - >-
        (impersonate_targets.stdout_lines
         | select('search', 'curl_cffi')
         | reject('search', '\(unavailable\)')
         | list | length) > 0
    fail_msg: >-
      yt-dlp lists no usable impersonation target (rc={{ impersonate_targets.rc }}).
      curl_cffi is missing from the venv or the pre-release posture did not apply;
      see roles/ytdlp and the 2026-08-25 incident record. Output:
      {{ impersonate_targets.stdout | default('') }}

- name: Verify Deno 2.x is installed for pipeline_user
  ansible.builtin.command: "{{ pipeline_home }}/.local/bin/deno --version"
  register: deno_version_out
  changed_when: false
  failed_when: deno_version_out.rc != 0 or not (deno_version_out.stdout is match('deno 2\\.'))
  become: "{{ verify_become | default(true) }}"
  become_user: "{{ pipeline_user }}"
```

In `roles/system_deps/tasks/main.yaml`, add `unzip` to the apt package list in alphabetical position (Deno ships as a zip).

- [ ] **Step 4: Run the test to verify it passes**

Run: `scripts/test-verify-role.sh`
Expected: five `ok:` lines and `all verify-role checks passed`.

- [ ] **Step 5: Lint and run the existing tier-1 checks**

Run: `.venv/bin/yamllint roles/verify roles/system_deps scripts/fixtures && .venv/bin/ansible-playbook --syntax-check deploy-ddp-transcribe.yaml && scripts/test-tiered-scripts.sh`
Expected: clean yamllint, syntax OK, the tiered-scripts test still passes (its templates are untouched).

- [ ] **Step 6: Handoff (ORG)**

Run: `git add roles/verify roles/system_deps scripts/test-verify-role.sh scripts/fixtures/verify-role-play.yaml && git status --short`
Report: story **ytdlp-stack** (same commit as Task 1), prefix `fix(ytdlp):`. Danielle commits.

---

### Task 3: pin `v0.5.1` and document `ytdlp_version` (story catalog-pins)

**Files:**
- Modify: `~/src/d3i-infra/researchcloud-ddp-transcribe/deploy-ddp-transcribe.yaml:47` (`pipeline_git_ref: "v0.4.0"` → `"v0.5.1"`)
- Modify: `~/src/d3i-infra/researchcloud-ddp-transcribe/README.md:56` (parameter table) and the "What it provisions" yt-dlp bullet
- Modify: `~/src/d3i-infra/researchcloud-ddp-transcribe/docs/catalog-item.md` §1 table (`pipeline_git_ref` row), §2 "New component declarations" (add `ytdlp_version`), §6 pipeline history line
- Modify: `~/src/d3i-infra/researchcloud-ddp-transcribe/docs/FOLLOWUPS.md` (add the Deno-necessity item)

- [ ] **Step 1: Make the edits**

`deploy-ddp-transcribe.yaml` line 47: `pipeline_git_ref: "v0.5.1"`.

README parameter table: change the `pipeline_git_ref` row's default to `` `v0.5.1` `` and add a row directly after it:

```markdown
| `ytdlp_version` | *(empty)* | keep (overwrite to pin) | yt-dlp **distribution** version to pin, e.g. `2026.8.25.233329.dev0` (note `.dev0`). Empty installs the latest pre-release at first provision and keeps it on re-runs |
```

README "What it provisions", replace the Rust/yt-dlp bullet with:

```markdown
- Rust stable (rustup, per-user) and the yt-dlp fetch stack: a **pre-release**
  `yt-dlp[default]` via pipx (stable releases before 2026-08-25 cannot pass
  TikTok's challenge flow), `curl_cffi` **injected** (the 2026-08-25 incident
  reversed the earlier never-inject rule), and Deno 2.x in `~/.local/bin`. See
  `roles/ytdlp/tasks/main.yaml` for the posture history and `ytdlp_version` for pinning.
```

`docs/catalog-item.md`: in §1's table, end the `pipeline_git_ref` Notes with "the item **overwrite** governs — `v0.5.1` since the OnlineHate launch (2026-10), playbook default bumped to match". In §2, under "New component declarations", add:

```markdown
| `ytdlp_version` | Fixed | *(blank)* | yt-dlp distribution version pin (`2026.8.25.233329.dev0` form); blank = latest pre-release at first provision |
```

followed by: "Overwritable ✓. Set it only after a validation batch has shown the latest pre-release failing (ddp-comments design §4.3)." In §6, append to the pipeline-history bullet: "overwrite bumped to `v0.5.1` for the OnlineHate launch (2026-10); `ytdlp_version` declared at the same component edit."

`docs/FOLLOWUPS.md`, under `## Open`, add:

```markdown
- **Is Deno actually required by the TikTok challenge flow?** The 2026-08-25
  incident record says its necessity was not isolated experimentally; the
  `ytdlp` role installs it deliberately. **Hypothesis (unverified):** the
  challenge solver needs a JS runtime and fails without one. Test on a
  disposable workspace by removing `~/.local/bin/deno` and running a 50-video
  batch; drop the Deno tasks if fetches are unaffected.
```

- [ ] **Step 2: Verify**

Run: `grep -n 'v0.5.1' deploy-ddp-transcribe.yaml README.md docs/catalog-item.md && grep -n 'ytdlp_version' README.md docs/catalog-item.md && .venv/bin/yamllint deploy-ddp-transcribe.yaml`
Expected: a hit in each file; yamllint clean.

- [ ] **Step 3: Handoff (ORG)**

Run: `git add deploy-ddp-transcribe.yaml README.md docs/catalog-item.md docs/FOLLOWUPS.md && git status --short`
Report: story **catalog-pins**, prefix `docs(catalog):`. The matching portal edits are in Task 11. Danielle commits.

---

### Task 4: bootstrap `researchcloud-ddp-comments` from the spike (story spike-import)

**Files:**
- Create: `README.md`, `.gitignore`, `scripts/lib.sh`, `scripts/test-all.sh`
- Create (copied from `~/projects/tiktok-harassment/comments/spike/`): `desk/mitm/addons/capture_comments.py`, `desk/firefox/policies.json`, `desk/nginx/websocket-map.conf`, `desk/nginx/fourcat.conf`, `desk/desk_stub.py`, `desk/desk-stub.service`, `docs/spike-runbook.md` (from `spike/README.md`), `docs/spike-compose-2026-09-07.yml` (from `spike/docker-compose.yml`, historical reference)
- Modify: `~/projects/tiktok-harassment/comments/PICKUP.md` (pointer at the top)

**Interfaces:**
- Produces: `scripts/lib.sh` exporting `check <desc> <cmd…>`, `FAIL`, and `report` (summary; exit 1 on failures), sourced by every later bash test; the layout `gpu/`, `desk/`, `docs/`, `scripts/`, `tests/`.

- [ ] **Step 1: Copy the spike artifacts**

```bash
cd ~/src/researchcloud-ddp-comments
S=~/projects/tiktok-harassment/comments/spike
mkdir -p desk/mitm/addons desk/firefox desk/nginx gpu docs scripts tests
cp "$S/mitm/addons/capture_comments.py" desk/mitm/addons/
cp "$S/firefox/policies.json" desk/firefox/
cp "$S/nginx/websocket-map.conf" "$S/nginx/fourcat.conf" desk/nginx/
cp "$S/desk_stub.py" desk/desk_stub.py
cp "$S/desk-stub.service" desk/desk-stub.service
cp "$S/README.md" docs/spike-runbook.md
cp "$S/docker-compose.yml" docs/spike-compose-2026-09-07.yml
```

(`nginx/desk-spike.conf` and `selection.csv` are deliberately not copied: the generator renders the locations and the feed supplies the rows.)

- [ ] **Step 2: Write the shared test helper and the runner**

`scripts/lib.sh`:

```bash
# Sourced by every scripts/test-*.sh. Counts failures; never `set -e` in tests.
set -uo pipefail
FAIL=0
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then echo "ok: $d"; else echo "FAIL: $d"; FAIL=$((FAIL+1)); fi; }
report() { echo; if [ "$FAIL" -eq 0 ]; then echo "all checks passed: $(basename "$0")"; else echo "$FAIL check(s) failed: $(basename "$0")"; exit 1; fi; }
```

`scripts/test-all.sh`:

```bash
#!/usr/bin/env bash
# Runs every hermetic test: bash tests in order, then Python unittest discovery.
set -uo pipefail
cd "$(dirname "$0")/.."
rc=0
for t in scripts/test-*.sh; do [ "$t" = scripts/test-all.sh ] && continue; echo "== $t"; "$t" || rc=1; done
echo "== python unittest"
PY=python3; [ -x .venv/bin/python ] && PY=.venv/bin/python
"$PY" -m unittest discover -s tests -v || rc=1
exit $rc
```

`.gitignore`:

```
state/
templates/firefox-profile.tar
*.pyc
__pycache__/
.venv/
```

- [ ] **Step 3: Write the README**

`README.md`:

```markdown
# researchcloud-ddp-comments

SURF Research Cloud deployment of **ddp-comments**: comment collection for the videos in
donated TikTok watch histories. Transcribed videos (from `ddp-transcribe`) become daily
worklists; collectors open them in SRAM-gated, streamed Firefox sessions where
Zeeschuimer captures the comments and uploads them to 4CAT; a mitmproxy sidecar keeps an
automatic copy.

Design: `docs/superpowers/specs/2026-10-02-monday-handover-chain-design.md`.
Spike record (2026-09-07): `docs/spike-runbook.md`, `docs/spike-compose-2026-09-07.yml`.

| Path | Runs on | Purpose |
|---|---|---|
| `gpu/feed-export.sh` | GPU workspace | read-only export of newly transcribed videos → `feed/<batch>/selection.csv` → Yoda |
| `desk/pull-feed.sh`, `desk/build_worklists.py` | desk workspace | mirror the feed, dedupe, split into per-session worklists |
| `desk/make-sessions.sh` | desk workspace | render N Firefox+mitm session pairs and the nginx locations |
| `desk/desk_stub.py`, `desk/desk-stub.service` | desk workspace | two-pane desk page per session; opens videos in that session's Firefox |
| `desk/mitm/addons/capture_comments.py` | desk workspace (in the proxy) | automatic comment capture |
| `docs/day-one.md`, `docs/collector-sheet.md` | people | operator runbook; collector protocol |

Tests: `scripts/test-all.sh` (bash + Python, no SRC access needed). The feed test needs a
`ddp-transcribe` binary: `DDP_TRANSCRIBE_BIN=~/src/ddp-transcribe/target/debug/ddp-transcribe`.
The addon test needs `python3 -m venv .venv && .venv/bin/pip install -r tests/requirements-dev.txt`.
```

- [ ] **Step 4: Point the study folder here**

Prepend to `~/projects/tiktok-harassment/comments/PICKUP.md`:

```markdown
> **2026-10:** the desk is now developed in `~/src/researchcloud-ddp-comments` (personal
> GitHub, to move into `d3i-infra` at Phase 1). `spike/` here is frozen as deployed on
> 2026-09-07; the design that supersedes it is that repo's
> `docs/superpowers/specs/2026-10-02-monday-handover-chain-design.md`.
```

- [ ] **Step 5: Verify and commit (PERSONAL)**

Run: `cd ~/src/researchcloud-ddp-comments && chmod +x scripts/test-all.sh && scripts/test-all.sh; git add -A && git status --short`
Expected: no bash tests yet; unittest reports `Ran 0 tests`. Then `git commit -m "chore(repo): import the comment-desk spike and test harness" -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"`. Report story **spike-import**.

---

### Task 5: `gpu/feed-export.sh` with the id ledger (story feed)

**Files:**
- Create: `gpu/feed-export.sh`
- Create: `scripts/test-feed-export.sh`

**Interfaces:**
- Consumes: `state.sqlite` schema v7 (`videos.video_id TEXT`, `canonical INTEGER`, `status TEXT`, `succeeded_at INTEGER`, `comment_count INTEGER`; `watch_history.respondent_id`, `video_id`, `in_window`); `gocmd` on PATH with `~/.irods` initialised.
- Produces: `feed/<UTC %Y%m%dT%H%M%SZ>-<seq>/selection.csv` with header `video_id,bucket,note,succeeded_at,comment_count,donors`; the ledger (one 19-digit id per line); stats line `candidates N / exported N / capped N / ledger N`; exit 0 success, 2 usage error, 1 runtime error. Test hooks: `FEED_PUSH=0` skips the push, `FEED_FAKE_TS` overrides the timestamp.

- [ ] **Step 1: Write the failing test**

`scripts/test-feed-export.sh`:

```bash
#!/usr/bin/env bash
# test-feed-export.sh — hermetic tests for gpu/feed-export.sh.
# Needs a ddp-transcribe binary (DDP_TRANSCRIBE_BIN) to build a real v7 state.sqlite
# from the pipeline's bundled DDP fixture; rows are marked succeeded by test-only SQL.
HERE="$(cd "$(dirname "$0")" && pwd)"; REPO="$(dirname "$HERE")"; . "$HERE/lib.sh"
BIN="${DDP_TRANSCRIBE_BIN:-$HOME/src/ddp-transcribe/target/debug/ddp-transcribe}"
FIX="${DDP_FIXTURE:-$HOME/src/ddp-transcribe/tests/fixtures/ddp/news_orgs}"
[ -x "$BIN" ] || { echo "need DDP_TRANSCRIBE_BIN (build: cd ~/src/ddp-transcribe && cargo build)"; exit 1; }
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
export PATH="$TMP/bin:$PATH"; mkdir -p "$TMP/bin"
export FAKE_GOCMD_LOG="$TMP/gocmd.log"; : > "$FAKE_GOCMD_LOG"
printf '#!/usr/bin/env bash\necho "gocmd $*" >> "$FAKE_GOCMD_LOG"; exit 0\n' > "$TMP/bin/gocmd"; chmod +x "$TMP/bin/gocmd"

DB="$TMP/state.sqlite"
"$BIN" --state-db "$DB" --inbox "$FIX" --transcripts "$TMP/t" init >/dev/null 2>&1
"$BIN" --state-db "$DB" --inbox "$FIX" --transcripts "$TMP/t" ingest >/dev/null 2>&1
sqlite3 "$DB" "UPDATE videos SET status='succeeded', succeeded_at=1700000000 WHERE canonical=1;"
TOTAL=$(sqlite3 "$DB" "SELECT count(*) FROM videos WHERE canonical=1 AND status='succeeded';")
check "fixture has succeeded rows" test "$TOTAL" -gt 3
MD5_BEFORE=$(md5sum "$DB" | cut -d' ' -f1)

EXPORT="$REPO/gpu/feed-export.sh"
OUT="$TMP/feed-out"; LEDGER="$OUT/exported-ids.txt"

# 1. first run, no cap
out=$("$EXPORT" --state-db "$DB" --out "$OUT" --ledger "$LEDGER" --collection /nluu10p/home/test 2>&1); rc=$?
check "first run exits 0" test "$rc" = 0
B1=$(ls -d "$OUT"/feed/*/ | head -1)
check "one batch dir" test "$(ls -d "$OUT"/feed/*/ | wc -l)" = 1
check "header exact" test "$(head -1 "$B1/selection.csv")" = "video_id,bucket,note,succeeded_at,comment_count,donors"
check "all rows exported" test "$(tail -n +2 "$B1/selection.csv" | wc -l)" = "$TOTAL"
check "ledger has all ids" test "$(wc -l < "$LEDGER")" = "$TOTAL"
check "stats line" bash -c "echo '$out' | grep -q 'candidates $TOTAL / exported $TOTAL / capped 0 / ledger $TOTAL'"
check "rows ordered video_id DESC" bash -c "tail -n +2 '$B1/selection.csv' | cut -d, -f1 | sort -r -c"
check "bucket is random" bash -c "! tail -n +2 '$B1/selection.csv' | cut -d, -f2 | grep -qv '^random$'"
check "push targets the collection PARENT" grep -q "gocmd sync $OUT/feed i:/nluu10p/home/test" "$FAKE_GOCMD_LOG"
check "DB untouched" test "$(md5sum "$DB" | cut -d' ' -f1)" = "$MD5_BEFORE"
check "no WAL left behind" bash -c "! ls '$DB-wal' 2>/dev/null"

# 2. second run: nothing new, still syncs feed/ (retry), no new batch dir
: > "$FAKE_GOCMD_LOG"
out=$("$EXPORT" --state-db "$DB" --out "$OUT" --ledger "$LEDGER" --collection /nluu10p/home/test 2>&1)
check "second run exports none" bash -c "echo '$out' | grep -q 'candidates 0 / exported 0'"
check "second run adds no batch dir" test "$(ls -d "$OUT"/feed/*/ | wc -l)" = 1
check "second run still syncs feed/" grep -q "gocmd sync" "$FAKE_GOCMD_LOG"

# 3. cap: fresh ledger, cap 2 → 2 exported, rest capped; next run picks up the remainder
OUT2="$TMP/feed-cap"; L2="$OUT2/ledger.txt"
out=$("$EXPORT" --state-db "$DB" --out "$OUT2" --ledger "$L2" --cap 2 --collection /c 2>&1)
check "cap exports 2" bash -c "echo '$out' | grep -q 'candidates $TOTAL / exported 2 / capped $((TOTAL-2)) / ledger 2'"
out=$("$EXPORT" --state-db "$DB" --out "$OUT2" --ledger "$L2" --cap 1000 --collection /c 2>&1)
check "next run exports the remainder" bash -c "echo '$out' | grep -q 'exported $((TOTAL-2)) / capped 0 / ledger $TOTAL'"
check "two batch dirs with distinct names" test "$(ls -d "$OUT2"/feed/*/ | sort -u | wc -l)" = 2

# 4. CRLF ledger (review focus 1)
OUT3="$TMP/feed-crlf"; L3="$OUT3/ledger.txt"; mkdir -p "$OUT3"
FIRST=$(sqlite3 "$DB" "SELECT video_id FROM videos WHERE canonical=1 ORDER BY video_id DESC LIMIT 1;")
printf '%s\r\n\r\n' "$FIRST" > "$L3"
out=$("$EXPORT" --state-db "$DB" --out "$OUT3" --ledger "$L3" --collection /c 2>&1); rc=$?
check "CRLF ledger: exits 0" test "$rc" = 0
check "CRLF ledger: the ledgered id is excluded" bash -c "echo '$out' | grep -q 'exported $((TOTAL-1))'"
check "CRLF ledger: not re-exported" bash -c "! grep -q '^$FIRST,' $OUT3/feed/*/selection.csv"

# 5. FEED_PUSH=0 never calls gocmd; bad args exit 2 without touching the ledger (review focus 5)
: > "$FAKE_GOCMD_LOG"; OUT4="$TMP/feed-nopush"
FEED_PUSH=0 "$EXPORT" --state-db "$DB" --out "$OUT4" --ledger "$OUT4/l" >/dev/null 2>&1
check "FEED_PUSH=0: gocmd not called" test ! -s "$FAKE_GOCMD_LOG"
OUT5="$TMP/feed-bad"; mkdir -p "$OUT5"; : > "$OUT5/l"
"$EXPORT" --state-db "$DB" --out "$OUT5" --ledger "$OUT5/l" --cap 0 --collection /c >/dev/null 2>&1; r1=$?
"$EXPORT" --state-db "$DB" --out "$OUT5" --ledger "$OUT5/l" --cap abc --collection /c >/dev/null 2>&1; r2=$?
"$EXPORT" --state-db "$DB" --out "$OUT5" --ledger "$OUT5/l" >/dev/null 2>&1; r3=$?     # push enabled, no collection
check "cap 0 is usage error" test "$r1" = 2
check "cap abc is usage error" test "$r2" = 2
check "missing --collection with push is usage error" test "$r3" = 2
check "bad args left ledger empty" test ! -s "$OUT5/l"
check "bad args wrote no batch" bash -c "! ls -d '$OUT5'/feed/*/ 2>/dev/null"

# 6. two exports in one second get distinct dirs (sequence suffix)
OUT6="$TMP/feed-seq"; mkdir -p "$OUT6/feed/20260101T000000Z-0"
FEED_PUSH=0 FEED_FAKE_TS=20260101T000000Z "$EXPORT" --state-db "$DB" --out "$OUT6" --ledger "$OUT6/l" >/dev/null 2>&1
check "existing batch name is not reused" test -d "$OUT6/feed/20260101T000000Z-1"

report
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd ~/src/ddp-transcribe && cargo build 2>&1 | tail -1 && cd ~/src/researchcloud-ddp-comments && chmod +x scripts/test-feed-export.sh && scripts/test-feed-export.sh`
Expected: "fixture has succeeded rows" ok, everything else FAIL (script missing).

- [ ] **Step 3: Write `gpu/feed-export.sh`**

```bash
#!/usr/bin/env bash
# feed-export.sh — read-only export of newly transcribed videos for the comment desk.
# Runs on the GPU workspace after `process` and `load-metadata`. Lives in the
# researchcloud-ddp-comments repo (ADR-0041 keeps wrapper scripts out of the
# pipeline's operator surface; a `feed` subcommand replaces this at Phase 1).
#
#   feed-export.sh --state-db PATH [--out DIR] [--ledger FILE] [--cap N] --collection IRODS_PATH
#   FEED_PUSH=0   skip the gocmd push (tests, or when Yoda is down; re-run to retry)
#
# Output: <out>/feed/<UTC %Y%m%dT%H%M%SZ>-<seq>/selection.csv
#   header: video_id,bucket,note,succeeded_at,comment_count,donors
# Ledger: one exported video_id per line; appended only after the CSV is in place.
# The DB is opened read-only; the temp ledger table is filled BEFORE query_only.
set -euo pipefail

usage() { echo "usage: $0 --state-db PATH [--out DIR] [--ledger FILE] [--cap N] --collection PATH" >&2; exit 2; }
DB=""; OUT="$HOME/ddp-feed"; LEDGER=""; CAP=""; COLLECTION=""
while [ $# -gt 0 ]; do
  case "$1" in
    --state-db) DB="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --ledger) LEDGER="$2"; shift 2 ;;
    --cap) CAP="$2"; shift 2 ;;
    --collection) COLLECTION="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$DB" ] && [ -r "$DB" ] || usage
LEDGER="${LEDGER:-$OUT/exported-ids.txt}"
if [ -n "$CAP" ]; then [[ "$CAP" =~ ^[1-9][0-9]*$ ]] || usage; fi
if [ "${FEED_PUSH:-1}" != 0 ] && [ -z "$COLLECTION" ]; then usage; fi

mkdir -p "$OUT/feed" "$(dirname "$LEDGER")"; touch "$LEDGER"
exec 9>"$OUT/.export.lock"; flock 9

# Ledger → temp-table INSERTs. Only 19-digit lines count; CR and blanks are dropped.
LEDGER_SQL="$(tr -d '\r' < "$LEDGER" | grep -E '^[0-9]{19}$' | sed "s/.*/INSERT OR IGNORE INTO ledger VALUES('&');/" || true)"
LIMIT_SQL=""; [ -n "$CAP" ] && LIMIT_SQL="LIMIT $CAP"

SELECT_SQL="SELECT v.video_id, 'random' AS bucket,
  'comments ' || COALESCE(CAST(v.comment_count AS TEXT), '?') || ' / donors ' || COALESCE(d.donors, 0) AS note,
  v.succeeded_at, COALESCE(CAST(v.comment_count AS TEXT), '') AS comment_count, COALESCE(d.donors, 0) AS donors
FROM videos v
LEFT JOIN (SELECT video_id, COUNT(DISTINCT respondent_id) AS donors FROM watch_history WHERE in_window = 1 GROUP BY video_id) d
  ON d.video_id = v.video_id
WHERE v.canonical = 1 AND v.status = 'succeeded' AND v.video_id NOT IN (SELECT video_id FROM ledger)
ORDER BY v.video_id DESC $LIMIT_SQL;"
COUNT_SQL="SELECT count(*) FROM videos v WHERE v.canonical = 1 AND v.status = 'succeeded' AND v.video_id NOT IN (SELECT video_id FROM ledger);"

run_sql() { # read-only connection; ledger first, then query_only, then the statements on stdin
  { printf 'CREATE TEMP TABLE ledger(video_id TEXT PRIMARY KEY);\n%s\nPRAGMA query_only = 1;\n' "$LEDGER_SQL"; cat; } \
    | sqlite3 -bail "file:${DB}?mode=ro"
}

CANDIDATES="$(printf '%s\n' "$COUNT_SQL" | run_sql)"
TMPCSV="$(mktemp "$OUT/.selection.XXXXXX")"
printf '.headers on\n.mode csv\n%s\n' "$SELECT_SQL" | run_sql > "$TMPCSV"
EXPORTED=$(( $(wc -l < "$TMPCSV") - 1 ))

if [ "$EXPORTED" -gt 0 ]; then
  TS="${FEED_FAKE_TS:-$(date -u +%Y%m%dT%H%M%SZ)}"; SEQ=0
  while [ -e "$OUT/feed/$TS-$SEQ" ]; do SEQ=$((SEQ+1)); done
  BATCH="$OUT/feed/$TS-$SEQ"; mkdir "$BATCH"
  mv "$TMPCSV" "$BATCH/selection.csv"
  tail -n +2 "$BATCH/selection.csv" | cut -d, -f1 >> "$LEDGER"
else
  rm -f "$TMPCSV"
fi
CAPPED=$(( CANDIDATES - EXPORTED ))
echo "candidates $CANDIDATES / exported $EXPORTED / capped $CAPPED / ledger $(tr -d '\r' < "$LEDGER" | grep -cE '^[0-9]{19}$' || true)"

if [ "${FEED_PUSH:-1}" != 0 ]; then
  # Destination is the PARENT: gocmd lands SRC as DEST/basename(SRC) when DEST exists,
  # so this produces <collection>/feed/<batch>/... and never feed/feed.
  gocmd sync "$OUT/feed" "i:$COLLECTION"
fi
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `chmod +x gpu/feed-export.sh && scripts/test-feed-export.sh`
Expected: all `ok:`, `all checks passed: test-feed-export.sh`. If "no WAL left behind" fails, the DB was opened read-write somewhere: check the `mode=ro` URI survived quoting. If `ingest` leaves the fixture's rows with `canonical = 0` for some, `TOTAL` still counts only canonical rows, which is what the script exports.

- [ ] **Step 5: Commit (PERSONAL)**

`git add gpu/feed-export.sh scripts/test-feed-export.sh && git commit -m "feat(feed): ledger-based read-only feed export for the GPU workspace" -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"`. Report story **feed**.

---

### Task 6: `desk/pull-feed.sh` and `desk/build_worklists.py` (story feed)

**Files:**
- Create: `desk/pull-feed.sh`, `desk/build_worklists.py`
- Create: `tests/test_build_worklists.py`, `scripts/test-pull-feed.sh`

**Interfaces:**
- Consumes: `<desk>/feed/<batch>/selection.csv` (Task 5 contract); `<desk>/state/desk-log.ndjson` lines `{"t":…,"user":…,"session":"2","action":"open","video_id":"…","code":"","note":"","exec_rc":0}` (Task 9 contract); `gocmd`, `systemctl` on PATH.
- Produces: `<desk>/loaded.txt` (one batch dir name per line), `<desk>/pool.csv` (header `video_id,bucket,note,first_batch`), `<desk>/worklist/session-<i>.csv` (header `video_id,bucket,note`) for `i = 1..N`, written atomically. `build_worklists.py --desk DIR --sessions N [--latest-batch NAME]` prints `outstanding N / carried N / new N / sessions N`.

- [ ] **Step 1: Write the failing Python test**

`tests/test_build_worklists.py`:

```python
import csv, json, subprocess, sys, tempfile, unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SCRIPT = HERE.parent / "desk" / "build_worklists.py"
HEADER = ["video_id", "bucket", "note", "first_batch"]


def write_csv(path, rows, header):
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", newline="") as f:
        w = csv.writer(f); w.writerow(header); w.writerows(rows)


class BuildWorklists(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp()); self.desk = self.tmp / "desk"
        (self.desk / "state").mkdir(parents=True)
        self.pool = self.desk / "pool.csv"

    def run_build(self, sessions, latest=None):
        cmd = [sys.executable, str(SCRIPT), "--desk", str(self.desk), "--sessions", str(sessions)]
        if latest: cmd += ["--latest-batch", latest]
        return subprocess.run(cmd, capture_output=True, text=True)

    def read_session(self, i):
        with open(self.desk / "worklist" / f"session-{i}.csv", newline="") as f:
            return list(csv.DictReader(f))

    def test_round_robin_disjoint_and_newest_first(self):
        ids = [f"7{str(n).zfill(18)}" for n in range(10, 16)]  # 6 ids
        write_csv(self.pool, [[v, "random", "comments 1 / donors 1", "B1"] for v in ids], HEADER)
        r = self.run_build(3, latest="B1"); self.assertEqual(r.returncode, 0, r.stderr)
        s = [self.read_session(i) for i in (1, 2, 3)]
        allids = [row["video_id"] for part in s for row in part]
        self.assertEqual(sorted(allids), sorted(ids))
        self.assertEqual(len(set(allids)), 6)                       # disjoint
        self.assertEqual([len(p) for p in s], [2, 2, 2])
        self.assertEqual(s[0][0]["video_id"], max(ids))             # newest first
        self.assertEqual(list(s[0][0].keys()), ["video_id", "bucket", "note"])
        self.assertIn("outstanding 6 / carried 0 / new 6 / sessions 3", r.stdout)

    def test_opened_rows_drop_and_carried_rows_come_first(self):
        old = ["7000000000000000001", "7000000000000000002"]
        new = ["7000000000000000009", "7000000000000000008"]
        write_csv(self.pool, [[v, "random", "n", "B1"] for v in old] + [[v, "random", "n", "B2"] for v in new], HEADER)
        log = self.desk / "state" / "desk-log.ndjson"
        with open(log, "w") as f:
            f.write(json.dumps({"t": 1, "user": "a", "session": "1", "action": "open", "video_id": old[0], "code": "", "note": "", "exec_rc": 0}) + "\n")
            f.write(json.dumps({"t": 2, "user": "a", "session": "1", "action": "open", "video_id": old[1], "code": "", "note": "", "exec_rc": 1}) + "\n")  # failed open stays outstanding
        r = self.run_build(1, latest="B2"); self.assertEqual(r.returncode, 0, r.stderr)
        rows = [x["video_id"] for x in self.read_session(1)]
        self.assertEqual(rows, [old[1], new[0], new[1]])               # carried first, then new by id DESC
        self.assertIn("outstanding 3 / carried 1 / new 2", r.stdout)

    def test_quoted_note_round_trips(self):   # review focus 2
        write_csv(self.pool, [["7000000000000000005", "random", 'comments 3 / donors 1, "pilot"', "B1"]], HEADER)
        r = self.run_build(1, latest="B1"); self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.read_session(1)[0]["note"], 'comments 3 / donors 1, "pilot"')

    def test_atomic_write_leaves_no_tmp(self):
        write_csv(self.pool, [["7000000000000000005", "random", "n", "B1"]], HEADER)
        self.run_build(2, latest="B1")
        names = sorted(p.name for p in (self.desk / "worklist").iterdir())
        self.assertEqual(names, ["session-1.csv", "session-2.csv"])


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd ~/src/researchcloud-ddp-comments && python3 -m unittest tests.test_build_worklists -v`
Expected: 4 errors, `No such file or directory: .../desk/build_worklists.py`.

- [ ] **Step 3: Write `desk/build_worklists.py`**

```python
#!/usr/bin/env python3
"""Build per-session worklists from the desk pool.

    build_worklists.py --desk ~/desk --sessions 3 [--latest-batch <name>]

Reads  <desk>/pool.csv              (video_id,bucket,note,first_batch)
       <desk>/state/desk-log.ndjson (opens with exec_rc 0 mark a row as opened)
Writes <desk>/worklist/session-<i>.csv (video_id,bucket,note), atomically.
Order: rows carried forward from earlier batches first, then rows from the latest batch;
within each group newest-published first (video_id DESC). Split round-robin so sessions
never share a row. Prints: outstanding N / carried N / new N / sessions N
"""
import argparse, csv, json, os, sys, tempfile
from pathlib import Path


def opened_ids(log_path: Path) -> set:
    done = set()
    if not log_path.exists():
        return done
    with open(log_path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            if ev.get("action") == "open" and ev.get("exec_rc") == 0 and ev.get("video_id"):
                done.add(ev["video_id"])
    return done


def write_atomic(path: Path, rows):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix=".tmp-")
    with os.fdopen(fd, "w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=["video_id", "bucket", "note"], extrasaction="ignore")
        w.writeheader(); w.writerows(rows)
    os.replace(tmp, path)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--desk", required=True); ap.add_argument("--sessions", type=int, required=True)
    ap.add_argument("--latest-batch", default=None)
    a = ap.parse_args()
    if a.sessions < 1:
        sys.exit("--sessions must be >= 1")
    desk = Path(a.desk)
    pool_path = desk / "pool.csv"
    rows = []
    if pool_path.exists():
        with open(pool_path, newline="", encoding="utf-8") as f:
            rows = list(csv.DictReader(f))
    done = opened_ids(desk / "state" / "desk-log.ndjson")
    outstanding = [r for r in rows if r["video_id"] not in done]
    latest = a.latest_batch or max((r["first_batch"] for r in rows), default=None)
    carried = sorted((r for r in outstanding if r["first_batch"] != latest), key=lambda r: r["video_id"], reverse=True)
    new = sorted((r for r in outstanding if r["first_batch"] == latest), key=lambda r: r["video_id"], reverse=True)
    ordered = carried + new
    buckets = [[] for _ in range(a.sessions)]
    for i, r in enumerate(ordered):
        buckets[i % a.sessions].append(r)
    for i, b in enumerate(buckets, start=1):
        write_atomic(desk / "worklist" / f"session-{i}.csv", b)
    print(f"outstanding {len(ordered)} / carried {len(carried)} / new {len(new)} / sessions {a.sessions}")


if __name__ == "__main__":
    main()
```

- [ ] **Step 4: Run the Python test to verify it passes**

Run: `python3 -m unittest tests.test_build_worklists -v`
Expected: `Ran 4 tests … OK`.

- [ ] **Step 5: Write the failing bash test for `pull-feed.sh`**

`scripts/test-pull-feed.sh`:

```bash
#!/usr/bin/env bash
# test-pull-feed.sh — pull-feed.sh against a fake gocmd (copies from a fake remote) and a fake systemctl.
HERE="$(cd "$(dirname "$0")" && pwd)"; REPO="$(dirname "$HERE")"; . "$HERE/lib.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
export PATH="$TMP/bin:$PATH"; mkdir -p "$TMP/bin"
export FAKE_REMOTE="$TMP/remote"; export FAKE_LOG="$TMP/calls.log"; : > "$FAKE_LOG"
cat > "$TMP/bin/gocmd" <<'FAKE'
#!/usr/bin/env bash
echo "gocmd $*" >> "$FAKE_LOG"
resolve() { case "$1" in i:*) printf '%s' "${FAKE_REMOTE}${1#i:}";; *) printf '%s' "$1";; esac; }
[ "$1" = sync ] || exit 0
s="$(resolve "$2")"; d="$(resolve "$3")"
if [ -d "$d" ]; then cp -r "$s" "$d/"; else mkdir -p "$d"; cp -r "$s/." "$d/"; fi
FAKE
printf '#!/usr/bin/env bash\necho "systemctl $*" >> "$FAKE_LOG"\n' > "$TMP/bin/systemctl"
printf '#!/usr/bin/env bash\nexit 1\n' > "$TMP/bin/sudo"      # force the direct systemctl branch
chmod +x "$TMP/bin/gocmd" "$TMP/bin/systemctl" "$TMP/bin/sudo"

COLL="/nluu10p/home/test"; mkdir -p "$FAKE_REMOTE$COLL/feed/20260101T080000Z-0" "$FAKE_REMOTE$COLL/feed/20260101T090000Z-0"
H="video_id,bucket,note,succeeded_at,comment_count,donors"
printf '%s\n7000000000000000003,random,comments 3 / donors 1,1700000000,3,1\n7000000000000000001,random,n,1700000000,,0\n' "$H" > "$FAKE_REMOTE$COLL/feed/20260101T080000Z-0/selection.csv"
printf '%s\n7000000000000000009,random,n,1700000001,5,2\n7000000000000000003,random,dup,1700000001,3,1\n' "$H" > "$FAKE_REMOTE$COLL/feed/20260101T090000Z-0/selection.csv"

PULL="$REPO/desk/pull-feed.sh"; DESK="$TMP/desk"; mkdir -p "$DESK/state"

out=$("$PULL" --desk "$DESK" --sessions 2 --collection "$COLL" 2>&1); rc=$?
check "pull exits 0" test "$rc" = 0
check "pull targets the desk PARENT (lands as desk/feed)" grep -q "gocmd sync i:$COLL/feed $DESK\$" "$FAKE_LOG"
check "both batches recorded in loaded.txt" test "$(wc -l < "$DESK/loaded.txt")" = 2
check "pool has 3 unique ids" test "$(tail -n +2 "$DESK/pool.csv" | wc -l)" = 3
check "duplicate keeps first occurrence's note" grep -q '^7000000000000000003,random,comments 3 / donors 1,' "$DESK/pool.csv"
check "two session files" test "$(ls "$DESK/worklist" | wc -l)" = 2
check "sessions disjoint, all 3 ids" test "$(cat "$DESK/worklist"/session-*.csv | grep -c '^7')" = 3
check "stub signalled with HUP" grep -q "systemctl kill -s HUP desk-stub" "$FAKE_LOG"

# second pull: nothing new
: > "$FAKE_LOG"
"$PULL" --desk "$DESK" --sessions 2 --collection "$COLL" >/dev/null 2>&1
check "re-pull adds nothing to loaded.txt" test "$(wc -l < "$DESK/loaded.txt")" = 2
check "re-pull keeps pool at 3" test "$(tail -n +2 "$DESK/pool.csv" | wc -l)" = 3

# invalid batch (bad header) is rejected and not loaded; session files untouched
mkdir -p "$FAKE_REMOTE$COLL/feed/20260101T100000Z-0"; printf 'id,b\n1,2\n' > "$FAKE_REMOTE$COLL/feed/20260101T100000Z-0/selection.csv"
BEFORE=$(md5sum "$DESK/worklist/session-1.csv")
out=$("$PULL" --desk "$DESK" --sessions 2 --collection "$COLL" 2>&1); rc=$?
check "invalid batch: non-zero exit" test "$rc" != 0
check "invalid batch: named in output" bash -c "echo '$out' | grep -q '20260101T100000Z-0'"
check "invalid batch: not in loaded.txt" bash -c "! grep -q 20260101T100000Z-0 '$DESK/loaded.txt'"
check "invalid batch: session file unchanged" test "$(md5sum "$DESK/worklist/session-1.csv")" = "$BEFORE"
rm -rf "$FAKE_REMOTE$COLL/feed/20260101T100000Z-0" "$DESK/feed/20260101T100000Z-0"

# offline mode: no gocmd call, picks up a batch copied in by hand
: > "$FAKE_LOG"; mkdir -p "$DESK/feed/20260102T080000Z-0"
printf '%s\n7000000000000000011,random,n,1700000002,1,1\n' "$H" > "$DESK/feed/20260102T080000Z-0/selection.csv"
"$PULL" --desk "$DESK" --sessions 2 --offline >/dev/null 2>&1
check "offline: gocmd not called" bash -c "! grep -q gocmd '$FAKE_LOG'"
check "offline: new batch loaded" grep -q 20260102T080000Z-0 "$DESK/loaded.txt"
check "offline: pool now 4" test "$(tail -n +2 "$DESK/pool.csv" | wc -l)" = 4

report
```

- [ ] **Step 6: Run it to verify it fails**

Run: `chmod +x scripts/test-pull-feed.sh && scripts/test-pull-feed.sh`
Expected: all FAIL (script missing).

- [ ] **Step 7: Write `desk/pull-feed.sh`**

```bash
#!/usr/bin/env bash
# pull-feed.sh — mirror the feed from Yoda, load new batches into the pool, rebuild
# per-session worklists, signal the desk stub.
#
#   pull-feed.sh --desk DIR --sessions N [--collection IRODS_PATH] [--offline]
#   --offline   skip gocmd; expects batch dirs copied into <desk>/feed/ by hand (scp fallback)
#
# Files under <desk>: feed/<batch>/selection.csv (mirror), loaded.txt (batches loaded),
# pool.csv (video_id,bucket,note,first_batch), worklist/session-<i>.csv.
# Run as the operator account that ran `gocmd init`; only the systemctl signal needs
# root (the unit runs as root): tries `sudo -n systemctl …`, then plain `systemctl …`.
set -euo pipefail
usage() { echo "usage: $0 --desk DIR --sessions N [--collection PATH] [--offline]" >&2; exit 2; }
DESK=""; SESSIONS=""; COLLECTION=""; OFFLINE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --desk) DESK="$2"; shift 2 ;; --sessions) SESSIONS="$2"; shift 2 ;;
    --collection) COLLECTION="$2"; shift 2 ;; --offline) OFFLINE=1; shift ;; *) usage ;;
  esac
done
[ -n "$DESK" ] && [[ "${SESSIONS:-}" =~ ^[1-9][0-9]*$ ]] || usage
[ "$OFFLINE" = 1 ] || [ -n "$COLLECTION" ] || usage
HERE="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$DESK/feed" "$DESK/worklist" "$DESK/state"; touch "$DESK/loaded.txt"
exec 9>"$DESK/.pull.lock"; flock 9

if [ "$OFFLINE" = 0 ]; then
  # Destination is the PARENT: gocmd lands SRC as DEST/basename(SRC) → <desk>/feed.
  gocmd sync "i:$COLLECTION/feed" "$DESK"
fi

HEADER="video_id,bucket,note,succeeded_at,comment_count,donors"
[ -s "$DESK/pool.csv" ] || echo "video_id,bucket,note,first_batch" > "$DESK/pool.csv"
BAD=0; LOADED=0; LATEST=""
for csvf in $(ls -d "$DESK"/feed/*/selection.csv 2>/dev/null | sort); do
  batch="$(basename "$(dirname "$csvf")")"
  grep -qx "$batch" "$DESK/loaded.txt" && { LATEST="$batch"; continue; }
  # validate the whole file before loading any of it
  if [ "$(head -1 "$csvf" | tr -d '\r')" != "$HEADER" ] || tail -n +2 "$csvf" | tr -d '\r' | grep -qvE '^[0-9]{19},'; then
    echo "invalid batch $batch: bad header or non-19-digit video_id; not loaded" >&2; BAD=$((BAD+1)); continue
  fi
  python3 - "$csvf" "$DESK/pool.csv" "$batch" <<'PY'
import csv, sys, os, tempfile
src, pool, batch = sys.argv[1:4]
seen = set(); rows = []
with open(pool, newline="", encoding="utf-8") as f:
    for r in csv.DictReader(f):
        rows.append(r); seen.add(r["video_id"])
with open(src, newline="", encoding="utf-8") as f:
    for r in csv.DictReader(f):
        if r["video_id"] in seen: continue
        seen.add(r["video_id"]); rows.append({"video_id": r["video_id"], "bucket": r["bucket"], "note": r["note"], "first_batch": batch})
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(pool), prefix=".tmp-")
with os.fdopen(fd, "w", newline="", encoding="utf-8") as f:
    w = csv.DictWriter(f, fieldnames=["video_id", "bucket", "note", "first_batch"]); w.writeheader(); w.writerows(rows)
os.replace(tmp, pool)
PY
  echo "$batch" >> "$DESK/loaded.txt"; LOADED=$((LOADED+1)); LATEST="$batch"
done

if [ "$LOADED" -gt 0 ]; then
  python3 "$HERE/build_worklists.py" --desk "$DESK" --sessions "$SESSIONS" ${LATEST:+--latest-batch "$LATEST"}
  sudo -n systemctl kill -s HUP desk-stub 2>/dev/null || systemctl kill -s HUP desk-stub || echo "could not signal desk-stub (not running?)" >&2
else
  echo "no new batches"
fi
[ "$BAD" -eq 0 ] || exit 1
```

- [ ] **Step 8: Run the bash test to verify it passes**

Run: `chmod +x desk/pull-feed.sh desk/build_worklists.py && scripts/test-pull-feed.sh`
Expected: all `ok:`.

- [ ] **Step 9: Commit (PERSONAL)**

`git add desk/pull-feed.sh desk/build_worklists.py tests/test_build_worklists.py scripts/test-pull-feed.sh && git commit -m "feat(feed): desk pull with loaded-batch ledger, pool, and per-session worklists" -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"`. Report story **feed**.

---

### Task 7: capture addon keeps bodies and video identity (story capture)

**Files:**
- Modify: `desk/mitm/addons/capture_comments.py` (whole file)
- Create: `tests/test_capture_addon.py`, `tests/requirements-dev.txt`

**Interfaces:**
- Consumes: env `DESK_SESSION`, `CAPTURE_ROOT` (new, default `/captures`); the proxy mounts `state/captures/<i>` at `/captures` (Task 8), so `DESK_USER` is no longer part of the path.
- Produces: `/captures/<YYYY-MM-DD>/api-calls.ndjson` lines with `aweme_id` (from the request), `parent_cid` (reply calls), `http_status`, `body` ∈ `ok|empty|missing|invalid`, plus `n, status_code, total, has_more, cursor, endpoint, path`; `session-<i>.ndjson` comment lines as before; `opens.ndjson` as before.

- [ ] **Step 1: Install mitmproxy into a dev venv and write the failing test**

`tests/requirements-dev.txt`: `mitmproxy==12.2.3`

Run: `cd ~/src/researchcloud-ddp-comments && python3 -m venv .venv && .venv/bin/pip install -q -r tests/requirements-dev.txt`

`tests/test_capture_addon.py`:

```python
import importlib.util, json, os, tempfile, unittest
from pathlib import Path

from mitmproxy.test import tflow

HERE = Path(__file__).resolve().parent
ADDON = HERE.parent / "desk" / "mitm" / "addons" / "capture_comments.py"


def load_addon(root):
    os.environ["CAPTURE_ROOT"] = str(root); os.environ["DESK_SESSION"] = "2"
    spec = importlib.util.spec_from_file_location("capture_comments", ADDON)
    mod = importlib.util.module_from_spec(spec); spec.loader.exec_module(mod)
    return mod


def comment_flow(path, body, status=200, host="www.tiktok.com"):
    f = tflow.tflow(resp=True)
    f.request.scheme = "https"; f.request.host = host; f.request.port = 443
    f.request.method = "GET"; f.request.path = path
    f.response.status_code = status
    f.response.headers["content-type"] = "application/json"
    if body is None:
        f.response.content = None
    else:
        f.response.text = body if isinstance(body, str) else json.dumps(body)
    return f


def read_ndjson(p):
    return [json.loads(l) for l in open(p, encoding="utf-8") if l.strip()]


class CaptureAddon(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp()); self.mod = load_addon(self.root); self.cap = self.mod.Capture()

    def calls(self):
        files = list(self.root.glob("*/api-calls.ndjson")); self.assertEqual(len(files), 1); return read_ndjson(files[0])

    def test_buffered_response_records_n_and_aweme_from_request(self):
        body = {"comments": [{"cid": "1", "aweme_id": "7000000000000000001", "text": "hi", "user": {}}], "total": 1, "has_more": 0, "cursor": 20, "status_code": 0}
        self.cap.response(comment_flow("/api/comment/list/?aweme_id=7000000000000000001&cursor=0&count=20", body))
        c = self.calls()[0]
        self.assertEqual((c["n"], c["aweme_id"], c["body"], c["http_status"], c["endpoint"]), (1, "7000000000000000001", "ok", 200, "list"))
        sess = read_ndjson(next(self.root.glob("*/session-2.ndjson")))
        self.assertEqual(sess[0]["cid"], "1")

    def test_empty_comments_keeps_video_identity(self):
        self.cap.response(comment_flow("/api/comment/list/?aweme_id=7000000000000000002&cursor=0", {"comments": [], "total": 0, "has_more": 0}))
        c = self.calls()[0]
        self.assertEqual((c["n"], c["aweme_id"], c["body"]), (0, "7000000000000000002", "empty"))

    def test_missing_body_is_recorded_not_misread_as_empty(self):
        self.cap.response(comment_flow("/api/comment/list/?aweme_id=7000000000000000003", None))
        c = self.calls()[0]
        self.assertEqual((c["aweme_id"], c["body"], c["n"]), ("7000000000000000003", "missing", 0))

    def test_invalid_json_is_recorded(self):
        self.cap.response(comment_flow("/api/comment/list/?aweme_id=7000000000000000004", "<html>blocked</html>", status=200))
        self.assertEqual(self.calls()[0]["body"], "invalid")

    def test_reply_call_records_parent_cid(self):
        self.cap.response(comment_flow("/api/comment/list/reply/?item_id=7000000000000000005&comment_id=99&cursor=0", {"comments": [], "total": 0}))
        c = self.calls()[0]
        self.assertEqual((c["endpoint"], c["aweme_id"], c["parent_cid"]), ("reply", "7000000000000000005", "99"))

    def test_responseheaders_disables_streaming_for_comment_api_only(self):
        f = comment_flow("/api/comment/list/?aweme_id=1", {"comments": []}); f.response.stream = True
        self.cap.responseheaders(f); self.assertFalse(f.response.stream)
        g = comment_flow("/video/tos/big.mp4", "x"); g.response.stream = True
        self.cap.responseheaders(g); self.assertTrue(g.response.stream)

    def test_non_tiktok_hosts_ignored(self):
        self.cap.response(comment_flow("/api/comment/list/?aweme_id=1", {"comments": []}, host="example.org"))
        self.assertEqual(list(self.root.glob("*/*.ndjson")), [])


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run it to verify it fails**

Run: `.venv/bin/python -m unittest tests.test_capture_addon -v`
Expected: failures: `aweme_id` None on empty/missing, `KeyError: 'body'`, no `responseheaders` attribute, writes attempted under `/captures` (PermissionError) because `CAPTURE_ROOT` is ignored.

- [ ] **Step 3: Rewrite the addon**

`desk/mitm/addons/capture_comments.py`:

```python
"""mitmproxy addon: record TikTok comment-list responses as NDJSON.

Per session the proxy mounts state/captures/<i> at /captures (CAPTURE_ROOT). Files under
/captures/<YYYY-MM-DD>/:
  api-calls.ndjson    one line per comment-API call: aweme_id and parent_cid from the REQUEST,
                      http_status, body ok|empty|missing|invalid, n, total, has_more, cursor
  session-<i>.ndjson  one line per comment (raw object included)
  opens.ndjson        every /video/ or /photo/ page the browser loads
Comment-API responses are kept BUFFERED even when mitmdump streams large bodies
(`--set stream_large_bodies=1m`): a streamed body is unavailable to addons and would
otherwise be recorded as an apparent zero-comment call.
"""
import datetime, json, os, re, time
from urllib.parse import parse_qs, urlparse

from mitmproxy import http

SESSION = os.environ.get("DESK_SESSION", "0")
ROOT = os.environ.get("CAPTURE_ROOT", "/captures")
COMMENT_API = re.compile(r"^/api/comment/list(/reply)?/?")
PAGE = re.compile(r"^/@[^/]+/(video|photo)/(\d+)")


def _path(name):
    d = os.path.join(ROOT, datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d"))
    os.makedirs(d, exist_ok=True)
    return os.path.join(d, name)


def _append(name, obj):
    with open(_path(name), "a", encoding="utf-8") as f:
        f.write(json.dumps(obj, ensure_ascii=False) + "\n")


def _is_tiktok(flow):
    host = flow.request.pretty_host.lower()
    return host == "tiktok.com" or host.endswith(".tiktok.com")


def _ids_from_request(flow):
    q = parse_qs(urlparse(flow.request.path).query)
    aweme = (q.get("aweme_id") or q.get("item_id") or [None])[0]
    parent = (q.get("comment_id") or [None])[0]
    return aweme, parent


class Capture:
    def responseheaders(self, flow: http.HTTPFlow):
        # Keep comment-API bodies buffered; let media keep streaming.
        if _is_tiktok(flow) and COMMENT_API.match(urlparse(flow.request.path).path):
            flow.response.stream = False

    def response(self, flow: http.HTTPFlow):
        if not _is_tiktok(flow):
            return
        path = urlparse(flow.request.path).path
        now = int(time.time())
        m = PAGE.match(path)
        if flow.request.method == "GET" and m and "text/html" in (flow.response.headers.get("content-type") or ""):
            _append("opens.ndjson", {"t": now, "session": SESSION, "kind": m.group(1), "video_id": m.group(2), "status": flow.response.status_code})
            return
        if not COMMENT_API.match(path):
            return
        aweme_id, parent_cid = _ids_from_request(flow)
        meta = {"t": now, "session": SESSION, "endpoint": "reply" if "/reply" in path else "list",
                "aweme_id": aweme_id, "parent_cid": parent_cid, "http_status": flow.response.status_code, "path": path}
        empty = dict(n=0, status_code=None, total=None, has_more=None, cursor=None)
        raw = flow.response.raw_content if flow.response.content is not None else None
        if raw is None:
            _append("api-calls.ndjson", dict(meta, body="missing", **empty))
            return
        try:
            data = json.loads(flow.response.get_text(strict=False) or "")
            if not isinstance(data, dict):
                raise ValueError("not an object")
        except Exception as e:
            _append("api-calls.ndjson", dict(meta, body="invalid", error=str(e)[:200], **empty))
            return
        comments = data.get("comments") or []
        _append("api-calls.ndjson", dict(meta, body="ok" if comments else "empty", n=len(comments),
                                         status_code=data.get("status_code"), total=data.get("total"),
                                         has_more=data.get("has_more"), cursor=data.get("cursor")))
        for c in comments:
            user = c.get("user") or {}
            _append(f"session-{SESSION}.ndjson", {
                "t": now, "session": SESSION,
                "cid": c.get("cid"), "post_id": c.get("aweme_id") or aweme_id, "text": c.get("text"),
                "create_time": c.get("create_time"), "digg_count": c.get("digg_count"),
                "reply_id": c.get("reply_id"), "reply_comment_total": c.get("reply_comment_total"),
                "author_unique_id": user.get("unique_id"), "author_nickname": user.get("nickname"), "author_uid": user.get("uid"),
                "comment_language": c.get("comment_language"), "author_pin": c.get("author_pin"), "stick_position": c.get("stick_position"),
                "raw": c,
            })


addons = [Capture()]
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `.venv/bin/python -m unittest tests.test_capture_addon -v`
Expected: `Ran 7 tests … OK`. If `tflow.tflow(resp=True)` attributes differ in 12.2.3 (for example `raw_content`), adjust the helper or the `raw is None` check to `flow.response.content is None`; the behaviour under test is the same.

- [ ] **Step 5: Commit (PERSONAL)**

`git add desk/mitm/addons/capture_comments.py tests/test_capture_addon.py tests/requirements-dev.txt && git commit -m "fix(capture): buffer comment-API bodies, take video id from the request, classify body state" -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"`. Report story **capture**.

---

### Task 8: `desk/make-sessions.sh` renders N isolated session pairs (story sessions)

**Files:**
- Create: `desk/make-sessions.sh`
- Create: `scripts/test-make-sessions.sh`

**Interfaces:**
- Consumes: `desk/firefox/policies.json`, `desk/mitm/addons/`, `desk/desk_stub.py` from the repo checkout (`--repo`); optional `<desk>/templates/firefox-profile.tar` (Task 13 creates it on the box).
- Produces, under `<desk>` (default `~/desk`): `docker-compose.yml` (project `desk`), `firefox/policies-<i>.json`, `nginx/desk-sessions.conf`, `mitm/addons/`, `desk_stub.py`, `state/{mitm-<i>,firefox-<i>,captures/<i>}` owned by uid 1000; with `--up`, starts `mitm-<i>`, waits for `state/mitm-<i>/mitmproxy-ca-cert.pem`, then starts `firefox-<i>`. Session `i` on `127.0.0.1:580<i>`; nginx `/desk/s/<i>/` → that port; `/desk/` → `127.0.0.1:8110` without trailing slash.

- [ ] **Step 1: Write the failing test**

`scripts/test-make-sessions.sh`:

```bash
#!/usr/bin/env bash
# test-make-sessions.sh — render-only tests (no Docker needed; `docker compose config` runs if docker is present).
HERE="$(cd "$(dirname "$0")" && pwd)"; REPO="$(dirname "$HERE")"; . "$HERE/lib.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
GEN="$REPO/desk/make-sessions.sh"; DESK="$TMP/desk"
export PATH="$TMP/bin:$PATH"; mkdir -p "$TMP/bin"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/chown"; printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/sudo"
chmod +x "$TMP/bin/chown" "$TMP/bin/sudo"   # no root in tests

check "render N=1 exits 0" "$GEN" --desk "$DESK" --repo "$REPO" 1
C="$DESK/docker-compose.yml"
check "services mitm-1 and firefox-1" bash -c "grep -q '^  mitm-1:' '$C' && grep -q '^  firefox-1:' '$C'"
check "container names" bash -c "grep -q 'container_name: desk-mitm-1' '$C' && grep -q 'container_name: desk-firefox-1' '$C'"
check "noVNC bound to 127.0.0.1:5801" grep -q '"127.0.0.1:5801:5800"' "$C"
check "pair network with mitm alias" bash -c "grep -q 'net-1:' '$C' && grep -q -- '- mitm$' '$C'"
check "captures mounted per session" grep -q './state/captures/1:/captures' "$C"
check "policy per session" test -f "$DESK/firefox/policies-1.json"
check "policy proxies to mitm:8080" grep -q '"HTTPProxy": "mitm:8080"' "$DESK/firefox/policies-1.json"
check "addons and stub copied" test -f "$DESK/mitm/addons/capture_comments.py" -a -f "$DESK/desk_stub.py"
check "state dirs exist" test -d "$DESK/state/mitm-1" -a -d "$DESK/state/firefox-1" -a -d "$DESK/state/captures/1"
N="$DESK/nginx/desk-sessions.conf"
check "nginx location /desk/s/1/ → 5801" bash -c "grep -A6 'location /desk/s/1/' '$N' | grep -q 'proxy_pass http://127.0.0.1:5801/;'"
check "nginx /desk/ has NO trailing slash" grep -q 'proxy_pass http://127.0.0.1:8110;' "$N"
check "nginx gates every location" test "$(grep -c 'auth_request /validate;' "$N")" = 2
check "nginx passes REMOTE_USER to the stub" grep -q 'proxy_set_header REMOTE_USER \$username;' "$N"

sed -n '/^  mitm-1:/,/^  mitm-2:/p' "$C" | grep -v '^  mitm-2:' > "$TMP/block1-n1.txt"
check "render N=3 exits 0" "$GEN" --desk "$DESK" --repo "$REPO" 3
check "N=3 has firefox-3 on 5803" grep -q '"127.0.0.1:5803:5800"' "$C"
sed -n '/^  mitm-1:/,/^  mitm-2:/p' "$C" | grep -v '^  mitm-2:' > "$TMP/block1-n3.txt"
check "N=3 keeps session 1 block byte-identical" cmp -s "$TMP/block1-n1.txt" "$TMP/block1-n3.txt"
check "N=3 nginx has 3 session locations" test "$(grep -c 'location /desk/s/' "$N")" = 3
if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  check "docker compose config validates" bash -c "cd '$DESK' && docker compose config -q"
else echo "skip: docker compose not available"; fi
check "N=0 is a usage error" bash -c "! '$GEN' --desk '$DESK' --repo '$REPO' 0"
report
```

- [ ] **Step 2: Run it to verify it fails**

Run: `chmod +x scripts/test-make-sessions.sh && scripts/test-make-sessions.sh`
Expected: all FAIL (generator missing).

- [ ] **Step 3: Write `desk/make-sessions.sh`**

```bash
#!/usr/bin/env bash
# make-sessions.sh — render N isolated Firefox+mitmproxy session pairs for the desk.
#
#   make-sessions.sh [--desk DIR] [--repo DIR] [--up] N
#   --desk  deployment dir (default ~/desk); --repo  this repo's checkout (default: script's parent)
#   --up    after rendering: start mitm-<i>, wait for its CA, then start firefox-<i>
#
# Renders <desk>/docker-compose.yml, <desk>/firefox/policies-<i>.json, <desk>/nginx/desk-sessions.conf,
# copies the mitm addon and the desk stub, creates <desk>/state/{mitm-<i>,firefox-<i>,captures/<i>}
# owned by uid 1000, and clones <desk>/templates/firefox-profile.tar into any NEW firefox-<i>.
# Each pair has its own network with the proxy aliased `mitm`, so policies-<i>.json's fixed
# `mitm:8080` reaches the right proxy. Re-running with a larger N leaves existing sessions'
# rendered blocks byte-identical.
set -euo pipefail
usage() { echo "usage: $0 [--desk DIR] [--repo DIR] [--up] N" >&2; exit 2; }
DESK="$HOME/desk"; REPO="$(cd "$(dirname "$0")/.." && pwd)"; UP=0; N=""
while [ $# -gt 0 ]; do
  case "$1" in --desk) DESK="$2"; shift 2;; --repo) REPO="$2"; shift 2;; --up) UP=1; shift;; -*) usage;; *) N="$1"; shift;; esac
done
[[ "${N:-}" =~ ^[1-9][0-9]*$ ]] || usage
MITM_IMAGE="mitmproxy/mitmproxy:12.2.3"; FF_IMAGE="jlesage/firefox:v26.08.3"

mkdir -p "$DESK/firefox" "$DESK/nginx" "$DESK/state/captures" "$DESK/mitm"
rm -rf "$DESK/mitm/addons"; cp -r "$REPO/desk/mitm/addons" "$DESK/mitm/addons"
cp "$REPO/desk/desk_stub.py" "$DESK/desk_stub.py"

{
  echo "# Rendered by make-sessions.sh — do not edit; re-run with a larger N to add sessions."
  echo "name: desk"
  echo "services:"
  for i in $(seq 1 "$N"); do
    cat <<EOF
  mitm-$i:
    image: $MITM_IMAGE
    container_name: desk-mitm-$i
    command: mitmdump --set block_global=false --set stream_large_bodies=1m -s /addons/capture_comments.py
    volumes:
      - ./mitm/addons:/addons:ro
      - ./state/mitm-$i:/home/mitmproxy/.mitmproxy
      - ./state/captures/$i:/captures
    environment:
      - DESK_SESSION=$i
    networks:
      net-$i:
        aliases:
          - mitm
    restart: unless-stopped
  firefox-$i:
    image: $FF_IMAGE
    container_name: desk-firefox-$i
    depends_on: [mitm-$i]
    ports:
      - "127.0.0.1:580$i:5800"
    volumes:
      - ./state/firefox-$i:/config
      - ./state/mitm-$i:/mitm-ca:ro
      - ./firefox/policies-$i.json:/usr/lib/firefox/distribution/policies.json:ro
    environment:
      - DISPLAY_WIDTH=1280
      - DISPLAY_HEIGHT=900
      - WEB_AUDIO=0
      - KEEP_APP_RUNNING=1
      - DARK_MODE=0
      - FF_OPEN_URL=https://www.tiktok.com/
      - USER_ID=1000
      - GROUP_ID=1000
    shm_size: "1g"
    networks: [net-$i]
    restart: unless-stopped
EOF
  done
  echo "networks:"
  for i in $(seq 1 "$N"); do echo "  net-$i: {}"; done
} > "$DESK/docker-compose.yml"

for i in $(seq 1 "$N"); do
  cp "$REPO/desk/firefox/policies.json" "$DESK/firefox/policies-$i.json"
  mkdir -p "$DESK/state/mitm-$i" "$DESK/state/captures/$i"
  if [ ! -d "$DESK/state/firefox-$i" ]; then
    mkdir -p "$DESK/state/firefox-$i"
    [ -f "$DESK/templates/firefox-profile.tar" ] && tar -xf "$DESK/templates/firefox-profile.tar" -C "$DESK/state/firefox-$i"
  fi
  chown -R 1000:1000 "$DESK/state/mitm-$i" "$DESK/state/firefox-$i" "$DESK/state/captures/$i" 2>/dev/null \
    || sudo chown -R 1000:1000 "$DESK/state/mitm-$i" "$DESK/state/firefox-$i" "$DESK/state/captures/$i"
done

{
  echo "# /etc/nginx/app-location-conf.d/desk-sessions.conf — rendered by make-sessions.sh (N=$N)."
  echo "# Every location is CO-gated by SRC-Nginx's /validate. websocket-map.conf must be in conf.d/."
  for i in $(seq 1 "$N"); do
    cat <<EOF
location /desk/s/$i/ {
    error_page 401 = @custom_401;
    auth_request /validate;
    auth_request_set \$username \$upstream_http_username;
    proxy_pass http://127.0.0.1:580$i/;
    proxy_http_version 1.1;
    proxy_set_header Upgrade \$http_upgrade;
    proxy_set_header Connection \$connection_upgrade;
    proxy_set_header Host \$host;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$scheme;
    proxy_buffering off;
    proxy_read_timeout 86400s;
    proxy_send_timeout 86400s;
}
EOF
  done
  cat <<'EOF'
location /desk/ {
    error_page 401 = @custom_401;
    auth_request /validate;
    auth_request_set $username $upstream_http_username;
    # NO trailing slash: the stub must see the full /desk/<i>/... path.
    proxy_pass http://127.0.0.1:8110;
    proxy_set_header REMOTE_USER $username;
    proxy_set_header Host $host;
}
EOF
} > "$DESK/nginx/desk-sessions.conf"

echo "rendered $N session(s) under $DESK"
if [ "$UP" = 1 ]; then
  cd "$DESK"
  for i in $(seq 1 "$N"); do
    docker compose up -d "mitm-$i"
    for _ in $(seq 1 60); do [ -f "state/mitm-$i/mitmproxy-ca-cert.pem" ] && break; sleep 1; done
    [ -f "state/mitm-$i/mitmproxy-ca-cert.pem" ] || { echo "mitm-$i produced no CA within 60 s" >&2; exit 1; }
    docker compose up -d "firefox-$i"
  done
  echo "install nginx: sudo cp $DESK/nginx/desk-sessions.conf /etc/nginx/app-location-conf.d/ && sudo nginx -t && sudo systemctl reload nginx"
fi
```

The test's `auth_request` count of 2 for N=1 is one session location plus `/desk/`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `chmod +x desk/make-sessions.sh && scripts/test-make-sessions.sh`
Expected: all `ok:` (the `docker compose config` line runs if Docker is installed on the laptop, else prints `skip`).

- [ ] **Step 5: Commit (PERSONAL)**

`git add desk/make-sessions.sh scripts/test-make-sessions.sh && git commit -m "feat(desk): render N isolated Firefox+mitm session pairs and the nginx locations" -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"`. Report story **sessions**.

---

### Task 9: desk stub serves one page per session and reports failed opens (story sessions)

**Files:**
- Modify: `desk/desk_stub.py` (whole file)
- Modify: `desk/desk-stub.service` (whole file)
- Create: `tests/test_desk_stub.py`

**Interfaces:**
- Consumes: `<desk>/worklist/session-<i>.csv` (Task 6), header `REMOTE_USER` set by nginx, the `docker` CLI (overridable for tests with `--docker PATH`).
- Produces: `GET /desk/<i>/` two-pane page; `GET /desk/<i>/rows` JSON `[{video_id,bucket,note,opened_at,code,note_text}]`; `POST /desk/<i>/open?id=…` → 204, or 500 when the exec failed; `POST /desk/<i>/problem?id=…&code=…&note=…` → 204; log lines in `<desk>/state/desk-log.ndjson`: `{"t","user","session","action":"open|problem|reload","video_id","code","note","exec_rc"}`; 403 without `REMOTE_USER`; 404 for an unknown session or a `video_id` not in that session's list. SIGHUP is caught and logged; worklists are re-read on every request.

- [ ] **Step 1: Write the failing test**

`tests/test_desk_stub.py`:

```python
import csv, json, os, signal, socket, subprocess, sys, tempfile, time, unittest, urllib.error, urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
STUB = HERE.parent / "desk" / "desk_stub.py"


def free_port():
    s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close(); return p


def start_stub(desk, docker, port):
    p = subprocess.Popen([sys.executable, str(STUB), "--desk", str(desk), "--sessions", "2", "--port", str(port), "--docker", str(docker)])
    for _ in range(50):
        try:
            urllib.request.urlopen(urllib.request.Request(f"http://127.0.0.1:{port}/desk/1/rows", headers={"REMOTE_USER": "probe"}), timeout=0.2); break
        except Exception:
            time.sleep(0.1)
    return p


def request(port, method, path, user="alice"):
    r = urllib.request.Request(f"http://127.0.0.1:{port}{path}", method=method)
    if user: r.add_header("REMOTE_USER", user)
    try:
        with urllib.request.urlopen(r, timeout=5) as resp: return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e: return e.code, e.read().decode()


class DeskStub(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = Path(tempfile.mkdtemp()); cls.desk = cls.tmp / "desk"
        (cls.desk / "worklist").mkdir(parents=True); (cls.desk / "state").mkdir()
        for i, ids in ((1, ["7000000000000000001", "7000000000000000002"]), (2, ["7000000000000000003"])):
            with open(cls.desk / "worklist" / f"session-{i}.csv", "w", newline="") as f:
                w = csv.writer(f); w.writerow(["video_id", "bucket", "note"]); [w.writerow([v, "random", f"note {v}"]) for v in ids]
        cls.docker_log = cls.tmp / "docker.log"
        cls.fake_docker = cls.tmp / "docker"
        cls.fake_docker.write_text('#!/usr/bin/env bash\necho "$*" >> "%s"\nexit 0\n' % cls.docker_log); os.chmod(cls.fake_docker, 0o755)
        cls.fake_fail = cls.tmp / "docker-fail"
        cls.fake_fail.write_text("#!/usr/bin/env bash\nexit 1\n"); os.chmod(cls.fake_fail, 0o755)
        cls.port = free_port(); cls.proc = start_stub(cls.desk, cls.fake_docker, cls.port)

    @classmethod
    def tearDownClass(cls):
        cls.proc.terminate(); cls.proc.wait(5)

    def req(self, method, path, user="alice"):
        return request(self.port, method, path, user)

    def log_lines(self):
        p = self.desk / "state" / "desk-log.ndjson"
        return [json.loads(l) for l in open(p) if l.strip()] if p.exists() else []

    def test_page_renders_session_rows_in_order_with_notes(self):
        st, body = self.req("GET", "/desk/1/")
        self.assertEqual(st, 200)
        self.assertIn("/desk/s/1/", body); self.assertIn("Session 1", body); self.assertIn("alice", body)
        st, rows = self.req("GET", "/desk/1/rows"); rows = json.loads(rows)
        self.assertEqual([r["video_id"] for r in rows], ["7000000000000000001", "7000000000000000002"])
        self.assertEqual(rows[0]["note"], "note 7000000000000000001")

    def test_open_execs_against_that_sessions_firefox_and_logs(self):
        st, _ = self.req("POST", "/desk/2/open?id=7000000000000000003")
        self.assertEqual(st, 204)
        self.assertIn("exec -e DISPLAY=:0 -e DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base -e HOME=/config -e XDG_RUNTIME_DIR=/tmp/run/user/app desk-firefox-2 su-exec 1000:1000 /usr/lib/firefox/firefox --profile /config/profile https://www.tiktok.com/@x/video/7000000000000000003/", self.docker_log.read_text())
        ev = [e for e in self.log_lines() if e["video_id"] == "7000000000000000003"][-1]
        self.assertEqual((ev["action"], ev["session"], ev["user"], ev["exec_rc"]), ("open", "2", "alice", 0))
        rows = json.loads(self.req("GET", "/desk/2/rows")[1])
        self.assertIsNotNone(rows[0]["opened_at"])

    def test_failed_exec_is_500_and_not_opened(self):
        port = free_port(); p = start_stub(self.desk, self.fake_fail, port)
        try:
            st, _ = request(port, "POST", "/desk/1/open?id=7000000000000000001", user="bob")
            self.assertEqual(st, 500)
            rows = json.loads(request(port, "GET", "/desk/1/rows", user="bob")[1])
            self.assertIsNone(next(r for r in rows if r["video_id"] == "7000000000000000001")["opened_at"])
            ev = [e for e in self.log_lines() if e["video_id"] == "7000000000000000001" and e["action"] == "open"][-1]
            self.assertEqual(ev["exec_rc"], 1)
        finally:
            p.terminate(); p.wait(5)

    def test_missing_remote_user_is_403(self):          # review focus 3
        st, _ = self.req("GET", "/desk/1/", user=None); self.assertEqual(st, 403)
        st, _ = self.req("POST", "/desk/1/open?id=7000000000000000001", user=None); self.assertEqual(st, 403)

    def test_foreign_id_is_404_without_exec(self):     # review focus 4
        before = self.docker_log.read_text() if self.docker_log.exists() else ""
        st, _ = self.req("POST", "/desk/1/open?id=7000000000000000003"); self.assertEqual(st, 404)
        self.assertEqual(before, self.docker_log.read_text() if self.docker_log.exists() else "")

    def test_unknown_session_is_404(self):
        st, _ = self.req("GET", "/desk/9/"); self.assertEqual(st, 404)

    def test_problem_logged_and_rendered(self):
        st, _ = self.req("POST", "/desk/1/problem?id=7000000000000000002&code=U&note=gone")
        self.assertEqual(st, 204)
        rows = json.loads(self.req("GET", "/desk/1/rows")[1])
        row = next(r for r in rows if r["video_id"] == "7000000000000000002")
        self.assertEqual((row["code"], row["note_text"]), ("U", "gone"))

    def test_sighup_survives_and_new_rows_appear(self):
        with open(self.desk / "worklist" / "session-2.csv", "a", newline="") as f:
            csv.writer(f).writerow(["7000000000000000004", "random", "late"])
        self.proc.send_signal(signal.SIGHUP); time.sleep(0.3)
        self.assertIsNone(self.proc.poll())
        rows = json.loads(self.req("GET", "/desk/2/rows")[1])
        self.assertIn("7000000000000000004", [r["video_id"] for r in rows])


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 2: Run it to verify it fails**

Run: `python3 -m unittest tests.test_desk_stub -v`
Expected: errors (the current stub takes `--worklist`, not `--desk`, and exits on unknown args), all tests fail.

- [ ] **Step 3: Rewrite `desk/desk_stub.py`**

```python
#!/usr/bin/env python3
"""Desk stub (launch week): one two-pane page per session beside its streamed Firefox.

    desk_stub.py --desk ~/desk --sessions 3 [--port 8110] [--docker docker]

Routes (nginx forwards /desk/ unchanged, with REMOTE_USER set; requests without it are refused):
  GET  /desk/<i>/                 page: sidebar worklist for session i + iframe /desk/s/<i>/
  GET  /desk/<i>/rows             JSON rows with durable state from the log
  POST /desk/<i>/open?id=V        docker exec into desk-firefox-<i>; 204, or 500 if the exec failed
  POST /desk/<i>/problem?id=V&code=C&note=N
Worklists: <desk>/worklist/session-<i>.csv (video_id,bucket,note), re-read on every request.
Log:       <desk>/state/desk-log.ndjson  {"t","user","session","action","video_id","code","note","exec_rc"}
SIGHUP is caught and logged; it is not needed for a reload but must not kill the process.
"""
import argparse, csv, html, json, re, signal, subprocess, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

PROBLEMS = [("U", "Unavailable / removed"), ("P", "Private"), ("L", "Login required to see the video"),
            ("W", "Login wall stopped the comments early"), ("R", "Not available in this region"),
            ("C", "Captcha / blocked / blank page - stop for today"), ("X", "Something else (say what)")]
ROUTE = re.compile(r"^/desk/(\d+)/(rows|open|problem)?$")

PAGE = """<!doctype html><html lang="en"><head><meta charset="utf-8"><title>desk - session {i}</title>
<style>
body{{margin:0;font:14px system-ui;display:flex;flex-direction:row;height:100vh;overflow:hidden;color:#14202b}}
aside{{width:340px;flex:0 0 340px;overflow:auto;border-right:1px solid #ddd;padding:12px;box-sizing:border-box}}
iframe{{flex:1 1 auto;height:100vh;border:0;min-width:0}}
li{{margin:8px 0}} li.opened{{color:#2b6b2b}} li.problem{{color:#9a4b00}} li.failed{{color:#b00020}}
button{{font:inherit;padding:2px 6px}} .n{{color:#7a8791;font-size:12px}} .bar{{display:flex;gap:8px;align-items:center;margin-bottom:8px}}
</style></head><body>
<aside><div class="bar"><b>Session {i}</b> - {user} <a href="#" onclick="load();return false">reload list</a></div>
<p class="n">Click a row (opens in the pane; logged). Open comments, scroll until they stop or 3 minutes pass. Only press Problem... when something went wrong. Logged out of TikTok, always.</p>
<ol id="rows"></ol></aside>
<iframe src="/desk/s/{i}/" allow="clipboard-read; clipboard-write"></iframe>
<script>
const S={i}; const P={problems};
async function load(){{const r=await fetch('/desk/'+S+'/rows');const rows=await r.json();const ol=document.getElementById('rows');ol.innerHTML='';
 for(const x of rows){{const li=document.createElement('li');li.id='r'+x.video_id;
  li.className=x.code?'problem':(x.opened_at?'opened':'');
  li.innerHTML='<a href="#" onclick="open_(\\''+x.video_id+'\\');return false">'+x.video_id+'</a> <span class="n">'+(x.note||'')+'</span> '+
   (x.opened_at?'<span class="n">opened</span> ':'')+(x.code?'<span class="n">problem '+x.code+(x.note_text?' - '+x.note_text:'')+'</span> ':'')+
   '<button onclick="problem(\\''+x.video_id+'\\')">Problem...</button>';ol.appendChild(li);}}}}
async function open_(id){{const r=await fetch('/desk/'+S+'/open?id='+id,{{method:'POST'}});
 if(r.status!==204){{const li=document.getElementById('r'+id);li.className='failed';li.insertAdjacentHTML('beforeend',' <b>could not open; tell the operator</b>');return;}} load();}}
async function problem(id){{const c=prompt('Problem code:\\n'+P.map(p=>p[0]+' = '+p[1]).join('\\n'));if(!c)return;const n=prompt('Optional note')||'';
 await fetch('/desk/'+S+'/problem?id='+id+'&code='+encodeURIComponent(c.trim().toUpperCase())+'&note='+encodeURIComponent(n),{{method:'POST'}});load();}}
load();
</script></body></html>"""


class Desk:
    def __init__(self, desk: Path, sessions: int, docker: str):
        self.desk, self.sessions, self.docker = desk, sessions, docker
        self.log_path = desk / "state" / "desk-log.ndjson"; self.log_path.parent.mkdir(parents=True, exist_ok=True)

    def rows(self, i):
        p = self.desk / "worklist" / f"session-{i}.csv"
        if not p.exists(): return []
        with open(p, newline="", encoding="utf-8") as f: return list(csv.DictReader(f))

    def state(self, i):
        st = {}
        if self.log_path.exists():
            with open(self.log_path, encoding="utf-8") as f:
                for line in f:
                    try: ev = json.loads(line)
                    except ValueError: continue
                    if str(ev.get("session")) != str(i): continue
                    s = st.setdefault(ev.get("video_id"), {"opened_at": None, "code": "", "note_text": ""})
                    if ev.get("action") == "open" and ev.get("exec_rc") == 0 and not s["opened_at"]: s["opened_at"] = ev["t"]
                    if ev.get("action") == "problem": s["code"], s["note_text"] = ev.get("code", ""), ev.get("note", "")
        return st

    def log(self, **ev):
        ev.setdefault("t", int(time.time()))
        with open(self.log_path, "a", encoding="utf-8") as f: f.write(json.dumps(ev, ensure_ascii=False) + "\n")

    def open_video(self, i, vid):
        url = f"https://www.tiktok.com/@x/video/{vid}/"
        cmd = [self.docker, "exec", "-e", "DISPLAY=:0", "-e", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base", "-e", "HOME=/config",
               "-e", "XDG_RUNTIME_DIR=/tmp/run/user/app", f"desk-firefox-{i}", "su-exec", "1000:1000",
               "/usr/lib/firefox/firefox", "--profile", "/config/profile", url]
        try:
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=15)
            return r.returncode, r.stderr[-200:]
        except subprocess.TimeoutExpired:
            return 124, "timeout after 15 s"


class Handler(BaseHTTPRequestHandler):
    desk: Desk = None  # set by main()

    def _send(self, code, body=b"", ctype="text/plain; charset=utf-8"):
        self.send_response(code); self.send_header("Content-Type", ctype); self.send_header("Content-Length", str(len(body))); self.end_headers()
        if body: self.wfile.write(body)

    def _route(self):
        u = urlparse(self.path); m = ROUTE.match(u.path)
        user = self.headers.get("REMOTE_USER", "").strip()
        if not user: self._send(403, b"no REMOTE_USER"); return None
        if not m: self._send(404); return None
        i = int(m.group(1))
        if not 1 <= i <= self.desk.sessions: self._send(404, b"unknown session"); return None
        return user, i, m.group(2), parse_qs(u.query)

    def do_GET(self):
        r = self._route()
        if not r: return
        user, i, action, _ = r
        if action is None:
            body = PAGE.format(i=i, user=html.escape(user), problems=json.dumps(PROBLEMS)).encode()
            self._send(200, body, "text/html; charset=utf-8")
        elif action == "rows":
            st = self.desk.state(i)
            out = [dict(x, **st.get(x["video_id"], {"opened_at": None, "code": "", "note_text": ""})) for x in self.desk.rows(i)]
            self._send(200, json.dumps(out).encode(), "application/json")
        else: self._send(405)

    def do_POST(self):
        r = self._route()
        if not r: return
        user, i, action, q = r
        vid = (q.get("id") or [""])[0]
        if action not in ("open", "problem") or not vid.isdigit(): self._send(404); return
        if vid not in {x["video_id"] for x in self.desk.rows(i)}: self._send(404, b"not in this session's worklist"); return
        if action == "open":
            rc, err = self.desk.open_video(i, vid)
            self.desk.log(user=user, session=str(i), action="open", video_id=vid, code="", note="", exec_rc=rc, exec_err=err)
            self._send(204 if rc == 0 else 500, b"" if rc == 0 else err.encode())
        else:
            code = (q.get("code") or [""])[0][:1].upper(); note = (q.get("note") or [""])[0][:200]
            self.desk.log(user=user, session=str(i), action="problem", video_id=vid, code=code, note=note, exec_rc=None)
            self._send(204)

    def log_message(self, *a): pass


def main():
    ap = argparse.ArgumentParser(); ap.add_argument("--desk", required=True); ap.add_argument("--sessions", type=int, required=True)
    ap.add_argument("--port", type=int, default=8110); ap.add_argument("--docker", default="docker")
    a = ap.parse_args()
    Handler.desk = Desk(Path(a.desk), a.sessions, a.docker)
    signal.signal(signal.SIGHUP, lambda *_: Handler.desk.log(user="-", session="-", action="reload", video_id="", code="", note="", exec_rc=None))
    ThreadingHTTPServer(("127.0.0.1", a.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
```

Replace `desk/desk-stub.service`:

```ini
# /etc/systemd/system/desk-stub.service — install with:
#   sudo cp desk/desk-stub.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable --now desk-stub
# Set DESK_DIR / SESSIONS below (or via `sudo systemctl edit desk-stub`) before enabling.
[Unit]
Description=ddp-comments desk stub (per-session worklist beside the streamed Firefox)
After=docker.service
Requires=docker.service

[Service]
Environment=DESK_DIR=/home/dmccool/desk
Environment=SESSIONS=3
WorkingDirectory=/home/dmccool/desk
ExecStart=/usr/bin/python3 /home/dmccool/desk/desk_stub.py --desk ${DESK_DIR} --sessions ${SESSIONS} --port 8110
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `python3 -m unittest tests.test_desk_stub -v && scripts/test-make-sessions.sh`
Expected: `Ran 8 tests … OK`; make-sessions still passes (it copies the new stub).

- [ ] **Step 5: Commit (PERSONAL)**

`git add desk/desk_stub.py desk/desk-stub.service tests/test_desk_stub.py && git commit -m "feat(desk): per-session two-pane stub with failed-open reporting and durable row state" -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"`. Report story **sessions**.

---

### Task 10: runbook, collector sheet, and the ddp-transcribe FOLLOWUPS entry (stories runbook, feed-followup)

**Files:**
- Create: `docs/day-one.md`, `docs/collector-sheet.md` (ddp-comments)
- Modify: `~/src/ddp-transcribe/docs/FOLLOWUPS.md` (scope-index line) and `~/src/ddp-transcribe/docs/followups/production-run.md` (entry body)

- [ ] **Step 1: Write `docs/day-one.md`**

Values in CAPITALS are filled from the open-questions answers before first use.

```markdown
# Day one — operator runbook (ddp-comments, launch week)

Fill in before use: COLLECTION=`<collection>`  VOLUME=`<volume>`  SESSIONS=`<N>`  CAP=`<rows>`
FQDN=`checkjefeedzee.onlinehate.src.surf-hosted.nl`  GPU=`<gpu-ip>`  DESKIP=`145.38.185.19`
Who watches the GPU run today: `<name>`. The breaker stops losses; it notifies nobody.

## A. GPU workspace — morning cycle (tmux)
- [ ] `~/pull-from-yoda.sh`                                   # new donations land on the volume inbox
- [ ] `ddp-transcribe --state-db ~/ddp-state/state.sqlite --inbox ~/data/VOLUME/inbox ingest`
- [ ] `mkdir -p ~/logs; CUDA_VISIBLE_DEVICES=0 ddp-transcribe --state-db ~/ddp-state/state.sqlite --transcripts ~/ddp-work/transcripts --whisper-model ~/ddp-work/models/ggml-large-v3-turbo-q5_0.bin process --max-videos 5000 --retries 2 --checkpoint-cmd ~/sync-to-storage.sh --checkpoint-every 15m 2>&1 | tee -a ~/logs/gpu0-$(date -u +%Y%m%d).log; echo "process exit ${PIPESTATUS[0]}"`
      exit 0 = done · 3 = queue drained (fine) · 4 = breaker tripped → section E, never a blind restart
      the first lines must show `whisper_backend_init_gpu: using CUDA0` (absent = CPU fallback: abort)
- [ ] `ddp-transcribe --state-db ~/ddp-state/state.sqlite load-metadata`
- [ ] `~/sync-to-storage.sh`                                  # a short batch can end before the first checkpoint fires
- [ ] `~/feed-export.sh --state-db ~/ddp-state/state.sqlite --cap CAP --collection COLLECTION`
      stats line: `candidates 0` = nothing new; `candidates > 0, exported 0` = ledger/cap problem
- [ ] `rsync -a ~/ddp-feed/ ~/data/VOLUME/ddp-feed/`          # ledger + batch dirs onto the volume
- [ ] `YODA_EXTRACT=0 YODA_THREADS=2 ~/push-to-yoda.sh`
- [ ] `gocmd ls COLLECTION/feed | tail -3`                    # today's batch dir is listed, directly under feed/

## B. Desk workspace — load the day's worklists
- [ ] `~/src-checkout/desk/pull-feed.sh --desk ~/desk --sessions SESSIONS --collection COLLECTION`
      expect `outstanding N / carried N / new N / sessions SESSIONS`; "no new batches" means A did not land
- [ ] `wc -l ~/desk/worklist/session-*.csv`                   # disjoint, roughly equal
- [ ] `systemctl is-active desk-stub`; `sudo docker ps --format '{{.Names}}' | sort`   # desk-mitm-i and desk-firefox-i per session
- [ ] Yoda down? On the GPU box: `scp -r ~/ddp-feed/feed/<batch> you@DESKIP:~/desk/feed/` for EVERY pending batch, then `pull-feed.sh --desk ~/desk --sessions SESSIONS --offline`

## C. Collectors
- [ ] Give collector k the URL `https://FQDN/desk/k/` and `docs/collector-sheet.md`
- [ ] First open of the day per session: the pane shows TikTok, the Z icon is present, and `https://FQDN/` inside the pane shows 4CAT after SRAM login
- [ ] End of day: one line per session in `~/desk/days.csv`:
      `date,session,collector,rows_opened,problems,comments_dataset_id,comments_count,posts_dataset_id,posts_count,capture_path,notes`
      A zero-comment or blocked day is a valid row.

## D. Checks that the day worked (each can fail independently)
- [ ] Yoda: `feed/<batch>/selection.csv` exists with rows; stats line explains the count
- [ ] `~/desk/worklist/session-*.csv` disjoint
- [ ] `grep -c '"action": "open"' ~/desk/state/desk-log.ndjson` grew; any `"exec_rc": 1` or `124` explained
- [ ] `ls ~/desk/state/captures/<i>/<date>/`: `api-calls.ndjson` has `"body": "ok"` rows for the opened videos
- [ ] 4CAT: one comments + one posts dataset per session; counts in `days.csv`
- [ ] GPU: `sync-to-storage.sh`, `push-to-yoda.sh`, and the `rsync` of `~/ddp-feed` all exited 0

## E. When something goes wrong
(Copy the table from the spec §7 verbatim here when writing this file.)

## F. One-time: migration from the spike layout (done on <date>)
- [ ] `cd ~/spike && sudo docker compose -p spike down`      # frees the names desk-mitm-1 / desk-firefox-1
- [ ] `mkdir -p ~/data/desk-spike/desk && ln -s ~/data/desk-spike/desk ~/desk`
- [ ] `mkdir -p ~/desk/state ~/desk/templates; mv ~/spike/state/mitm-1 ~/desk/state/; mv ~/spike/state/firefox-1 ~/desk/state/; mv ~/spike/state/captures ~/desk/state/captures-spike`
- [ ] template: section G, then `~/src-checkout/desk/make-sessions.sh --desk ~/desk --repo ~/src-checkout SESSIONS --up`
- [ ] `sudo rm /etc/nginx/app-location-conf.d/desk-spike.conf; sudo cp ~/desk/nginx/desk-sessions.conf /etc/nginx/app-location-conf.d/; sudo nginx -t && sudo systemctl reload nginx`
- [ ] 4CAT onto the volume: `cd ~/4cat && sudo docker compose -f docker-compose_public_ip.yml down`; write `docker-compose.override.yml` bind-mounting the db and data volumes under `~/data/desk-spike/4cat/{db,data}`; copy the named volumes' contents in (`sudo docker run --rm -v <vol>:/from -v ~/data/desk-spike/4cat/<name>:/to alpine cp -a /from/. /to/`); `up -d`; open 4CAT and confirm the spike dataset is still listed
- [ ] stub: edit SESSIONS in `desk-stub.service`, `sudo cp ~/src-checkout/desk/desk-stub.service /etc/systemd/system/`, `sudo systemctl daemon-reload && sudo systemctl enable --now desk-stub && systemctl status desk-stub`
- [ ] rollback = `~/spike` untouched until the first collection day has passed

## G. One-time: session template
- [ ] with a fresh `state/firefox-1`, start session 1; in the pane: Zeeschuimer → both TikTok switches ON, item lists empty; Z pinned; 4CAT URL = `https://FQDN`; log in to 4CAT with the shared collector account; Remote Resizing on; log OUT of TikTok; do NOT log in to SRAM inside
- [ ] `cd ~/desk && sudo docker compose stop firefox-1 && sudo tar -C ~/desk/state/firefox-1 -cf ~/desk/templates/firefox-profile.tar . && sudo docker compose start firefox-1`

## Validation log
(dates and results from Tasks 11–14)
```

- [ ] **Step 2: Write `docs/collector-sheet.md`**

```markdown
# Comment collection — collector sheet (desk edition)

You need: membership of the OnlineHate collaboration, your session URL from the operator
(`https://checkjefeedzee.onlinehate.src.surf-hosted.nl/desk/<n>/`), a laptop browser.
No installs, no exports, no 4CAT password.

1. Open your session URL. Log in with SRAM when asked. Left: your list. Right: a Firefox that runs on the server.
2. In the right pane, open a new tab to `https://checkjefeedzee.onlinehate.src.surf-hosted.nl/` and log in with SRAM there too. You should see 4CAT. Close that tab. (This second login lets the upload at the end work; it lasts 12 hours.)
3. Stay logged OUT of TikTok in the right pane, all day.
4. Click a row on the left. The video opens on the right. Open the comments, scroll until they stop loading or three minutes pass. Do not expand replies. Close the tab. Next row.
5. If the video played and comments loaded: nothing to record. Only when something went wrong, press **Problem…** on that row: U removed · P private · L login needed for the video · W a login wall stopped the comments early · R not in this region · C captcha / blocked / blank page (**stop for today and tell the operator**) · X other (say what).
6. "could not open; tell the operator" in red = the server could not open the video. Tell the operator, carry on with the next row.
7. End of session: in the right pane click the Z icon → on the **TikTok (comments)** row press **to 4CAT**, wait for "done"; then the **TikTok** row → **to 4CAT**. Tell the operator both counts. Do NOT clear the items until the operator says so next morning.
8. If "to 4CAT" says log in: redo step 2.
```

- [ ] **Step 3: Add the ddp-transcribe FOLLOWUPS entry (PERSONAL)**

Append to `~/src/ddp-transcribe/docs/followups/production-run.md`:

```markdown
### `feed` subcommand to replace the GPU-box export script (ADR-0041)

**Context:** the tiktok-harassment study (2026-10) runs `feed-export.sh` from
`researchcloud-ddp-comments` on the GPU workspace: a read-only SQL export of newly
`succeeded` canonical videos not yet in an id ledger, newest-published first, capped,
written as `feed/<batch>/selection.csv` and synced to Yoda. ADR-0041 says new operator
surfaces extend the binary rather than grow wrapper scripts; this is a declared stopgap.
**Disposition:** add `feed` (or `status --feed`) in a tagged release with the same
columns (`video_id,bucket,note,succeeded_at,comment_count,donors`), honouring the
existing ledger file and batch-dir contract so the desk side needs no change.
**Trigger:** Phase 1 of the comment desk, or the first pipeline release after it.
**Hypothesis (unverified):** the script's `video_id NOT IN (temp ledger)` query stays
fast at campaign scale; if not, the subcommand keeps the ledger in a table.
```

Add one scope-index line in `~/src/ddp-transcribe/docs/FOLLOWUPS.md` under the production-run group pointing at that heading.

- [ ] **Step 4: Verify and commit**

ddp-comments: `scripts/test-all.sh` still passes; `git add docs/day-one.md docs/collector-sheet.md && git commit -m "docs(ops): day-one runbook, collector sheet, spike migration and template steps" -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"`.
ddp-transcribe: `cd ~/src/ddp-transcribe && adg lean check docs/FOLLOWUPS.md docs/followups/production-run.md && git add docs/FOLLOWUPS.md docs/followups/production-run.md && git commit -m "docs(followups): feed subcommand to replace the GPU-box export script" -m "Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"`. The pre-commit hook runs `adg lean index`; if it fails, fix the inconsistency it names.
Report stories **runbook** and **feed-followup**.

---

### Task 11: Danielle — portal work and GPU workspace launch (story catalog-pins; validation-log commit only)

**Prerequisite:** Tasks 1–3 committed to `main` of the deploy repo (the component's Tag is `main`).

- [ ] Component **edit** (never recreate, snag S7) of `ddp-transcribe`: declare `ytdlp_version` (Fixed, Overwritable ✓, default blank).
- [ ] Item: overwrite `pipeline_git_ref` = `v0.5.1`; share the DDP Transcribe item with the **OnlineHate** CO.
- [ ] On OnlineHate: create the volume (e.g. `ddp-transcribe-harassment`, sized for the study, growability checked); generate a fresh Yoda DAP (no `$`, S3); create Co-Secret `yoda_data_access_password`.
- [ ] Confirm `A10 - 2 GPU` schedules today (S5); else `A10 - 1 GPU`.
- [ ] Launch: `storage_backend=yoda`, `yoda_collection=<collection>`, `yoda_user`, `pipeline_user`, `download_workers` as on `uutiktok`, `storage_path` placeholder, volume attached at the second-to-last step.
- [ ] Verification on the box: deployment log green with zero manual fixes (only "censored"? S17: check the secret and interactive values); `ddp-transcribe -V` prints `0.5.1`; `ldd $(which ddp-transcribe) | grep libcudart`; `~/.local/bin/yt-dlp --version` is a `2026.09.x` or later nightly; `~/.local/bin/yt-dlp --list-impersonate-targets` shows rows without `(unavailable)`; `~/.local/bin/deno --version` starts with `deno 2.`; the four generated scripts present.
- [ ] `scp ~/src/researchcloud-ddp-comments/gpu/feed-export.sh <user>@<gpu-ip>:~/feed-export.sh && ssh <user>@<gpu-ip> chmod +x ~/feed-export.sh`.
- [ ] Record cold-provision wallclock and the installed yt-dlp version in `docs/catalog-item.md` §6 (deploy repo, story catalog-pins, your commit).

---

### Task 12: Danielle — validation batch, pin decision, first feed (story runbook)

- [ ] First launch only: `~/pull-from-yoda.sh` (inbox must hold at least one donation) → `~/restore-from-storage.sh` → `ddp-transcribe --state-db ~/ddp-state/state.sqlite --inbox ~/data/<volume>/inbox init` if no snapshot existed.
- [ ] `ingest`; the summary line shows the donor-file count and row counts.
- [ ] Validation batch: `process --max-videos 50 --retries 2`, teed. Pass = CUDA banner; zero `HttpError`/`YtDlpOther`; metadata envelopes captured; failures explained by terminal classes. `sqlite3 ~/ddp-state/state.sqlite "select params_json from batch_runs order by id desc limit 1"` shows `"ytdlp_impersonation_available": true`.
- [ ] If it fails on the challenge rung: `pipx install --force 'yt-dlp[default]==2026.8.25.233329.dev0' && pipx inject yt-dlp curl_cffi`, re-run; then set `ytdlp_version` to the working value in the portal **and** in `roles/ytdlp/defaults/main.yaml` (deploy repo, your commit).
- [ ] `load-metadata`; `~/sync-to-storage.sh`; `~/feed-export.sh --state-db ~/ddp-state/state.sqlite --cap <CAP> --collection <collection>`: stats line with `exported > 0`; `gocmd ls <collection>/feed` lists the batch dir directly (not `feed/feed`).
- [ ] `rsync -a ~/ddp-feed/ ~/data/<volume>/ddp-feed/`; `YODA_EXTRACT=0 YODA_THREADS=2 ~/push-to-yoda.sh`.
- [ ] Fill the header of `docs/day-one.md` (who watches, CAP, SESSIONS, COLLECTION, VOLUME, GPU) — ddp-comments, story runbook.

---

### Task 13: Danielle — prepare `checkjefeedzee` (story runbook)

- [ ] Confirm the 10 GB `desk-spike` volume grows in place; otherwise create a larger one now and attach it.
- [ ] `git clone` the ddp-comments repo to `~/src-checkout` on the box.
- [ ] gocmd as the operator: v0.12.2 static binary to `/usr/local/bin/gocmd`; `~/.irods/irods_environment.json` as `roles/yoda/templates/irods_environment.json.j2` renders it (host `fsw.data.uu.nl`, port 1247, zone `nluu10p`, your user, `irods_authentication_file` = `~/.irods/.irodsA`); fresh DAP; `gocmd init --ttl 720 -c ~/.irods`; `gocmd ls <collection>/feed`.
- [ ] Run `docs/day-one.md` §F (migration) and §G (template) in order. Decide `docker` group vs `sudo` (open question 3) and note it in the runbook header.
- [ ] Verification: `sudo docker ps` shows `desk-mitm-<i>` and `desk-firefox-<i>` for every session; `curl -s -o /dev/null -w '%{http_code}' -H 'REMOTE_USER: you' http://127.0.0.1:8110/desk/1/` prints `200`, without the header `403`; from a laptop, `https://<fqdn>/desk/1/` shows the two panes after SRAM login; a non-member gets 403; `sudo docker network inspect desk_net-2 | grep -c mitm` is 1.
- [ ] `~/src-checkout/desk/pull-feed.sh --desk ~/desk --sessions N --collection <collection>` → `outstanding … / sessions N`; the sidebar lists rows.

---

### Task 14: Danielle — live acceptance before collectors start (story runbook; validation-log commit)

- [ ] **Second donation**: one more file in Yoda `inbox/`; run §A; the new video ids appear in the next `feed/<batch>/`.
- [ ] **Two round trips**: after the second `pull-feed.sh`, `~/desk/feed/` holds both batches at the same level (no `feed/feed`); `loaded.txt` lists both.
- [ ] **Two simultaneous sessions**: `/desk/1/` and `/desk/2/` in two browsers open different rows; `desk-log.ndjson` shows `session` 1 and 2 with `exec_rc 0`; `state/captures/1/<date>/api-calls.ndjson` and `…/2/…` each have `"body": "ok"` rows for their own videos only.
- [ ] **Failed open**: `sudo docker stop desk-firefox-2`, click a row in session 2 → red "could not open"; `sudo docker start desk-firefox-2`.
- [ ] **Inner SRAM expiry**: in a session, clear cookies for the FQDN inside the pane, press "to 4CAT" → the documented login message; log in inside the pane → upload succeeds and the dataset appears in 4CAT.
- [ ] **Restore**: `cd ~/desk && sudo docker compose down`, move `state/firefox-1` aside, `make-sessions.sh --desk ~/desk --repo ~/src-checkout N --up`: session 1 returns from the template; move the original back and restart. 4CAT: `down`, `up -d` → datasets still listed (volume-backed).
- [ ] Record results and dates under "Validation log" in `docs/day-one.md` (ddp-comments, story runbook).
