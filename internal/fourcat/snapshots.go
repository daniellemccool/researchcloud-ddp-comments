package fourcat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Snapshot is one Zeeschuimer upload as 4CAT holds it (ADR-0027): a
// finished, top-level dataset of an allowed type with at least one row.
// Keep means 4CAT's `keep` is present and not null (the expire worker skips
// such a row); DeskMarked means `desk-retention` is JSON true and an
// `expires-after` is present; ForeignExpiry means an `expires-after` is
// present without the desk's marker. A marker without an expiry is stale
// (4CAT's keep route deletes `expires-after` and leaves it) and reads as a
// plain snapshot.
type Snapshot struct {
	Key, Type, Creator string
	Timestamp, Rows    int64
	Keep, DeskMarked   bool
	ForeignExpiry      bool
}

// keyRe is 4CAT's dataset key: an md5 hexdigest (common/lib/dataset.py
// get_key, common/lib/helpers.py hash_to_md5). A key is validated against it
// before it is interpolated into any SQL.
var keyRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// snapshotTypes is the type allowlist; snapshotScope is the same list as SQL.
var snapshotTypes = map[string]bool{"tiktok-search": true, "tiktok-comments-search": true}

const snapshotScope = `d.type IN ('tiktok-search', 'tiktok-comments-search') AND d.key_parent = ''`

// IsSnapshotType reports whether t is one of the two allowed types, for a
// caller that must not trust a Snapshot's Type blindly.
func IsSnapshotType(t string) bool { return snapshotTypes[t] }

// withRows narrows the scope to finished datasets that hold rows: 4CAT also
// finishes a failed or empty import, with 0 rows.
const withRows = ` AND d.is_finished AND d.num_rows > 0`

// parsable guards every jsonb cast: 4CAT writes json.dumps output, which
// jsonb can still refuse (NaN, or a \u0000 escape), and a statement must not
// die on such a row. A row it refuses is never read as a snapshot and never
// changed.
const parsable = `pg_input_is_valid(d.parameters, 'jsonb')`

// snapshotsSQL is constant text. It never selects the raw parameters (they
// may hold | and newlines); the three judgements arrive as t/f columns.
// Every standalone copy 4CAT makes (merge, filter, refresh) takes the input's
// type and is top-level; it is recognised by `copied_from` (written by
// DataSet.copy) or `producer_type` (written by adopt_type), whatever their
// values. A Zeeschuimer upload carries neither; were one ever to, the pass
// would mark nothing, which is the safe failure.
const snapshotsSQL = `SELECT d.key, d.type, d.creator, d.timestamp, d.num_rows, ` +
	`p.j->>'keep' IS NOT NULL, ` +
	`p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' = 'true', false), ` +
	`p.j ? 'expires-after' AND coalesce(p.j->'desk-retention' <> 'true', true) ` +
	`FROM datasets d CROSS JOIN LATERAL (SELECT CASE WHEN ` + parsable + ` THEN d.parameters::jsonb END AS j) p ` +
	`WHERE ` + snapshotScope + withRows + ` AND jsonb_typeof(p.j) = 'object' ` +
	`AND NOT (p.j ? 'copied_from') AND NOT (p.j ? 'producer_type')`

// markGuard keeps the mark inside the read's scope, and skips a row that has
// `keep` or already has `expires-after`, whoever set them: the read and this
// write are separate statements, and an admin's change in between is never
// overwritten. A row the desk marked always has `expires-after`, so it is
// never re-marked; a stale marker alone does not refuse a row, and the ||
// overwrites it.
const markGuard = snapshotScope + withRows + ` AND CASE WHEN ` + parsable + ` THEN jsonb_typeof(d.parameters::jsonb) = 'object' ` +
	`AND NOT (d.parameters::jsonb ? 'copied_from') AND NOT (d.parameters::jsonb ? 'producer_type') ` +
	`AND d.parameters::jsonb->>'keep' IS NULL ` +
	`AND NOT (d.parameters::jsonb ? 'expires-after') ELSE false END`

// unmarkGuard touches only a row carrying the desk's own marker.
const unmarkGuard = `CASE WHEN ` + parsable + ` THEN jsonb_typeof(d.parameters::jsonb) = 'object' ` +
	`AND d.parameters::jsonb->'desk-retention' = 'true' ELSE false END`

// Snapshots reads every snapshot once. A line counts only when it has eight
// fields, a valid key, an allowed type, a valid username as creator,
// integer time and rows, and t/f judgements; every other line is skipped.
func (c *Client) Snapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := c.psql(ctx, snapshotsSQL)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) != 8 || !keyRe.MatchString(f[0]) || !snapshotTypes[f[1]] || !userRe.MatchString(f[2]) {
			continue
		}
		at, err1 := strconv.ParseInt(f[3], 10, 64)
		rows, err2 := strconv.ParseInt(f[4], 10, 64)
		keep, ok1 := pgBool(f[5])
		desk, ok2 := pgBool(f[6])
		foreign, ok3 := pgBool(f[7])
		if err1 != nil || err2 != nil || !ok1 || !ok2 || !ok3 {
			continue
		}
		snaps = append(snaps, Snapshot{Key: f[0], Type: f[1], Creator: f[2], Timestamp: at, Rows: rows,
			Keep: keep, DeskMarked: desk, ForeignExpiry: foreign})
	}
	return snaps, nil
}

// MarkExpiring adds `expires-after = until` and `desk-retention = true` to
// the given snapshots in one statement and returns the keys it changed,
// which markGuard may make fewer than it was given.
func (c *Client) MarkExpiring(ctx context.Context, keys []string, until int64) ([]string, error) {
	if until <= 0 {
		return nil, fmt.Errorf("invalid expiry time: %d", until)
	}
	in, err := keyList(keys)
	if err != nil || in == "" {
		return nil, err
	}
	sql := `UPDATE datasets d SET parameters = (d.parameters::jsonb || '{"expires-after": ` +
		strconv.FormatInt(until, 10) + `, "desk-retention": true}')::text WHERE d.key IN (` + in + `) AND ` +
		markGuard + ` RETURNING d.key`
	return c.changed(ctx, sql, keys)
}

// Unmark removes `expires-after` and `desk-retention` from the given
// snapshots in one statement, only where `desk-retention` is true, and
// returns the keys it changed.
func (c *Client) Unmark(ctx context.Context, keys []string) ([]string, error) {
	in, err := keyList(keys)
	if err != nil || in == "" {
		return nil, err
	}
	sql := `UPDATE datasets d SET parameters = (d.parameters::jsonb - 'expires-after' - 'desk-retention')::text ` +
		`WHERE d.key IN (` + in + `) AND ` + snapshotScope + ` AND ` + unmarkGuard + ` RETURNING d.key`
	return c.changed(ctx, sql, keys)
}

// keyList validates every key before any SQL runs and renders them as a
// quoted list; no keys is the empty string and no error.
func keyList(keys []string) (string, error) {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		if !keyRe.MatchString(k) {
			return "", errors.New("invalid dataset key")
		}
		quoted[i] = "'" + k + "'"
	}
	return strings.Join(quoted, ", "), nil
}

// changed runs an UPDATE … RETURNING and collects the returned keys that
// were asked for; psql's command tag and any other line are skipped.
func (c *Client) changed(ctx context.Context, sql string, keys []string) ([]string, error) {
	out, err := c.psql(ctx, sql)
	if err != nil {
		return nil, err
	}
	asked := make(map[string]bool, len(keys))
	for _, k := range keys {
		asked[k] = true
	}
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if k := strings.TrimSpace(line); asked[k] {
			got = append(got, k)
		}
	}
	return got, nil
}

// pgBool reads psql's unaligned boolean.
func pgBool(s string) (v, ok bool) {
	switch s {
	case "t":
		return true, true
	case "f":
		return false, true
	}
	return false, false
}
