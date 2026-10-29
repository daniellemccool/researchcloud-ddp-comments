"""Tests for desk/pool-add.py: the CLI over desklib.pool (item kinds, Excel-tolerant parsing, rejects)."""
import csv, os, subprocess, sys, tempfile, unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ADD = os.path.join(REPO, "desk", "pool-add.py")
sys.path.insert(0, os.path.join(REPO, "desk"))
from desklib.config import DeskConfig, save_config  # noqa: E402


def make_desk(t):
    root = os.path.join(t, "desk")
    save_config(DeskConfig(desk_root=root, repo_dir=REPO, platform="tiktok", collector_role="src_co_students",
                           admin_role="src_co_admin", contact="x"))
    return root


def put(path, data: bytes):
    with open(path, "wb") as f:
        f.write(data)
    return path


class PoolAddTest(unittest.TestCase):
    def setUp(self):
        self._t = tempfile.TemporaryDirectory()
        self.t = self._t.name
        self.desk = make_desk(self.t)

    def tearDown(self):
        self._t.cleanup()

    def add(self, name, data: bytes, desk=None):
        path = put(os.path.join(self.t, name), data)
        return subprocess.run([sys.executable, ADD, "--desk", desk or self.desk, path],
                              capture_output=True, text=True)

    def pool(self):
        with open(os.path.join(self.desk, "pool.csv"), newline="") as f:
            return list(csv.DictReader(f))

    def test_legacy_video_id_csv_imports_as_video_kind(self):
        r = self.add("a.csv", b'video_id,bucket,note\n1,x,"n1, q"\n2,x,\n2,x,dup\n')
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("added 2 to ", r.stdout)
        self.assertIn("(2 in pool)", r.stdout)
        rows = self.pool()
        self.assertEqual([(x["id"], x["kind"]) for x in rows], [("1", "video"), ("2", "video")])
        self.assertEqual(rows[0]["note"], "n1, q")

    def test_kind_column_and_dedupe_on_kind_and_id(self):
        self.add("a.csv", b"id,kind,bucket,note\n1,video,x,\n")
        r = self.add("b.csv", b"id,kind,bucket,note\n1,video,y,\n2,video,y,\n")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("added 1 to ", r.stdout)
        self.assertIn("(2 in pool)", r.stdout)
        self.assertEqual([(x["id"], x["kind"]) for x in self.pool()], [("1", "video"), ("2", "video")])

    def test_excel_export_imports(self):
        r = self.add("x.csv", "﻿Video ID;Bucket;Note\r\n11;a;hi\r\n12;b;\r\n".encode("utf-8"))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("added 2 to ", r.stdout)
        rows = self.pool()
        self.assertEqual([(x["id"], x["bucket"], x["note"]) for x in rows], [("11", "a", "hi"), ("12", "b", "")])

    def test_bad_ids_reported_with_line_numbers(self):
        r = self.add("a.csv", b"video_id,bucket,note\n1,x,\njunk,y,\n3,y,\n,z,\n")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("added 2 to ", r.stdout)
        self.assertIn("line 3: id must match", r.stdout)
        self.assertIn("line 5: id is empty", r.stdout)
        self.assertEqual([x["id"] for x in self.pool()], ["1", "3"])

    def test_unknown_kind_rejected(self):
        r = self.add("a.csv", b"id,kind\n1,video\n2,playlist\n")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("added 1 to ", r.stdout)
        self.assertIn("line 3: unknown kind 'playlist'", r.stdout)
        self.assertEqual([x["id"] for x in self.pool()], ["1"])

    def test_header_without_id_column_is_an_error(self):
        r = self.add("a.csv", b"foo,bar\n1,2\n")
        self.assertEqual(r.returncode, 1)
        self.assertIn("no id column", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.desk, "pool.csv")))

    def test_unreadable_selection_and_uninitialised_desk_are_errors(self):
        r = subprocess.run([sys.executable, ADD, "--desk", self.desk, os.path.join(self.t, "missing.csv")],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 1)
        self.assertTrue(r.stderr.strip())
        r = self.add("a.csv", b"id\n1\n", desk=os.path.join(self.t, "nodesk"))
        self.assertEqual(r.returncode, 1)
        self.assertIn("desk.json", r.stderr)

    def test_desk_flag_is_required(self):
        r = subprocess.run([sys.executable, ADD, os.path.join(self.t, "a.csv")], capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
