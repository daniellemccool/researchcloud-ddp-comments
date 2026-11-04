package provision

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTemplateIdempotent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if st := env.prov.Advance(ctx, "alice", false); !st.Ready() {
		t.Fatalf("not ready: %v", st.Steps)
	}

	profile := filepath.Join(env.root, "state", "firefox", "alice", "profile")
	marker := filepath.Join(profile, "marker.txt")
	writeFile(t, marker, "collector data")
	// A stale staging dir left by a crashed run.
	stale := filepath.Join(env.root, "state", "firefox", "alice", ".template.tmp")
	writeFile(t, filepath.Join(stale, "junk"), "junk")

	st := env.prov.Advance(ctx, "alice", false)
	if !st.Ready() {
		t.Fatalf("second Advance not ready: %v", st.Steps)
	}
	if got := readFile(t, marker); got != "collector data" {
		t.Errorf("marker file = %q, want it to survive the second Advance", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .template.tmp still present (err = %v)", err)
	}
}

func TestTemplateMissing(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if err := os.RemoveAll(filepath.Join(env.root, "templates", "release", generation)); err != nil {
		t.Fatal(err)
	}

	st := env.prov.Advance(ctx, "alice", false)
	if st.Ready() {
		t.Fatal("ready without a release template")
	}
	want := "error: template missing: templates/release/" + generation
	if got := st.Steps["template"]; got != want {
		t.Errorf("template step = %q, want %q", got, want)
	}
	if st.Current() != "template" || st.Error() != want {
		t.Errorf("Current() = %q Error() = %q, want template / %q", st.Current(), st.Error(), want)
	}
	if st.Steps["registry"] != "done" || st.Steps["dirs"] != "done" {
		t.Errorf("earlier steps = %v", st.Steps)
	}
}

func TestHideMediaCSS(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	if st := env.prov.Advance(ctx, "alice", false); !st.Ready() {
		t.Fatalf("not ready: %v", st.Steps)
	}
	css := filepath.Join(env.root, "state", "firefox", "alice", "profile", "chrome", "userContent.css")
	if got := readFile(t, css); got != "/* hide media */\n" {
		t.Errorf("userContent.css = %q, want the platform stylesheet", got)
	}
	if got := statMode(t, css); got != 0o644 {
		t.Errorf("userContent.css mode %v, want 0644", got)
	}

	// HideMedia off: the stylesheet is removed on the next run of the step.
	env.cfg.HideMedia = false
	if err := os.Remove(StatePath(env.cfg, "alice")); err != nil {
		t.Fatal(err)
	}
	if st := env.prov.Advance(ctx, "alice", false); !st.Ready() {
		t.Fatalf("second Advance not ready: %v", st.Steps)
	}
	if _, err := os.Stat(css); !os.IsNotExist(err) {
		t.Errorf("userContent.css still present with HideMedia off (err = %v)", err)
	}
}

func TestHideMediaCSSMissing(t *testing.T) {
	env := newTestEnv(t)
	if err := os.Remove(filepath.Join(env.root, "platform", "userContent.css")); err != nil {
		t.Fatal(err)
	}

	st := env.prov.Advance(context.Background(), "alice", false)
	want := "error: hide-media stylesheet missing: platform/userContent.css"
	if got := st.Steps["template"]; got != want {
		t.Errorf("template step = %q, want %q", got, want)
	}
	if st.Current() != "template" {
		t.Errorf("Current() = %q, want template", st.Current())
	}
}
