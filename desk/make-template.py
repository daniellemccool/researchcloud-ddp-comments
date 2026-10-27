#!/usr/bin/env python3
"""Freeze a prepared Firefox /config as the session template, stripped of identity.

    make-template.py --fqdn HOST SRC_CONFIG_DIR OUT_TAR

SRC_CONFIG_DIR is a *stopped* jlesage Firefox /config (e.g. ~/spike/state/firefox-1).
The copy keeps Zeeschuimer's extension storage and two cookies: the 4CAT
`remember_token` for HOST (the shared collector login) and TikTok's `cookie-consent`
(the banner choice, no identifier). It drops every other cookie (SRAM `Authorization`,
IdP sessions, TikTok device ids), session-restore files (which
can hold session cookies), non-extension site storage, and caches. The source is never
modified. OUT_TAR is owned by uid 1000, ready for make-sessions.sh to unpack.
"""
import argparse, glob, os, shutil, sqlite3, sys, tarfile, tempfile

KEEP_COOKIE = "remember_token"
TIKTOK_CONSENT_HOSTS = (".tiktok.com", "www.tiktok.com", ".www.tiktok.com")
SKIP_DIRS = {"cache2", "startupCache", "sessionstore-backups", "crashes", "minidumps", "saved-telemetry-pings"}
SKIP_FILES = ("sessionstore.jsonlz4", "recovery.jsonlz4", "recovery.baklz4")


def strip(profile, fqdn):
    db = os.path.join(profile, "cookies.sqlite")
    if os.path.exists(db):
        c = sqlite3.connect(db)
        c.execute("DELETE FROM moz_cookies WHERE NOT ((host = ? AND name = ?) OR (host IN (?, ?, ?) AND name = 'cookie-consent'))",
                  (fqdn, KEEP_COOKIE, *TIKTOK_CONSENT_HOSTS))
        c.commit()
        c.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        c.execute("VACUUM")
        c.close()
        for side in ("-wal", "-shm"):
            if os.path.exists(db + side):
                os.remove(db + side)
    for d in glob.glob(os.path.join(profile, "storage", "default", "*")):
        if not os.path.basename(d).startswith("moz-extension+++"):
            shutil.rmtree(d, ignore_errors=True)
    for f in SKIP_FILES:
        p = os.path.join(profile, f)
        if os.path.exists(p):
            os.remove(p)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fqdn", required=True)
    ap.add_argument("src"); ap.add_argument("out")
    a = ap.parse_args()
    if not os.path.isdir(os.path.join(a.src, "profile")):
        sys.exit(f"{a.src} has no profile/ directory")
    if os.path.exists(os.path.join(a.src, "profile", "lock")) and os.path.islink(os.path.join(a.src, "profile", "lock")):
        sys.exit("profile is locked: stop the Firefox container first")
    with tempfile.TemporaryDirectory() as t:
        work = os.path.join(t, "config")
        shutil.copytree(a.src, work, symlinks=True,
                        ignore=lambda d, names: [n for n in names if n in SKIP_DIRS or n in ("lock", ".parentlock")])
        strip(os.path.join(work, "profile"), a.fqdn)
        os.makedirs(os.path.dirname(os.path.abspath(a.out)), exist_ok=True)

        def owned(ti):
            ti.uid = ti.gid = 1000; ti.uname = ti.gname = ""
            return ti
        with tarfile.open(a.out, "w") as tar:
            for name in sorted(os.listdir(work)):
                tar.add(os.path.join(work, name), arcname=name, filter=owned)
    print(f"template written to {a.out} ({os.path.getsize(a.out) // (1024 * 1024)} MB)")


if __name__ == "__main__":
    main()
