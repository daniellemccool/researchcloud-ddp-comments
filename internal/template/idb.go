package template

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; depguard allows it only here
)

// zeeschuimerStores are the object stores of Zeeschuimer's IndexedDB database.
var zeeschuimerStores = []string{"items", "uploads", "nav", "settings"}

// findStore locates the one extension directory under storage/default whose
// IndexedDB holds Zeeschuimer's object stores. It returns the extension
// directory relative to dir and the sqlite file holding the stores.
func findStore(dir string) (extRel, idbFile string, err error) {
	base := filepath.Join(dir, "storage", "default")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", "", ErrNoStore
	}
	var foundExt, foundFile string
	count := 0
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
			ok, err := hasZeeschuimerStores(p)
			if err != nil || !ok {
				continue
			}
			count++
			foundExt = "storage/default/" + e.Name()
			foundFile = p
			break
		}
	}
	switch {
	case count == 0:
		return "", "", ErrNoStore
	case count > 1:
		return "", "", ErrAmbiguousStore
	}
	return foundExt, foundFile, nil
}

// hasZeeschuimerStores reports whether the sqlite file at path is an IndexedDB
// database holding all of Zeeschuimer's object stores.
func hasZeeschuimerStores(path string) (bool, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT name FROM object_store")
	if err != nil {
		return false, nil // not an IndexedDB file
	}
	defer func() { _ = rows.Close() }()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, s := range zeeschuimerStores {
		if !names[s] {
			return false, nil
		}
	}
	return true, nil
}

// countStores counts the object_data rows of each object store in the
// Zeeschuimer IndexedDB file.
func countStores(idbFile string) (Counts, error) {
	db, err := sql.Open("sqlite", "file:"+idbFile+"?mode=ro")
	if err != nil {
		return Counts{}, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT s.name, COUNT(d.object_store_id) FROM object_store s " +
		"LEFT JOIN object_data d ON d.object_store_id = s.id GROUP BY s.id, s.name")
	if err != nil {
		return Counts{}, err
	}
	defer func() { _ = rows.Close() }()
	var c Counts
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return Counts{}, err
		}
		switch name {
		case "items":
			c.Items = n
		case "uploads":
			c.Uploads = n
		case "nav":
			c.Nav = n
		case "settings":
			c.Settings = n
		}
	}
	return c, rows.Err()
}

// emptyNavStore deletes every row of the nav object store (and its index
// rows), then rebuilds the file with VACUUM INTO so the deleted rows leave no
// trace, and replaces the original.
func emptyNavStore(idbFile string) error {
	db, err := sql.Open("sqlite", idbFile)
	if err != nil {
		return err
	}
	// Firefox's IndexedDB triggers call the browser-internal update_refcount
	// function; while they exist, plain SQLite cannot even prepare a statement
	// on object_data. Drop them for the delete and recreate them afterwards
	// so the rebuilt file keeps Firefox's schema.
	triggers, err := readTriggers(db)
	if err != nil {
		_ = db.Close()
		return err
	}
	for _, tr := range triggers {
		if _, err := db.Exec("DROP TRIGGER " + quoteIdent(tr.name)); err != nil {
			_ = db.Close()
			return err
		}
	}
	if _, err := db.Exec("DELETE FROM object_data WHERE object_store_id = " +
		"(SELECT id FROM object_store WHERE name = 'nav')"); err != nil {
		_ = db.Close()
		return err
	}
	for _, table := range []string{"index_data", "unique_index_data"} {
		var one int
		err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&one)
		if err != nil {
			continue // table absent
		}
		if _, err := db.Exec("DELETE FROM " + table + " WHERE object_store_id = " +
			"(SELECT id FROM object_store WHERE name = 'nav')"); err != nil {
			_ = db.Close()
			return err
		}
	}
	for _, tr := range triggers {
		if _, err := db.Exec(tr.sql); err != nil {
			_ = db.Close()
			return err
		}
	}
	tmpDir, err := os.MkdirTemp("", "ddp-idb-")
	if err != nil {
		_ = db.Close()
		return err
	}
	rebuilt := filepath.Join(tmpDir, "rebuilt.sqlite")
	query := "VACUUM INTO '" + strings.ReplaceAll(rebuilt, "'", "''") + "'"
	if _, err := db.Exec(query); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(tmpDir)
		return err
	}
	if err := db.Close(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return err
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Remove(idbFile + side)
	}
	if err := os.Rename(rebuilt, idbFile); err != nil {
		_ = os.RemoveAll(tmpDir)
		return err
	}
	return os.RemoveAll(tmpDir)
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
		return nil // no storage/default: findStore reports ErrNoStore
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
				return fmt.Errorf("%w: %s", ErrLocked, filepath.Join("storage", "default", e.Name(), "idb", fi.Name()))
			}
		}
	}
	return nil
}
