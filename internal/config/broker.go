package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
)

const (
	ConnectionModeDirect = "direct"
	ConnectionModeBroker = "broker"
)

// BrokerClientConfig contains no credential bytes. Session files are
// owner-private runtime snapshots and CAFile is trust material scoped only to
// the Broker origin.
type BrokerClientConfig struct {
	BaseURL               string `json:"base_url"`
	BrokerID              string `json:"broker_id"`
	Audience              string `json:"audience"`
	CAFile                string `json:"ca_file,omitempty"`
	JiraSessionFile       string `json:"jira_session_file,omitempty"`
	ConfluenceSessionFile string `json:"confluence_session_file,omitempty"`
}

type BrokerClientProjection struct {
	Configured                  bool `json:"configured"`
	CAConfigured                bool `json:"ca_configured"`
	JiraSessionConfigured       bool `json:"jira_session_configured"`
	ConfluenceSessionConfigured bool `json:"confluence_session_configured"`
}

func BrokerProjection(c *Config) BrokerClientProjection {
	if c == nil || c.Broker == nil {
		return BrokerClientProjection{}
	}
	return BrokerClientProjection{
		Configured: true, CAConfigured: c.Broker.CAFile != "",
		JiraSessionConfigured:       c.Broker.JiraSessionFile != "",
		ConfluenceSessionConfigured: c.Broker.ConfluenceSessionFile != "",
	}
}

func EffectiveConnectionMode(value string) string {
	if value == "" {
		return ConnectionModeDirect
	}
	return value
}

func ValidateBrokerClientConfig(mode string, broker *BrokerClientConfig) error {
	mode = EffectiveConnectionMode(mode)
	if mode != ConnectionModeDirect && mode != ConnectionModeBroker {
		return fmt.Errorf("connection_mode must be direct or broker")
	}
	if broker == nil {
		if mode == ConnectionModeBroker {
			return fmt.Errorf("configuration is required in broker mode")
		}
		return nil
	}
	parsed, parseErr := url.Parse(broker.BaseURL)
	_, originErr := backendid.OriginSHA256(broker.BaseURL)
	if parseErr != nil || originErr != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimSpace(broker.BaseURL) != broker.BaseURL || strings.HasSuffix(broker.BaseURL, "/") {
		return fmt.Errorf("base_url must be one exact HTTPS origin")
	}
	if !validBrokerConfigIdentifier(broker.BrokerID, 128) || !validBrokerConfigIdentifier(broker.Audience, 128) {
		return fmt.Errorf("broker_id and audience must be printable identifiers")
	}
	if strings.TrimSpace(broker.CAFile) != broker.CAFile || strings.TrimSpace(broker.JiraSessionFile) != broker.JiraSessionFile || strings.TrimSpace(broker.ConfluenceSessionFile) != broker.ConfluenceSessionFile {
		return fmt.Errorf("file references must not contain surrounding whitespace")
	}
	if mode == ConnectionModeBroker && broker.JiraSessionFile == "" && broker.ConfluenceSessionFile == "" {
		return fmt.Errorf("at least one service session file is required in broker mode")
	}
	return nil
}

func overlayBrokerEnvironment(c *Config) {
	if value := os.Getenv("ATL_CONNECTION_MODE"); value != "" {
		c.ConnectionMode = value
	}
	values := []struct {
		key string
		set func(*BrokerClientConfig, string)
	}{
		{"ATL_BROKER_URL", func(b *BrokerClientConfig, value string) { b.BaseURL = strings.TrimRight(value, "/") }},
		{"ATL_BROKER_ID", func(b *BrokerClientConfig, value string) { b.BrokerID = value }},
		{"ATL_BROKER_AUDIENCE", func(b *BrokerClientConfig, value string) { b.Audience = value }},
		{"ATL_BROKER_CA_BUNDLE", func(b *BrokerClientConfig, value string) { b.CAFile = value }},
		{"ATL_BROKER_JIRA_SESSION_FILE", func(b *BrokerClientConfig, value string) { b.JiraSessionFile = value }},
		{"ATL_BROKER_CONFLUENCE_SESSION_FILE", func(b *BrokerClientConfig, value string) { b.ConfluenceSessionFile = value }},
	}
	for _, current := range values {
		if value := os.Getenv(current.key); value != "" {
			if c.Broker == nil {
				c.Broker = &BrokerClientConfig{}
			}
			current.set(c.Broker, value)
		}
	}
}

func validBrokerConfigIdentifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}
