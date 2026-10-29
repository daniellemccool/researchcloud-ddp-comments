# Sandbox log — desk core, Tier 3 (Task 14)

Box: `checkjefeedzee.onlinehate.src.surf-hosted.nl` (Ubuntu 24.04.4, Python 3.12.3, Docker Compose 2.40.3).
All commands run from the laptop as `ssh -F ~/.config/claude-ssh/config desk '<cmd>'` unless marked
"on the box". Plan: docs/superpowers/plans/2026-10-29-desk-core.md, Task 14.

## Inventory (2026-10-29 15:35 UTC)

| Need | Present |
|---|---|
| docker + compose v2 | yes (2.40.3) |
| nginx, systemctl, rsync, curl, jq, ss, git, unzip, tar | yes |
| python3 >= 3.12 with sqlite3/fcntl/tarfile/zipfile | yes (3.12.3) |
| sqlite3 CLI | no (not needed; the Python module is used) |
| 4CAT checkout `/home/dmccool/4cat` with `docker-compose_public_ip.yml` | yes |
| images `jlesage/firefox:v26.08.3`, `mitmproxy/mitmproxy:12.2.3`, `digitalmethodsinitiative/4cat:latest`, `postgres:17`, `memcached:alpine` | yes |

Nothing had to be installed for the desk code itself. A fresh box needs the catalog item's
docker/nginx/4CAT, then `scripts/sandbox/push.sh`; the `prereqs` stage of `install.sh` (R45)
asserts the list above and pre-pulls the two desk images.

## Baseline before the install (15:31 UTC)

- `sessions.tsv`: six two-column rows (dmccool 1, riwema 2, ivanderveg 3, lsabajo 4, jterbraak 5, myenga 6).
- Running: `desk-mitm-<u>` for all six, `desk-firefox-dmccool`, `4cat_frontend/backend/db/memcached`.
- Networks: `4cat_default`, `desk_net-<u>` x6. No `desk-fourcat` yet.
- Old spike stub: `/data/onlinehate_zeeschuimer/desk/desk_stub.py` on 127.0.0.1:8110 via `desk-stub.service`.
- `/usr/local/bin` empty; `/opt/ddp-comments` absent.
- `/etc/nginx/app-location-conf.d/`: `authentication.conf`, `desk.conf`, `fourcat.conf` (D11: authentication.conf never edited).

## Step 3: push + install

Run 1 (15:33 UTC), code at `4d2214a`. push.sh's three stages were run as bare `ssh`/`scp` calls
(the sandbox excludes only bare `ssh`/`scp` lines; the tree was staged with `git archive HEAD`).

```
== code: /tmp/ddp-comments-push -> /opt/ddp-comments
== bin symlinks in /usr/local/bin
== docker network desk-fourcat
== 4CAT frontend on desk-fourcat
wrote /home/dmccool/4cat/docker-compose.desk.yml
 Container 4cat_frontend  Recreated / Started
== desk.json
wrote /data/onlinehate_zeeschuimer/desk/desk.json; migrated pool.csv
== render (compose, policies, nginx -t and reload)
TypeError: chown() got an unexpected keyword argument 'follow_symlinks'   (render.py:257, _chown_tree)
install-exit=1
```

Result: FAILED at render. `shutil.chown(follow_symlinks=)` is Python 3.13+; the box has 3.12.3.
nginx untouched (`nginx -t` ok), old stub still active, desk.json written with
`template_path: null`, pool.csv migrated to `id,kind,bucket,note`. Fix: Task E
(`os.lchown`, suite runs under 3.12 locally).

Suite on the box under 3.12 (`cd /opt/ddp-comments && python3 -m unittest discover -s tests`):
320/321; the one failure (`test_page_waits_for_screen_before_loading_it`) assumed port 5801 free,
which `desk-firefox-dmccool` occupies. Fixed in Task E (test binds its own port).

Run 2 (15:48 UTC), code at `a01cce2` (Task E). `install-exit=0`.

```
== 4CAT frontend on desk-fourcat        Container 4cat_frontend  Running   (no recreate: converged)
== desk.json                            present, left alone
== render                               nginx -t ok; wrote firefox/policy/<u>/policies.json x6,
                                        state/firefox/<u>/profile/chrome/userContent.css x6, platform,
                                        nginx/desk-map.conf desk.conf fourcat.conf, docker-compose.yml
== desk-stub.service                    desk-stub: active
== sessions                             6 live rows (dmccool 1 running; the other five stopped), all "at registry"
```

Expected per the brief: stage headers, nginx ok, stub active, six live rows — met. The rows show
"at registry" because the new provisioner has not run for them yet; they advance on their next visit
(their `state/firefox/<u>` dirs exist, so the template step is skipped for them).
Containers after the run: unchanged apart from `4cat_frontend` (recreated in run 1 onto `desk-fourcat`).

After the install, no visit had reached the new stub yet (`state/access.ndjson` ends before the
install; no `state/users/*/provision.json`, no `secrets/`), so all six rows stay "at registry"
until their next visit. Box clock: local time is UTC+1 (file mtimes vs `date -u`).

## Step 4: template build

`sudo /opt/ddp-comments/scripts/sandbox/template-session.sh up` (15:42 UTC): generated
`state/firefox/_template/compose.yml` (jlesage/firefox:v26.08.3, `/config` = the empty `_template`
dir, dmccool's mitm CA mounted read-only, `firefox/policy/_template` mounted as
`/usr/lib/firefox/distribution`, bound to 127.0.0.1:5999, on `desk_net-dmccool`). Firefox 151.0.3
started; the policy carried `Proxy` (mitm:8080, locked), `Certificates`, `ExtensionSettings`
force-installing Zeeschuimer 1.14.2 from GitHub through the proxy. Within 60 s
`profile/extensions/{677bb78c-...}.xpi` (5.2 MB) existed.

Clicks (Danielle, via `ssh -L 5999:127.0.0.1:5999 desk` from the laptop): tiktok.com consent
accepted with the modules off (the feed autoplays videos; that is fine, nothing is captured while
the modules are off and the release verifies the item store is empty); Zeeschuimer popup: modules
`tiktok` and `tiktok-comments` on, 4CAT URL `https://fourcat.desk`.

`template-session.sh release` (15:50 UTC): `compose down` stopped and removed `desk-firefox-tpl`,
then `desk-template release` refused: `profile is locked: stop the Firefox container first`.
`profile/lock -> 172.18.0.4:+1360` and `.parentlock` had survived the stop (Firefox did not get a
clean exit under docker's default 10 s grace). `prefs.js` and `sessionstore.jsonlz4` were written
at shutdown. Fix: Task F (stop with `-t 60`, confirm the container is gone, then clear the stale
lock files; `desk-template` stays strict).

Run 3 of the install (15:54 UTC, code at `89e360a` with Task F): `install-exit=0`, stub active.
`template-session.sh release` attempt 2: the stale lock was cleared
(`removed stale Firefox profile lock (lock, .parentlock)`), then the verifier refused:
`zeeschuimer-items has upload history`. Actual object-store counts (`idb_databases`):

| database | items | uploads | nav | settings |
|---|---|---|---|---|
| `zeeschuimer-items` | 0 | 0 | 2 | 1 |
| `webExtensions-storage-local` | 4 rows of `storage-local-data` | | | |

`nav` is Zeeschuimer's navigation record, written on the tiktok.com page loads the consent step
needs. Nothing was captured or uploaded, so the recipe held; the check was too coarse. Ruling R47 /
Task G (`76795da`): the sanitiser empties `nav` before the rebuild and verification requires
items 0, uploads 0, nav 0, settings >= 1. Task G is committed but not yet reviewed or pushed to the
box; the `_template` profile is still on disk for the next release attempt.

## Paused here (17:05 UTC)

Danielle stopped the plan to re-plan with the box as the design environment (see the ledger).
Box state at the pause: `/opt/ddp-comments` at `89e360a`, new stub active on 8110, six live rows
"at registry", `desk.json` `template_path: null`, `4cat_frontend` on `desk-fourcat`,
`desk-firefox-tpl` removed, `state/firefox/_template/` left in place, `/tmp/ddp-comments-push`
present. Not done: template release, logins, Tier 3 items 1–8.
