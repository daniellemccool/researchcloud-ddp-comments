package main

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// bareID is the shape of every sent-list line.
var bareID = regexp.MustCompile(`^[0-9]+$`)

// readSent returns the IDs on the sent list at path; a missing file is an
// empty list. A line that is not a bare numeric ID is an error naming the
// line number only, never its content.
func readSent(path string) (map[string]struct{}, error) {
	ids := map[string]struct{}{}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sent list: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if !bareID.MatchString(sc.Text()) {
			return nil, fmt.Errorf("sent list %s: line %d is not a bare numeric ID", path, n)
		}
		ids[sc.Text()] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sent list %s: %w", path, err)
	}
	return ids, nil
}

// numeric orders bare IDs ascending as numbers: by length once leading zeros
// are dropped, then by digits, then by the full string.
func numeric(a, b string) int {
	ta, tb := strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(ta) != len(tb) {
		return len(ta) - len(tb)
	}
	if c := strings.Compare(ta, tb); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// writeSent writes ids to path, one per line, sorted ascending as numbers so
// the list never keeps any history order. The write is atomic: a temporary
// file in the same folder, synced, then renamed over path.
func writeSent(path string, ids map[string]struct{}) error {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	slices.SortFunc(list, numeric)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sent-ids-*.tmp")
	if err != nil {
		return fmt.Errorf("sent list: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	w := bufio.NewWriter(tmp)
	for _, id := range list {
		if _, err := w.WriteString(id + "\n"); err != nil {
			return fmt.Errorf("sent list: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("sent list: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sent list: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sent list: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("sent list: %w", err)
	}
	ok = true
	return nil
}

// shuffled returns ids in a uniformly random order: sorted first, so the
// result never depends on how the IDs arrived, then a Fisher–Yates shuffle
// drawing every index from crypto/rand.
func shuffled(ids map[string]struct{}) ([]string, error) {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	slices.SortFunc(list, numeric)
	for i := len(list) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return nil, fmt.Errorf("shuffle: %w", err)
		}
		k := j.Int64()
		list[i], list[k] = list[k], list[i]
	}
	return list, nil
}

// writeUploads writes ids, in the order given, to outDir as
// pool-upload-<stamp>-<k>.csv (k from 1): header `id`, then at most maxRows
// IDs per file, creating outDir if needed. It never overwrites a file. On any error it removes every
// file it created and returns no paths.
func writeUploads(outDir, stamp string, ids []string, maxRows int) (paths []string, err error) {
	if maxRows < 1 {
		return nil, fmt.Errorf("max rows must be at least 1, not %d", maxRows)
	}
	defer func() {
		if err != nil {
			for _, p := range paths {
				_ = os.Remove(p)
			}
			paths = nil
		}
	}()
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("output folder: %w", err)
	}
	for k, start := 1, 0; start < len(ids); k, start = k+1, start+maxRows {
		end := min(start+maxRows, len(ids))
		p := filepath.Join(outDir, fmt.Sprintf("pool-upload-%s-%d.csv", stamp, k))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return paths, fmt.Errorf("upload file: %w", err)
		}
		paths = append(paths, p)
		if err := writeRows(f, ids[start:end]); err != nil {
			return paths, fmt.Errorf("upload file %s: %w", p, err)
		}
	}
	return paths, nil
}

// writeRows writes the header and rows to f and closes it.
func writeRows(f *os.File, rows []string) error {
	w := bufio.NewWriter(f)
	_, _ = w.WriteString("id\n")
	for _, id := range rows {
		_, _ = w.WriteString(id + "\n")
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
