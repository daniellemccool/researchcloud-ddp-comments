package web

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
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

// wantStage is one stage of the collection's flow as the page must write
// it, spelled out here so the tests do not lean on the code under test:
// the node ("" a ring, "now" the target, "done" filled), the number, its
// label and, when not empty, the line under it (already HTML).
func wantStage(node, value, label, sub string) string {
	cls := "flow__node"
	if node != "" {
		cls += " flow__node--" + node
	}
	s := `<li class="flow__stage"><i class="` + cls + `"></i><b class="readout">` + value + `</b><span class="flow__label">` + label + `</span>`
	if sub != "" {
		s += `<span class="flow__sub">` + sub + `</span>`
	}
	return s + `</li>`
}

// wantFlow is the flow of the given stages.
func wantFlow(stages ...string) string {
	return `<ol class="flow">` + strings.Join(stages, "") + `</ol>`
}

// The collection as a flow, on a platform whose item is a post: of a pool
// of 12, 5 are claimed by 3 people, 4 of those are done and 2 of the done
// were reported as a problem. Every label that names the item follows the
// noun, and the stages come in the order an item moves through them.
func TestCollectionFlow(t *testing.T) {
	srv, root := newAdminServerWith(t, func(d *Deps) {
		d.Platform.DefaultKind = "post"
		d.Platform.ItemKinds = map[string]platform.ItemKind{"post": {URL: "https://example.org/p/{id}", ID: regexp.MustCompile(`^[0-9]{1,20}$`)}}
		d.Log.DefaultKind = "post"
	})
	csv := "id,kind,bucket,note\n"
	for i := 1; i <= 12; i++ {
		csv += strconv.Itoa(i) + ",post,,\n"
	}
	if err := os.WriteFile(filepath.Join(root, "pool.csv"), []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	appendLog(t, root,
		desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "post", ID: "1", Batch: 1},
		desklog.Event{T: 1, User: "alice", Action: "claim", Kind: "post", ID: "2", Batch: 1},
		desklog.Event{T: 1, User: "bob", Action: "claim", Kind: "post", ID: "3", Batch: 1},
		desklog.Event{T: 1, User: "bob", Action: "claim", Kind: "post", ID: "4", Batch: 1},
		desklog.Event{T: 1, User: "carol", Action: "claim", Kind: "post", ID: "5", Batch: 1},
		desklog.Event{T: 2, User: "alice", Action: "open", Kind: "post", ID: "1", ExecRC: rc(0)},
		desklog.Event{T: 2, User: "alice", Action: "problem", Kind: "post", ID: "2", Code: "NOT_FOUND"},
		desklog.Event{T: 2, User: "bob", Action: "open", Kind: "post", ID: "3", ExecRC: rc(0)},
		desklog.Event{T: 2, User: "bob", Action: "problem", Kind: "post", ID: "4", Code: "OTHER", Note: "x"},
	)
	rec := getAdmin(t, srv, "root", adminRole, "/desk/admin/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	b := rec.Body.String()
	inOrder(t, b,
		`<ol class="flow">`,
		`>7</b><span class="flow__label">waiting in the pool</span><span class="flow__sub">of 12 posts</span>`,
		`>1</b><span class="flow__label">with a collector now</span><span class="flow__sub">claimed, not finished</span>`,
		`>4</b><span class="flow__label">done, by 3 collectors</span><span class="flow__sub"><i class="flow__aside"></i>2 of them reported as a problem</span>`,
		`<span class="flow__label">records captured</span>`,
		`</ol>`,
	)
	if strings.Contains(b, "video") {
		t.Error("the admin page of a platform of posts says video")
	}
}

// One collector and no problems: "by 1 collector", and no problems line
// under it; one problem is "1 of them".
func TestCollectionFlowSingulars(t *testing.T) {
	one := collectionFlow(collection{Queue: 2400000, Claimed: 6447, Done: 6412, Problems: 0, Collectors: 1}, "video", true, true, "912,406")
	want := wantFlow(
		wantStage("", "2,393,553", "waiting in the pool", "of 2,400,000 videos"),
		wantStage("now", "35", "with a collector now", "claimed, not finished"),
		wantStage("done", "6,412", "done, by 1 collector", ""),
		wantStage("done", "912,406", "records captured", ""))
	if one != want {
		t.Errorf("flow = %q\nwant   %q", one, want)
	}
	if strings.Contains(one, "flow__aside") {
		t.Error("a collection without problems has a problems line")
	}
	p1 := collectionFlow(collection{Queue: 3, Claimed: 2, Done: 2, Problems: 1, Collectors: 2}, "video", true, true, "0")
	if want := wantStage("done", "2", "done, by 2 collectors", `<i class="flow__aside"></i>1 of them reported as a problem`); !strings.Contains(p1, want) {
		t.Errorf("flow with one problem lacks %q:\n%s", want, p1)
	}
}

// An unreadable desk log: the stages that come from it read unknown, the
// third label is "done" alone, and the pool's size and the records still
// show. An unreadable pool leaves its size out too.
func TestCollectionFlowUnknownLog(t *testing.T) {
	got := collectionFlow(collection{Queue: 12}, "post", true, false, "40")
	want := wantFlow(
		wantStage("", "unknown", "waiting in the pool", "of 12 posts"),
		wantStage("now", "unknown", "with a collector now", "claimed, not finished"),
		wantStage("done", "unknown", "done", ""),
		wantStage("done", "40", "records captured", ""))
	if got != want {
		t.Errorf("flow with an unknown log = %q\nwant %q", got, want)
	}
	noPool := collectionFlow(collection{Collectors: 2}, "post", false, true, "unknown")
	want = wantFlow(
		wantStage("", "unknown", "waiting in the pool", ""),
		wantStage("now", "unknown", "with a collector now", "claimed, not finished"),
		wantStage("done", "unknown", "done, by 2 collectors", ""),
		wantStage("done", "unknown", "records captured", ""))
	if noPool != want {
		t.Errorf("flow with an unreadable pool = %q\nwant %q", noPool, want)
	}
}

// flowStage writes a stage as wantStage spells it.
func TestFlowStage(t *testing.T) {
	for _, c := range [][4]string{
		{"", "7", "waiting in the pool", "of 12 posts"},
		{"now", "1", "with a collector now", "claimed, not finished"},
		{"done", "4", "records captured", ""},
	} {
		if got, want := flowStage(c[0], c[1], c[2], c[3]), wantStage(c[0], c[1], c[2], c[3]); got != want {
			t.Errorf("flowStage(%q) = %q, want %q", c, got, want)
		}
	}
}
