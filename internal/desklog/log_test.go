package desklog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixedNow = int64(1700000000)

func newTestLog(t *testing.T) *Log {
	t.Helper()
	return &Log{
		Path:        filepath.Join(t.TempDir(), "state", "desk-log.ndjson"),
		DefaultKind: "video",
		Now:         func() int64 { return fixedNow },
	}
}

func writeLog(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestAppendMode0600AndShape(t *testing.T) {
	l := newTestLog(t)
	err := l.Append(Event{T: fixedNow, User: "alice", Action: "claim", Kind: "video", ID: "101", Batch: 1, Code: ""})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	info, err := os.Stat(l.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 0600", got)
	}
	data, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("file does not end with a newline: %q", data)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), data)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &obj); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	wantKeys := []string{"t", "user", "action", "kind", "id", "batch", "code"}
	if len(obj) != len(wantKeys) {
		t.Fatalf("line has keys %v, want exactly %v", keysOf(obj), wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := obj[k]; !ok {
			t.Fatalf("line is missing key %q: %v", k, obj)
		}
	}
	if obj["code"] != "" {
		t.Fatalf("code = %v, want empty string", obj["code"])
	}
	if obj["t"] != float64(fixedNow) || obj["user"] != "alice" || obj["action"] != "claim" ||
		obj["kind"] != "video" || obj["id"] != "101" || obj["batch"] != float64(1) {
		t.Fatalf("unexpected line: %v", obj)
	}
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestReadSkipsBlankLinesAndMissingFile(t *testing.T) {
	l := newTestLog(t)
	evs, err := l.Read()
	if err != nil {
		t.Fatalf("Read on missing file: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("Read on missing file = %+v, want no events", evs)
	}
	writeLog(t, l.Path, "\n{\"t\":1,\"user\":\"alice\",\"action\":\"claim\",\"kind\":\"video\",\"id\":\"1\",\"batch\":1,\"code\":\"\"}\n\n   \n{\"t\":2,\"user\":\"bob\",\"action\":\"stop\",\"code\":\"\"}\n")
	evs, err = l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("Read = %+v, want 2 events", evs)
	}
	if evs[0].User != "alice" || evs[0].Action != "claim" || evs[0].ID != "1" || evs[0].Batch != 1 {
		t.Fatalf("evs[0] = %+v", evs[0])
	}
	if evs[1].User != "bob" || evs[1].Action != "stop" {
		t.Fatalf("evs[1] = %+v", evs[1])
	}
}

func TestReadBadLineNamesLineNumber(t *testing.T) {
	l := newTestLog(t)
	writeLog(t, l.Path, "{\"t\":1,\"user\":\"a\",\"action\":\"stop\",\"code\":\"\"}\n\nnot json at all\n")
	_, err := l.Read()
	if err == nil {
		t.Fatal("Read on a bad line returned nil error")
	}
	if !strings.Contains(err.Error(), "desk-log.ndjson: line 3:") {
		t.Fatalf("error = %q, want it to name desk-log.ndjson: line 3:", err)
	}
}

func TestLegacyVideoIDEvents(t *testing.T) {
	l := newTestLog(t)
	writeLog(t, l.Path,
		`{"t":1,"user":"alice","action":"claim","video_id":"7","batch":1,"code":""}`+"\n"+
			`{"t":2,"user":"alice","action":"open","video_id":"7","exec_rc":0,"code":""}`+"\n")
	evs, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("Read = %+v, want 2 events", evs)
	}
	v := l.ViewOf("alice", evs)
	key := Key{Kind: "video", ID: "7"}
	if len(v.Claims) != 1 || v.Claims[0].Key != key || v.Claims[0].Batch != 1 {
		t.Fatalf("claims = %+v, want [{%v %v} batch 1]", v.Claims, key.Kind, key.ID)
	}
	if !v.Done[key] {
		t.Fatalf("key %v not done after open with exec_rc 0", key)
	}
	if !v.Opened[key] {
		t.Fatalf("key %v not opened after open with exec_rc 0", key)
	}
	if l.KeyOf(evs[0]) != key {
		t.Fatalf("KeyOf(legacy claim) = %v, want %v", l.KeyOf(evs[0]), key)
	}
}
