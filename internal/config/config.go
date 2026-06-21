// Package config resolves backend URLs and the on-disk config/credential
// locations. URLs are non-secret and may come from a config file or env; PATs
// are never stored here (see internal/auth).
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/safepath"
)

// Config holds non-secret settings.
type Config struct {
	ConfluenceURL string `json:"confluence_url,omitempty"`
	JiraURL       string `json:"jira_url,omitempty"`
	// UpdateBaseURL is the distribution server used for self-update; empty
	// disables auto-update.
	UpdateBaseURL string `json:"update_base_url,omitempty"`
}

// Dir returns the per-user config directory (~/.config/atl), honoring
// XDG_CONFIG_HOME and ATL_CONFIG_DIR.
func Dir() string {
	if d := os.Getenv("ATL_CONFIG_DIR"); d != "" {
		return d
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "atl")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".atl"
	}
	return filepath.Join(home, ".config", "atl")
}

func path() string { return filepath.Join(Dir(), "config.json") }

// Load reads the config file (if any) then overlays environment variables.
// Env always wins so CI/agents can override without touching disk.
func Load() (*Config, error) {
	c := &Config{}
	if b, err := os.ReadFile(path()); err == nil {
		_ = json.Unmarshal(b, c) // tolerate partial/legacy files
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if v := firstEnv("ATL_CONFLUENCE_URL", "CONFLUENCE_URL"); v != "" {
		c.ConfluenceURL = v
	}
	if v := firstEnv("ATL_JIRA_URL", "JIRA_URL"); v != "" {
		c.JiraURL = v
	}
	if v := os.Getenv("ATL_UPDATE_URL"); v != "" {
		c.UpdateBaseURL = v
	}
	c.ConfluenceURL = strings.TrimRight(c.ConfluenceURL, "/")
	c.JiraURL = strings.TrimRight(c.JiraURL, "/")
	c.UpdateBaseURL = strings.TrimRight(c.UpdateBaseURL, "/")
	return c, nil
}

// Save persists non-secret config to disk (0700 dir, 0600 file).
func Save(c *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600 written atomically: config carries the self-update source URL; keep it
	// owner-only, consistent with the credentials/sidecar files.
	return safepath.WriteFileAtomic(path(), append(b, '\n'), 0o600)
}

// CheckSecureURL rejects a backend base URL that would transmit the PAT in
// cleartext: any non-https scheme for a non-loopback host. Loopback hosts (test
// servers) are always allowed, and ATL_ALLOW_INSECURE=1 overrides the check for
// an internal http-only instance the operator explicitly trusts.
func CheckSecureURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %v", raw, err)
	}
	if u.Scheme == "https" || isLoopbackHost(u.Hostname()) || os.Getenv("ATL_ALLOW_INSECURE") != "" {
		return nil
	}
	return fmt.Errorf("refusing to send the PAT over %q to %q (use https, or set ATL_ALLOW_INSECURE=1 to override)", u.Scheme, u.Host)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
