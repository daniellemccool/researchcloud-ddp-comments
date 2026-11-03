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
   pushes `desk:<tag>`, `deskproxy:<tag>` and mirrors `4cat:<4CAT tag>` under
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
   ```

   The 4CAT tag is currently v1.57; it follows `FOURCAT_IMAGE` in
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

## First release only

GHCR packages start private. In the GitHub package settings, set `desk`,
`deskproxy` and `4cat` to public. Workspaces pull anonymously, so a release
is not usable until this is done.

## The 4CAT mirror

The workflow reads `FOURCAT_IMAGE` from `deploy/.env.example` and mirrors
that tag. To change the pin, edit `deploy/.env.example`, tag a release, and
run `sudo /rsc/plugins/<uuid>/deploy/init.sh update <tag>` on each workspace
(`<uuid>` is the plugin directory the platform created): `update`
adopts the TAG from that release's `deploy/.env.example` and maps it onto the
GHCR mirror (`ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:<tag>`),
writing that reference into `desk.json` and `.env`, so 4CAT's database
migrations run at that update. The `.env.example` value itself names the
Docker Hub source and is never used verbatim. Bump it deliberately,
not as part of an unrelated release.
