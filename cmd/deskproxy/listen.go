package main

import (
	"fmt"
	"net"
)

// bindAddr resolves the address a listener should bind to when --listen-cidr
// is set (ADR-0007): the host part of listen is replaced by the first IPv4
// address of addrs that lies inside cidr, keeping listen's port. addrs are
// interface addresses as returned by net.Interface.Addrs(); entries that are
// not *net.IPNet and IPv6 addresses are ignored. With no address inside cidr
// it returns the error "no interface address in <cidr>".
func bindAddr(cidr, listen string, addrs []net.Addr) (string, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse --listen-cidr %q: %w", cidr, err)
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse listen address %q: %w", listen, err)
	}
	for _, addr := range addrs {
		ipn, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP.To4()
		if ip == nil {
			continue // IPv6 never matches: the desk's per-user networks are IPv4
		}
		if network.Contains(ip) {
			return net.JoinHostPort(ip.String(), port), nil
		}
	}
	return "", fmt.Errorf("no interface address in %s", cidr)
}

// localAddrs collects the addresses of every interface on this host.
func localAddrs() ([]net.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var addrs []net.Addr
	for _, iface := range ifaces {
		a, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("addresses of interface %s: %w", iface.Name, err)
		}
		addrs = append(addrs, a...)
	}
	return addrs, nil
}
