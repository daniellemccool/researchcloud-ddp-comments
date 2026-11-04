package render

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// writeSource builds a platform source tree: platform.json, userContent.css
// and fixtures/a.json.
func writeSource(t *testing.T, files map[string]string) string {
	t.Helper()
	src := t.TempDir()
	for name, content := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func TestWritePlatformCopyAtomic(t *testing.T) {
	cfg := testConfig(t)
	src := writeSource(t, map[string]string{
		"platform.json":   `{"name":"tiktok"}`,
		"userContent.css": "body{}",
		"fixtures/a.json": `{"a":1}`,
	})

	if err := WritePlatformCopy(cfg, src); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(cfg.DeskRoot, "platform")
	for name, want := range map[string]string{
		"platform.json":   `{"name":"tiktok"}`,
		"userContent.css": "body{}",
		"fixtures/a.json": `{"a":1}`,
	} {
		data, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("platform/%s: %v", name, err)
		}
		if string(data) != want {
			t.Errorf("platform/%s = %q, want %q", name, data, want)
		}
		fi, err := os.Stat(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("platform/%s mode = %o, want 644", name, fi.Mode().Perm())
		}
	}
	for _, d := range []string{dst, filepath.Join(dst, "fixtures")} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() || fi.Mode().Perm() != 0o755 {
			t.Errorf("dir %s mode = %o, want 755", d, fi.Mode().Perm())
		}
	}
	// No temp tree remains.
	entries, err := os.ReadDir(cfg.DeskRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > len("platform.tmp-") && e.Name()[:len("platform.tmp-")] == "platform.tmp-" {
			t.Errorf("leftover temp tree %q", e.Name())
		}
	}

	// A second call with a changed source replaces the tree and drops a file
	// that disappeared from the source.
	src2 := writeSource(t, map[string]string{
		"platform.json":   `{"name":"tiktok","v":2}`,
		"userContent.css": "body{color:red}",
	})
	if err := WritePlatformCopy(cfg, src2); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "platform.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"name":"tiktok","v":2}` {
		t.Errorf("platform.json after second copy = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dst, "fixtures", "a.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("fixtures/a.json after second copy: err = %v, want not-exist", err)
	}
	entries, err = os.ReadDir(cfg.DeskRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > len("platform.tmp-") && e.Name()[:len("platform.tmp-")] == "platform.tmp-" {
			t.Errorf("leftover temp tree after second copy: %q", e.Name())
		}
	}
}

func TestRuntimeRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	s := Build(cfg, "alice", 2)
	if err := WriteUserFiles(cfg, s); err != nil {
		t.Fatal(err)
	}

	rt := &Runtime{
		Subnet:    "10.200.7.0/24",
		NetworkID: "nid",
		ProxyID:   "pid",
		FirefoxID: "fid",
		StartedAt: time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC),
	}
	if err := WriteRuntime(cfg, "alice", rt); err != nil {
		t.Fatal(err)
	}
	gotSpec, gotRT, err := ReadPair(cfg, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSpec, s) {
		t.Errorf("ReadPair spec = %+v, want %+v", gotSpec, s)
	}
	if gotRT == nil || *gotRT != *rt {
		t.Errorf("ReadPair runtime = %+v, want %+v", gotRT, rt)
	}

	// nil clears the runtime to null.
	if err := WriteRuntime(cfg, "alice", nil); err != nil {
		t.Fatal(err)
	}
	_, gotRT, err = ReadPair(cfg, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if gotRT != nil {
		t.Errorf("runtime after WriteRuntime(nil) = %+v, want nil", gotRT)
	}

	// An absent pair wraps os.ErrNotExist.
	_, _, err = ReadPair(cfg, "nobody")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadPair(absent) err = %v, want it to wrap os.ErrNotExist", err)
	}
}
