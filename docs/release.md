# Release runbook

A release is a tag `vX.Y.Z` (or `vX.Y.Z-alpha.N`). The tag builds the images;
the template artifact is built on the box and uploaded by hand. The namespace
is `ghcr.io/daniellemccool/researchcloud-ddp-comments`.

## Steps

1. Build the template artifact on the box (when the template changed). First
   build the image once
   (`sudo docker build -f cmd/desk/Dockerfile -t desk:dev .`), then run the
   tool; the profile is a copy of a sanitisable Firefox profile owned by
   uid 1000. Example for TikTok with Zeeschuimer 1.14.3 (the version check
   refuses a label that differs from the extension's own version in the
   profile's `extensions.json`):

   ```
   sudo docker run --rm -u 1000:1000 -v /home/dmccool/template-work:/w -v /home/dmccool/ddp-comments/platforms:/platforms:ro desk:dev template release --profile /w/profile --platform tiktok --platforms-dir /platforms --zeeschuimer 1.14.3 --out /w/out
   ```

   Generic form: `--platform <platform> --zeeschuimer <zeeschuimer_version>`.
   It writes `/w/out/ddp-comments-template-<platform>-<zeeschuimer_version>.tar`
   and the sibling `.sha256` file, and prints the manifest JSON plus a
   `sha256 <hex>  <filename>` line. Copy both files to the local checkout.

   When the template did NOT change, skip the build: download the previous
   release's artifact and its checksum
   (`gh release download <previous tag> -p 'ddp-comments-template-*'`) and
   upload those same files under the new tag in step 4.

2. Bump the pins in the local checkout: `DESK_IMAGE` and `DESKPROXY_IMAGE` in
   `deploy/.env.example` to the new tag (`FOURCAT_IMAGE` only on a deliberate
   4CAT bump) and, when the artifact changed, the `zeeschuimer_version`,
   `sha256`, filename and build date in `platforms/<platform>/template.json`
   (step 1's values). Commit, then run `scripts/check-pins.sh vX.Y.Z` — it
   must exit 0.

3. Tag from the local checkout, on a commit that passed `scripts/check.sh`:

   ```
   git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z
   ```

   If the pin guard fails after the tag was pushed: nothing was published
   (no images were pushed and the `release` branch did not move), so delete
   the tag locally and remotely (`git tag -d vX.Y.Z && git push origin
   :refs/tags/vX.Y.Z`), fix the pins, and re-tag; or bump to the next tag.

4. Immediately create the GitHub release and upload the artifact and its
   checksum, from the local checkout (GitHub is not reached from the box):

   ```
   gh release create vX.Y.Z --title vX.Y.Z --notes "vX.Y.Z"
   gh release upload vX.Y.Z <tar> <tar>.sha256
   ```

   Every release needs the artifact uploaded under its own tag, even when the
   template did not change, because the init script derives the artifact
   download URL from the tag.

5. Wait for the `release` workflow in GitHub Actions. It checks the pins,
   pushes `desk:<tag>`, `deskproxy:<tag>` and mirrors `4cat:<4CAT tag>` and
   `firefox:<Firefox tag>` under
   the namespace, waits until the GitHub release carries the artifact named
   by every `platforms/*/template.json`, and only then fast-forwards the
   branch `release` to the tagged commit — the branch moves only once the
   workflow sees the artifact for every platform manifest, so a workspace
   launched from it can always fetch its template. The wait step also
   downloads each uploaded artifact and verifies its sha256 against the
   manifest's `template_sha256`, so a wrong upload fails the workflow
   instead of moving `release`. First release: make the
   packages public (section "First release only") before this check. Then
   verify anonymously, from a machine that is not logged in to GHCR:

   ```
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:vX.Y.Z
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/deskproxy:vX.Y.Z
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:v1.57
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/firefox:v26.08.3
   ```

   The 4CAT tag is currently v1.57 and the Firefox tag v26.08.3; they follow
   `FOURCAT_IMAGE` and `FIREFOX_IMAGE` in
   `deploy/.env.example`. Also verify that the branch moved to the tagged
   commit:

   ```
   gh api repos/daniellemccool/researchcloud-ddp-comments/branches/release --jq .commit.sha
   git rev-parse vX.Y.Z^{commit}
   ```

   The two shas must be equal.

## The catalog component

Its tag field is the branch `release`, a moving ref the workflow
fast-forwards, so a release never edits the component; the component
declares only the study parameters and `co_id`, `co_name`, `workspace_fqdn`;
image references and the template digest are read from the checked-out
release (ADR-0020). Component values reach a workspace at launch and at
reattach; a running workspace moves to a release with
`sudo /rsc/plugins/<uuid>/deploy/init.sh update <tag>` (`<uuid>` is the
plugin directory the platform created). `update` adopts the release's image
pins and refuses a release whose `template.json` differs from the
workspace's `desk.json` (reattach the workspace on that release instead;
installing a new template at update is a later plan's item).

A workspace on `v0.1.0-alpha.3` or earlier is recreated on the new release, not
updated with `init.sh update`: the layout changed (25 `desk.json` keys instead of
24, a fourth image, the `desk-egress` network option), and `update` runs the
installed copy's old script, which writes the old layout. `update` is for
releases from the next tag on. The desk root on the volume survives a recreate.

## Running desk: seats and the Docker floor

The desk stops a collector's pair (Firefox and proxy) once its page has reported
no input, or no screen has been open, for `desk_idle_seconds` (checked every
minute; `0` turns the idle stop off), and the collector's next visit starts it
again. Once `desk_max_running` collectors
have a running pair, every other collector who opens `/desk/` sees "The desk is
busy, try again in a few minutes" until a seat is freed. Check the pairs with
`sudo docker exec desk-desk-1 desk status` (first a seats line and a CO poll
line, then one line per registry row: user, index, `running` or `stopped`, and
`ready` or the provisioning step it is at),
and free a seat with `sudo docker exec desk-desk-1 desk stop <user>`. The
collector's next visit starts a fresh pair; the profile, captures and 4CAT
account stay on the volume.

The desk needs Docker Engine 28 or newer (API 1.48). It creates each pair
network with Docker's isolated gateway mode
(`com.docker.network.bridge.gateway_mode_ipv4=isolated`, ADR-0007), and
`desk serve` refuses to start on an older daemon, with an error naming the
requirement. A launch installs current docker-ce; check an existing host with
`docker version --format '{{.Server.Version}} {{.Server.APIVersion}}'`.

## Running desk: a platform-file fix between releases

A file under `platforms/<platform>/` that the desk reads at run time, such as
`userContent.css`, can reach a running workspace before the next release.
Commit and push it first, then run these on the workspace in bash, as a user
with sudo. The wrapper `/usr/local/bin/desk` holds the plugin directory's real
path:

```
REPO=$(sed -n 's/.*-f "\(.*\)\/deploy\/compose.yml".*/\1/p' /usr/local/bin/desk)
DR=$(sudo sed -n 's/^DESK_ROOT=//p' "$REPO/deploy/.env")
sudo curl -fsSL https://raw.githubusercontent.com/daniellemccool/researchcloud-ddp-comments/<commit>/platforms/<platform>/<file> -o "$REPO/platforms/<platform>/<file>"
sudo "$REPO/deploy/init.sh" apply
```

`apply` makes the desk copy `platforms/<platform>/` to `$DR/platform/`. New
collectors get the new file from there. A collector's profile takes
`userContent.css` only once, at their first visit, so for each name in
`sudo ls "$DR/state/firefox/"` also run
`sudo cp "$DR/platform/userContent.css" "$DR/state/firefox/<user>/profile/chrome/userContent.css"`
(copying onto the existing file keeps its owner, uid 1000, and its 0644 mode).
Firefox reads the stylesheet at start, so stop any running pair
(`sudo docker exec desk-desk-1 desk stop <user>`, or Stop on the admin page).

The workspace's checkout now differs from its release. Reattaching or updating
it to that same release brings the old file back; the next release from `main`
carries the fix.

## First release only

GHCR packages start private. In the GitHub package settings, set `desk`,
`deskproxy`, `4cat` and `firefox` to public. Workspaces pull anonymously, so a
release is not usable until this is done.

## The mirrors

The workflow reads `FOURCAT_IMAGE` and `FIREFOX_IMAGE` from
`deploy/.env.example` and mirrors those tags to `4cat:<tag>` and
`firefox:<tag>`. To change a pin, edit `deploy/.env.example`, tag a release,
and run `sudo /rsc/plugins/<uuid>/deploy/init.sh update <tag>` on each
workspace (`<uuid>` is the plugin directory the platform created): `update`
adopts the TAG from that release's `deploy/.env.example` and maps it onto the
GHCR mirror (`ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:<tag>`,
`ghcr.io/daniellemccool/researchcloud-ddp-comments/firefox:<tag>`),
writing that reference into `desk.json` and `.env`, so 4CAT's database
migrations run at that update. The `.env.example` values themselves name the
Docker Hub sources and are never used verbatim. Bump them deliberately,
not as part of an unrelated release.

## Snapshot retention

Every Zeeschuimer upload is a new 4CAT dataset holding the collector's whole
collection so far. Per collector and format (`tiktok-search`,
`tiktok-comments-search`) the desk keeps the newest two snapshots, any snapshot
no newer one supersedes (has at least as many rows), and both sides of every
drop in rows. The rest get an "expires on" date 7 days out, and 4CAT deletes
them then. A dataset with "keep" is never marked and, while marking is on,
loses a desk mark within 15 minutes. Set "keep" on 4CAT's Dataset bulk
management page; the "cancel
deletion" link on a dataset's own page answers 403 on this 4CAT. When a
snapshot expires, the analyses run on it go with it, while a merged or filtered
standalone copy stays. Filter, merge and refresh outputs are never touched.

The desk writes to 4CAT only while the file `state/retention-on` exists in the
desk root. Without it every pass is a dry run, and a release ships without
it.

### Before the update

1. In 4CAT's control panel, Settings → Data sources: every expiry timeout is
   0. Only the desk's dates may drive deletion, and no code checks this.

2. Run the database checks below. All are read-only except the last, which
   rolls back. Paste the output back with names blanked. The key preview
   prints dataset keys and 4CAT usernames, and the rolled-back mark (here)
   and the key check (under "Afterwards") print the keys you type in; no
   other command prints dataset keys or usernames. Each command carries the
   desk's own scope (`internal/fourcat/snapshots.go`). A row can be counted
   under more than one reason. The commands are written for bash, the
   workspace's shell: fish fails to parse the creators query and both
   previews (the `$'…'` in their quoting) and runs nothing.

   Version and encoding:

   ```
   sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "SELECT version()" -c "SHOW server_encoding" -c "SELECT pg_input_is_valid('{}', 'jsonb')"
   ```

   Good: PostgreSQL 16 or later (the compose file pins 17), `UTF8`, and `t`.

   What the desk reads, by type, and what it leaves out, by reason:

   ```
   sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "
   SELECT d.type, count(*) AS all_rows,
     count(*) FILTER (WHERE p.j IS NULL) AS parameters_unparsable,
     count(*) FILTER (WHERE jsonb_typeof(p.j) <> 'object') AS parameters_not_an_object,
     count(*) FILTER (WHERE p.j ? 'copied_from') AS copied_from,
     count(*) FILTER (WHERE p.j ? 'producer_type') AS producer_type,
     count(*) FILTER (WHERE d.is_finished IS NOT TRUE) AS not_finished,
     count(*) FILTER (WHERE coalesce(d.num_rows, 0) <= 0) AS no_rows,
     count(*) FILTER (WHERE d.key_parent IS DISTINCT FROM '') AS not_top_level,
     count(*) FILTER (WHERE d.key_parent = '' AND d.is_finished AND d.num_rows > 0 AND jsonb_typeof(p.j) = 'object' AND NOT (p.j ? 'copied_from') AND NOT (p.j ? 'producer_type')) AS included
   FROM datasets d CROSS JOIN LATERAL (SELECT CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN d.parameters::jsonb END AS j) p
   WHERE d.type IN ('tiktok-search', 'tiktok-comments-search')
   GROUP BY d.type ORDER BY d.type"
   ```

   Good: `parameters_unparsable`, `parameters_not_an_object` and
   `not_top_level` are 0. `copied_from` and `producer_type` count the merges,
   filters and refreshes made from snapshots. `not_finished` counts uploads
   still importing, and `no_rows` failed or empty imports.

   Creators and keys among the included rows (counts only):

   ```
   sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "
   SELECT count(DISTINCT d.creator) AS creators,
     count(DISTINCT d.creator) FILTER (WHERE NOT (d.creator ~ '^[a-z][a-z0-9._-]{0,31}$')) AS creators_not_matching,
     count(*) FILTER (WHERE d.creator IS NULL) AS rows_without_creator,
     count(*) FILTER (WHERE NOT (d.key ~ '^[0-9a-f]{32}$')) AS rows_with_other_keys
   FROM datasets d CROSS JOIN LATERAL (SELECT CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN d.parameters::jsonb END AS j) p
   WHERE d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND d.is_finished AND d.num_rows > 0
     AND jsonb_typeof(p.j) = 'object' AND NOT (p.j ? 'copied_from') AND NOT (p.j ? 'producer_type')"
   ```

   Good: `creators` is the number of people who uploaded (collectors, and
   you if you uploaded); the other three are 0. The desk skips a row whose
   creator does not match its username pattern or whose key is not 4CAT's
   32-hex form: such a row is never marked and counts in no group.

   The parameter keys among the included rows (keys only, no values):

   ```
   sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "
   SELECT k AS parameter, count(*) AS rows
   FROM datasets d CROSS JOIN LATERAL (SELECT CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN d.parameters::jsonb END AS j) p
     CROSS JOIN LATERAL jsonb_object_keys(CASE WHEN jsonb_typeof(p.j) = 'object' THEN p.j END) AS k
   WHERE d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND d.is_finished AND d.num_rows > 0
     AND jsonb_typeof(p.j) = 'object' AND NOT (p.j ? 'copied_from') AND NOT (p.j ? 'producer_type')
   GROUP BY k ORDER BY k"
   ```

   Good: `datasource` on every row (a Zeeschuimer import writes only that),
   perhaps `label` (a renamed dataset), and `keep` or `expires-after` only
   where someone set them; before the first update with this feature there
   is no `desk-retention`. A snapshot with an `expires-after` the desk did not
   set is left alone. Any other key is worth a look before marking goes on.

   The preview, as counts per creator (creators numbered in username order,
   no names): what the rule would mark and unmark now.

   ```
   sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "
   WITH s AS (
     SELECT d.key, d.type, d.creator, d.timestamp, d.num_rows,
       p.j->>'keep' IS NOT NULL AS keep,
       p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' = 'true', false) AS desk_marked,
       p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' <> 'true', true) AS foreign_expiry
     FROM datasets d CROSS JOIN LATERAL (SELECT CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN d.parameters::jsonb END AS j) p
     WHERE d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND d.is_finished AND d.num_rows > 0
       AND jsonb_typeof(p.j) = 'object' AND NOT (p.j ? 'copied_from') AND NOT (p.j ? 'producer_type')
       AND d.key ~ '^[0-9a-f]{32}$' AND d.creator ~ '^[a-z][a-z0-9._-]{0,31}$' AND d.timestamp IS NOT NULL
   ), w AS (
     SELECT s.*, row_number() OVER g AS pos,
       max(num_rows) OVER (g ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS newer_max,
       lag(num_rows) OVER g AS next_newer, lead(num_rows) OVER g AS next_older
     FROM s WINDOW g AS (PARTITION BY creator, type ORDER BY timestamp DESC, key COLLATE \"C\" DESC)
   ), x AS (
     SELECT w.*, pos > 2 AND newer_max >= num_rows
       AND NOT coalesce(next_newer < num_rows, false) AND NOT coalesce(next_older > num_rows, false) AS superseded
     FROM w
   )
   SELECT dense_rank() OVER (ORDER BY creator) AS creator_no, type, count(*) AS snapshots,
     count(*) FILTER (WHERE superseded AND NOT keep AND NOT foreign_expiry AND NOT desk_marked) AS would_mark,
     count(*) FILTER (WHERE desk_marked AND (keep OR NOT superseded)) AS would_unmark
   FROM x GROUP BY creator, type ORDER BY creator_no, type"
   ```

   Good: `would_mark` is 0 for anyone with two or fewer snapshots of a
   type, and for the rest at most `snapshots` − 2; `would_unmark` is 0 before
   marking was ever on. The totals of the two columns are what the admin line
   will show after the update, if no upload comes in between.

   The same preview with keys: this one prints dataset keys and 4CAT
   usernames and is for your eyes only, not to paste back.

   ```
   sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "
   WITH s AS (
     SELECT d.key, d.type, d.creator, d.timestamp, d.num_rows,
       p.j->>'keep' IS NOT NULL AS keep,
       p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' = 'true', false) AS desk_marked,
       p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' <> 'true', true) AS foreign_expiry
     FROM datasets d CROSS JOIN LATERAL (SELECT CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN d.parameters::jsonb END AS j) p
     WHERE d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND d.is_finished AND d.num_rows > 0
       AND jsonb_typeof(p.j) = 'object' AND NOT (p.j ? 'copied_from') AND NOT (p.j ? 'producer_type')
       AND d.key ~ '^[0-9a-f]{32}$' AND d.creator ~ '^[a-z][a-z0-9._-]{0,31}$' AND d.timestamp IS NOT NULL
   ), w AS (
     SELECT s.*, row_number() OVER g AS pos,
       max(num_rows) OVER (g ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS newer_max,
       lag(num_rows) OVER g AS next_newer, lead(num_rows) OVER g AS next_older
     FROM s WINDOW g AS (PARTITION BY creator, type ORDER BY timestamp DESC, key COLLATE \"C\" DESC)
   ), x AS (
     SELECT w.*, pos > 2 AND newer_max >= num_rows
       AND NOT coalesce(next_newer < num_rows, false) AND NOT coalesce(next_older > num_rows, false) AS superseded
     FROM w
   )
   SELECT CASE WHEN desk_marked THEN 'unmark' ELSE 'mark' END AS would, key, creator, type, num_rows, to_timestamp(timestamp) AS uploaded
   FROM x WHERE (superseded AND NOT keep AND NOT foreign_expiry AND NOT desk_marked) OR (desk_marked AND (keep OR NOT superseded))
   ORDER BY would, key"
   ```

   Good: every `mark` row is an older upload of someone with a newer one at
   least as large, and is neither side of a drop in that person's row
   counts.

   The mark itself, on one `mark` key from the preview, rolled back (replace
   `<key>` twice; the expiry time is any number, as nothing is kept):

   ```
   sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "BEGIN" -c "
   UPDATE datasets d SET parameters = (d.parameters::jsonb || '{\"expires-after\": 1795608000, \"desk-retention\": true}')::text
   WHERE d.key IN ('<key>') AND d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND d.is_finished AND d.num_rows > 0
     AND CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN jsonb_typeof(d.parameters::jsonb) = 'object'
       AND NOT (d.parameters::jsonb ? 'copied_from') AND NOT (d.parameters::jsonb ? 'producer_type')
       AND d.parameters::jsonb->>'keep' IS NULL AND NOT (d.parameters::jsonb ? 'expires-after') ELSE false END
   RETURNING d.key" -c "ROLLBACK" -c "SELECT count(*) AS still_marked FROM datasets d WHERE d.key = '<key>' AND d.parameters LIKE '%desk-retention%'"
   ```

   Good: the key once and `UPDATE 1`, then `ROLLBACK`, then `still_marked`
   0. `UPDATE 0` means the guard refused the row (it has "keep" or an
   expiry already); pick another key from the preview.

### After the update (dry run)

The admin page's Health section ends with
`Snapshot retention (dry run): last run <date time>, S snapshots read, N would be marked to expire, M would be unmarked`;
N and M are the preview's totals, if no upload came in between. The desk
log is the desk container's output:

```
sudo docker logs desk-desk-1 2>&1 | grep 'retention:'
```

The first pass after start writes, with invented key and username:

```
desk: 2026/11/25 09:00:01 retention: marking is off (dry run)
desk: 2026/11/25 09:00:02 retention: dry run: would mark 3f9a0c1e7b2d4a6f8e1c0b9d7a5e3c21 (collector-a, tiktok-search): superseded
desk: 2026/11/25 09:00:02 retention: dry run: 14 snapshots read, 1 would be marked, 0 would be unmarked
```

The `would mark` and `would unmark` lines are written again only when the
decision changes, so the last block is the current one. Its keys must be
the key preview's `mark` and `unmark` rows, with the preview run again after
that block was written. Nothing has been written to 4CAT at this point.

### Turning marking on and off

The desk root is `/data/<volume>/desk` (with `desk_storage_path` set, that
path plus `/desk`); `DESK_ROOT` in `/rsc/plugins/<uuid>/deploy/.env` names it.
Turn marking on:

```
sudo touch /data/<volume>/desk/state/retention-on
```

It takes effect at the next pass, within 15 minutes. The log confirms it
with `retention: marking is on`, then one
`retention: mark <key> (<creator>, <type>): superseded, expires <date> UTC`
line per marked key and a summary
`retention: S snapshots read, N marked to expire, M unmarked` (written only
when a pass changes something). The admin line loses "(dry run)". In 4CAT a
marked dataset's page says "Note: this dataset will no longer be available
after <date>", and dataset lists show a bin icon whose tooltip gives the
date.

Turn marking off:

```
sudo rm /data/<volume>/desk/state/retention-on
```

The log says `retention: marking is off (dry run)` at the next pass. Off,
the desk writes nothing, and does not unmark either: marks already set stay,
and 4CAT deletes those datasets at their date unless the marks are removed.
Removing the file during a pass does not stop that pass's writes, so wait
for the `retention: marking is off (dry run)` line before running the undo
below.

### Removing every desk mark

Turn marking off first and wait for `retention: marking is off (dry run)` in
the log. Count the marked rows:

```
sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "SELECT count(*) AS desk_marked FROM datasets d WHERE d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN jsonb_typeof(d.parameters::jsonb) = 'object' AND d.parameters::jsonb->'desk-retention' = 'true' ELSE false END"
```

Remove both parameters from every row carrying the desk's marker:

```
sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "UPDATE datasets d SET parameters = (d.parameters::jsonb - 'expires-after' - 'desk-retention')::text WHERE d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN jsonb_typeof(d.parameters::jsonb) = 'object' AND d.parameters::jsonb->'desk-retention' = 'true' ELSE false END"
```

Good: `UPDATE n` with n the count before, and the count run again is 0. A
row without the desk's marker, including one with an expiry an admin set,
is not touched.

### Afterwards

- Keep the mark lines: `docker logs` holds only the current container's
  output, and an update or a recreate starts a new one.

  ```
  sudo docker logs desk-desk-1 2>&1 | grep 'retention: mark ' > ~/retention-marks.txt
  ```

- 7 days after the first marks, plus up to 30 minutes for 4CAT's expire
  worker, check that 4CAT deleted exactly the keys the log named. These
  keys print nothing:

  ```
  sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "SELECT key FROM datasets WHERE key IN ('<key>', '<key>')"
  ```

  and no desk mark is more than an hour overdue (good: 0):

  ```
  sudo docker exec desk-4cat-db-1 psql -U fourcat -d fourcat -c "SELECT count(*) AS overdue FROM datasets d WHERE d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN CASE WHEN jsonb_typeof(d.parameters::jsonb) = 'object' AND d.parameters::jsonb->'desk-retention' = 'true' THEN (d.parameters::jsonb->>'expires-after')::numeric < extract(epoch FROM now()) - 3600 ELSE false END ELSE false END"
  ```

  The included count from the reads above, run again, is the earlier one
  minus the keys deleted plus the uploads since, less anything deleted by
  hand.

- Tell collectors never to press Zeeschuimer's Reset. No row-count rule sees
  a reset followed by more collecting before the next upload.
- After you delete a collector's newest snapshots by hand, the next pass
  (within 15 minutes) unmarks the older ones the rule now keeps, while
  marking is on; switched off, the marks stay until you remove them. A
  restart of the desk (`sudo docker restart desk-desk-1`) runs a pass at
  once.
