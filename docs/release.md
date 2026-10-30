# Release runbook

A release is a tag `vX.Y.Z` (or `vX.Y.Z-alpha.N`). The tag builds the images;
the template artifact is built on the box and uploaded by hand. The namespace
is `ghcr.io/daniellemccool/researchcloud-ddp-comments`.

## Steps

1. Tag from the local checkout, on a commit that passed `scripts/check.sh`:

   ```
   git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z
   ```

2. Wait for the `release` workflow in GitHub Actions. It pushes
   `desk:<tag>`, `deskproxy:<tag>` and `4cat:<4CAT tag>` under the namespace.
   First release: make the packages public (section "First release only")
   before this check. Then verify anonymously, from a machine that is not
   logged in to GHCR:

   ```
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:vX.Y.Z
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/deskproxy:vX.Y.Z
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:v1.57
   ```

   The 4CAT tag is currently v1.57; it follows `FOURCAT_IMAGE` in
   `deploy/.env.example`.

3. Build the template artifact on the box. First build the image once
   (`sudo docker build -f cmd/desk/Dockerfile -t desk:dev .`), then run the
   tool; the profile is a copy of a sanitisable Firefox profile owned by
   uid 1000. Example for TikTok with Zeeschuimer 1.14.2:

   ```
   sudo docker run --rm -u 1000:1000 -v /home/dmccool/template-work:/w -v /home/dmccool/ddp-comments/platforms:/platforms:ro desk:dev template release --profile /w/profile --platform tiktok --platforms-dir /platforms --zeeschuimer 1.14.2 --out /w/out
   ```

   Generic form: `--platform <platform> --zeeschuimer <zeeschuimer_version>`.
   It writes `/w/out/ddp-comments-template-<platform>-<zeeschuimer_version>.tar`
   and the sibling `.sha256` file, and prints the manifest JSON plus a
   `sha256 <hex>  <filename>` line. Keep the hex for step 5, and copy both
   files to the local checkout.

4. Create the GitHub release and upload the artifact and its checksum, from
   the local checkout (GitHub is not reached from the box):

   ```
   gh release create vX.Y.Z --title vX.Y.Z --notes "vX.Y.Z"
   gh release upload vX.Y.Z <tar> <tar>.sha256
   ```

5. In the SRC portal, edit the component and set `template_sha256` to the
   hex from step 3, and the image parameters `desk_image`, `deskproxy_image`
   and `fourcat_image` to full references, for example
   `desk_image=ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:v0.1.0-alpha.1`.
   The init script derives the release tag, and so the artifact download URL,
   from the tag part of `desk_image`, so it must equal the git tag. It refuses
   the artifact if the sha256 does not match.

## First release only

GHCR packages start private. In the GitHub package settings, set `desk`,
`deskproxy` and `4cat` to public. Workspaces pull anonymously, so a release
is not usable until this is done.

## The 4CAT mirror

The workflow reads `FOURCAT_IMAGE` from `deploy/.env.example` and mirrors
that tag. Bumping the pin changes only what the workflow mirrors. A running
workspace adopts the new version only when `fourcat_image` is changed in the
component and `init.sh update` is run, which runs 4CAT's database migrations.
Bump it deliberately, not as part of an unrelated release.
