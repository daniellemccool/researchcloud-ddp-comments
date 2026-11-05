#!/usr/bin/env python3
"""Save a logged-in browser state for the e2e tests.

    .venv/bin/python tests/e2e/login.py IDENTITY [BASE_URL]

Opens a headed Chromium at BASE_URL (default: $E2E_BASE) /desk/. Log in through SRAM as
IDENTITY by hand; the script waits until /desk/ answers 200 for that session, then writes the
storage state to ~/.config/ddp-comments-e2e/<IDENTITY>.json (mode 600). The file holds session
cookies: it stays outside the repository.
"""
import os
import re
import sys
import time

STATE_DIR = os.path.expanduser("~/.config/ddp-comments-e2e")
TIMEOUT_S = 600


def state_path(identity: str) -> str:
    return os.path.join(STATE_DIR, identity + ".json")


def main(argv) -> int:
    if len(argv) not in (2, 3) or not re.fullmatch(r"[A-Za-z0-9._-]{1,64}", argv[1]):
        print(__doc__, file=sys.stderr)
        return 2
    identity = argv[1]
    base = (argv[2] if len(argv) == 3 else os.environ.get("E2E_BASE", "")).rstrip("/")
    if not base.startswith("https://"):
        print("login.py: give the desk's https URL as BASE_URL or in E2E_BASE", file=sys.stderr)
        return 2
    from playwright.sync_api import sync_playwright   # lazy: the module imports without playwright

    os.makedirs(STATE_DIR, mode=0o700, exist_ok=True)
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=False)
        ctx = browser.new_context()
        page = ctx.new_page()
        page.goto(base + "/desk/")
        print(f"log in as {identity} in the browser window; waiting up to {TIMEOUT_S // 60} minutes")
        deadline = time.time() + TIMEOUT_S
        while time.time() < deadline:
            try:
                if ctx.request.get(base + "/desk/", max_redirects=0).status == 200:
                    break
            except Exception:
                pass   # mid-redirect or a page closing: keep waiting
            page.wait_for_timeout(2000)
        else:
            print("login.py: timed out before /desk/ answered 200", file=sys.stderr)
            browser.close()
            return 1
        path = state_path(identity)
        ctx.storage_state(path=path)
        os.chmod(path, 0o600)
        browser.close()
    print(f"saved {path}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
