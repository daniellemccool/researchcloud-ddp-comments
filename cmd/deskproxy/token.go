package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// tokenFile is the collector's 4CAT token (ADR-0009): a file mounted into this
// proxy only. current re-reads it whenever the file's mtime or size changes, so
// rotation needs no restart. The value is never logged and never put in an
// error.
type tokenFile struct {
	path string

	mu    sync.Mutex
	mtime time.Time
	size  int64
	value string
}

func newTokenFile(path string) *tokenFile { return &tokenFile{path: path} }

// current returns the token. It fails, and keeps nothing cached, when the file
// is missing, unreadable or empty, so callers answer 403 rather than forward
// unauthenticated.
func (t *tokenFile) current() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	fi, err := os.Stat(t.path)
	if err != nil {
		t.reset()
		return "", fmt.Errorf("token file: %w", errors.Unwrap(err))
	}
	if t.value != "" && fi.ModTime().Equal(t.mtime) && fi.Size() == t.size {
		return t.value, nil
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		t.reset()
		return "", fmt.Errorf("token file: %w", errors.Unwrap(err))
	}
	v := strings.TrimSpace(string(raw))
	if v == "" {
		t.reset()
		return "", errors.New("token file: empty")
	}
	t.mtime, t.size, t.value = fi.ModTime(), fi.Size(), v
	return v, nil
}

func (t *tokenFile) reset() {
	t.mtime, t.size, t.value = time.Time{}, 0, ""
}
