package desklog

import (
	"errors"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
)

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

// Claim takes the next n unclaimed pool rows (file order) for user, under the
// log's mutex. If any of the user's claims is not done it returns ErrBusy; if
// no unclaimed row is left, ErrEmpty. Otherwise it appends one claim event
// per key (batch = the user's highest batch + 1, 1 when none) and returns the
// keys in order.
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
	taken := l.Taken(evs)
	var keys []Key
	for _, row := range rows {
		if len(keys) >= n {
			break
		}
		k := Key{Kind: row.Kind, ID: row.ID}
		if !taken[k] {
			keys = append(keys, k)
		}
	}
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
	t := l.now()
	claims := make([]Event, 0, len(keys))
	for _, k := range keys {
		claims = append(claims, Event{T: t, User: user, Action: "claim", Kind: k.Kind, ID: k.ID, Batch: batch, Code: ""})
	}
	if err := l.append(claims); err != nil {
		return nil, err
	}
	return keys, nil
}
