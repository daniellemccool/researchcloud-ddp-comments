// Package fourcat provisions and queries the per-collector accounts of the
// bundled 4CAT: it creates accounts by running 4CAT's own helper script
// inside the backend container, tags admins by updating the users table
// through psql in the database container, detects whether an admin has
// completed 4CAT's first-run setup, and obtains a login token by posting the
// login form to the frontend over the compose network (ADR-0009). All
// container access goes through internal/docker; the only HTTP client is the
// one on Client, which never follows redirects.
package fourcat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

// ErrNeedsAdopt is returned by provisioning (a later package) when a 4CAT
// account already exists for a collector who has none on the desk; this
// package defines it but does not use it.
var ErrNeedsAdopt = errors.New("4CAT account exists; ask an admin to adopt it")

// ErrSetupPending means no admin has completed 4CAT's first-run setup yet.
var ErrSetupPending = errors.New("4CAT setup not completed; an admin opens /fourcat/")

// userRe is the username grammar: ^[a-z][a-z0-9._-]{0,31}$ (the same shape
// internal/access enforces on the identity header; it does not export its
// regexp, so it is compiled here). A username is validated against it before
// it is interpolated into any SQL or argv.
var userRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

// LoginError means the 4CAT frontend refused the login (401, or a 200/302
// that set no remember_token cookie).
type LoginError struct {
	User string
}

func (e *LoginError) Error() string {
	return "login refused for " + e.User
}

// Client talks to the bundled 4CAT. Backend and DB are container names or
// ids; when empty they are looked up lazily via FindContainers on first use.
// FrontendURL is "http://4cat-frontend:5000" in production. A nil HTTP
// client means one with a 15 s timeout that never follows redirects.
type Client struct {
	Docker      docker.Client
	Backend, DB string
	FrontendURL string
	HTTP        *http.Client

	mu          sync.Mutex   // guards Backend, DB and defaultHTTP
	defaultHTTP *http.Client // the nil-HTTP client, built once
}

// httpClient returns the client to talk to the 4CAT frontend with: a shallow
// copy of c.HTTP when the caller supplied one, with redirects always refused
// and a 15 s timeout when it has none; otherwise the default client, built
// once under c.mu. A redirect is never followed: the login form carries the
// password.
func (c *Client) httpClient() *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HTTP != nil {
		cp := *c.HTTP
		cp.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
		if cp.Timeout == 0 {
			cp.Timeout = 15 * time.Second
		}
		return &cp
	}
	if c.defaultHTTP == nil {
		c.defaultHTTP = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return c.defaultHTTP
}

// checkUser validates user against the username grammar. It runs before any
// SQL or argv interpolation, so an invalid name performs no exec.
func checkUser(user string) error {
	if !userRe.MatchString(user) {
		return fmt.Errorf("invalid username: %q", user)
	}
	return nil
}

// exec runs cmd in container and returns its stdout. A non-zero exit code is
// an error naming only the tool and the status; a transport error names only
// the Go type of the error. Neither ever echoes the command line or output:
// they may hold secrets.
func (c *Client) exec(ctx context.Context, container, what string, cmd ...string) (string, error) {
	res, err := c.Docker.Exec(ctx, container, cmd, nil, nil)
	if err != nil {
		return "", fmt.Errorf("docker exec failed to run: %T", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%s exited with status %d", what, res.ExitCode)
	}
	return res.Stdout, nil
}

// psql runs one SQL statement through psql in the DB container with
// ON_ERROR_STOP=1 (a failed statement exits non-zero instead of reading as
// empty output) and returns the trimmed stdout.
func (c *Client) psql(ctx context.Context, sql string) (string, error) {
	db, err := c.db(ctx)
	if err != nil {
		return "", err
	}
	out, err := c.exec(ctx, db, "psql", "psql", "-v", "ON_ERROR_STOP=1", "-U", "fourcat", "-d", "fourcat", "-tAc", sql)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SetupPending reports whether the 4CAT frontend still shows its first-run
// setup page (a body containing "Complete 4CAT Setup").
func (c *Client) SetupPending(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.FrontendURL, "/")+"/", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return false, fmt.Errorf("4CAT unreachable: %T", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("unexpected setup page status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	return strings.Contains(string(body), "Complete 4CAT Setup"), nil
}

// AccountExists reports whether a 4CAT account named user exists.
func (c *Client) AccountExists(ctx context.Context, user string) (bool, error) {
	if err := checkUser(user); err != nil {
		return false, err
	}
	out, err := c.psql(ctx, "SELECT 1 FROM users WHERE name = '"+user+"'")
	if err != nil {
		return false, err
	}
	return out == "1", nil
}

// CreateAccount creates the 4CAT account user with password by running
// 4CAT's own create_user.py helper in the backend container (-a when admin).
func (c *Client) CreateAccount(ctx context.Context, user, password string, admin bool) error {
	if err := checkUser(user); err != nil {
		return err
	}
	backend, err := c.backend(ctx)
	if err != nil {
		return err
	}
	cmd := []string{"python3", "helper-scripts/create_user.py", "-u", user, "-e", "-p", password}
	if admin {
		cmd = append(cmd, "-a")
	}
	_, err = c.exec(ctx, backend, "create_user.py", cmd...)
	return err
}

// SetAdmin adds or removes the "admin" tag of user's account by updating the
// users table through psql. Success requires the last non-empty stdout line
// to be "UPDATE 1"; anything else means no such account.
func (c *Client) SetAdmin(ctx context.Context, user string, admin bool) error {
	if err := checkUser(user); err != nil {
		return err
	}
	var sql string
	if admin {
		sql = `UPDATE users SET tags = (tags - 'admin') || '["admin"]'::jsonb WHERE name = '` + user + `'`
	} else {
		sql = `UPDATE users SET tags = tags - 'admin' WHERE name = '` + user + `'`
	}
	out, err := c.psql(ctx, sql)
	if err != nil {
		return err
	}
	last := ""
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			last = trimmed
		}
	}
	if last != "UPDATE 1" {
		return fmt.Errorf("no 4CAT account for %s", user)
	}
	return nil
}

// LoginToken posts the login form to the 4CAT frontend and returns the first
// non-empty remember_token cookie of the response. The client never follows
// redirects, so the 302 of a successful login arrives here un-followed.
func (c *Client) LoginToken(ctx context.Context, user, password string) (string, error) {
	if err := checkUser(user); err != nil {
		return "", err
	}
	form := url.Values{"username": {user}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(c.FrontendURL, "/")+"/login/", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("4CAT unreachable: %T", err)
	}
	defer func() { _ = resp.Body.Close() }()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "remember_token" && cookie.Value != "" {
			return cookie.Value, nil
		}
	}
	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusFound {
		return "", &LoginError{User: user}
	}
	return "", fmt.Errorf("unexpected login response status %d", resp.StatusCode)
}

// NewPassword returns a fresh random password: 24 bytes from crypto/rand,
// base64.RawURLEncoding (32 URL-safe characters). The first character is
// never '-' or '_', so the password is always a safe argv element for
// create_user.py's -p flag (a leading '-' would make argparse fail).
func NewPassword() string {
	for {
		var b [24]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("crypto/rand failed: " + err.Error())
		}
		s := base64.RawURLEncoding.EncodeToString(b[:])
		if s[0] != '-' && s[0] != '_' {
			return s
		}
	}
}
