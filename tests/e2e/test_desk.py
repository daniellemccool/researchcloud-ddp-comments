"""End-to-end tests of the desk page against a live sandbox, in a logged-in Chromium.

Not collected by scripts/test-all.sh (tests/e2e has no __init__.py and is outside the
discovery start directory's packages); run with scripts/e2e.sh IDENTITY. Environment:

    E2E_STATE   storage state saved by login.py (required)
    E2E_BASE    the desk's https URL, no trailing slash (required)
    E2E_ADMIN   1 if the identity is an admin (enables the admin tests)
    E2E_HEADED  1 to watch the browser

Without E2E_STATE and E2E_BASE every test skips.
"""
import json
import os
import unittest

STATE = os.environ.get("E2E_STATE", "")
BASE = os.environ.get("E2E_BASE", "").rstrip("/")
ADMIN = os.environ.get("E2E_ADMIN") == "1"
HEADED = os.environ.get("E2E_HEADED") == "1"
READY_TIMEOUT_MS = 240_000


@unittest.skipUnless(STATE and BASE, "E2E_STATE and E2E_BASE not set (run scripts/e2e.sh IDENTITY)")
class DeskE2E(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        try:
            from playwright.sync_api import sync_playwright
        except ImportError:
            raise unittest.SkipTest("playwright is not installed (uv pip install --python .venv/bin/python playwright)")
        cls._pw = sync_playwright().start()
        cls._browser = cls._pw.chromium.launch(headless=not HEADED)

    @classmethod
    def tearDownClass(cls):
        cls._browser.close()
        cls._pw.stop()

    def setUp(self):
        self.ctx = self._browser.new_context(storage_state=STATE)
        self.page = self.ctx.new_page()
        self.addCleanup(self.ctx.close)

    def _desk(self):
        self.page.goto(BASE + "/desk/")

    def _wait_ready(self):
        """Poll /desk/ until the 'Setting up' page (refreshes every 3 s) is replaced by the desk."""
        self._desk()
        self.page.wait_for_selector("aside", timeout=READY_TIMEOUT_MS)
        self.assertNotIn("Setting up", self.page.content())
        self.assertNotIn("Setup failed", self.page.content())

    def test_desk_page_loads_two_panes(self):
        self._wait_ready()
        self.assertEqual(self.page.locator("aside").count(), 1)          # the queue pane
        self.assertEqual(self.page.locator("iframe#screen").count(), 1)  # the browser pane

    def test_first_visit_reaches_ready(self):
        self._desk()
        self.page.wait_for_function(
            "() => !document.body.innerText.includes('Setting up')", timeout=READY_TIMEOUT_MS)
        body = self.page.inner_text("body")
        self.assertNotIn("Setup failed", body)
        self.page.wait_for_selector("aside", timeout=30_000)

    @unittest.skipUnless(ADMIN, "E2E_ADMIN=1 not set: the identity is not an admin")
    def test_admin_page_for_admin_identity(self):
        resp = self.page.goto(BASE + "/desk/admin/")
        self.assertEqual(resp.status, 200)
        self.assertGreater(self.page.locator('a[href="/desk/admin/export/captures.zip"]').count(), 0)
        self.assertGreater(self.page.locator('form[action="/desk/admin/pool"] input[name=file]').count(), 0)

    def test_claim_and_open_logs_event(self):
        self._wait_ready()
        rows = self.page.locator("ul.rows li")
        if rows.count() == 0 or self.page.locator("ul.rows li[data-state=todo]").count() == 0:
            more = self.page.locator("#getmore button", has_text="Get 5")
            if more.count() == 0:
                self.skipTest("nothing to claim and no open rows (pool empty or claims unfinished)")
            more.first.click()   # POSTs /desk/claim?n=5, then reloads the page
            self.page.wait_for_selector("ul.rows li", timeout=30_000)
        row = self.page.locator("ul.rows li[data-state=todo]").first
        kind, item_id = row.get_attribute("data-kind"), row.get_attribute("data-id")
        row.locator("a").first.click()
        self.page.wait_for_selector(f'ul.rows li[data-id="{item_id}"][data-state=done]', timeout=60_000)
        if not ADMIN:
            return   # the desk log is admin-only; the row turning done is the open having been accepted
        resp = self.ctx.request.get(BASE + "/desk/admin/export/desk-log.ndjson")
        self.assertEqual(resp.status, 200)
        events = [json.loads(line) for line in resp.text().splitlines() if line.strip()]
        self.assertTrue(
            any(e.get("action") == "open" and e.get("kind") == kind and str(e.get("id")) == item_id
                for e in events),
            f"no open event for {kind} {item_id} in the desk log")


if __name__ == "__main__":
    unittest.main()
