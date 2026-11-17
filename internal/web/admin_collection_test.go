package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
)

func rc(code int) *int { return &code }

// TestCollectNumbers: the collection's figures over the whole pool. An item
// is done when its claimant opened it successfully or reported a problem on
// it; a failed open, and anything another person did to it, marks nothing.
func TestCollectNumbers(t *testing.T) {
	l := &desklog.Log{DefaultKind: "video"}
	rows := []pool.Row{
		{ID: "1", Kind: "video"}, {ID: "2", Kind: "video"}, {ID: "3", Kind: "video"},
		{ID: "4", Kind: "video"}, {ID: "5", Kind: "video"},
	}
	evs := []desklog.Event{
		{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "1", Batch: 1},
		{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "2", Batch: 1},
		{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "3", Batch: 1},
		{T: 2, User: "alice", Action: "open", Kind: "video", ID: "1", ExecRC: rc(0)},
		{T: 3, User: "alice", Action: "open", Kind: "video", ID: "2", ExecRC: rc(1)},
		{T: 4, User: "alice", Action: "problem", Kind: "video", ID: "3", Code: "OTHER", Note: "x"},
		{T: 5, User: "bob", Action: "claim", Kind: "video", ID: "4", Batch: 1},
		{T: 6, User: "alice", Action: "active"},
	}
	got := collect(rows, evs, l)
	if want := (collection{Queue: 5, Claimed: 4, Done: 2, Problems: 1, Collectors: 2}); got != want {
		t.Errorf("collect = %+v, want %+v", got, want)
	}

	// Bob opening alice's item 2 does not make it done; carol's claim of an
	// item that is no longer in the pool counts her as a collector and
	// nothing else.
	evs = append(evs,
		desklog.Event{T: 7, User: "bob", Action: "open", Kind: "video", ID: "2", ExecRC: rc(0)},
		desklog.Event{T: 8, User: "carol", Action: "claim", Kind: "video", ID: "9", Batch: 1},
	)
	got = collect(rows, evs, l)
	if want := (collection{Queue: 5, Claimed: 4, Done: 2, Problems: 1, Collectors: 3}); got != want {
		t.Errorf("collect with another person's open and a claim outside the pool = %+v, want %+v", got, want)
	}

	if got := collect(nil, nil, l); got != (collection{}) {
		t.Errorf("collect of nothing = %+v, want zero", got)
	}
}

// TestRecordCounterCountsAndRemembers: the total is the number of lines in
// every person's captures file, and a file that did not change is not read
// again.
func TestRecordCounterCountsAndRemembers(t *testing.T) {
	root := t.TempDir()
	write := func(user, text string) string {
		path := filepath.Join(root, "state", "captures", user, "captures.ndjson")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	var c recordCounter
	if n, err := c.Total(root); err != nil || n != 0 {
		t.Fatalf("Total with no captures directory = %d, %v; want 0, nil", n, err)
	}
	write("alice", "{}\n{}\n{}\n")
	bob := write("bob", "{}\n{}\n")
	if n, err := c.Total(root); err != nil || n != 5 {
		t.Fatalf("Total = %d, %v; want 5", n, err)
	}
	if c.filesRead != 2 {
		t.Fatalf("files read = %d, want 2", c.filesRead)
	}
	if n, _ := c.Total(root); n != 5 || c.filesRead != 2 {
		t.Errorf("second Total = %d after %d files read, want 5 with no file read again", n, c.filesRead)
	}
	f, err := os.OpenFile(bob, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.Total(root); n != 6 {
		t.Errorf("Total after one more line = %d, want 6", n)
	}
	if c.filesRead != 3 {
		t.Errorf("files read = %d, want 3: only the file that grew", c.filesRead)
	}
}
