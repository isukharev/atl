// Package config resolves backend URLs and the on-disk config/credential
// locations. URLs are non-secret and may come from a config file or env; PATs
// are never stored here (see internal/auth).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// Save persists non-secret config to disk (0600 dir, 0644 file).
func Save(c *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600: config carries the self-update source URL; keep it owner-only,
	// consistent with the credentials/sidecar files.
	return os.WriteFile(path(), append(b, '\n'), 0o600)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
