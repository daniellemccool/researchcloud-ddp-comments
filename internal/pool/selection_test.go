package pool

import (
	"errors"
	"strings"
	"testing"
)

func TestParseBOMAndCRLF(t *testing.T) {
	rows, rejects, err := ParseSelection("\uFEFFVideo ID;Bucket;Note\r\n11;a;hi\r\n12;b;\r\n", fakeKinds{})
	if err != nil {
		t.Fatalf("ParseSelection: %v", err)
	}
	want := []Row{{ID: "11", Kind: "video", Bucket: "a", Note: "hi"}, {ID: "12", Kind: "video", Bucket: "b", Note: ""}}
	if len(rows) != len(want) || len(rejects) != 0 {
		t.Fatalf("rows = %+v, rejects = %+v; want %+v and no rejects", rows, rejects, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("rows[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestParseSemicolon(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []Row
	}{
		{"semicolons outnumber commas", "id;bucket;note\n11;a;hi\n", []Row{{ID: "11", Kind: "video", Bucket: "a", Note: "hi"}}},
		{"commas win", "id,bucket,note\n11,a,hi\n", []Row{{ID: "11", Kind: "video", Bucket: "a", Note: "hi"}}},
		{"tie goes to comma", "id,x;y\n11,a;b\n", []Row{{ID: "11", Kind: "video"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, rejects, err := ParseSelection(tt.text, fakeKinds{})
			if err != nil {
				t.Fatalf("ParseSelection: %v", err)
			}
			if len(rejects) != 0 {
				t.Fatalf("rejects = %+v, want none", rejects)
			}
			if len(rows) != len(tt.want) {
				t.Fatalf("rows = %+v, want %+v", rows, tt.want)
			}
			for i := range tt.want {
				if rows[i] != tt.want[i] {
					t.Fatalf("rows[%d] = %+v, want %+v", i, rows[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseHeaderAliases(t *testing.T) {
	for _, header := range []string{"Video-ID", "videoid", "ID"} {
		rows, rejects, err := ParseSelection(header+"\n11\n", fakeKinds{})
		if err != nil {
			t.Fatalf("ParseSelection(%q): %v", header, err)
		}
		if len(rejects) != 0 || len(rows) != 1 || rows[0] != (Row{ID: "11", Kind: "video"}) {
			t.Fatalf("header %q: rows = %+v, rejects = %+v; want one row (11, video)", header, rows, rejects)
		}
	}
}

func TestParseRejects(t *testing.T) {
	rows, rejects, err := ParseSelection("video_id,bucket,note\n1,x,\njunk,y,\n3,y,\n,z,\n", fakeKinds{})
	if err != nil {
		t.Fatalf("ParseSelection: %v", err)
	}
	wantRows := []Row{{ID: "1", Kind: "video", Bucket: "x"}, {ID: "3", Kind: "video", Bucket: "y"}}
	if len(rows) != len(wantRows) {
		t.Fatalf("rows = %+v, want %+v", rows, wantRows)
	}
	for i := range wantRows {
		if rows[i] != wantRows[i] {
			t.Fatalf("rows[%d] = %+v, want %+v", i, rows[i], wantRows[i])
		}
	}
	wantRejects := []Reject{{Line: 3, Reason: "id must match ^[0-9]{1,20}$"}, {Line: 5, Reason: "id is empty"}}
	if len(rejects) != len(wantRejects) {
		t.Fatalf("rejects = %+v, want %+v", rejects, wantRejects)
	}
	for i := range wantRejects {
		if rejects[i] != wantRejects[i] {
			t.Fatalf("rejects[%d] = %+v, want %+v", i, rejects[i], wantRejects[i])
		}
	}

	rows, rejects, err = ParseSelection("id,kind\n1,video\n2,playlist\n", fakeKinds{})
	if err != nil {
		t.Fatalf("ParseSelection: %v", err)
	}
	if len(rows) != 1 || rows[0] != (Row{ID: "1", Kind: "video"}) {
		t.Fatalf("rows = %+v, want one row (1, video)", rows)
	}
	if len(rejects) != 1 || rejects[0] != (Reject{Line: 3, Reason: "unknown kind 'playlist'"}) {
		t.Fatalf("rejects = %+v, want [{3 unknown kind 'playlist'}]", rejects)
	}
}

func TestParseFileLevelErrors(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantErr  string
		wantLine int
	}{
		{"header without id column", "foo,bar\n1,2\n", "line 1: header has no id column", 1},
		{"unbalanced quote", "id,note\n1,ok\n2,\"unfinished\n3,valid\n", "line 3: a quoted field", 3},
		{"bare quote", "id,note\n1,ok\n2,ab\"c\n", "line 3: unreadable:", 3},
		{"blank file", "\n \n", "line 1: file is empty (no header)", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, rejects, err := ParseSelection(tt.text, fakeKinds{})
			if err == nil {
				t.Fatalf("ParseSelection = nil error, want %q", tt.wantErr)
			}
			var selErr *SelectionError
			if !errors.As(err, &selErr) {
				t.Fatalf("err = %T (%v), want *SelectionError", err, err)
			}
			if !strings.HasPrefix(err.Error(), tt.wantErr) {
				t.Fatalf("err = %q, want prefix %q", err.Error(), tt.wantErr)
			}
			if selErr.Line != tt.wantLine {
				t.Fatalf("SelectionError.Line = %d, want %d", selErr.Line, tt.wantLine)
			}
			if len(rows) != 0 {
				t.Fatalf("rows = %+v, want none on a file-level error", rows)
			}
			if len(rejects) != 0 {
				t.Fatalf("rejects = %+v, want none on a file-level error", rejects)
			}
		})
	}
}

func TestParseBlankLinesSkipped(t *testing.T) {
	rows, rejects, err := ParseSelection("id,note\n\n1,x\n   \n2,y\n", fakeKinds{})
	if err != nil {
		t.Fatalf("ParseSelection: %v", err)
	}
	want := []Row{{ID: "1", Kind: "video", Note: "x"}, {ID: "2", Kind: "video", Note: "y"}}
	if len(rejects) != 0 || len(rows) != len(want) {
		t.Fatalf("rows = %+v, rejects = %+v; want %+v and no rejects", rows, rejects, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("rows[%d] = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestParseMissingKindCellUsesDefault(t *testing.T) {
	rows, rejects, err := ParseSelection("id,bucket\n1,x\n", fakeKinds{})
	if err != nil {
		t.Fatalf("ParseSelection: %v", err)
	}
	if len(rejects) != 0 || len(rows) != 1 || rows[0] != (Row{ID: "1", Kind: "video", Bucket: "x"}) {
		t.Fatalf("rows = %+v, rejects = %+v; want one row (1, video, x)", rows, rejects)
	}
	rows, rejects, err = ParseSelection("id,kind\n1,\n", fakeKinds{})
	if err != nil {
		t.Fatalf("ParseSelection: %v", err)
	}
	if len(rejects) != 0 || len(rows) != 1 || rows[0] != (Row{ID: "1", Kind: "video"}) {
		t.Fatalf("rows = %+v, rejects = %+v; want one row (1, video)", rows, rejects)
	}
}
