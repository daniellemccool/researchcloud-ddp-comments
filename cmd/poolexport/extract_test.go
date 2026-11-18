package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// table is one single-key object of an export file: a table name holding rows.
type table map[string][]map[string]any

// writeExport writes a synthetic export file named name under dir: a JSON
// array of single-key table objects, as the participant export holds them.
func writeExport(t *testing.T, dir, name string, tables ...table) {
	t.Helper()
	b, err := json.Marshal(tables)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// links builds the rows of a table whose column col holds each value.
func links(col string, values ...any) []map[string]any {
	rows := make([]map[string]any, 0, len(values))
	for _, v := range values {
		rows = append(rows, map[string]any{col: v})
	}
	return rows
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestExtractAllTables(t *testing.T) {
	dir := t.TempDir()
	writeExport(t, dir, "p1-tiktok.json",
		table{"tiktok_watch_history": links("Link", "https://www.tiktokv.com/share/video/7301444403200000001/")},
		table{"tiktok_like_list": links("Link", "https://www.tiktok.com/@a/video/7301444403200000002")},
		table{"tiktok_favorite_videos": links("Link", "https://www.tiktokv.com/share/video/7301444403200000003/")},
		table{"tiktok_comments": links("Url", "https://www.tiktokv.com/share/video/7301444403200000004/")},
	)
	ids, st, err := extractIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"7301444403200000001", "7301444403200000002", "7301444403200000003", "7301444403200000004"}
	got := sortedKeys(ids)
	if len(got) != len(want) {
		t.Fatalf("got %d IDs, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ID %d = %s, want %s", i, got[i], want[i])
		}
	}
	for _, name := range []string{"tiktok_watch_history", "tiktok_like_list", "tiktok_favorite_videos", "tiktok_comments"} {
		if st.Rows[name] != 1 {
			t.Errorf("Rows[%s] = %d, want 1", name, st.Rows[name])
		}
	}
	if st.Files != 1 || st.Distinct != 4 || st.Skipped != 0 {
		t.Errorf("Files %d Distinct %d Skipped %d, want 1 4 0", st.Files, st.Distinct, st.Skipped)
	}
}

func TestExtractDedupesAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	link := "https://www.tiktokv.com/share/video/7301444403200000001/"
	writeExport(t, dir, "p1-tiktok.json", table{"tiktok_watch_history": links("Link", link, link)})
	writeExport(t, dir, "p2-tiktok.json", table{"tiktok_like_list": links("Link", link)})
	ids, st, err := extractIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || st.Distinct != 1 {
		t.Fatalf("got %d IDs, Distinct %d, want 1 1", len(ids), st.Distinct)
	}
	if st.Files != 2 || st.Rows["tiktok_watch_history"] != 2 || st.Rows["tiktok_like_list"] != 1 {
		t.Errorf("Files %d, Rows %v", st.Files, st.Rows)
	}
}

func TestExtractSkipsOddValues(t *testing.T) {
	dir := t.TempDir()
	rows := links("Link", "", nil, "https://f/1", 123)
	rows = append(rows, map[string]any{"Date": "2026-11-18"})
	writeExport(t, dir, "p1-tiktok.json",
		table{"tiktok_watch_history": rows},
		table{"deleted row count": nil},
	)
	ids, st, err := extractIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 || st.Skipped != 5 {
		t.Fatalf("got %d IDs, Skipped %d, want 0 5", len(ids), st.Skipped)
	}
}

func TestExtractReadsOnlyTiktokFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x-onboarding.json"), []byte(`{"status":"consent accepted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExport(t, dir, "y-instagram.json",
		table{"tiktok_watch_history": links("Link", "https://www.tiktokv.com/share/video/7301444403200000009/")})
	writeExport(t, dir, "z-tiktok.json",
		table{"tiktok_watch_history": links("Link", "https://www.tiktokv.com/share/video/7301444403200000001/")})
	ids, st, err := extractIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 1 {
		t.Fatalf("Files = %d, want 1", st.Files)
	}
	if _, ok := ids["7301444403200000009"]; ok || len(ids) != 1 {
		t.Fatalf("got %d IDs, want only the tiktok file's one", len(ids))
	}
}

// setupBadExport writes an export whose byte order of names (B, a, p10, p2)
// differs from both locale order (a, B, p10, p2) and natural order (a, B, p2,
// p10): files 1 (B) and 3 (p10) in byte order are malformed.
func setupBadExport(t *testing.T, dir string) (truncated, wrongType []byte) {
	t.Helper()
	truncated = []byte(`[{"tiktok_watch_history": [{"Link": "zzsecretone`)
	wrongType = []byte(`[{"tiktok_like_list": "zzsecrettwo"}]`)
	if err := os.WriteFile(filepath.Join(dir, "B-tiktok.json"), truncated, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p10-tiktok.json"), wrongType, 0o600); err != nil {
		t.Fatal(err)
	}
	watchExport(t, dir, "a-tiktok.json", "7301444403200000001")
	watchExport(t, dir, "p2-tiktok.json", "7301444403200000002")
	return truncated, wrongType
}

func TestExtractReportsEveryBadFile(t *testing.T) {
	root := t.TempDir()
	export := filepath.Join(root, "export")
	if err := os.Mkdir(export, 0o700); err != nil {
		t.Fatal(err)
	}
	truncated, wrongType := setupBadExport(t, export)
	sent := filepath.Join(root, "sent-ids.txt")
	before := []byte("7301444403200000009\n")
	if err := os.WriteFile(sent, before, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	_, err := runQuiet("-export", export, "-out", out, "-sent", sent)
	if err == nil {
		t.Fatal("run succeeded on an export with malformed files")
	}
	msg := err.Error()
	var typeErr *json.UnmarshalTypeError
	if json.Unmarshal(wrongType, &[]exportObject{}) == nil || !errors.As(json.Unmarshal(wrongType, &[]exportObject{}), &typeErr) {
		t.Fatal("the wrong-type fixture does not give a type error")
	}
	for _, want := range []string{
		"2 of 4",
		fmt.Sprintf("file 1 (%d bytes): not valid JSON at byte %d", len(truncated), len(truncated)),
		fmt.Sprintf("file 3 (%d bytes): wrong JSON type at byte %d", len(wrongType), typeErr.Offset),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q: %v", want, msg)
		}
	}
	for _, leak := range []string{"B-tiktok", "p10", "zzsecret", "tiktok_watch_history"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error carries a file name or content (%s)", leak)
		}
	}
	after, rerr := os.ReadFile(sent)
	if rerr != nil || !bytes.Equal(before, after) {
		t.Fatalf("the sent list changed (err %v)", rerr)
	}
	if got, _ := filepath.Glob(filepath.Join(out, "pool-upload-*")); len(got) != 0 {
		t.Fatalf("%d upload files were written", len(got))
	}
}

func TestExtractCountsNonRegularEntries(t *testing.T) {
	root := t.TempDir()
	export := filepath.Join(root, "export")
	if err := os.Mkdir(export, 0o700); err != nil {
		t.Fatal(err)
	}
	watchExport(t, root, "elsewhere-tiktok.json", "7301444403200000009")
	if err := os.Symlink(filepath.Join(root, "elsewhere-tiktok.json"), filepath.Join(export, "l-tiktok.json")); err != nil {
		t.Fatal(err)
	}
	watchExport(t, export, "r-tiktok.json", "7301444403200000001")
	msg, err := runQuiet("-export", export, "-out", filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "tiktok entries not regular files (ignored): 1\n") {
		t.Fatalf("report does not count the symlink: %q", msg)
	}
	if !strings.Contains(msg, "tiktok files read: 1\n") || !strings.Contains(msg, "distinct video IDs: 1\n") {
		t.Fatalf("the symlink was read: %q", msg)
	}
}
