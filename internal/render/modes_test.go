package render

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestWriteUserFilesModes(t *testing.T) {
	cfg := testConfig(t)
	s := Build(cfg, "alice", 1)
	if err := WriteUserFiles(cfg, s); err != nil {
		t.Fatal(err)
	}

	policyPath := filepath.Join(cfg.DeskRoot, "firefox", "alice", "policies.json")
	fi, err := os.Stat(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("policies.json mode = %o, want 644", fi.Mode().Perm())
	}
	dir := filepath.Join(cfg.DeskRoot, "firefox", "alice")
	fi, err = os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o755 {
		t.Errorf("firefox/alice dir mode = %o, want 755", fi.Mode().Perm())
	}

	pairPath := filepath.Join(cfg.DeskRoot, "state", "users", "alice", "pair.json")
	fi, err = os.Stat(pairPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("pair.json mode = %o, want 600", fi.Mode().Perm())
	}

	if os.Geteuid() == 0 {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
			t.Errorf("pair.json uid = %d, want 0 (root)", st.Uid)
		}
		pi, err := os.Stat(policyPath)
		if err != nil {
			t.Fatal(err)
		}
		if st, ok := pi.Sys().(*syscall.Stat_t); ok && st.Uid != 1000 {
			t.Errorf("policies.json uid = %d, want 1000", st.Uid)
		}
		di, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if st, ok := di.Sys().(*syscall.Stat_t); ok && st.Uid != 1000 {
			t.Errorf("firefox/alice dir uid = %d, want 1000", st.Uid)
		}
	} else {
		t.Log("not running as root: uid checks skipped")
	}
}

func TestWriteUserFilesKeepsRuntime(t *testing.T) {
	cfg := testConfig(t)
	s := Build(cfg, "alice", 1)
	if err := WriteUserFiles(cfg, s); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{
		Subnet:    "10.200.1.0/24",
		NetworkID: "netid",
		ProxyID:   "proxyid",
		FirefoxID: "firefoxid",
		StartedAt: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
	}
	if err := WriteRuntime(cfg, "alice", rt); err != nil {
		t.Fatal(err)
	}
	// Re-rendering the pair must not forget the running runtime.
	if err := WriteUserFiles(cfg, s); err != nil {
		t.Fatal(err)
	}
	gotSpec, gotRT, err := ReadPair(cfg, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSpec, s) {
		t.Errorf("spec after re-render = %+v, want %+v", gotSpec, s)
	}
	if gotRT == nil || *gotRT != *rt {
		t.Errorf("runtime after re-render = %+v, want %+v", gotRT, rt)
	}
}
