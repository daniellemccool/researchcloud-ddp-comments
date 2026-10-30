# ddp-comments catalog item

Data-donation desk for TikTok comments (Zeeschuimer + 4CAT), built from
`researchcloud-ddp-comments` at `deploy/compose.yml`.

## Component

Parameters declared on the component (spec §5.2), with their defaults:

| Parameter | Default |
|---|---|
| `desk_collector_role` | `src_co_<fill-me-in>` |
| `desk_admin_role` | `src_co_admin` |
| `desk_platform` | `tiktok` |
| `desk_contact` | `<fill-me-in>` |
| `desk_hide_media` | `true` |
| `desk_batch_sizes` | `5,10` |
| `desk_idle_seconds` | `900` |
| `desk_timer_minutes` | `3` |
| `desk_max_sessions` | `99` |
| `desk_max_running` | `auto` |
| `desk_storage_path` | `auto` |
| `fourcat_enabled` | `true` |
| `fourcat_image` | pinned `digitalmethodsinitiative/4cat:<tag>` |
| `desk_image` | pinned `ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:<tag>` |
| `deskproxy_image` | pinned `ghcr.io/daniellemccool/researchcloud-ddp-comments/deskproxy:<tag>` |
| `zeeschuimer_version` | `1.14.2` |
| `template_sha256` | pinned per release |

## Item

Item overwrites (spec §5.1):

- `co_passwordless_sudo=true`
- `co_admin_user_only=true`
- `rsc_nginx_user_header_name=REMOTE_USER`
- `rsc_nginx_co_role` left empty (the desk gates per role itself)
- `timeout=7200`
- `docker_initialization_command=init.sh`
- Access button: Webinterface (https)

## Template artifacts

| Platform | Zeeschuimer | Artifact | SHA256 | Built on |
|---|---|---|---|---|
| tiktok | 1.14.2 | `ddp-comments-template-tiktok-1.14.2.tar` | `c4f12ac12356906a94d202fc67020a5abffb576e77a7e6034290dee6ef2935c9` | 2026-10-30, from the sandbox `state/firefox/_template` profile |

## Snags

(none)
