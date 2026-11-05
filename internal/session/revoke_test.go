package session

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/provision"
)

// screenCloser records the closeScreens calls Revoke makes; each call runs
// at (optional), with the call's index from 1.
type screenCloser struct {
	mu    sync.Mutex
	calls []string
	at    func(n int, user string)
}

func (s *screenCloser) close(user string) int {
	s.mu.Lock()
	s.calls = append(s.calls, user)
	n := len(s.calls)
	s.mu.Unlock()
	if s.at != nil {
		s.at(n, user)
	}
	return 0
}

func (s *screenCloser) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// rowStatus returns user's registry status, "" when the user has no row.
func (f *fixture) rowStatus(t *testing.T, user string) string {
	t.Helper()
	rows, err := f.reg.Rows()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.User == user {
			return r.Status
		}
	}
	return ""
}

// retire marks user's row retired, as a revoke (or an operator) would.
func (f *fixture) retire(t *testing.T, user string) {
	t.Helper()
	if err := f.reg.Retire(user); err != nil {
		t.Fatal(err)
	}
}

// registryDone reports whether user's provision.json marks registry done.
func (f *fixture) registryDone(user string) bool {
	return provision.Load(f.cfg, user).Steps["registry"] == "done"
}

// absentSet is the poll's view in the session tests.
type absentSet map[string]bool

func (a absentSet) Absent(user string) bool { return a[user] }

// Retire and MarkUndone happen before the first Docker call, so a crash at
// any later point leaves a state the next poll finishes; the screens are
// closed before the stop and again after it.
func TestRevokeOrder(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	before := len(f.fake.Calls)
	sc := &screenCloser{at: func(n int, user string) {
		calls := f.fake.Calls[before:]
		switch n {
		case 1:
			if len(calls) != 0 {
				t.Errorf("first closeScreens after Docker calls %v; want before any", calls)
			}
			if got := f.rowStatus(t, user); got != "retired" {
				t.Errorf("row at first closeScreens = %q, want retired", got)
			}
			if f.registryDone(user) {
				t.Error("registry still done at first closeScreens; MarkUndone must come first")
			}
		case 2:
			stopped := false
			for _, c := range calls {
				if c == "ContainerStop desk-firefox-alice" {
					stopped = true
				}
			}
			if !stopped {
				t.Errorf("second closeScreens before the stop: calls %v", calls)
			}
		}
	}}
	if err := f.m.Revoke(context.Background(), "alice", sc.close); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if n := sc.count(); n != 2 {
		t.Errorf("closeScreens called %d times, want 2", n)
	}
	f.gone(t, "alice")
}

// A Docker error on the stop still closes the screens twice and is returned.
func TestRevokeClosesScreensWhenStopFails(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	f.fake.Fail["ContainerStop desk-firefox-alice"] = errors.New("docker is down")
	sc := &screenCloser{}
	err := f.m.Revoke(context.Background(), "alice", sc.close)
	if err == nil || !strings.Contains(err.Error(), "docker is down") {
		t.Errorf("Revoke = %v, want the stop's error", err)
	}
	if n := sc.count(); n != 2 {
		t.Errorf("closeScreens called %d times, want 2 even when the stop fails", n)
	}
	if got := f.rowStatus(t, "alice"); got != "retired" {
		t.Errorf("row = %q, want retired", got)
	}
}

func TestRevokeIdempotent(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	sc := &screenCloser{}
	ctx := context.Background()
	if err := f.m.Revoke(ctx, "alice", sc.close); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	reg, err := os.ReadFile(f.reg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.Revoke(ctx, "alice", sc.close); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	again, err := os.ReadFile(f.reg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(reg) != string(again) {
		t.Errorf("registry changed on the second Revoke:\n%s\nthen\n%s", reg, again)
	}
	f.gone(t, "alice")
	if got := f.rowStatus(t, "alice"); got != "retired" {
		t.Errorf("row = %q, want retired", got)
	}
	if f.registryDone("alice") {
		t.Error("registry step done after Revoke")
	}
}

// A revoke cut short after Retire but before MarkUndone (or a row retired by
// hand) is finished by the next poll: the step is marked undone.
func TestRevokeAbsentFinishesCutShortRevoke(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.retire(t, "alice")
	if !f.registryDone("alice") {
		t.Fatal("setup: registry should still be done")
	}
	got := f.m.RevokeAbsent(context.Background(), map[string]bool{}, (&screenCloser{}).close)
	if !reflect.DeepEqual(got, []string{"alice"}) {
		t.Errorf("RevokeAbsent = %v, want [alice]", got)
	}
	if f.registryDone("alice") {
		t.Error("registry still done; the cut-short revoke was not finished")
	}
	// Finished: a further poll selects nobody.
	if got := f.m.RevokeAbsent(context.Background(), map[string]bool{}, (&screenCloser{}).close); len(got) != 0 {
		t.Errorf("second RevokeAbsent = %v, want none", got)
	}
}

// An empty present set never revokes a live row (ADR-0005: an empty list
// changes nothing), even with a running pair; a retired row with registry
// still done is finished all the same.
func TestRevokeAbsentEmptyPresentRevokesNoLiveRow(t *testing.T) {
	f := newFixture(t, 4, "alice", "bob")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	f.retire(t, "bob")
	sc := &screenCloser{}
	for _, present := range []map[string]bool{nil, {}} {
		got := f.m.RevokeAbsent(context.Background(), present, sc.close)
		if len(got) > 1 || (len(got) == 1 && got[0] != "bob") {
			t.Errorf("RevokeAbsent(%v) = %v; want no live user revoked", present, got)
		}
	}
	if got := f.rowStatus(t, "alice"); got != "live" {
		t.Errorf("alice row = %q, want live", got)
	}
	if !f.m.IsRunning(context.Background(), "alice") {
		t.Error("alice's pair was stopped by an empty list")
	}
	if !f.registryDone("alice") {
		t.Error("alice's registry step marked undone by an empty list")
	}
	if f.registryDone("bob") {
		t.Error("bob's cut-short revoke was not finished")
	}
	for _, u := range sc.calls {
		if u == "alice" {
			t.Error("closeScreens(alice) called on an empty list")
		}
	}
}

// Selection: live rows absent from the list, plus retired rows with anything
// running; a present live user and a finished retired user are left alone.
func TestRevokeAbsentSelectsLiveAndRetiredRunning(t *testing.T) {
	f := newFixture(t, 4, "alice", "bob", "carol", "dave")
	f.runningPair(t, "carol", "tiktok-1.14.3")
	f.startFirefox(t, "bob")
	f.retire(t, "bob")
	if err := provision.MarkUndone(f.cfg, "bob", "registry"); err != nil {
		t.Fatal(err)
	}
	f.retire(t, "dave") // finished revoke: retired, nothing running, registry undone
	if err := provision.MarkUndone(f.cfg, "dave", "registry"); err != nil {
		t.Fatal(err)
	}
	sc := &screenCloser{}
	got := f.m.RevokeAbsent(context.Background(), map[string]bool{"carol": true}, sc.close)
	if !reflect.DeepEqual(got, []string{"alice", "bob"}) {
		t.Errorf("RevokeAbsent = %v, want [alice bob]", got)
	}
	f.gone(t, "bob")
	if got := f.rowStatus(t, "alice"); got != "retired" {
		t.Errorf("alice row = %q, want retired", got)
	}
	if got := f.rowStatus(t, "carol"); got != "live" {
		t.Errorf("carol row = %q, want live", got)
	}
	if !f.m.IsRunning(context.Background(), "carol") {
		t.Error("carol's pair was stopped")
	}
	for _, c := range f.fake.Calls {
		if strings.HasSuffix(c, "-dave") {
			t.Errorf("finished revoke of dave touched Docker: %s", c)
		}
	}
	for _, u := range sc.calls {
		if u == "carol" || u == "dave" {
			t.Errorf("closeScreens(%s) called", u)
		}
	}
}

// A user whose row turned live again between the selection and the lock
// (re-added and reactivated on a visit) is not revoked when the list holds
// them.
func TestRevokeAbsentSkipsReactivatedPresentUser(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	if ok, err := f.m.revokeIf(context.Background(), "alice", map[string]bool{"alice": true}, (&screenCloser{}).close); ok || err != nil {
		t.Errorf("revokeIf = %v, %v; want false, nil for a live user the list holds", ok, err)
	}
	if got := f.rowStatus(t, "alice"); got != "live" {
		t.Errorf("alice row = %q, want live", got)
	}
	if !f.m.IsRunning(context.Background(), "alice") {
		t.Error("alice's pair was stopped")
	}
}

// A provisioning run queued on the user lock during a revoke cannot
// reactivate the row: the registry step refuses a user absent from the list.
func TestQueuedAdvanceCannotReactivate(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	prov := &provision.Provisioner{Cfg: f.cfg, Reg: f.reg, Locks: f.m.Locks, Members: absentSet{"alice": true}}
	result := make(chan provision.State, 1)
	sc := &screenCloser{at: func(n int, user string) {
		if n != 1 {
			return
		}
		go func() { result <- prov.Advance(context.Background(), user, false) }()
		time.Sleep(50 * time.Millisecond) // let it block on the user lock
	}}
	if err := f.m.Revoke(context.Background(), "alice", sc.close); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	st := <-result
	if got := st.Steps["registry"]; got != "error: access removed" {
		t.Errorf("queued Advance registry step = %q, want %q", got, "error: access removed")
	}
	if got := f.rowStatus(t, "alice"); got != "retired" {
		t.Errorf("alice row = %q after the queued Advance, want retired", got)
	}
}

// A page reload during the revoke blocks on the user lock and then finds the
// row retired: Gone, and nothing is started.
func TestRevokeReloadNeverRestarts(t *testing.T) {
	f := newFixture(t, 4, "alice")
	f.runningPair(t, "alice", "tiktok-1.14.3")
	type startOut struct {
		res StartResult
		err error
	}
	out := make(chan startOut, 1)
	sc := &screenCloser{at: func(n int, user string) {
		if n != 1 {
			return
		}
		go func() {
			res, err := f.m.Start(context.Background(), user)
			out <- startOut{res, err}
		}()
		time.Sleep(50 * time.Millisecond) // let it block on the user lock
	}}
	before := len(f.fake.Calls)
	if err := f.m.Revoke(context.Background(), "alice", sc.close); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got := <-out
	if got.res != Gone || got.err != nil {
		t.Errorf("Start during Revoke = %v, %v; want Gone, nil", got.res, got.err)
	}
	for _, c := range f.fake.Calls[before:] {
		if strings.HasPrefix(c, "ContainerCreate") || strings.HasPrefix(c, "NetworkCreate") || strings.HasPrefix(c, "ContainerStart") {
			t.Errorf("a pair was started during the revoke: %v", f.fake.Calls[before:])
			break
		}
	}
	f.gone(t, "alice")
}
