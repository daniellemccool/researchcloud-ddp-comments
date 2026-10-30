package template

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// zeeschuimerStores are the object stores of Zeeschuimer's IndexedDB database.
var zeeschuimerStores = []string{"items", "uploads", "nav", "settings"}

// Verify opens the Zeeschuimer extension's IndexedDB in dir and counts its
// object stores, then runs the full release verification: the counts must be
// items 0, uploads 0, navigation records 0 and settings at least 1, the
// storage.local database must exist and hold rows, and the sanitised tree,
// prefs.js, extensions.json, cookies and IndexedDB files must all be within
// the allowlist. Ported from the legacy Python tool's verify_release.
func Verify(dir string, p *platform.Platform, zsVersion string) (Counts, error) {
	if err := checkIDBWal(dir); err != nil {
		return Counts{}, err
	}
	var problems []string
	uuid, err := zeeschuimerUUID(dir)
	if err != nil {
		return Counts{}, err
	}
	if uuid == "" {
		problems = append(problems, "uuid not found in prefs")
	}
	dbs, err := idbDatabases(dir)
	if err != nil {
		return Counts{}, err
	}
	var items []idbDB
	extSet := map[string]bool{}
	for _, db := range dbs {
		if hasAllStores(db.counts) {
			items = append(items, db)
			extSet[db.extDir] = true
		}
	}
	if len(extSet) == 0 {
		return Counts{}, ErrNoStore
	}
	if len(extSet) > 1 {
		return Counts{}, ErrAmbiguousStore
	}
	counts := countsOf(items[0].counts)
	if uuid != "" && items[0].extDir != "moz-extension+++"+uuid {
		problems = append(problems, itemsDB+" database not found")
	}
	switch {
	case counts.Items > 0:
		problems = append(problems, "template has captured items")
	case counts.Uploads > 0:
		problems = append(problems, "template has upload history")
	case counts.Nav > 0:
		problems = append(problems, "template has navigation history")
	case counts.Settings < 1:
		problems = append(problems, "template has no settings (modules were never enabled)")
	}

	ctxDir := "moz-extension+++" + uuid + userContext
	storageOK := false
	for _, db := range dbs {
		if db.extDir == ctxDir && db.name == storageDB && sumCounts(db.counts) > 0 {
			storageOK = true
		}
	}
	if !storageOK {
		problems = append(problems, "extension storage.local database not found or empty")
	}

	if !hasXPI(dir) {
		problems = append(problems, "xpi id missing")
	}
	problems = append(problems, cookieProblems(dir, p)...)
	problems = append(problems, prefProblems(dir)...)
	problems = append(problems, jsonProblems(dir, zsVersion)...)

	exts := extDirs(uuid)
	if len(exts) == 0 {
		exts = []string{items[0].extDir}
	}
	problems = append(problems, idbProblems(dir, exts)...)
	problems = append(problems, treeProblems(dir, exts)...)

	if len(problems) > 0 {
		return counts, errors.New(strings.Join(problems, "; "))
	}
	return counts, nil
}

// hasAllStores reports whether counts holds all of Zeeschuimer's stores.
func hasAllStores(counts map[string]int) bool {
	for _, s := range zeeschuimerStores {
		if _, ok := counts[s]; !ok {
			return false
		}
	}
	return true
}

// countsOf maps store counts onto Counts.
func countsOf(counts map[string]int) Counts {
	return Counts{
		Items:    counts["items"],
		Uploads:  counts["uploads"],
		Nav:      counts["nav"],
		Settings: counts["settings"],
	}
}

// sumCounts sums all store counts.
func sumCounts(counts map[string]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

// hasXPI reports whether Zeeschuimer's xpi is present, either as the file or
// named in extensions.json.
func hasXPI(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(xpiRel))); err == nil && info.Mode().IsRegular() {
		return true
	}
	data, err := os.ReadFile(filepath.Join(dir, "extensions.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), xpiID)
}

// cookieProblems checks cookies.sqlite against the platform's keep list.
func cookieProblems(dir string, p *platform.Platform) []string {
	path := filepath.Join(dir, "cookies.sqlite")
	keep := map[[2]string]bool{}
	for _, c := range p.KeepCookies {
		keep[[2]string{c.Host, c.Name}] = true
	}
	var problems []string
	present := map[[2]string]bool{}
	if _, err := os.Stat(path); err == nil {
		err := withSnapshot(path, func(db *sql.DB) error {
			rows, err := db.Query("SELECT host, name FROM moz_cookies ORDER BY host, name")
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var host, name string
				if err := rows.Scan(&host, &name); err != nil {
					return err
				}
				if !keep[[2]string{host, name}] {
					problems = append(problems, fmt.Sprintf("cookie outside keep list: %s %s", host, name))
				} else {
					present[[2]string{host, name}] = true
				}
			}
			return rows.Err()
		})
		if err != nil {
			problems = append(problems, "cookies.sqlite unreadable: "+err.Error())
		}
	}
	if len(p.KeepCookies) > 0 && len(present) == 0 {
		problems = append(problems, "no keep cookie present")
	}
	return problems
}

// prefProblems reports prefs.js lines that are not allowlisted user_pref lines.
func prefProblems(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "prefs.js"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := prefLineRe.FindStringSubmatch(line)
		if m == nil || !allowPrefs[m[1]] {
			name := line
			if m != nil {
				name = m[1]
			}
			if len(name) > 60 {
				name = name[:60]
			}
			out = append(out, "unexpected pref: "+name)
		}
	}
	return out
}

// jsonProblems reports extensions.json problems, ported from the Python
// tool's _json_problems.
func jsonProblems(dir, zsVersion string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "extensions.json"))
	if err != nil {
		return []string{"extensions.json does not list the xpi"}
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return []string{"extensions.json is not valid JSON"}
	}
	m, ok := parsed.(map[string]any)
	if !ok {
		return []string{"foreign addon id in extensions.json: <addons>", "extensions.json does not list the xpi"}
	}
	addons, ok := m["addons"].([]any)
	if !ok {
		return []string{"foreign addon id in extensions.json: <addons>", "extensions.json does not list the xpi"}
	}
	var out []string
	var mine []map[string]any
	for _, a := range addons {
		am, ok := a.(map[string]any)
		if !ok {
			out = append(out, "foreign addon id in extensions.json: <entry>")
			continue
		}
		if am["id"] == xpiID {
			mine = append(mine, am)
		} else {
			out = append(out, fmt.Sprintf("foreign addon id in extensions.json: %v", am["id"]))
		}
	}
	if len(mine) == 0 {
		out = append(out, "extensions.json does not list the xpi")
	}
	for _, e := range mine {
		out = append(out, entryProblems(e, zsVersion)...)
	}
	return out
}

// entryProblems reports a non-normalised addon entry, ported from the Python
// tool's _entry_problems.
func entryProblems(e map[string]any, zsVersion string) []string {
	var out []string
	bad := func(field string) {
		msg := "extensions.json entry not normalised: " + field
		if !slices.Contains(out, msg) {
			out = append(out, msg)
		}
	}
	if e["path"] != xpiContainerPath {
		bad("path")
	}
	if e["syncGUID"] != syncGUID {
		bad("syncGUID")
	}
	for k := range e {
		if strings.Contains(strings.ToLower(k), "elemetry") {
			bad(k)
		}
	}
	if r, ok := e["rootURI"]; ok && r != "jar:file://"+xpiContainerPath+"!/" {
		bad("rootURI")
	}
	if s, ok := e["sourceURI"]; ok && (zsVersion == "" || s != policyURL(zsVersion)) {
		bad("sourceURI")
	}
	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch t := v.(type) {
		case string:
			if strings.HasPrefix(t, "/") {
				bad(key)
			}
		case map[string]any:
			for kk, vv := range t {
				walk(vv, kk)
			}
		case []any:
			for _, vv := range t {
				walk(vv, key)
			}
		}
	}
	for k, v := range e {
		if k != "path" {
			walk(v, k)
		}
	}
	return out
}

// idbProblems checks the kept extension directories' idb files: no wal, no
// blob files, only the expected database, no free pages, no leftover file
// rows. Ported from the Python tool's _idb_problems.
func idbProblems(dir string, exts []string) []string {
	var problems []string
	for i, ext := range exts {
		label, expected := itemsDB, itemsDB
		if i == 1 {
			label, expected = storageLabel, storageDB
		}
		idbDir := filepath.Join(dir, "storage", "default", ext, "idb")
		fis, err := os.ReadDir(idbDir)
		if err != nil {
			continue
		}
		for _, fi := range fis {
			p := filepath.Join(idbDir, fi.Name())
			switch {
			case strings.HasSuffix(fi.Name(), "-wal"):
				problems = append(problems, label+" has a wal file")
			case strings.HasSuffix(fi.Name(), ".files") && fi.IsDir():
				if entries, _ := os.ReadDir(p); len(entries) > 0 {
					problems = append(problems, label+" has blob files")
				}
			case strings.HasSuffix(fi.Name(), ".sqlite") && fi.Type().IsRegular():
				name, _, ok := readIDB(p)
				if !ok || name != expected {
					problems = append(problems, "unexpected file: storage/default/"+ext+"/idb/"+fi.Name())
					continue
				}
				err := withSnapshot(p, func(db *sql.DB) error {
					var free int
					if err := db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
						return err
					}
					if free != 0 {
						problems = append(problems, label+" has free pages")
					}
					if tableExists(db, "file") {
						var n int
						if err := db.QueryRow("SELECT COUNT(*) FROM file").Scan(&n); err != nil {
							return err
						}
						if n != 0 {
							problems = append(problems, label+" has file rows left after the nav delete")
						}
					}
					return nil
				})
				if err != nil {
					problems = append(problems, label+" unreadable: "+err.Error())
				}
			}
		}
	}
	return problems
}

// treeProblems walks the tree and reports every path the allowlist does not
// cover. Ported from the Python tool's _tree_problems.
func treeProblems(dir string, exts []string) []string {
	okDirs := map[string]bool{
		"extensions":      true,
		"chrome":          true,
		"storage":         true,
		"storage/default": true,
	}
	for _, e := range exts {
		okDirs["storage/default/"+e] = true
		okDirs["storage/default/"+e+"/idb"] = true
	}
	alts := make([]string, len(exts))
	for i, e := range exts {
		alts[i] = regexp.QuoteMeta("storage/default/" + e + "/idb")
	}
	alt := strings.Join(alts, "|")
	if alt == "" {
		alt = "(?!)"
	}
	filesDirRe := regexp.MustCompile(`^(?:` + alt + `)/[^/]+\.files$`)
	idbFileRe := regexp.MustCompile(`^(?:` + alt + `)/[^/]+\.sqlite(?:-wal)?$`)
	blobRe := regexp.MustCompile(`^(?:` + alt + `)/[^/]+\.files/[^/]+$`)
	dirOK := func(r string) bool { return okDirs[r] || filesDirRe.MatchString(r) }
	fileOK := func(r string) bool {
		return allowFiles[r] || r == xpiRel || idbFileRe.MatchString(r) || blobRe.MatchString(r)
	}

	var bad []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		r := filepath.ToSlash(rel)
		if d.IsDir() {
			if !dirOK(r) {
				bad = append(bad, entriesUnder(path, r)...)
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || !fileOK(r) {
			bad = append(bad, r)
		}
		return nil
	})
	sort.Strings(bad)
	bad = slices.Compact(bad)
	out := make([]string, 0, len(bad))
	for _, r := range bad {
		out = append(out, "unexpected file: "+r)
	}
	return out
}

// entriesUnder lists every file, symlink and empty directory below path,
// relative to the tree with rel as prefix.
func entriesUnder(path, rel string) []string {
	var out []string
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if r == "." {
			return nil
		}
		pre := rel + "/" + filepath.ToSlash(r)
		if d.Type()&os.ModeSymlink != 0 {
			out = append(out, pre)
			return nil
		}
		if d.IsDir() {
			if entries, err := os.ReadDir(p); err == nil && len(entries) == 0 {
				out = append(out, pre)
			}
			return nil
		}
		out = append(out, pre)
		return nil
	})
	return out
}
