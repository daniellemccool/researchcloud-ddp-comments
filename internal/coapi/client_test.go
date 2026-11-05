package coapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
)

func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{Endpoint: srv.URL, Token: "tok"}
}

func TestFetchSendsRawToken(t *testing.T) {
	var auth, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, accept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		_, _ = w.Write([]byte(`[{"username":"alice","roles":["src_co_admin"]}]`))
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Token: "raw-token-123"}
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "raw-token-123" {
		t.Fatalf("Authorization = %q, want the raw token", auth)
	}
	if accept != "application/json" {
		t.Fatalf("Accept = %q", accept)
	}
}

func TestFetchFailures(t *testing.T) {
	refused := httptest.NewServer(http.NotFoundHandler())
	refusedURL := refused.URL
	refused.Close()

	cases := []struct {
		name   string
		c      *Client
		empty  bool
		wantOK bool
	}{
		{name: "connection refused", c: &Client{Endpoint: refusedURL, Token: "t"}},
		{name: "500", c: serve(t, 500, `[]`)},
		{name: "401", c: serve(t, 401, `{"detail":"no"}`)},
		{name: "429", c: serve(t, 429, `[]`)},
		{name: "object body", c: serve(t, 200, `{}`)},
		{name: "not json", c: serve(t, 200, `not json`)},
		{name: "empty list", c: serve(t, 200, `[]`), empty: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.c.Fetch(context.Background())
			if err == nil {
				t.Fatalf("want error, got %v", got)
			}
			if errors.Is(err, ErrEmpty) != tc.empty {
				t.Fatalf("ErrEmpty = %v, want %v (err %v)", errors.Is(err, ErrEmpty), tc.empty, err)
			}
		})
	}
}

func TestFetchSkipsBadEntries(t *testing.T) {
	c := serve(t, 200, `[
		{"username":"alice","roles":["src_co_admin"]},
		{"username":"Bad User","roles":[]},
		{"username":"bob","roles":"src_co_member"},
		{"roles":[]},
		{"username":"carol","roles":["x"]}]`)
	got, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Member{{"alice", []string{"src_co_admin"}}, {"carol", []string{"x"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFetchAllBadIsEmpty(t *testing.T) {
	c := serve(t, 200, `[{"username":"Bad User","roles":[]}]`)
	if _, err := c.Fetch(context.Background()); !errors.Is(err, ErrEmpty) {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
}

func TestFetchFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/members.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := serve(t, 200, string(body)).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Member{
		{"alice", []string{"src_co_admin", "rsc_developers"}},
		{"bob", []string{"src_co_member"}},
		{"carol", []string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDefaultHTTPClientIsShared(t *testing.T) {
	c := &Client{Endpoint: "http://127.0.0.1:1", Token: "t"}
	a, b := c.http(), c.http()
	if a != b {
		t.Fatal("default http client is rebuilt per call")
	}
	if a.Timeout != 10*time.Second {
		t.Fatalf("timeout = %v", a.Timeout)
	}
	tr, ok := a.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("transport = %#v, want *http.Transport with nil Proxy", a.Transport)
	}
	if tr.IdleConnTimeout == 0 {
		t.Fatal("no IdleConnTimeout")
	}
	other := &Client{}
	if other.http() != a {
		t.Fatal("clients do not share the default")
	}
}
