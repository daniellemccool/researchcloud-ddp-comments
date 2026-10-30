package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tiktokPath = "../../platforms/tiktok/platform.json"

func TestLoadTikTok(t *testing.T) {
	p, err := Load(tiktokPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Captures) != 1 || p.Captures[0].Name != "comments" {
		t.Fatalf("captures = %+v, want one rule named comments", p.Captures)
	}
	rule := p.Captures[0]
	if rule.PostID.From != "query" {
		t.Errorf("PostID.From = %q, want query", rule.PostID.From)
	}
	if rule.MaxBodyBytes != 16<<20 {
		t.Errorf("MaxBodyBytes = %d, want %d", rule.MaxBodyBytes, 16<<20)
	}
	if len(p.KeepCookies) != 3 {
		t.Errorf("KeepCookies = %d, want 3", len(p.KeepCookies))
	}
}

func TestHostMatches(t *testing.T) {
	p, err := Load(tiktokPath)
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{
		"www.tiktok.com": true,
		"tiktok.com":     true,
		"nottiktok.com":  false,
	} {
		if got := p.HostMatches(host); got != want {
			t.Errorf("HostMatches(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestItemURL(t *testing.T) {
	p, err := Load(tiktokPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ItemURL("video", "123")
	if err != nil {
		t.Fatalf("ItemURL: %v", err)
	}
	if want := "https://www.tiktok.com/@x/video/123/"; got != want {
		t.Errorf("ItemURL = %q, want %q", got, want)
	}
	if _, err := p.ItemURL("video", "abc"); err == nil {
		t.Error("ItemURL(video, abc) succeeded, want error")
	}
}

// loadPatched loads the TikTok definition after one textual patch (or an
// appended suffix when old is empty).
func loadPatched(t *testing.T, old, replacement string) error {
	t.Helper()
	raw, err := os.ReadFile(tiktokPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := string(raw) + replacement
	if old != "" {
		patched = strings.Replace(string(raw), old, replacement, 1)
		if patched == string(raw) {
			t.Fatalf("fixture patch %q did not apply", old)
		}
	}
	path := filepath.Join(t.TempDir(), "platform.json")
	if err := os.WriteFile(path, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	return err
}

func TestUnknownKeyRefused(t *testing.T) {
	cases := []struct{ name, old, replacement, key string }{
		{"top level", `{"name": "tiktok",`, `{"foo": 1, "name": "tiktok",`, "foo"},
		{"capture rule", `{"name": "comments",`, `{"capkey": 1, "name": "comments",`, "capkey"},
		{"keep_cookies entry", `{"host": ".tiktok.com",`, `{"cookiekey": 1, "host": ".tiktok.com",`, "cookiekey"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := loadPatched(t, c.old, c.replacement)
			if err == nil || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("Load error = %v, want one naming %s", err, c.key)
			}
		})
	}
}

func TestTrailingContentRefused(t *testing.T) {
	if err := loadPatched(t, "", "{}"); err == nil {
		t.Fatal("Load accepted content after the JSON object")
	}
}
