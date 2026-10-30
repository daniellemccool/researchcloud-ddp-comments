# Box inventory — checkjefeedzee.onlinehate.src.surf-hosted.nl

Read-only inventory taken 2026-10-29 (UTC). All commands run as `dmccool` with passwordless `sudo` used only for reading. No packages were installed/removed, no services started/stopped, no files edited outside `/home/dmccool/box-inventory/`.

Secrets policy: `co_token` values from `/etc/rsc/workspace.json` and `/etc/rsc/storage.json`, and every `*PASSWORD*`/`*TOKEN*`/`*SECRET*`/`*KEY*` value in any `.env` or compose file, are replaced with `REDACTED`. No `secrets` directory contents are listed.

---

## 1. Launch baseline

**Commands:** `date -u; uptime; stat /; last -F; ls -la /var/log/cloud-init* /var/log/installer /var/log/apt/; sudo head/grep /var/log/cloud-init.log; zcat /var/log/apt/history.log.1.gz; head /var/log/apt/history.log; sudo ls -laR /etc/rsc/; sudo cat /etc/rsc/workspace.json; sudo grep -rl rsc /etc/systemd/system /lib/systemd/system; sudo crontab -l; sudo find /etc/nginx -type f -printf ...`

**Timeline (all times as reported; cloud-init logs are UTC, `last` is local CEST):**

- `stat /` birth time: **2026-08-26 14:58:21 +0200** — the base image filesystem was created Aug 26, 2026.
- Earliest apt transaction (`/var/log/apt/history.log.1.gz`): `Start-Date: 2026-08-26 12:58:49`, `apt-get install -qqy grub-pc shim-signed` — image build.
- `wtmp begins Sun Sep 6 03:16:29 2026`; earliest `last` entry: boot `6.8.0-138-generic` Sep 6 03:16 (base image build boot, user `ubuntu`).
- dist-upgrade Sep 6 01:17 and installs Sep 7 22:05–22:24 (see below).
- **Workspace first boot: Mon 2026-09-07 22:04 CEST (20:04 UTC)** — `last`: `reboot system boot 6.8.0-139-generi Mon Sep 7 22:04:11 2026`; cloud-init: `Cloud-init v. 26.1-0ubuntu1~24.04.1 running 'init-local' at Mon, 07 Sep 2026 20:04:13 +0000`, datasource `DataSourceOpenStackLocal`, hostname set to `checkjefeedzee-onlinehate-src-surf-hosted-nl`. This is the boot on which the SRC plugins ran (artifacts stamped 22:05–22:24 CEST the same evening).
- Later reboots of the same instance: Wed 2026-09-16 10:10, Tue 2026-10-27 10:24:33 (current boot; `uptime` at inventory time: `up 2 days, 7:36`). On the Oct 27 boot cloud-init logged `Instance link already exists, not re-loading configuration, instance id and datasource have not changed` — i.e. this is a reboot of the same workspace instance, not a fresh launch; `/etc/rsc/workspace.json` still has its Sep 7 22:06 mtime.
- `/var/log/installer` does not exist (no subiquity install; image-based VM).
- Current apt history (`/var/log/apt/history.log`, rotated Oct 28 06:40) contains only `unattended-upgrade` entries from 2026-10-28 06:36 onward (46 transactions, no `Requested-By:` lines).

**SRC components that ran (inferred from file mtimes clustering on 2026-09-07 22:05–22:24 CEST, i.e. the first-boot plugin window):**

- **SRC-OS (base OS image + hardening):** apt installs at `2026-09-07 22:05:13` by `ubuntu`: `apt-transport-https=2.8.3 python3-pip=24.0+dfsg-1ubuntu1.3 fail2ban=1.0.2-3ubuntu0.1 xterm=390-1ubuntu3` plus automatic build-essential/gcc/etc. `fail2ban.service` enabled (symlink stamped Sep 7 22:05).
- **SRC-CO (collaboration/CO plugin):** created `/etc/rsc/` (dir birth 2026-09-07 22:05:47) with `workspace.json` (co_token REDACTED), `storage.json` (co_token REDACTED), `cron_user.sh`, `cron_webdav.sh`, `check_fs_usage.sh`, `disk_full_template`, `setup_webdav.sh`, `totp_login.sh`, `webdav-mountnames` (`researchdrive`, `webdav`), `managedusers/` (dmccool, dmccool2, ivanderveg, jterbraak, lsabajo, myenga, riwema — all stamped Oct 29 18:01, refreshed every minute by cron), `managedgroups`. Installed root crontab (`/var/spool/cron/crontabs/root`, Sep 7 22:14):
  ```
  #Ansible: Add new users
  */1 * * * * /etc/rsc/cron_user.sh >> /var/log/src.txt 2>&1
  #Ansible: check new users and mount webdav
  */1 * * * * /etc/rsc/cron_webdav.sh >> /var/log/src_webdav.log 2>&1
  #Ansible: Check if root FS is full
  */15 * * * * /etc/rsc/check_fs_usage.sh
  34 2,8,14,20 * * * "/root/.acme.sh"/acme.sh --cron --home "/root/.acme.sh" > /dev/null
  ```
  Created `rsc-disk-handler.service` (see §3), `/opt/rsc-utilities/` (`disk_handler_rsc.sh`, `format_rsc_disk.sh`, `automount_rsc_disk.sh`, `mount-recovery-disk.sh`), `/data/` and `/data/datasets` (Sep 7 22:05), the fstab entry for the storage volume, and per-user home symlinks `~/data -> /data` and `~/local-share -> /local-share` (Sep 7 22:05). It also created the CO users and `src_*`/`rsc_*` groups (§7) via `cron_user.sh` against the CO API.
- **SRC-Nginx:** apt install `2026-09-07 22:14:23` by `ubuntu`: `nginx=1.24.0-2ubuntu7.17` (+`nginx-common`); wrote `/etc/nginx/nginx.conf`, `/etc/nginx/conf.d/tls_main.conf`, `/etc/nginx/conf.d/map_upgrade.conf`, `/etc/nginx/app-location-conf.d/authentication.conf` (all mtime Sep 7 22:14), `/var/www/html/index.nginx-debian.html`; installed acme.sh to `/root/.acme.sh` (22:14:34) and obtained a Let's Encrypt cert into `/etc/ssl/acme/checkjefeedzee.onlinehate.src.surf-hosted.nl/` (22:14). `tls_main.conf` serves 80→443 redirect plus a 443 ssl server for `checkjefeedzee.onlinehate.src.surf-hosted.nl` with `include /etc/nginx/app-location-conf.d/*.conf;`. `authentication.conf` (first 5 lines):
  ```
  location @custom_401 {
    internal;

      if ($cookie_redirecturi = "") {
        add_header Set-Cookie "redirecturi=$request_uri;path=/oauth2_callback;Max-Age=300;";
  ```
  `map_upgrade.conf` is the standard `map $http_upgrade $connection_upgrade { default upgrade; '' close; }`.
- **SRC-External (inferred):** the WebDAV/researchdrive mounting machinery — `/etc/rsc/setup_webdav.sh`, `cron_webdav.sh`, `webdav-mountnames` (`researchdrive`, `webdav`), `storage.json` with `storage_api_endpoint` — matches an "external storage" component. Inferred because these files are SRC-stamped but are about mounting external (SURF Research Drive / WebDAV) storage rather than OS/nginx/CO-user management.
- No `/var/log/rsc*` files exist; the only "rsc" logs are syslog lines for `rsc-disk-handler.service` and the root cron jobs writing `/var/log/src.txt` (8.7 MB) and `/var/log/src_webdav.log` (0 bytes). No ansible logs; the root crontab comments say `#Ansible:` but the scripts are plain bash+curl+jq against the CO API.

---

## 2. Packages added after launch

**Commands:** `zcat /var/log/apt/history.log.1.gz; cat /var/log/apt/history.log; grep Requested-By /var/log/apt/history.log; ls /etc/apt/sources.list.d/; cat sources; dpkg -l | grep -iE 'docker|containerd|runc|compose'; sudo docker version; sudo docker compose version; ls -la /home/dmccool/.local/bin; ls /home/dmccool/.local/share/uv/tools; snap list; which pipx`

**apt history, all rotations:** `/var/log/apt/history.log.1.gz` covers 2026-08-26 → 2026-09-07 22:24:04; `/var/log/apt/history.log` covers 2026-10-28 06:36 → now. There is **no apt transaction at all between 2026-09-07 22:24:04 and 2026-10-28 06:36:25**. After launch day the only entries are 46 `unattended-upgrade` runs on 2026-10-28 (openssh, systemd, glibc, linux-6.8.0-147 kernel, etc.) — no manual installs or removes.

Launch-day installs (2026-09-07, for reference — these are part of the baseline, not "after launch"):

- 22:05:13 by `ubuntu`: `apt-transport-https python3-pip fail2ban xterm` (+ build-essential etc.) — SRC-OS.
- 22:14:23 by `ubuntu`: `nginx=1.24.0-2ubuntu7.17` — SRC-Nginx.
- **22:23:56 by `dmccool` (uid 5050): `apt-get install -y docker.io docker-compose-v2`** — installed `docker.io:amd64 29.1.3-0ubuntu3~24.04.2`, `docker-compose-v2:amd64 2.40.3+ds1-0ubuntu1~24.04.1`, plus automatic `containerd 2.2.1-0ubuntu1~24.04.3`, `runc 1.3.4-0ubuntu1~24.04.1`, `bridge-utils`, `pigz`, `dnsmasq-base`, `dns-root-data`, `ubuntu-fan`.

**Repositories:** `/etc/apt/sources.list.d/` contains only `ubuntu.sources` (nova.clouds.archive.ubuntu.com, written by cloud-init) and `fallback-mirrors.list` (nl.archive.ubuntu.com). **There is no Docker repo** — no `download.docker.com` list, no docker keyring in `/etc/apt/keyrings` or `/usr/share/keyrings`.

**Docker install method (stated plainly):** Docker was installed from **Ubuntu's own archive** as the `docker.io` package (version **29.1.3-0ubuntu3~24.04.2**, engine API 1.52, containerd 2.2.1, runc 1.3.4, docker-init 0.19.0), and Docker Compose as the Ubuntu package **`docker-compose-v2` 2.40.3+ds1-0ubuntu1~24.04.1** (`Docker Compose version v2.40.3+ds1-0ubuntu1~24.04.1`). NOT docker-ce, NOT the convenience script, NOT snap. `~/.bash_history` confirms the hand-run command `sudo apt-get install -y docker.io docker-compose-v2` on launch evening (Sep 7 22:23). `dpkg -l` shows exactly: `containerd 2.2.1-0ubuntu1~24.04.3`, `docker-compose-v2 2.40.3+ds1-0ubuntu1~24.04.1`, `docker.io 29.1.3-0ubuntu3~24.04.2`, `runc 1.3.4-0ubuntu1~24.04.1`.

**`~/.local/bin`** (all dmccool-owned; the `vibe` entries were created today 2026-10-29 17:03 by the Vibe CLI itself):

```
-rw-rw-r--  env, env.fish                      (uv env shims, Oct 29 17:03)
lrwxrwxrwx  python3.14 -> ~/.local/share/uv/python/cpython-3.14.6-linux-x86_64-gnu/bin/python3.14
-rwxr-xr-x  uv, uvx                             (binaries dated Jun 30 2026; uv installer)
lrwxrwxrwx  vibe, vibe-acp, vibe-app-server -> ~/.local/share/uv/tools/mistral-vibe/bin/...  (Oct 29 17:03)
```

So: **uv/uvx** (standalone binary, Jun 30 2026 build) manages **Python 3.14.6** (`~/.local/share/uv/python/cpython-3.14.6-linux-x86_64-gnu`) and one **uv tool: `mistral-vibe`** (`~/.local/share/uv/tools/mistral-vibe`) — i.e. the Vibe CLI. `pipx` is not installed. `snap list` is **empty** (snapd is installed, `/snap` contains only a README — no snaps).

---

## 3. Services and units not from packages

**Commands:** `sudo ls -la /etc/systemd/system/; sudo grep -rl rsc /etc/systemd/system /lib/systemd/system /usr/lib/systemd/system; sudo systemctl status desk-stub.service; sudo crontab -l; crontab -l; sudo ls /etc/cron.d /var/spool/cron/crontabs`

Regular files in `/etc/systemd/system` (everything else there is a symlink into /lib or /usr/lib or a stock `.wants` dir):

- `/etc/systemd/system/rsc-disk-handler.service` (Sep 7 22:05, **from SRC-CO**, enabled) — runs at boot, `Type=forking`, `ExecStart=/bin/bash /opt/rsc-utilities/disk_handler_rsc.sh`, `WantedBy=multi-user.target`. Status: loaded, ran at the Oct 27 boot (`Started rsc-disk-handler.service ... Deactivated successfully` in syslog) — it formats/mounts the SRC storage volume.
- `/etc/systemd/system/desk-stub.service` (Oct 29 16:41, **hand-made**, enabled, `active (running) since Thu 2026-10-29 16:54:39 CET`, Main PID python3):
  ```
  # /etc/systemd/system/desk-stub.service — the desk page and queue for every user's session.
  # Root for the Docker socket (it execs into desk-firefox-<user>); listens on 127.0.0.1 only.
  # Everything but the desk root comes from <desk root>/desk.json.
  [Unit]
  Description=ddp-comments desk
  After=docker.service
  RequiresMountsFor=/data/onlinehate_zeeschuimer
  [Service]
  ExecStart=/usr/bin/python3 /opt/ddp-comments/desk/desk_stub.py --desk-root /data/onlinehate_zeeschuimer/desk
  Restart=always
  [Install]
  WantedBy=multi-user.target
  ```

Cron: root crontab = the four SRC/acme lines listed in §1 (no hand-added entries). `dmccool` has **no** crontab. `/etc/cron.d` contains only stock `e2scrub_all`, `sysstat`, `.placeholder`; `/etc/cron.daily` stock only.

---

## 4. Files added outside home directories

**Commands:** `sudo find /opt -maxdepth 2 -printf ...; sudo ls -la /usr/local/bin /usr/local/sbin; sudo find /etc/nginx -type f -printf ...; head of each conf.d / app-location-conf.d file; ls -la /etc/docker; cat /etc/docker/daemon.json; lsblk; df -h; findmnt; cat /etc/fstab; sudo find /data -maxdepth 2 -printf ...`

**/opt (depth 2, owners/mtimes):**

```
2026-09-07 22:05 root:root  /opt/rsc-utilities/            (disk_handler_rsc.sh, format_rsc_disk.sh, automount_rsc_disk.sh, mount-recovery-disk.sh)  — SRC
2026-09-07 22:24 root:root  /opt/containerd/{bin,lib}     — docker.io package
2026-10-29 16:54 root:root  /opt/ddp-comments/            (bin, desk, docs, platforms, scripts, tests, README.md, .gitignore)  — hand-made desk
```

**/usr/local/bin** (dir mtime Oct 29 16:54): `desk-admin -> /opt/ddp-comments/bin/desk-admin`, `desk-sessions -> /opt/ddp-comments/bin/desk-sessions`, `desk-template -> /opt/ddp-comments/bin/desk-template`. **/usr/local/sbin**: empty.

**/etc/nginx** — package files (2023-11-30) plus:

| file | mtime | origin |
|---|---|---|
| `/etc/nginx/nginx.conf` | 2026-09-07 22:14 | SRC-Nginx |
| `/etc/nginx/conf.d/tls_main.conf` | 2026-09-07 22:14 | SRC-Nginx (80→443 + 443 ssl server, acme cert, includes app-location-conf.d/*.conf) |
| `/etc/nginx/conf.d/map_upgrade.conf` | 2026-09-07 22:14 | SRC-Nginx (websocket upgrade map) |
| `/etc/nginx/app-location-conf.d/authentication.conf` | 2026-09-07 22:14 | SRC-Nginx (OAuth2 `@custom_401` / validate flow; first 5 lines in §1) |
| `/etc/nginx/conf.d/desk-map.conf` | 2026-10-29 16:54 | hand/desk ("Rendered by desklib.render. Do not edit"; maps `$src_roles` → `$desk_member`/`$desk_admin`, e.g. `src_co_students`, `src_co_admin`) |
| `/etc/nginx/conf.d/websocket-map.conf` | 2026-10-29 16:54 | hand/desk (duplicate of map_upgrade) |
| `/etc/nginx/app-location-conf.d/desk.conf` | 2026-10-29 16:54 | hand/desk (`location /desk/` with `auth_request /validate`, `auth_request_set $username/$src_roles`, proxy identity headers) |
| `/etc/nginx/app-location-conf.d/fourcat.conf` | 2026-10-29 16:54 | hand/desk (`location /` — 4CAT UI, admins only, same auth_request pattern) |

`/etc/nginx/sites-enabled/` is empty. `nginx.conf` includes `conf.d/*.conf` and `sites-enabled/*`.

**/etc/docker:** directory exists (Apr 29 2026, from the docker.io package) but is **empty — no daemon.json**.

**Mounts:**

```
lsblk:  vda 40G disk  ├─vda1 39G /          (boot disk, ext4)
                        ├─vda14 4M, vda15 106M /boot/efi, vda16 913M /boot
        vdb 10G disk  └─vdb1 10G /data/onlinehate_zeeschuimer   (xfs)
df -h:  /dev/vda1  38G 14G 25G 36% /      ;  /dev/vdb1 10G 2.0G 8.0G 20% /data/onlinehate_zeeschuimer
fstab:  LABEL=cloudimg-rootfs / ext4 ... ; LABEL=BOOT /boot ; LABEL=UEFI /boot/efi
        UUID=a287b79d-5c9d-4fdd-a348-066e43b069d8 /data/onlinehate_zeeschuimer xfs defaults,nofail,x-mount.mkdir=777 0 0
```

**`/dev/vda1` (40G vda) is the boot disk; `/dev/vdb1` (10G, label `onlinehate_z`, partlabel `onlinehate_zeeschuimer`) mounted at `/data/onlinehate_zeeschuimer` is the SRC storage volume** (mounted by `rsc-disk-handler.service` via `/opt/rsc-utilities/disk_handler_rsc.sh`; the fstab UUID entry was added by SRC-CO).

**/data top two levels:**

```
2026-09-07 22:05 root:root  /data
2026-09-07 22:05 root:root  /data/datasets                      — SRC-CO
2026-10-27 12:06 root:root  /data/onlinehate_zeeschuimer          — SRC volume mountpoint
2026-10-29 16:54 root:root  /data/onlinehate_zeeschuimer/desk     — hand-made desk root (desk.json, desk_stub.py, docker-compose.yml, firefox/, mitm/, nginx/, platform/, pool.csv, sessions.tsv, state/, templates/, admins.txt, .pool.lock, .render.lock)
```

---

## 5. Docker state

**Commands:** `sudo docker version; sudo docker compose version; sudo docker ps -a; sudo docker network ls; sudo docker volume ls; sudo docker images; sudo docker inspect <c> --format '{{ labels }}'`

- Engine: `docker.io` **29.1.3** (API 1.52, go1.24.4, commit 29.1.3-0ubuntu3~24.04.2), containerd 2.2.1, runc 1.3.4-0ubuntu1~24.04.1. Compose: **v2.40.3+ds1-0ubuntu1~24.04.1**.
- `docker ps -a`:

| container | image | status | ports |
|---|---|---|---|
| 4cat_frontend | digitalmethodsinitiative/4cat:latest | Up ~1h | 0.0.0.0:446→443, 0.0.0.0:8080→5000 |
| 4cat_backend | digitalmethodsinitiative/4cat:latest | Up 2 days | — |
| 4cat_db | postgres:17 | Up 2 days (healthy) | 5432/tcp |
| 4cat_memcached | memcached:alpine | Up 2 days (healthy) | 11211/tcp |
| desk-mitm-{dmccool,myenga,jterbraak,riwema,lsabajo,ivanderveg} | mitmproxy/mitmproxy:12.2.3 | Up 2 days | 8080-8081/tcp |
| desk-firefox-{dmccool,myenga,jterbraak,riwema} | jlesage/firefox:v26.08.3 | Exited (0) | — |
| desk-firefox-{ivanderveg,lsabajo} | jlesage/firefox:v26.08.3 | Created (never started) | — |

- Networks: `4cat_default`, `desk-fourcat`, `desk_net-{dmccool,ivanderveg,jterbraak,lsabajo,myenga,riwema}` (all bridge), plus `bridge`, `host`, `none`.
- Volumes (all driver `local`): `4cat_config`, `4cat_data`, `4cat_db`, `4cat_logs` — all created Sep 7 23:16 and all living under **`/var/lib/docker/volumes/` on the boot disk** (`/dev/vda1`), not on /data. (`4cat_data/_data` contains `datasets/`, `images/`.)
- Images: `digitalmethodsinitiative/4cat:latest` (5.37GB), `jlesage/firefox:v26.08.3` (1.19GB), `mitmproxy/mitmproxy:12.2.3` (382MB), `postgres:17` (645MB), `memcached:alpine` (20MB).
- Compose project labels (`com.docker.compose.project` | `project.config_files` | service):
  - `4cat` | `/home/dmccool/4cat/docker-compose_public_ip.yml,/home/dmccool/4cat/docker-compose.desk.yml` | frontend (the extra `docker-compose.desk.yml` attaches frontend to the external `desk-fourcat` network)
  - `4cat` | `/home/dmccool/4cat/docker-compose_public_ip.yml` | backend, db, memcached
  - `desk` | `/data/onlinehate_zeeschuimer/desk/docker-compose.yml` | mitm-<user>, firefox-<user> for each user

---

## 6. Home directory additions for dmccool

**Commands:** `ls -la /home/dmccool; du -sh /home/dmccool/* ...; cd /home/dmccool/4cat && git remote -v; git branch; git rev-parse HEAD; git status --porcelain; ls docker-compose*.yml; du -sh /home/*`

Top level of `/home/dmccool` (sizes from `du -sh`):

```
369M .cache          203M .local          148M 4cat          80M spike
2.7M .vibe           636K vibe-probe      156K ddp-comments  36K nginx-backup-20261027
28K .config  24K .terminfo  12K .ssh  12K .bash_history  4K box-inventory (+ stock dotfiles)
symlinks created by SRC-CO at launch (Sep 7 22:05):  data -> /data ;  local-share -> /local-share
```

**/home/dmccool/4cat** (148M, cloned by hand Sep 7 23:13 per `.bash_history`: `git clone --branch surf https://github.com/digitalmethodsinitiative/4cat.git ~/4cat`):

- remote: `https://github.com/digitalmethodsinitiative/4cat.git`; branch: **`surf`** (local); HEAD: `05d267da117ccfa14dbd8f8da5f608f2659f917b` ("remove server_name for surf", committed 2025-11-05).
- Working tree **dirty**: ` M .env` (hand-edited; `SERVER_NAME` appended, `POSTGRES_PASSWORD` randomized — values REDACTED) and `?? docker-compose.desk.yml` (untracked, **root-owned**, Oct 29 16:32, 105 bytes: attaches the frontend service to networks `[default, desk-fourcat]` with `desk-fourcat` external).
- Compose files present: `docker-compose.yml`, `docker-compose_build.yml`, `docker-compose_public_ip.yml` (all Sep 7 23:13, from the clone) and `docker-compose.desk.yml` (Oct 29 16:32, hand-made).

Other additions in the home: `spike/` (Sep 7 22:23–23:05 — the desk prototype: desk_stub.py, docker-compose.yml, firefox/, mitm/, nginx/, selection.csv, state/), `ddp-comments/` (Oct 27 12:02 — a copy of the desk code), `nginx-backup-20261027/` (Oct 27 12:16 — backup of the Sep 7 SRC nginx conf.d and app-location-conf.d), `vibe-probe/` (Oct 29 — Vibe CLI test artifacts).

**Other users' homes** (names and sizes only): `/home/ubuntu` 4.0K, `/home/lsabajo` 4.0K, `/home/ivanderveg` 4.0K, `/home/jterbraak` 4.0K, `/home/myenga` 4.0K, `/home/riwema` 4.0K, `/home/dmccool2` 4.0K — all essentially empty (SRC-CO created the accounts; the webdav mount points were never populated).

---

## 7. Users and groups

**Commands:** `getent passwd | awk -F: '$3>=1000'; getent group | grep -E '^src_|^rsc_'`

```
ubuntu:x:1000:1000:Ubuntu:/home/ubuntu:/bin/bash
lsabajo:x:1095:500002::/home/lsabajo:/usr/sbin/nologin
ivanderveg:x:3860:500003::/home/ivanderveg:/usr/sbin/nologin
dmccool:x:5050:500004::/home/dmccool:/bin/bash
riwema:x:7021:500009::/home/riwema:/usr/sbin/nologin
jterbraak:x:7063:500010::/home/jterbraak:/usr/sbin/nologin
myenga:x:7064:500011::/home/myenga:/usr/sbin/nologin
dmccool2:x:7085:500012::/home/dmccool2:/usr/sbin/nologin
(nobody:x:65534 also matches uid>=1000 filter)
```

Groups created by SRC-CO (`/etc/rsc/cron_user.sh` via the CO API; no password hashes or tokens shown):

```
rsc_co_202513:x:202513:lsabajo,ivanderveg,dmccool,riwema,jterbraak,myenga,dmccool2
src_co_developer:x:500005:dmccool
rsc_developers:x:500006:dmccool
src_co_admin:x:500007:dmccool
src_co_students:x:500008:dmccool
```

`/etc/rsc/managedgroups` additionally lists `src_co_wallet` and `src_ws_admin`, but those two groups do **not** exist in `getent group` (see §8).

---

## 8. Puzzles

1. **Docker was not installed "over the following weeks" and not from docker-ce.** apt history and `.bash_history` both show `sudo apt-get install -y docker.io docker-compose-v2` ran on **launch evening, 2026-09-07 22:23:56**, from Ubuntu noble (no download.docker.com repo exists). A fresh launch from SRC-OS/SRC-CO/SRC-Nginx would NOT have Docker — the catalog item must install `docker.io` + `docker-compose-v2` itself.
2. **No apt activity at all between 2026-09-07 22:24 and 2026-10-28 06:36** (history.log.1.gz ends at the docker install; history.log starts with unattended-upgrades). Everything added after launch day was file-based (git clone, /opt, /etc edits, docker images pulled), not package-based — so the catalog item cannot rely on apt logs to enumerate the additions.
3. **"nginx installed by hand" is contradicted**: the nginx *package* and all SRC plugin configs (`nginx.conf`, `tls_main.conf`, `map_upgrade.conf`, `authentication.conf`, acme.sh + LE cert) are stamped 2026-09-07 22:14, i.e. SRC-Nginx at launch. Only the four desk/4CAT conf files (`desk-map.conf`, `websocket-map.conf`, `desk.conf`, `fourcat.conf`, all Oct 29 16:54, "Rendered by desklib.render") are hand/desk-made. 4CAT itself was hand-cloned Sep 7 23:13 (branch `surf`), so "4CAT installed by hand" holds.
4. **This is not a fresh launch**: the Oct 27 boot is a reboot of the same instance (cloud-init: "instance id and datasource have not changed"; `/etc/rsc/workspace.json` still Sep 7 22:06; same IP/MAC as Sep 7). The Sep 7 22:04 boot is the true first boot of the workspace.
5. `/etc/rsc/managedgroups` lists `src_co_wallet` and `src_ws_admin`, but `getent group` has no such groups — half-done or stale CO role sync.
6. `desk-firefox-ivanderveg` and `desk-firefox-lsabajo` are `Created` but never started; four other `desk-firefox-*` containers are `Exited (0)`. The desk spawns per-user firefox+mitm containers on demand.
7. `4cat_frontend` publishes `0.0.0.0:8080→5000` and `0.0.0.0:446→443` directly, in addition to the nginx reverse proxy on 443 — the 8080/446 bindings are a second, direct exposure of 4CAT.
8. `/var/log/src.txt` (root cron output of `cron_user.sh`) has grown to 8.7 MB; `/var/log/src_webdav.log` is 0 bytes — the webdav cron has never logged anything.
9. `.bash_history` shows failed attempts `sudo apt get install vibe` / `sudo apt install vibe` (no matching apt history entry); the Vibe CLI actually present is the **uv-managed** `mistral-vibe` tool in `~/.local/bin` (installed today, Oct 29 17:03, by this very agent session) — not an apt package.
10. `/home/dmccool/4cat/docker-compose.desk.yml` is **root-owned** inside dmccool's checkout, and `.env` is modified with a randomized `POSTGRES_PASSWORD` (REDACTED) — the desk tooling writes into the 4CAT checkout as root.
11. `/etc/rsc/storage.json` contains a second `co_token` (REDACTED here) alongside the one in `workspace.json` — both are live-looking API tokens stored root-only; worth rotating when this box is rebuilt from a catalog item.
