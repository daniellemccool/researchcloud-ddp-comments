# researchcloud-ddp-comments

SURF Research Cloud deployment of **ddp-comments**: comment collection for the videos in
donated TikTok watch histories. Each collector signs in through SRAM and gets their own
streamed Firefox, in which Zeeschuimer captures the comments of the videos they open and
uploads them to 4CAT under the collector's own account. A capture proxy in front of each
Firefox keeps a safety copy of every comment response.

The desk is one Go binary. On a collector's visit it provisions what the collector needs
(a 4CAT account and token, a Firefox profile from the verified release template), then starts
a pair of containers for them on a private Docker network with no route out: their Firefox,
and a proxy that is the Firefox's only way to the internet and to 4CAT. Stopping the session
removes the pair; everything persistent stays on the volume.

| Path | Purpose |
|---|---|
| `cmd/desk` | the desk: `serve` (the collector and admin pages, the screen relay, provisioning and sessions), the operator commands `status`, `stop` and `render`, and `template release`, which builds the sanitised Firefox profile template a release ships |
| `cmd/deskproxy` | the per-collector capture proxy: the egress boundary, the 4CAT token injection and the safety copy |
| `internal/` | the desk's packages: `access` (identity from the SRAM gate), `provision`, `session`, `render`, `docker`, `fourcat`, `web`, `ctl`, `pool`, `registry`, `template`, … |
| `deploy/` | `compose.yml`, the init script SRC runs at launch, and the nginx locations it installs |
| `platforms/tiktok/` | the platform definition: capture rules, item kinds, the pinned profile template |
| `docs/decisions/` | architecture decisions (lean ADRs); start at `docs/decisions/README.md` |
| `docs/release.md` | release and operator runbook |
| `docs/catalog-item.md` | the SRC catalog item record |

Checks: `scripts/check.sh` runs the layers `adr`, `shell`, `go`, `compose` and `shape`; a missing
tool is a failure, not a skip. Go tests: `go test ./...`.

The Python under `desk/`, `bin/` and `tests/test_*.py` is the prototype the Go desk was ported
from; it is retired in the next plan.
