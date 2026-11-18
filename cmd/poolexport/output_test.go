package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestReadSentRejectsJunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent-ids.txt")
	if err := os.WriteFile(path, []byte("7301444403200000001\n7301444403200000002\nabc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readSent(path)
	if err == nil {
		t.Fatal("readSent accepted a junk line")
	}
	if !strings.Contains(err.Error(), "line 3") || strings.Contains(err.Error(), "abc") {
		t.Fatalf("error must name line 3 and not its content: %v", err)
	}
}

func TestReadSentMissingIsEmpty(t *testing.T) {
	got, err := readSent(filepath.Join(t.TempDir(), "absent.txt"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d IDs, err %v; want an empty set", len(got), err)
	}
}

func TestWriteSentSorted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent-ids.txt")
	ids := map[string]struct{}{"9": {}, "10": {}, "7301444403200000001": {}, "100": {}}
	if err := writeSent(path, ids); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	want := []string{"9", "10", "100", "7301444403200000001"}
	if !slices.Equal(got, want) {
		t.Fatalf("sent list lines %v, want %v", got, want)
	}
	back, err := readSent(path)
	if err != nil || len(back) != len(ids) {
		t.Fatalf("read back %d IDs, err %v", len(back), err)
	}
}

// numbered returns n distinct 19-digit TikTok-shaped IDs.
func numbered(n int) map[string]struct{} {
	ids := make(map[string]struct{}, n)
	for i := range n {
		ids[strconv.FormatUint(7301444403200000000+uint64(i), 10)] = struct{}{}
	}
	return ids
}

func TestShuffledNotInputOrder(t *testing.T) {
	ids := numbered(1000)
	got, err := shuffled(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %d IDs, want %d", len(got), len(ids))
	}
	seen := map[string]struct{}{}
	for _, id := range got {
		if _, ok := ids[id]; !ok {
			t.Fatalf("shuffled returned an ID not in the input: %s", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != len(ids) {
		t.Fatalf("shuffled returned %d distinct IDs, want %d", len(seen), len(ids))
	}
	if slices.IsSorted(got) {
		t.Fatal("shuffled returned the IDs in ascending order")
	}
}

var digits = regexp.MustCompile(`^[0-9]+$`)

// readUpload returns the rows of an upload file after checking its header is
// `id` and every row is a bare numeric ID.
func readUpload(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	if !sc.Scan() || sc.Text() != "id" {
		t.Fatalf("%s: first line is not the header id", filepath.Base(path))
	}
	var rows []string
	for sc.Scan() {
		if !digits.MatchString(sc.Text()) {
			t.Fatalf("%s: row %d is not a bare numeric ID", filepath.Base(path), len(rows)+1)
		}
		rows = append(rows, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestWriteUploadsSplits(t *testing.T) {
	dir := t.TempDir()
	ids, err := shuffled(numbered(450001))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := writeUploads(dir, "20261118T120000Z", ids, 200000)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{200000, 200000, 50001}
	if len(paths) != len(want) {
		t.Fatalf("got %d files, want %d", len(paths), len(want))
	}
	var all []string
	for i, p := range paths {
		if filepath.Base(p) != fmt.Sprintf("pool-upload-20261118T120000Z-%d.csv", i+1) {
			t.Errorf("file %d is named %s", i+1, filepath.Base(p))
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() >= 5242880 {
			t.Errorf("file %d is %d bytes, not under the desk's 5 MB limit", i+1, fi.Size())
		}
		rows := readUpload(t, p)
		if len(rows) != want[i] {
			t.Errorf("file %d holds %d rows, want %d", i+1, len(rows), want[i])
		}
		all = append(all, rows...)
	}
	if !slices.Equal(all, ids) {
		t.Fatal("the files do not hold the IDs in the order given")
	}
}

// runQuiet calls run with args and returns its error and what it printed.
func runQuiet(args ...string) (string, error) {
	var buf bytes.Buffer
	err := run(args, &buf)
	return buf.String(), err
}

// uploads returns the rows of every pool upload in dir.
func uploads(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "pool-upload-*.csv"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, p := range paths {
		rows = append(rows, readUpload(t, p)...)
	}
	slices.Sort(rows)
	return rows
}

func watchExport(t *testing.T, dir, name string, ids ...string) {
	t.Helper()
	vals := make([]any, len(ids))
	for i, id := range ids {
		vals[i] = "https://www.tiktokv.com/share/video/" + id + "/"
	}
	writeExport(t, dir, name, table{"tiktok_watch_history": links("Link", vals...)})
}

func TestSentListUntouchedOnWriteFailure(t *testing.T) {
	root := t.TempDir()
	export := filepath.Join(root, "export")
	if err := os.Mkdir(export, 0o700); err != nil {
		t.Fatal(err)
	}
	watchExport(t, export, "p1-tiktok.json", "7301444403200000001", "7301444403200000002")
	sent := filepath.Join(root, "sent-ids.txt")
	before := []byte("7301444403200000001\n")
	if err := os.WriteFile(sent, before, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	if err := os.WriteFile(out, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runQuiet("-export", export, "-out", out, "-sent", sent); err == nil {
		t.Fatal("run succeeded with an output dir that is a regular file")
	}
	after, err := os.ReadFile(sent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the sent list changed although no upload was written")
	}
	err = filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.Base(p), "pool-upload-") {
			return fmt.Errorf("an upload file was left behind: %s", filepath.Base(p))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunSecondRunOnlyNew(t *testing.T) {
	root := t.TempDir()
	exportA := filepath.Join(root, "a")
	exportAB := filepath.Join(root, "ab")
	for _, d := range []string{exportA, exportAB} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a := []string{"7301444403200000001", "7301444403200000002", "7301444403200000003"}
	b := []string{"7301444403200000003", "7301444403200000004", "7301444403200000005"}
	watchExport(t, exportA, "p1-tiktok.json", a...)
	watchExport(t, exportAB, "p1-tiktok.json", a...)
	watchExport(t, exportAB, "p2-tiktok.json", b...)
	sent := filepath.Join(root, "sent-ids.txt")

	out1 := filepath.Join(root, "out1")
	if _, err := runQuiet("-export", exportA, "-out", out1, "-sent", sent); err != nil {
		t.Fatal(err)
	}
	if got := uploads(t, out1); !slices.Equal(got, a) {
		t.Fatalf("first run wrote %v, want %v", got, a)
	}

	out2 := filepath.Join(root, "out2")
	if _, err := runQuiet("-export", exportAB, "-out", out2, "-sent", sent); err != nil {
		t.Fatal(err)
	}
	if got, want := uploads(t, out2), []string{"7301444403200000004", "7301444403200000005"}; !slices.Equal(got, want) {
		t.Fatalf("second run wrote %v, want only B \\ A %v", got, want)
	}

	out3 := filepath.Join(root, "out3")
	if _, err := runQuiet("-export", exportAB, "-out", out3, "-sent", sent, "-all"); err != nil {
		t.Fatal(err)
	}
	union := []string{"7301444403200000001", "7301444403200000002", "7301444403200000003", "7301444403200000004", "7301444403200000005"}
	if got := uploads(t, out3); !slices.Equal(got, union) {
		t.Fatalf("-all wrote %v, want A ∪ B %v", got, union)
	}

	out4 := filepath.Join(root, "out4")
	msg, err := runQuiet("-export", exportAB, "-out", out4, "-sent", sent)
	if err != nil {
		t.Fatal(err)
	}
	if got := uploads(t, out4); len(got) != 0 {
		t.Fatalf("a run with nothing new wrote %d IDs", len(got))
	}
	if !strings.Contains(msg, "no new IDs") {
		t.Fatalf("a run with nothing new did not say so: %q", msg)
	}
}
