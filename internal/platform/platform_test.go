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

func TestUnknownKeyRefused(t *testing.T) {
	raw, err := os.ReadFile(tiktokPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(raw), `{"name": "tiktok",`, `{"foo": 1, "name": "tiktok",`, 1)
	if patched == string(raw) {
		t.Fatal("fixture patch did not apply")
	}
	path := filepath.Join(t.TempDir(), "platform.json")
	if err := os.WriteFile(path, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "foo") {
		t.Fatalf("Load error = %v, want one naming foo", err)
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
