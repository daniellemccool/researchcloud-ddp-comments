package template

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; depguard allows it only here
)

// idbDB is one IndexedDB database file found under an extension directory.
type idbDB struct {
	extDir string // e.g. "moz-extension+++<uuid>"
	file   string // absolute path of the sqlite file
	name   string // the database name recorded in its `database` table
	counts map[string]int
}

// idbDatabases lists every IndexedDB sqlite file under storage/default's
// moz-extension+++* directories, with its database name and object-store row
// counts. Files that are not IndexedDB databases are skipped.
func idbDatabases(dir string) ([]idbDB, error) {
	base := filepath.Join(dir, "storage", "default")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, ErrNoStore
	}
	var out []idbDB
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "moz-extension+++") {
			continue
		}
		idbDir := filepath.Join(base, e.Name(), "idb")
		fis, err := os.ReadDir(idbDir)
		if err != nil {
			continue
		}
		for _, fi := range fis {
			if !strings.HasSuffix(fi.Name(), ".sqlite") || !fi.Type().IsRegular() {
				continue
			}
			p := filepath.Join(idbDir, fi.Name())
			name, counts, ok := readIDB(p)
			if !ok {
				continue
			}
			out = append(out, idbDB{extDir: e.Name(), file: p, name: name, counts: counts})
		}
	}
	return out, nil
}

// readIDB reads the database name and object-store counts of an IndexedDB
// file through a temp copy, so the original is never touched.
func readIDB(path string) (string, map[string]int, bool) {
	var name string
	counts := map[string]int{}
	err := withSnapshot(path, func(db *sql.DB) error {
		if err := db.QueryRow("SELECT name FROM database").Scan(&name); err != nil {
			return err
		}
		rows, err := db.Query("SELECT s.name, COUNT(d.object_store_id) FROM object_store s " +
			"LEFT JOIN object_data d ON d.object_store_id = s.id GROUP BY s.id, s.name")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var n string
			var c int
			if err := rows.Scan(&n, &c); err != nil {
				return err
			}
			counts[n] = c
		}
		return rows.Err()
	})
	if err != nil {
		return "", nil, false
	}
	return name, counts, true
}

// withSnapshot copies a sqlite file (with its -wal/-shm siblings) to a temp
// dir and runs fn on the copy.
func withSnapshot(path string, fn func(*sql.DB) error) error {
	tmpDir, err := os.MkdirTemp("", "ddp-snap-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	dst := filepath.Join(tmpDir, "db.sqlite")
	if err := copyFile(path, dst, 0o644); err != nil {
		return err
	}
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + side); err == nil {
			if err := copyFile(path+side, dst+side, 0o644); err != nil {
				return err
			}
		}
	}
	db, err := sql.Open("sqlite", dst)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return fn(db)
}

// rebuildIDB rewrites an IndexedDB file so deleted rows (free pages, WAL)
// leave no trace: it optionally clears the nav store, checkpoints the WAL,
// rebuilds the file with VACUUM INTO a sibling temp file, and replaces the
// original.
func rebuildIDB(path string, clearNav bool) error {
	tmp := path + ".rebuilt"
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	if clearNav {
		if err := clearNavRows(db); err != nil {
			_ = db.Close()
			return err
		}
	}
	_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	query := "VACUUM INTO '" + strings.ReplaceAll(tmp, "'", "''") + "'"
	if _, err := db.Exec(query); err != nil {
		_ = db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Remove(path + side)
	}
	return os.Rename(tmp, path)
}

// clearNavRows deletes every row of the nav object store and its index rows.
// Firefox's triggers on those tables call the browser-internal
// update_refcount function, which plain SQLite cannot even prepare a
// statement against, so they are dropped for the delete and recreated
// afterwards; file rows left without a referencing object_data row are
// deleted as well.
func clearNavRows(db *sql.DB) error {
	triggers, err := readTriggers(db)
	if err != nil {
		return err
	}
	for _, tr := range triggers {
		if _, err := db.Exec("DROP TRIGGER " + quoteIdent(tr.name)); err != nil {
			return err
		}
	}
	if _, err := db.Exec("DELETE FROM object_data WHERE object_store_id = " +
		"(SELECT id FROM object_store WHERE name = 'nav')"); err != nil {
		return err
	}
	for _, table := range []string{"index_data", "unique_index_data"} {
		if !tableExists(db, table) {
			continue
		}
		if _, err := db.Exec("DELETE FROM " + table + " WHERE object_store_id = " +
			"(SELECT id FROM object_store WHERE name = 'nav')"); err != nil {
			return err
		}
	}
	if tableExists(db, "file") {
		if _, err := db.Exec("DELETE FROM file WHERE NOT EXISTS (SELECT 1 FROM object_data " +
			"WHERE (',' || file_ids || ',') LIKE ('%,' || id || ',%'))"); err != nil {
			return err
		}
	}
	for _, tr := range triggers {
		if _, err := db.Exec(tr.sql); err != nil {
			return err
		}
	}
	return nil
}

// readTriggers returns the triggers on the tables the nav delete touches.
func readTriggers(db *sql.DB) ([]struct{ name, sql string }, error) {
	rows, err := db.Query("SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND " +
		"tbl_name IN ('object_data', 'index_data', 'unique_index_data')")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []struct{ name, sql string }
	for rows.Next() {
		var tr struct{ name, sql string }
		if err := rows.Scan(&tr.name, &tr.sql); err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// tableExists reports whether the sqlite connection has the named table.
func tableExists(db *sql.DB, name string) bool {
	var one int
	return db.QueryRow("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&one) == nil
}

// quoteIdent quotes an SQL identifier.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// checkIDBWal refuses a profile whose IndexedDB still has a write-ahead log:
// the browser was not shut down cleanly.
func checkIDBWal(dir string) error {
	base := filepath.Join(dir, "storage", "default")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil // no storage/default: idbDatabases reports ErrNoStore
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "moz-extension+++") {
			continue
		}
		idbDir := filepath.Join(base, e.Name(), "idb")
		fis, err := os.ReadDir(idbDir)
		if err != nil {
			continue
		}
		for _, fi := range fis {
			if strings.HasSuffix(fi.Name(), "-wal") {
				return fmt.Errorf("%w: %s", ErrLocked,
					filepath.Join("storage", "default", e.Name(), "idb", fi.Name()))
			}
		}
	}
	return nil
}
