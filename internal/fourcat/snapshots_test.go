package fourcat

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

// Invented dataset keys: 32 lowercase hex characters, as 4CAT's md5 keys.
const (
	keyA = "0123456789abcdef0123456789abcdef"
	keyB = "fedcba9876543210fedcba9876543210"
	keyC = "00000000000000000000000000000abc"
)

// wantSnapshotsSQL is the read, spelled out so a change to any guard in it
// fails this test.
const wantSnapshotsSQL = `SELECT d.key, d.type, d.creator, d.timestamp, d.num_rows, p.j->>'keep' IS NOT NULL, p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' = 'true', false), p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' <> 'true', true) FROM datasets d CROSS JOIN LATERAL (SELECT CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN d.parameters::jsonb END AS j) p WHERE d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND d.is_finished AND d.num_rows > 0 AND jsonb_typeof(p.j) = 'object' AND NOT (p.j ? 'copied_from') AND NOT (p.j ? 'producer_type')`

func lastSQL(t *testing.T, f *docker.Fake) string {
	t.Helper()
	if len(f.Execs) != 1 || f.Execs[0].Container != dbName {
		t.Fatalf("execs = %v, want one psql in %s", f.Execs, dbName)
	}
	cmd := f.Execs[0].Cmd
	return cmd[len(cmd)-1]
}

// TestSnapshotsSQL: the read is constant text, run once through psql.
func TestSnapshotsSQL(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	if _, err := c.Snapshots(ctx); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if got := lastSQL(t, f); got != wantSnapshotsSQL {
		t.Errorf("sql =\n%s\nwant\n%s", got, wantSnapshotsSQL)
	}
}

// TestSnapshotsGuards names each guard of the read, so a guard check shows
// which one went missing.
func TestSnapshotsGuards(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	if _, err := c.Snapshots(ctx); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	sql := lastSQL(t, f)
	for name, frag := range map[string]string{
		"type allowlist":      `d.type IN ('tiktok-search', 'tiktok-comments-search')`,
		"top-level":           `d.key_parent = ''`,
		"finished":            `d.is_finished`,
		"rows":                `d.num_rows > 0`,
		"parsable parameters": `pg_input_is_valid(d.parameters, 'jsonb')`,
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("%s: sql lacks %q", name, frag)
		}
	}
	if strings.Contains(sql, "SELECT d.parameters") || strings.Contains(sql, ", d.parameters,") {
		t.Error("the raw parameters text is selected; it may hold | and newlines")
	}
}

// TestSnapshotsParse: keep, the desk's own mark and a foreign expires-after
// arrive as t/f columns; a line counts only when every field is valid.
func TestSnapshotsParse(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: strings.Join([]string{
		keyA + "|tiktok-search|alice|100|5|t|f|t",
		keyB + "|tiktok-comments-search|alice|200|7|f|t|f",
		keyC + "|tiktok-search|bob|300|9|f|f|t",
		"0123456789ABCDEF0123456789ABCDEF|tiktok-search|alice|100|5|f|f|f", // uppercase key
		keyA + "|csv-import|alice|100|5|f|f|f",                             // type outside the allowlist
		keyA + "|tiktok-search|tag:x|100|5|f|f|f",                          // not a username
		keyA + "|tiktok-search|alice|soon|5|f|f|f",                         // time not an integer
		keyA + "|tiktok-search|alice|100|many|f|f|f",                       // rows not an integer
		keyA + "|tiktok-search|alice|100|5|yes|f|f",                        // not t or f
		keyA + "|tiktok-search|alice|100|5|f|f",                            // a field short
		"broken line",
		"",
	}, "\n")}
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	got, err := c.Snapshots(ctx)
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	want := []Snapshot{
		{Key: keyA, Type: "tiktok-search", Creator: "alice", Timestamp: 100, Rows: 5, Keep: true, ForeignExpiry: true},
		{Key: keyB, Type: "tiktok-comments-search", Creator: "alice", Timestamp: 200, Rows: 7, DeskMarked: true},
		{Key: keyC, Type: "tiktok-search", Creator: "bob", Timestamp: 300, Rows: 9, ForeignExpiry: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshots =\n%+v\nwant\n%+v", got, want)
	}

	f = newFakeWith4CAT(t)
	c = &Client{Docker: f, Backend: backendName, DB: dbName}
	if got, err := c.Snapshots(ctx); err != nil || len(got) != 0 {
		t.Errorf("no datasets: %v, %v; want none and no error", got, err)
	}

	f = newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 2}
	c = &Client{Docker: f, Backend: backendName, DB: dbName}
	if _, err := c.Snapshots(ctx); err == nil {
		t.Error("a failing psql is not an error")
	}
}

// wantMarkGuard is the WHERE clause the mark must carry: the read's scope
// (the two types, top-level, finished, rows, parsable, no standalone copy), and never
// a row with keep or an expires-after, whoever set them. A bare desk-retention
// marker (its expiry removed by 4CAT's keep route) does not refuse the row.
const wantMarkGuard = `d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND d.is_finished AND d.num_rows > 0 AND CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN jsonb_typeof(d.parameters::jsonb) = 'object' AND NOT (d.parameters::jsonb ? 'copied_from') AND NOT (d.parameters::jsonb ? 'producer_type') AND d.parameters::jsonb->>'keep' IS NULL AND NOT (d.parameters::jsonb ? 'expires-after') ELSE false END`

// TestMarkExpiringSQL: one statement for all keys, adding exactly the two
// parameters, guarded in its own WHERE, returning the keys it changed.
func TestMarkExpiringSQL(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: keyB + "\nUPDATE 1\n"}
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	got, err := c.MarkExpiring(ctx, []string{keyA, keyB}, 1700604800)
	if err != nil {
		t.Fatalf("MarkExpiring: %v", err)
	}
	want := `UPDATE datasets d SET parameters = (d.parameters::jsonb || '{"expires-after": 1700604800, "desk-retention": true}')::text WHERE d.key IN ('` + keyA + `', '` + keyB + `') AND ` + wantMarkGuard + ` RETURNING d.key`
	if sql := lastSQL(t, f); sql != want {
		t.Errorf("sql =\n%s\nwant\n%s", sql, want)
	}
	if !reflect.DeepEqual(got, []string{keyB}) {
		t.Errorf("changed = %v, want only %s (the guard skipped the other)", got, keyB)
	}

	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 2}
	if _, err := c.MarkExpiring(ctx, []string{keyA}, 1700604800); err == nil {
		t.Error("a failing psql is not an error")
	}
}

// TestMarkExpiringGuard names the guard of the mark.
func TestMarkExpiringGuard(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	if _, err := c.MarkExpiring(ctx, []string{keyA}, 1700604800); err != nil {
		t.Fatalf("MarkExpiring: %v", err)
	}
	sql := lastSQL(t, f)
	for name, frag := range map[string]string{
		"type allowlist":          `d.type IN ('tiktok-search', 'tiktok-comments-search')`,
		"top-level":               `d.key_parent = ''`,
		"finished":                `d.is_finished`,
		"rows":                    `d.num_rows > 0`,
		"keep":                    `d.parameters::jsonb->>'keep' IS NULL`,
		"existing expiry":         `NOT (d.parameters::jsonb ? 'expires-after')`,
		"parsable":                `CASE WHEN pg_input_is_valid(d.parameters, 'jsonb')`,
		"returns what it changed": `RETURNING d.key`,
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("%s: sql lacks %q", name, frag)
		}
	}
}

// TestUnmarkSQL: removes both parameters, only where the desk's own marker
// is true, returning the keys it changed.
func TestUnmarkSQL(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: keyA + "\n" + keyC + "\nUPDATE 2\n"}
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	got, err := c.Unmark(ctx, []string{keyA, keyB, keyC})
	if err != nil {
		t.Fatalf("Unmark: %v", err)
	}
	want := `UPDATE datasets d SET parameters = (d.parameters::jsonb - 'expires-after' - 'desk-retention')::text WHERE d.key IN ('` + keyA + `', '` + keyB + `', '` + keyC + `') AND d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = '' AND CASE WHEN pg_input_is_valid(d.parameters, 'jsonb') THEN jsonb_typeof(d.parameters::jsonb) = 'object' AND d.parameters::jsonb->'desk-retention' = 'true' ELSE false END RETURNING d.key`
	if sql := lastSQL(t, f); sql != want {
		t.Errorf("sql =\n%s\nwant\n%s", sql, want)
	}
	if !strings.Contains(want, `d.parameters::jsonb->'desk-retention' = 'true'`) {
		t.Fatal("test expectation lost its desk-retention guard")
	}
	if !reflect.DeepEqual(got, []string{keyA, keyC}) {
		t.Errorf("changed = %v, want %s and %s", got, keyA, keyC)
	}

	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 2}
	if _, err := c.Unmark(ctx, []string{keyA}); err == nil {
		t.Error("a failing psql is not an error")
	}
}

// TestUnmarkGuard names the guard of the unmark.
func TestUnmarkGuard(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	if _, err := c.Unmark(ctx, []string{keyA}); err != nil {
		t.Fatalf("Unmark: %v", err)
	}
	sql := lastSQL(t, f)
	for name, frag := range map[string]string{
		"type allowlist": `d.type IN ('tiktok-search', 'tiktok-comments-search')`,
		"top-level":      `d.key_parent = ''`,
		"desk-retention": `d.parameters::jsonb->'desk-retention' = 'true'`,
		"parsable":       `CASE WHEN pg_input_is_valid(d.parameters, 'jsonb')`,
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("%s: sql lacks %q", name, frag)
		}
	}
}

// TestChangedKeysOnlyRequested: a returned line counts only when it is a key
// the call asked for; the command tag and anything else are skipped.
func TestChangedKeysOnlyRequested(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	f.ExecResults["psql -v"] = docker.Exec{ExitCode: 0, Stdout: keyA + "\n" + keyC + "\nnot-a-key\nUPDATE 2\n"}
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	got, err := c.MarkExpiring(ctx, []string{keyA, keyB}, 1700604800)
	if err != nil {
		t.Fatalf("MarkExpiring: %v", err)
	}
	if !reflect.DeepEqual(got, []string{keyA}) {
		t.Errorf("changed = %v, want only %s", got, keyA)
	}
}

// TestEmptyKeysRunNoSQL: nothing to change runs no statement and is no error.
func TestEmptyKeysRunNoSQL(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	if got, err := c.MarkExpiring(ctx, nil, 1700604800); err != nil || len(got) != 0 {
		t.Errorf("MarkExpiring(nil) = %v, %v; want none and no error", got, err)
	}
	if got, err := c.Unmark(ctx, []string{}); err != nil || len(got) != 0 {
		t.Errorf("Unmark(empty) = %v, %v; want none and no error", got, err)
	}
	if len(f.Execs) != 0 {
		t.Errorf("execs = %v, want none", f.Execs)
	}
}

// TestBadInputRunsNoSQL: one bad key anywhere in the list, or a time that is
// not a positive integer, refuses the whole call before any SQL runs.
func TestBadInputRunsNoSQL(t *testing.T) {
	ctx := context.Background()
	bad := []string{
		"0123456789ABCDEF0123456789ABCDEF",  // uppercase
		"0123456789abcdef0123456789abcde",   // 31 characters
		"0123456789abcdef0123456789abcdef0", // 33 characters
		"0123456789abcdef0123456789abcde'",  // a quote
		"0123456789abcdef0123456789abcdeg",  // not hex
		"",
	}
	for _, b := range bad {
		f := newFakeWith4CAT(t)
		c := &Client{Docker: f, Backend: backendName, DB: dbName}
		keys := []string{keyA, b, keyB}
		if _, err := c.MarkExpiring(ctx, keys, 1700604800); err == nil || !strings.Contains(err.Error(), "invalid dataset key") {
			t.Errorf("MarkExpiring with %q: err = %v, want invalid dataset key", b, err)
		}
		if _, err := c.Unmark(ctx, keys); err == nil || !strings.Contains(err.Error(), "invalid dataset key") {
			t.Errorf("Unmark with %q: err = %v, want invalid dataset key", b, err)
		}
		if len(f.Execs) != 0 {
			t.Errorf("bad key %q: execs = %v, want none", b, f.Execs)
		}
	}
	for _, until := range []int64{0, -1} {
		f := newFakeWith4CAT(t)
		c := &Client{Docker: f, Backend: backendName, DB: dbName}
		if _, err := c.MarkExpiring(ctx, []string{keyA}, until); err == nil {
			t.Errorf("MarkExpiring until %d: no error", until)
		}
		if len(f.Execs) != 0 {
			t.Errorf("until %d: execs = %v, want none", until, f.Execs)
		}
	}
}

// excludedBy reports whether sql refuses a row whose parameters carry any of
// keys: it does when it holds `NOT (<j> ? '<key>')` for one of them.
func excludedBy(sql, j string, keys []string) bool {
	for _, k := range keys {
		if strings.Contains(sql, `NOT (`+j+` ? '`+k+`')`) {
			return true
		}
	}
	return false
}

// TestCopiesExcluded: every standalone copy 4CAT makes from a dataset takes
// the input's type and is top-level, so it is recognised by its parameters.
// A filter copy carries copied_from, a refresh or filter copy may carry only
// a non-merge producer_type, a merge output carries both; each is outside the
// read and refused by the mark. A Zeeschuimer upload carries neither.
func TestCopiesExcluded(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	if _, err := c.Snapshots(ctx); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if _, err := c.MarkExpiring(ctx, []string{keyA}, 1700604800); err != nil {
		t.Fatalf("MarkExpiring: %v", err)
	}
	read, mark := f.Execs[0].Cmd[len(f.Execs[0].Cmd)-1], f.Execs[1].Cmd[len(f.Execs[1].Cmd)-1]
	for _, tc := range []struct {
		name string
		keys []string // parameter keys the row carries
		out  bool     // excluded
	}{
		{"filter copy", []string{"datasource", "copied_from", "copied_at"}, true},
		{"refresh copy", []string{"datasource", "producer_type"}, true},
		{"merge output", []string{"datasource", "copied_from", "copied_at", "producer_type", "source"}, true},
		{"zeeschuimer upload", []string{"datasource", "label"}, false},
	} {
		if got := excludedBy(read, "p.j", tc.keys); got != tc.out {
			t.Errorf("%s: read excludes = %v, want %v", tc.name, got, tc.out)
		}
		if got := excludedBy(mark, "d.parameters::jsonb", tc.keys); got != tc.out {
			t.Errorf("%s: mark refuses = %v, want %v", tc.name, got, tc.out)
		}
	}
}

// TestStaleMarker: 4CAT's keep route deletes expires-after and leaves the
// desk's marker; after an unkeep the row carries desk-retention true and
// nothing else. Such a marker is stale: the row reads as a plain snapshot
// (DeskMarked needs an expires-after too) and the mark accepts it, its ||
// overwriting the marker. A row with marker and expiry still reads
// DeskMarked and is still refused, by its expires-after.
func TestStaleMarker(t *testing.T) {
	ctx := context.Background()
	f := newFakeWith4CAT(t)
	c := &Client{Docker: f, Backend: backendName, DB: dbName}
	if _, err := c.Snapshots(ctx); err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if _, err := c.MarkExpiring(ctx, []string{keyA}, 1700604800); err != nil {
		t.Fatalf("MarkExpiring: %v", err)
	}
	read, mark := f.Execs[0].Cmd[len(f.Execs[0].Cmd)-1], f.Execs[1].Cmd[len(f.Execs[1].Cmd)-1]
	if !strings.Contains(read, `, p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' = 'true', false), `) {
		t.Error("stale marker: DeskMarked does not require an expires-after")
	}
	_, guard, ok := strings.Cut(mark, " WHERE ")
	if !ok {
		t.Fatalf("mark has no WHERE: %s", mark)
	}
	if strings.Contains(guard, `'desk-retention'`) {
		t.Error("stale marker: the mark's guard refuses a row by its desk-retention marker")
	}
	if !strings.Contains(mark, `NOT (d.parameters::jsonb ? 'expires-after')`) {
		t.Error("marker plus expiry: the mark's guard does not refuse a row with an expires-after")
	}
}

// TestIsSnapshotType: exactly the two allowed types, spelled exactly.
func TestIsSnapshotType(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		want bool
	}{
		{"tiktok-search", true},
		{"tiktok-comments-search", true},
		{"", false},
		{"tiktok", false},
		{"TikTok-Search", false},
		{"tiktok-search ", false},
		{"instagram-search", false},
		{"expand-datasets", false},
	} {
		if got := IsSnapshotType(tc.typ); got != tc.want {
			t.Errorf("IsSnapshotType(%q) = %v, want %v", tc.typ, got, tc.want)
		}
	}
}
