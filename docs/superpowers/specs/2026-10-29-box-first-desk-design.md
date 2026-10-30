# ddp-comments, box-first: the desk as an SRC catalog item — design

Date: 2026-10-29. Status: draft for Danielle's approval, after a builder check.
Supersedes `2026-10-28-src-catalog-item-design.md` (the "earlier spec" below) where
the two disagree; carries its product decisions forward where they still hold.
Evidence this design rests on: `docs/box-inventory-2026-10-29.md` (what the sandbox
really is), `docs/proxy-spike-2026-10-29.md` (a Go capture proxy works), the SRC plugin
sources (`plugin-external-docker` 1.1.9, `plugin-nginx` 1.2.4, `plugin-co` 1.3.9) and
the SURF wiki mirror in `d3i-claude-skills`.

## 1. Intent

A requestable SURF Research Cloud (SRC) catalog item that gives a study a
comment-collection desk: collectors log in with SRAM, get a streamed Firefox with
Zeeschuimer whose every request passes through their own capture proxy, work a shared
queue of items, and upload to a bundled 4CAT; a safety copy of the captured API
responses lands on the study's storage volume; admins run the day from a web page.

Why now, and why this way. The desk exists on the `checkjefeedzee` sandbox and works,
but it was designed on a desktop and pushed to the box, and the push kept breaking on
things only the box knows (Python version, file ownership, ports in use). Danielle's
intent is the reverse: **design on the box, configure it precisely, and capture that as
the item**. Every component is built, run and verified on the sandbox at each step. The
desktop is a place to review and commit, never a proxy for the box.

Two further decisions shape everything: **Go for everything we write** (one static
binary, Docker's own client library, no runtime on the box), and the item is a
**Docker Compose component** run by SRC's own `SRC-External Docker Compose` plugin,
so the platform installs Docker, copies our repository, runs our init script as root
and starts our compose file. We write no Ansible and no Python.

## 2. Decisions Danielle took (2026-10-29)

| # | Decision | Consequence |
|---|---|---|
| B1 | Box-first: design, configure and test on `checkjefeedzee`; the desktop is not a proxy. | Every build step runs and is verified on the box; the record of what the box needs is the item. |
| B2 | The product is the catalog item, tailored to that box. | A generic Docker-and-nginx deployment is a failure even if it works. |
| B3 | Use SRC components where they suit us better than our own; never "make do". | SRC-Nginx and SRC-CO are used as designed; the 4CAT component is not (§4.3). |
| B4 | Go, unless Python is actually the right tool for a piece. The spike showed it is not needed anywhere. | Desk, capture proxy, operator commands, template tooling: Go. Init script: POSIX sh. |
| B5 | Two SRAM roles: collectors (e.g. student assistants) collect and nothing more; admins set permissions and run the study. | Role names are parameters; the desk reads roles from the platform. |
| B6 | Non-technical admins do all **daily** work on the admin page; **occasional** work may be ssh with a documented command. | §6 lists which is which. |
| B7 | Vibe (Mistral on the box) is build-time only. | Nothing in the product depends on it; it is removed from the box before release. |
| B8 | The desk does not manage data, but its path must be legible. One study drive, which for now is the attached SRC storage volume. | §7. Research Drive is a later sink, not this release. |
| B9 | No relaunch of the sandbox now; reshape it in place; relaunch from the portal only as the final proof. | §9 tiers. |
| B10 | Something nice, not something that works. | Craft is in scope: one binary, no hand steps, readable config, honest errors. |
| B12 | The sandbox is idle during the build and its 4CAT data and sessions are discardable (2026-10-30). | Each old piece is replaced outright (§8); no side-by-side run, no data migration. |
| B13 | The **D3I data donation** CO owns the component and the catalog item; the `ddp-comments` component does not exist yet and is created once, then only edited. A workspace is launched by the **study's own CO**, OnlineHate here, and its SRAM groups define collectors and admins (2026-10-30). | The desk keys every role decision on the workspace's owning CO, which is what the platform already does: `workspace.json` carries that CO's id and token, and `/validate` checks membership of it. §9.3 launches happen in OnlineHate. `docs/catalog-item.md` records the component and item values for Danielle to enter in the portal. |
| B14 | **Automated enforcement first; ADRs above it** (2026-10-30). Layers: Go correctness (`gofmt`, `go vet`, `golangci-lint`), Go architecture (`depguard` plus our own architecture tests), shell (`shellcheck`, `shfmt`), configuration (`docker compose config`), and what else a layer can hold. What cannot be enforced automatically gets an ADR. An ADR whose rule has an automated consequence points at that enforcement; an ADR is never a substitute for it. ADRs exist to put the right rule in front of an editor when something unexpected comes up. | §10. |
| B11 | Keep the capture proxy and the raw safety copy (after a split advisor review: Codex would drop interception for a plain relay; Mistral and I would keep it). | §5.5 stays a TLS-intercepting proxy. The insurance is against an upload that never happened: items stuck in a browser's IndexedDB cannot be read from outside, and the raw copy is the only record when 4CAT is down or a token has died. Hiding videos from collectors is the profile stylesheet (`desk_hide_media`), not the proxy. |

## 3. Goals, non-goals, anti-goals

**Goals**
- A researcher with a CO, two SRAM groups and a storage volume launches the item and
  has a working desk with no ssh step at launch.
- Every day-to-day operation of a running study is on the admin page.
- Everything on the box that the platform did not put there is in our repository, and
  the list is short enough to read in a minute.
- The OnlineHate study's desk (TikTok) is reproducible from the item.
- A replacement workspace on the same volume comes back with accounts, sessions and
  data.
- Verifiable on the box at every step, and by a portal launch at the end.

**Non-goals (this release)**
- Instagram and login-required platforms. The platform definition and the template
  format keep room for them; nothing is built for them.
- The GPU/feed chain. Selections arrive by upload.
- Managing or exporting the collected data beyond leaving it in a clear place on the
  volume.
- Running 4CAT on its own workspace.

**Anti-goals**
- A second way into 4CAT or the desk besides SRC-Nginx and the collectors' proxies.
- Anything a fresh launch would not have that is not written down.
- A Python interpreter, pip, or a hand-run script on the production box.
- A hand-prepared Firefox profile per workspace for a logged-out platform.

## 4. Constraints: what the platform is (read from source, verified on the box)

### 4.1 SRC-External Docker Compose (plugin-external-docker 1.1.9)

With a component of script type **Docker Compose** attached, and only then, it:
installs `docker-ce` from Docker's apt repository; copies the component's repository to
`/rsc/plugins/<repo-name>/` as root with mode 0600; if the component sets
`docker_initialization_command`, runs that script (a path relative to the compose
file's directory) **as root, before compose up**, with the environment variable
`PLUGIN_PARAMETERS` holding the component's parameters; restarts the Docker daemon;
writes `src_ansible.env` next to the compose file with every Ansible variable and
parameter as `key=value`; runs `docker compose up -d` on the named file, with the
component's `timeout` as the provisioning time limit. Without such a component it does
nothing, which is why the sandbox's Docker was installed by hand.

Consequences. The init script is our only host-level hook and runs once per
provisioning. Updates after launch are an ssh step (§6). Parameters reach the init
script as JSON in `PLUGIN_PARAMETERS`; `src_ansible.env` is written **after** the
init script and feeds container environments only when a service names it as an
`env_file`. It does **not** feed compose `${...}` interpolation, so every value the
compose file itself needs (the desk root on the volume, the FQDN, image tags) is
written by the init script into `.env` next to the compose file, which compose reads
for interpolation. The init script runs **before** the plugin restarts the Docker
daemon: it may validate, write files, and pull images, but it starts no container.
The repository copy is root-only; our containers run as root through the Docker
socket anyway, but every file Firefox (uid 1000) must read is a copy on the volume
with an explicit 0644 mode, never a bind of the repository copy.

### 4.2 SRC-Nginx (plugin-nginx 1.2.4)

Installs nginx and an acme.sh certificate; `tls_main.conf` is the one server block on
443 and ends with `include /etc/nginx/app-location-conf.d/*.conf;`. That folder is the
designed extension point and the wiki's SRAM-login recipe says to put our location
files there. `authentication.conf` provides the OAuth2 flow, `/validate` (proxies to
the CO user API with the optional `rsc_nginx_co_role`; the response carries `username`
and `src_co_roles`), `/logout` and `/test`. `client_max_body_size 0` is global. The
root serves a static page. Observed on the sandbox gateway: `src_co_roles` arrives
semicolon-separated, no spaces, e.g. `src_co_admin;src_co_students`; a location that
does not set the header gets an empty value.

### 4.3 SRC-CO (plugin-co 1.3.9)

Every minute `cron_user.sh` fetches the CO member list from the CO user API with a
root-only token (`/etc/rsc/workspace.json`, mode 0600) and makes Unix users (uid =
SRAM integer id), one Unix group per SRAM role named `src_co_<group>` (cut to 32
characters; `@all`, `src_co_wallet`, `src_no_ext_apps`, `src_ws_admin`,
`src_developers` are ignored), and gives admins passwordless sudo. The same API, with
the same token, lists every member's roles. `co_admin_user_only=true` keeps
non-admins' shells at `nologin`; they still exist as users. Research Drive is mounted
per user, by rclone, only for members who connected it; nobody on the sandbox has.

### 4.4 The 4CAT catalog component

Script type Docker Compose on 4CAT's `surf` branch, `docker-compose_public_ip.yml`, no
init command. So it is `docker-ce` plus `compose up` of a file that publishes the
frontend on all interfaces at port 8080 and port 446, ungated, with no `SERVER_NAME`,
and its `surf_init.sh` (which would ungate the nginx root) never runs. It gives
nothing we need and opens a door we must close. Not used (B3).

### 4.5 The sandbox (`docs/box-inventory-2026-10-29.md`)

First boot 2026-09-07 with SRC-OS, SRC-CO and SRC-Nginx. Docker is Ubuntu's
`docker.io` 29.1 with the Compose plugin 2.40, installed by hand that evening; the item
will install `docker-ce` instead, and the sandbox keeps `docker.io` (the plugin cannot
be re-run there). Hand additions, all file-based: `/opt/ddp-comments`,
`desk-stub.service`, four nginx files, three symlinks in `/usr/local/bin`, the desk
root on the volume, `~/4cat` (dirty), `~/spike`, and the build tooling (uv, Python
3.14, Vibe). 4CAT's volumes are on the boot disk. `4cat_frontend` publishes
`0.0.0.0:8080` and `0.0.0.0:446`; only the SRC firewall shields it. The storage volume
is `/dev/vdb1` at `/data/onlinehate_zeeschuimer`, 10 GB, with `~/data` a symlink to
`/data`.

### 4.6 Catalog mechanics (SURF wiki, transcribe snag list)

Parameter values are strings, never empty, with no single or double quotes. Source
types `Workspace` (`co_id`, `co_name`) and `Resource` (`workspace_fqdn`) supply those
values without a parameter of ours. Only parameters declared on the component reach
the script. Editing a component creates a development version; it is tested by cloning
the catalog item and pointing the clone at that version; promote development → pilot
→ live. Never recreate a component. A Co-Secret censors the whole provisioning log.
The firewall is 22, 80 and 443, immutable. Portal logs show status changes only; the
full provisioning log is a download.

### 4.7 Browser and proxy facts

jlesage/firefox runs Firefox as uid 1000 and **silently ignores an unreadable policy
file**. The Firefox policy locks the proxy and installs the CA. Zeeschuimer 1.14.2
keeps its state in the profile's IndexedDB; its modules are off in a fresh profile. A
Go proxy on `goproxy` intercepts TLS with its own CA, passes websockets and large
downloads, and captured six TikTok comment-list responses on the box in the spike;
TikTok served them uncompressed and the post id is the `aweme_id` query parameter.
HTTP/2 to the browser needs `NextProtos` set on the MITM TLS config.

## 5. Architecture

### 5.1 The catalog item

Components, in order: **SRC-OS → SRC-CO → SRC-Nginx → SRC-External Docker Compose →
ddp-comments** (ours; script type Docker Compose; repository
`researchcloud-ddp-comments`, path `deploy/compose.yml`, tag = the release). Ubuntu
24.04. Default flavour 4 core / 16 GB (one core per running collector plus 4CAT),
boot disk 40 GB, one attached volume required. Item overwrites: `co_passwordless_sudo
= true`, `co_admin_user_only = true`, `rsc_nginx_user_header_name = REMOTE_USER`,
`rsc_nginx_co_role` left empty (the desk gates per role itself; see §5.4 for why),
`timeout = 7200` (image pulls dominate provisioning: 4CAT's image alone is over 5 GB,
fetched through a shared egress address subject to Docker Hub's anonymous limits),
`docker_initialization_command = init.sh`. Access button: Webinterface (https).
Owner CO: D3I data donation; available on request. Item record and snag list in
`docs/catalog-item.md`, modelled on transcribe's.

**Images are prebuilt and pinned.** Each release publishes `desk` and `deskproxy`
images to GHCR under the repository, tagged with the release; the compose file
references those tags, and the 4CAT image tag, through `.env`. Nothing is built from
source at provisioning time; a cold build on a 4-core flavour inside the timeout is
the likeliest way to fail a launch. The sandbox builds the same images locally in the
`golang:1.26` container during development.

Release mechanics (my calls after the builder check): the repository is public
(D8), so its GHCR packages are set public and a fresh workspace pulls them
anonymously. A GitHub Actions workflow on a release tag builds and pushes the two
images, mirrors the pinned 4CAT image from Docker Hub into the same GHCR namespace
(removing the anonymous-limit risk for the 5 GB pull), and attaches the template
artifacts to the GitHub release. No credentials exist on any box. The 4CAT pin is a
versioned upstream tag chosen by the builder from Docker Hub (not `latest`);
bumping it runs 4CAT's own migrations on existing data at the next `update`, which
the runbook says. The platform copies our repository to `/rsc/plugins/<repo>` as a
plain copy, not a git clone, so `init.sh update` fetches the release tarball from
GitHub and replaces the copy. After the transfer to `d3i-infra` (D7) the GHCR
namespace and release URL move with it; `.env` carries the namespace so a release
made before the move keeps working.

**Pre-launch checklist** (in `docs/catalog-item.md`): the release's images and the
template artifact for `(desk_platform, zeeschuimer_version)` are published; the two
SRAM groups exist and each has at least one member; the volume exists in the
launching CO.

### 5.2 Parameters (declared on the component)

| Parameter | Item action | Default | Meaning |
|---|---|---|---|
| `desk_collector_role` | interactive | `src_co_<fill-me-in>` | SRAM role of the collectors. Preflight: `^src_co_[A-Za-z0-9_-]+$`, no placeholder, held by at least one CO member. |
| `desk_admin_role` | keep | `src_co_admin` | Role for the admin page, any session's screen, the 4CAT UI. Same preflight checks as the collector role, including "held by at least one member", or a launch succeeds with nobody able to reach the admin page. |
| `desk_platform` | interactive | `tiktok` | Directory under `platforms/`. |
| `desk_contact` | interactive | `<fill-me-in>` | Who collectors are told to ask; appears in page text. No quotes (platform rule). |
| `desk_hide_media` | keep | `true` | Install the platform's `userContent.css`. |
| `desk_batch_sizes` | keep | `5,10` | Claim buttons. |
| `desk_idle_seconds` | keep | `900` | Idle stop. |
| `desk_timer_minutes` | keep | `3` | Per-item timer on the page; `0` hides it. |
| `desk_max_sessions` | keep | `99` | Registry cap (ports 5801–5899). |
| `desk_max_running` | keep | `auto` | Concurrently running pairs; `auto` = cores − 1. |
| `desk_storage_path` | keep | `auto` | `auto` = the single mount under `/data` that is not `datasets`; several mounts fail loudly. |
| `fourcat_enabled` | keep | `true` | Bundle 4CAT. `false`: no accounts, no tokens, upload text omitted, proxy refuses `fourcat.desk`. |
| `fourcat_image` | keep | pinned `digitalmethodsinitiative/4cat:<tag>` | Image for backend and frontend. |
| `desk_image` | keep | pinned `ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:<tag>` | Image for the desk service; the tag is the release. |
| `deskproxy_image` | keep | pinned `.../deskproxy:<tag>` | Image for every collector's capture proxy. |
| `zeeschuimer_version` | keep | `1.14.2` | Selects the template artifact. |
| `template_sha256` | keep | pinned per release | The digest the init script requires of the fetched template artifact. The catalog item is the trust root; a digest shipped beside the artifact in the release would only guard against corruption. |

`co_id`, `co_name` and `workspace_fqdn` come from the platform's own source types.
No Co-Secrets: every secret is generated on the box and stored under the desk root
(§5.7). The init script validates every parameter before touching the box and prints
one line per failure; because the plugin runs the script before compose, a failed
validation fails provisioning with the message in the downloadable log.

### 5.3 What runs where

```
host (platform-owned)                      compose project "desk" (ours)
─────────────────────────────────────      ──────────────────────────────────────────────
SRC-Nginx :443 ──include──► desk.conf  ──► desk        (Go; :8110 on 127.0.0.1; Docker socket)
                            fourcat.conf ─► 4CAT frontend (via desk, admins only)
SRC-CO cron (users, groups)                4cat-backend, 4cat-db, 4cat-memcached (internal net)
docker-ce (installed by the plugin)        per collector, on demand, started by desk:
/rsc/plugins/ddp-comments (repo copy)         firefox-<user>   (jlesage/firefox, net-<user>)
/data/<volume>/desk (state, captures, 4CAT)   proxy-<user>     (Go capture proxy, net-<user> + desk-fourcat)
```

- **desk** is the one long-lived container of ours: the collector pages, the admin
  page, the noVNC relay, provisioning, the registry, the reaper, rendering of
  per-user compose files and Firefox policies, and the operator commands (the same
  binary with subcommands, run with `docker compose exec desk desk <cmd>` or through
  a thin `bin/desk` wrapper the init script links into `/usr/local/bin`). It mounts
  the Docker socket, the desk root on the volume, and the repository copy read-only.
  It runs as root for the socket and writes the files Firefox must read with mode
  0644 and uid 1000 ownership.
- **4CAT** is four services in our compose file, written from 4CAT's own
  `docker-compose_public_ip.yml`: same image, entrypoints and env, but **no published
  ports**; the frontend is on the internal `desk-fourcat` network only; the four
  volumes are bind mounts under `<desk_root>/4cat/`; `SERVER_NAME` is the FQDN;
  `memcached` and `db` restart `unless-stopped`. No clone of 4CAT's repository exists
  on the box.
- **Per collector**: a Firefox and a capture proxy, rendered into
  `<desk_root>/compose/<user>.yml` by desk and started with the Docker client library,
  on `net-<user>`. **`net-<user>` is a Docker `internal` network**: it has no gateway
  and no route to the internet or the host. The proxy is also on `desk-egress` (a
  bridge with internet access) and on `desk-fourcat`; the desk is on `net-<user>` for
  the screen relay. So the Firefox container's only way to anything outside its
  network is through its proxy, whatever its preferences say; the policy lock is a
  convenience for the browser, not the isolation. The Firefox's `/config` is the
  user's profile dir on the volume; its policy file is rendered world-readable.

**No nginx changes after launch.** A container cannot reload the host's nginx without
a privilege hack, so the design never needs one: nginx has exactly two location
files, both static, written once by the init script, which then runs `nginx -t` and
`systemctl reload nginx` (it is root on the host; this is the one reload). `desk.conf`
proxies everything under `/desk/`, gated, to `127.0.0.1:8110`; `fourcat.conf`
proxies `/` for admins to the desk, which proxies on to the 4CAT frontend. The desk
serves every noVNC websocket itself and routes by the authenticated username to that
user's `firefox-<user>:5800` over the Docker network, since the desk is on every
`net-<user>`; an admin watching another screen is a separate admin-gated route. No
per-user map file is rendered, and the desk needs no host privilege beyond the
Docker socket. The websocket location's `proxy_read_timeout` is set long in
`desk.conf` once. (This replaces the earlier spec's rendered nginx maps and render
lock.)

**Two listeners, and a gate secret.** Being on every `net-<user>` means a browser
whose policy failed could reach the desk container directly and send forged identity
headers. So the desk has two listeners. The **gated listener** (`:8110`, published on
the host loopback only) accepts a request only when it carries `X-Desk-Gate` equal to
a secret the init script generated into `<desk_root>/secrets/gate` and wrote into
both location files as a `proxy_set_header`; nginx overwrites any client-supplied
copy of that header and of `REMOTE_USER` and `X-Desk-Roles`. The **container
listener** (a second port, bound only on the per-user networks) serves exactly the
endpoints that take no identity from the request: the proxies' health reports and
nothing else; every other path on it is 404. A forged request to either listener
from a browser gets 403.

**Lifecycle.** Every service in the compose file, including per-user pairs, has
`restart: unless-stopped`, so pause and resume bring the desk and 4CAT back without
a hand step. A desk restart drops open websockets; collectors reload the page and the
pair is still running. The running cap is reserved atomically under the global lock
before a pair is started, not per user. Retiring a user stops the pair, detaches the
desk from `net-<user>`, then removes the network. When the CO API is unreachable the
desk keeps its last member list, serves from it, logs the outage on the admin page,
and never revokes on a failed poll; revocation needs a successful poll that omits the
user. A successful poll that returns **no members at all** is treated as a failed
poll and raises an alarm on the admin page, since a CO with a workspace always has at
least its admin.

### 5.4 Access model

Every gated location in `desk.conf` and `fourcat.conf`:

```
error_page 401 = @custom_401;
auth_request /validate;
auth_request_set $username  $upstream_http_username;
auth_request_set $src_roles $upstream_http_src_co_roles;
proxy_set_header REMOTE_USER  $username;
proxy_set_header X-Desk-Roles $src_roles;
```

The desk decides from `X-Desk-Roles` per request, after checking the gate secret
(§5.3): the header is split on `;` and a role matches only as a whole segment, never
as a substring (`src_co_students_x` is not `src_co_students`). Collector if a
segment equals `desk_collector_role`, admin if one equals `desk_admin_role`; admins
are collectors too. The username is validated against `^[a-z][a-z0-9._-]{0,31}$`
before it is used in any path or container name. Non-members get the 403 page
naming `desk_contact`. `rsc_nginx_co_role` stays
empty on the item because the platform's gate can hold one role and we need two
populations on one site; the desk refuses non-members itself, on every request. As
belt and braces the desk also polls the CO user API every minute with the workspace
token (the init script copies `co_token` from `/etc/rsc/workspace.json` into
`<desk_root>/secrets/co_token`, root 0600, mounted into desk) and treats a username
absent from the list, or without the role, as revoked: its running pair is stopped
within a minute, every websocket the desk relays for or to that user is closed, and
its registry row retired. An admin demoted to collector loses any open watch-screen
socket the same way. This replaces the earlier spec's "gate_mode" switch: both
signals are always on.

**First visit** provisions idempotently toward "ready": registry row and ports;
profile directory from the template; 4CAT account (password generated and stored
before the create call; an existing account without a stored password stops with
"ask an admin to adopt it"); 4CAT token (login, store, uid 1000 0600, mounted only
into that user's proxy); render the pair; start it. A per-user lock makes concurrent
first visits do the work once; a global lock covers registry allocation only. Steps,
errors and the last applied template generation live in
`state/users/<user>/provision.json`. SRC roles are authoritative for 4CAT privilege:
the admin tag is set or cleared on every visit and every poll; demotion rotates the
password and token.

**Caps.** `desk_max_sessions` is the registry ("the desk is full");
`desk_max_running` is runtime ("the desk is busy, try again in a few minutes"). The
reaper stops idle pairs after `desk_idle_seconds`; opening the page starts a stopped
pair.

### 5.5 The capture proxy and the 4CAT boundary

One Go program (`cmd/deskproxy`), from the spike, hardened: HTTP/2 to the browser;
per-rule `max_body_bytes` (default 16 MB) with an error record instead of a body
beyond it; the safety-copy envelope `t, user, session, platform, capture, kind,
post_id, request{method,host,path,query_keep}, response{status,...}` plus `item` (for
rules with `items`) or `body`; rule file re-read on change; one stderr line per
request; a `/healthz` on a second port for the desk.

**"Raw" means this (B11):** the stored record always contains the upstream object
exactly as decoded (the full response body for a `body` rule; each element of the
named array for an `items` rule), with the rule's `fields` map adding named columns
beside it, never replacing it. Reprocessing the safety copy must be possible without
the browser or 4CAT. Why the proxy stays a TLS interceptor rather than a plain
relay: Zeeschuimer's 4CAT URL is reached from `https://` pages, so it must be
`https://fourcat.desk` and the browser must trust our CA for that name; once that
is true, intercepting the platform's responses adds no new trust exposure.

The boundary, enforced in the proxy because the browser's only route out is the
policy-locked proxy:
- `fourcat.desk` with exactly `POST /api/import-dataset/` or `GET /api/check-query/`:
  forwarded to `http://4cat-frontend:5000` with `Host: fourcat.desk`, the client's
  `Cookie` replaced by `remember_token=<that user's token>`, redirects and errors
  passed back unmodified and never followed.
- Everything else on `fourcat.desk`: 403 from the proxy.
- Hosts that are `localhost`, an IP literal, dotless, or `*.desk` other than
  `fourcat.desk`: 403.
- The workspace FQDN: allowed (nginx gates it).
- Everything else: pass through, captured only when a rule matches.

### 5.6 Platform definitions and templates

`platforms/<name>/platform.json`, `userContent.css`, `README.md`, `fixtures/` with a
recorded request and response per capture rule: the earlier spec's schema, unchanged
(item kinds, hosts, `open_pattern`, captures with `post_id`, `fields`, `query_keep`,
`max_body_bytes`, `login`, `keep_cookies`, `problems`). Go loads it with a strict
decoder; an unknown key is an error.

Templates for logged-out platforms are **release artifacts**
(`ddp-comments-template-<platform>-<zeeschuimer_version>.tar` with sha256), built by
`desk template release` from a disposable profile by the earlier spec's recipe
(consent accepted with modules off; modules enabled; 4CAT URL set; stop; sanitise to
an explicit allowlist; verify items 0, uploads 0, navigation records 0, settings ≥ 1).
The item's init script fetches the artifact for `(desk_platform, zeeschuimer_version)`
from the release and refuses a sha256 mismatch.

The release recipe has never completed on the box: the Python tool's last run was
paused after the navigation-record fix. **My call:** the Go `desk template release`
establishes it, and it is built early, before the throwaway portal launch, because
that launch needs a published artifact. Its first input is the sandbox's existing
`state/firefox/_template/` profile, where consent is already accepted and the
modules are already on, so the first `tiktok-1.14.2` artifact needs no new clicks.
The Python tool is not run on the box again; its sanitiser allowlist and its
verification rules are the behavioural reference for the Go port. Login-required platforms (workspace
templates, auth generation refresh) keep their place in the schema and the manifest
kinds, and are built in a later plan.

### 5.7 Storage layout on the volume

```
<volume>/desk/
  desk.json                      parameters as applied, package version, layout version
  registry.tsv  pool.csv  locks/
  compose/<user>.yml  firefox/<user>/policies.json (0644)
  templates/release/<platform>-<zs-version>/
  state/desk-log.ndjson  state/users/<user>/provision.json
  state/firefox/<user>/  state/proxy/<user>/  state/captures/<user>/  state/retired/<user>/
  secrets/   root 0600: co_token, 4cat-postgres, 4cat-passwords/<user>; uid 1000 0600: 4cat-tokens/<user>
  4cat/{db,data,config,logs}/    bind targets for 4CAT's volumes
```

Nothing of ours lives on the boot disk except the platform's repository copy and two
nginx files. **Reattachment** (a new workspace on an existing volume): the init
script finds `desk.json` on the volume and switches to reattach mode: it reuses the
Postgres password and the 4CAT data, creates no accounts, and **re-derives
everything workspace-bound**: the new `co_token`, the new FQDN (into `.env`, the
4CAT `SERVER_NAME`, the policies), a fresh gate secret, the Docker networks and
per-user compose files (rendered again from the registry; absolute bind paths change
with the volume name only if the volume was renamed, which preflight reports), and
the runtime state (every registered pair is "stopped" until its next visit;
`provision.json` steps that reference containers are re-run idempotently). A layout
version in `desk.json` gates future migrations. The distinction between "partial
provisioning to resume" and "an account we did not create" is made per step from
`provision.json`, never from the mere existence of a 4CAT account. **Data path (B8):** selections come in by admin upload to `pool.csv`; captures
land in 4CAT (`4cat/`) and the safety copy (`state/captures/`), both on the volume;
leaving the box is the volume itself, plus the admin page's captures zip and 4CAT's
own exports. Research Drive is a later sink.

## 6. The admin page and the ssh list

**On the page** (`/desk/admin/`, admin role), each action the same code the operator
subcommand calls:

| On the page | Does |
|---|---|
| Queue: pool size, claimed and done per collector; upload a selection CSV | Excel-tolerant parse; rejects with line numbers; file-level errors refuse the whole file. |
| Sessions: expected (CO API) vs registered; running / stopped / stale; last seen; provisioning step and last error | start, stop, retire, repair, prune; watch screen. |
| 4CAT: my password (shown once, reissue); per-user token rotate; admin-tag sync | |
| Export: captures zip; desk log | |
| Problems: collectors' reports, newest first | |

**Over ssh, documented, occasional:** update to a new release (`sudo
/rsc/plugins/ddp-comments/deploy/init.sh update <release>`: fetches that release's
repository tag into the copy, rewrites `.env` with the new pinned image tags,
`compose pull`, then `compose up -d`, which recreates only the services whose image
or spec changed, after which the desk re-renders per-user files itself on start);
change a parameter after launch (the portal cannot reach a running workspace: edit
`desk.json` on the volume and run `init.sh apply`, which re-validates, rewrites
`.env`, and asks the running desk to re-render with `docker compose exec desk desk
render`; rendering is only ever the desk's job); adopt a pre-existing 4CAT account;
build a workspace template (later plan); read the Postgres password.

## 7. How today's code is used

The Python package `desk/desklib/` (4,154 lines, 325 tests) is the behavioural
reference: registry, provisioning steps and their error texts, pool parsing rules,
template sanitiser allowlist, capture envelope, 4CAT account and token handling, the
admin routes. The Go port keeps those behaviours and their tests' intent (table tests
in Go), not the code. The bash sandbox scripts, the systemd unit, `/opt/ddp-comments`
and the Python test suite are deleted from the repository when their Go replacement is
verified on the box, never before. `platforms/tiktok/` is kept as is.

## 8. How the build happens (box-first)

- The repository has a workspace on the box at `~/ddp-comments`, mirrored with the
  local checkout by `scripts/box-sync.sh` (rsync both ways, excluding `.git`); commits
  happen locally after reviewing the diff; the path and the rule go in `CLAUDE.md`.
- Hands-on work (writing Go, compose, the init script; running it; reading logs) is
  done on the box by the Mistral box worker from a brief, or directly over ssh for
  small things. Builds run in a `golang:1.26` container; `go test` runs there too.
  No Go toolchain, no Python, no pip on the host.
- Each step ends with a verification on the box named in its brief: a `go test`
  pass, a container up and answering, a page served through SRC-Nginx as `dmccool`
  and `dmccool2`.
- The sandbox's hand install is retired in place as the compose-based desk replaces
  it (B12: nobody is collecting; nothing on it is kept): stop and disable
  `desk-stub.service`, remove `/opt/ddp-comments` and the `/usr/local/bin` symlinks,
  replace the four nginx files with the two new ones, stop the hand-run 4CAT and
  start ours fresh on the volume (no data migration), remove `~/4cat`, `~/spike`,
  `~/ddp-comments` (the old copy) and the old desk root. Each removal is recorded in
  the ledger.
- Vibe, uv and Python 3.14 in `~/.local` are removed at the end (B7).

## 9. Verification

1. **On the box, every step**: `go vet`, `go test` in the build container; the piece
   under construction running on the box and exercised with curl or the browser
   through the real gate.
2. **Sandbox end to end** (`checkjefeedzee`, real CO, `dmccool` admin and `dmccool2`
   collector): first visit provisions `dmccool2`; a TikTok video opens, comments land
   under `dmccool2` in 4CAT and in the safety copy; from the collector session
   `https://fourcat.desk/` and the internal routes are refused; concurrent first visits
   make one account, one token, one row; 4CAT down during a first visit, then
   recovery; demotion and promotion; idle stop and start-on-open; every admin-page
   action as `dmccool`; a non-member gets the 403 page (stubbed header if no such
   identity exists). Playwright in a container on the box with Danielle's saved SRAM
   state, or by hand with the checklist.
3. **Portal launches** (the proof, B9; the sandbox itself is never relaunched):
   - *Early, throwaway* (my call, both advisors asked for it): as soon as the init
     script, the compose file, the first images and the first template artifact
     exist, launch a workspace in the OnlineHate CO (B13: the study's CO launches,
     D3I owns the item) from a cloned catalog item pointing at the development
     version of the component, with its real SRAM groups. This is the only way to
     exercise the plugin's `docker-ce` path, the root-only repository copy, the
     parameter delivery and the image pulls inside the timeout. Delete it afterwards.
   - *Final*: a fresh workspace in the OnlineHate CO with its SRAM groups and a new
     volume: zero ssh fixes; the sandbox checklist; forged-header and cross-user
     screen attempts refused; pause and resume with everything back; delete the
     workspace and launch another on the same volume (reattachment). Then promote
     the component.

## 10. Enforcement: automated layers first, ADRs above them (B14)

Every rule that a tool can hold is held by a tool, run by one command
(`scripts/check.sh`, also the CI job and the pre-commit gate), in the build container
on the box as well as locally. The layers, each with its home:

| Layer | Tool | Home |
|---|---|---|
| Go correctness | `gofmt -l`, `go vet`, `golangci-lint` | `.golangci.yml` |
| Go architecture | `depguard` for import rules per package (only `cmd/deskproxy` imports the proxy library; only `internal/docker` imports the Docker client; `os/exec` nowhere but `internal/docker`), plus our own architecture tests in `internal/arch/` that walk the packages and the rendered output. Import rules cannot see who reads an HTTP header, so identity extraction is centralised in `internal/access` and an **AST-based rule** (a `go/analysis` analyzer run by `golangci-lint`, or an `internal/arch` test over `go/ast`) rejects any `Header.Get` of `REMOTE_USER`, `X-Desk-Roles` or `X-Desk-Gate` outside that package. | `.golangci.yml`, `internal/arch/*_test.go`, `internal/lint/` |
| Behavioural invariants | table tests that *are* the rule: the 4CAT allowlist, exact-segment roles, the gate secret (forged headers tested through both listeners), raw-object records, template verification, idempotent provisioning steps, revoke-only-on-success, token and screen binding, atomic cap reservation, and that collector lifecycle operations (start, stop, retire, repair) never modify or reload nginx (nginx file hashes and reload count unchanged across them, on the box) | the package's `*_test.go`, `tests/box/` |
| Shell | `shellcheck`, `shfmt -d` | `deploy/init.sh`, `scripts/*.sh` |
| Configuration | `docker compose config` on `deploy/compose.yml` and on every rendered per-user file; an architecture test over the **resolved** configuration asserting the only published port is the gated listener on loopback, that every `net-<user>` is `internal`, and that no service lacks a restart policy; and, on the box, an integration test that inspects the **actual** port bindings of running containers and that a Firefox container cannot reach the internet or the host except through its proxy. The rule is scoped to Compose: SRC-Nginx legitimately listens on 443. | `internal/arch/compose_test.go`, `tests/box/ports_test.go`, `tests/box/isolation_test.go` |
| Repository shape | a check that no `*.py` exists in the product tree and no file under `deploy/` references `nginx -s reload` or `systemctl` outside `init.sh` | `scripts/check.sh` |
| Platform definitions | strict decoding of every `platforms/*/platform.json` against fixtures | `internal/platform` tests |

**Where a check lives.** A pure grep, a regular expression over file contents with a
record behind it, is a frontmatter `checks` entry on that record, run by `adg lean
check`. Anything that needs a command's output, the import graph or the type system
is a test or a linter rule, named by path in the record's `applies_to`, so that
editing the test is editing the rule. `scripts/check.sh` runs `adg lean index --root
.` and `adg lean check` as one of its layers and fails loudly when `adg` is missing;
it is the first implementation task, before `cmd/` exists.

ADRs (`docs/decisions/`, lean records through `adg`) cover what the layers cannot:
boundaries an editor could move while every check still passes, orderings, and the
reasons behind a rule. A record whose rule has an automated consequence names the
test or lint that holds it, so an editor who trips the check finds the rule, and an
editor who reads the rule finds the check. A record is never a substitute for a check
that could exist; when a check can exist and does not yet, the plan task that writes
the code writes the check.

## 11. Left to the builder

- Go module layout (`cmd/desk`, `cmd/deskproxy`, `internal/...`), the HTTP router,
  template engine for the pages, and the noVNC websocket relay implementation.
- The init script's exact `PLUGIN_PARAMETERS` parsing (jq is present from SRC-CO).
- The Docker client calls versus `docker compose` subprocess for per-user pairs
  (either is acceptable; the client library is preferred).
- Test fixtures for the capture proxy (recorded TikTok responses from the spike).
- The Playwright harness, if used, and where the SRAM storage state lives.
- The catalog item record, snag list and README structure, following transcribe's.
