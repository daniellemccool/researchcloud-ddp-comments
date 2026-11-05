# ddp-comments catalog item

Data-donation desk for TikTok comments (Zeeschuimer + 4CAT), built from
`researchcloud-ddp-comments`. Architecture decisions: `docs/decisions/README.md`.
Release steps: `docs/release.md`.

Owner CO of the component and the item: D3I data donation. A workspace is
launched in the study's CO (OnlineHate for the sandbox and the throwaway
launch), where the two SRAM groups and the volume must exist.

## Component

Script type Docker Compose. Repository
`https://github.com/daniellemccool/researchcloud-ddp-comments` (public), path
`deploy/compose.yml`, tag field = the branch `release`, a moving ref the
release workflow fast-forwards to every tagged commit, so the component is
never edited per release (`v0.1.0-alpha.3` = the alpha.3 commit). The script
`init.sh` is named on the ITEM, as the overwrite `docker_initialization_command`
of the SRC-External Docker Compose component (there is no field for it on this
component): the plugin resolves it relative to the compose file's directory,
runs it as root with that directory as cwd and the parameters in
`PLUGIN_PARAMETERS`, then restarts docker, writes `src_ansible.env` next to the
compose file (0644, every ansible variable plus the flattened parameters) and
only then runs `compose up`.

Portal rules for parameters: values are strings, never empty, no quotes. Override
order is component, then item, then workspace user. A parameter not declared on
the component has no effect. Placeholder defaults fail at preflight, on purpose.
On the component, "Overwritable" lets an item parameter of the same name override
the value and "Required" makes the portal ask the launcher when the item gives
none; both are N/A for the platform-sourced parameters.

| Parameter | Source | Default | Overwritable | Required | Item action |
|---|---|---|---|---|---|
| `desk_collector_role` | Fixed | `src_co_<fill-me-in>` | yes | yes | interactive |
| `desk_admin_role` | Fixed | `src_co_admin` | yes | no | keep |
| `desk_platform` | Fixed | `tiktok` | yes | no | interactive |
| `desk_contact` | Fixed | `<fill-me-in>` | yes | yes | interactive |
| `desk_hide_media` | Fixed | `true` | yes | no | keep |
| `desk_batch_sizes` | Fixed | `5,10` | yes | no | keep |
| `desk_idle_seconds` | Fixed | `900` | yes | no | keep |
| `desk_timer_minutes` | Fixed | `3` | yes | no | keep |
| `desk_max_sessions` | Fixed | `99` | yes | no | keep |
| `desk_max_running` | Fixed | `auto` | yes | no | keep |
| `desk_storage_path` | Fixed | `auto` | yes | no | keep |
| `fourcat_enabled` | Fixed | `true` | yes | no | keep |
| `co_id` | Workspace (`co_id`) | none | N/A | N/A | keep |
| `co_name` | Workspace (`co_name`) | none | N/A | N/A | keep |
| `workspace_fqdn` | Resource (the workspace's FQDN) | none | N/A | N/A | keep |

Image references and the template digest are not parameters: the init script reads
them from the checked-out release (`deploy/.env.example`,
`platforms/<platform>/template.json`; ADR-0020), and refuses a component that still
declares one.

Notes on the values:

- `desk_collector_role` and `desk_admin_role` must match `^src_co_[A-Za-z0-9_-]+$`
  and each be held by at least one CO member. The collector default is the
  placeholder so an unedited launch fails loudly.
- `desk_contact` appears in page text; no quotes.
- `desk_idle_seconds` is how long a collector's pair may go without page input or
  without an open screen before the desk stops it; `0` turns the idle stop off.
- `co_id`, `co_name` and `workspace_fqdn` are read by the init script from
  `PLUGIN_PARAMETERS` too, so all three must be declared.

## Item

Components in order: SRC-OS, SRC-CO, SRC-Nginx, SRC-External Docker Compose,
ddp-comments. Ubuntu 24.04. Default flavour 4 core / 16 GB; the boot disk follows
the flavour (the item's boot-disk field did not apply: `hpc-2core-16gb-20gb` gave a
38 GB boot volume); one attached volume required (the init script refuses zero or
several). Access button: Custom, `https://==REVERSE_PROXY==/desk/` (the
Webinterface preset has no path and lands on the 4CAT placeholder's 503).
Firewall 22/80/443 (immutable on the platform). The wizard's parameter step shows
no overwritable parameters until every component row of step 1 has been expanded
once (portal bug); expand them, then set the three interactive ones and the
overwrites below.

| Overwrite | Value |
|---|---|
| `co_passwordless_sudo` | `true` |
| `co_admin_user_only` | `true` |
| `rsc_nginx_user_header_name` | `REMOTE_USER` |
| `rsc_nginx_co_role` | empty (the desk gates per role itself) |
| `timeout` | `7200` (image pulls dominate provisioning) |
| `docker_initialization_command` | `init.sh` |

After the first launch, an admin-role user opens `/fourcat/` through the
gate and completes 4CAT's setup form once, under a name that is not a SRAM
username, such as the admin's email address (an admin is also a collector, and a
collector's 4CAT account is created under their SRAM username, so a clash has no
repair path yet); the desk does not create that account (Danielle, 2026-11-02;
2026-11-03).

## Pre-launch checklist

- The release's four images (`desk`, `deskproxy`, `4cat`, `firefox`) and the
  template artifact named by its `platforms/<platform>/template.json` are
  published, and the GHCR packages are public. `firefox` is the Firefox mirror
  (`jlesage/firefox` copied into GHCR by `release.yml`); a workspace never pulls
  it from Docker Hub.
- The two SRAM groups (collector and admin roles) exist and each has at least one
  member; the init script refuses otherwise.
- The volume exists in the launching CO.
- The checked-out release's `.env.example` names images that exist in GHCR and its
  `template.json` digest matches the uploaded artifact (`scripts/check-pins.sh <tag>`
  passed when the release was tagged).

## Template artifacts

| Platform | Zeeschuimer | Artifact | SHA256 | Built on | Manifest |
|---|---|---|---|---|---|
| tiktok | 1.14.3 | `ddp-comments-template-tiktok-1.14.3.tar` | `143aa7b6055ad6c4d740a2cccf8b24209c2acf5424ce4aa33e724557e22d507c` | 2026-10-30, from the sandbox `state/firefox/_template` profile | `platforms/tiktok/template.json` |

## Snags

- `src_ansible.env` is written 0644 after init and before `compose up`, and holds
  every playbook variable plus the parameters. Seen at launch 1: no token, gate or
  password in it (the only secret-bearing file is `deploy/.env`, 0600), but it is
  world-readable because the plugin copies files 0600 and directories 0755, so the
  tree is not root-0600 as assumed. Nothing secret may ever be a parameter.
- The copied repository's folder under `/rsc/plugins/` is the plugin's UUID
  (`/rsc/plugins/1b0c7882-…` at launch 1), not the repository name. The compose
  file and the wrapper take the real path from the init script, so it works; the
  runbook's `/rsc/plugins/<repo>/deploy/init.sh` means that UUID folder.
- Portal bug: the catalog item wizard reports "No (overwritable) parameters found"
  until each component row in step 1 has been expanded once.
- `docker_initialization_command` is an item overwrite on SRC-External Docker
  Compose, not a component field (recorded above).
- The item's boot-disk size does not override the flavour's disk.
- The 4CAT backend has no outbound internet by design. Processors that fetch
  externally fail; NLTK downloads fail at start, which is non-fatal.
- The compose project name `desk` prefixes container and network names
  (`desk-desk-1`, `desk_desk-fourcat`). Per-user compose files in plan 2 must
  attach to the project-prefixed network.
- A release that bumps the 4CAT pin is adopted by a running workspace only through
  `init.sh update <tag>`, which maps the new copy's tag onto the GHCR mirror and
  runs 4CAT's migrations then.

## Throwaway launch 1

- Date: 2026-11-02, OnlineHate, flavour `hpc-2core-16gb-20gb`, 5 GB volume,
  hostname `ddpcomments`; deleted after the checks.
- Release: component ref `release` = `v0.1.0-alpha.2` (bcf4473); images
  desk/deskproxy `v0.1.0-alpha.2`, 4CAT `v1.57`; `template_sha256`
  `143aa7b6…507c`, Zeeschuimer 1.14.3.
- Provisioning wall-clock: 8 min 44 s cold (15:36:31 → 15:45:15 CET); Terraform
  44 s; the plugin copy 1 min 47 s; the init script 72 s; compose up 18 s.
- Image-pull time: about 1 min for all three from the GHCR mirror (5.4 GB 4CAT
  image pulled 15:43:31 → 15:44:31).
- Provisioning log: green; every play recap failed=0; no ssh fix.
- `docker version`: 29.9.2 Docker Engine - Community (package docker-ce
  5:29.9.2-1~ubuntu.24.04~noble).
- `/rsc/plugins/1b0c7882-2956-4cc0-863a-81f9a4166f88/`: directories 0755 root,
  files 0600 root, `init.sh` 0700, `src_ansible.env` 0644.
- `src_ansible.env`: present, 0644, 40 lines; no token or secret in it.
- Checks: five containers up (db and memcached healthy); only `127.0.0.1:8110`
  published; `/desk/` without headers 403, wrong gate 403, `/healthz` on the gated
  port 403; `sudo desk --help` prints usage and exits 2 (plan 2); 4CAT frontend
  answers its first-run setup page from inside the network; backend has no egress
  (`ping.4cat.nl` unresolvable, by design). Desk root on the volume: secrets 0700/0600
  root, templates 0644/0755 uid 1000, state directories empty.
- Browser as `dmccool`: SRAM round trip, then `/desk/` 200 with the admin
  placeholder ("Signed in as dmccool on TikTok"); one access record with collector
  and admin true. `dmccool2` is not an OnlineHate member, so the non-member case was
  not exercised. Full log: the owner's sandbox log, "Throwaway launch 1".
