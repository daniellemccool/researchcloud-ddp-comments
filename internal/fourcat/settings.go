package fourcat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Setting is one row of 4CAT's settings table: Name, Tag ("" is the global
// value) and Value, the JSON text 4CAT reads.
type Setting struct {
	Name, Tag, Value string
}

// The shapes a setting must have before it reaches SQL or a cache key: no
// quote, no space in a name or tag, and a value of plain JSON words.
var (
	settingNameRe  = regexp.MustCompile(`^[a-z0-9_.-]+$`)
	settingTagRe   = regexp.MustCompile(`^[a-z0-9_]*$`)
	settingValueRe = regexp.MustCompile(`^[A-Za-z0-9\[\]",_. -]*$`)
)

func (s Setting) check() error {
	if !settingNameRe.MatchString(s.Name) {
		return fmt.Errorf("setting name %q not allowed", s.Name)
	}
	if !settingTagRe.MatchString(s.Tag) {
		return fmt.Errorf("setting %s: tag %q not allowed", s.Name, s.Tag)
	}
	if !settingValueRe.MatchString(s.Value) || !json.Valid([]byte(s.Value)) {
		return fmt.Errorf("setting %s: value is not allowed JSON", s.Name)
	}
	return nil
}

// cacheKey is the memcached key 4CAT keeps the setting under: its client
// prefixes "4cat-config" to "<name>" or "<name>-<tag>" (config_manager.py).
func (s Setting) cacheKey() string {
	if s.Tag == "" {
		return "4cat-config" + s.Name
	}
	return "4cat-config" + s.Name + "-" + s.Tag
}

// EnsureSettings makes each setting's row hold its value: a row that is
// missing or differs (compared as JSON, since 4CAT writes its own spelling)
// is upserted, a row already right is not written. Then the cache key of
// every listed setting is deleted, written or not: a row written in an
// earlier attempt whose cache delete failed must still reach 4CAT, which
// otherwise serves the cached value forever. Every setting is checked before
// anything runs. The secret in flask.proxy_secret never appears in an error.
func (c *Client) EnsureSettings(ctx context.Context, settings []Setting) error {
	for _, s := range settings {
		if err := s.check(); err != nil {
			return err
		}
	}
	for _, s := range settings {
		cur, err := c.psql(ctx, `SELECT value FROM settings WHERE name = '`+s.Name+`' AND tag = '`+s.Tag+`'`)
		if err != nil {
			return fmt.Errorf("read setting %s: %w", s.Name, err)
		}
		if cur != "" && sameJSON(cur, s.Value) {
			continue
		}
		out, err := c.psql(ctx, `INSERT INTO settings (name, value, tag) VALUES ('`+s.Name+`', '`+s.Value+`', '`+s.Tag+
			`') ON CONFLICT (name, tag) DO UPDATE SET value = EXCLUDED.value`)
		if err != nil {
			return fmt.Errorf("write setting %s: %w", s.Name, err)
		}
		if lastLine(out) != "INSERT 0 1" {
			return fmt.Errorf("write setting %s: unexpected answer", s.Name)
		}
	}
	for _, s := range settings {
		if err := c.deleteCacheKey(ctx, s.cacheKey()); err != nil {
			return err
		}
	}
	return nil
}

// sameJSON reports whether two JSON texts hold the same value.
func sameJSON(a, b string) bool {
	var va, vb any
	if json.Unmarshal([]byte(a), &va) != nil || json.Unmarshal([]byte(b), &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// ClearAdmin removes the "admin" tag from user's account and deletes the
// account's cached tags, so 4CAT reads the row again. The CASE normalises
// tags to an array first: 4CAT v1.57's create_user.py -a stored them as a
// JSON string. "UPDATE 0" (no such account: a password whose account was
// never created) is success.
func (c *Client) ClearAdmin(ctx context.Context, user string) error {
	if err := checkUser(user); err != nil {
		return err
	}
	out, err := c.psql(ctx, `UPDATE users SET tags = (CASE WHEN jsonb_typeof(tags) = 'array' THEN tags - 'admin' ELSE '[]'::jsonb END) WHERE name = '`+user+`'`)
	if err != nil {
		return err
	}
	switch lastLine(out) {
	case "UPDATE 0", "UPDATE 1":
	default:
		return fmt.Errorf("clear admin tag of %s: unexpected answer", user)
	}
	return c.deleteCacheKey(ctx, "4cat-config_usertags-"+user)
}

// deleteCacheKey deletes key in 4CAT's memcached by running nc in the
// memcached container with the delete command on stdin. "DELETED" and
// "NOT_FOUND" are success; anything else, or nc failing, is an error. The
// key is built only from checked names.
func (c *Client) deleteCacheKey(ctx context.Context, key string) error {
	mc, err := c.memcached(ctx)
	if err != nil {
		return err
	}
	res, err := c.Docker.Exec(ctx, mc, []string{"nc", "127.0.0.1", "11211"}, nil, []byte("delete "+key+"\r\nquit\r\n"))
	if err != nil {
		return fmt.Errorf("docker exec failed to run: %T", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("nc exited with status %d", res.ExitCode)
	}
	switch strings.TrimSpace(string(bytes.SplitN([]byte(res.Stdout), []byte("\n"), 2)[0])) {
	case "DELETED", "NOT_FOUND":
		return nil
	}
	return fmt.Errorf("memcached refused to delete %s", key)
}
