package retention

import (
	"fmt"
	"slices"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

const (
	posts    = "tiktok-search"
	comments = "tiktok-comments-search"
)

// key is an invented dataset key: n as 32 lowercase hex digits, so keys sort
// as their numbers do.
func key(n int) string { return fmt.Sprintf("%032x", n) }

func snap(n int, creator, typ string, at, rows int64) fourcat.Snapshot {
	return fourcat.Snapshot{Key: key(n), Type: typ, Creator: creator, Timestamp: at, Rows: rows}
}

func kept(s fourcat.Snapshot) fourcat.Snapshot    { s.Keep = true; return s }
func marked(s fourcat.Snapshot) fourcat.Snapshot  { s.DeskMarked = true; return s }
func foreign(s fourcat.Snapshot) fourcat.Snapshot { s.ForeignExpiry = true; return s }

func keys(ns ...int) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = key(n)
	}
	return out
}

// check runs Decide and compares both results; nil and empty are equal.
func check(t *testing.T, snaps []fourcat.Snapshot, wantMark, wantUnmark []string) {
	t.Helper()
	mark, unmark := Decide(snaps)
	if !slices.Equal(mark, wantMark) {
		t.Errorf("mark = %v, want %v", mark, wantMark)
	}
	if !slices.Equal(unmark, wantUnmark) {
		t.Errorf("unmark = %v, want %v", unmark, wantUnmark)
	}
}

// TestNewestTwoKept: a collector with one or two uploads loses nothing; with
// three growing uploads only the oldest is marked.
func TestNewestTwoKept(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 10),
		snap(2, "bob", posts, 100, 10),
		snap(3, "bob", posts, 200, 20),
		snap(4, "carol", posts, 100, 10),
		snap(5, "carol", posts, 200, 20),
		snap(6, "carol", posts, 300, 30),
	}, keys(4), nil)
}

// TestPreDropKept: after a drop, the older, larger snapshot is never marked,
// however many smaller ones follow it, and nor is the first smaller one, the
// other side of the drop; a small one a newer one supersedes is.
func TestPreDropKept(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 100),
		snap(2, "alice", posts, 200, 10),
		snap(3, "alice", posts, 300, 20),
		snap(4, "alice", posts, 400, 30),
		snap(5, "alice", posts, 500, 40),
	}, keys(3), nil)
}

// TestEqualRowsSupersede: a newer snapshot with the same rows supersedes.
func TestEqualRowsSupersede(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 50),
		snap(2, "alice", posts, 200, 50),
		snap(3, "alice", posts, 300, 50),
		snap(4, "alice", posts, 400, 50),
	}, keys(1, 2), nil)
}

// TestDropBothSidesKept: both sides of a drop are kept, the larger snapshot
// before it and the smaller one after it; the rest outside the newest two
// that a newer one supersedes are marked.
func TestDropBothSidesKept(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 100),
		snap(2, "alice", posts, 200, 300),
		snap(3, "alice", posts, 300, 50),
		snap(4, "alice", posts, 400, 80),
		snap(5, "alice", posts, 500, 400),
		snap(6, "alice", posts, 600, 500),
	}, keys(1, 4), nil)
}

// TestDropSurvivesCleanup: once the desk's marks have done their work, both
// sides of the drop are still kept, so the evidence of the drop stays.
func TestDropSurvivesCleanup(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(2, "alice", posts, 200, 300),
		snap(3, "alice", posts, 300, 50),
		snap(5, "alice", posts, 500, 400),
		snap(6, "alice", posts, 600, 500),
	}, nil, nil)
}

// TestEqualRowsIsNoDrop: equal rows are no drop, so neither of two equal
// snapshots is kept for bordering the other.
func TestEqualRowsIsNoDrop(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 200),
		snap(2, "alice", posts, 200, 200),
		snap(3, "alice", posts, 300, 300),
		snap(4, "alice", posts, 400, 400),
	}, keys(1, 2), nil)
}

// TestConsecutiveDrops: in two drops in a row, the snapshot between them is
// after one and before the other; every side is kept.
func TestConsecutiveDrops(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 300),
		snap(2, "alice", posts, 200, 50),
		snap(3, "alice", posts, 300, 20),
		snap(4, "alice", posts, 400, 400),
		snap(5, "alice", posts, 500, 500),
	}, nil, nil)
}

// TestMarkedSnapshotBesideDropIsUnmarked: a snapshot the desk marked that now
// borders a drop is unmarked: alice's before the drop, bob's after it.
func TestMarkedSnapshotBesideDropIsUnmarked(t *testing.T) {
	check(t, []fourcat.Snapshot{
		marked(snap(1, "alice", posts, 100, 300)),
		snap(2, "alice", posts, 200, 50),
		snap(3, "alice", posts, 300, 400),
		snap(4, "alice", posts, 400, 500),
		snap(5, "bob", posts, 100, 300),
		marked(snap(6, "bob", posts, 200, 50)),
		snap(7, "bob", posts, 300, 400),
		snap(8, "bob", posts, 400, 500),
	}, nil, keys(1, 6))
}

// TestForeignExpiryUntouched: a superseded snapshot with an expiry the desk
// did not set is neither marked nor unmarked, in or out of the newest two.
func TestForeignExpiryUntouched(t *testing.T) {
	check(t, []fourcat.Snapshot{
		foreign(snap(1, "alice", posts, 100, 10)),
		snap(2, "alice", posts, 200, 20),
		snap(3, "alice", posts, 300, 30),
		foreign(snap(4, "bob", posts, 100, 10)),
	}, nil, nil)
}

// TestForeignExpiryStillCounts: a snapshot with a foreign expiry still takes
// a place among its group's newest two (alice's 2 is third), and still
// supersedes an older one (bob's 5, which only the foreign 6 outgrows).
func TestForeignExpiryStillCounts(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 10),
		snap(2, "alice", posts, 200, 10),
		snap(3, "alice", posts, 300, 10),
		foreign(snap(4, "alice", posts, 400, 40)),
		snap(5, "bob", posts, 100, 30),
		foreign(snap(6, "bob", posts, 200, 40)),
		snap(7, "bob", posts, 300, 10),
		snap(8, "bob", posts, 400, 10),
	}, keys(1, 2, 5), nil)
}

// TestMergeOutputsIgnored: a dataset of any other type is ignored entirely:
// never marked or unmarked, and never in a group's newest two.
func TestMergeOutputsIgnored(t *testing.T) {
	for _, typ := range []string{"", "expand-datasets", "instagram-search", "TikTok-Search", "tiktok-search "} {
		t.Run(typ, func(t *testing.T) {
			check(t, []fourcat.Snapshot{
				snap(1, "alice", typ, 100, 10),
				snap(2, "alice", typ, 200, 20),
				snap(3, "alice", typ, 300, 30),
				marked(snap(4, "alice", typ, 400, 40)),
				snap(5, "alice", posts, 50, 10),
				snap(6, "alice", posts, 60, 20),
			}, nil, nil)
		})
	}
}

// TestUnmarkWhenNoLongerSuperseded: the desk takes its mark off a snapshot
// the rule now keeps, whether among the newest two (an admin deleted newer
// ones) or as a pre-drop snapshot.
func TestUnmarkWhenNoLongerSuperseded(t *testing.T) {
	check(t, []fourcat.Snapshot{
		marked(snap(1, "alice", posts, 100, 10)),
		snap(2, "alice", posts, 200, 20),
		marked(snap(3, "bob", posts, 100, 100)),
		snap(4, "bob", posts, 200, 10),
		snap(5, "bob", posts, 300, 20),
	}, nil, keys(1, 3))
}

// TestAlreadyMarkedNotRemarked: a superseded snapshot the desk marked is in
// neither result, so its expiry never moves.
func TestAlreadyMarkedNotRemarked(t *testing.T) {
	check(t, []fourcat.Snapshot{
		marked(snap(1, "alice", posts, 100, 10)),
		snap(2, "alice", posts, 200, 20),
		snap(3, "alice", posts, 300, 30),
	}, nil, nil)
}

// TestKeptAndMarkedIsUnmarked: `keep` with the desk's mark is unmarked,
// superseded or not; `keep` alone is in neither result.
func TestKeptAndMarkedIsUnmarked(t *testing.T) {
	check(t, []fourcat.Snapshot{
		kept(marked(snap(1, "alice", posts, 100, 10))),
		kept(snap(2, "alice", posts, 150, 15)),
		snap(3, "alice", posts, 200, 20),
		kept(marked(snap(4, "alice", posts, 300, 30))),
	}, nil, keys(1, 4))
}

// TestKeptStillCounts: a kept snapshot still takes a place among its group's
// newest two (alice's 2 is third), and still supersedes an older one (bob's
// 5, which only the kept 6 outgrows).
func TestKeptStillCounts(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 10),
		snap(2, "alice", posts, 200, 10),
		snap(3, "alice", posts, 300, 10),
		kept(snap(4, "alice", posts, 400, 40)),
		snap(5, "bob", posts, 100, 30),
		kept(snap(6, "bob", posts, 200, 40)),
		snap(7, "bob", posts, 300, 10),
		snap(8, "bob", posts, 400, 10),
	}, keys(1, 2, 5), nil)
}

// TestGroupsByCreatorAndType: a creator's comments never supersede their
// posts, and one creator's uploads never supersede another's.
func TestGroupsByCreatorAndType(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 10),
		snap(2, "alice", posts, 200, 20),
		snap(3, "alice", comments, 300, 30),
		snap(4, "alice", comments, 400, 40),
		snap(5, "bob", posts, 500, 50),
		snap(6, "bob", posts, 600, 60),
	}, nil, nil)
}

// TestTimestampTieByKey: among equal timestamps the greater key is newer,
// whatever the input order.
func TestTimestampTieByKey(t *testing.T) {
	in := []fourcat.Snapshot{
		snap(1, "alice", posts, 100, 5),
		snap(2, "alice", posts, 100, 5),
		snap(3, "alice", posts, 200, 1),
		snap(4, "alice", posts, 300, 1),
	}
	check(t, in, keys(1), nil)
	check(t, []fourcat.Snapshot{in[3], in[1], in[2], in[0]}, keys(1), nil)
	check(t, []fourcat.Snapshot{in[0], in[2], in[1], in[3]}, keys(1), nil)
}

// TestResultsSortedByKey: both results come out in key order, across groups
// and against the newest-first order within one (alice's keys grow with time).
func TestResultsSortedByKey(t *testing.T) {
	check(t, []fourcat.Snapshot{
		snap(9, "bob", posts, 100, 10),
		snap(8, "bob", posts, 200, 20),
		snap(7, "bob", posts, 300, 30),
		marked(snap(6, "bob", posts, 400, 40)),
		snap(1, "alice", posts, 100, 10),
		snap(2, "alice", posts, 200, 20),
		snap(3, "alice", posts, 300, 30),
		marked(snap(4, "alice", posts, 400, 40)),
		snap(5, "alice", posts, 500, 50),
	}, keys(1, 2, 3, 8, 9), keys(4, 6))
}

// TestInputUnchanged: Decide neither reorders nor changes its input.
func TestInputUnchanged(t *testing.T) {
	in := []fourcat.Snapshot{
		snap(3, "alice", posts, 300, 30),
		marked(snap(1, "alice", posts, 100, 10)),
		snap(2, "alice", posts, 200, 20),
		snap(4, "alice", posts, 400, 40),
		kept(marked(snap(5, "bob", comments, 100, 10))),
	}
	want := slices.Clone(in)
	Decide(in)
	if !slices.Equal(in, want) {
		t.Errorf("input = %v, want %v", in, want)
	}
}

// TestEmptyInput: no snapshots, no marks and no unmarks.
func TestEmptyInput(t *testing.T) {
	check(t, nil, nil, nil)
	check(t, []fourcat.Snapshot{}, nil, nil)
}
