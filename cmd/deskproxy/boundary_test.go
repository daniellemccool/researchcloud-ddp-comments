package main

import (
	"io"
	"net/http"
	"net/url"
	"testing"
)

func TestDeniedHostsUntilBoundary(t *testing.T) {
	denied := []string{"localhost", "127.0.0.1", "[::1]", "10.0.0.5", "frontend", "fourcat.desk", "other.desk"}
	allowed := []string{"www.tiktok.com", "example.org"}

	for _, tc := range []struct {
		hosts []string
		deny  bool
	}{{denied, true}, {allowed, false}} {
		for _, h := range tc.hosts {
			for _, withPort := range []bool{false, true} {
				hostport := h
				if withPort {
					hostport += ":443"
				}
				u, err := url.Parse("https://" + hostport + "/api/x")
				if err != nil {
					t.Fatal(err)
				}
				req := &http.Request{Method: "GET", URL: u, Host: hostport}
				resp := denyResponse(req)
				if !tc.deny {
					if resp != nil {
						t.Errorf("%s: denied, want pass", hostport)
					}
					continue
				}
				if resp == nil || resp.StatusCode != 403 {
					t.Errorf("%s: want 403, got %v", hostport, resp)
					continue
				}
				b, _ := io.ReadAll(resp.Body)
				if string(b) != "deskproxy: denied by rule" {
					t.Errorf("%s: body %q", hostport, b)
				}
			}
		}
	}
}
