package desklog

import (
	"container/heap"
	crand "crypto/rand"
	"errors"
	"math"
	"math/rand/v2"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
)

// drawKey is a row's key in the weighted draw (ADR-0026): ln(u) * age, the
// log of the Efraimidis-Spirakis key u^(1/w) with w = 1/age. It is at most
// 0, larger wins, and it stays finite where u^age underflows to 0. The age
// is clamped to at least 1 s (ruling F121): an age of 0 would make the key
// 0, the maximum, and draw that row first every time.
func drawKey(u, age float64) float64 {
	if !(age >= 1) { // also catches NaN
		age = 1
	}
	return math.Log(u) * age
}

// drawn is a row in drawN's heap: its key, its score and its row index (the
// tie-break, so equal scores order the same on every run).
type drawn struct {
	key   Key
	score float64
	row   int
}

// minHeap holds the n best rows so far, the worst on top. at maps a key to
// its index in the heap, so a row listed twice in the pool is held once.
type minHeap struct {
	items []drawn
	at    map[Key]int
}

func (h *minHeap) Len() int { return len(h.items) }
func (h *minHeap) Less(i, j int) bool {
	return worse(h.items[i], h.items[j])
}
func (h *minHeap) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.at[h.items[i].key] = i
	h.at[h.items[j].key] = j
}
func (h *minHeap) Push(x any) {
	d := x.(drawn)
	h.at[d.key] = len(h.items)
	h.items = append(h.items, d)
}
func (h *minHeap) Pop() any {
	d := h.items[len(h.items)-1]
	h.items = h.items[:len(h.items)-1]
	delete(h.at, d.key)
	return d
}

// worse reports whether a ranks below b: a lower score, or on a tie the
// later row.
func worse(a, b drawn) bool {
	if a.score != b.score {
		return a.score < b.score
	}
	return a.row > b.row
}

// drawN draws up to n distinct rows not in taken, without replacement, each
// weighted 1/age (ADR-0026): every untaken row, in row order, takes one u
// from rnd and the key drawKey(u, age(kind, id, now)); the n largest keys
// win, returned in descending key order. A nil age weighs every row 1.
func drawN(rows []pool.Row, taken map[Key]bool, n int, now int64, age func(kind, id string, now int64) float64, rnd func() float64) []Key {
	if n <= 0 {
		return nil
	}
	h := &minHeap{items: make([]drawn, 0, n), at: make(map[Key]int, n)}
	for i, row := range rows {
		k := Key{Kind: row.Kind, ID: row.ID}
		if taken[k] {
			continue
		}
		a := 1.0
		if age != nil {
			a = age(row.Kind, row.ID, now)
		}
		d := drawn{key: k, score: drawKey(rnd(), a), row: i}
		if len(h.items) == n && !worse(h.items[0], d) {
			continue // below the n best: cannot enter, so no duplicate check
		}
		if j, ok := h.at[k]; ok { // a row listed twice: keep its better key
			if worse(h.items[j], d) {
				h.items[j].score, h.items[j].row = d.score, d.row
				heap.Fix(h, j)
			}
			continue
		}
		if len(h.items) < n {
			heap.Push(h, d)
			continue
		}
		delete(h.at, h.items[0].key)
		h.items[0] = d
		h.at[k] = 0
		heap.Fix(h, 0)
	}
	keys := make([]Key, len(h.items))
	for i := len(keys) - 1; i >= 0; i-- {
		keys[i] = heap.Pop(h).(drawn).key
	}
	return keys
}

// uSource is the log's u source in (0, 1]: Rand, or a ChaCha8 source seeded
// from crypto/rand on first use. The caller holds l.mu.
func (l *Log) uSource() func() float64 {
	if l.Rand != nil {
		return l.Rand
	}
	if l.rng == nil {
		var seed [32]byte
		_, _ = crand.Read(seed[:]) // never fails (crypto/rand.Read, Go 1.24+)
		l.rng = rand.New(rand.NewChaCha8(seed))
	}
	return func() float64 { return 1 - l.rng.Float64() }
}

// ErrBusy is returned by Claim when the user still has claims that are not
// done. ErrEmpty is returned when no unclaimed pool row is left.
var (
	ErrBusy  = errors.New("claims outstanding")
	ErrEmpty = errors.New("pool exhausted")
)

// Claimed is one of the user's claims: the key and its batch number.
type Claimed struct {
	Key   Key
	Batch int
}

// Problem is a problem report on a key.
type Problem struct {
	Code, Note string
}

// View is the user's view of the log: their claims in log order and which of
// them are opened, done, or reported as a problem.
type View struct {
	Claims   []Claimed // in log order
	Done     map[Key]bool
	Opened   map[Key]bool
	Problems map[Key]Problem
}

// ViewOf walks the user's events in file order: a claim appends (key, batch);
// an open with exec_rc == 0 marks the key opened and done (any other exit
// code, or no exec_rc, marks nothing); a problem records (code, note) and
// marks the key done. A stop (the idle stop) marks nothing, so an
// outstanding claim stays outstanding. Other users' events are ignored.
func (l *Log) ViewOf(user string, evs []Event) View {
	v := View{
		Done:     make(map[Key]bool),
		Opened:   make(map[Key]bool),
		Problems: make(map[Key]Problem),
	}
	for _, e := range evs {
		if e.User != user {
			continue
		}
		k := l.KeyOf(e)
		switch e.Action {
		case "claim":
			v.Claims = append(v.Claims, Claimed{Key: k, Batch: e.Batch})
		case "open":
			if e.ExecRC != nil && *e.ExecRC == 0 {
				v.Opened[k] = true
				v.Done[k] = true
			}
		case "problem":
			v.Problems[k] = Problem{Code: e.Code, Note: e.Note}
			v.Done[k] = true
		}
	}
	return v
}

// Taken is the set of keys any user ever claimed.
func (l *Log) Taken(evs []Event) map[Key]bool {
	taken := make(map[Key]bool)
	for _, e := range evs {
		if e.Action == "claim" {
			taken[l.KeyOf(e)] = true
		}
	}
	return taken
}

// Claim draws n unclaimed pool rows for user, under the log's mutex: at
// random without replacement, each weighted 1/Age (drawN, ADR-0026), never
// in file order. If any of the user's claims is not done it returns ErrBusy;
// if no unclaimed row is left, ErrEmpty. Otherwise it appends one claim
// event per key (batch = the user's highest batch + 1, 1 when none) and
// returns the keys in draw order, the order of the events.
func (l *Log) Claim(user string, n int, rows []pool.Row) ([]Key, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	evs, err := l.read()
	if err != nil {
		return nil, err
	}
	view := l.ViewOf(user, evs)
	for _, c := range view.Claims {
		if !view.Done[c.Key] {
			return nil, ErrBusy
		}
	}
	t := l.now()
	keys := drawN(rows, l.Taken(evs), n, t, l.Age, l.uSource())
	if len(keys) == 0 {
		return nil, ErrEmpty
	}
	batch := 0
	for _, c := range view.Claims {
		if c.Batch > batch {
			batch = c.Batch
		}
	}
	batch++
	claims := make([]Event, 0, len(keys))
	for _, k := range keys {
		claims = append(claims, Event{T: t, User: user, Action: "claim", Kind: k.Kind, ID: k.ID, Batch: batch, Code: ""})
	}
	if err := l.append(claims); err != nil {
		return nil, err
	}
	return keys, nil
}
