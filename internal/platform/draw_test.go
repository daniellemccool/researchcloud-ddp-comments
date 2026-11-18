package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const (
	tiktokDrawPath = "../../platforms/tiktok/draw.json"
	floor14        = 14 * 86400 // 1209600
	posted0        = 1700000000
)

func tiktokDraw() *Draw {
	return &Draw{FloorSeconds: floor14, Posted: map[string]string{"video": "high32"}}
}

func writeDraw(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "draw.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

var videoKinds = map[string]ItemKind{"video": {}}

func TestDrawAgeHigh32(t *testing.T) {
	d := tiktokDraw()
	const id = "7301444403200000000" // 1700000000 << 32
	if got := d.Age("video", id, posted0+30*86400); got != 30*86400 {
		t.Errorf("Age 30 days after posting = %v, want %d", got, 30*86400)
	}
	if got := d.Age("video", id, posted0+3*86400); got != 1209600 {
		t.Errorf("Age 3 days after posting = %v, want the floor 1209600", got)
	}
}

func TestDrawAgeFallbacks(t *testing.T) {
	d := tiktokDraw()
	now := int64(posted0 + 30*86400)
	cases := []struct {
		name, kind, id string
		now            int64
	}{
		{"kind without a rule", "photo", "7301444403200000000", now},
		{"id that does not parse", "video", "abc", now},
		{"posted after now", "video", "7301444403200000000", posted0 - 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := d.Age(c.kind, c.id, c.now); got != 1209600 {
				t.Errorf("Age = %v, want the floor 1209600", got)
			}
		})
	}
	t.Run("nil draw", func(t *testing.T) {
		var nd *Draw
		if got := nd.Age("video", "7301444403200000000", now); got != 1 {
			t.Errorf("nil Draw Age = %v, want 1 (uniform)", got)
		}
	})
}

func TestLoadDrawMissingIsNil(t *testing.T) {
	d, err := LoadDraw(filepath.Join(t.TempDir(), "draw.json"), videoKinds)
	if err != nil || d != nil {
		t.Fatalf("LoadDraw(missing) = %+v, %v; want nil, nil", d, err)
	}
}

func TestLoadDrawRejectsUnknownField(t *testing.T) {
	_, err := LoadDraw(writeDraw(t, `{"floor_days":14,"x":1}`), videoKinds)
	if err == nil || !strings.Contains(err.Error(), `"x"`) {
		t.Fatalf("LoadDraw error = %v, want one naming the unknown field x", err)
	}
}

func TestLoadDrawRejectsUnknownKindOrRule(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"unknown kind", `{"floor_days":14,"posted_from_id":{"photo":"high32"}}`, "photo"},
		{"unknown rule", `{"floor_days":14,"posted_from_id":{"video":"low32"}}`, "low32"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadDraw(writeDraw(t, c.body), videoKinds)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("LoadDraw error = %v, want one naming %s", err, c.want)
			}
		})
	}
}

func TestLoadDrawRejectsBadFloorOrTrailingContent(t *testing.T) {
	cases := []struct{ name, body string }{
		{"negative floor", `{"floor_days":-1,"posted_from_id":{"video":"high32"}}`},
		{"floor that overflows seconds", `{"floor_days":200000000000000}`},
		{"trailing content", `{"floor_days":14}{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := LoadDraw(writeDraw(t, c.body), videoKinds); err == nil {
				t.Fatalf("LoadDraw accepted %s", c.body)
			}
		})
	}
}

// TestLoadDrawRequiresFloorOfAtLeastOneDay: a floor below 1 day lets one
// just-posted or unparseable ID crowd out the rest, so floor_days must be
// present and at least 1. A platform that wants a uniform draw omits the file.
func TestLoadDrawRequiresFloorOfAtLeastOneDay(t *testing.T) {
	refused := []struct{ name, body string }{
		{"omitted", `{"posted_from_id":{"video":"high32"}}`},
		{"empty object", `{}`},
		{"null", `{"floor_days":null,"posted_from_id":{"video":"high32"}}`},
		{"top-level null", `null`},
		{"zero", `{"floor_days":0,"posted_from_id":{"video":"high32"}}`},
		{"negative", `{"floor_days":-1,"posted_from_id":{"video":"high32"}}`},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadDraw(writeDraw(t, c.body), videoKinds)
			if err == nil || !strings.Contains(err.Error(), "floor_days must be at least 1") {
				t.Fatalf("LoadDraw(%s) error = %v, want one saying floor_days must be at least 1", c.body, err)
			}
		})
	}
	for _, days := range []int64{1, 14} {
		t.Run("accepts "+strconv.FormatInt(days, 10), func(t *testing.T) {
			body := `{"floor_days":` + strconv.FormatInt(days, 10) + `,"posted_from_id":{"video":"high32"}}`
			d, err := LoadDraw(writeDraw(t, body), videoKinds)
			if err != nil {
				t.Fatalf("LoadDraw(%s): %v", body, err)
			}
			if d.FloorSeconds != days*86400 {
				t.Errorf("FloorSeconds = %d, want %d", d.FloorSeconds, days*86400)
			}
		})
	}
}

func TestTiktokDrawFile(t *testing.T) {
	p, err := Load(tiktokPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d, err := LoadDraw(tiktokDrawPath, p.ItemKinds)
	if err != nil {
		t.Fatalf("LoadDraw: %v", err)
	}
	if d == nil {
		t.Fatal("LoadDraw returned nil for the shipped draw.json")
	}
	if d.FloorSeconds != 1209600 {
		t.Errorf("FloorSeconds = %d, want 1209600", d.FloorSeconds)
	}
	if d.Posted["video"] != "high32" {
		t.Errorf(`Posted["video"] = %q, want high32`, d.Posted["video"])
	}
}

// The capture proxy loads platform.json strictly, so an older proxy would
// refuse any draw setting there: the draw lives in draw.json only.
func TestPlatformJSONUnchangedByDraw(t *testing.T) {
	if _, err := Load(tiktokPath); err != nil {
		t.Fatalf("Load: %v", err)
	}
	drawKeys := map[string]bool{"draw": true, "floor_days": true, "posted_from_id": true}
	for _, typ := range []reflect.Type{reflect.TypeOf(rawPlatform{}), reflect.TypeOf(rawItemKind{})} {
		for i := range typ.NumField() {
			tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			if drawKeys[tag] {
				t.Errorf("%s has draw field %q; draw settings belong in draw.json", typ.Name(), tag)
			}
		}
	}

	b, err := os.ReadFile(tiktokPath)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	for k := range drawKeys {
		if _, ok := top[k]; ok {
			t.Errorf("platform.json has top-level key %q; draw settings belong in draw.json", k)
		}
	}
	if strings.Contains(string(b), "posted_from_id") || strings.Contains(string(b), "floor_days") {
		t.Error("platform.json mentions a draw setting; draw settings belong in draw.json")
	}
}
