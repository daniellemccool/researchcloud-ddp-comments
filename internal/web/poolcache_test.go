package web

import (
	"bufio"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// poolWays runs test twice: with the pool read on every request (Deps.Pool
// nil) and through one cache (Deps.Pool set). In the cached way the pool's
// rows (rows of them, unchanged during the test) must be checked once only,
// so every request after the first used the cached snapshot.
func poolWays(t *testing.T, rows int, test func(t *testing.T, fill func(*Deps))) {
	t.Run("direct", func(t *testing.T) { test(t, nil) })
	t.Run("cached", func(t *testing.T) {
		var checks atomic.Int64
		test(t, func(d *Deps) {
			c := NewPoolCache(d.Cfg, d.Platform)
			valid := c.Valid
			c.Valid = func(kind, id string) bool {
				checks.Add(1)
				return valid(kind, id)
			}
			d.Pool = c
		})
		if n := checks.Load(); n != int64(rows) {
			t.Errorf("pool rows checked %d times, want %d (one read)", n, rows)
		}
	})
}

// benchRows is the real pool's size (Task 6, ruling F124).
const benchRows = 1_250_000

// writeBenchPool writes a pool.csv of n TikTok-like video IDs (header id,
// posting times spread over two years before now), as cmd/poolexport
// writes them. The IDs are synthetic.
func writeBenchPool(b *testing.B, root string, n int) {
	b.Helper()
	f, err := os.Create(filepath.Join(root, "pool.csv"))
	if err != nil {
		b.Fatal(err)
	}
	w := bufio.NewWriter(f)
	rng := rand.New(rand.NewPCG(1, 2))
	now := uint64(time.Now().Unix())
	_, _ = w.WriteString("id\n")
	for range n {
		posted := now - rng.Uint64N(2*365*86400)
		_, _ = w.WriteString(strconv.FormatUint(posted<<32|uint64(rng.Uint32()), 10))
		_ = w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
}

// benchEnv is a ready collector page over a pool of benchRows, claims drawn
// by age as on TikTok, the pool through a cache when cached is true.
func benchEnv(b *testing.B, cached bool) *collectorEnv {
	b.Helper()
	draw := &platform.Draw{FloorSeconds: 14 * 86400, Posted: map[string]string{"video": "high32"}}
	e := readyPageEnvWith(b, true, func(d *Deps) {
		d.Log.Age = draw.Age
		if cached {
			d.Pool = NewPoolCache(d.Cfg, d.Platform)
		}
	})
	writeBenchPool(b, e.root, benchRows)
	return e
}

// benchClaim measures the second and later claims, each by a new collector.
func benchClaim(b *testing.B, cached bool) {
	e := benchEnv(b, cached)
	if rec := e.post(b, "alice", collectorRole, "/desk/claim?n=10"); rec.Code != http.StatusNoContent {
		b.Fatalf("first claim: status = %d, want 204", rec.Code)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		user := "c" + strconv.Itoa(i)
		writeProvisionState(b, e.root, user, allDoneSteps())
		b.StartTimer()
		if rec := e.post(b, user, collectorRole, "/desk/claim?n=10"); rec.Code != http.StatusNoContent {
			b.Fatalf("claim %d: status = %d, want 204", i, rec.Code)
		}
	}
}

// benchPage measures the second and later views of a page with ten claims.
func benchPage(b *testing.B, cached bool) {
	e := benchEnv(b, cached)
	if rec := e.post(b, "alice", collectorRole, "/desk/claim?n=10"); rec.Code != http.StatusNoContent {
		b.Fatalf("claim: status = %d, want 204", rec.Code)
	}
	if rec := e.get(b, "alice", collectorRole, "/desk/"); rec.Code != http.StatusOK {
		b.Fatalf("first view: status = %d, want 200", rec.Code)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if rec := e.get(b, "alice", collectorRole, "/desk/"); rec.Code != http.StatusOK {
			b.Fatalf("view %d: status = %d, want 200", i, rec.Code)
		}
	}
}

func BenchmarkClaimCached(b *testing.B) { benchClaim(b, true) }
func BenchmarkClaimDirect(b *testing.B) { benchClaim(b, false) }
func BenchmarkPageCached(b *testing.B)  { benchPage(b, true) }
func BenchmarkPageDirect(b *testing.B)  { benchPage(b, false) }
