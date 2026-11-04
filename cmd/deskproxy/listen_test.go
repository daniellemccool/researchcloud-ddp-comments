package main

import (
	"net"
	"strings"
	"testing"
)

func mustIPNet(t *testing.T, s string) *net.IPNet {
	t.Helper()
	ip, ipn, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	// Interface.Addrs() reports the address with the network's mask, not the
	// masked network ParseCIDR returns.
	return &net.IPNet{IP: ip, Mask: ipn.Mask}
}

// TestBindAddr is ADR-0007's resolution rule: with --listen-cidr the listener
// binds to the first IPv4 interface address inside the CIDR, keeping the port
// of the listen flag and replacing any explicit host.
func TestBindAddr(t *testing.T) {
	unix := &net.UnixAddr{Name: "@deskproxy", Net: "unix"}
	for _, tc := range []struct {
		name    string
		cidr    string
		listen  string
		addrs   []net.Addr
		want    string
		wantErr string
	}{
		{
			name:   "first address inside the cidr wins",
			cidr:   "10.200.1.0/24",
			listen: ":8080",
			addrs:  []net.Addr{mustIPNet(t, "10.200.1.2/24"), mustIPNet(t, "172.18.0.5/16")},
			want:   "10.200.1.2:8080",
		},
		{
			name:    "no address inside the cidr",
			cidr:    "10.200.1.0/24",
			listen:  ":8080",
			addrs:   []net.Addr{mustIPNet(t, "172.18.0.5/16")},
			wantErr: "no interface address in 10.200.1.0/24",
		},
		{
			name:   "ipv6 address is skipped for a v4 cidr",
			cidr:   "10.200.1.0/24",
			listen: ":8080",
			addrs:  []net.Addr{mustIPNet(t, "fd00::1/64"), mustIPNet(t, "10.200.1.2/24")},
			want:   "10.200.1.2:8080",
		},
		{
			name:    "only an ipv6 address inside the v4 cidr",
			cidr:    "10.200.1.0/24",
			listen:  ":8080",
			addrs:   []net.Addr{mustIPNet(t, "fd00::1/64")},
			wantErr: "no interface address in 10.200.1.0/24",
		},
		{
			name:    "a ::1/128 entry never matches, not even its own cidr",
			cidr:    "::1/128",
			listen:  ":8080",
			addrs:   []net.Addr{mustIPNet(t, "::1/128")},
			wantErr: "no interface address in ::1/128",
		},
		{
			name:   "explicit host in listen is replaced",
			cidr:   "10.200.1.0/24",
			listen: "0.0.0.0:8081",
			addrs:  []net.Addr{mustIPNet(t, "10.200.1.2/24")},
			want:   "10.200.1.2:8081",
		},
		{
			name:   "non-IPNet addr entries are ignored",
			cidr:   "10.200.1.0/24",
			listen: ":8080",
			addrs:  []net.Addr{unix, mustIPNet(t, "10.200.1.2/24")},
			want:   "10.200.1.2:8080",
		},
		{
			name:    "only a non-IPNet addr entry",
			cidr:    "10.200.1.0/24",
			listen:  ":8080",
			addrs:   []net.Addr{unix},
			wantErr: "no interface address in 10.200.1.0/24",
		},
		{
			name:    "bad cidr is an error",
			cidr:    "bogus",
			listen:  ":8080",
			addrs:   []net.Addr{mustIPNet(t, "10.200.1.2/24")},
			wantErr: "bogus",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bindAddr(tc.cidr, tc.listen, tc.addrs)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("bindAddr(%q, %q, %v) = %q, want an error containing %q", tc.cidr, tc.listen, tc.addrs, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("bindAddr(%q, %q, %v) error = %q, want it to contain %q", tc.cidr, tc.listen, tc.addrs, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("bindAddr(%q, %q, %v) unexpected error: %v", tc.cidr, tc.listen, tc.addrs, err)
			}
			if got != tc.want {
				t.Errorf("bindAddr(%q, %q, %v) = %q, want %q", tc.cidr, tc.listen, tc.addrs, got, tc.want)
			}
		})
	}
}
