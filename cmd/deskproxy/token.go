package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// tokenFile is the collector's 4CAT token (ADR-0009): a file mounted into this
// proxy only. current reads it on every call, so rotation, removal and a
// permission change take effect at once with no cache to go stale. The value
// is never logged and never put in an error.
type tokenFile struct {
	path string
}

func newTokenFile(path string) *tokenFile { return &tokenFile{path: path} }

// current returns the token. It fails when the file is missing, unreadable or
// empty, so callers answer 403 rather than forward unauthenticated.
func (t *tokenFile) current() (string, error) {
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", errors.Unwrap(err))
	}
	v := strings.TrimSpace(string(raw))
	if v == "" {
		return "", errors.New("token file: empty")
	}
	return v, nil
}
