# ddp-comments — Monday handover chain design

**Written:** 2026-10-02, brainstorming session with Danielle, from the exploration
report of 2026-10-01. **Revised** the same day after a codex-advisor review (fresh
session, oriented on this file; its full reply is in the advisor transcript). **Target:**
collectors working on Monday 2026-10-05.

## 1. Purpose and success criteria

The tiktok-harassment study measures exposure to harassment on TikTok. Donated watch
histories name the videos; the transcripts say what was said in them; the comments
under those videos are what the study needs next. Videos that attract harassment are
the ones most likely to be moderated away within days, so the time from a donation
landing to a collector opening the video's comment panel is the quantity to minimise.

Success on Monday:

1. A donation placed in the Yoda inbox is ingested and transcribed on a GPU workspace
   in the OnlineHate CO within one operator cycle.
2. After one pull, the desk shows a capped batch of newly transcribed videos,
   newest-published first, split across the sessions; the remainder waits for the
   next cycle and is never lost.
3. Two or three collectors, each in their own streamed Firefox session behind the
   SRAM gate, open worklist rows, let Zeeschuimer capture the comments, and press
   "to 4CAT" at session end. The mitmproxy sidecar keeps an automatic copy.
4. An operator can tell from the end-of-day checks that the day worked, and has a
   named first move for each known failure.

Not Monday (week one or Phase 1): keyword scoring, per-collector session spawning,
the comment store and reconciliation, automated cron cycles, the org-repo transfer.

**Scope limit to record now:** only videos the pipeline transcribed enter the feed.
Videos that fail fetch or transcription (removed, private, region-locked, photo posts
without audio) never reach a collector even where a browser could still show their
comments. The resulting corpus is "comments on transcribed watched videos", not
"comments on watched videos". Comments collected later are also not proof they existed
when the donor watched; the temporal join is a later analysis step on the GPU side.

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
| D9 | The `ytdlp` role installs a **pre-release** with an optional exact pin (`ytdlp_version`); unpinned means "latest at first provision, retained on re-runs"; the validation batch decides whether to pin | leave the role on stable (installs 2026.08.19, known to fail the 2026-08-25 challenge rung); hard-pin up front |
| D10 | The desk keeps the **two-pane layout**, so a server process stays; the **Python stub stays for Monday**; the Phase 1 language is decided in §8 | static worklist inside the contained Firefox (loses the two panes and server-side open attribution) |
| D11 | Tool name **`ddp-comments`**, repo **`researchcloud-ddp-comments`**, personal GitHub first at `~/src/researchcloud-ddp-comments/`, transferred to `d3i-infra` at Phase 1 | `researchcloud-comment-desk`; `researchcloud-zeeschuimer`; prototyping on in the study folder |
| D12 | Each session gets a **disjoint worklist** (round-robin split of the batch); unopened rows carry forward to the next day's list, ahead of new rows | one shared list per session (collectors start on identical rows) |
| D13 | The template profile holds the **4CAT login only**; each collector logs into **SRAM inside the contained Firefox** at shift start | cloning the operator's SRAM session (expires mid-shift; attributes uploads to the operator) |

## 3. Topology and ownership

Two SRC workspaces in the OnlineHate CO (SRAM collaboration 7889, shortname
`onlinehate`), one Yoda collection between them, three code homes.

**GPU workspace** (new). DDP Transcribe catalog item, flavour `A10 - 2 GPU`
(`A10 - 1 GPU` if unavailable that day), `storage_backend=yoda`, one fresh volume
(tiered mode). Runs the operator cycle in §4.4. Holds donor watch histories,
`state.sqlite`, and transcripts. SSH only; no web surface.

**Desk workspace** (`checkjefeedzee`, existing, 4 core / 16 GB, Ubuntu 24.04,
SRC-Nginx with the SRAM `auth_request` gate, the 10 GB volume `desk-spike`
attached but unused). The desk state moves onto that volume: `~/desk` becomes a
symlink to `~/data/desk-spike/desk`, holding sessions' `/config`, captures, desk log,
worklists, feed mirror and ledger, so a workspace rebuild loses nothing. 4CAT's
Postgres data directory moves there too, or 4CAT is treated as rebuildable from the
NDJSON and the desk log (decision in §10). Receives only `selection.csv` files; never
holds `state.sqlite`, transcripts, or watch histories.

**Yoda collection** (OnlineHate's; path supplied by Danielle, `<collection>` below).
Layout: `inbox/` (Danielle's manual donation drop, Next file names unchanged),
`transcripts-tars/` and `state-snapshot.sqlite` (the existing push layout), and new
`feed/<batch>/selection.csv`. One data-access password (DAP) per workspace, each
generated immediately before use; see §5.2 for the retry rule.

**Code homes and regimes.**

| Repo | Regime | What changes |
|---|---|---|
| `d3i-infra/researchcloud-ddp-transcribe` | ORG (Danielle commits) | `ytdlp` role, `ytdlp_version` parameter, `pipeline_git_ref` default, verify asserts, docs |
| `daniellemccool/ddp-transcribe` | PERSONAL | one FOLLOWUPS entry (future `feed` subcommand per ADR-0041); no code |
| `researchcloud-ddp-comments` (this repo) | PERSONAL | spike artifacts, `make-sessions.sh`, `feed-export.sh`, `pull-feed.sh`, `desk_stub.py`, mitm addon fix, runbook, this spec |
| `~/projects/tiktok-harassment/comments/` | study folder, not a repo | gains a pointer to this repo in `PICKUP.md`; documents and data stay |

`feed-export.sh` runs on the GPU box but lives here, copied over by hand. ADR-0041
favours operator surfaces in the pipeline binary; this script is a declared exception
with its replacement (§8.5) explicit.

Collectors are OnlineHate members; the gate is CO membership. One shared 4CAT
account, stored in the template profile, performs uploads; collectors need no 4CAT
password, SSH, or TOTP, but they do log into SRAM twice: once in their laptop browser
to reach the desk, once inside the contained Firefox so Zeeschuimer's upload passes
the gate on the 4CAT FQDN.

## 4. Launching the GPU workspace

### 4.1 Deploy-repo changes (`researchcloud-ddp-transcribe`)

1. **`roles/ytdlp/tasks/main.yaml`.** When `ytdlp_version` is empty and no pipx
   `yt-dlp` package is installed: `pipx install --pip-args=--pre 'yt-dlp[default]'`.
   When set: `pipx install --force 'yt-dlp[default]=={{ ytdlp_version }}'` unless the
   installed distribution version already equals it. Then `pipx inject yt-dlp
   curl_cffi` (skip if `pipx runpip yt-dlp show curl_cffi` succeeds). Then Deno 2.x
   to `~/.local/bin` from the release tarball with a pinned checksum (skip if `deno
   --version` reports major 2). All probes run as `pipeline_user` with explicit
   paths (`{{ pipeline_home }}/.local/bin/...`), as the existing verify role does.
   Idempotence compares **distribution** versions from `pipx list --json`, never the
   CLI banner: the CLI prints `2026.08.29.232711` where the distribution is
   `2026.8.29.232711.dev0`. The header comment that forbids `pipx inject` is
   rewritten to record the 2026-08-25 reversal and cite
   `incident-2026-08-25-tiktok-challenge-requires-impersonation.md`.
2. **`ytdlp_version`** declared as a role default (empty) and a component parameter
   (Fixed, Overwritable, default empty) so the pin is a portal value.
3. **`pipeline_git_ref`** default `v0.5.1`; README and `docs/catalog-item.md`
   tables updated; the item's overwrite bumped in the portal.
4. **`roles/verify/tasks/main.yaml`**: the impersonation assert requires at least one
   target row that does not contain `(unavailable)` and rejects header-only output
   (today it only greps for `chrome`, which a listed-but-unavailable target passes);
   add `deno --version` major 2; keep yt-dlp, curl_cffi and Deno as three
   independent asserts so a matching yt-dlp version cannot mask a missing dependency.

Evidence for D9: PyPI on 2026-10-02 shows stable `2026.8.19` (before the 2026-08-25
change) and pre-releases through `2026.9.27.232945.dev0`.

### 4.2 Portal and CO work (Danielle)

Share the DDP Transcribe item with OnlineHate. On OnlineHate: create the volume
(generous, growable), create the `yoda_data_access_password` Co-Secret from a fresh
DAP, then launch with `storage_backend=yoda`, `yoda_collection=<collection>`,
`yoda_user`, `pipeline_user`, `download_workers` as on `uutiktok`, `storage_path`
left at its placeholder. The `catalog-item.md` §4 launch checklist applies, plus one
check: the first batch's `batch_runs.params_json` must show
`ytdlp_impersonation_available: true` and the expected `ytdlp_version`.

### 4.3 Validation and the pin decision

`process --max-videos 50 --retries 2`, teed to a dated log. Acceptance is: the
ADR-0013 CUDA banner present; zero `HttpError` / `YtDlpOther`; metadata envelopes
captured; the failure breakdown inspected and explained by terminal classes. No
fixed success percentage: the historical 70 % was one cohort's rate, and this
cohort's liveness is unknown. If the batch fails on the challenge rung:

```sh
pipx install --force 'yt-dlp[default]==2026.8.25.233329.dev0'   # distribution version, .dev0 included
pipx inject yt-dlp curl_cffi
```

re-run the batch, then set `ytdlp_version` in the role and the portal to the
distribution version that worked. No relaunch is needed; the role update closes the
divergence.

### 4.4 Operating cycle (week one, by hand)

First launch only: `~/pull-from-yoda.sh` seeds the volume, `~/restore-from-storage.sh`
hydrates the boot disk (it may overwrite a live `state.sqlite` with the snapshot, so it
is never part of the daily cycle), `ddp-transcribe init` if the snapshot was absent.

Recurring cycle, every command CWD-independent, in `tmux`, `mkdir -p ~/logs` once:

```sh
~/pull-from-yoda.sh                                   # new inbox files land on the volume
ddp-transcribe --state-db ~/ddp-state/state.sqlite --inbox ~/data/<volume>/inbox ingest
CUDA_VISIBLE_DEVICES=0 ddp-transcribe --state-db ~/ddp-state/state.sqlite \
    --transcripts ~/ddp-work/transcripts --whisper-model ~/ddp-work/models/ggml-large-v3-turbo-q5_0.bin \
    process --max-videos N --retries 2 --checkpoint-cmd ~/sync-to-storage.sh --checkpoint-every 15m \
    2>&1 | tee -a ~/logs/gpu0-$(date -u +%Y%m%d).log ; echo "process exit ${PIPESTATUS[0]}"
ddp-transcribe --state-db ~/ddp-state/state.sqlite load-metadata
~/sync-to-storage.sh                                  # a short batch may end before the first checkpoint fires
~/feed-export.sh --cap <N>                            # §5.1; pushes feed/ itself
YODA_EXTRACT=0 YODA_THREADS=2 ~/push-to-yoda.sh
```

`process` exit 3 means the queue drained (fine); exit 4 means the breaker tripped
(§7). The bare binary in tiered mode reads the volume inbox in place, as the
generated runner does; the explicit `--inbox` is what makes that true outside the
runner, whose default is `./inbox`.

ADR-0048 orders claims `attempt_count ASC, video_id DESC`: among first attempts the
newest-published video goes first. New donations' recent videos therefore reach the
feed ahead of any older backlog with no change. A cron wrapper for the whole cycle is a
week-one followup after two clean manual runs; until then the runbook names who
watches the run, since a breaker trip stops losses but notifies nobody.

## 5. The feed

### 5.1 `feed-export.sh` (GPU box)

Arguments: `--state-db`, `--out <dir>` (default `~/ddp-feed`), `--ledger <file>`
(default `~/ddp-feed/exported-ids.txt`), `--cap N`, `--collection <iRODS path>`,
`FEED_PUSH=0` to skip the push. Serialised with `flock` on the out dir.

Steps:

1. Open the pipeline DB read-only (`file:${db}?mode=ro`), create the temp ledger
   table and `.import` the ledger into it, **then** `PRAGMA query_only=1`. (With
   `query_only` first the temp-table write fails with "attempt to write a readonly
   database"; reproduced locally.)
2. Query canonical rows with `status='succeeded'` not in the ledger, ordered by
   `video_id` DESC (the ADR-0048 publish axis; fixed-width 19-digit ids make the
   string order the numeric order), limited to `--cap`. Join the count of distinct
   respondents in `watch_history` with `in_window=1` (`donors`) and `comment_count`
   from the metadata columns (NULL until `load-metadata` has run).
3. Create a unique batch dir `feed/<UTC %Y%m%dT%H%M%SZ>-<seq>/` (refuse to reuse an
   existing name; `-<seq>` disambiguates two exports in one second) and write
   `selection.csv` with columns `video_id,bucket,note,succeeded_at,comment_count,donors`
   via tmp-file and rename. `bucket` is `random` for every row in week one (a
   compatibility label for a deterministic newest-first selection). `note` is a
   human string such as `comments 123 · donors 2`.
4. Append the exported ids to the ledger only after the rename.
5. Print one stats line: `candidates N / exported N / capped N / ledger N`.
6. Unless `FEED_PUSH=0`: `gocmd sync <out>/feed i:<collection>` — the destination is
   the **parent**: `gocmd sync SRC DEST` lands SRC as `DEST/basename(SRC)` when DEST
   exists (the rule `yoda-sync.sh` documents), so targeting `<collection>/feed` would
   produce `feed/feed`. The whole `feed/` tree is synced every run, so a batch whose
   push failed is retried next time even when there are zero new candidates.

Why a ledger (D8): a `succeeded_at` watermark fails when a concurrent writer commits
a row after an export read it, when timestamps tie, when the clock steps, and
outright when a cap cuts rows whose timestamps lie below the last written row. The
ledger is immune to all of these; a capped row simply waits for the next run.
Advancing the ledger before the remote push is acceptable because the retained batch
dir is the retry source.

Durability: `~/ddp-feed` (ledger plus batch dirs) is not covered by
`sync-to-storage.sh`, whose paths are fixed. The runbook adds one line after the
sync: `rsync -a ~/ddp-feed/ ~/data/<volume>/ddp-feed/`. Losing the ledger re-exports
everything once; the desk dedupes, so the cost is noise.

### 5.2 `pull-feed.sh` (desk box)

Prerequisites installed by hand as the operator account: `gocmd`,
`~/.irods/irods_environment.json` written as the deploy repo's `yoda` role does, then
`gocmd init --ttl 720 -c ~/.irods` with a DAP generated for this box. Renewal: remove
the cached token file under `~/.irods` **before** `gocmd init` with the new DAP,
otherwise the stale token is reused. Retry rule: inspect a failing `gocmd ls` before
assuming a burned DAP (the invalidation mechanism is documented as unconfirmed); a
DAP that failed authentication once is not retried.

Steps, serialised with `flock`: `gocmd sync i:<collection>/feed ~/desk` (parent
rule again; lands as `~/desk/feed/`); for each `feed/<batch>/selection.csv` not in
`~/desk/loaded.txt`: validate the whole CSV (header, 19-digit ids), append rows to the
outstanding pool, dedupe on `video_id` keeping the first occurrence, record the
batch. Then build the day's lists: outstanding rows ordered carried-forward first
(rows from earlier days never opened), then new rows by `video_id` DESC; split
round-robin into `~/desk/worklist/session-<i>.csv` for `i = 1..N`; write each via
tmp-file and rename; `sudo systemctl kill -s HUP desk-stub`. "Opened" comes from the
desk log (§6.3), so the carry-forward set is computable without a store.

`--offline`: skip `gocmd`; expects every outstanding batch dir copied into
`~/desk/feed/` by `scp` (all pending batches, not only the newest).

### 5.3 Contract

The desk reads only `video_id,bucket,note`; other columns are informational. Data
flows one way: GPU box → Yoda → desk. No comments, logs, or captures flow back into
`state.sqlite`. The batch-dir layout is identical for the Yoda path and the scp
fallback. Acceptance checks two successive round trips, because the `gocmd` landing
rule differs between a first run (destination absent) and later runs.

## 6. The desk on `checkjefeedzee`

### 6.1 Sessions: `make-sessions.sh N`

Renders, under `~/desk/` (Compose project `desk`):

- `docker-compose.yml` with services `mitm-<i>` / `firefox-<i>` for `i = 1..N`
  (container names `desk-mitm-<i>` / `desk-firefox-<i>`, noVNC on
  `127.0.0.1:580<i>`, state dirs `state/mitm-<i>`, `state/firefox-<i>`,
  `state/captures/<i>`, `DESK_SESSION=<i>`), each pair on its **own Compose
  network** with the proxy aliased `mitm` inside it, so the policy's `mitm:8080`
  resolves to the right proxy and the CA mount pairs with it. (A shared network with
  one `mitm` alias on several proxies, or the spike's single `mitm` service name
  with renamed services, routes sessions through the wrong proxy.)
- `firefox/policies.json` per session (identical content today; the file exists per
  session so a per-session difference later is one edit).
- `nginx/desk-sessions.conf` with N gated locations `/desk/s/<i>/` (websocket
  upgrade, `proxy_read_timeout 86400s`) and the `/desk/` location with
  `proxy_pass http://127.0.0.1:8110;` (**no trailing slash**, so the stub sees the
  full `/desk/<i>/...` path; the spike's `…:8110/` stripped the prefix).

It creates missing state dirs owned by uid 1000 (`captures` included: the addon
writes there), clones the session template into any new `state/firefox-<i>`, starts
`mitm-<i>`, **waits for `state/mitm-<i>/mitmproxy-ca-cert.pem` to exist**, then
starts `firefox-<i>`. Re-running with a larger N leaves existing sessions untouched
(byte-identical rendered blocks).

**Migration from the spike layout** (one-time, in the runbook): `docker compose -p
spike down` in `~/spike` (container names `desk-mitm-1` / `desk-firefox-1` are reused
by the new project and must be gone); move `~/spike/state/{mitm-1,firefox-1,captures}`
under `~/desk/state/`; remove `desk-spike.conf`, install `desk-sessions.conf`
(`websocket-map.conf` and `fourcat.conf` stay), `nginx -t`, reload. Rollback: the
`~/spike` tree is kept until the first collection day has passed.

### 6.2 Session template

One Firefox `/config` prepared by hand on the box, then **stopped** and frozen as
`~/desk/templates/firefox-profile.tar` (on the volume; not committed, it holds a 4CAT
session cookie): Zeeschuimer's two TikTok switches on, Zeeschuimer item lists
**empty**, the Z pinned (`default_area: navbar`), 4CAT URL set to the workspace FQDN,
the shared 4CAT account logged in, Remote Resizing on,
`browser.link.open_newwindow.override.external=1`, TikTok logged out, **no SRAM
session** (D13). Cloned per new session. Never a blank profile.

### 6.3 Desk stub (Python, Monday only)

`desk_stub.py` keeps its shape and gains:

- `/desk/<i>/` serves the two-pane page for session `i` from
  `worklist/session-<i>.csv` (rows in file order, `note` shown; no shuffle);
  iframe `/desk/s/<i>/`.
- A row click execs against `desk-firefox-<i>` (unchanged `su-exec` + D-Bus + profile
  invocation; `docker exec -u` is a documented failure on this image). A non-zero
  exit or a timeout returns HTTP 500 and the sidebar shows "could not open; tell the
  operator" instead of marking the row done; the timeout is caught and logged.
- Rows show their durable state from the log at render time (opened at, problem
  code), so a page refresh restores progress; "opened" means dispatched, not
  collected.
- SIGHUP re-reads the worklists; the sidebar has a "reload list" link that reloads
  only the sidebar frame, since a server-side reload does not repaint an open page.
- Log line: `t, user, session, action, video_id, code, exec_rc`.

Installed as `desk-stub.service` with paths rendered to `~/desk` (the spike unit
points at `/home/dmccool/spike/selection.csv`), `WorkingDirectory=~/desk`, root for
the Docker socket, bind 127.0.0.1:8110, `daemon-reload`, `enable --now`, and a
restart check in the runbook. Each collector gets one URL for the day:
`https://<fqdn>/desk/<i>/`.

### 6.4 4CAT and the collector protocol

4CAT unchanged from the spike: `~/4cat`, branch `surf`,
`docker-compose_public_ip.yml`, at the site root behind the gate (`fourcat.conf`,
`client_max_body_size 512m`). One shared collector account created by Danielle and
stored in the template.

Collector protocol (one page, `docs/collector-sheet.md`, replacing the laptop-era
sheet's install and export sections): open the desk URL and log in with SRAM; in the
streamed Firefox open `https://<fqdn>/` once and log in with SRAM there too (12-hour
token; if an upload says "log in to 4CAT", this is the login that expired, not the
laptop's); stay logged **out** of TikTok; click a row, open comments, scroll until
they stop loading or three minutes pass, do not expand replies unless told otherwise,
close the tab, next; a login wall that stops comments early is code W, a captcha or
blank page is code C and the session stops for the day; at session end press "to
4CAT" on the *TikTok (comments)* row and then the *TikTok* row, wait for both
confirmations, and tell the operator the two dataset counts; do not clear
Zeeschuimer items until the operator confirms the next morning.

End-of-day record (operator, one line per session in `~/desk/days.csv`): date,
session, collector, rows opened, problems, comments dataset id and count, posts
dataset id and count, capture path, notes. A zero-comment or blocked day is a valid
row, not a missing one.

### 6.5 mitm capture addon fixes

The spike addon has two blind spots that matter because the capture is the safety
copy (D6): with `stream_large_bodies=1m` any comment response above 1 MB is streamed
and its body is unavailable to the addon, which then records `{}` as if the call
returned zero comments; and `aweme_id` is taken from the first comment, so an empty
or missing body loses the video identity. Fixes: in `responseheaders`, set
`flow.response.stream = False` for comment-API paths so they are buffered while media
still streams; take `aweme_id` (and the parent comment id for reply calls) from the
**request** query; record HTTP status, `status_code`, `total`, `has_more`, `cursor`,
and a `body` field of `ok | empty | missing | invalid`. Unit-tested with mitmproxy's
test flows (§9).

### 6.6 Operator fixes on the box before Monday

Attach and lay out the `desk-spike` volume (§3); copy the newer `policies.json`;
install `gocmd` and init it (§5.2); run the migration in §6.1; freeze the template
(§6.2); install `desk-stub.service` (§6.3); decide `sudo docker` vs the `docker`
group for the operator account; write an initial valid `session-<i>.csv` per session
so the stub starts clean.

## 7. Day-one runbook and failure modes

`docs/day-one.md` in this repo, executable as written (real paths, collection value,
batch sizes, who watches): the GPU cycle (§4.4), the desk pull (§5.2), handing a
collector their session URL, end-of-day uploads and the `days.csv` line.

End-of-day checks, each of which can fail independently: a new `feed/<batch>/` on
Yoda with rows and a stats line that explains the count; one `session-<i>.csv` per
session with disjoint rows; `desk-log.ndjson` with `exec_rc 0` opens for each
session; `state/captures/<i>/<date>/` with comment rows (`n > 0`) for the opened
videos; one comments and one posts dataset per session in 4CAT with counts recorded
in `days.csv`; the GPU box's `sync-to-storage.sh` and `push-to-yoda.sh` both exited
0 and the ledger was rsynced.

| Symptom | Likely cause | First move |
|---|---|---|
| Zero successes in an hour, `YtDlpOther` growth, `process` exit 4 | TikTok WAF rung change | Probe matrix and the fallback ladder in `src-vm.md`; never a blind restart; the day's partial results are already synced by the checkpoint hook |
| `feed-export.sh` exports 0 | nothing new succeeded, or ledger/cap | read the stats line: `candidates 0` is normal; `candidates > 0, exported 0` means ledger or cap |
| Validation batch shows `ytdlp_impersonation_available: false` | stale fetch posture | §4.3 hand pin with the `.dev0` distribution version, then land the pin in the role |
| `gocmd ls` fails on either box | token expired, DAP burned, or a transient PAM outage | inspect the error; renew per §5.2 (remove token, fresh DAP, `init`); never retry a DAP that failed auth |
| Feed landed as `feed/feed/…` | sync targeted the child instead of the parent | fix the destination (§5.1/5.2); move the misplaced batch dirs up one level |
| Collector gets 403 or an OAuth loop on the desk URL | not an OnlineHate member, or the laptop's 12 h token expired | membership first; then re-login |
| "To 4CAT" says log in, or hangs | the **inner** Firefox's SRAM session expired, or 4CAT session lost | log into `https://<fqdn>/` inside the streamed Firefox; then the 4CAT login |
| Firefox pane blank or tiny | remote display sized to a tiny pane | container restart or Remote Resizing |
| Row click shows "could not open" | stub cannot exec, or the container is down | `systemctl status desk-stub`; `exec_rc` in the log; `docker ps` |
| TikTok captcha / login wall / blank page from the SRC IP | WAF treating the address as a bot | stop that session for the day, record code C, do not loop retries from the same IP |
| Zeeschuimer counts stay 0 | switch off, page loaded before the switch, or session behind the wrong proxy | template prevents the first two; check `docker network inspect` for the pair |
| `desk-mitm-<i>` crash-loops | state dir not uid 1000 | `chown -R 1000:1000` and restart |
| Capture has `body: missing` rows | a comment response was streamed, addon fix not deployed | deploy §6.5; the 4CAT upload still has the comments |

## 8. Phase 1 path (not Monday scope)

Each step says what it replaces, what it keeps, and which risk it retires; the
"record now" lines are risks the advisor review surfaced that must shape the design
when it is planned.

1. **Session manager** replaces `make-sessions.sh` and per-session URLs: spawn a
   Firefox+mitm pair per `REMOTE_USER` from the template, reap after idle, one nginx
   location with a lookup map. Keeps the template and capture layout. Retires
   hand-rendered compose and collectors sharing a URL by mistake. *Record now:* CO
   membership is not session ownership; both the control requests and the noVNC
   websocket must be authorised against the session's owner; define reconnects,
   second tabs, revocation, operator access; idle reaping must not discard a session
   with unfinished captures or uploads.
2. **Store and loader** replace the NDJSON folders as the record: captures →
   `comments.sqlite`. Keeps the NDJSON as raw. Retires reconciliation by hand.
   *Record now:* one mutable row per comment id erases history; keep comment identity
   separate from observations (capture time, session, request, selection, raw
   provenance) and the reply→parent relation; fix the capture-format contract (§6.5
   fields) before writing the loader; "loaded vs platform total" is an observation
   with a timestamp and a denominator, not completeness; the loader must resume
   growing NDJSON files with partial trailing lines.
3. **4CAT push** server-side via `/api/import-dataset/`, per session per day. Keeps
   Zeeschuimer's button as the visible fallback. Retires a forgotten upload losing a
   day. *Record now:* the API-key contract on the deployed `surf` version is
   unverified, an API key does not pass the SRAM gate at the public FQDN (use the
   internal `127.0.0.1:8080` route), and import job ids must be tracked so a retry
   does not create duplicate datasets.
4. **Scoring in the feed**: keyword tiers from a PI-owned TOML, `bucket` becomes
   `priority|random` at 75/25. Retires collector hours on irrelevant videos. *Record
   now:* define eligibility, overlap between the two buckets, sampling without
   replacement, seed, scoring-rule version, cap interaction; persist the selection
   reason, not only the rendered note.
5. **`feed` subcommand** in ddp-transcribe, released per ADR-0043, replacing
   `feed-export.sh` (ADR-0041). *Record now:* it must import or honour the existing
   ledger and batch-dir contract.
6. **Repo transfer** to `d3i-infra/researchcloud-ddp-comments`; ADRs in that repo for
   capture-by-proxy as the automatic copy, one contained browser per collector,
   comments never entering `state.sqlite`, 4CAT as sink not store, and the §1 scope
   limit (transcribed videos only).

**Language for the Phase 1 desk app** (D10 deferred). The work is service-shaped
(container lifecycle, per-user state, websocket proxying). Recommendation: Go, one
static binary cross-compiled from the laptop. PHP stays the alternative if
consistency with `ddp-inspector` weighs more; Python by continuity with the stub.
This is a recommendation to compare at Phase 1 planning, with SQLite driver and
supervision implications checked, not an adopted decision.

## 9. Testing

Hermetic script tests, runnable on the laptop, in the deploy repo's `scripts/test-*.sh`
style. No SRC access required.

- **Deploy repo.** Render-and-assert for the `ytdlp` role: empty `ytdlp_version` →
  `--pre` and no pin; set → exact pin; idempotence reads `pipx list --json` (fixture
  JSON with `.dev0` versions). Verify-role asserts against fake `yt-dlp` and `deno`
  on `PATH`: a listing with a usable target passes; a listing whose only target says
  `(unavailable)` fails; header-only output fails; `deno` 1.x fails. Existing
  `yamllint` and `--syntax-check` tier.
- **`feed-export.sh`.** Fixture `state.sqlite` built by `ddp-transcribe init` +
  `ingest` on the pipeline's bundled DDP fixture; test-only SQL marks rows
  `succeeded`. Cases: first run exports all; second exports none; cap leaves the
  remainder and the next run picks it up; a ledgered id is never re-exported; the
  real SQL runs against real SQLite with the temp-ledger-then-`query_only` order; a
  write attempt through the script's connection fails; two exports in one second get
  distinct batch dirs; `FEED_PUSH=0` never calls `gocmd` (a fake `gocmd` fails the
  test if invoked); with a fake `gocmd` the destination argument is the parent.
- **`pull-feed.sh`.** Overlapping fixture batches; asserts dedupe, disjoint
  round-robin split for N=2 and N=3, carried-forward rows first (from a fixture desk
  log), `loaded.txt` records each batch once, invalid CSV rejected without touching
  `session-<i>.csv`, a fake `systemctl` receives the HUP, `--offline` skips `gocmd`.
- **`make-sessions.sh`.** Render N=1 and N=3: service names, ports, per-pair
  networks with the `mitm` alias, state dirs, nginx locations, `proxy_pass` without
  trailing slash; `docker compose config` where Docker is present; re-render N=3 after
  N=2 leaves sessions 1–2 byte-identical.
- **`desk_stub.py`.** HTTP tests with a fake `REMOTE_USER` and a fake `docker` on
  `PATH`: `/desk/2/` renders `session-2.csv` in order with notes; a click execs
  against `desk-firefox-2`; a fake `docker` exiting 1 yields HTTP 500 and no "opened"
  state; a hanging fake `docker` yields a logged timeout; SIGHUP reloads a changed
  CSV; render shows prior opens from a fixture log.
- **mitm addon.** Unit tests with mitmproxy's test flows: a buffered comment response
  records `n` and `aweme_id` from the request; an empty `comments` array records
  `body: empty` with `aweme_id` set; a streamed/missing body records `body:
  missing`; `responseheaders` disables streaming for comment paths only.
- **Live acceptance** (runbook, not tests): the validation batch (§4.3); a second
  donation after setup reaching the feed; two feed round trips (first-run and
  existing-destination landing); two simultaneous sessions opening different rows
  with captures in their own `captures/<i>/`; an upload after the inner SRAM session
  is cleared (must fail with the documented message, then succeed after login); one
  restore exercise of `~/desk` from the volume.

## 10. Risks and open questions

Risks carried into the plan: A10 flavour availability (deploy snag S5); Co-Secret
censoring the provisioning log (S17); DAP burned by a failed first attempt (S2); the
spike-to-`~/desk` migration on a live box; TikTok WAF changes hitting the single SRC
egress for all collectors at once; the inner-Firefox SRAM login as a new step
collectors can forget.

Open for Danielle before or during execution:

1. The OnlineHate Yoda collection path and whether `inbox/` exists there yet.
2. Number of sessions N for Monday (2 or 3) and the cap: rows per collector-hour and
   hours per shift.
3. Whether the operator on `checkjefeedzee` joins the `docker` group or keeps `sudo`.
4. 4CAT durability: move its Postgres data dir onto the volume, or accept 4CAT as
   rebuildable from the NDJSON and `days.csv`.
5. Phase 1 language (§8), not needed before Monday.
