package web

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
)

// collection is the whole collection's figures on the admin page. Queue is
// the pool rows this platform can open; Claimed, of those, the ones anyone
// ever claimed; Done, of the claimed, the ones a claimant opened
// successfully or reported as a problem; Problems, of the claimed, the ones
// a claimant reported as a problem; Collectors, the distinct users with a
// claim. Unclaimed is Queue − Claimed and open is Claimed − Done.
type collection struct {
	Queue, Claimed, Done, Problems, Collectors int
}

// collect counts the collection over rows (already limited to what the
// platform can open) and the desk log's events. Whether an item is done is
// its claimant's own view of it (desklog.Log.ViewOf), the rule the
// student's page follows: another person's open or report marks nothing.
func collect(rows []pool.Row, evs []desklog.Event, l *desklog.Log) collection {
	claimants := make(map[desklog.Key][]string)
	collectors := make(map[string]bool)
	for _, e := range evs {
		if e.Action != "claim" {
			continue
		}
		collectors[e.User] = true
		k := l.KeyOf(e)
		if !slices.Contains(claimants[k], e.User) {
			claimants[k] = append(claimants[k], e.User)
		}
	}
	views := make(map[string]desklog.View)
	view := func(user string) desklog.View {
		v, ok := views[user]
		if !ok {
			v = l.ViewOf(user, evs)
			views[user] = v
		}
		return v
	}
	c := collection{Queue: len(rows), Collectors: len(collectors)}
	for _, row := range rows {
		k := desklog.Key{Kind: row.Kind, ID: row.ID}
		users := claimants[k]
		if len(users) == 0 {
			continue
		}
		c.Claimed++
		done, problem := false, false
		for _, u := range users {
			v := view(u)
			done = done || v.Done[k]
			if _, ok := v.Problems[k]; ok {
				problem = true
			}
		}
		if done {
			c.Done++
		}
		if problem {
			c.Problems++
		}
	}
	return c
}

// recordCounter totals the lines of every person's captures file
// (capturesPath), one line per captured record, repeats and error records
// included. Each file's count is remembered against its size and
// modification time, so a page load reads only the files that changed. The
// zero value is ready to use and safe for concurrent page loads.
type recordCounter struct {
	mu        sync.Mutex
	seen      map[string]fileLines // by path
	filesRead int                  // how many files were read (counted), for the tests
}

// fileLines is one captures file's remembered line count.
type fileLines struct {
	size  int64
	mod   time.Time
	lines int64
}

// Total returns the number of lines in state/captures/<user>/captures.ndjson
// under deskRoot, over every user. A missing captures directory is 0 and no
// error; a user directory without a captures file counts nothing.
func (c *recordCounter) Total(deskRoot string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ents, err := os.ReadDir(filepath.Join(deskRoot, "state", "captures"))
	if errors.Is(err, os.ErrNotExist) {
		c.seen = nil
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	next := make(map[string]fileLines, len(ents))
	var total int64
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		path := capturesPath(deskRoot, e.Name())
		st, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !st.Mode().IsRegular() {
			continue
		}
		fl, ok := c.seen[path]
		if !ok || fl.size != st.Size() || !fl.mod.Equal(st.ModTime()) {
			n, err := countLines(path, st.Size())
			if err != nil {
				return 0, err
			}
			c.filesRead++
			fl = fileLines{size: st.Size(), mod: st.ModTime(), lines: n}
		}
		next[path] = fl
		total += fl.lines
	}
	c.seen = next
	return total, nil
}

// countLines counts the newlines in the first size bytes of the file at
// path, so the count matches the size it is remembered against even while
// the file grows.
func countLines(path string, size int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	var n int64
	buf := make([]byte, 64<<10)
	r := io.LimitReader(f, size)
	for {
		k, err := r.Read(buf)
		n += int64(bytes.Count(buf[:k], []byte{'\n'}))
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
}
