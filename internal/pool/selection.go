package pool

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Reject is a single bad row of an uploaded selection: the physical 1-based
// line in the upload and the user-facing reason.
type Reject struct {
	Line   int
	Reason string
}

// SelectionError means the whole selection file is unusable (as opposed to
// single bad rows, which are rejects): no header, a header without an id
// column, an unreadable CSV, or a quoted field running over several lines.
type SelectionError struct {
	Line   int
	Reason string
}

func (e *SelectionError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
}

const quotedFieldReason = "a quoted field runs over several lines (unbalanced quote?)"

// idNames are the header names (after normalisation) that mean the id column.
var idNames = map[string]bool{"id": true, "videoid": true, "video_id": true}

// headerSep matches the runs of whitespace and dashes a header name may carry.
var headerSep = regexp.MustCompile(`[\s\-]+`)

// normHeader normalises a header name: lower-cased, trimmed, runs of
// whitespace and '-' replaced with '_' ("Video ID" == "video_id" == "id").
func normHeader(name string) string {
	return headerSep.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
}

// sniffDelimiter picks the delimiter: ';' when the first 4096 bytes hold more
// ';' than ',', else ','.
func sniffDelimiter(text string) rune {
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	if strings.Count(head, ";") > strings.Count(head, ",") {
		return ';'
	}
	return ','
}

// ParseSelection parses an uploaded selection into (valid rows, rejects).
// Row-level problems are rejects; file-level ones return a *SelectionError
// and no rows. It accepts a leading BOM, CRLF and a ';' or ',' delimiter; the
// first non-blank line is the header, whose names are case-, space- and
// dash-insensitive; kind, bucket and note are optional and a missing kind
// cell means the default kind. Reject line numbers are the physical lines of
// the upload, 1-based.
func ParseSelection(text string, kinds Kinds) (rows []Row, rejects []Reject, err error) {
	text = strings.TrimLeft(text, "\uFEFF")
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = false
	r.Comma = sniffDelimiter(text)
	var cols map[string]int
	for {
		rec, readErr := r.Read()
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			var parseErr *csv.ParseError
			if errors.As(readErr, &parseErr) {
				// A quoted field left open at EOF surfaces as ErrQuote on a
				// later line than the record started on; Python's reader
				// returns the record with the embedded newline instead, so
				// report it as the multi-line quoted field it is.
				if errors.Is(parseErr.Err, csv.ErrQuote) && parseErr.Line > parseErr.StartLine {
					return nil, nil, &SelectionError{Line: parseErr.StartLine, Reason: quotedFieldReason}
				}
				return nil, nil, &SelectionError{Line: parseErr.Line, Reason: "unreadable: " + parseErr.Err.Error()}
			}
			return nil, nil, readErr
		}
		line, _ := r.FieldPos(0)
		multiLine := false
		for _, cell := range rec {
			if strings.ContainsAny(cell, "\n\r") {
				multiLine = true
				break
			}
		}
		if multiLine {
			return nil, nil, &SelectionError{Line: line, Reason: quotedFieldReason}
		}
		blank := true
		for _, cell := range rec {
			if strings.TrimSpace(cell) != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}
		if cols == nil {
			cols = make(map[string]int, len(rec))
			for i, name := range rec {
				n := normHeader(name)
				key := n
				if idNames[n] {
					key = "id"
				}
				if _, exists := cols[key]; !exists {
					cols[key] = i
				}
			}
			if _, ok := cols["id"]; !ok {
				return nil, nil, &SelectionError{Line: line, Reason: "header has no id column"}
			}
			continue
		}
		get := func(name string) string {
			i, ok := cols[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		id := get("id")
		kind := get("kind")
		if kind == "" {
			kind = kinds.Default()
		}
		pattern, known := kinds.Kind(kind)
		switch {
		case !known:
			rejects = append(rejects, Reject{Line: line, Reason: fmt.Sprintf("unknown kind '%s'", kind)})
		case id == "":
			rejects = append(rejects, Reject{Line: line, Reason: "id is empty"})
		case !fullMatch(pattern, id):
			rejects = append(rejects, Reject{Line: line, Reason: "id must match " + pattern.String()})
		default:
			rows = append(rows, Row{ID: id, Kind: kind, Bucket: get("bucket"), Note: get("note")})
		}
	}
	if cols == nil {
		return nil, nil, &SelectionError{Line: 1, Reason: "file is empty (no header)"}
	}
	return rows, rejects, nil
}
