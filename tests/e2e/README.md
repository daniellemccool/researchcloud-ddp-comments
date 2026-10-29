# e2e tests (Playwright, against the live sandbox)

These drive the real desk page in Chromium. They are not part of `scripts/test-all.sh`.

## Install (once)

    uv venv .venv && uv pip install --python .venv/bin/python playwright
    .venv/bin/playwright install chromium

## Log in (once per identity, repeat when the SRAM session expires)

    E2E_BASE=https://<desk fqdn> .venv/bin/python tests/e2e/login.py dmccool
    E2E_BASE=https://<desk fqdn> .venv/bin/python tests/e2e/login.py dmccool2

A browser window opens; log in through SRAM by hand. The script waits until `/desk/` answers
200, then saves the session to `~/.config/ddp-comments-e2e/<identity>.json` (outside the repo,
mode 600; it holds session cookies).

## Run

    E2E_BASE=https://<desk fqdn> scripts/e2e.sh dmccool2
    E2E_BASE=https://<desk fqdn> E2E_ADMIN=1 scripts/e2e.sh dmccool

| variable | meaning |
| --- | --- |
| `E2E_BASE` | the desk's https URL, no trailing slash (required) |
| `E2E_STATE` | storage state file; `scripts/e2e.sh` sets it from the identity |
| `E2E_ADMIN` | `1` for an admin identity: enables the admin page test and makes the claim test check the desk log through the admin export |
| `E2E_HEADED` | `1` to watch the browser |

Without `E2E_STATE` and `E2E_BASE` every test skips. A first visit provisions the user's
session, which can take a couple of minutes; the tests wait up to four.
