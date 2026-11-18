package pool

import (
	"bufio"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// countingCache returns a cache over path whose reads are counted; each read
// waits pause first, so concurrent callers overlap.
func countingCache(path string, pause time.Duration) (*Cache, *atomic.Int64) {
	var reads atomic.Int64
	c := &Cache{Path: path, DefaultKind: "video", Valid: videoValid}
	c.read = func(p, kind string) ([]Row, error) {
		reads.Add(1)
		time.Sleep(pause)
		return Read(p, kind)
	}
	return c, &reads
}

var videoID = regexp.MustCompile(`^[0-9]{1,20}$`)

// videoValid is the test platform's rule: kind video with a numeric id.
func videoValid(kind, id string) bool { return kind == "video" && videoID.MatchString(id) }

func ids(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func sameIDs(got []Row, want ...string) bool {
	g := ids(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

func TestCacheReadsOnceWhileUnchanged(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id,kind,bucket,note\n1,video,,a\n2,video,,b\n")
	c, reads := countingCache(path, 0)
	s1, err := c.Snapshot()
	if err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	s2, err := c.Snapshot()
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("reads = %d, want 1", n)
	}
	if s1 != s2 {
		t.Errorf("the second Snapshot is not the cached one")
	}
	if !sameIDs(s2.Valid, "1", "2") {
		t.Errorf("Valid = %v, want [1 2]", ids(s2.Valid))
	}
}

func TestCacheSeesAdd(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id,kind,bucket,note\n1,video,,a\n")
	c, reads := countingCache(path, 0)
	if _, err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Add(path, []Row{{ID: "2", Kind: "video"}}, fakeKinds{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(s.All, "1", "2") || !sameIDs(s.Valid, "1", "2") {
		t.Errorf("after Add: All = %v, Valid = %v, want [1 2] both", ids(s.All), ids(s.Valid))
	}
	if n := reads.Load(); n != 2 {
		t.Errorf("reads = %d, want 2", n)
	}
}

// A file replaced by rename with the same size and the same modification
// time is still seen: the inode changed.
func TestCacheSeesSameSizeRewrite(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id\n111\n")
	c, _ := countingCache(path, 0)
	if s, err := c.Snapshot(); err != nil || !sameIDs(s.Valid, "111") {
		t.Fatalf("first Snapshot: %v", err)
	}
	old, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(filepath.Dir(path), ".pool.csv.next")
	writePool(t, tmp, "id\n222\n")
	if err := os.Chtimes(tmp, old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if now.Size() != old.Size() || !now.ModTime().Equal(old.ModTime()) {
		t.Fatalf("setup: size %d→%d, mtime %v→%v; want both equal", old.Size(), now.Size(), old.ModTime(), now.ModTime())
	}
	if now.Sys().(*syscall.Stat_t).Ino == old.Sys().(*syscall.Stat_t).Ino {
		t.Fatalf("setup: the rename kept the inode")
	}
	s, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(s.Valid, "222") {
		t.Errorf("after the rewrite: Valid = %v, want [222]", ids(s.Valid))
	}
}

// A file overwritten in place (same inode) with the same size and its old
// modification time put back, as `cp -p` or `touch -r` leave it, is still
// seen: only the kernel sets the change time, and it moved.
func TestCacheSeesInPlaceRewriteWithOldMtime(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id\n111\n")
	c, _ := countingCache(path, 0)
	if s, err := c.Snapshot(); err != nil || !sameIDs(s.Valid, "111") {
		t.Fatalf("first Snapshot: %v", err)
	}
	old, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Past one coarse kernel timestamp tick, so the change time moves on
	// kernels without fine-grained timestamps too.
	time.Sleep(20 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_TRUNC|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("id\n222\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ost, nst := old.Sys().(*syscall.Stat_t), now.Sys().(*syscall.Stat_t)
	if now.Size() != old.Size() || !now.ModTime().Equal(old.ModTime()) || nst.Ino != ost.Ino {
		t.Fatalf("setup: size %d→%d, mtime %v→%v, inode %d→%d; want all equal",
			old.Size(), now.Size(), old.ModTime(), now.ModTime(), ost.Ino, nst.Ino)
	}
	if nst.Ctim == ost.Ctim {
		t.Fatalf("setup: the change time did not move")
	}
	s, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(s.Valid, "222") {
		t.Errorf("after the in-place rewrite: Valid = %v, want [222]", ids(s.Valid))
	}
}

// A read error is returned and the old snapshot is not served; a removed
// file is an empty pool, as Read has it, never the old rows.
func TestCacheReadError(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id\n1\n")
	c, _ := countingCache(path, 0)
	if _, err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	writePool(t, path, "a,b\n1,2\n")
	s, err := c.Snapshot()
	if err == nil {
		t.Errorf("unreadable pool: no error, rows %v", ids(s.All))
	}
	if s != nil {
		t.Errorf("unreadable pool: got a snapshot with %v", ids(s.All))
	}
	writePool(t, path, "id\n1\n")
	if s, err := c.Snapshot(); err != nil || !sameIDs(s.Valid, "1") {
		t.Fatalf("readable again: err %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s, err = c.Snapshot()
	if err != nil {
		t.Fatalf("removed pool: %v", err)
	}
	if s == nil || len(s.All) != 0 || len(s.Valid) != 0 {
		t.Errorf("removed pool: stale rows %v", s)
	}
}

// Eight callers at once wait for one read and share its snapshot.
func TestCacheConcurrent(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id\n1\n2\n")
	c, reads := countingCache(path, 20*time.Millisecond)
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		snaps = make([]*Snapshot, 8)
	)
	for i := range snaps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := c.Snapshot()
			if err != nil {
				t.Errorf("Snapshot: %v", err)
			}
			snaps[i] = s
		}()
	}
	close(start)
	wg.Wait()
	if n := reads.Load(); n != 1 {
		t.Errorf("reads = %d, want 1", n)
	}
	for i, s := range snaps {
		if s != snaps[0] {
			t.Errorf("caller %d got another snapshot", i)
		}
	}
}

// All is the file as Read returns it; Valid keeps the rows Valid accepts,
// in file order.
func TestSnapshotRows(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id,kind,bucket,note\n1,video,,a\nx9,video,,bad id\n2,photo,,unknown kind\n3,video,,c\n")
	c, _ := countingCache(path, 0)
	s, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(s.All, "1", "x9", "2", "3") {
		t.Errorf("All = %v, want [1 x9 2 3]", ids(s.All))
	}
	if !sameIDs(s.Valid, "1", "3") {
		t.Errorf("Valid = %v, want [1 3]", ids(s.Valid))
	}
}

// BenchmarkSnapshotRead is one full read of a 1.25 M-row pool of synthetic
// TikTok-like IDs (header id, as cmd/poolexport writes): the cost of the
// first Snapshot after pool.csv changed. MB-resident is the heap the cached
// snapshot keeps after a GC.
func BenchmarkSnapshotRead(b *testing.B) {
	const n = 1_250_000
	path := filepath.Join(b.TempDir(), "pool.csv")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	w := bufio.NewWriter(f)
	rng := rand.New(rand.NewPCG(1, 2))
	now := uint64(time.Now().Unix())
	_, _ = w.WriteString("id\n")
	for range n {
		posted := now - rng.Uint64N(2*365*86400)
		_, _ = w.WriteString(strconv.FormatUint(posted<<32|uint64(rng.Uint32()), 10) + "\n")
	}
	if err := w.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	valid := regexp.MustCompile(`^(?:[0-9]{1,20})$`)
	var resident float64
	b.ReportAllocs()
	for b.Loop() {
		var before, after runtime.MemStats
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&before)
		b.StartTimer()
		c := &Cache{Path: path, DefaultKind: "video", Valid: func(kind, id string) bool { return kind == "video" && valid.MatchString(id) }}
		s, err := c.Snapshot()
		if err != nil || len(s.Valid) != n {
			b.Fatalf("Snapshot: %v", err)
		}
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&after)
		resident = float64(after.HeapAlloc-before.HeapAlloc) / 1e6
		runtime.KeepAlive(c)
		b.StartTimer()
	}
	b.ReportMetric(resident, "MB-resident")
}

func TestSnapshotNote(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id,kind,bucket,note\n1,video,,First clip\n2,video,,\nx9,video,,Hidden\n")
	c, _ := countingCache(path, 0)
	s, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, id string
		note     string
		ok       bool
	}{
		{"video", "1", "First clip", true},
		{"video", "2", "", false},  // no note
		{"video", "x9", "", false}, // not a valid row
		{"photo", "1", "", false},  // another kind
		{"video", "7", "", false},  // not in the pool
	} {
		note, ok := s.Note(tc.kind, tc.id)
		if note != tc.note || ok != tc.ok {
			t.Errorf("Note(%s, %s) = %q, %v; want %q, %v", tc.kind, tc.id, note, ok, tc.note, tc.ok)
		}
	}
}
