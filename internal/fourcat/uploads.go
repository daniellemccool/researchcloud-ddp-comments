package fourcat

import (
	"context"
	"strconv"
	"strings"
)

// Uploads is, per owner and dataset type, the time (unix seconds) of the
// latest finished upload that holds at least one row.
type Uploads map[string]map[string]int64

// latestUploadsSQL is constant text: no value from a request ever enters it.
// The row count is part of the rule: 4CAT v1.57 also marks a failed or empty
// import finished, with 0 rows (common/lib/dataset.py, finish_with_error and
// finish), and such a dataset must never count as an upload.
const latestUploadsSQL = `SELECT o.name, d.type, max(d.timestamp) FROM datasets d JOIN datasets_owners o ON o.key = d.key WHERE d.is_finished AND d.num_rows > 0 AND o.role = 'owner' GROUP BY o.name, d.type`

// LatestUploads reads 4CAT's dataset table once, through the same psql path
// as the settings and the tokens. A line counts only when it has exactly
// three fields, a valid username as owner (4CAT also has `tag:…` owners) and
// an integer time; every other line is skipped. No datasets is an empty map,
// not an error.
func (c *Client) LatestUploads(ctx context.Context) (Uploads, error) {
	out, err := c.psql(ctx, latestUploadsSQL)
	if err != nil {
		return nil, err
	}
	ups := Uploads{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		if len(f) != 3 || !userRe.MatchString(f[0]) || f[1] == "" {
			continue
		}
		at, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			continue
		}
		if ups[f[0]] == nil {
			ups[f[0]] = map[string]int64{}
		}
		ups[f[0]][f[1]] = at
	}
	return ups, nil
}
