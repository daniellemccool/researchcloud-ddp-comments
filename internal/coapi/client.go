// Package coapi polls the SURF Research Cloud CO member API and keeps the
// last good member list (ADR-0005: only a successful, non-empty poll counts).
package coapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// ErrEmpty means the API answered 200 with a list that holds no usable member.
var ErrEmpty = errors.New("coapi: member list is empty")

// userRe is the username grammar: ^[a-z][a-z0-9._-]{0,31}$
var userRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,31}$`)

// maxBody caps how much of a response is read.
const maxBody = 16 << 20

// Member is one CO member as the desk uses it.
type Member struct {
	Username string
	Roles    []string
}

// Client reads the CO member list. HTTP nil means a client with a 10 s timeout
// and no proxy.
type Client struct {
	Endpoint, Token string
	HTTP            *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
}

// Fetch returns the members, skipping entries with an invalid username or
// roles that are not a list. Any failure, and an empty list (ErrEmpty), is an
// error.
func (c *Client) Fetch(ctx context.Context) ([]Member, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("coapi: build request: %w", err)
	}
	req.Header.Set("Authorization", c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("coapi: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("coapi: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("coapi: read body: %w", err)
	}
	var raw []struct {
		Username string          `json:"username"`
		Roles    json.RawMessage `json:"roles"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("coapi: body is not a JSON array: %w", err)
	}
	var out []Member
	for _, e := range raw {
		if !userRe.MatchString(e.Username) {
			continue
		}
		if !bytes.HasPrefix(bytes.TrimSpace(e.Roles), []byte("[")) {
			continue
		}
		var roles []string
		if err := json.Unmarshal(e.Roles, &roles); err != nil {
			continue
		}
		out = append(out, Member{Username: e.Username, Roles: roles})
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}
