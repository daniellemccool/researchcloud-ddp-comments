# ddp-comments as a SURF Research Cloud catalog item — design

Date: 2026-10-28. Status: approved by Danielle 2026-10-28 (v3), after a builder check, a
Mistral Large 4 review and a Codex review (§11). Next step: the implementation plan
(superpowers:writing-plans). Supersedes the desk sections of the 2026-10-02 spec.

Terms used below, defined once:
- **D14** (from the 2026-10-02 spec): 4CAT's Postgres and data belong on the SRC volume,
  not the boot disk; memcached needs a restart policy.
- **Capture fixes** (spec §6.5): the mitm addon loses response bodies over 1 MB because
  mitmproxy streams them, and takes the post id from the response instead of the request.
- **S7 / S17** (transcribe snag list): edit an SRC component, never recreate it; a
  Co-Secret on the command line censors the whole component log.

## 1. Context

`researchcloud-ddp-comments` runs on one study's workspace (`checkjefeedzee`, CO
OnlineHate): a per-user streamed Firefox with Zeeschuimer behind SRAM, a mitmproxy safety
copy, a shared work queue, and a hand-run 4CAT. It works, but every study-specific value
is hard-coded, Docker and 4CAT were installed by hand, users are typed into a file, and
the Firefox session template was hand-prepared.

The goal is a **requestable SRC catalog item** owned by the D3I CO, with the repo released
under `d3i-infra`, so other data-donation studies can launch a comment-collection desk
without us hand-holding.

## 2. Decisions Danielle took (2026-10-28)

| # | Decision | Consequence |
|---|---|---|
| D1 | Audience: **any platform Zeeschuimer supports**. | TikTok specifics become a per-platform definition; the work item generalises from "video id" to "item of a kind". |
| D2 | Shape: **SRC catalog item** = SRC components + our Ansible component, like `researchcloud-ddp-transcribe`. | Playbook, roles, `docs/catalog-item.md`. |
| D3 | **4CAT is bundled**, gated by us, never by 4CAT's `surf_init.sh`. | The playbook installs 4CAT; accounts are created per user (D12). |
| D4 | Users come from a **SRAM group**, not a typed list. | Access keys on the SRC role; sessions are provisioned on first visit. |
| D5 | Template profile: **no hand step per workspace for logged-out platforms**; login-required platforms keep one admin login step (§6). | Mechanism revised after inspecting the profile (my call). |
| D6 | Second platform: **Instagram or X**. | Instagram (§5.5, my call). |
| D7 | Repo stays personal during the build; moves to `d3i-infra` at first release. | Agents commit with story anchors until the move. |
| D8 | **Public remote under Danielle's personal GitHub now**; transfer at release, history kept. | History checked: no student usernames; a colleague's first name in two commits of UI text stays. |
| D9 | Test identities: `dmccool` (admin, also in the students group), `dmccool2` (students group only). No non-group CO member yet. | Member and admin paths verified live; the 403 path verified with stubbed headers unless a non-group member is added. |
| D10 | Instagram: **login optional**. | `login: "optional"`; logged-out reach measured during the build; acceptance criteria in §5.5. |
| D11 | `checkjefeedzee` is a **sandbox** with no participant data: the build target. | Only SRC-managed files are never edited. |
| D12 | **One 4CAT account per collector**, created with the session. | First visit is a provisioning workflow (§5.4); the queue is unaffected. |

## 3. Goals, non-goals, anti-goals

**Goals**
- A researcher with a CO, a SRAM group of collectors and a storage volume launches the
  item from the portal and has a working desk with no SSH step at launch.
- **Day-to-day admin needs no SSH** (Danielle, 2026-10-28): everything an admin does
  during a study happens in the SRAM-gated admin overview (§5.6). SSH is for
  provisioning re-runs and rare repairs, each with a documented command.
- Everything study-specific is a catalog parameter or a platform definition.
- The OnlineHate deployment is reproducible from the package with the TikTok definition.
- A replacement workspace attached to an existing volume comes back with its accounts,
  sessions and data (§5.7).
- Testable without SRC: hermetic tests for Python and bash, and a container run of the
  playbook's logic.

**Non-goals (this release)**
- The GPU/feed chain. Worklists are fed by an operator with `pool-add.py`.
- Platforms beyond TikTok and Instagram; a general platform framework.
- Changes to Zeeschuimer or 4CAT upstream.
- Turning Instagram safety copies into 4CAT datasets (named follow-up, §5.5).
- An admin web application or automated backup.

**Anti-goals**
- 4CAT reachable without SRAM: not from the internet, and from a collector's browser
  only through the two upload calls on its own proxy (§6.1 states the boundary).
- A collector reaching another collector's session; any platform identity cookie,
  browsing residue or captured item in a public template artifact.
- Secrets in the repo, in provisioning logs, or in a public release artifact.
- Silent misconfiguration: unfilled placeholders fail preflight loudly.

## 4. Constraints (facts the design rests on)

Verified this session unless marked.

- **SRC-Nginx's gate** (`plugin-nginx`, `authentication.conf.j2`): `/validate` proxies
  to `<user_info_endpoint>cos/<co_id>/?co_role=<rsc_nginx_co_role>`; 200 means "CO
  member (with that role, if set)". The response carries `username`; Utrecht's
  `nginx_location` role also reads `$upstream_http_src_co_roles` into a `REMOTE_ROLES`
  header, semicolon-separated. **Not yet observed on our gateway** (§5.4). `if`/`rewrite`
  run before `auth_request`, so per-user decisions stay in `map`s. SRC-Nginx owns
  `location = /logout`.
- **Plugin CO** writes `/etc/rsc/workspace.json` (CO id, user API endpoint, token,
  flags) and runs `/etc/rsc/cron_user.sh` every minute: CO members get Unix accounts,
  roles become Unix groups. SRAM groups appear as roles `src_co_<group>`. The live CO API
  lists all six users with `src_co_students`.
- **Zeeschuimer 1.14.2**: module ids (`tiktok`, `tiktok-comments`, `instagram`, …);
  enable state `browser.storage.local['zs-enabled-<id>']`, **off in a fresh profile**;
  4CAT URL `browser.storage.local['4cat-url']`. Upload: POST `<4cat-url>/api/import-dataset/`
  with `X-Zeeschuimer-Platform`, then GET `/api/check-query/?key=…`; auth is the browser's
  cookies; a response URL containing `/login/` is reported as "not logged in". Firefox
  keeps `storage.local` and captured items in IndexedDB under
  `storage/default/moz-extension+++<uuid>…/idb/`; writing it from outside Firefox is
  not practical, but SQLite row counts can be read.
- **4CAT `surf` branch**: `.env` binds `127.0.0.1:8080`; Compose project with services
  `db`, `memcached`, `backend`, `frontend` (port 5000 inside); four named volumes;
  `helper-scripts/create_user.py -u NAME -p PASS [-a] -e` creates users without mail,
  "admin" is a user tag. `surf_init.sh` ungates SRC-Nginx's root and is never run. 4CAT's
  `/logout` collides with SRC-Nginx's. A 4CAT user sees and can delete their own
  datasets; admins see all.
- **Docker networking**: `host.docker.internal` via `host-gateway` resolves to the
  bridge address, **not** to the host's loopback; a service bound to `127.0.0.1` is not
  reachable from containers that way. Containers on different user-defined networks
  cannot reach each other; a container can be on several networks.
- **jlesage/firefox**: exec as root with `su-exec 1000:1000` and
  `DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base`; idle sessions cost ~1 core; the
  Firefox policy locks the proxy to `mitm:8080`, so every request the browser makes
  passes through its own proxy.
- **mitmproxy** must create its CA before its Firefox first starts; state dirs are uid
  1000; `stream_large_bodies` is global unless an addon turns streaming off per flow.
- **SRC parameters** arrive as string extra-vars; only declared params reach the
  playbook; Co-Secrets resolve from the launching CO and censor the log (S17). SRC-
  External's `timeout` is the **provisioning** timeout, not a workspace lifetime.
- **Instagram** shows a public post page logged out but walls profile scrolling and most
  of the comments API; X blocks logged-out views outright; TikTok needs no login.

## 5. Architecture

### 5.1 Catalog item

Components: SRC-OS → SRC-CO → SRC-Nginx → SRC-External plugin → **ddp-comments**
(this repo, `deploy-ddp-comments.yaml`, run locally by SRC-External). Ubuntu 24.04,
4 core / 16 GB default (one core per running collector plus 4CAT), 40 GB boot disk,
one attached SRC volume required. Item overwrites: `co_passwordless_sudo=true`,
`co_admin_user_only=true`, `rsc_nginx_user_header_name=REMOTE_USER`, `timeout=3600`
(provisioning). Owner CO: D3I data donation; available on request. Record and snag
list in `docs/catalog-item.md`, modelled on transcribe's.

### 5.2 Parameters

| Parameter | Item action | Default | Meaning |
|---|---|---|---|
| `desk_collector_role` | interactive | `<src_co_your-group>` | SRC role of the collectors' SRAM group. Preflight: `^src_co_[A-Za-z0-9_-]+$` and held by at least one CO member (CO API). |
| `desk_admin_role` | keep | `src_co_admin` | Role for the admin overview, any session's screen, the 4CAT UI. Same check. |
| `desk_platform` | interactive | `tiktok` | Directory under `platforms/`. |
| `desk_hide_media` | interactive | `true` | Install the platform's `userContent.css`. |
| `desk_contact` | interactive | `<name>` | Who collectors are told to ask; in UI text. |
| `desk_batch_sizes` | keep | `5,10` | Claim buttons (rendered into the page). |
| `desk_idle_seconds` | keep | `900` | Idle stop. |
| `desk_timer_minutes` | keep | `3` | Per-item timer on the page (0 hides it). |
| `desk_max_sessions` | keep | `99` | Registry cap (ports 5801–5899). |
| `desk_max_running` | keep | `cores - 1` | Concurrently running session pairs; start-on-open refuses above it with "busy", distinct from "full". |
| `desk_storage_path` | interactive | placeholder = auto-detect | Volume mount; transcribe's detection. |
| `desk_template_source` | keep | `release` | `release` or `workspace` (§6). |
| `desk_gate_mode` | keep | `roles` | `roles` (gateway header) or `api` (CO API polling), §5.4. |
| `fourcat_enabled` | keep | `true` | Bundle 4CAT. When false: no accounts, no tokens, no `fourcat.desk`, the page's upload text is omitted, and the proxy answers 403 for that name. |
| `fourcat_git_ref` | keep | pinned `surf` tag | Upstream checkout. |
| `fourcat_image_tag` | keep | pinned | `DOCKER_TAG`. |
| `zeeschuimer_version` | keep | `1.14.2` | Pinned xpi; selects the template artifact and its sha256. |

No Co-Secrets. Generated secrets are written root-only to `<desk_root>/secrets/` with
`no_log: true` on every task that touches them; the component log stays readable (S17).

### 5.3 Roles (Ansible)

- `preflight`: validate parameters (charset, placeholders), resolve `desk_root =
  <volume>/desk`, require `/etc/rsc/workspace.json` and SRC-Nginx's
  `authentication.conf`, check the collector role via the CO API, **detect an existing
  desk on the volume** (`<desk_root>/desk.json`, §5.7) and switch to reattach mode, and
  probe the gateway (call the user-info endpoint with the workspace token and record
  whether `src_co_roles` is present; 401/403 is inconclusive and only warns).
- `docker`: Docker CE + compose plugin from Docker's apt repo, pinned major.
- `desk`: install the checkout to `/opt/ddp-comments` (the service runs from there),
  lay out `<desk_root>` (`state/`, `templates/`, `secrets/`, `pool.csv`, lock files,
  `desk.json` with the package version and layout version), render `desk-stub.service`
  from the parameters, enable it, **restart it on change** (handler). Install the
  operator commands (`desk-sessions`, `desk-template`, `desk-admin`, `pool-add`).
- `gate`: render nginx pieces (§5.4) under the render lock, `nginx -t`, reload.
- `fourcat` (when enabled): clone 4CAT at `fourcat_git_ref` to `/opt/4cat`; `.env` with
  `127.0.0.1:8080`, pinned tags, the Postgres password (generated once, **reused from
  `secrets/` on reattach**), the four volumes as bind mounts under `<desk_root>/4cat/`
  (D14); a Compose override that puts `frontend` on an external network `desk-fourcat`
  (§6.1); `compose up`; wait for HTTP 200 (not TCP accept); set `SERVER_NAME`;
  memcached `unless-stopped`. No accounts are created here (D12, §5.4).
- `template`: fetch (and sha256-verify) or build the session template (§6).
- `verify`: stub answers on 8110; nginx config valid; a throwaway session provisions
  end to end (registry, 4CAT account, token, containers), its Firefox reaches
  `about:blank` through its proxy; from inside that session `https://fourcat.desk/`
  is 403, `GET https://fourcat.desk/api/check-query/?key=x` reaches 4CAT, and
  `http://frontend:5000/`, `http://172.17.0.1:8080/` and `http://localhost:8080/` are
  refused by the proxy; 4CAT answers 200 on the host; the throwaway session is retired.

### 5.4 Access model: roles in nginx, provisioning on first visit

Every gated location:

```
auth_request /validate;
auth_request_set $username  $upstream_http_username;
auth_request_set $src_roles $upstream_http_src_co_roles;
proxy_set_header REMOTE_USER   $username;
proxy_set_header X-Desk-Roles  $src_roles;      # logged by the stub
proxy_set_header X-Desk-Member $desk_member;
proxy_set_header X-Desk-Admin  $desk_admin;
```

Static maps from the (charset-checked) parameters:

```
map $src_roles $desk_admin  { default 0; "~(^|;)<desk_admin_role>(;|$)" 1; }
map $src_roles $desk_member { default 0; "~(^|;)<desk_collector_role>(;|$)" 1;
                                         "~(^|;)<desk_admin_role>(;|$)" 1; }
```

**Admins are members.** The user's own screen map `$desk_vnc` is keyed on
`"$desk_member:$username"`; the admin screen map `$desk_admin_vnc` on
`"$desk_admin:$target"`. A user whose role is gone falls to the stub on every
location.

**Revocation guarantees, stated honestly:** new HTTP requests from a removed user are
refused at once in `roles` mode and within a minute in `api` mode. An already open
noVNC WebSocket is not re-authenticated; it lives until it closes or the pair is
stopped. `desk-sessions reconcile` (hourly from the stub, and on demand) stops the pair
of any registered user who no longer holds a member role per the CO API, retires the
row, and syncs 4CAT admin tags (below). Prompt revocation = run `reconcile`.

**The registry** (`sessions.tsv`: `username<TAB>index<TAB>status`) allocates ports;
indexes of retired rows are reused. Per-user state is in
`state/users/<user>/provision.json`: steps done (`registry`, `dirs`, `template`,
`4cat_account`, `4cat_token`, `render`, `up`), the applied auth generation (§6.3), and
the last error. **First visit is an idempotent provisioning operation that reconciles
toward "ready"**: a member with no ready state sees "Setting up your desk" with the
current step, and the stub advances the steps; a failed step is retried on the next
visit; the collector sees the step and `desk_contact` if it keeps failing. Locks: a
per-user lock serialises provisioning for that user (two concurrent first visits do the
work once); the **global render lock** (`<desk_root>/.render.lock`, shared with the
playbook) is held only for registry allocation and for rendering compose + nginx and
reloading, never while waiting for 4CAT or Firefox. Operator repair:
`desk-sessions repair <user>` re-runs the steps; `retire <user>` stops and tombstones
without deleting state.

**4CAT accounts (D12):** the 4CAT username is the SRAM username (already validated
`^[a-z][a-z0-9._-]{0,31}$`; 4CAT's `-e` mode accepts it). Creation is
`create_user.py -e -p <generated> [-a]`; the password is stored in
`secrets/4cat-passwords/<user>` (root, 0600) **before** the create call, so a retry
after a crash can log in instead of recreating. If the account exists and no stored
password exists, provisioning stops at that step with "account exists; run
`desk-admin 4cat-user adopt <user>`" (which resets the password through the DB and
stores it); the desk never silently resets an account it did not create. The token
step logs in with curl, stores `secrets/4cat-tokens/<user>` (owner uid 1000, mode 0600,
bind-mounted **only into that user's proxy**; the mount is the isolation boundary, not
the uid). **SRC roles are authoritative for 4CAT privilege:** on first visit and on
every `reconcile`, the user's 4CAT "admin" tag is set or cleared to match
`$desk_admin`; demotion also rotates the user's password and token so existing 4CAT
sessions die. Admins log into the nginx-gated 4CAT UI in their normal browser with
their generated password, shown once to them on the admin overview (§5.6) and
reissuable there; the SSH command `desk-admin 4cat-user show <user>` is the fallback.

Non-members get the 403 page with `desk_contact`. At `desk_max_sessions` the page
says "The desk is full" (registry exhausted); at `desk_max_running` the start-on-open
step says "The desk is busy, try again in a few minutes" (runtime capacity).

**Settling the roles header:** the stub logs `X-Desk-Roles` on every request; the
first Tier 3 step is Danielle logging in and reading that line. If the header is
absent on our gateway, `desk_gate_mode=api` is the designed fallback: the stub polls
the CO user API every minute with the workspace token and renders `$desk_member` /
`$desk_admin` as username maps. Both modes render the same per-user maps.

### 5.5 Platform definitions

`platforms/<name>/`: `platform.json`, `userContent.css`, `README.md`, and `fixtures/`
with one recorded request+response per capture rule (used by the tests).

```json
{
  "name": "tiktok", "label": "TikTok",
  "zeeschuimer_modules": ["tiktok-comments", "tiktok"],
  "item_kinds": {"video": {"url": "https://www.tiktok.com/@x/video/{id}/", "id": "^[0-9]+$"}},
  "default_kind": "video",
  "hosts": ["tiktok.com"],
  "open_pattern": "^/@[^/]+/(video|photo)/(?P<id>[0-9]+)",
  "captures": [
    {"name": "comments", "path": "^/api/comment/list", "items": "comments", "id": "cid",
     "post_id": {"from": "query", "param": "aweme_id"},
     "fields": {"text": "text", "author": "user.unique_id", "created": "create_time",
                "reply_to": "reply_id"},
     "no_stream": true}
  ],
  "login": "none",
  "keep_cookies": [{"host": ".tiktok.com", "name": "cookie-consent"}],
  "problems": [["C", "Captcha or blank page: stop for today and tell {contact}"], "..."]
}
```

**Safety-copy contract:** every captured record is the **raw upstream object plus a
stable envelope**: `t`, `user`, `session`, `platform`, `capture` (rule name), `kind`,
`post_id`, `request` (method, host, path, pagination parameters such as `cursor`; never
headers or tokens), `response` (status, `has_more`/`cursor` fields named by the rule).
A rule's optional `fields` map adds convenience columns (TikTok keeps today's); the
raw object is always present. A rule without `items` stores one record per response
with `body` as the raw JSON. `no_stream` buffers the body up to `max_body_bytes`
(default 16 MB, per rule); larger responses produce an error record and no body, so
one abnormal response cannot exhaust a proxy.

Consumers: `desk_stub.py` (URL from `item_kinds`, id validation, problem dropdown,
how-to text, label; page-side values from the service parameters); `pool-add.py`
(`id,kind,bucket,note`; `kind` optional → `default_kind`; legacy `video_id` accepted;
queue key `(kind, id)`); the mitm addon (§6.1 lists its other duties);
`make-template.py` (`keep_cookies` only; the 4CAT cookie rule and `--fqdn` are
deleted); `make-sessions.sh` (stylesheet when `desk_hide_media`). The schema is frozen
only after the Instagram rule is written against a recorded response (plan task before
the addon is generalised).

**Instagram** (my call over X): modules `["instagram"]`; kinds `post`
(`https://www.instagram.com/p/{id}/`) and `profile`; hosts `instagram.com`; a raw
capture for `^/api/v1/media/(?P<post>[0-9]+)/comments/` with `post_id.from=path` and
pagination from `max_id`/`next_min_id`; `login: "optional"`; `keep_cookies` = the
session cookies observed in the spike (§6.3). Zeeschuimer captures Instagram *posts*,
not comments, so Instagram comments exist only in the safety copy in this release;
`desk-export` (safety copies → CSV for 4CAT's `csv_to_dataset.py`) is the named
follow-up in `docs/FOLLOWUPS.md`.

**Acceptance criteria for "Instagram supported" in this release:** (a) logged out, a
`post` item opens in a collector session and Zeeschuimer captures the post; (b) with a
study account provided by the study, the comments endpoint's responses land in the
safety copy for an opened post and the refresh flow (§6.3) restores capture after the
session is replaced. (a) is required; (b) is required if Danielle provides an account
for the build and otherwise recorded as unverified in the README, which also says that
a study account is shared by every session and that collectors should expect
interruptions when Instagram challenges it.

### 5.6 Admin overview: the daily surface

`/desk/admin/` (admin role only, served by the stub, SRAM-gated) carries every
day-to-day operation, so the operator runbook's SSH section shrinks to provisioning
and repair. Each action is the same code the operator commands call.

| On the page | Does |
|---|---|
| Queue: status (pool size, claimed, done per collector), **upload a selection CSV** | `pool-add` through the browser; rejects bad ids with a line-numbered report. |
| Sessions table: expected (CO API) vs registered, running/stopped/stale, last seen, provisioning step and last error | `reconcile`, `prune`, `stop`, `retire`, `repair <user>` buttons; "watch screen" link (existing). |
| 4CAT: my password (shown once, reissue), per-user token rotate, admin-tag sync | `desk-admin 4cat-user`/`4cat-token`. |
| Template (workspace mode): freeze, refresh, which sessions are stale | `desk-template freeze|refresh`. |
| Export: download `state/captures/` as a zip, and the desk log | End-of-study and spot checks without SSH. |
| Problems: the collectors' problem reports, newest first | Reads the desk log. |

Not on the page, by design: anything that changes parameters or code (re-run the item),
adopting a pre-existing 4CAT account, reading the Postgres password. Those stay SSH.

### 5.7 Storage layout and reattachment

```
<volume>/desk/
  desk.json  sessions.tsv  pool.csv  .render.lock  locks/<user>
  docker-compose.yml  firefox/policies-<user>.json  nginx/*.conf  platform/  templates/
  state/desk-log.ndjson  state/users/<user>/provision.json
  state/{firefox,mitm,captures}/<user>/  state/retired/<user>/
  secrets/   # root 0600: 4cat-postgres, 4cat-passwords/<user>; uid 1000 0600: 4cat-tokens/<user>
  4cat/{db,data,config,logs}/   # bind targets for 4CAT's volumes (D14)
```

**Reattachment** (supported scenario: a new workspace with the same item and an
existing volume): preflight finds `desk.json`, the `fourcat` role reuses the Postgres
password and brings the existing database up, no 4CAT account is created or reset
(provision state says they exist; tokens are re-issued only if a login with the stored
password fails), `desk-sessions render` re-renders compose and nginx from the registry
and the current parameters, and sessions resume on next visit. A layout-version bump in
`desk.json` gates any future migration. Tier 4 tests this separately from a re-run on
the same machine.

The volume survives workspace deletion. **End of study** (runbook): download the
captures zip and a 4CAT export from the admin overview (or copy them over SSH to
Research Drive or Yoda), then delete the workspace. No automatic backup is provided;
the runbook says so.

## 6. The session template and the 4CAT login

### 6.1 The 4CAT boundary (my call, revised after review)

**Topology.** Each user's mitmproxy sits on two networks: `net-<user>` (with its
Firefox) and `desk-fourcat` (external, shared with 4CAT's `frontend` only). Firefox is
on `net-<user>` only. 4CAT keeps its `127.0.0.1:8080` publication for host nginx. The
proxy reaches 4CAT as `http://frontend:5000`; nothing else on `desk-fourcat` is
reachable by collectors because their browsers are not on it.

**The boundary, written down:** a collector controls everything their browser does,
and the browser's only route out is its own proxy (policy-locked). Therefore the proxy
is the enforcement point, and the addon enforces:

- Requests to `fourcat.desk` with method+path exactly `POST /api/import-dataset/` or
  `GET /api/check-query/` are forwarded to `http://frontend:5000` with the request's
  own `Host: fourcat.desk`, **any client-supplied `Cookie` replaced** by that user's
  token cookie (file re-read on mtime change). Redirects and error bodies are returned
  to the browser unmodified and not followed; a redirect to `/login/` is what
  Zeeschuimer reports as "not logged in", which is the right symptom for a dead token.
- Every other request to `fourcat.desk`: 403 from the proxy.
- Requests whose host is `localhost`, an IP literal, a name without a dot, or any
  `*.desk` other than `fourcat.desk`: refused by the proxy. This closes the routes via
  the bridge gateway, container names and the backend network.
- Requests to the workspace's own FQDN: allowed (the collector reaches the desk page
  through SRAM like anyone else; nginx gates it).

`SERVER_NAME` in 4CAT stays the FQDN; the API responses Zeeschuimer uses carry a
dataset URL with that name, which collectors cannot open (no SRAM session in the
browser), and that is acceptable. Tier 3 includes a Playwright check that an upload
from a collector session succeeds and that the dataset appears under that user in 4CAT.

Consequences: no SRAM session inside Firefox (the Enable-uploads button goes), the
`/logout` collision stops mattering for collectors, nginx exposes no 4CAT path to
collectors, and the template contains nothing workspace-specific.

### 6.2 Templates for logged-out platforms are release artifacts

`ddp-comments-template-<platform>-<zeeschuimer_version>.tar`, built from a disposable
profile by a reproducible recipe whose order makes capture impossible: (1) start with
the policy and no template; (2) open the platform once and accept consent, with
Zeeschuimer's modules **still off** (their fresh-profile default); (3) open the
Zeeschuimer popup, enable the listed modules, set the 4CAT URL to
`https://fourcat.desk`; (4) stop; (5) `desk-template release --platform <p>`.

The release command keeps an **explicit allowlist of persistent state** and deletes
everything else: `prefs.js` (filtered to the extension uuid and policy-set prefs),
`extensions.json`, `extension-preferences.json`, `extensions/*.xpi`,
`storage/default/moz-extension+++*` (the extension's IndexedDB), allowlisted rows of
`cookies.sqlite`, `chrome/userContent.css`. `places.sqlite`, `formhistory.sqlite`,
`logins.json`, `key4.db`, session-restore files, caches and crash data are removed.
It then verifies: xpi id present; no cookie outside `keep_cookies`; the extension's
item store **found and empty** ("no database found" fails, never passes); writes
`template.json` (`kind: release`, platform, Zeeschuimer version, modules, built-on,
sha256). The playbook carries the expected sha256 per `(platform, zeeschuimer_version)`
and refuses a mismatch. Release and workspace templates have different commands,
output paths (`templates/release/`, `templates/workspace/`) and manifest kinds, so a
workspace template cannot become a release artifact by default.

### 6.3 Templates for login-required platforms are built on the workspace

`desk_template_source=workspace`: the playbook renders a `template` session pair
reachable only by admins; the runbook: log the study account in, flip switches, set the
URL, then `desk-template freeze --platform <p>`. Until then members see "The desk is
not ready yet; ask {contact}".

**Auth refresh with a generation marker:** each freeze bumps `auth_generation` in the
workspace manifest; each profile's `provision.json` records `applied_generation`. The
stub owns every container start (start-on-open, and `up`), so **before any start it
applies a pending refresh**: with Firefox stopped, it writes the current platform
cookies into that profile's `cookies.sqlite` and records the generation. Running
sessions are refreshed at their next start after an idle stop; the admin overview
shows which sessions are stale and offers "stop now". Captured items live in the
extension's IndexedDB and are untouched.

**Feasibility spike (plan task, before the refresh command is built):** confirm that
Instagram's logged-in state transfers through cookies alone between two profiles. If
site storage is also required, the refresh copies the allowlisted site-storage entries
for the platform origin as well, never the extension store. Proxy-injected platform
cookies were rejected: Instagram's web app reads `csrftoken` from `document.cookie`.

Rejected overall: scripting the IndexedDB (Firefox's structured-clone encoding).

## 7. What changes in the existing code

- `desk_stub.py`: definition-driven; parameters from the service file; first-visit
  provisioning with per-user state and locks; roles headers logged; `kind` in events;
  403, full, busy and not-ready pages; running cap; drop Enable uploads; keep
  start-on-open (now applying pending auth refresh), idle stop, claims, problems,
  timer; the admin overview becomes the daily surface of §5.6 (queue upload, sessions
  table with actions, 4CAT password/token, template freeze/refresh, exports, problem
  reports); hourly `reconcile`.
- `make-sessions.sh` → `desk-sessions` (`add`, `render`, `up`, `reconcile`, `prune`,
  `retire`, `repair`): no `--admin`; `--platform`, `--no-css`; the two networks per
  proxy and the token-file mount; member/admin-keyed maps; locks; nginx reload.
- `make-template.py` → `desk-template` (`release`, `freeze`, `refresh`, `verify`):
  `--platform`, allowlist, empty-store check, manifests, generation; the
  `remember_token` rule and `--fqdn` are deleted.
- `desk-admin`: `4cat-user show|adopt|sync`, `4cat-token rotate <user>`.
- `pool-add.py`: `kind` column, legacy `video_id`, `(kind, id)` key.
- `mitm/addons/capture.py`: generic, definition-driven, envelope contract, body cap;
  the `fourcat.desk` handling and the host refusal rules (§6.1); supersedes
  `capture_comments.py`.
- `firefox/policies.json`: templated for `zeeschuimer_version`.
- New: `deploy-ddp-comments.yaml`, `roles/*`, `platforms/{tiktok,instagram}`,
  `docs/catalog-item.md`, `docs/runbook-operator.md`, `docs/runbook-collectors.md`,
  `docs/platforms.md`, `docs/FOLLOWUPS.md`, `scripts/tier2-docker.sh`,
  `scripts/check-no-pii.sh` (pre-push grep against a list kept outside the repo).
- Tests: `tests/test_platforms.py` (schema, both definitions), `tests/test_capture.py`
  (fixtures replayed through the addon: envelope, body cap, `fourcat.desk` scoping,
  cookie replacement, host refusals), stub tests (first visit steps and retries,
  concurrent first visits, 4CAT down then back, token step crash, full, busy, not
  ready, admin-not-in-group, promotion/demotion on reconcile, pending refresh applied
  before start), ansible-lint, Tier 2.

## 8. Left to the builder

- Ansible task structure, handler names, `platform.json` schema details.
- Stub ↔ `desk-sessions` invocation and the exact lock files.
- Instagram cookie list and comments path: record logged out first, then with a study
  account if provided.
- Playwright harness layout (`tests/e2e/`), storage-state location outside the repo,
  identity selection per run.

## 9. Verification

1. **Tier 1, laptop:** `scripts/test-all.sh` green, `ansible-lint`.
2. **Tier 2, container — scoped on purpose:** `scripts/tier2-docker.sh` runs the
   playbook in an Ubuntu 24.04 container with a stubbed `/etc/rsc/workspace.json` and
   SRC-Nginx layout and **proves rendering and logic**: preflight decisions (including
   reattach detection), rendered compose and nginx files, `nginx -t`, the stub and addon
   against stubbed headers (member, admin-not-in-group, non-member → 403). It does not
   start browsers, systemd or 4CAT; those are Tier 3's job. If the harness starts needing
   Docker-in-Docker accommodations, a small VM replaces it rather than growing it.
3. **Tier 3, sandbox (`checkjefeedzee`):** the playbook runs on it over SSH against
   the real CO (`src_co_students`, `dmccool`, `dmccool2`); Playwright drives the desk
   through the real SRAM gate. Login is Danielle's: a visible browser, she completes
   SRAM, the saved storage state (12-hour cookie) carries the runs. First step: read the
   logged roles header. Then, per build step: first visit provisions `dmccool2` end to
   end; a TikTok video opens and its comments land under `dmccool2` in 4CAT and in the
   safety copy; `https://fourcat.desk/` and the internal-host routes are refused from
   the collector session; concurrent first visits; 4CAT stopped during a first visit,
   then recovery; admin demotion/promotion via `reconcile`; a running session stopped
   and refreshed; **every §5.6 action exercised from the admin page as `dmccool`**
   (queue upload, prune, repair, password reissue, token rotate, captures download).
   Agent SSH: plain commands only (no `$( )`, no heredoc); scripts are copied with
   scp and run by path.
4. **Tier 4, fresh workspace from the item** in the D3I CO with a test SRAM group:
   zero manual SSH fixes; the Tier 3 checklist; a non-member gets the 403 page (if an
   identity exists); idle stop and start-on-open; pause/resume keeps state; prune
   retires a removed user; the Instagram definition reaches "not ready" and the admin
   template flow completes; re-run the playbook (idempotent, service restarted on
   change); **delete the workspace and launch a new one on the same volume**
   (reattachment: accounts, tokens, sessions and data intact).

## 10. Carried-over items

Folded in: capture fixes, D14, the 403 wording, runbooks, 4CAT account lifecycle,
reattachment. Still open, out of scope: GPU/feed chain; `desk-export` for Instagram.

## 11. Advisor reviews (2026-10-28)

**Mistral Large 4** (v1 → v2): `fourcat.desk` path scoping and the account decision;
roles-header settlement, charset check, fallback mode; revocation, prune, cap;
admins-as-members; stale cookie rule; artifact built without capture, empty-store
check, sha256 pin; history check before the public push; `no_log`; service from the
checkout, restart handler, render lock; 4CAT admin/token lifecycle; Instagram data
path; capture schema against recorded responses, `(kind, id)` key; doc hygiene. Pushed
back: SRC-External's `timeout` is the provisioning timeout.

**Codex** (v2 → v3): `host-gateway` cannot reach a loopback-bound 4CAT, so the proxy
joins a backend network with 4CAT's frontend and the browser stays off it (§6.1); the
boundary is written down with the alternate routes the proxy refuses, cookie
replacement and redirect handling (§6.1); first visit is an idempotent provisioning
operation with per-user state, per-user locks, a narrow global lock, stored passwords
before creation, adoption of pre-existing accounts, repair (§5.4); SRC roles are
authoritative for 4CAT privilege, reconciled on visit and hourly, with demotion
rotating credentials; admins' browser login path stated (§5.4); revocation guarantees
qualified and the admin screen map keyed on admin status (§5.4); auth refresh via a
generation marker applied before every start, plus a feasibility spike (§6.3); template
allowlist, found-and-empty check, consent-before-enable recipe, release vs workspace
separation (§6.2); safety-copy envelope contract, pagination provenance, body cap
(§5.5); running cap distinct from the registry cap (§5.2, §5.4); reattachment to an
existing volume (§5.7, §9.4); token-file isolation by mount (§5.4); the failure tests
and an honest Tier 2 scope (§9); Instagram acceptance criteria (§5.5);
`fourcat_enabled=false` behaviour (§5.2). Nothing pushed back.
