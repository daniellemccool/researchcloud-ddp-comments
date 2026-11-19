// Package retention decides which of 4CAT's TikTok snapshots the desk marks
// for 4CAT to expire (ADR-0027).
package retention

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/fourcat"
)

type group struct{ creator, typ string }

// Decide applies ADR-0027's rule to every snapshot 4CAT holds and returns the
// keys to mark and the keys to unmark, each sorted. Per creator and type,
// newest first (among equal timestamps the greater key is newer), the rule
// keeps a snapshot among the newest two, one no newer snapshot has at least
// as many rows as, and one that borders a drop: before a drop (its
// next-newer snapshot has fewer rows) or after one (its next-older snapshot
// has more); equal rows are no drop. It calls the rest superseded. Every
// snapshot of an allowed type counts for that, whatever its flags, including
// as a next-newer or next-older one; any other type is ignored. A snapshot
// with `keep` is unmarked if the desk marked it and is otherwise left alone,
// as is one with a foreign expiry. Of the rest, one the rule keeps is
// unmarked if the desk marked it, and one it calls superseded is marked
// unless the desk already marked it. Keys are unique in 4CAT's datasets
// table, so no key comes out twice. Decide is pure: no clock, no I/O, and
// the input is left as it was.
func Decide(snaps []fourcat.Snapshot) (mark, unmark []string) {
	groups := map[group][]fourcat.Snapshot{}
	for _, s := range snaps {
		if fourcat.IsSnapshotType(s.Type) {
			g := group{s.Creator, s.Type}
			groups[g] = append(groups[g], s)
		}
	}
	for _, g := range groups {
		slices.SortFunc(g, func(a, b fourcat.Snapshot) int {
			return cmp.Or(cmp.Compare(b.Timestamp, a.Timestamp), strings.Compare(b.Key, a.Key))
		})
		most := int64(math.MinInt64) // the most rows of any newer snapshot
		for i, s := range g {
			before := i > 0 && g[i-1].Rows < s.Rows       // its next-newer has fewer rows
			after := i+1 < len(g) && g[i+1].Rows > s.Rows // its next-older has more
			superseded := i >= 2 && most >= s.Rows && !before && !after
			most = max(most, s.Rows)
			switch {
			case s.Keep:
				if s.DeskMarked {
					unmark = append(unmark, s.Key)
				}
			case s.ForeignExpiry:
			case !superseded:
				if s.DeskMarked {
					unmark = append(unmark, s.Key)
				}
			case !s.DeskMarked:
				mark = append(mark, s.Key)
			}
		}
	}
	slices.Sort(mark)
	slices.Sort(unmark)
	return mark, unmark
}
