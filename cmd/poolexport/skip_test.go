package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// watchBody is a tiktok export file whose watch history holds ids.
func watchBody(ids ...string) []byte {
	rows := make([]string, len(ids))
	for i, id := range ids {
		rows[i] = `{"Link":"https://www.tiktokv.com/share/video/` + id + `/"}`
	}
	return []byte(`[{"tiktok_watch_history":[` + strings.Join(rows, ",") + `]}]`)
}

// writeBoth writes files (base name → body) into a folder and, under the
// folder entry top/, into a zip, and returns both paths.
func writeBoth(t *testing.T, root string, files map[string][]byte) (dir, zipPath string) {
	t.Helper()
	dir = filepath.Join(root, "export")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entries := []zipEntry{{name: "top/"}}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, zipEntry{name: "top/" + name, data: body})
	}
	zipPath = filepath.Join(root, "export.zip")
	writeZip(t, zipPath, entries...)
	return dir, zipPath
}

const (
	shared   = "7301444403200000001" // in the test file and a real one
	testOnly = "7301444403200000009" // in the test file only
)

func TestSkipsTestParticipant(t *testing.T) {
	dir, zipPath := writeBoth(t, t.TempDir(), map[string][]byte{
		"assignment=1_task=2_participant=test_source=tiktok_key=3-tiktok.json":   watchBody(shared, testOnly),
		"assignment=1_task=2_participant=tester_source=tiktok_key=4-tiktok.json": watchBody("7301444403200000002"),
		"assignment=1_task=2_participant=test2_source=tiktok_key=5-tiktok.json":  watchBody("7301444403200000004"),
		"assignment=1_task=2_participant=p7_source=tiktok_key=6-tiktok.json":     watchBody(shared, "7301444403200000003"),
	})
	want := []string{shared, "7301444403200000002", "7301444403200000003", "7301444403200000004"}
	for _, p := range []string{dir, zipPath} {
		ids, st, err := extractIDs(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := sortedKeys(ids); !slices.Equal(got, want) {
			t.Errorf("%s: got IDs %v, want %v", filepath.Ext(p), got, want)
		}
		if st.TestSkipped != 1 || st.Files != 3 || st.Rows["tiktok_watch_history"] != 4 || st.Distinct != 4 {
			t.Errorf("%s: TestSkipped %d Files %d Rows %d Distinct %d, want 1 3 4 4",
				filepath.Ext(p), st.TestSkipped, st.Files, st.Rows["tiktok_watch_history"], st.Distinct)
		}
	}
}

func TestRunWritesSharedIDAndReportsTestSkip(t *testing.T) {
	root := t.TempDir()
	_, zipPath := writeBoth(t, root, map[string][]byte{
		"assignment=1_task=2_participant=test_source=tiktok_key=3-tiktok.json": watchBody(shared, testOnly),
		"assignment=1_task=2_participant=p7_source=tiktok_key=6-tiktok.json":   watchBody(shared),
	})
	out := filepath.Join(root, "out")
	msg, err := runQuiet("-export", zipPath, "-out", out)
	if err != nil {
		t.Fatal(err)
	}
	if got := uploads(t, out); !slices.Equal(got, []string{shared}) {
		t.Fatalf("wrote %v, want only the shared ID %s", got, shared)
	}
	if !strings.Contains(msg, "test-participant files skipped: 1\n") {
		t.Fatalf("report lacks the test-participant count: %q", msg)
	}
}

func TestBadFilePositionExcludesTestFiles(t *testing.T) {
	// Byte order: assignment=1 (test, skipped, malformed: never read),
	// assignment=2 (real, malformed: K=1), assignment=3 (real, good).
	dir, zipPath := writeBoth(t, t.TempDir(), map[string][]byte{
		"assignment=1_participant=test_key=1-tiktok.json": []byte("["),
		"assignment=2_participant=p1_key=2-tiktok.json":   []byte("[{"),
		"assignment=3_participant=p2_key=3-tiktok.json":   watchBody("7301444403200000001"),
	})
	for _, p := range []string{dir, zipPath} {
		_, _, err := extractIDs(p)
		if err == nil {
			t.Fatalf("%s: no error for a malformed real file", filepath.Ext(p))
		}
		if msg := err.Error(); !strings.Contains(msg, "1 of 2 tiktok files") || !strings.Contains(msg, "file 1 (2 bytes)") {
			t.Errorf("%s: want 1 of 2 and file 1 (2 bytes): %v", filepath.Ext(p), msg)
		}
	}
}
