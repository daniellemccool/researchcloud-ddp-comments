// Package fourcat provisions and queries the per-collector accounts of the
// bundled 4CAT: it creates accounts by running 4CAT's own helper script
// inside the backend container, tags admins by updating the users table
// through psql in the database container, detects whether an admin has
// completed 4CAT's first-run setup, and obtains a collector's API token by
// logging in and asking 4CAT's own token route over the compose network
// (ADR-0009). All
// container access goes through internal/docker; the only HTTP client is the
// one on Client, which never follows redirects.
package fourcat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
// setup page, by requesting GET <FrontendURL>/first-run/ directly (redirects
// are never followed). Only a 2xx whose body (up to 1 MiB) contains
// "Complete 4CAT Setup" means pending; every other answer with a status
// below 500 — a 2xx without the title, any 3xx, any 4xx including the 403
// 4CAT answers once the admin completed the form, and 404 — means the setup
// is done. A 5xx is an error.
func (c *Client) SetupPending(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.FrontendURL, "/")+"/first-run/", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return false, fmt.Errorf("4CAT unreachable: %T", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 500 {
		return false, fmt.Errorf("unexpected setup page status %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, nil
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
// users table through psql. The CASE normalises tags to an array first:
// 4CAT v1.57's create_user.py -a stores tags as the jsonb string
// "[\"admin\"]" (a scalar), and `tags - 'admin'` on a scalar fails. Success
// requires the last non-empty stdout line to be "UPDATE 1"; anything else
// means no such account.
func (c *Client) SetAdmin(ctx context.Context, user string, admin bool) error {
	if err := checkUser(user); err != nil {
		return err
	}
	var sql string
	if admin {
		sql = `UPDATE users SET tags = (CASE WHEN jsonb_typeof(tags) = 'array' THEN tags - 'admin' ELSE '[]'::jsonb END) || '["admin"]'::jsonb WHERE name = '` + user + `'`
	} else {
		sql = `UPDATE users SET tags = (CASE WHEN jsonb_typeof(tags) = 'array' THEN tags - 'admin' ELSE '[]'::jsonb END) WHERE name = '` + user + `'`
	}
	out, err := c.psql(ctx, sql)
	if err != nil {
		return err
	}
	if lastLine(out) != "UPDATE 1" {
		return fmt.Errorf("no 4CAT account for %s", user)
	}
	return nil
}

// lastLine returns the last non-empty line of psql's output, trimmed: the
// command tag ("UPDATE 1", "DELETE 0") of the statement.
func lastLine(out string) string {
	last := ""
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			last = trimmed
		}
	}
	return last
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

// tokenRe is the shape of a 4CAT v1.57 API token: 64 lowercase hex characters.
var tokenRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// RequestToken returns user's 4CAT API token, the credential both proxies
// send as the Authentication header (ADR-0009). It logs in with password,
// asks GET <FrontendURL>/api/request-token/ with the login cookie (which
// lives only for this call; redirects are never followed), and accepts a JSON
// object with a string "token" or a bare JSON string, which must match
// ^[0-9a-f]{64}$. It then sets the token's expiry to 0 ("never") through
// psql and requires "UPDATE 1", so the sign-in and uploads do not stop a year
// after the first visit. A 3xx, 401 or 403 answer is "token request refused
// for <user>"; any other answer that is not a token is "unexpected token
// answer". No error ever holds the body, the cookie or the token.
func (c *Client) RequestToken(ctx context.Context, user, password string) (string, error) {
	if err := checkUser(user); err != nil {
		return "", err
	}
	cookie, err := c.LoginToken(ctx, user, password)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(c.FrontendURL, "/")+"/api/request-token/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cookie", "remember_token="+cookie)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("4CAT unreachable: %T", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400,
		resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("token request refused for %s", user)
	case resp.StatusCode != http.StatusOK:
		return "", errors.New("unexpected token answer")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("4CAT unreachable: %T", err)
	}
	token, ok := parseToken(body)
	if !ok {
		return "", errors.New("unexpected token answer")
	}
	out, err := c.psql(ctx, `UPDATE access_tokens SET expires = 0 WHERE name = '`+user+`'`)
	if err != nil {
		return "", err
	}
	if lastLine(out) != "UPDATE 1" {
		return "", fmt.Errorf("no 4CAT API token row for %s", user)
	}
	return token, nil
}

// DeleteToken deletes user's API token in 4CAT's access_tokens table, so the
// token stops working at once (revocation, ADR-0005). "DELETE 1" and
// "DELETE 0" (no token) are both success; anything else is an error.
func (c *Client) DeleteToken(ctx context.Context, user string) error {
	if err := checkUser(user); err != nil {
		return err
	}
	out, err := c.psql(ctx, `DELETE FROM access_tokens WHERE name = '`+user+`'`)
	if err != nil {
		return err
	}
	switch lastLine(out) {
	case "DELETE 0", "DELETE 1":
		return nil
	}
	return fmt.Errorf("unexpected answer deleting the 4CAT token of %s", user)
}

// parseToken reads a token answer: a JSON object with a string "token", or a
// bare JSON string; the value must match tokenRe.
func parseToken(body []byte) (string, bool) {
	var token string
	if err := json.Unmarshal(body, &token); err != nil {
		var obj struct {
			Token *string `json:"token"`
		}
		if err := json.Unmarshal(body, &obj); err != nil || obj.Token == nil {
			return "", false
		}
		token = *obj.Token
	}
	if !tokenRe.MatchString(token) {
		return "", false
	}
	return token, true
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
