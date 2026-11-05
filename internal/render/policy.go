package render

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/config"
)

// policyTmpl is the Firefox enterprise policies document; Policy substitutes
// the per-pair values into it.
//
//go:embed policies.json.tmpl
var policyTmpl string

// Policy renders the Firefox enterprise policy for cfg: every request goes
// through the user's proxy (proxy:8080 on net-<user>) and the proxy's CA is
// installed from the single-file mount /proxy-ca/ca.pem (ADR-0007, ADR-0013).
func Policy(cfg *config.Config) ([]byte, error) {
	out := strings.ReplaceAll(policyTmpl, `"/mitm-ca/mitmproxy-ca-cert.pem"`, `"/proxy-ca/ca.pem"`)
	out = strings.ReplaceAll(out, `"mitm:8080"`, `"proxy:8080"`)
	out = strings.ReplaceAll(out, "{{ZEESCHUIMER_VERSION}}", cfg.ZeeschuimerVersion)
	if !json.Valid([]byte(out)) {
		return nil, fmt.Errorf("rendered policy is not valid JSON (zeeschuimer version %q)", cfg.ZeeschuimerVersion)
	}
	return []byte(out), nil
}
