package desklog

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
)

const day = 86400

// seededU is a fixed-seed u source in (0, 1], the shape Log.Rand takes.
func seededU(seed uint64) func() float64 {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	return func() float64 { return 1 - r.Float64() }
}

// tiktokDraw is platforms/tiktok/draw.json's setting.
func tiktokDraw() *platform.Draw {
	return &platform.Draw{FloorSeconds: 14 * day, Posted: map[string]string{"video": "high32"}}
}

// tiktokID is a synthetic TikTok-shaped ID posted at the given Unix second.
func tiktokID(posted int64, low uint32) string {
	return fmt.Sprint(uint64(posted)<<32 | uint64(low))
}

// winRatio draws one row of two, draws times, and returns how often a won
// divided by how often b won.
func winRatio(t *testing.T, rows []pool.Row, age func(kind, id string, now int64) float64, rnd func() float64, draws int) float64 {
	t.Helper()
	wins := make(map[string]int)
	for range draws {
		keys := drawN(rows, nil, 1, fixedNow, age, rnd)
		if len(keys) != 1 {
			t.Fatalf("drawN(n=1) = %v, want one key", keys)
		}
		wins[keys[0].ID]++
	}
	if wins[rows[1].ID] == 0 {
		t.Fatalf("row %s never won in %d draws (wins %v)", rows[1].ID, draws, wins)
	}
	return float64(wins[rows[0].ID]) / float64(wins[rows[1].ID])
}

// TestClaimNotFileOrder: a uniform draw over rows in ascending ID order does
// not hand out the first rows of the file (ADR-0026).
func TestClaimNotFileOrder(t *testing.T) {
	l := newTestLog(t)
	l.Rand = seededU(1)
	rows := videoRows(1000)
	keys, err := l.Claim("alice", 10, rows)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(keys) != 10 {
		t.Fatalf("got %d keys, want 10", len(keys))
	}
	first := make(map[Key]bool)
	for _, row := range rows[:10] {
		first[Key{Kind: row.Kind, ID: row.ID}] = true
	}
	inFirst := 0
	for _, k := range keys {
		if first[k] {
			inFirst++
		}
	}
	if inFirst == len(keys) {
		t.Fatalf("claimed %v: the first 10 rows of the file", keysOfRows(keys))
	}
}

// TestClaimWeightRatio: a 15-day-old video wins about 365/15 = 24.3 times as
// often as a one-year-old one.
func TestClaimWeightRatio(t *testing.T) {
	rows := []pool.Row{{ID: "young", Kind: "video"}, {ID: "old", Kind: "video"}}
	age := func(_, id string, _ int64) float64 {
		if id == "young" {
			return 15 * day
		}
		return 365 * day
	}
	if r := winRatio(t, rows, age, seededU(2), 20000); r < 20 || r > 29 {
		t.Fatalf("15-day : 365-day win ratio = %.2f, want 20..29 (expected 24.3)", r)
	}

	// Through Claim: the log's Age reaches the draw. 500 young and 500 old
	// rows; 100 claimed should be almost all young (a uniform draw gives 50).
	l := newTestLog(t)
	l.Rand = seededU(3)
	l.Age = func(_, id string, _ int64) float64 {
		var i int
		_, _ = fmt.Sscan(id, &i)
		if i%2 == 0 {
			return 15 * day
		}
		return 365 * day
	}
	keys, err := l.Claim("alice", 100, videoRows(1000))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	young := 0
	for _, k := range keys {
		var i int
		_, _ = fmt.Sscan(k.ID, &i)
		if i%2 == 0 {
			young++
		}
	}
	if young < 80 {
		t.Fatalf("Claim took %d young of 100, want at least 80: Log.Age does not reach the draw", young)
	}
}

// TestClaimUniformUnderFloor: two rows that both weigh the floor (both
// younger than it, or one of a kind with no time rule) win about equally
// often (Review Focus 4).
func TestClaimUniformUnderFloor(t *testing.T) {
	age := tiktokDraw().Age
	cases := map[string][]pool.Row{
		"both younger than the floor": {
			{ID: tiktokID(fixedNow-3*day, 1), Kind: "video"},
			{ID: tiktokID(fixedNow-10*day, 2), Kind: "video"},
		},
		"a kind with no time rule": {
			{ID: tiktokID(fixedNow-3*day, 1), Kind: "video"},
			{ID: tiktokID(fixedNow-300*day, 2), Kind: "comment"},
		},
	}
	seed := uint64(4)
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			for _, row := range rows {
				if a := age(row.Kind, row.ID, fixedNow); a != 14*day {
					t.Fatalf("fixture: age(%s) = %v, want the floor", row.ID, a)
				}
			}
			if r := winRatio(t, rows, age, seededU(seed), 20000); r < 0.9 || r > 1.1 {
				t.Fatalf("win ratio = %.3f, want 0.9..1.1", r)
			}
		})
		seed++
	}
}

// TestClaimZeroAgeNotAlwaysFirst (ruling F121): an age of 0 is clamped to
// 1 s, so its key is not ln(u)*0 = 0, the maximum, which would draw that row
// first every time. LoadDraw refuses a floor below 1 day, so this is defence
// in depth against an age function that returns 0.
func TestClaimZeroAgeNotAlwaysFirst(t *testing.T) {
	rows := []pool.Row{{ID: "zero", Kind: "video"}, {ID: "one", Kind: "video"}}
	age := func(_, id string, _ int64) float64 {
		if id == "zero" {
			return 0
		}
		return 1
	}
	if r := winRatio(t, rows, age, seededU(6), 20000); r < 0.9 || r > 1.1 {
		t.Fatalf("age-0 : age-1 win ratio = %.3f, want 0.9..1.1 (age clamped to 1 s)", r)
	}
}

// TestClaimKeysFinite: with realistic ages the draw keys stay finite and
// distinct (the log-space form; u^age underflows to 0), and the draw returns
// rows in descending key order.
func TestClaimKeysFinite(t *testing.T) {
	rows := videoRows(1000)
	r := rand.New(rand.NewPCG(7, 8))
	ages := make(map[string]float64, len(rows))
	for _, row := range rows {
		ages[row.ID] = 1.2e6 + r.Float64()*(3e7-1.2e6)
	}
	age := func(_, id string, _ int64) float64 { return ages[id] }
	// drawN takes one u per untaken row in row order: replay that sequence.
	replay := seededU(9)
	score := make(map[string]float64, len(rows))
	for _, row := range rows {
		score[row.ID] = drawKey(replay(), ages[row.ID])
	}
	keys := drawN(rows, nil, 50, fixedNow, age, seededU(9))
	if len(keys) != 50 {
		t.Fatalf("got %d keys, want 50", len(keys))
	}
	seenKey := make(map[Key]bool)
	seenScore := make(map[float64]bool)
	for i, k := range keys {
		s := score[k.ID]
		if math.IsInf(s, 0) || math.IsNaN(s) {
			t.Fatalf("key of %s = %v, want finite", k.ID, s)
		}
		if seenKey[k] {
			t.Fatalf("row %s drawn twice", k.ID)
		}
		if seenScore[s] {
			t.Fatalf("draw key %v repeats (row %s): keys are not distinct", s, k.ID)
		}
		seenKey[k], seenScore[s] = true, true
		if i > 0 && s > score[keys[i-1].ID] {
			t.Fatalf("keys[%d] scores %v above keys[%d] %v: not in descending key order", i, s, i-1, score[keys[i-1].ID])
		}
	}
}

// TestClaimNeverRepeatsOrRetakes: rows another user claimed, and rows this
// user claimed in an earlier batch, are never returned; a pool row listed
// twice is never claimed twice, within a batch or across batches.
func TestClaimNeverRepeatsOrRetakes(t *testing.T) {
	l := newTestLog(t)
	l.Rand = seededU(10)
	var rows []pool.Row
	for i := 1; i <= 60; i++ {
		row := pool.Row{ID: fmt.Sprint(i), Kind: "video", Bucket: "training"}
		rows = append(rows, row)
		if i > 20 {
			rows = append(rows, row) // listed twice
		}
	}
	bobs := make(map[Key]bool)
	for i := 1; i <= 20; i++ {
		k := Key{Kind: "video", ID: fmt.Sprint(i)}
		bobs[k] = true
		if err := l.Append(Event{T: 1, User: "bob", Action: "claim", Kind: k.Kind, ID: k.ID, Batch: 1, Code: ""}); err != nil {
			t.Fatal(err)
		}
	}
	got := make(map[Key]bool)
	for batch := 1; ; batch++ {
		keys, err := l.Claim("alice", 7, rows)
		if errors.Is(err, ErrEmpty) {
			break
		}
		if err != nil {
			t.Fatalf("batch %d: %v", batch, err)
		}
		for _, k := range keys {
			if bobs[k] {
				t.Fatalf("batch %d returned %v, which bob claimed", batch, k)
			}
			if got[k] {
				t.Fatalf("batch %d returned %v a second time", batch, k)
			}
			got[k] = true
			openOK(l, "alice", k.ID)
		}
	}
	if len(got) != 40 {
		t.Fatalf("alice got %d distinct keys, want the 40 unclaimed", len(got))
	}
}

// TestClaimConcurrentDistinct: two users claiming 50 each from 100 rows at
// the same moment get 100 distinct keys (the log's mutex; Review Focus 5).
// It uses the default random source, so -race covers its lazy start.
func TestClaimConcurrentDistinct(t *testing.T) {
	for round := range 10 {
		l := newTestLog(t)
		rows := videoRows(100)
		var wg sync.WaitGroup
		got := make([][]Key, 2)
		for i, user := range []string{"alice", "bob"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				keys, err := l.Claim(user, 50, rows)
				if err != nil {
					t.Errorf("round %d: claim %s: %v", round, user, err)
				}
				got[i] = keys
			}()
		}
		wg.Wait()
		union := make(map[Key]bool)
		for _, keys := range got {
			for _, k := range keys {
				union[k] = true
			}
		}
		if len(union) != 100 {
			t.Fatalf("round %d: %d distinct keys over both claims, want 100", round, len(union))
		}
	}
}

// BenchmarkClaim1M: one claim of 10 from 1,000,000 TikTok-shaped rows
// posted over two years, weighted by the TikTok rule.
func BenchmarkClaim1M(b *testing.B) {
	r := rand.New(rand.NewPCG(11, 12))
	rows := make([]pool.Row, 1_000_000)
	for i := range rows {
		posted := fixedNow - r.Int64N(730*day)
		rows[i] = pool.Row{ID: tiktokID(posted, uint32(i)), Kind: "video", Bucket: "training"}
	}
	l := &Log{
		Path:        b.TempDir() + "/state/desk-log.ndjson",
		DefaultKind: "video",
		Now:         func() int64 { return fixedNow },
		Age:         tiktokDraw().Age,
	}
	for i := 0; b.Loop(); i++ {
		if _, err := l.Claim(fmt.Sprint("u", i), 10, rows); err != nil {
			b.Fatal(err)
		}
	}
}
