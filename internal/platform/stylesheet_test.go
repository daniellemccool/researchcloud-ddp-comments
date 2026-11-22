package platform

import (
	"os"
	"strings"
	"testing"
)

// The TikTok stylesheet shows a plain block in place of every image, so a
// collector sees that an image was there and nothing of what it showed. It
// uses no filter: a blur leaves rough shapes and colours, a colour filter
// leaves the outline of an image with a transparent ground (a sticker's
// silhouette, seen on the study desk on 2026-11-22), and either costs
// processor time in a browser that draws without a graphics card. The image
// is still fetched, so TikTok sees the same requests.
func TestTikTokStylesheetShowsPlainBlocksForImages(t *testing.T) {
	raw, err := os.ReadFile("../../platforms/tiktok/userContent.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, want := range []string{
		// the picture is pushed out of its own frame; the frame shows its ground
		`img { object-position: -20000px 0 !important; background-color: #dfe6ee !important; }`,
		// what page code draws has no frame of its own to show: it is hidden
		`canvas, svg image { visibility: hidden !important; }`,
		// a background picture goes; the element and its text stay
		`[style*="background-image"] { background-image: none !important; background-color: #dfe6ee !important; }`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
	for _, gone := range []string{"filter:", "blur("} {
		if strings.Contains(css, gone) {
			t.Errorf("stylesheet still has %q", gone)
		}
	}
}
