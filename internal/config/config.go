// Package config resolves backend URLs and the on-disk config/credential
// locations. URLs are non-secret and may come from a config file or env; PATs
// are never stored here (see internal/auth).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

type secureURLError struct {
	message string
}

func (e *secureURLError) Error() string {
	return e.message
}

// IsSecureURLError reports whether err came from backend URL transport-safety
// validation. Callers at privacy boundaries can redact its configuration
// details while local CLI diagnostics retain the original actionable text.
func IsSecureURLError(err error) bool {
	var target *secureURLError
	return errors.As(err, &target)
}

// Config holds non-secret settings.
type Config struct {
	// ReadOnly blocks mutating CLI commands before credentials, stdin, or
	// network access. ATL_READ_ONLY and the global --read-only flag can enable
	// the same monotonic safety policy for one process.
	ReadOnly      bool   `json:"read_only,omitempty"`
	ConfluenceURL string `json:"confluence_url,omitempty"`
	JiraURL       string `json:"jira_url,omitempty"`
	// UpdateBaseURL is the distribution server used for self-update; empty
	// disables auto-update.
	UpdateBaseURL string `json:"update_base_url,omitempty"`
	// Transport contains backend-specific trust settings. The pointers keep an
	// untouched config byte-compatible: no empty transport object is persisted.
	Transport *TransportConfig `json:"transport,omitempty"`
	// Render holds presentation-only markdown-view settings. Pointer so a
	// config without render keys stays byte-stable (no empty "render":{}) when
	// re-saved. This is the only section a per-mirror local file may set.
	Render *RenderConfig `json:"render,omitempty"`
	// JiraListViews contains reusable source-aware list projections. Built-in
	// default/full entries are always present in effective config.
	JiraListViews map[string]JiraListView `json:"jira_list_views"`
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
	c, err := LoadForEdit()
	if err != nil {
		return nil, err
	}
	if err := ValidateRenderConfig(c.Render); err != nil {
		return nil, fmt.Errorf("%w: render: %v", domain.ErrConfig, err)
	}
	views, err := NormalizeJiraListViews(c.JiraListViews)
	if err != nil {
		return nil, fmt.Errorf("%w: jira_list_views: %v", domain.ErrConfig, err)
	}
	c.JiraListViews = views
	return c, nil
}

// LoadForEdit reads non-secret config and applies environment overrides without
// normalizing jira_list_views. It is reserved for effective inspection and
// safety-policy reads, so a malformed view can still be inspected while every
// runtime command continues to use strict Load.
func LoadForEdit() (*Config, error) {
	c, err := LoadPersistedForEdit()
	if err != nil {
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
	overlayTransportEnvironment(c)
	trimConfigURLs(c)
	return c, nil
}

// LoadPersistedForEdit reads only the global config file, without environment
// overlays or jira_list_views normalization. Global config mutations must edit
// this persisted target so process-local environment values are never written
// to disk as an unintended side effect.
func LoadPersistedForEdit() (*Config, error) {
	c := &Config{}
	if b, err := os.ReadFile(path()); err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("%w: decode config.json: %v", domain.ErrConfig, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	trimConfigURLs(c)
	return c, nil
}

func trimConfigURLs(c *Config) {
	c.ConfluenceURL = strings.TrimRight(c.ConfluenceURL, "/")
	c.JiraURL = strings.TrimRight(c.JiraURL, "/")
	c.UpdateBaseURL = strings.TrimRight(c.UpdateBaseURL, "/")
}

// Save persists non-secret config to disk (0700 dir, 0600 file).
func Save(c *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	if err := ValidateRenderConfig(c.Render); err != nil {
		return fmt.Errorf("%w: render: %v", domain.ErrConfig, err)
	}
	views, err := NormalizeJiraListViews(c.JiraListViews)
	if err != nil {
		return fmt.Errorf("%w: jira_list_views: %v", domain.ErrConfig, err)
	}
	copy := *c
	copy.JiraListViews = views
	return saveConfigBytes(&copy)
}

// SaveForListViewRepair persists a config loaded through LoadForEdit after one
// custom list-view deletion. It intentionally skips whole-catalog
// normalization so multiple invalid entries can be removed one at a time;
// runtime Load remains strict. Do not use this for general config writes.
func SaveForListViewRepair(c *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	return saveConfigBytes(c)
}

func saveConfigBytes(c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600 written atomically: config carries the self-update source URL; keep it
	// owner-only, consistent with the credentials/sidecar files.
	return safepath.WriteFileAtomic(path(), append(b, '\n'), 0o600)
}

// NormalizeBackendURL supplies https for an unambiguous schemeless backend
// host. It deliberately leaves an explicit scheme unchanged so CheckSecureURL
// can reject an explicit insecure transport rather than silently changing it.
func NormalizeBackendURL(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" || strings.HasPrefix(normalized, "//") {
		return "", invalidBackendURLError(raw)
	}
	if strings.Contains(normalized, "://") {
		return normalized, nil
	}
	if schemeLikeWithoutAuthority(normalized) {
		return "", invalidBackendURLError(raw)
	}
	u, err := url.Parse("https://" + normalized)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", invalidBackendURLError(raw)
	}
	return u.String(), nil
}

// schemeLikeWithoutAuthority rejects a malformed scheme such as https:/host
// while preserving ordinary host:port input, including single-label hosts.
func schemeLikeWithoutAuthority(raw string) bool {
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 || !validScheme(raw[:colon]) {
		return false
	}
	rest := raw[colon+1:]
	if strings.HasPrefix(rest, "//") {
		return false
	}
	port := rest
	if slash := strings.IndexByte(port, '/'); slash >= 0 {
		port = port[:slash]
	}
	if port != "" && allASCIIDigits(port) && !webScheme(raw[:colon]) {
		return false
	}
	return true
}

func validScheme(value string) bool {
	for i := range value {
		c := value[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (i == 0 || (c < '0' || c > '9') && c != '+' && c != '-' && c != '.') {
			return false
		}
	}
	return true
}

func allASCIIDigits(value string) bool {
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func webScheme(value string) bool {
	return strings.EqualFold(value, "http") || strings.EqualFold(value, "https")
}

func invalidBackendURLError(raw string) error {
	return &secureURLError{message: fmt.Sprintf("invalid backend URL %q", raw)}
}

// NormalizeAndCheckBackendURL applies the shared input and PAT transport
// policies before a backend URL is persisted or used for authentication.
func NormalizeAndCheckBackendURL(raw string) (string, error) {
	normalized, err := NormalizeBackendURL(raw)
	if err != nil {
		return "", err
	}
	if err := CheckSecureURL(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

// CheckSecureURL rejects a backend base URL that would transmit the PAT in
// cleartext: HTTP is accepted only for a loopback host or with the explicit
// ATL_ALLOW_INSECURE=1 override for an internal HTTP-only instance.
func CheckSecureURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return &secureURLError{message: fmt.Sprintf("invalid URL %q: %v", raw, err)}
	}
	if u.Scheme == "" || u.Host == "" || u.Hostname() == "" {
		return invalidBackendURLError(raw)
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(u.Scheme, "http") && (isLoopbackHost(u.Hostname()) || os.Getenv("ATL_ALLOW_INSECURE") != "") {
		return nil
	}
	return &secureURLError{message: fmt.Sprintf(
		"refusing to send the PAT over %q to %q (use https, or set ATL_ALLOW_INSECURE=1 to override)",
		u.Scheme, u.Host,
	)}
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
