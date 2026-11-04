package render

import (
	"encoding/json"
	"strings"
	"testing"
)

// policyShape is the part of the rendered enterprise policy the tests read.
type policyShape struct {
	Policies struct {
		Certificates struct {
			Install []string `json:"Install"`
		} `json:"Certificates"`
		Proxy struct {
			Mode                        string `json:"Mode"`
			HTTPProxy                   string `json:"HTTPProxy"`
			SSLProxy                    string `json:"SSLProxy"`
			UseHTTPProxyForAllProtocols bool   `json:"UseHTTPProxyForAllProtocols"`
			Passthrough                 string `json:"Passthrough"`
			Locked                      bool   `json:"Locked"`
		} `json:"Proxy"`
		ExtensionSettings map[string]struct {
			InstallationMode string `json:"installation_mode"`
			InstallURL       string `json:"install_url"`
		} `json:"ExtensionSettings"`
		DisableAppUpdate bool `json:"DisableAppUpdate"`
	} `json:"policies"`
}

func TestPolicyValues(t *testing.T) {
	cfg := testConfig(t)
	raw, err := Policy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "{{") {
		t.Error("rendered policy still contains a {{ placeholder")
	}

	var p policyShape
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("rendered policy is not valid JSON: %v", err)
	}

	if p.Policies.Proxy.HTTPProxy != "proxy:8080" {
		t.Errorf("HTTPProxy = %q, want proxy:8080", p.Policies.Proxy.HTTPProxy)
	}
	if p.Policies.Proxy.SSLProxy != "proxy:8080" {
		t.Errorf("SSLProxy = %q, want proxy:8080", p.Policies.Proxy.SSLProxy)
	}
	if !p.Policies.Proxy.Locked {
		t.Error("Proxy.Locked = false, want true")
	}
	if p.Policies.Proxy.Mode != "manual" || !p.Policies.Proxy.UseHTTPProxyForAllProtocols || p.Policies.Proxy.Passthrough != "localhost, 127.0.0.1" {
		t.Errorf("proxy block drifted from the template: %+v", p.Policies.Proxy)
	}
	if len(p.Policies.Certificates.Install) != 1 || p.Policies.Certificates.Install[0] != "/proxy-ca/ca.pem" {
		t.Errorf("Certificates.Install = %v, want [/proxy-ca/ca.pem]", p.Policies.Certificates.Install)
	}

	const ext = "{677bb78c-7168-4aa5-8e9b-32e9f26c6157}"
	e, ok := p.Policies.ExtensionSettings[ext]
	if !ok {
		t.Fatalf("extension %s missing from ExtensionSettings", ext)
	}
	if !strings.Contains(e.InstallURL, "v1.14.3/zeeschuimer-1.14.3.xpi") {
		t.Errorf("install_url = %q, want it to contain v1.14.3/zeeschuimer-1.14.3.xpi", e.InstallURL)
	}
	if e.InstallationMode != "force_installed" {
		t.Errorf("installation_mode = %q, want force_installed", e.InstallationMode)
	}
	if !p.Policies.DisableAppUpdate {
		t.Error("DisableAppUpdate = false, want true")
	}
}
