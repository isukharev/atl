package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/isukharev/atl/internal/safepath"
)

// RenderService holds view-related knobs for one backend. Most are
// presentation-only and may live in a mirror-local config. JiraMacros is the
// exception: it controls whether authenticated Jira reads may occur and is
// therefore global/per-run only; LoadLocal always drops it.
type RenderService struct {
	Profile      string                `json:"profile,omitempty"` // minimal|default|full ("" = default)
	Include      []string              `json:"include,omitempty"`
	Exclude      []string              `json:"exclude,omitempty"`
	CustomFields []string              `json:"custom_fields,omitempty"` // jira only
	FieldViews   []JiraFieldView       `json:"field_views,omitempty"`   // jira only; typed presentation
	EpicField    string                `json:"epic_field,omitempty"`    // jira only; empty = auto-detect Epic Link
	PageFields   []ConfluenceFieldView `json:"page_fields,omitempty"`   // confluence only; typed page metadata
	JiraMacros   string                `json:"jira_macros,omitempty"`   // confluence only: auto (default) | off
}

// JiraFieldView describes how one raw Jira field is presented in the derived
// Markdown view. ID is the API field id/key. Label is the human-readable name
// in the metadata table or the Markdown heading for a section (defaults to ID).
// Format is auto|scalar|list|jira_wiki|
// date|datetime. Missing values are omitted unless ShowEmpty is true.
//
// These descriptors are presentation-only. They may live in a mirror-local
// config without changing the backend host or credential scope.
type JiraFieldView struct {
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	Placement string `json:"placement,omitempty"` // metadata (default) | section
	Format    string `json:"format,omitempty"`    // auto (default) | scalar | list | jira_wiki | date | datetime
	ShowEmpty bool   `json:"show_empty,omitempty"`
	Editable  bool   `json:"editable,omitempty"` // only section+jira_wiki; mirror views only
}

// ConfluenceFieldView describes one closed, read-only page metadata field in a
// Confluence derived view. ID is one of title, space, version, parent,
// ancestors, labels, restricted, or updated. Placement is metadata (default)
// or section; format is auto (default), scalar, list, date, or datetime.
type ConfluenceFieldView struct {
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	Placement string `json:"placement,omitempty"`
	Format    string `json:"format,omitempty"`
	ShowEmpty bool   `json:"show_empty,omitempty"`
}

// RenderConfig groups the per-backend render sections. The pointer fields keep
// serialized output minimal — an unset service is elided rather than emitted as
// an empty object, so a config file without render keys stays byte-stable when
// re-saved.
type RenderConfig struct {
	DisplayTimeZone string         `json:"display_time_zone,omitempty"`
	Jira            *RenderService `json:"jira,omitempty"`
	Confluence      *RenderService `json:"confluence,omitempty"`
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

// DefaultDisplayTimeZone makes offline rendering deterministic across laptops,
// containers, and CI. It is presentation-only and never defines JQL/CQL
// semantics.
const DefaultDisplayTimeZone = "UTC"

// validProfiles is the closed set of render profile names (empty means default).
var validProfiles = map[string]bool{"minimal": true, "default": true, "full": true}

// ValidJiraMacroMode reports whether a Confluence Jira-query expansion policy
// is accepted. Empty inherits the effective default (auto).
func ValidJiraMacroMode(mode string) bool { return mode == "" || mode == "auto" || mode == "off" }

// ValidProfile reports whether p is an accepted render profile. Empty is valid
// (it falls back to the default profile).
func ValidProfile(p string) bool { return p == "" || validProfiles[p] }

// NormalizeDisplayTimeZone validates one IANA presentation timezone. Empty
// means the stable built-in UTC default.
func NormalizeDisplayTimeZone(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultDisplayTimeZone, nil
	}
	location, err := time.LoadLocation(value)
	if err != nil {
		return "", fmt.Errorf("invalid IANA display timezone %q", value)
	}
	return location.String(), nil
}

// ValidateRenderConfig checks global render values that must fail closed.
func ValidateRenderConfig(rc *RenderConfig) error {
	if rc == nil || strings.TrimSpace(rc.DisplayTimeZone) == "" {
		return nil
	}
	_, err := NormalizeDisplayTimeZone(rc.DisplayTimeZone)
	return err
}

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
	b, err := safepath.ReadFileWithin(root, localConfigPath(root))
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
	if rc.DisplayTimeZone != "" {
		normalized, err := NormalizeDisplayTimeZone(rc.DisplayTimeZone)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("ignoring render.display_time_zone=%q in local config %s: %v", rc.DisplayTimeZone, path, err))
			rc.DisplayTimeZone = ""
		} else {
			rc.DisplayTimeZone = normalized
		}
	}
	rc.Jira, warnings = sanitizeService(rc.Jira, "render.jira", path, warnings)
	rc.Confluence, warnings = sanitizeService(rc.Confluence, "render.confluence", path, warnings)
	if rc.DisplayTimeZone == "" && rc.Jira == nil && rc.Confluence == nil {
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
	if keyPrefix == "render.jira" {
		var kept []JiraFieldView
		for i, fv := range s.FieldViews {
			norm, err := NormalizeJiraFieldView(fv)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("ignoring %s.field_views[%d] in local config %s: %v", keyPrefix, i, path, err))
				continue
			}
			kept = append(kept, norm)
		}
		s.FieldViews = kept
		if len(s.PageFields) > 0 {
			warnings = append(warnings, fmt.Sprintf("ignoring Confluence-only page_fields under %s in local config %s", keyPrefix, path))
			s.PageFields = nil
		}
		if s.JiraMacros != "" {
			warnings = append(warnings, fmt.Sprintf("ignoring Confluence-only jira_macros under %s in local config %s", keyPrefix, path))
			s.JiraMacros = ""
		}
		if strings.ContainsAny(s.EpicField, "\r\n") {
			warnings = append(warnings, fmt.Sprintf("ignoring %s.epic_field in local config %s: line breaks are not allowed", keyPrefix, path))
			s.EpicField = ""
		}
	} else {
		if s.JiraMacros != "" {
			warnings = append(warnings, fmt.Sprintf("ignoring global-only %s.jira_macros in local config %s: a shared mirror cannot enable authenticated Jira reads", keyPrefix, path))
			s.JiraMacros = ""
		}
		var kept []ConfluenceFieldView
		for i, fv := range s.PageFields {
			norm, err := NormalizeConfluenceFieldView(fv)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("ignoring %s.page_fields[%d] in local config %s: %v", keyPrefix, i, path, err))
				continue
			}
			kept = append(kept, norm)
		}
		s.PageFields = kept
		if len(s.CustomFields) > 0 || len(s.FieldViews) > 0 || s.EpicField != "" {
			warnings = append(warnings, fmt.Sprintf("ignoring Jira-only custom_fields/field_views/epic_field under %s in local config %s", keyPrefix, path))
			s.CustomFields = nil
			s.FieldViews = nil
			s.EpicField = ""
		}
	}
	return s, warnings
}

// EffectiveRender merges the built-in defaults, the global config, and the
// sanitized local config per key (profile, include, exclude, custom_fields,
// field_views, and epic_field are
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
	displayTimeZone := DefaultDisplayTimeZone
	prov["render.display_time_zone"] = "default"
	if globalRender != nil && globalRender.DisplayTimeZone != "" {
		if normalized, err := NormalizeDisplayTimeZone(globalRender.DisplayTimeZone); err == nil {
			displayTimeZone = normalized
			prov["render.display_time_zone"] = "global"
		}
	}
	if localRender != nil && localRender.DisplayTimeZone != "" {
		displayTimeZone = localRender.DisplayTimeZone
		prov["render.display_time_zone"] = "local"
	}
	jira := mergeService("render.jira", serviceOf(globalRender, true), serviceOf(localRender, true), prov, true)
	conf := mergeService("render.confluence", serviceOf(globalRender, false), serviceOf(localRender, false), prov, false)
	return RenderConfig{DisplayTimeZone: displayTimeZone, Jira: jira, Confluence: conf}, prov
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
		prov[keyPrefix+".field_views"] = "default"
		prov[keyPrefix+".epic_field"] = "default"
	} else {
		prov[keyPrefix+".page_fields"] = "default"
		prov[keyPrefix+".jira_macros"] = "default"
		out.JiraMacros = "auto"
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
		if jira && len(s.FieldViews) > 0 {
			out.FieldViews = append([]JiraFieldView(nil), s.FieldViews...)
			prov[keyPrefix+".field_views"] = source
		}
		if jira && s.EpicField != "" {
			out.EpicField = strings.TrimSpace(s.EpicField)
			prov[keyPrefix+".epic_field"] = source
		}
		if !jira && len(s.PageFields) > 0 {
			out.PageFields = append([]ConfluenceFieldView(nil), s.PageFields...)
			prov[keyPrefix+".page_fields"] = source
		}
		if !jira && s.JiraMacros != "" {
			out.JiraMacros = s.JiraMacros
			prov[keyPrefix+".jira_macros"] = source
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
	"field_views":   true,
	"epic_field":    true,
	"page_fields":   true,
	"jira_macros":   true,
}

// ValidRenderKeys lists the accepted dotted keys for `config set`, for error
// messages.
func ValidRenderKeys() []string {
	return []string{
		"render.display_time_zone",
		"render.jira.profile", "render.jira.include", "render.jira.exclude", "render.jira.custom_fields", "render.jira.field_views", "render.jira.epic_field",
		"render.confluence.profile", "render.confluence.include", "render.confluence.exclude",
		"render.confluence.page_fields",
		"render.confluence.jira_macros",
	}
}

// ErrNotRenderKey is returned by SetRenderKey when the key is not a render key.
var ErrNotRenderKey = errors.New("not a render key")

// SetRenderKey validates and applies a dotted render key/value onto rc,
// allocating the service section as needed. include/exclude/custom_fields take a
// comma-separated value. It returns ErrNotRenderKey for a non-render key and a
// descriptive error for an unknown/misused key or an invalid profile value.
func SetRenderKey(rc *RenderConfig, key, value string) error {
	if key == "render.display_time_zone" {
		normalized, err := NormalizeDisplayTimeZone(value)
		if err != nil {
			return err
		}
		rc.DisplayTimeZone = normalized
		return nil
	}
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
	if (field == "custom_fields" || field == "field_views" || field == "epic_field") && svc != "jira" {
		return fmt.Errorf("%s is jira-only; %q is not valid", field, key)
	}
	if field == "jira_macros" && svc != "confluence" {
		return fmt.Errorf("jira_macros is confluence-only; %q is not valid", key)
	}
	if field == "page_fields" && svc != "confluence" {
		return fmt.Errorf("page_fields is confluence-only; %q is not valid", key)
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
	case "field_views":
		var views []JiraFieldView
		if err := json.Unmarshal([]byte(value), &views); err != nil {
			return fmt.Errorf("field_views must be a JSON array: %v", err)
		}
		for i, fv := range views {
			norm, err := NormalizeJiraFieldView(fv)
			if err != nil {
				return fmt.Errorf("field_views[%d]: %v", i, err)
			}
			views[i] = norm
		}
		s.FieldViews = views
	case "epic_field":
		value = strings.TrimSpace(value)
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("epic_field must not contain line breaks")
		}
		s.EpicField = value
	case "page_fields":
		var views []ConfluenceFieldView
		if err := json.Unmarshal([]byte(value), &views); err != nil {
			return fmt.Errorf("page_fields must be a JSON array: %v", err)
		}
		for i, fv := range views {
			norm, err := NormalizeConfluenceFieldView(fv)
			if err != nil {
				return fmt.Errorf("page_fields[%d]: %v", i, err)
			}
			views[i] = norm
		}
		s.PageFields = views
	case "jira_macros":
		value = strings.TrimSpace(value)
		if !ValidJiraMacroMode(value) || value == "" {
			return fmt.Errorf("jira_macros must be auto or off")
		}
		s.JiraMacros = value
	}
	return nil
}

var confluenceFieldDefaults = map[string]struct {
	label  string
	format string
}{
	"title":      {label: "Title", format: "scalar"},
	"space":      {label: "Space", format: "scalar"},
	"version":    {label: "Version", format: "scalar"},
	"parent":     {label: "Parent", format: "scalar"},
	"ancestors":  {label: "Ancestors", format: "list"},
	"labels":     {label: "Labels", format: "list"},
	"restricted": {label: "Restricted", format: "scalar"},
	"updated":    {label: "Updated", format: "datetime"},
}

// NormalizeConfluenceFieldView validates a closed Confluence page descriptor
// and fills stable defaults. It deliberately has no editable mode.
func NormalizeConfluenceFieldView(fv ConfluenceFieldView) (ConfluenceFieldView, error) {
	fv.ID = strings.TrimSpace(fv.ID)
	fv.Label = strings.TrimSpace(fv.Label)
	fv.Placement = strings.TrimSpace(fv.Placement)
	fv.Format = strings.TrimSpace(fv.Format)
	def, ok := confluenceFieldDefaults[fv.ID]
	if !ok {
		return ConfluenceFieldView{}, fmt.Errorf("id %q is invalid (want ancestors|labels|parent|restricted|space|title|updated|version)", fv.ID)
	}
	if strings.ContainsAny(fv.Label, "\r\n") {
		return ConfluenceFieldView{}, fmt.Errorf("label must not contain line breaks")
	}
	if fv.Label == "" {
		fv.Label = def.label
	}
	if fv.Placement == "" {
		fv.Placement = "metadata"
	}
	if fv.Placement != "metadata" && fv.Placement != "section" {
		return ConfluenceFieldView{}, fmt.Errorf("placement %q is invalid (want metadata|section)", fv.Placement)
	}
	if fv.Format == "" || fv.Format == "auto" {
		fv.Format = def.format
	}
	switch fv.Format {
	case "scalar":
	case "list":
		if fv.ID != "ancestors" && fv.ID != "labels" {
			return ConfluenceFieldView{}, fmt.Errorf("list format is only valid for ancestors or labels")
		}
	case "date", "datetime":
		if fv.ID != "updated" {
			return ConfluenceFieldView{}, fmt.Errorf("%s format is only valid for updated", fv.Format)
		}
	default:
		return ConfluenceFieldView{}, fmt.Errorf("format %q is invalid (want auto|scalar|list|date|datetime)", fv.Format)
	}
	return fv, nil
}

// NormalizeJiraFieldView validates a descriptor and fills its stable defaults.
// It is used at config-write, local-config load, and render resolution so a
// hand-edited global config degrades to warnings rather than reaching the
// renderer with an unsafe/ambiguous shape.
func NormalizeJiraFieldView(fv JiraFieldView) (JiraFieldView, error) {
	fv.ID = strings.TrimSpace(fv.ID)
	fv.Label = strings.TrimSpace(fv.Label)
	fv.Placement = strings.TrimSpace(fv.Placement)
	fv.Format = strings.TrimSpace(fv.Format)
	if fv.ID == "" {
		return JiraFieldView{}, fmt.Errorf("id is required")
	}
	if strings.ContainsAny(fv.ID+fv.Label, "\r\n") {
		return JiraFieldView{}, fmt.Errorf("id and label must not contain line breaks")
	}
	if fv.Label == "" {
		fv.Label = fv.ID
	}
	if fv.Placement == "" {
		fv.Placement = "metadata"
	}
	if fv.Placement != "metadata" && fv.Placement != "section" {
		return JiraFieldView{}, fmt.Errorf("placement %q is invalid (want metadata|section)", fv.Placement)
	}
	if fv.Format == "" {
		fv.Format = "auto"
	}
	switch fv.Format {
	case "auto", "scalar", "list", "jira_wiki", "date", "datetime":
	default:
		return JiraFieldView{}, fmt.Errorf("format %q is invalid (want auto|scalar|list|jira_wiki|date|datetime)", fv.Format)
	}
	if fv.Format == "jira_wiki" && fv.Placement != "section" {
		return JiraFieldView{}, fmt.Errorf("jira_wiki format requires section placement")
	}
	if fv.Editable && (fv.Placement != "section" || fv.Format != "jira_wiki") {
		return JiraFieldView{}, fmt.Errorf("editable requires section placement with jira_wiki format")
	}
	if fv.Editable && fv.ID == "description" {
		return JiraFieldView{}, fmt.Errorf("description already has its own editable generated section")
	}
	return fv, nil
}

// SaveLocal writes a render-only local config to <root>/.atl/config.json
// atomically (0600), creating the .atl dir if needed. The file never contains
// any credential-adjacent key.
func SaveLocal(root string, lc *LocalConfig) error {
	dir := filepath.Join(root, ".atl")
	if err := safepath.MkdirAllWithin(root, dir, 0o700); err != nil {
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
	return safepath.WriteFileWithin(root, localConfigPath(root), append(b, '\n'), 0o600)
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
