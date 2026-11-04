// Package pool is the shared pool of items to collect: <DeskRoot>/pool.csv,
// header id,kind,bucket,note, stdlib only. The legacy header was
// video_id,bucket,note; its rows get the platform's default kind.
//
// Locking contract: Add does an unlocked read-modify-write, so the CALLER must
// hold the desk's global in-process lock around it. Every write replaces the
// whole file atomically (temp file in the same directory, chmod 0644,
// rename), so a reader sees either the old or the new content, never a
// partial file.
package pool

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Row is one pool item.
type Row struct {
	ID     string
	Kind   string
	Bucket string
	Note   string
}

// Kinds describes the platform's item kinds: the default kind and, per kind,
// the regexp an id must match. Kind returns false for an unknown kind.
type Kinds interface {
	Default() string
	Kind(kind string) (*regexp.Regexp, bool)
}

// header is the modern pool.csv header; writeAll always writes it, which also
// upgrades a legacy video_id,bucket,note file.
var header = []string{"id", "kind", "bucket", "note"}

// Read returns the rows of the pool file at path. A missing file is not an
// error: nil, nil. The header must hold an id or a legacy video_id column,
// else the error is `pool.csv: unrecognised header [<names>]`. Legacy rows
// get defaultKind (a row's own kind cell, when present and non-empty, wins
// otherwise); empty ids are skipped; duplicates on (kind, id) are skipped
// keeping the first; a leading BOM is stripped and a final line without a
// newline is a complete row.
func Read(path string, defaultKind string) ([]Row, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	names := records[0]
	hasID, hasVideoID := false, false
	for _, name := range names {
		if name == "id" {
			hasID = true
		}
		if name == "video_id" {
			hasVideoID = true
		}
	}
	if !hasID && !hasVideoID {
		return nil, fmt.Errorf("pool.csv: unrecognised header %v", names)
	}
	legacy := !hasID
	column := func(name string) int {
		for i, n := range names {
			if n == name {
				return i
			}
		}
		return -1
	}
	idCol := column("id")
	if legacy {
		idCol = column("video_id")
	}
	kindCol, bucketCol, noteCol := column("kind"), column("bucket"), column("note")
	cell := func(rec []string, i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return rec[i]
	}
	var rows []Row
	seen := make(map[[2]string]bool)
	for _, rec := range records[1:] {
		id := strings.TrimSpace(cell(rec, idCol))
		kind := defaultKind
		if !legacy {
			if k := strings.TrimSpace(cell(rec, kindCol)); k != "" {
				kind = k
			}
		}
		if id == "" {
			continue
		}
		key := [2]string{kind, id}
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, Row{ID: id, Kind: kind, Bucket: cell(rec, bucketCol), Note: cell(rec, noteCol)})
	}
	return rows, nil
}

// Add appends the rows not already in the pool file at path, deduping on
// (kind, id) both against the file and within the batch. A row whose kind is
// unknown or whose id does not match the kind's pattern is skipped, never
// written. When at least one row is fresh the whole file is rewritten (header
// + all rows), which also upgrades a legacy header. Returns (added, total)
// where total is the row count after the write. The CALLER must hold the
// global lock.
func Add(path string, rows []Row, kinds Kinds) (added, total int, err error) {
	existing, err := Read(path, kinds.Default())
	if err != nil {
		return 0, 0, err
	}
	seen := make(map[[2]string]bool, len(existing)+len(rows))
	for _, row := range existing {
		seen[[2]string{row.Kind, row.ID}] = true
	}
	var fresh []Row
	for _, row := range rows {
		pattern, ok := kinds.Kind(row.Kind)
		if !ok || !fullMatch(pattern, row.ID) {
			continue
		}
		key := [2]string{row.Kind, row.ID}
		if seen[key] {
			continue
		}
		seen[key] = true
		fresh = append(fresh, row)
	}
	if len(fresh) > 0 {
		if err := writeAll(path, append(existing, fresh...)); err != nil {
			return 0, 0, err
		}
	}
	return len(fresh), len(existing) + len(fresh), nil
}

// fullMatch reports whether s matches the whole of re (Python's fullmatch).
func fullMatch(re *regexp.Regexp, s string) bool {
	return regexp.MustCompile(`^(?:` + re.String() + `)$`).MatchString(s)
}

// writeAll replaces the whole pool file atomically: temp file in the same
// directory, chmod 0644, rename over path. On any error the temp file is
// removed and path keeps its old content.
func writeAll(path string, rows []Row) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pool.csv.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	w := csv.NewWriter(tmp)
	if err = w.Write(header); err != nil {
		_ = tmp.Close()
		return err
	}
	for _, row := range rows {
		if err = w.Write([]string{row.ID, row.Kind, row.Bucket, row.Note}); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	w.Flush()
	if err = w.Error(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
