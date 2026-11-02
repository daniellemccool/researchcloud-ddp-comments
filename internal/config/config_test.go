package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostDeskRoot is the desk_root value written into test desk.json files: a
// HOST path, deliberately different from the temp directory the file is
// loaded from.
const hostDeskRoot = "/data/vol1/desk"

// deskJSON builds a complete, realistic desk.json. mutate changes the key map
// before marshaling (add or delete keys).
func deskJSON(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	m := map[string]any{
		"desk_collector_role": "src_co_students",
		"desk_admin_role":     "src_co_admin",
		"desk_platform":       "tiktok",
		"desk_contact":        "desk-admin@example.org",
		"desk_hide_media":     "false",
		"desk_batch_sizes":    "5,10",
		"desk_idle_seconds":   "3600",
		"desk_timer_minutes":  "10",
		"desk_max_sessions":   "8",
		"desk_max_running":    "auto",
		"desk_storage_path":   "auto",
		"fourcat_enabled":     "true",
		"fourcat_image":       "ghcr.io/fourcat/fourcat:1.0.0",
		"desk_image":          "ghcr.io/ns/desk:v1.2.3",
		"deskproxy_image":     "ghcr.io/ns/deskproxy:v1.2.3",
		"zeeschuimer_version": "1.2.3",
		"template_sha256":     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"co_id":               "12345",
		"co_name":             "Example CO",
		"workspace_fqdn":      "desk.example.org",
		"desk_root":           hostDeskRoot,
		"layout_version":      1,
		"package_version":     "v1.2.3",
		"applied_at":          "2026-10-30T12:00:00Z",
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeDeskRoot creates a temp desk root with desk.json and secrets/gate.
func writeDeskRoot(t *testing.T, mutate func(map[string]any), withGate bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "desk.json"), []byte(deskJSON(t, mutate)), 0o644); err != nil {
		t.Fatal(err)
	}
	if withGate {
		if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "secrets", "gate"), []byte("  test-gate-secret \n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadDeskJSONStrict(t *testing.T) {
	t.Run("complete file converts every field", func(t *testing.T) {
		dir := writeDeskRoot(t, nil, true)
		cfg, err := Load(filepath.Join(dir, "desk.json"))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.DeskRoot != dir {
			t.Errorf("DeskRoot = %q, want the temp dir %q", cfg.DeskRoot, dir)
		}
		if cfg.HostDeskRoot != hostDeskRoot {
			t.Errorf("HostDeskRoot = %q, want the file's desk_root %q", cfg.HostDeskRoot, hostDeskRoot)
		}
		if cfg.CollectorRole != "src_co_students" || cfg.AdminRole != "src_co_admin" {
			t.Errorf("roles = %q/%q", cfg.CollectorRole, cfg.AdminRole)
		}
		if cfg.Platform != "tiktok" || cfg.FQDN != "desk.example.org" || cfg.Contact != "desk-admin@example.org" {
			t.Errorf("platform/fqdn/contact = %q/%q/%q", cfg.Platform, cfg.FQDN, cfg.Contact)
		}
		if cfg.HideMedia {
			t.Error("HideMedia = true, want false")
		}
		if !cfg.FourcatEnabled {
			t.Error("FourcatEnabled = false, want true")
		}
		if len(cfg.BatchSizes) != 2 || cfg.BatchSizes[0] != 5 || cfg.BatchSizes[1] != 10 {
			t.Errorf("BatchSizes = %v, want [5 10]", cfg.BatchSizes)
		}
		if cfg.IdleSeconds != 3600 || cfg.TimerMinutes != 10 || cfg.MaxSessions != 8 {
			t.Errorf("idle/timer/sessions = %d/%d/%d", cfg.IdleSeconds, cfg.TimerMinutes, cfg.MaxSessions)
		}
		if cfg.MaxRunning < 1 {
			t.Errorf("MaxRunning = %d, want >= 1 (auto)", cfg.MaxRunning)
		}
		if cfg.StoragePath != "auto" {
			t.Errorf("StoragePath = %q, want auto", cfg.StoragePath)
		}
		if cfg.FourcatImage != "ghcr.io/fourcat/fourcat:1.0.0" || cfg.DeskImage != "ghcr.io/ns/desk:v1.2.3" ||
			cfg.DeskproxyImage != "ghcr.io/ns/deskproxy:v1.2.3" {
			t.Errorf("images = %q/%q/%q", cfg.FourcatImage, cfg.DeskImage, cfg.DeskproxyImage)
		}
		if cfg.ZeeschuimerVersion != "1.2.3" || cfg.TemplateSHA256 != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
			t.Errorf("zeeschuimer/template = %q/%q", cfg.ZeeschuimerVersion, cfg.TemplateSHA256)
		}
		if cfg.COID != "12345" || cfg.COName != "Example CO" {
			t.Errorf("co = %q/%q", cfg.COID, cfg.COName)
		}
		if cfg.LayoutVersion != 1 {
			t.Errorf("LayoutVersion = %d, want 1", cfg.LayoutVersion)
		}
		if cfg.PackageVersion != "v1.2.3" || cfg.AppliedAt != "2026-10-30T12:00:00Z" {
			t.Errorf("package/applied = %q/%q", cfg.PackageVersion, cfg.AppliedAt)
		}
		if cfg.GateSecret != "test-gate-secret" {
			t.Errorf("GateSecret = %q, want trimmed %q", cfg.GateSecret, "test-gate-secret")
		}
	})

	t.Run("unknown key names the key", func(t *testing.T) {
		dir := writeDeskRoot(t, func(m map[string]any) { m["foo"] = "1" }, true)
		_, err := Load(filepath.Join(dir, "desk.json"))
		if err == nil || !strings.Contains(err.Error(), "foo") {
			t.Fatalf("err = %v, want one naming foo", err)
		}
	})

	t.Run("missing key names the key", func(t *testing.T) {
		dir := writeDeskRoot(t, func(m map[string]any) { delete(m, "desk_contact") }, true)
		_, err := Load(filepath.Join(dir, "desk.json"))
		if err == nil || !strings.Contains(err.Error(), "desk_contact") {
			t.Fatalf("err = %v, want one naming desk_contact", err)
		}
	})

	t.Run("missing gate file is an error", func(t *testing.T) {
		dir := writeDeskRoot(t, nil, false)
		if _, err := Load(filepath.Join(dir, "desk.json")); err == nil {
			t.Fatal("err = nil, want an error for the missing gate file")
		}
	})

	t.Run("null string key names the key", func(t *testing.T) {
		dir := writeDeskRoot(t, func(m map[string]any) { m["desk_contact"] = nil }, true)
		_, err := Load(filepath.Join(dir, "desk.json"))
		if err == nil || !strings.Contains(err.Error(), "desk_contact") {
			t.Fatalf("err = %v, want one naming desk_contact", err)
		}
	})

	t.Run("empty desk_contact is an error", func(t *testing.T) {
		dir := writeDeskRoot(t, func(m map[string]any) { m["desk_contact"] = "" }, true)
		_, err := Load(filepath.Join(dir, "desk.json"))
		if err == nil || !strings.Contains(err.Error(), "desk_contact") {
			t.Fatalf("err = %v, want one naming desk_contact", err)
		}
	})

	t.Run("bad desk_platform is an error", func(t *testing.T) {
		for _, p := range []string{"TikTok", "1x", "../etc"} {
			dir := writeDeskRoot(t, func(m map[string]any) { m["desk_platform"] = p }, true)
			_, err := Load(filepath.Join(dir, "desk.json"))
			if err == nil || !strings.Contains(err.Error(), "desk_platform") {
				t.Fatalf("platform %q: err = %v, want one naming desk_platform", p, err)
			}
		}
	})

	t.Run("layout_version 2 is refused and names the value", func(t *testing.T) {
		dir := writeDeskRoot(t, func(m map[string]any) { m["layout_version"] = 2 }, true)
		_, err := Load(filepath.Join(dir, "desk.json"))
		if err == nil || !strings.Contains(err.Error(), "layout_version") || !strings.Contains(err.Error(), "2") {
			t.Fatalf("err = %v, want one naming layout_version and the value 2", err)
		}
	})
}

// TestNumericParamBounds: the numeric parameters accept 1-9 digits, matching
// the shell layer's ^[0-9]{1,9}$.
func TestNumericParamBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
		want  bool // true: Load must succeed
	}{
		{"nine digits ok", "desk_max_sessions", "123456789", true},
		{"ten digits refused", "desk_max_sessions", "1234567890", false},
		{"twenty digits refused", "desk_max_sessions", "12345678901234567890", false},
		{"non-digit refused", "desk_idle_seconds", "90a", false},
		{"empty refused", "desk_timer_minutes", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeDeskRoot(t, func(m map[string]any) { m[tc.key] = tc.value }, true)
			_, err := Load(filepath.Join(dir, "desk.json"))
			if tc.want {
				if err != nil {
					t.Fatalf("Load: %v, want success", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), "1-9 digits") {
				t.Fatalf("err = %v, want one naming %s and \"1-9 digits\"", err, tc.key)
			}
		})
	}
}
