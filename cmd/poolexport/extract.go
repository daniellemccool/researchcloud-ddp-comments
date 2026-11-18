package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// stats are the counts a run reports. They hold no file name, participant
// identifier or row content, only numbers.
type stats struct {
	Files       int            // *-tiktok.json files read
	NonRegular  int            // *-tiktok.json entries ignored: symlinks, folders, ...
	TestSkipped int            // *-tiktok.json files of participant=test, never read
	Rows        map[string]int // rows seen per table read
	Skipped     int            // rows whose link held no video ID
	Distinct    int            // distinct video IDs in the export
	AlreadySent int            // of those, IDs already on the sent list
	Written     int            // IDs written to upload files
}

// exportObject is one element of an export file: the tables read, by name.
// Other keys are ignored.
type exportObject struct {
	WatchHistory   []json.RawMessage `json:"tiktok_watch_history"`
	LikeList       []json.RawMessage `json:"tiktok_like_list"`
	FavoriteVideos []json.RawMessage `json:"tiktok_favorite_videos"`
	Comments       []json.RawMessage `json:"tiktok_comments"`
}

// tableRows pairs a table's name and link column with its rows in o, in the
// order the report lists the tables.
type tableRows struct {
	name, col string
	rows      []json.RawMessage
}

func (o exportObject) tables() []tableRows {
	return []tableRows{
		{"tiktok_watch_history", "Link", o.WatchHistory},
		{"tiktok_like_list", "Link", o.LikeList},
		{"tiktok_favorite_videos", "Link", o.FavoriteVideos},
		{"tiktok_comments", "Url", o.Comments},
	}
}

// tableOrder is the order the report lists the tables in.
var tableOrder = []string{"tiktok_watch_history", "tiktok_like_list", "tiktok_favorite_videos", "tiktok_comments"}

// videoID is the digit run after /video/ in both the share link
// (www.tiktokv.com/share/video/<id>/) and the web link
// (www.tiktok.com/@<user>/video/<id>).
var videoID = regexp.MustCompile(`/video/([0-9]+)`)

// tiktokSuffix marks the export files read; onboarding files and other
// platforms' files are ignored.
const tiktokSuffix = "-tiktok.json"

// extractIDs returns the distinct video IDs of the export at path: a folder
// (its regular *-tiktok.json files, not recursive) or a zip (its regular
// *-tiktok.json entries at any depth, read in memory, never extracted).
// *-tiktok.json entries that are not regular files (symlinks, folders) are
// counted and ignored. A link that is not a JSON string, is empty or holds no
// /video/<digits> is skipped and counted; unknown tables and keys are ignored.
// A file that is refused or does not decode stops the run, after every file
// was tried: the error lists each bad file by its position among the regular
// *-tiktok.json names (full entry names, for a zip) in byte order, its size
// and the fault, never by its name (which carries the participant) or any of
// its content.
func extractIDs(path string) (map[string]struct{}, stats, error) {
	st := stats{Rows: map[string]int{}}
	src, err := openExport(path)
	if err != nil {
		return nil, st, err
	}
	defer func() { _ = src.close() }()
	st.NonRegular = src.nonRegular
	st.TestSkipped = src.testSkipped
	ids := map[string]struct{}{}
	var bad []string
	for i, e := range src.entries {
		if fault := extractEntry(e, ids, &st); fault != "" {
			bad = append(bad, fmt.Sprintf("file %d %s", i+1, fault))
			continue
		}
		st.Files++
	}
	if len(bad) > 0 {
		note := ""
		if st.NonRegular > 0 {
			note = fmt.Sprintf(" (%d non-regular tiktok entries not counted in the positions)", st.NonRegular)
		}
		return nil, st, fmt.Errorf("%d of %d tiktok files are not valid (positions in byte order of name, see docs/pool-export.md)%s; nothing written: %s",
			len(bad), len(src.entries), note, strings.Join(bad, "; "))
	}
	st.Distinct = len(ids)
	return ids, st, nil
}

// extractEntry adds the IDs of one export file to ids. When the file is
// refused, cannot be read or does not decode it returns the fault as
// "(<size> bytes): <what>", which never carries the name or any of the file's
// content; otherwise "".
func extractEntry(e entry, ids map[string]struct{}, st *stats) string {
	size := "size unknown"
	if e.size >= 0 {
		size = fmt.Sprintf("%d bytes", e.size)
	}
	if e.refuse != "" {
		return fmt.Sprintf("(%s): %s", size, e.refuse)
	}
	b, err := e.read(maxEntryBytes)
	if errors.Is(err, errTooLarge) {
		return fmt.Sprintf("(%s): %v", size, err)
	}
	if err != nil {
		return fmt.Sprintf("(%s): unreadable: %v", size, err)
	}
	var objs []exportObject
	if err := json.Unmarshal(b, &objs); err != nil {
		var syn *json.SyntaxError
		var typ *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syn):
			return fmt.Sprintf("(%d bytes): not valid JSON at byte %d", len(b), syn.Offset)
		case errors.As(err, &typ):
			return fmt.Sprintf("(%d bytes): wrong JSON type at byte %d", len(b), typ.Offset)
		default:
			return fmt.Sprintf("(%d bytes): not a JSON array of tables", len(b))
		}
	}
	for _, obj := range objs {
		for _, t := range obj.tables() {
			for _, row := range t.rows {
				st.Rows[t.name]++
				if id, ok := rowID(row, t.col); ok {
					ids[id] = struct{}{}
				} else {
					st.Skipped++
				}
			}
		}
	}
	return ""
}

// rowID returns the video ID in column col of row, if the row is an object
// whose column is a string holding /video/<digits>.
func rowID(row json.RawMessage, col string) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(row, &fields); err != nil {
		return "", false
	}
	raw, ok := fields[col]
	if !ok {
		return "", false
	}
	var link *string
	if err := json.Unmarshal(raw, &link); err != nil || link == nil {
		return "", false
	}
	m := videoID.FindStringSubmatch(*link)
	if m == nil {
		return "", false
	}
	return m[1], true
}
