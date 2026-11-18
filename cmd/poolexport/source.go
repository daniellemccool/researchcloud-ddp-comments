package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxEntryBytes is the most an export file may hold uncompressed: a guard
// against a zip bomb, checked on the declared size and on the bytes read.
const maxEntryBytes = 1 << 30

// participantField is the participant field of an export file name: the text
// after participant= up to the next _.
var participantField = regexp.MustCompile(`(^|_)participant=([^_]+)_`)

// isTestParticipant reports whether base, an export file's base name, belongs
// to the study's test participant (participant=test exactly; not tester, not
// test2). Such files hold synthetic IDs and are never read.
func isTestParticipant(base string) bool {
	m := participantField.FindStringSubmatch(base)
	return m != nil && m[2] == "test"
}

// errTooLarge is a file that holds more than the read limit.
var errTooLarge = errors.New("larger than 1 GiB, refused")

// entry is one regular *-tiktok.json file of an export, in a folder or a zip.
type entry struct {
	name   string // sort key only; never printed
	size   int64  // uncompressed size in bytes (declared, for a zip); -1 when unknown
	refuse string // non-empty: refused without reading, for this reason
	open   func() (io.ReadCloser, error)
}

// source is an opened export: its regular *-tiktok.json entries sorted by name
// in byte order, and the count of *-tiktok.json entries that are not regular
// files (symlinks, folders), which are ignored, and of participant=test files,
// which are skipped unread.
type source struct {
	entries     []entry
	nonRegular  int
	testSkipped int
	close       func() error
}

// openExport opens the export at p: a folder (its *-tiktok.json files, not
// recursive) or a zip (its *-tiktok.json entries at any depth, read in
// memory, never extracted to disk).
func openExport(p string) (source, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return source{}, fmt.Errorf("export: %w", err)
	}
	if fi.IsDir() {
		return folderSource(p)
	}
	zr, err := zip.OpenReader(p)
	if err != nil && (zr == nil || !errors.Is(err, zip.ErrInsecurePath)) {
		return source{}, fmt.Errorf("export %s is neither a folder nor a readable zip file: %w", p, err)
	}
	return zipSource(zr), nil
}

func folderSource(dir string) (source, error) {
	src := source{close: func() error { return nil }}
	des, err := os.ReadDir(dir)
	if err != nil {
		return src, fmt.Errorf("read the export folder: %w", err)
	}
	for _, de := range des {
		if !strings.HasSuffix(de.Name(), tiktokSuffix) {
			continue
		}
		if isTestParticipant(de.Name()) {
			src.testSkipped++
			continue
		}
		if !de.Type().IsRegular() {
			src.nonRegular++
			continue
		}
		e := entry{name: de.Name(), size: -1}
		if info, err := de.Info(); err == nil {
			e.size = info.Size()
		}
		if e.size > maxEntryBytes {
			e.refuse = errTooLarge.Error()
		}
		full := filepath.Join(dir, de.Name())
		e.open = func() (io.ReadCloser, error) { return os.Open(full) }
		src.entries = append(src.entries, e)
	}
	sortEntries(src.entries)
	return src, nil
}

func zipSource(zr *zip.ReadCloser) source {
	src := source{close: zr.Close}
	for _, f := range zr.File {
		if !strings.HasSuffix(path.Base(f.Name), tiktokSuffix) {
			continue
		}
		if isTestParticipant(path.Base(f.Name)) {
			src.testSkipped++
			continue
		}
		if !f.Mode().IsRegular() {
			src.nonRegular++
			continue
		}
		e := entry{name: f.Name, size: -1, open: f.Open}
		if f.UncompressedSize64 <= 1<<62 {
			e.size = int64(f.UncompressedSize64)
		}
		switch {
		case f.Flags&0x1 != 0:
			e.refuse = "encrypted, refused"
		case f.Method != zip.Store && f.Method != zip.Deflate:
			e.refuse = fmt.Sprintf("unsupported compression method %d, refused", f.Method)
		case f.UncompressedSize64 > maxEntryBytes:
			e.refuse = errTooLarge.Error()
		}
		src.entries = append(src.entries, e)
	}
	sortEntries(src.entries)
	return src
}

// sortEntries sorts by full name in byte order, as LC_ALL=C sort does.
func sortEntries(es []entry) {
	sort.SliceStable(es, func(i, j int) bool { return es[i].name < es[j].name })
}

// read returns the entry's bytes, reading at most limit+1 of them, so an entry
// whose declared size understates its content (a data-descriptor entry's local
// header carries no sizes) still stops at the limit: more than limit bytes is
// errTooLarge. Errors never carry the entry's path or name.
func (e entry) read(limit int64) ([]byte, error) {
	rc, err := e.open()
	if err != nil {
		return nil, bare(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, bare(err)
	}
	if int64(len(b)) > limit {
		return nil, errTooLarge
	}
	return b, nil
}

// bare strips the path from an *os.PathError, which would name the file.
func bare(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
