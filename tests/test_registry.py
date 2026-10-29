"""Tests for desk/desklib/registry.py and desk/desklib/locks.py."""
import os, sys, tempfile, threading, time, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib import locks, registry  # noqa: E402


class RegistryTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = self._tmp.name

    def reg(self, **kw):
        return registry.Registry(self.root, **kw)

    def write(self, text):
        with open(os.path.join(self.root, "sessions.tsv"), "w") as f:
            f.write(text)

    def lines(self):
        with open(os.path.join(self.root, "sessions.tsv")) as f:
            return f.read().splitlines()

    def test_legacy_two_column_rows_are_live(self):
        self.write("alice\t1\nbob\t2\n")
        r = self.reg()
        self.assertEqual({u: row.index for u, row in r.live().items()}, {"alice": 1, "bob": 2})
        self.assertEqual(r.port("alice"), 5801)
        self.assertIsNone(r.port("nobody"))

    def test_allocate_is_idempotent_and_sequential(self):
        r = self.reg()
        self.assertEqual(r.allocate("alice").index, 1)
        self.assertEqual(r.allocate("alice").index, 1)
        self.assertEqual(r.allocate("bob").index, 2)
        lines = self.lines()
        self.assertEqual(len(lines), 2)
        self.assertTrue(all(l.split("\t")[2] == "live" for l in lines))

    def test_retired_index_is_reused(self):
        r = self.reg()
        r.allocate("alice"); r.allocate("bob")
        r.retire("alice")
        self.assertIsNone(r.port("alice"))
        self.assertEqual(r.allocate("carol").index, 1)

    def test_returning_retired_user_gets_old_index_if_free(self):
        r = self.reg()
        r.allocate("alice"); r.allocate("bob")
        r.retire("alice")
        self.assertEqual(r.allocate("alice").index, 1)
        self.assertEqual(len(self.lines()), 2)
        self.assertEqual(r.live()["alice"].status, "live")

    def test_returning_retired_user_gets_lowest_free_otherwise(self):
        r = self.reg()
        r.allocate("alice"); r.allocate("bob")
        r.retire("alice")
        self.assertEqual(r.allocate("carol").index, 1)
        self.assertEqual(r.allocate("alice").index, 3)
        self.assertEqual(len(self.lines()), 3)
        self.assertEqual(sorted(row.user for row in r.rows()), ["alice", "bob", "carol"])
        self.assertEqual(len(r.live()), 3)

    def test_cap_raises(self):
        r = self.reg(max_sessions=2)
        r.allocate("a"); r.allocate("b")
        with self.assertRaises(registry.RegistryFull):
            r.allocate("c")

    def test_dotted_username_round_trips(self):
        r = self.reg()
        row = r.allocate("j.smith")
        self.assertEqual(r.live()["j.smith"].index, row.index)
        for bad in ("Bad;user", "", "1abc", "a" * 33):
            with self.assertRaises(ValueError):
                r.allocate(bad)

    def test_flocked_blocks_second_holder(self):
        path = locks.user_lock(self.root, "alice")
        held = threading.Event()

        def holder():
            with locks.flocked(path):
                held.set()
                time.sleep(0.3)

        t = threading.Thread(target=holder)
        t.start()
        self.assertTrue(held.wait(2))
        with self.assertRaises(TimeoutError):
            with locks.flocked(path, timeout=0.05):
                pass
        start = time.monotonic()
        with locks.flocked(path):
            pass
        self.assertGreater(time.monotonic() - start, 0.05)
        t.join()
        self.assertTrue(path.endswith(os.path.join("locks", "alice")))
        self.assertEqual(locks.render_lock(self.root), os.path.join(self.root, ".render.lock"))


if __name__ == "__main__":
    unittest.main()
