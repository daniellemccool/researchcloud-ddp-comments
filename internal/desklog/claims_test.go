package desklog

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/pool"
)

func videoRows(n int) []pool.Row {
	rows := make([]pool.Row, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, pool.Row{ID: fmt.Sprint(i), Kind: "video", Bucket: "training"})
	}
	return rows
}

func keysOfRows(keys []Key) []string {
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	return ids
}

func wantKeys(t *testing.T, got []Key, wantIDs ...string) {
	t.Helper()
	if len(got) != len(wantIDs) {
		t.Fatalf("keys = %v, want %v", keysOfRows(got), wantIDs)
	}
	for i, id := range wantIDs {
		if got[i] != (Key{Kind: "video", ID: id}) {
			t.Fatalf("keys[%d] = %v, want {video %s}", i, got[i], id)
		}
	}
}

func openOK(l *Log, user, id string) {
	if err := l.Append(Event{T: fixedNow, User: user, Action: "open", Kind: "video", ID: id, Code: "", ExecRC: intPtr(0)}); err != nil {
		panic(err)
	}
}

func intPtr(v int) *int { return &v }

func TestClaimTakesNextUnclaimedInOrder(t *testing.T) {
	l := newTestLog(t)
	rows := videoRows(7)
	keys, err := l.Claim("alice", 5, rows)
	if err != nil {
		t.Fatalf("alice claim: %v", err)
	}
	wantKeys(t, keys, "1", "2", "3", "4", "5")
	keys, err = l.Claim("bob", 5, rows)
	if err != nil {
		t.Fatalf("bob claim: %v", err)
	}
	wantKeys(t, keys, "6", "7")
	evs, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := l.ViewOf("alice", evs).Claims; len(got) != 5 || got[0].Batch != 1 {
		t.Fatalf("alice claims = %+v, want 5 claims batch 1", got)
	}
	if got := l.ViewOf("bob", evs).Claims; len(got) != 2 || got[0].Batch != 1 {
		t.Fatalf("bob claims = %+v, want 2 claims batch 1", got)
	}
}

func TestClaimBusyUntilDone(t *testing.T) {
	l := newTestLog(t)
	rows := videoRows(7)
	keys, err := l.Claim("alice", 2, rows)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	wantKeys(t, keys, "1", "2")
	if _, err := l.Claim("alice", 2, rows); !errors.Is(err, ErrBusy) {
		t.Fatalf("second claim err = %v, want ErrBusy", err)
	}
	// an open with a non-zero exit code marks nothing
	if err := l.Append(Event{T: fixedNow, User: "alice", Action: "open", Kind: "video", ID: "1", Code: "", ExecRC: intPtr(1)}); err != nil {
		t.Fatalf("append open rc=1: %v", err)
	}
	if _, err := l.Claim("alice", 2, rows); !errors.Is(err, ErrBusy) {
		t.Fatalf("claim after failed open err = %v, want ErrBusy", err)
	}
	// an open with no exec_rc marks nothing either
	if err := l.Append(Event{T: fixedNow, User: "alice", Action: "open", Kind: "video", ID: "1", Code: ""}); err != nil {
		t.Fatalf("append open without exec_rc: %v", err)
	}
	if _, err := l.Claim("alice", 2, rows); !errors.Is(err, ErrBusy) {
		t.Fatalf("claim after open without exec_rc err = %v, want ErrBusy", err)
	}
	openOK(l, "alice", "1")
	if err := l.Append(Event{T: fixedNow, User: "alice", Action: "problem", Kind: "video", ID: "2", Code: "U", Note: "broken"}); err != nil {
		t.Fatalf("append problem: %v", err)
	}
	keys, err = l.Claim("alice", 2, rows)
	if err != nil {
		t.Fatalf("claim after done: %v", err)
	}
	wantKeys(t, keys, "3", "4")
	evs, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	v := l.ViewOf("alice", evs)
	if len(v.Claims) != 4 {
		t.Fatalf("claims = %+v, want 4", v.Claims)
	}
	if v.Claims[2].Batch != 2 || v.Claims[3].Batch != 2 {
		t.Fatalf("second batch = %d, %d, want 2, 2", v.Claims[2].Batch, v.Claims[3].Batch)
	}
	if !v.Done[Key{Kind: "video", ID: "1"}] || !v.Done[Key{Kind: "video", ID: "2"}] {
		t.Fatalf("done = %v, want keys 1 and 2 done", v.Done)
	}
	if v.Problems[Key{Kind: "video", ID: "2"}] != (Problem{Code: "U", Note: "broken"}) {
		t.Fatalf("problems = %v", v.Problems)
	}
}

func TestClaimEmpty(t *testing.T) {
	l := newTestLog(t)
	rows := videoRows(2)
	if _, err := l.Claim("alice", 2, rows); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	openOK(l, "alice", "1")
	openOK(l, "alice", "2")
	before, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := l.Claim("alice", 2, rows); !errors.Is(err, ErrEmpty) {
		t.Fatalf("claim on exhausted pool err = %v, want ErrEmpty", err)
	}
	after, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("log grew from %d to %d events on ErrEmpty", len(before), len(after))
	}
}

func TestClaimBatchIncrements(t *testing.T) {
	l := newTestLog(t)
	rows := videoRows(9)
	for round, wantBatch := 1, 1; round <= 3; round, wantBatch = round+1, wantBatch+1 {
		keys, err := l.Claim("alice", 3, rows)
		if err != nil {
			t.Fatalf("round %d claim: %v", round, err)
		}
		for _, k := range keys {
			openOK(l, "alice", k.ID)
		}
		evs, err := l.Read()
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		claims := l.ViewOf("alice", evs).Claims
		last := claims[len(claims)-1]
		if last.Batch != wantBatch {
			t.Fatalf("round %d batch = %d, want %d", round, last.Batch, wantBatch)
		}
	}
}

func TestClaimConcurrentTwoUsersNoOverlap(t *testing.T) {
	l := newTestLog(t)
	rows := videoRows(20)
	users := []string{"u1", "u2", "u3", "u4", "u5"}
	var mu sync.Mutex
	got := make(map[string][]Key) // user -> keys returned across attempts
	var wg sync.WaitGroup
	for _, u := range users {
		for attempt := 0; attempt < 2; attempt++ {
			wg.Add(1)
			go func(user string) {
				defer wg.Done()
				keys, err := l.Claim(user, 3, rows)
				if errors.Is(err, ErrBusy) {
					return // second attempt while the first set is outstanding
				}
				if err != nil {
					t.Errorf("claim %s: %v", user, err)
					return
				}
				mu.Lock()
				got[user] = append(got[user], keys...)
				mu.Unlock()
			}(u)
		}
	}
	wg.Wait()
	union := make(map[Key]bool)
	total := 0
	for _, keys := range got {
		for _, k := range keys {
			if union[k] {
				t.Fatalf("key %v claimed twice", k)
			}
			union[k] = true
			total++
		}
	}
	if total != 15 {
		t.Fatalf("total claimed keys = %d, want 15 (5 users x 3)", total)
	}
	for _, u := range users {
		if len(got[u]) != 3 {
			t.Fatalf("user %s got %d keys, want 3", u, len(got[u]))
		}
	}
	evs, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	taken := l.Taken(evs)
	if len(taken) != len(union) {
		t.Fatalf("Taken has %d keys, union has %d", len(taken), len(union))
	}
	for k := range union {
		if !taken[k] {
			t.Fatalf("returned key %v not in Taken", k)
		}
	}
}

func TestViewOfIgnoresOtherUsers(t *testing.T) {
	l := newTestLog(t)
	evs := []Event{
		{T: 1, User: "alice", Action: "claim", Kind: "video", ID: "1", Batch: 1, Code: ""},
		{T: 2, User: "bob", Action: "claim", Kind: "video", ID: "2", Batch: 1, Code: ""},
		{T: 3, User: "bob", Action: "open", Kind: "video", ID: "2", Code: "", ExecRC: intPtr(0)},
		{T: 4, User: "alice", Action: "problem", Kind: "video", ID: "1", Code: "U", Note: "x"},
	}
	v := l.ViewOf("alice", evs)
	if len(v.Claims) != 1 || v.Claims[0].Key != (Key{Kind: "video", ID: "1"}) {
		t.Fatalf("alice claims = %+v, want only key 1", v.Claims)
	}
	if !v.Done[Key{Kind: "video", ID: "1"}] || v.Done[Key{Kind: "video", ID: "2"}] {
		t.Fatalf("alice done = %v, want only key 1", v.Done)
	}
	if len(v.Opened) != 0 {
		t.Fatalf("alice opened = %v, want empty", v.Opened)
	}
	if len(v.Problems) != 1 || v.Problems[Key{Kind: "video", ID: "1"}] != (Problem{Code: "U", Note: "x"}) {
		t.Fatalf("alice problems = %v", v.Problems)
	}
	b := l.ViewOf("bob", evs)
	if len(b.Claims) != 1 || !b.Done[Key{Kind: "video", ID: "2"}] || !b.Opened[Key{Kind: "video", ID: "2"}] {
		t.Fatalf("bob view = %+v", b)
	}
}
