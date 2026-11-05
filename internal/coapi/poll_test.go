package coapi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 11, 5, 9, 7, 0, 0, time.UTC) }

func TestPollFailureKeepsLastList(t *testing.T) {
	fail := false
	p := &Poller{Now: fixedNow, Fetch: func(context.Context) ([]Member, error) {
		if fail {
			return nil, errors.New("down")
		}
		return []Member{{Username: "alice"}}, nil
	}}
	present, ok := p.Poll(context.Background())
	if !ok || !present["alice"] {
		t.Fatalf("first poll: %v %v", present, ok)
	}
	if p.Absent("alice") || !p.Absent("bob") {
		t.Fatal("after good poll: alice present, bob absent")
	}
	fail = true
	if _, ok := p.Poll(context.Background()); ok {
		t.Fatal("failed poll reported ok")
	}
	if p.Absent("alice") || !p.Absent("bob") {
		t.Fatal("failure changed Absent")
	}
	if p.Failures() != 1 {
		t.Fatalf("failures = %d", p.Failures())
	}
	fail = false
	p.Poll(context.Background())
	if p.Failures() != 0 {
		t.Fatalf("failures not zeroed: %d", p.Failures())
	}
}

// An empty list changes nothing whether Fetch reports it as ErrEmpty or
// returns zero members with a nil error: it is a failed poll.
func TestPollEmptyListChangesNothing(t *testing.T) {
	cases := []struct {
		name  string
		fetch func() ([]Member, error)
	}{
		{"zero members nil error", func() ([]Member, error) { return []Member{}, nil }},
		{"nil members nil error", func() ([]Member, error) { return nil, nil }},
		{"ErrEmpty", func() ([]Member, error) { return nil, ErrEmpty }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			p := &Poller{Now: fixedNow, Fetch: func(context.Context) ([]Member, error) {
				n++
				if n == 1 {
					return []Member{{Username: "alice"}}, nil
				}
				return tc.fetch()
			}}
			p.Poll(context.Background())
			present, ok := p.Poll(context.Background())
			if ok || present != nil {
				t.Fatalf("empty list: Poll = %v, %v; want nil, false", present, ok)
			}
			if p.Absent("alice") {
				t.Fatal("empty list made alice absent")
			}
			if !p.Absent("bob") {
				t.Fatal("empty list dropped the last good list")
			}
			if p.Failures() != 1 {
				t.Fatalf("failures = %d, want 1", p.Failures())
			}
		})
	}
}

func TestAbsentWithoutGoodList(t *testing.T) {
	p := &Poller{Now: fixedNow, Fetch: func(context.Context) ([]Member, error) { return nil, errors.New("x") }}
	if p.Absent("alice") {
		t.Fatal("Absent true with no good list")
	}
	p.Poll(context.Background())
	if p.Absent("alice") {
		t.Fatal("Absent true after only failures")
	}
}

func TestStatusLineAlarm(t *testing.T) {
	good := true
	p := &Poller{Now: fixedNow, Fetch: func(context.Context) ([]Member, error) {
		if good {
			return []Member{{Username: "alice"}}, nil
		}
		return nil, errors.New("x")
	}}
	if got, want := p.StatusLine(), "ALARM: CO poll: no good poll yet, 0 failures"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	p.Poll(context.Background())
	if got, want := p.StatusLine(), "CO poll: last good 09:07, 0 failures"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	good = false
	for i := 0; i < 4; i++ {
		p.Poll(context.Background())
	}
	if got, want := p.StatusLine(), "CO poll: last good 09:07, 4 failures"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	p.Poll(context.Background())
	if got, want := p.StatusLine(), "ALARM: CO poll: last good 09:07, 5 failures"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStatusLineNoGoodPollFailures(t *testing.T) {
	p := &Poller{Now: fixedNow, Fetch: func(context.Context) ([]Member, error) { return nil, errors.New("x") }}
	p.Poll(context.Background())
	if got, want := p.StatusLine(), "ALARM: CO poll: no good poll yet, 1 failures"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
