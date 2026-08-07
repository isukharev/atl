package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	TransportServiceJira       = "jira"
	TransportServiceConfluence = "confluence"
)

// TransportConfig holds trust settings scoped to one backend. Client
// certificates are deliberately absent: adding secret key material requires a
// separate reviewed lifecycle.
type TransportConfig struct {
	Jira       *BackendTransportConfig `json:"jira,omitempty"`
	Confluence *BackendTransportConfig `json:"confluence,omitempty"`
}

type BackendTransportConfig struct {
	CABundle string `json:"ca_bundle,omitempty"`
}

type BackendTransportSummary struct {
	CABundleConfigured bool   `json:"ca_bundle_configured"`
	CABundleSource     string `json:"ca_bundle_source"`
}

type TransportSummary struct {
	Jira       BackendTransportSummary `json:"jira"`
	Confluence BackendTransportSummary `json:"confluence"`
}

func (c *Config) CABundle(service string) string {
	if c == nil || c.Transport == nil {
		return ""
	}
	var backend *BackendTransportConfig
	switch service {
	case TransportServiceJira:
		backend = c.Transport.Jira
	case TransportServiceConfluence:
		backend = c.Transport.Confluence
	}
	if backend == nil {
		return ""
	}
	return strings.TrimSpace(backend.CABundle)
}

func TransportProjection(c *Config) TransportSummary {
	return TransportSummary{
		Jira:       backendTransportSummary(c, TransportServiceJira, "ATL_JIRA_CA_BUNDLE"),
		Confluence: backendTransportSummary(c, TransportServiceConfluence, "ATL_CONFLUENCE_CA_BUNDLE"),
	}
}

func backendTransportSummary(c *Config, service, envKey string) BackendTransportSummary {
	configured := c.CABundle(service) != ""
	source := "missing"
	if strings.TrimSpace(os.Getenv(envKey)) != "" {
		source = "environment"
	} else if configured {
		source = "config_file"
	}
	return BackendTransportSummary{CABundleConfigured: configured, CABundleSource: source}
}

func overlayTransportEnvironment(c *Config) {
	if value := strings.TrimSpace(os.Getenv("ATL_JIRA_CA_BUNDLE")); value != "" {
		setCABundle(c, TransportServiceJira, value)
	}
	if value := strings.TrimSpace(os.Getenv("ATL_CONFLUENCE_CA_BUNDLE")); value != "" {
		setCABundle(c, TransportServiceConfluence, value)
	}
}

// SetTransportKey applies one global-only dotted transport key. An empty value
// clears the setting; environment overrides still win on the next Load.
func SetTransportKey(c *Config, key, value string) error {
	switch key {
	case "transport.jira.ca_bundle":
		setCABundle(c, TransportServiceJira, strings.TrimSpace(value))
	case "transport.confluence.ca_bundle":
		setCABundle(c, TransportServiceConfluence, strings.TrimSpace(value))
	default:
		return fmt.Errorf("unknown transport key %q", key)
	}
	pruneEmptyTransport(c)
	return nil
}

func ValidTransportKeys() []string {
	return []string{"transport.confluence.ca_bundle", "transport.jira.ca_bundle"}
}

func setCABundle(c *Config, service, value string) {
	if c.Transport == nil {
		c.Transport = &TransportConfig{}
	}
	backend := &BackendTransportConfig{CABundle: value}
	if service == TransportServiceJira {
		c.Transport.Jira = backend
	} else {
		c.Transport.Confluence = backend
	}
}

func pruneEmptyTransport(c *Config) {
	if c == nil || c.Transport == nil {
		return
	}
	if c.Transport.Jira != nil && strings.TrimSpace(c.Transport.Jira.CABundle) == "" {
		c.Transport.Jira = nil
	}
	if c.Transport.Confluence != nil && strings.TrimSpace(c.Transport.Confluence.CABundle) == "" {
		c.Transport.Confluence = nil
	}
	if c.Transport.Jira == nil && c.Transport.Confluence == nil {
		c.Transport = nil
	}
}
