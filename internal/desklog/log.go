// Package desklog is the desk's append-only event log:
// <DeskRoot>/state/desk-log.ndjson, one JSON event per line, stdlib only.
//
// The log is the only record of who claimed which item, which items were
// opened in the browser, and which were reported as a problem. It carries the
// same events the Python prototype wrote, so older logs on existing
// workspaces keep counting: legacy lines carry video_id instead of id and may
// lack kind; they are read, never written.
//
// Locking contract: Append, Claim and Read take the log's own mutex, so
// callers do not need an external lock around them. The file is only ever
// appended to (O_RDWR|O_APPEND|O_CREATE, mode 0600, parent directory 0755)
// and never rewritten, with one exception: a last line that lacks its
// newline (a write cut short by a crash or a full disk) is ignored by Read
// and cut off by the next append before it writes. Read takes the mutex
// because of that cut: an unlocked read that overlapped it could join the
// old fragment to the new event.
package desklog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Event is one line of the desk log. The JSON field names are the Python's,
// exactly. Code is always present (a claim writes it empty); VideoID is
// legacy and read only.
type Event struct {
	T       int64  `json:"t"`
	User    string `json:"user"`
	Action  string `json:"action"` // claim | open | problem | stop (codes idle_no_input, idle_no_screen) | revoke (code co_absent)
	Kind    string `json:"kind,omitempty"`
	ID      string `json:"id,omitempty"`
	Batch   int    `json:"batch,omitempty"`
	Code    string `json:"code"`
	Note    string `json:"note,omitempty"`
	ExecRC  *int   `json:"exec_rc,omitempty"`
	ExecErr string `json:"exec_err,omitempty"`
	IdleS   int    `json:"idle_s,omitempty"`
	VideoID string `json:"video_id,omitempty"` // legacy, read only
}

// Key identifies a pool item across log lines: (kind, id).
type Key struct {
	Kind, ID string
}

// Log is the desk log at Path. DefaultKind is the platform's default kind,
// used for legacy events without a kind. Now supplies the unix-second
// timestamp for appended events; nil means time.Now().Unix().
type Log struct {
	Path        string // <DeskRoot>/state/desk-log.ndjson
	DefaultKind string // the platform's default kind, for legacy events
	Now         func() int64
	// Warn, when not nil, is told once each time an append cuts an
	// unfinished last line off the file. It is called under the log's
	// mutex and must not call back into the log.
	Warn func(format string, args ...any)
	mu   sync.Mutex
}

// Append adds the events to the log under the log's mutex, one write call for
// all lines (one json.Marshal plus "\n" per event).
func (l *Log) Append(evs ...Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.append(evs)
}

// append writes the events assuming the caller holds l.mu. An unfinished
// last line is cut off first; when that fails nothing is written.
func (l *Log) append(evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := l.cutUnfinishedTail(f); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// cutUnfinishedTail truncates f to just after its last newline when its last
// byte is not one (to nothing when it holds no newline), syncs, and tells
// Warn how many bytes went. An empty file and a file that ends in a newline
// are left alone. The caller holds l.mu.
func (l *Log) cutUnfinishedTail(f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if size == 0 {
		return nil
	}
	buf := make([]byte, 4096)
	if _, err := io.ReadFull(io.NewSectionReader(f, size-1, 1), buf[:1]); err != nil {
		return err
	}
	if buf[0] == '\n' {
		return nil
	}
	keep := int64(0) // the file's length after the cut
	for end := size; end > 0; {
		start := max(end-int64(len(buf)), 0)
		chunk := buf[:end-start]
		if _, err := io.ReadFull(io.NewSectionReader(f, start, end-start), chunk); err != nil {
			return err
		}
		if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
			keep = start + int64(i) + 1
			break
		}
		end = start
	}
	if err := f.Truncate(keep); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if l.Warn != nil {
		l.Warn("desk-log.ndjson: cut an unfinished last line (%d bytes)", size-keep)
	}
	return nil
}

// Read returns the log's events in file order, under the log's mutex. A
// missing file is not an error: no events, no error. Blank lines are
// skipped. An unfinished last line (one without its newline) is ignored,
// whether or not it parses. Any other line that is not valid JSON is an
// error naming the line number: `desk-log.ndjson: line <n>: <err>`.
func (l *Log) Read() ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.read()
}

// read is Read for a caller that holds l.mu.
func (l *Log) read() ([]Event, error) {
	data, err := os.ReadFile(l.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if n := len(data); n > 0 && data[n-1] != '\n' {
		data = data[:bytes.LastIndexByte(data, '\n')+1]
	}
	var evs []Event
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", filepath.Base(l.Path), i+1, err)
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

// KeyOf is the (kind, id) of an event: the event's kind or the default kind
// when empty, and its id or the legacy video_id when empty.
func (l *Log) KeyOf(e Event) Key {
	kind := e.Kind
	if kind == "" {
		kind = l.DefaultKind
	}
	id := e.ID
	if id == "" {
		id = e.VideoID
	}
	return Key{Kind: kind, ID: id}
}

// now is the timestamp for appended events.
func (l *Log) now() int64 {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now().Unix()
}
