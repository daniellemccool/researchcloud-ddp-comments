// Package registry is the seat registry: <DeskRoot>/registry.tsv maps each
// user to a seat index, one row per user, as `user<TAB>index<TAB>status`
// with status in {live, retired}. Legacy two-column rows (`user<TAB>index`)
// are live. Each user appears in one row.
//
// Locking contract: Allocate and Retire do an unlocked read-modify-write, so
// the CALLER must hold the desk's global in-process lock around them. The
// readers (Rows, Live) are lock-free; every write replaces the whole file
// atomically (temp file in the same directory, chmod 0644, rename), so a
// reader sees either the old or the new content, never a partial file.
package registry

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ErrFull is returned (wrapped) by Allocate when every seat index in
// 1..MaxSessions is held by a live row.
var ErrFull = errors.New("registry full")

// userRe is the username grammar: ^[a-z][a-z0-9._-]{0,31}$
var userRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

// Row is one registry line. Status is "live" or "retired".
type Row struct {
	User   string
	Index  int
	Status string
}

// Registry reads and writes the seat registry at Path (normally
// <DeskRoot>/registry.tsv). MaxSessions caps the seat indexes Allocate hands
// out (1..MaxSessions).
type Registry struct {
	Path        string
	MaxSessions int
}

// Rows returns every row of the registry file. A missing file is not an
// error: nil, nil. Rows with fewer than two fields or an empty user are
// skipped; a two-column row is live; a non-integer index is an error.
func (r *Registry) Rows() ([]Row, error) {
	data, err := os.ReadFile(r.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []Row
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("registry row %q: invalid index %q: %w", parts[0], parts[1], err)
		}
		status := "live"
		if len(parts) > 2 && strings.TrimSpace(parts[2]) != "" {
			status = strings.TrimSpace(parts[2])
		}
		rows = append(rows, Row{User: strings.TrimSpace(parts[0]), Index: index, Status: status})
	}
	return rows, nil
}

// Live returns the live rows keyed by user.
func (r *Registry) Live() (map[string]Row, error) {
	rows, err := r.Rows()
	if err != nil {
		return nil, err
	}
	live := make(map[string]Row, len(rows))
	for _, row := range rows {
		if row.Status == "live" {
			live[row.User] = row
		}
	}
	return live, nil
}

// Allocate returns the live row for user, creating or reactivating it. The
// CALLER must hold the global lock. A live row is returned unchanged. A
// returning retired user gets their old index back when it is still free and
// within 1..MaxSessions, else the lowest free index; none free means ErrFull
// (wrapped). The user's existing row is rewritten in place, a new user is
// appended, and the whole file is rewritten (never appended to), so a legacy
// file lacking a final newline cannot have the new row glued onto its last
// line.
func (r *Registry) Allocate(user string) (Row, error) {
	if !userRe.MatchString(user) {
		return Row{}, fmt.Errorf("invalid username: %q", user)
	}
	rows, err := r.Rows()
	if err != nil {
		return Row{}, err
	}
	for _, row := range rows {
		if row.User == user && row.Status == "live" {
			return row, nil
		}
	}
	taken := make(map[int]bool, len(rows))
	for _, row := range rows {
		if row.Status == "live" {
			taken[row.Index] = true
		}
	}
	var old *Row
	for i := range rows {
		if rows[i].User == user {
			old = &rows[i]
			break
		}
	}
	index := 0
	if old != nil && !taken[old.Index] && old.Index >= 1 && old.Index <= r.MaxSessions {
		index = old.Index
	} else {
		for i := 1; i <= r.MaxSessions; i++ {
			if !taken[i] {
				index = i
				break
			}
		}
		if index == 0 {
			return Row{}, fmt.Errorf("all %d sessions are in use: %w", r.MaxSessions, ErrFull)
		}
	}
	fresh := Row{User: user, Index: index, Status: "live"}
	if old == nil {
		rows = append(rows, fresh)
	} else {
		*old = fresh
	}
	if err := r.rewrite(rows); err != nil {
		return Row{}, err
	}
	return fresh, nil
}

// Retire marks user's row retired. The CALLER must hold the global lock. With
// no live row for user it is a no-op and writes nothing.
func (r *Registry) Retire(user string) error {
	rows, err := r.Rows()
	if err != nil {
		return err
	}
	live := false
	for i := range rows {
		if rows[i].User == user {
			if rows[i].Status == "live" {
				live = true
			}
			rows[i].Status = "retired"
		}
	}
	if !live {
		return nil
	}
	return r.rewrite(rows)
}

// rewrite replaces the whole registry file atomically: temp file in the same
// directory, chmod 0644, rename over Path. On any error the temp file is
// removed and Path keeps its old content.
func (r *Registry) rewrite(rows []Row) (err error) {
	dir := filepath.Dir(r.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".registry.tsv.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	w := bufio.NewWriter(tmp)
	for _, row := range rows {
		if _, err = fmt.Fprintf(w, "%s\t%d\t%s\n", row.User, row.Index, row.Status); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err = w.Flush(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, r.Path)
}
