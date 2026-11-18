package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// zipEntry is one entry of a synthetic export zip. A nil data with a name
// ending in "/" is a folder entry.
type zipEntry struct {
	name string
	data []byte
	mode fs.FileMode // 0 = a regular file
}

// writeZip writes entries, deflated, to a zip at path.
func writeZip(t *testing.T, path string, entries ...zipEntry) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			fh.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// zipDir zips every file of dir under the folder entry top/.
func zipDir(t *testing.T, dir, path, top string) {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries := []zipEntry{{name: top + "/"}}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, zipEntry{name: top + "/" + f.Name(), data: b})
	}
	writeZip(t, path, entries...)
}

func TestZipMatchesDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "export")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExport(t, dir, "p1-tiktok.json",
		table{"tiktok_watch_history": links("Link", "https://www.tiktokv.com/share/video/7301444403200000001/", "", nil, "https://f/1")},
		table{"tiktok_like_list": links("Link", "https://www.tiktok.com/@a/video/7301444403200000002")},
		table{"tiktok_favorite_videos": links("Link", 123, "https://www.tiktokv.com/share/video/7301444403200000003/")},
		table{"tiktok_comments": links("Url", "https://www.tiktokv.com/share/video/7301444403200000004/")},
	)
	watchExport(t, dir, "p2-tiktok.json", "7301444403200000001", "7301444403200000005")
	if err := os.WriteFile(filepath.Join(dir, "p1-onboarding.json"), []byte(`{"status":"consent accepted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "20990101_export.zip")
	zipDir(t, dir, zipPath, "20990101_export")
	// Eyra's export zips put every entry's sizes in a data descriptor
	// (general-purpose flag bit 3); the fixture must keep that case.
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range zr.File {
		if !strings.HasSuffix(f.Name, "/") && f.Flags&0x8 == 0 {
			t.Fatalf("fixture entry %d has no data descriptor (flags %#x)", i+1, f.Flags)
		}
	}
	if err := zr.Close(); err != nil {
		t.Fatal(err)
	}

	dirIDs, dirStats, err := extractIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	zipIDs, zipStats, err := extractIDs(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dirIDs, zipIDs) {
		t.Fatalf("zip gave %d IDs, folder %d", len(zipIDs), len(dirIDs))
	}
	if !reflect.DeepEqual(dirStats, zipStats) {
		t.Fatalf("zip stats %+v, folder stats %+v", zipStats, dirStats)
	}
	if zipStats.Files != 2 || len(zipIDs) != 5 {
		t.Fatalf("zip read %d files and %d IDs, want 2 and 5", zipStats.Files, len(zipIDs))
	}
}

func TestZipNestedAndIgnored(t *testing.T) {
	root := t.TempDir()
	tiktok := func(id string) []byte {
		return []byte(`[{"tiktok_watch_history":[{"Link":"https://www.tiktokv.com/share/video/` + id + `/"}]}]`)
	}
	zipPath := filepath.Join(root, "export.zip")
	writeZip(t, zipPath,
		zipEntry{name: "top/"},
		zipEntry{name: "top/a/"},
		zipEntry{name: "top/a/b/n-tiktok.json", data: tiktok("7301444403200000001")},
		zipEntry{name: "top/x-onboarding.json", data: []byte(`{"status":"consent accepted"}`)},
		zipEntry{name: "top/y-instagram.json", data: tiktok("7301444403200000008")},
		zipEntry{name: "top/notes.txt", data: []byte("7301444403200000007")},
		zipEntry{name: "top/l-tiktok.json", data: []byte("top/a/b/n-tiktok.json"), mode: fs.ModeSymlink | 0o777},
	)
	ids, st, err := extractIDs(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ids["7301444403200000001"]; !ok || len(ids) != 1 {
		t.Fatalf("got %d IDs, want only the nested entry's one", len(ids))
	}
	if st.Files != 1 || st.NonRegular != 1 {
		t.Fatalf("Files %d NonRegular %d, want 1 1", st.Files, st.NonRegular)
	}
}

// rawEntry adds an entry with a raw (already compressed) body and the given
// header, so a test can declare sizes, methods and flags it never writes.
func rawEntry(t *testing.T, zw *zip.Writer, fh *zip.FileHeader, body []byte) {
	t.Helper()
	fh.CRC32 = crc32.ChecksumIEEE(body)
	fh.CompressedSize64 = uint64(len(body))
	if fh.UncompressedSize64 == 0 {
		fh.UncompressedSize64 = uint64(len(body))
	}
	w, err := zw.CreateRaw(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
}

func TestZipRefusesBadEntries(t *testing.T) {
	root := t.TempDir()
	malformed := []byte(`[{"tiktok_watch_history": [{"Link": "zzsecretone`)
	good := []byte(`[{"tiktok_watch_history":[{"Link":"https://www.tiktokv.com/share/video/7301444403200000001/"}]}]`)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Byte order of the full names: top/Z/q (1), top/a (2), top/big (3),
	// top/m (4), top/s (5).
	rawEntry(t, zw, &zip.FileHeader{Name: "top/Z/q-tiktok.json", Method: zip.Store}, malformed)
	rawEntry(t, zw, &zip.FileHeader{Name: "top/a-tiktok.json", Method: zip.Store}, good)
	rawEntry(t, zw, &zip.FileHeader{Name: "top/big-tiktok.json", Method: zip.Store, UncompressedSize64: 2 << 30}, []byte("zzsecretbig"))
	rawEntry(t, zw, &zip.FileHeader{Name: "top/m-tiktok.json", Method: 99}, []byte("zzsecretmethod"))
	rawEntry(t, zw, &zip.FileHeader{Name: "top/s-tiktok.json", Method: zip.Store, Flags: 0x1}, []byte("zzsecretcrypt"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "export.zip")
	if err := os.WriteFile(zipPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	sent := filepath.Join(root, "sent-ids.txt")
	before := []byte("7301444403200000009\n")
	if err := os.WriteFile(sent, before, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	_, err := runQuiet("-export", zipPath, "-out", out, "-sent", sent)
	if err == nil {
		t.Fatal("run succeeded on a zip with bad entries")
	}
	msg := err.Error()
	for _, want := range []string{
		"4 of 5",
		fmt.Sprintf("file 1 (%d bytes): not valid JSON at byte %d", len(malformed), len(malformed)),
		"file 3 (2147483648 bytes): larger than 1 GiB",
		"file 4 (14 bytes): unsupported compression method 99",
		"file 5 (13 bytes): encrypted",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q: %v", want, msg)
		}
	}
	for _, leak := range []string{"q-tiktok", "big-tiktok", "m-tiktok", "s-tiktok", "top/", "zzsecret"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error carries an entry name or content (%s)", leak)
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

func TestExportNeitherFolderNorZip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "export.txt")
	if err := os.WriteFile(path, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runQuiet("-export", path, "-out", filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), "neither a folder nor a readable zip file") {
		t.Fatalf("got %v, want a neither-folder-nor-zip error", err)
	}
}

// TestReadStopsAtLimit checks the read-side size guard with a small limit, on
// a data-descriptor zip entry and on a folder file: more than limit bytes is
// errTooLarge, whatever the declared size said.
func TestReadStopsAtLimit(t *testing.T) {
	root := t.TempDir()
	body := []byte(`[{"tiktok_comments":[]}]`) // 24 bytes
	zipPath := filepath.Join(root, "export.zip")
	writeZip(t, zipPath, zipEntry{name: "top/a-tiktok.json", data: body})
	dir := filepath.Join(root, "export")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a-tiktok.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{zipPath, dir} {
		src, err := openExport(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(src.entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(src.entries))
		}
		e := src.entries[0]
		if _, err := e.read(int64(len(body)) - 1); !errors.Is(err, errTooLarge) {
			t.Errorf("read past the limit gave %v, want errTooLarge", err)
		}
		if b, err := e.read(int64(len(body))); err != nil || len(b) != len(body) {
			t.Errorf("read at the limit gave %d bytes, err %v", len(b), err)
		}
		if err := src.close(); err != nil {
			t.Fatal(err)
		}
	}
}
