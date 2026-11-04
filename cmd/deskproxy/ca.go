package main

import (
	"crypto/tls"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/ca"
)

// ensureCA loads or generates this proxy's CA through the shared package, so
// the desk's provisioning (internal/provision) and the proxy always agree on
// the pair: cert 0644, key 0600.
func ensureCA(certPath, keyPath string) (*tls.Certificate, error) {
	return ca.Ensure(certPath, keyPath)
}
