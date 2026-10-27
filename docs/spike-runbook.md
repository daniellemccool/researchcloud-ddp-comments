# Comment-desk spike — runbook

Goal: answer S1–S5 of `../comment-desk-spike-plan.md` on a throwaway workspace in the
**OnlineHate** CO (`onlinehate`, SRAM 7889). Nothing here is the product; it is the evidence
for building it. Portal steps are Danielle's; the commands are pasted on the workspace.

## 0. Prepare (portal, Danielle) — ~20 min

1. In the OnlineHate CO create a small storage volume (10 GB) named `desk-spike`.
2. Assemble a dev catalog item (same recipe as the inspector's dev item, `ddp-inspector/docs/sandbox-facts-2026-07-29.md`):
   `SRC-OS → SRC-CO → SRC-Nginx → SRC-External Docker Compose` (or SRC-External plugin with no externals — we run docker by hand in the spike). Ubuntu 24.04, flavour **4 core – 16 GB**. Firewall defaults (22/80/443).
3. Launch it in OnlineHate, attach `desk-spike`. Note the FQDN: `<name>.onlinehate.src.surf-hosted.nl`.
4. Add one non-admin CO member you can test 403/login with (or use a second SRAM identity).

## 1. Copy the spike and start the containers — S1/S2 setup

Known from the first run (2026-09-07): create the state dirs owned by uid 1000 or the mitmproxy
container crash-loops trying to adopt your SRAM uid (`sudo chown -R 1000:1000 state/mitm-1 state/captures`);
Firefox must be (re)started *after* the CA exists; the signed Zeeschuimer xpi's add-on ID is
`{677bb78c-7168-4aa5-8e9b-32e9f26c6157}` (not the source manifest's), which `policies.json` now uses.

```sh
# from the laptop
scp -r ~/projects/tiktok-harassment/comments/spike <you>@<ip>:~/spike
# on the workspace
cd ~/spike && mkdir -p state/mitm-1 state/firefox-1 state/captures
sudo docker compose pull && sudo docker compose up -d mitm      # generates the CA in state/mitm-1
ls state/mitm-1                                                 # expect mitmproxy-ca-cert.pem
sudo docker compose up -d firefox
sudo docker exec desk-firefox-1 ls /usr/lib/firefox/distribution # expect policies.json (bind-mounted)
sudo docker logs --tail 50 desk-firefox-1
```

If `policies.json` is not where Alpine's Firefox looks, find it with
`sudo docker exec desk-firefox-1 sh -c 'ls -d /usr/lib/firefox* /opt/firefox* 2>/dev/null'` and
fix the mount path in `docker-compose.yml`.

## 2. Gate it behind SRC-Nginx — S1

```sh
sudo cp nginx/websocket-map.conf /etc/nginx/conf.d/websocket-map.conf
sudo cp nginx/desk-spike.conf   /etc/nginx/app-location-conf.d/desk-spike.conf
sudo nginx -t && sudo systemctl reload nginx
```

Then in a laptop browser: `https://<fqdn>/desk/s/1/` → SRAM login → Firefox appears.
Checks: (a) a non-member gets 403; (b) TikTok home loads *through the proxy* (padlock fine,
issuer = mitmproxy — that proves the CA policy worked); (c) the Zeeschuimer Z icon is present
(force-installed by policy); (d) scroll a comment panel for a minute and judge the feel.

## 3. Capture — S2

Open three videos from `selection.csv` in the streamed Firefox by hand, open comments, scroll.

```sh
ls -R state/captures
tail -3 state/captures/spike/$(date -u +%F)/api-calls.ndjson
wc -l state/captures/spike/$(date -u +%F)/session-1.ndjson
```

Record for the plan: how many comments loaded before "Log in to see more" per video, whether
any captcha/"visiting too frequently" appears from the SRC address, and compare the same three
videos from a laptop Firefox. Repeat with the 19,500-comment row (`7664240723646057735`).

## 4. Open a URL from outside — S3

Verified 2026-09-07. Three things were needed: no `-u` (Docker 29's runc refuses the image's
`/etc/passwd` symlink — drop privileges inside with `su-exec` instead), the running instance's
D-Bus session address (Firefox hands URLs to an existing instance over D-Bus), and the same
profile path. Values are stable across restarts of this image:

```sh
sudo docker exec -e DISPLAY=:0 -e DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus.base -e HOME=/config \
  -e XDG_RUNTIME_DIR=/tmp/run/user/app desk-firefox-1 su-exec 1000:1000 \
  /usr/lib/firefox/firefox --profile /config/profile --new-tab "https://www.tiktok.com/@x/video/7199717060027452678/"
```

`desk_stub.py` uses exactly this. The stub must run with access to the Docker socket (`sudo`, or
a user in the `docker` group).

Then the stub desk (sidebar + streamed Firefox in one page):

```sh
sudo apt-get install -y python3 >/dev/null; nohup python3 desk_stub.py --worklist selection.csv --port 8110 > state/desk-stub.log 2>&1 &
```

Browse `https://<fqdn>/desk/` → click a row → the video should open in the pane on the right;
`state/desk-log.ndjson` records `open`/`problem` with `REMOTE_USER`.

Observed 2026-09-07: Zeeschuimer's per-dataset capture switches default to OFF inside the
contained browser too. Its settings live in extension storage within `/config`, so the product
clones a pre-configured profile (both TikTok rows on, Z pinned, Remote Resizing) as the template
for each new session. The mitm capture does not depend on Zeeschuimer at all.

## 5. Cost — S4

```sh
sudo docker stats --no-stream
```

With one video playing (muted). Then duplicate the two services as `-2`, `-3` (copy the service
blocks, ports 5801/5802, `DESK_SESSION=2/3`, state dirs `-2/-3`) and read `docker stats` again.

## 6. 4CAT as sink — S5

```sh
git clone --branch surf https://github.com/digitalmethodsinitiative/4cat.git ~/4cat && cd ~/4cat
cp .env-example .env   # set PUBLIC_PORT=8080, a POSTGRES_PASSWORD, SERVER_NAME=<fqdn>
sudo docker compose -f docker-compose_public_ip.yml up -d
```

Add to `/etc/nginx/app-location-conf.d/desk-spike.conf` a gated `location / { … proxy_pass http://127.0.0.1:8080; }`
(4CAT expects to live at the root; the desk stays under `/desk/`), reload nginx, create the 4CAT
admin user per its README, then: in the streamed Firefox open Zeeschuimer → set 4CAT URL to
`https://<fqdn>` → after capturing a video press **to 4CAT** on the *TikTok (comments)* row.
Also try the proxy capture route: convert `session-1.ndjson` to Zeeschuimer's item shape and POST it
to `/api/import-dataset/` with `X-Zeeschuimer-Platform: tiktok-comments` (curl, with the browser's
cookie) — a dataset should appear.

Write down: does 4CAT know who uploaded (its own user) — attribution otherwise stays in the desk.

Observed 2026-09-07: the `surf` branch ships a committed `.env` (no `.env-example`); add
`SERVER_NAME=<fqdn>` and replace the default `POSTGRES_PASSWORD`. Admin account is created on the
first page load, not by a helper script. Nginx snippet used: gated `location /` → `127.0.0.1:8080`
with `client_max_body_size 512m`. Zeeschuimer → 4CAT upload through the SRAM gate works when the
same browser holds the SRAM session.

## 7. Tear down

Pause/delete the workspace; keep `state/captures` and your notes. Fill the S1–S5 exit-criteria
table in `../comment-desk-spike-plan.md` with what was observed.
