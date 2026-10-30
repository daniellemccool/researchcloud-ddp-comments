package template

import (
	"database/sql"
	"os"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// filterCookies deletes every moz_cookies row whose (host, name) is not in
// keep, then VACUUMs the file. A missing cookies.sqlite is ErrNoCookiesDB: the
// consent cookie is part of what the template is for.
func filterCookies(path string, keep []platform.CookieSpec) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return ErrNoCookiesDB
		}
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	query := "DELETE FROM moz_cookies"
	var args []any
	if len(keep) > 0 {
		var cond strings.Builder
		cond.WriteString("DELETE FROM moz_cookies WHERE NOT (")
		for i, c := range keep {
			if i > 0 {
				cond.WriteString(" OR ")
			}
			cond.WriteString("(host = ? AND name = ?)")
			args = append(args, c.Host, c.Name)
		}
		cond.WriteString(")")
		query = cond.String()
	}
	if _, err := db.Exec(query, args...); err != nil {
		_ = db.Close()
		return err
	}
	_, _ = db.Exec("PRAGMA journal_mode=DELETE")
	if _, err := db.Exec("VACUUM"); err != nil {
		_ = db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Remove(path + side)
	}
	return nil
}
