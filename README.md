# researchcloud-ddp-comments

SURF Research Cloud deployment of **ddp-comments**: comment collection for the videos in
donated TikTok watch histories. Each collector gets their own SRAM-gated, streamed
Firefox session where Zeeschuimer captures the comments and uploads them to 4CAT; a
mitmproxy sidecar keeps an automatic copy.

Design: `docs/superpowers/specs/2026-10-02-monday-handover-chain-design.md` (the desk
part is superseded by per-user sessions, see `docs/students-runbook.md` once written).
Spike record (2026-09-07): `docs/spike-runbook.md`, `docs/spike-compose-2026-09-07.yml`.

| Path | Runs on | Purpose |
|---|---|---|
| `desk/desklib/render.py` | desk workspace | render Compose, the nginx gate and the Firefox policy from the session registry |
| `desk/desklib/config.py` | desk workspace | the desk's configuration file `desk.json` |
| `desk/desk_stub.py`, `desk/desk-stub.service` | desk workspace | two-pane desk page per user; users pull batches from one shared queue; opens videos in that user's Firefox |
| `desk/pool-add.py` | desk workspace | append a `selection.csv` to the shared queue (`pool.csv`), skipping known ids |
| `desk/mitm/addons/capture.py` | desk workspace (in the proxy) | automatic comment capture |

Tests: `scripts/test-all.sh` (bash + Python, no SRC access needed).
