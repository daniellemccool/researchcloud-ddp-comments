package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
)

// secondsPerDay converts draw.json's floor_days to seconds.
const secondsPerDay = 86400

// postedRules are the time rules a draw.json may name. "high32" reads the
// posting time, in Unix seconds, from the top 32 bits of a numeric ID.
var postedRules = map[string]bool{"high32": true}

// Draw is a platform's desk-only claim-draw setting, read from
// platforms/<name>/draw.json. It never lives in platform.json: the capture
// proxy loads that file strictly, and an older proxy would refuse new fields.
type Draw struct {
	FloorSeconds int64             // the least age a row is weighted by
	Posted       map[string]string // item kind -> time rule ("high32")
}

type rawDraw struct {
	FloorDays    *int64            `json:"floor_days"`
	PostedFromID map[string]string `json:"posted_from_id"`
}

// LoadDraw reads the draw setting at path. A missing file returns nil, nil,
// and a nil *Draw draws uniformly. An unknown key, a floor_days that is
// missing, null or below 1, a kind not in kinds or an unknown time rule is an
// error: with a floor below 1 day, one just-posted or unparseable ID would
// outweigh the whole pool, and a platform that wants a uniform draw omits the
// file instead.
func LoadDraw(path string, kinds map[string]ItemKind) (*Draw, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var raw rawDraw
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("draw %s: %w", path, err)
	}
	var extra struct{}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("draw %s: unexpected content after the JSON object", path)
	}

	if raw.FloorDays == nil {
		return nil, fmt.Errorf("draw %s: floor_days must be at least 1 (it is missing or null)", path)
	}
	floorDays := *raw.FloorDays
	if floorDays < 1 {
		return nil, fmt.Errorf("draw %s: floor_days must be at least 1, not %d", path, floorDays)
	}
	if floorDays > math.MaxInt64/secondsPerDay {
		return nil, fmt.Errorf("draw %s: floor_days %d is out of range", path, floorDays)
	}
	d := &Draw{FloorSeconds: floorDays * secondsPerDay, Posted: make(map[string]string, len(raw.PostedFromID))}

	// Sorted, so the first error is the same on every run.
	for _, kind := range slices.Sorted(maps.Keys(raw.PostedFromID)) {
		rule := raw.PostedFromID[kind]
		if _, ok := kinds[kind]; !ok {
			return nil, fmt.Errorf("draw %s: posted_from_id.%s: kind %q is not in item_kinds", path, kind, kind)
		}
		if !postedRules[rule] {
			return nil, fmt.Errorf("draw %s: posted_from_id.%s: unknown rule %q", path, kind, rule)
		}
		d.Posted[kind] = rule
	}
	return d, nil
}

// Age is the age in seconds a row is weighted by: max(now - posted, floor).
// A kind without a time rule, an ID that does not parse or a posting time
// after now gets the floor. A nil Draw returns 1, so every row weighs the
// same.
func (d *Draw) Age(kind, id string, now int64) float64 {
	if d == nil {
		return 1
	}
	floor := float64(d.FloorSeconds)
	if d.Posted[kind] != "high32" {
		return floor
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return floor
	}
	posted := int64(n >> 32) // at most 2^32-1: never overflows
	// A posting time after now gives a negative age, which the floor replaces.
	return math.Max(float64(now-posted), floor)
}
