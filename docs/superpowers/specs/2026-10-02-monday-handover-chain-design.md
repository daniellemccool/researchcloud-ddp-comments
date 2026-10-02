# ddp-comments — Monday handover chain design

**Written:** 2026-10-02, brainstorming session with Danielle, from the exploration
report of 2026-10-01 (in the session transcript; its findings are restated where
load-bearing). **Target:** collectors working on Monday 2026-10-05.

## 1. Purpose and success criteria

The tiktok-harassment study measures exposure to harassment on TikTok. Donated watch
histories name the videos; the transcripts say what was said in them; the comments
under those videos are what the study needs next. Videos that attract harassment are
the ones most likely to be moderated away within days, so the time from a donation
landing to a collector opening the video's comment panel is the quantity to minimise.

Success on Monday:

1. A donation placed in the Yoda inbox is ingested and transcribed on a GPU workspace
   in the OnlineHate CO within one operator cycle.
2. Every newly transcribed video appears, newest-published first, in a worklist on
   the desk workspace `checkjefeedzee` after one pull.
3. Two or three collectors, each in their own streamed Firefox session behind the
   SRAM gate, open worklist rows, let Zeeschuimer capture the comments, and press
   "to 4CAT" at session end. The mitmproxy sidecar keeps an automatic copy.
4. An operator can tell from three checks that the day worked, and has a named first
   move for each known failure.

Not Monday (week one or Phase 1): keyword scoring, per-collector session spawning,
the comment store and reconciliation, automated cron cycles, the org-repo transfer.

## 2. Decisions taken in this session

| # | Decision | Alternatives rejected |
|---|---|---|
| D1 | Monday bar is the hand-fed worklist plus a written path to the full Phase 1 desk, with failure modes and troubleshooting | automated feed on Monday; full Phase 1 on Monday |
| D2 | Transcription runs on a **new SRC GPU workspace in OnlineHate** | reuse the crime-policing VM `uutiktok`; desktop at home for week one |
| D3 | Selection v0 is **every newly transcribed video, newest-published first, capped**, no scoring | keyword tiers if lists arrive; keyword tiers required |
| D4 | Feed hop is **Yoda**, with scp via the laptop as the fallback if time runs out | direct SSH between workspaces |
| D5 | **Two or three hand-duplicated sessions**, generated from a template | one shared session; Phase 1 session manager |
| D6 | Record of truth for comments is **both**: Zeeschuimer → 4CAT per session, mitm NDJSON kept as the safety copy | 4CAT only; mitm only |
| D7 | Feed is a **read-only script on the GPU box**, no pipeline release before Monday | a `feed` subcommand released as v0.6.0; selecting on the desk box from the state snapshot |
| D8 | Export bookkeeping is an **id ledger**, not a `succeeded_at` watermark | watermark (fails on commit ordering, equal timestamps, and the cap) |
| D9 | The `ytdlp` role installs a **pre-release** with an optional exact pin (`ytdlp_version`), default latest; the validation batch decides whether to pin | leave the role on stable (installs 2026.08.19, known to fail the 2026-08-25 challenge rung); hard-pin up front |
| D10 | The desk keeps the **two-pane layout**, so a server process stays; the **Python stub stays for Monday**; the Phase 1 language is decided in §8 | static worklist inside the contained Firefox (loses the two panes and server-side open attribution) |
| D11 | Tool name **`ddp-comments`**, repo **`researchcloud-ddp-comments`**, personal GitHub first at `~/src/researchcloud-ddp-comments/`, transferred to `d3i-infra` at Phase 1 | `researchcloud-comment-desk`; `researchcloud-zeeschuimer`; prototyping on in the study folder |

## 3. Topology and ownership

Two SRC workspaces in the OnlineHate CO (SRAM collaboration 7889, shortname
`onlinehate`), one Yoda collection between them, three code homes.

**GPU workspace** (new). DDP Transcribe catalog item, flavour `A10 - 2 GPU`
(`A10 - 1 GPU` if unavailable that day), `storage_backend=yoda`, one fresh volume.
Runs the operator cycle `pull-from-yoda → ingest → process → load-metadata →
feed-export → push-to-yoda`. Holds donor watch histories, `state.sqlite`, and
transcripts. SSH only; no web surface.

**Desk workspace** (`checkjefeedzee`, existing, 4 core / 16 GB, Ubuntu 24.04,
SRC-Nginx with the SRAM `auth_request` gate). Runs N Firefox+mitm session pairs, the
desk stub, 4CAT (`surf` branch) and nginx locations. Receives only `selection.csv`
files. Never holds `state.sqlite`, transcripts, or watch histories.

**Yoda collection** (OnlineHate's; path supplied by Danielle, called `<collection>`
below). Layout: `inbox/` (Danielle's manual donation drop, Next file names
unchanged), `transcripts-tars/` and `state-snapshot.sqlite` (the existing push
layout), and new `feed/<UTC timestamp>/selection.csv`. One data-access password
(DAP) per workspace, each generated immediately before use and never retried after a
failure.

**Code homes and regimes.**

| Repo | Regime | What changes |
|---|---|---|
| `d3i-infra/researchcloud-ddp-transcribe` | ORG (Danielle commits) | `ytdlp` role, `pipeline_git_ref` default, verify asserts, docs |
| `daniellemccool/ddp-transcribe` | PERSONAL | one FOLLOWUPS entry (future `feed` subcommand per ADR-0041); no code |
| `researchcloud-ddp-comments` (this repo) | PERSONAL | spike artifacts, `make-sessions.sh`, `feed-export.sh`, `pull-feed.sh`, `desk_stub.py`, runbook, this spec |
| `~/projects/tiktok-harassment/comments/` | study folder, not a repo | gains a pointer to this repo in `PICKUP.md`; documents and data stay |

`feed-export.sh` runs on the GPU box but lives here, copied over by hand: under
ADR-0041 a wrapper script may not become part of the pipeline repo's operator surface.

Collectors are OnlineHate members; the gate is CO membership. One shared 4CAT account
per session profile performs uploads; collectors need no 4CAT login, SSH, or TOTP.

## 4. Launching the GPU workspace

### 4.1 Deploy-repo changes (`researchcloud-ddp-transcribe`)

1. **`roles/ytdlp/tasks/main.yaml`.** Replace the stable install with:
   `pipx install --force --pip-args=--pre 'yt-dlp[default]'` when `ytdlp_version`
   is empty, or `pipx install --force 'yt-dlp[default]=={{ ytdlp_version }}'`
   when set; then `pipx inject yt-dlp curl_cffi`; then Deno 2.x installed to
   `~/.local/bin` (official install script or release tarball, checksum pinned).
   Idempotent: skip when `yt-dlp --version` already matches the requested version
   (or is any `.dev` version when unpinned) and `deno --version` succeeds. The header
   comment that forbids `pipx inject` is rewritten to record the 2026-08-25 reversal
   and cite `incident-2026-08-25-tiktok-challenge-requires-impersonation.md`.
2. **`ytdlp_version`** declared as a role default (empty) and a component parameter
   (Fixed, Overwritable, default empty) so the pin is a portal value, not a code
   edit.
3. **`pipeline_git_ref`** default `v0.5.1`; README and `docs/catalog-item.md`
   tables updated; the item's overwrite bumped in the portal.
4. **`roles/verify/tasks/main.yaml`** gains two asserts: `yt-dlp
   --list-impersonate-targets` lists at least one target; `deno --version` exits 0.
   A provision with the stale posture fails loudly instead of launching a silent
   fetch outage.

Evidence for D9: PyPI on 2026-10-02 shows stable `2026.8.19` (before the 2026-08-25
change) and pre-releases through `2026.9.27`.

### 4.2 Portal and CO work (Danielle)

Share the DDP Transcribe item with OnlineHate. On OnlineHate: create the volume
(generous, growable), create the `yoda_data_access_password` Co-Secret from a fresh
DAP, then launch with `storage_backend=yoda`, `yoda_collection=<collection>`,
`yoda_user`, `pipeline_user`, `download_workers` as on `uutiktok`, `storage_path`
left at its placeholder. The `catalog-item.md` §4 launch checklist applies, plus one
check: the first batch's `batch_runs.params_json` must show
`ytdlp_impersonation_available: true` and the expected `ytdlp_version`.

### 4.3 Validation and the pin decision

`process --max-videos 50 --retries 2`, teed to a dated log. Expect roughly 70 %
success, zero `HttpError` / `YtDlpOther`, the ADR-0013 CUDA banner. If the batch
fails on the challenge rung: `pipx install --force 'yt-dlp[default]==2026.8.25.233329'`
+ `pipx inject yt-dlp curl_cffi` by hand (the stack proven 2026-08-26), re-run the
batch, then set `ytdlp_version` in the role and the portal to the version that
worked. No relaunch is needed for this; the divergence is closed by the role update.

### 4.4 Operating cycle (week one, by hand)

```
~/pull-from-yoda.sh                                    # new inbox files
ddp-transcribe --state-db ~/ddp-state/state.sqlite ingest
CUDA_VISIBLE_DEVICES=0 ... process --max-videos N --retries 2 \
    --checkpoint-cmd ~/sync-to-storage.sh --checkpoint-every 15m  2>&1 | tee -a ~/logs/gpu0-$(date -u +%Y%m%d).log
ddp-transcribe --state-db ~/ddp-state/state.sqlite load-metadata
~/feed-export.sh --cap <collectors × hours × rows-per-hour>
YODA_EXTRACT=0 YODA_THREADS=2 ~/push-to-yoda.sh
```

ADR-0048 makes `process` claim newest-published first, so recent watches reach the
feed before any backlog with no change. A cron wrapper for the whole cycle is a
week-one followup after two clean manual runs.

## 5. The feed

### 5.1 `feed-export.sh` (GPU box)

Arguments: `--state-db`, `--out <dir>` (default `~/ddp-feed`), `--ledger <file>`
(default `~/ddp-feed/exported-ids.txt`), `--cap N`, `--collection <iRODS path>`,
`FEED_PUSH=0` to skip the push.

Steps:

1. Open the DB read-only: `sqlite3 "file:${db}?mode=ro"` with `PRAGMA query_only=1`.
   The script never holds a write lock and cannot cut across the store's contracts.
2. Load the ledger into a temp table. Query canonical rows with
   `status='succeeded'` whose `video_id` is not in the ledger, ordered by `video_id`
   descending (publish order, the ADR-0048 axis), limited to `--cap`. Join the count
   of distinct respondents in `watch_history` with `in_window=1` (`donors`) and
   `comment_count` from the metadata columns (NULL until `load-metadata` has run).
3. Write `feed/<UTC %Y%m%dT%H%M%SZ>/selection.csv` with columns
   `video_id,bucket,note,succeeded_at,comment_count,donors`. `bucket` is `random`
   for every row in week one. `note` is a human string such as
   `comments 123 · donors 2` so `make_worklist.py` shows it unchanged.
4. Append the exported ids to the ledger only after the CSV is fully written and
   renamed into place.
5. Print one stats line: `candidates N / exported N / capped N / ledger N`.
6. Unless `FEED_PUSH=0`: `gocmd sync <out>/feed i:<collection>/feed` using the
   `~/.irods` environment the `yoda` role created. Idempotent and checksummed.

Why a ledger (D8): a `succeeded_at` watermark fails when a concurrent writer commits
a row after an export read it, when timestamps tie, when the clock steps, and
outright when a cap cuts rows whose timestamps lie below the last written row. The
ledger is immune to all of these; a capped row simply waits for the next run.

The ledger lives beside `state.sqlite` and is included in the volume sync by adding
`~/ddp-feed` to the operator's backup list (runbook line). Losing it re-exports
everything once; the desk dedupes, so the cost is noise, not loss.

### 5.2 `pull-feed.sh` (desk box)

Prerequisites installed by hand: `gocmd`, `~/.irods/irods_environment.json` written
as the deploy repo's `yoda` role does, `gocmd init -c` with a DAP generated for this
box.

Steps: `gocmd sync i:<collection>/feed ~/desk/feed`; for each `selection.csv` not in
`~/desk/loaded.txt`, append its rows to `~/desk/worklist/current.csv`, dedupe on
`video_id` keeping the first occurrence, record the file; signal the desk stub
(`systemctl kill -s HUP desk-stub`) so it reloads without dropping sessions.

### 5.3 Contract

The desk reads only `video_id,bucket,note`; other columns are informational. Data
flows one way: GPU box → Yoda → desk. No comments, logs, or captures flow back into
`state.sqlite`.

Laptop fallback (D4): `scp` the newest `selection.csv` off the GPU box and onto
`~/desk/feed/<ts>/`, then run `pull-feed.sh --offline` (skips the `gocmd` step).

## 6. The desk on `checkjefeedzee`

### 6.1 Sessions: `make-sessions.sh N`

Renders `docker-compose.yml` with services `mitm-<i>` / `firefox-<i>` for
`i = 1..N` (container names `desk-mitm-<i>` / `desk-firefox-<i>`, noVNC on
`127.0.0.1:580<i>`, state dirs `state/mitm-<i>` and `state/firefox-<i>`,
`DESK_SESSION=<i>`), and `nginx/desk-sessions.conf` with N gated locations
`/desk/s/<i>/` (websocket upgrade, `proxy_read_timeout 86400s`). Creates missing
state dirs owned by uid 1000, clones the session template into any new
`state/firefox-<i>`, and prints the `docker compose up -d mitm-* && … firefox-*`
ordering (mitm first so the CA exists). Re-running with a larger N adds pairs and
leaves existing ones untouched.

### 6.2 Session template

One Firefox `/config` prepared by hand on the box: Zeeschuimer's two TikTok switches
on, the Z pinned (`default_area: navbar`), 4CAT URL set to the workspace FQDN, the
shared 4CAT account logged in, Remote Resizing on,
`browser.link.open_newwindow.override.external=1`. Saved as
`templates/firefox-profile.tar` (on the box; not committed, it holds a session
cookie) and cloned per new session. Never a blank profile.

### 6.3 Desk stub (Python, Monday only)

`desk_stub.py` keeps its shape and gains: `/desk/<i>/` serves the two-pane page for
session `i` (sidebar + iframe `/desk/s/<i>/`); a row click execs against
`desk-firefox-<i>`; `--worklist` is re-read on SIGHUP; the log line carries `user`,
`session`, `action`, `video_id`, `code`. Installed as the systemd unit
`desk-stub.service` (runs as root for the Docker socket, binds 127.0.0.1:8110).
nginx location `/desk/` → 8110 with `REMOTE_USER`, unchanged.

Each collector gets one URL for the day: `https://<fqdn>/desk/<i>/`.

### 6.4 4CAT

Unchanged from the spike: `~/4cat`, branch `surf`,
`docker-compose_public_ip.yml`, at the site root behind the gate
(`fourcat.conf`, `client_max_body_size 512m`). One shared collector account created
by Danielle and stored in the template profile. At session end the collector presses
"to 4CAT" on the *TikTok (comments)* row and the *TikTok* row. Attribution is the
uploading account; session identity comes from the mitm capture directory and the
desk log.

### 6.5 Operator fixes on the box before Monday

Copy the newer `firefox/policies.json`; install `gocmd` and init it; install
`desk-stub.service`; decide `sudo docker` vs adding the operator to the `docker`
group; confirm the spike containers are stopped before `make-sessions.sh` renames
them.

## 7. Day-one runbook and failure modes

`docs/day-one.md` in this repo: the GPU cycle (§4.4), the desk pull (§5.2), handing a
collector their session URL, end-of-day uploads, and the three checks: a new
`feed/<ts>/` on Yoda with rows, `state/desk-log.ndjson` growing, one dataset per
session in 4CAT.

| Symptom | Likely cause | First move |
|---|---|---|
| Zero successes in an hour, `YtDlpOther` growth, `process` exit 4 | TikTok WAF rung change | Probe matrix and the fallback ladder in `src-vm.md`; never a blind restart |
| `feed-export.sh` exports 0 | nothing new succeeded, or ledger problem | read the stats line: `candidates 0` is normal; `candidates > 0, exported 0` means ledger/cap |
| `gocmd ls` fails on either box | DAP burned or token expired | fresh DAP, `gocmd init -c`; never retry the old DAP |
| Collector gets 403 or an OAuth loop | not an OnlineHate member, or 12 h token expired | membership first; then re-login |
| Firefox pane blank or tiny | remote display sized to a tiny pane | container restart or Remote Resizing |
| TikTok captcha / login wall / blank page from the SRC IP | WAF treating the address as a bot | stop that session for the day, record code C, do not loop retries from the same IP |
| Zeeschuimer counts stay 0 | switch off, or page loaded before switch | template profile prevents it; a reset profile reintroduces it |
| `desk-mitm-<i>` crash-loops | state dir not uid 1000 | `chown -R 1000:1000` and restart |
| Row click does nothing | stub not running or `docker exec` failed | `systemctl status desk-stub`; `exec_rc` in the log |
| Validation batch shows `ytdlp_impersonation_available: false` | stale fetch posture | §4.3 hand pin, then land the pin in the role |

## 8. Phase 1 path (not Monday scope)

Each step says what it replaces, what it keeps, and which risk it retires.

1. **Session manager** replaces `make-sessions.sh` and the per-session URLs: spawn a
   Firefox+mitm pair per `REMOTE_USER` from the template, reap after idle N minutes,
   one nginx location with a lookup map. Keeps the template profile and the capture
   layout. Retires: hand-rendered compose, collectors sharing a URL by mistake.
2. **Store and loader** replace the NDJSON folders as the record: captures →
   `comments.sqlite` (comments, api_calls, opens, problems; dedupe on comment id;
   loaded-vs-platform-total per video). Keeps the NDJSON as raw. Retires:
   reconciliation by hand.
3. **4CAT push** server-side via `/api/import-dataset/` with an API key, per session
   per day. Keeps Zeeschuimer's button as the visible fallback. Retires: a forgotten
   upload losing a day.
4. **Scoring in the feed**: keyword tiers from a PI-owned TOML, `bucket` becomes
   `priority|random` at 75/25. Retires: collector hours spent on irrelevant videos.
5. **`feed` subcommand** in ddp-transcribe, released per ADR-0043, replacing
   `feed-export.sh` (ADR-0041 says operator surfaces belong in the binary).
6. **Repo transfer** to `d3i-infra/researchcloud-ddp-comments`; ADRs in that repo
   for capture-by-proxy as primary, one contained browser per collector, comments
   never entering `state.sqlite`, 4CAT as sink not store.

**Language for the Phase 1 desk app** (D10 deferred): the work is service-shaped
(container lifecycle, per-user state, websocket proxying). Recommendation: **Go**,
one static binary cross-compiled from the laptop, standard-library HTTP and exec, no
runtime on the workspace. **PHP** stays the alternative if consistency with
`ddp-inspector` is weighted higher; it suits the pages and suits the session manager
poorly. Python is the third option by continuity with the stub. Decide at Phase 1
planning.

## 9. Testing

Hermetic script tests, runnable on the laptop, in the deploy repo's `scripts/test-*.sh`
style. No SRC access required.

- **Deploy repo.** Render-and-assert for the `ytdlp` role: empty `ytdlp_version` →
  `--pre` and no pin; set → exact pin. Verify-role asserts run against fake `yt-dlp`
  and `deno` on `PATH` (one listing a target, one not). Existing `yamllint` and
  `--syntax-check` tier.
- **`feed-export.sh`.** Fixture `state.sqlite` built by `ddp-transcribe init` +
  `ingest` on the pipeline's bundled DDP fixture; test-only SQL marks rows
  `succeeded`. Cases: first run exports all; second exports none; cap leaves the
  remainder and the next run picks it up; a ledgered id is never re-exported; the DB
  is opened read-only (a write attempt through the script's connection fails; no WAL
  growth); `FEED_PUSH=0` never calls `gocmd` (a fake `gocmd` that fails the test if
  invoked).
- **`pull-feed.sh`.** Overlapping fixture feeds; asserts deduped `current.csv`,
  `loaded.txt` records each file once, a fake `systemctl` receives the HUP,
  `--offline` skips `gocmd`.
- **`make-sessions.sh`.** Render N=1 and N=3: service names, ports, state dirs,
  nginx locations; `docker compose config` where Docker is present, skipped
  otherwise; re-render N=3 after N=2 leaves sessions 1–2 byte-identical.
- **`desk_stub.py`.** HTTP test with a fake `REMOTE_USER` header and a fake `docker`
  on `PATH`: a click on `/desk/2/` execs against `desk-firefox-2`; the log line
  carries user and session; SIGHUP reloads a changed CSV.
- **Live acceptance** (runbook, not tests): the validation batch on the new GPU
  workspace; one feed round trip GPU → Yoda → desk; one collector round trip to
  4CAT with the mitm copy present.

## 10. Risks and open questions

Risks carried into the plan: A10 flavour availability (deploy snag S5); Co-Secret
censoring the provisioning log (S17); DAP burned by a failed first attempt (S2);
`checkjefeedzee` hand-installed state drifting from this repo (mitigated by
`make-sessions.sh` owning the compose and nginx files); TikTok WAF changes hitting
the single SRC egress for all collectors at once.

Open for Danielle before or during execution:

1. The OnlineHate Yoda collection path and whether `inbox/` exists there yet.
2. Number of sessions N for Monday (2 or 3) and the cap formula inputs (hours per
   collector, rows per hour).
3. Whether the operator on `checkjefeedzee` joins the `docker` group or keeps `sudo`.
4. Phase 1 language (§8), not needed before Monday.
