package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/safepath"
)

// RenderService holds the markdown-view rendering knobs for one backend. It is
// presentation-only: none of these keys can influence where a PAT is sent, so
// they are the sole content a per-mirror local config may carry.
type RenderService struct {
	Profile      string   `json:"profile,omitempty"` // minimal|default|full ("" = default)
	Include      []string `json:"include,omitempty"`
	Exclude      []string `json:"exclude,omitempty"`
	CustomFields []string `json:"custom_fields,omitempty"` // jira only
}

// RenderConfig groups the per-backend render sections. The pointer fields keep
// serialized output minimal — an unset service is elided rather than emitted as
// an empty object, so a config file without render keys stays byte-stable when
// re-saved.
type RenderConfig struct {
	Jira       *RenderService `json:"jira,omitempty"`
	Confluence *RenderService `json:"confluence,omitempty"`
}

// LocalConfig is the sanitized view of a per-mirror <root>/.atl/config.json. It
// carries render keys only; any other key found in the file is reported as a
// warning and dropped (never applied) — a mirror directory can be shared or
// checked out, so a repo-local file must never redirect credentials.
type LocalConfig struct {
	Render *RenderConfig `json:"render,omitempty"`
}

// Provenance maps a dotted render key (e.g. "render.jira.profile") to the source
// that supplied its effective value: "default", "global", or "local".
type Provenance map[string]string

// DefaultProfile is the built-in render profile when none is configured.
const DefaultProfile = "default"

// validProfiles is the closed set of render profile names (empty means default).
var validProfiles = map[string]bool{"minimal": true, "default": true, "full": true}

// ValidProfile reports whether p is an accepted render profile. Empty is valid
// (it falls back to the default profile).
func ValidProfile(p string) bool { return p == "" || validProfiles[p] }

// localConfigPath returns the per-mirror config path under a mirror root.
func localConfigPath(root string) string {
	return filepath.Join(root, ".atl", "config.json")
}

// LocalConfigPath is the exported path helper for callers that need to report
// where a mirror's local config lives.
func LocalConfigPath(root string) string { return localConfigPath(root) }

// knownGlobalKeys are credential-adjacent keys that are legal in the global
// config file but are a hard error (reported + ignored) in a local file.
var knownGlobalKeys = map[string]bool{
	"confluence_url":  true,
	"jira_url":        true,
	"update_base_url": true,
}

// LoadLocal reads <root>/.atl/config.json and returns its sanitized render
// section plus any warnings. A missing file yields (nil, nil, nil). The file is
// parsed defensively: it never fails a command — malformed JSON, forbidden
// (credential-adjacent) keys, unknown keys, and invalid profile values all
// degrade to a warning and are dropped, so the effective config falls open to
// global/defaults rather than trusting untrusted local bytes.
func LoadLocal(root string) (*LocalConfig, []string, error) {
	b, err := os.ReadFile(localConfigPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var warnings []string
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		// A malformed local file is ignored entirely (fail open to global), not
		// fatal — a corrupt shared repo file must not brick every command.
		return nil, []string{fmt.Sprintf("ignoring local config %s: malformed JSON (%v)", localConfigPath(root), err)}, nil
	}

	lc := &LocalConfig{}
	for _, key := range sortedKeys(raw) {
		switch {
		case key == "render":
			rc, rw := parseLocalRender(raw[key], localConfigPath(root))
			lc.Render = rc
			warnings = append(warnings, rw...)
		case knownGlobalKeys[key]:
			warnings = append(warnings, fmt.Sprintf("ignoring %q in local config %s: credential-adjacent keys are global/env-only and never read from a mirror-local file", key, localConfigPath(root)))
		default:
			warnings = append(warnings, fmt.Sprintf("ignoring unknown key %q in local config %s", key, localConfigPath(root)))
		}
	}
	if lc.Render == nil {
		// File had no usable render section.
		return nil, warnings, nil
	}
	return lc, warnings, nil
}

// parseLocalRender decodes the render section of a local file and validates each
// service's profile, dropping invalid values with a warning.
func parseLocalRender(raw json.RawMessage, path string) (*RenderConfig, []string) {
	var rc RenderConfig
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, []string{fmt.Sprintf("ignoring render section in local config %s: %v", path, err)}
	}
	var warnings []string
	rc.Jira, warnings = sanitizeService(rc.Jira, "render.jira", path, warnings)
	rc.Confluence, warnings = sanitizeService(rc.Confluence, "render.confluence", path, warnings)
	if rc.Jira == nil && rc.Confluence == nil {
		return nil, warnings
	}
	return &rc, warnings
}

func sanitizeService(s *RenderService, keyPrefix, path string, warnings []string) (*RenderService, []string) {
	if s == nil {
		return nil, warnings
	}
	if !ValidProfile(s.Profile) {
		warnings = append(warnings, fmt.Sprintf("ignoring %s.profile=%q in local config %s: expected minimal|default|full", keyPrefix, s.Profile, path))
		s.Profile = ""
	}
	return s, warnings
}

// EffectiveRender merges the built-in defaults, the global config, and the
// sanitized local config per key (profile, include, exclude, custom_fields are
// each independently overridden by the highest-precedence source that sets
// them: local > global > default). It returns the fully-populated effective
// RenderConfig and a Provenance for every tracked key.
func EffectiveRender(global *Config, local *LocalConfig) (RenderConfig, Provenance) {
	prov := Provenance{}
	var globalRender *RenderConfig
	if global != nil {
		globalRender = global.Render
	}
	var localRender *RenderConfig
	if local != nil {
		localRender = local.Render
	}
	jira := mergeService("render.jira", serviceOf(globalRender, true), serviceOf(localRender, true), prov, true)
	conf := mergeService("render.confluence", serviceOf(globalRender, false), serviceOf(localRender, false), prov, false)
	return RenderConfig{Jira: jira, Confluence: conf}, prov
}

func serviceOf(rc *RenderConfig, jira bool) *RenderService {
	if rc == nil {
		return nil
	}
	if jira {
		return rc.Jira
	}
	return rc.Confluence
}

// mergeService applies default -> global -> local per key and records provenance.
// custom_fields is jira-only; it is neither merged nor tracked for confluence.
func mergeService(keyPrefix string, global, local *RenderService, prov Provenance, jira bool) *RenderService {
	out := &RenderService{Profile: DefaultProfile}
	prov[keyPrefix+".profile"] = "default"
	prov[keyPrefix+".include"] = "default"
	prov[keyPrefix+".exclude"] = "default"
	if jira {
		prov[keyPrefix+".custom_fields"] = "default"
	}

	apply := func(s *RenderService, source string) {
		if s == nil {
			return
		}
		if s.Profile != "" {
			out.Profile = s.Profile
			prov[keyPrefix+".profile"] = source
		}
		if len(s.Include) > 0 {
			out.Include = append([]string(nil), s.Include...)
			prov[keyPrefix+".include"] = source
		}
		if len(s.Exclude) > 0 {
			out.Exclude = append([]string(nil), s.Exclude...)
			prov[keyPrefix+".exclude"] = source
		}
		if jira && len(s.CustomFields) > 0 {
			out.CustomFields = append([]string(nil), s.CustomFields...)
			prov[keyPrefix+".custom_fields"] = source
		}
	}
	apply(global, "global")
	apply(local, "local")
	return out
}

// renderFields is the closed set of settable render fields; custom_fields is
// jira-only.
var renderFields = map[string]bool{
	"profile":       true,
	"include":       true,
	"exclude":       true,
	"custom_fields": true,
}

// ValidRenderKeys lists the accepted dotted keys for `config set`, for error
// messages.
func ValidRenderKeys() []string {
	return []string{
		"render.jira.profile", "render.jira.include", "render.jira.exclude", "render.jira.custom_fields",
		"render.confluence.profile", "render.confluence.include", "render.confluence.exclude",
	}
}

// ErrNotRenderKey is returned by SetRenderKey when the key is not a render key.
var ErrNotRenderKey = errors.New("not a render key")

// SetRenderKey validates and applies a dotted render key/value onto rc,
// allocating the service section as needed. include/exclude/custom_fields take a
// comma-separated value. It returns ErrNotRenderKey for a non-render key and a
// descriptive error for an unknown/misused key or an invalid profile value.
func SetRenderKey(rc *RenderConfig, key, value string) error {
	parts := strings.Split(key, ".")
	if len(parts) == 0 || parts[0] != "render" {
		return ErrNotRenderKey
	}
	if len(parts) != 3 {
		return fmt.Errorf("invalid render key %q (want render.<jira|confluence>.<field>)", key)
	}
	svc, field := parts[1], parts[2]
	if svc != "jira" && svc != "confluence" {
		return fmt.Errorf("invalid render service %q in %q (want jira or confluence)", svc, key)
	}
	if !renderFields[field] {
		return fmt.Errorf("unknown render field %q in %q", field, key)
	}
	if field == "custom_fields" && svc != "jira" {
		return fmt.Errorf("custom_fields is jira-only; %q is not valid", key)
	}

	target := &rc.Jira
	if svc == "confluence" {
		target = &rc.Confluence
	}
	if *target == nil {
		*target = &RenderService{}
	}
	s := *target

	switch field {
	case "profile":
		if !ValidProfile(value) {
			return fmt.Errorf("invalid profile %q (want minimal|default|full)", value)
		}
		s.Profile = value
	case "include":
		s.Include = splitList(value)
	case "exclude":
		s.Exclude = splitList(value)
	case "custom_fields":
		s.CustomFields = splitList(value)
	}
	return nil
}

// SaveLocal writes a render-only local config to <root>/.atl/config.json
// atomically (0600), creating the .atl dir if needed. The file never contains
// any credential-adjacent key.
func SaveLocal(root string, lc *LocalConfig) error {
	dir := filepath.Join(root, ".atl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	out := &LocalConfig{Render: nil}
	if lc != nil {
		out.Render = lc.Render
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return safepath.WriteFileAtomic(localConfigPath(root), append(b, '\n'), 0o600)
}

func splitList(value string) []string {
	var out []string
	for _, p := range strings.Split(value, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
