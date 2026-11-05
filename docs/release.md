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
`sudo docker exec desk-desk-1 desk status` (one line per registry row: user,
index, `running` or `stopped`, and `ready` or the provisioning step it is at),
and free a seat with `sudo docker exec desk-desk-1 desk stop <user>`. The
collector's next visit starts a fresh pair; the profile, captures and 4CAT
account stay on the volume.

The desk needs Docker Engine 28 or newer (API 1.48). It creates each pair
network with Docker's isolated gateway mode
(`com.docker.network.bridge.gateway_mode_ipv4=isolated`, ADR-0007), and
`desk serve` refuses to start on an older daemon, with an error naming the
requirement. A launch installs current docker-ce; check an existing host with
`docker version --format '{{.Server.Version}} {{.Server.APIVersion}}'`.

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
