package pool

import (
	"errors"
	"os"
	"sync"
	"syscall"
)

// Cache keeps the parsed pool file in memory until the file changes, so a
// claim or a page view does not re-read and re-check every row (ruling F124).
// The file is taken as unchanged while os.Stat gives the same size,
// modification time (ns), inode and change time (ns) as at the last read; the
// change time catches a hand edit in place that restores the old mtime. Add
// replaces the file
// by rename, so every write gives it a new inode. Path, DefaultKind and Valid
// are set before the first Snapshot and never changed; a nil Valid accepts
// every row. A Cache is safe for concurrent use.
type Cache struct {
	Path        string
	DefaultKind string
	Valid       func(kind, id string) bool

	mu   sync.Mutex
	key  fileKey
	snap *Snapshot // nil: nothing cached
	// read is Read; the tests count the reads through it.
	read func(path, defaultKind string) ([]Row, error)
}

// fileKey is what tells one version of the pool file from another.
type fileKey struct {
	size  int64
	mtime int64 // ns
	ino   uint64
	// ctime (ns) is set by the kernel on every change, so an overwrite in
	// place that keeps the size and puts the old mtime back (`cp -p`,
	// `touch -r`) still changes the key.
	ctime int64
}

// Snapshot is one read of the pool file. All is the file's rows as Read
// returns them (file order); Valid is the rows Valid accepts, in file order.
// Both are shared by every caller and read-only: never modify them.
type Snapshot struct {
	All   []Row
	Valid []Row

	notes map[[2]string]string // (kind, id) → note, for the Valid rows with a note
}

// Snapshot returns the pool as it is now: the cached snapshot while the file
// is unchanged, else a new read under the cache's lock (concurrent callers
// wait for that one read). A missing file is an empty pool, as Read has it. A
// read error is returned with no snapshot, and the old one is dropped: it is
// never served for a file that has changed.
func (c *Cache) Snapshot() (*Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fi, err := os.Stat(c.Path)
	if err != nil {
		c.snap = nil
		if errors.Is(err, os.ErrNotExist) {
			return &Snapshot{}, nil
		}
		return nil, err
	}
	key := keyOf(fi)
	if c.snap != nil && key == c.key {
		return c.snap, nil
	}
	c.snap = nil
	read := c.read
	if read == nil {
		read = Read
	}
	// Stat came first: a file replaced during the read is cached under the
	// older key, so the next call reads again rather than keep stale rows.
	rows, err := read(c.Path, c.DefaultKind)
	if err != nil {
		return nil, err
	}
	c.snap, c.key = newSnapshot(rows, c.Valid), key
	return c.snap, nil
}

func keyOf(fi os.FileInfo) fileKey {
	k := fileKey{size: fi.Size(), mtime: fi.ModTime().UnixNano()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		k.ino = st.Ino
		k.ctime = st.Ctim.Sec*1e9 + st.Ctim.Nsec
	}
	return k
}

// newSnapshot builds the snapshot of rows. The slices are capped at their
// length, so an append by a caller copies instead of writing into a backing
// array another caller reads; when every row is valid, Valid shares All's.
func newSnapshot(rows []Row, valid func(kind, id string) bool) *Snapshot {
	all := rows[:len(rows):len(rows)]
	s := &Snapshot{All: all, Valid: all}
	if valid != nil {
		var out []Row // nil while every row so far is valid
		for i, r := range all {
			ok := valid(r.Kind, r.ID)
			switch {
			case !ok && out == nil:
				out = append(make([]Row, 0, len(all)-1), all[:i]...)
			case ok && out != nil:
				out = append(out, r)
			}
		}
		if out != nil {
			s.Valid = out[:len(out):len(out)]
		}
	}
	for _, r := range s.Valid {
		if r.Note == "" {
			continue
		}
		if s.notes == nil {
			s.notes = make(map[[2]string]string)
		}
		s.notes[[2]string{r.Kind, r.ID}] = r.Note
	}
	return s
}

// Note returns the note of the valid row (kind, id) and true; "" and false
// when no valid row has that key or its note is empty. Only rows with a note
// are indexed, so a pool of bare IDs costs no index at all.
func (s *Snapshot) Note(kind, id string) (string, bool) {
	note, ok := s.notes[[2]string{kind, id}]
	return note, ok
}
