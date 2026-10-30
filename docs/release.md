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
   Verify anonymously, from a machine that is not logged in to GHCR:

   ```
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:vX.Y.Z
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/deskproxy:vX.Y.Z
   docker pull ghcr.io/daniellemccool/researchcloud-ddp-comments/4cat:v1.57
   ```

3. Build the template artifact on the box with `desk template release ...`
   (the Task 8 tool). It writes `ddp-comments-template-<platform>-<zeeschuimer_version>.tar`
   and a `.sha256` file, and prints the sha256; keep that value for step 5.

4. Create the GitHub release and upload the artifact and its checksum, from
   the local checkout (GitHub is not reached from the box):

   ```
   gh release create vX.Y.Z --title vX.Y.Z --notes "vX.Y.Z"
   gh release upload vX.Y.Z <tar> <tar>.sha256
   ```

5. In the SRC portal, edit the component and set `template_sha256` to the
   value from step 3, and the image parameters `desk_image`, `deskproxy_image`
   and `fourcat_image` to the full references of the pushed images.
   `init.sh` fetches the artifact from this release and refuses it if the
   sha256 does not match.

## First release only

GHCR packages start private. In the GitHub package settings, set `desk`,
`deskproxy` and `4cat` to public. Workspaces pull anonymously, so a release
is not usable until this is done; repeat the anonymous pulls from step 2.

## The 4CAT mirror

The mirror tag follows `FOURCAT_IMAGE` in `deploy/.env.example`; the workflow
reads it at release time. Bumping that pin pulls in a new 4CAT version, and
4CAT runs its database migrations at the next `init.sh update`, so bump it
deliberately, not as part of an unrelated release.
