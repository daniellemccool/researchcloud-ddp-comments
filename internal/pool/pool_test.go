package pool

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type fakeKinds struct{}

func (fakeKinds) Default() string { return "video" }

func (fakeKinds) Kind(kind string) (*regexp.Regexp, bool) {
	switch kind {
	case "video":
		return regexp.MustCompile(`^[0-9]{1,20}$`), true
	case "user":
		return regexp.MustCompile(`^@?[A-Za-z0-9._]{1,24}$`), true
	}
	return nil, false
}

func poolPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "pool.csv")
}

func writePool(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readPoolFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestReadLegacyHeader(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "video_id,bucket,note\n1,x,\"n1, q\"\n2,x,\n2,x,dup\n")
	rows, err := Read(path, "video")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []Row{{ID: "1", Kind: "video", Bucket: "x", Note: "n1, q"}, {ID: "2", Kind: "video", Bucket: "x", Note: ""}}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("rows[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestReadDedupes(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id,kind,bucket,note\n1,video,x,\n1,video,y,\n1,user,z,\n")
	rows, err := Read(path, "video")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []Row{{ID: "1", Kind: "video", Bucket: "x", Note: ""}, {ID: "1", Kind: "user", Bucket: "z", Note: ""}}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("rows[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestReadBadHeader(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "foo,bar\n1,2\n")
	_, err := Read(path, "video")
	if err == nil {
		t.Fatal("Read = nil error, want unrecognised header")
	}
	if got, want := err.Error(), "pool.csv: unrecognised header [foo bar]"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}
}

func TestReadMissingFile(t *testing.T) {
	rows, err := Read(filepath.Join(t.TempDir(), "pool.csv"), "video")
	if rows != nil || err != nil {
		t.Fatalf("Read(missing) = %+v, %v; want nil, nil", rows, err)
	}
}

func TestReadStripsBOM(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "\uFEFFid,kind\n1,video\n")
	rows, err := Read(path, "video")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rows) != 1 || rows[0] != (Row{ID: "1", Kind: "video"}) {
		t.Fatalf("rows = %+v, want one row (1, video)", rows)
	}
}

func TestAddDedupesWithinBatchAndFile(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id,kind,bucket,note\n1,video,x,\n")
	added, total, err := Add(path, []Row{
		{ID: "1", Kind: "video"},
		{ID: "2", Kind: "video"},
		{ID: "2", Kind: "video"},
		{ID: "junk", Kind: "video"},
	}, fakeKinds{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added != 1 || total != 2 {
		t.Fatalf("Add = %d, %d; want 1, 2", added, total)
	}
	content := readPoolFile(t, path)
	if !strings.HasPrefix(content, "id,kind,bucket,note\n") {
		t.Fatalf("file = %q, want the modern header", content)
	}
	rows, err := Read(path, "video")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []Row{{ID: "1", Kind: "video", Bucket: "x", Note: ""}, {ID: "2", Kind: "video"}}
	if len(rows) != 2 || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
}

func TestAddUpgradesLegacyHeader(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "video_id,bucket,note\n1,x,\n")
	added, total, err := Add(path, []Row{{ID: "2", Kind: "video"}}, fakeKinds{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added != 1 || total != 2 {
		t.Fatalf("Add = %d, %d; want 1, 2", added, total)
	}
	content := readPoolFile(t, path)
	if !strings.HasPrefix(content, "id,kind,bucket,note\n") {
		t.Fatalf("file = %q, want the legacy header upgraded to id,kind,bucket,note", content)
	}
	rows, err := Read(path, "video")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []Row{{ID: "1", Kind: "video", Bucket: "x"}, {ID: "2", Kind: "video"}}
	if len(rows) != 2 || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
}

func TestAddAtomicMode0644(t *testing.T) {
	path := poolPath(t)
	writePool(t, path, "id,kind,bucket,note\n1,video,x,\n")
	if _, _, err := Add(path, []Row{{ID: "2", Kind: "video"}}, fakeKinds{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, want 644", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "pool.csv" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir holds %v, want only pool.csv (no temp file left)", names)
	}
}
