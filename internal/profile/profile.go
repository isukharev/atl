// Package profile owns the private, structured agent workflow profile.
// Profiles contain no credentials, but may contain private selectors and schema
// names, so callers persist them owner-only under the ATL config directory.
package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	SchemaVersion = 1
	FileName      = "profile.json"
	MaxBytes      = 4 << 20
	lockFileName  = ".profile.lock"
	maxItems      = 1000
)

// Profile separates verified backend facts from human choices and team policy.
// The split is intentional: later learning may suggest facts/preferences, but
// must never infer or silently change TeamPolicy.
type Profile struct {
	SchemaVersion  int                  `json:"schema_version"`
	Schema         SchemaFacts          `json:"schema,omitempty"`
	Preferences    Preferences          `json:"preferences,omitempty"`
	TeamPolicy     *TeamPolicy          `json:"team_policy,omitempty"`
	RenderDefaults *config.RenderConfig `json:"render_defaults,omitempty"`
	Selectors      Selectors            `json:"selectors,omitempty"`
}

type SchemaFacts struct {
	JiraFields       []JiraFieldFact       `json:"jira_fields,omitempty"`
	ConfluenceSpaces []ConfluenceSpaceFact `json:"confluence_spaces,omitempty"`
}

type JiraFieldFact struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Type       string    `json:"type,omitempty"`
	Source     string    `json:"source"`
	VerifiedAt time.Time `json:"verified_at"`
}

type ConfluenceSpaceFact struct {
	Key        string    `json:"key"`
	Name       string    `json:"name,omitempty"`
	Source     string    `json:"source"`
	VerifiedAt time.Time `json:"verified_at"`
}

type Preferences struct {
	Confirmed  bool     `json:"confirmed,omitempty"`
	Services   []string `json:"services,omitempty"`
	MirrorRoot string   `json:"mirror_root,omitempty"`
}

type TeamPolicy struct {
	Source string   `json:"source"`
	Rules  []string `json:"rules,omitempty"`
}

type Selectors struct {
	Jira       []JiraSelector       `json:"jira,omitempty"`
	Confluence []ConfluenceSelector `json:"confluence,omitempty"`
}

type JiraSelector struct {
	Name   string   `json:"name"`
	JQL    string   `json:"jql"`
	Fields []string `json:"fields,omitempty"`
}

type ConfluenceSelector struct {
	Name string `json:"name"`
	CQL  string `json:"cql"`
}

type SectionChange struct {
	Section string `json:"section"`
	Status  string `json:"status"` // added|removed|changed|unchanged
}

type Preview struct {
	Path                       string          `json:"path"`
	CurrentExists              bool            `json:"current_exists"`
	CurrentHash                string          `json:"current_hash"`
	CandidateHash              string          `json:"candidate_hash"`
	Changed                    bool            `json:"changed"`
	MigrationFromSchemaVersion *int            `json:"migration_from_schema_version,omitempty"`
	Sections                   []SectionChange `json:"sections"`
	NormalizedCandidate        Profile         `json:"normalized_candidate"`
}

type ApplyResult struct {
	Path         string `json:"path"`
	PreviousHash string `json:"previous_hash"`
	ProfileHash  string `json:"profile_hash"`
	Changed      bool   `json:"changed"`
}

// Path returns the private profile path for a given ATL config directory.
func Path(configDir string) string { return filepath.Join(configDir, FileName) }

// MissingHash is the stable optimistic-concurrency identity of no profile.
func MissingHash() string { return hashBytes([]byte("atl-profile:none\n")) }

// DecodeStrict rejects unknown fields so misspelled policy or preference keys
// never appear accepted while being silently ignored.
func DecodeStrict(data []byte) (Profile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("%w: invalid profile JSON: %v", domain.ErrUsage, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Profile{}, fmt.Errorf("%w: profile contains trailing JSON values", domain.ErrUsage)
		}
		return Profile{}, fmt.Errorf("%w: invalid trailing profile JSON: %v", domain.ErrUsage, err)
	}
	normalize(&p)
	if err := Validate(p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Validate enforces provenance and confirmation boundaries.
func Validate(p Profile) error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported profile schema_version %d (want %d)", domain.ErrUsage, p.SchemaVersion, SchemaVersion)
	}
	if hasPreferences(p.Preferences) && !p.Preferences.Confirmed {
		return fmt.Errorf("%w: preferences.confirmed must be true when preferences are present", domain.ErrUsage)
	}
	for _, service := range p.Preferences.Services {
		if service != "jira" && service != "confluence" {
			return fmt.Errorf("%w: unknown preference service %q (want jira or confluence)", domain.ErrUsage, service)
		}
	}
	if p.TeamPolicy != nil {
		if strings.TrimSpace(p.TeamPolicy.Source) == "" {
			return fmt.Errorf("%w: team_policy.source is required; team policy must have explicit provenance", domain.ErrUsage)
		}
		for _, rule := range p.TeamPolicy.Rules {
			if strings.TrimSpace(rule) == "" {
				return fmt.Errorf("%w: team_policy.rules must not contain empty rules", domain.ErrUsage)
			}
		}
	}
	if len(p.Schema.JiraFields) > maxItems || len(p.Schema.ConfluenceSpaces) > maxItems ||
		len(p.Selectors.Jira) > maxItems || len(p.Selectors.Confluence) > maxItems {
		return fmt.Errorf("%w: profile section exceeds the %d-item limit", domain.ErrUsage, maxItems)
	}
	if err := validateJiraFacts(p.Schema.JiraFields); err != nil {
		return err
	}
	if err := validateSpaceFacts(p.Schema.ConfluenceSpaces); err != nil {
		return err
	}
	if err := validateSelectors(p.Selectors); err != nil {
		return err
	}
	if p.RenderDefaults != nil {
		if p.RenderDefaults.Jira != nil && !config.ValidProfile(p.RenderDefaults.Jira.Profile) {
			return fmt.Errorf("%w: invalid render_defaults Jira profile %q", domain.ErrUsage, p.RenderDefaults.Jira.Profile)
		}
		if p.RenderDefaults.Confluence != nil && !config.ValidProfile(p.RenderDefaults.Confluence.Profile) {
			return fmt.Errorf("%w: invalid render_defaults Confluence profile %q", domain.ErrUsage, p.RenderDefaults.Confluence.Profile)
		}
		if c := p.RenderDefaults.Confluence; c != nil &&
			(len(c.CustomFields) > 0 || len(c.FieldViews) > 0 || c.EpicField != "") {
			return fmt.Errorf("%w: Confluence render_defaults cannot contain Jira-only custom_fields, field_views, or epic_field", domain.ErrUsage)
		}
		if p.RenderDefaults.Jira != nil {
			for i, view := range p.RenderDefaults.Jira.FieldViews {
				if _, err := config.NormalizeJiraFieldView(view); err != nil {
					return fmt.Errorf("%w: invalid render_defaults Jira field_views[%d]: %v", domain.ErrUsage, i, err)
				}
			}
			if strings.ContainsAny(p.RenderDefaults.Jira.EpicField, "\r\n") {
				return fmt.Errorf("%w: render_defaults Jira epic_field must not contain line breaks", domain.ErrUsage)
			}
		}
	}
	return nil
}

func validateJiraFacts(facts []JiraFieldFact) error {
	seen := map[string]bool{}
	for _, fact := range facts {
		if fact.ID == "" || fact.Name == "" || fact.Source == "" || fact.VerifiedAt.IsZero() {
			return fmt.Errorf("%w: every jira field fact requires id, name, source, and verified_at", domain.ErrUsage)
		}
		if seen[fact.ID] {
			return fmt.Errorf("%w: duplicate jira field id %q", domain.ErrUsage, fact.ID)
		}
		seen[fact.ID] = true
	}
	return nil
}

func validateSpaceFacts(facts []ConfluenceSpaceFact) error {
	seen := map[string]bool{}
	for _, fact := range facts {
		if fact.Key == "" || fact.Source == "" || fact.VerifiedAt.IsZero() {
			return fmt.Errorf("%w: every confluence space fact requires key, source, and verified_at", domain.ErrUsage)
		}
		if seen[fact.Key] {
			return fmt.Errorf("%w: duplicate confluence space key %q", domain.ErrUsage, fact.Key)
		}
		seen[fact.Key] = true
	}
	return nil
}

func validateSelectors(selectors Selectors) error {
	seen := map[string]bool{}
	for _, selector := range selectors.Jira {
		if selector.Name == "" || selector.JQL == "" {
			return fmt.Errorf("%w: every Jira selector requires name and jql", domain.ErrUsage)
		}
		name := "jira:" + selector.Name
		if seen[name] {
			return fmt.Errorf("%w: duplicate Jira selector name %q", domain.ErrUsage, selector.Name)
		}
		seen[name] = true
	}
	for _, selector := range selectors.Confluence {
		if selector.Name == "" || selector.CQL == "" {
			return fmt.Errorf("%w: every Confluence selector requires name and cql", domain.ErrUsage)
		}
		name := "confluence:" + selector.Name
		if seen[name] {
			return fmt.Errorf("%w: duplicate Confluence selector name %q", domain.ErrUsage, selector.Name)
		}
		seen[name] = true
	}
	return nil
}

// Read loads and validates the current profile. A missing file is not an error.
func Read(configDir string) (Profile, bool, string, error) {
	data, exists, err := readRaw(configDir)
	if err != nil {
		return Profile{}, false, "", err
	}
	if !exists {
		return Profile{}, false, MissingHash(), nil
	}
	p, err := DecodeStrict(data)
	if err != nil {
		return Profile{}, false, "", fmt.Errorf("%w: invalid stored profile: %v", domain.ErrConfig, err)
	}
	hash, _, err := Canonical(p)
	return p, true, hash, err
}

func readRaw(configDir string) ([]byte, bool, error) {
	profilePath := Path(configDir)
	info, err := os.Lstat(profilePath)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Size() > MaxBytes {
		return nil, false, fmt.Errorf("%w: stored profile exceeds the %d MiB limit", domain.ErrConfig, MaxBytes>>20)
	}
	data, err := safepath.ReadFileWithin(configDir, profilePath)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Canonical returns the stable hash and pretty JSON for a normalized profile.
func Canonical(p Profile) (string, []byte, error) {
	normalize(&p)
	if err := Validate(p); err != nil {
		return "", nil, err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", nil, err
	}
	data = append(data, '\n')
	return hashBytes(data), data, nil
}

// BuildPreview validates a candidate without writing it.
func BuildPreview(configDir string, candidate Profile) (Preview, error) {
	current, exists, currentHash, migrationVersion, err := currentForPreview(configDir)
	if err != nil {
		return Preview{}, err
	}
	candidateHash, _, err := Canonical(candidate)
	if err != nil {
		return Preview{}, err
	}
	normalize(&candidate)
	sections := sectionChanges(current, exists, candidate)
	if migrationVersion != nil {
		sections = migrationSectionChanges()
	}
	return Preview{
		Path:                       Path(configDir),
		CurrentExists:              exists,
		CurrentHash:                currentHash,
		CandidateHash:              candidateHash,
		Changed:                    currentHash != candidateHash,
		MigrationFromSchemaVersion: migrationVersion,
		Sections:                   sections,
		NormalizedCandidate:        candidate,
	}, nil
}

// currentForPreview accepts a syntactically valid future-version profile only
// as opaque bytes. Its raw hash can authorize an exact replacement without
// interpreting unknown fields as today's schema. Ordinary Read remains strict.
func currentForPreview(configDir string) (Profile, bool, string, *int, error) {
	data, exists, err := readRaw(configDir)
	if err != nil {
		return Profile{}, false, "", nil, err
	}
	if !exists {
		return Profile{}, false, MissingHash(), nil, nil
	}
	p, err := DecodeStrict(data)
	if err == nil {
		hash, _, hashErr := Canonical(p)
		return p, true, hash, nil, hashErr
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if headerErr := json.Unmarshal(data, &header); headerErr == nil && header.SchemaVersion > SchemaVersion {
		version := header.SchemaVersion
		return Profile{}, true, hashBytes(data), &version, nil
	}
	return Profile{}, false, "", nil, fmt.Errorf("%w: invalid stored profile: %v", domain.ErrConfig, err)
}

// Apply writes only the exact candidate/current pair reviewed by Preview.
func Apply(configDir string, candidate Profile, expectedCandidateHash, expectedCurrentHash string) (ApplyResult, error) {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return ApplyResult{}, err
	}
	lockPath := filepath.Join(configDir, lockFileName)
	lock, acquired, err := safepath.TryLockFileWithin(configDir, lockPath, 0o600)
	if err != nil {
		return ApplyResult{}, err
	}
	if !acquired {
		return ApplyResult{}, fmt.Errorf("%w: another profile apply is in progress", domain.ErrCheckFailed)
	}
	defer func() { _ = lock.Unlock() }()

	preview, err := BuildPreview(configDir, candidate)
	if err != nil {
		return ApplyResult{}, err
	}
	if expectedCandidateHash == "" || expectedCurrentHash == "" {
		return ApplyResult{}, fmt.Errorf("%w: apply requires candidate and current hashes from profile preview", domain.ErrUsage)
	}
	if preview.CandidateHash != expectedCandidateHash {
		return ApplyResult{}, fmt.Errorf("%w: candidate changed since preview (got %s, previewed %s)", domain.ErrCheckFailed, preview.CandidateHash, expectedCandidateHash)
	}
	if preview.CurrentHash != expectedCurrentHash {
		return ApplyResult{}, fmt.Errorf("%w: current profile changed since preview (got %s, previewed %s)", domain.ErrVersionConflict, preview.CurrentHash, expectedCurrentHash)
	}
	if !preview.Changed {
		// Semantic no-op still repairs an owner-readable profile restored with
		// permissive modes. Avoid rewriting an already-private file.
		if info, statErr := os.Lstat(preview.Path); statErr != nil {
			return ApplyResult{}, statErr
		} else if info.Mode().Perm() != 0o600 {
			_, data, canonicalErr := Canonical(preview.NormalizedCandidate)
			if canonicalErr != nil {
				return ApplyResult{}, canonicalErr
			}
			if writeErr := safepath.WriteFileWithin(configDir, preview.Path, data, 0o600); writeErr != nil {
				return ApplyResult{}, writeErr
			}
		}
		return ApplyResult{Path: preview.Path, PreviousHash: preview.CurrentHash, ProfileHash: preview.CandidateHash}, nil
	}
	_, data, err := Canonical(preview.NormalizedCandidate)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := safepath.WriteFileWithin(configDir, preview.Path, data, 0o600); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		Path:         preview.Path,
		PreviousHash: preview.CurrentHash,
		ProfileHash:  preview.CandidateHash,
		Changed:      true,
	}, nil
}

func normalize(p *Profile) {
	for i := range p.Schema.JiraFields {
		fact := &p.Schema.JiraFields[i]
		fact.ID = strings.TrimSpace(fact.ID)
		fact.Name = strings.TrimSpace(fact.Name)
		fact.Type = strings.TrimSpace(fact.Type)
		fact.Source = strings.TrimSpace(fact.Source)
		fact.VerifiedAt = fact.VerifiedAt.UTC()
	}
	for i := range p.Schema.ConfluenceSpaces {
		fact := &p.Schema.ConfluenceSpaces[i]
		fact.Key = strings.TrimSpace(fact.Key)
		fact.Name = strings.TrimSpace(fact.Name)
		fact.Source = strings.TrimSpace(fact.Source)
		fact.VerifiedAt = fact.VerifiedAt.UTC()
	}
	p.Preferences.MirrorRoot = strings.TrimSpace(p.Preferences.MirrorRoot)
	p.Preferences.Services = uniqueSorted(p.Preferences.Services)
	if p.TeamPolicy != nil {
		p.TeamPolicy.Source = strings.TrimSpace(p.TeamPolicy.Source)
		for i := range p.TeamPolicy.Rules {
			p.TeamPolicy.Rules[i] = strings.TrimSpace(p.TeamPolicy.Rules[i])
		}
	}
	sort.Slice(p.Schema.JiraFields, func(i, j int) bool { return p.Schema.JiraFields[i].ID < p.Schema.JiraFields[j].ID })
	sort.Slice(p.Schema.ConfluenceSpaces, func(i, j int) bool { return p.Schema.ConfluenceSpaces[i].Key < p.Schema.ConfluenceSpaces[j].Key })
	for i := range p.Selectors.Jira {
		p.Selectors.Jira[i].Name = strings.TrimSpace(p.Selectors.Jira[i].Name)
		p.Selectors.Jira[i].JQL = strings.TrimSpace(p.Selectors.Jira[i].JQL)
		p.Selectors.Jira[i].Fields = uniqueSorted(p.Selectors.Jira[i].Fields)
	}
	sort.Slice(p.Selectors.Jira, func(i, j int) bool { return p.Selectors.Jira[i].Name < p.Selectors.Jira[j].Name })
	for i := range p.Selectors.Confluence {
		p.Selectors.Confluence[i].Name = strings.TrimSpace(p.Selectors.Confluence[i].Name)
		p.Selectors.Confluence[i].CQL = strings.TrimSpace(p.Selectors.Confluence[i].CQL)
	}
	sort.Slice(p.Selectors.Confluence, func(i, j int) bool { return p.Selectors.Confluence[i].Name < p.Selectors.Confluence[j].Name })
	if p.RenderDefaults != nil {
		if p.RenderDefaults.Jira != nil {
			p.RenderDefaults.Jira.Profile = strings.TrimSpace(p.RenderDefaults.Jira.Profile)
			p.RenderDefaults.Jira.EpicField = strings.TrimSpace(p.RenderDefaults.Jira.EpicField)
			for i, view := range p.RenderDefaults.Jira.FieldViews {
				if normalized, err := config.NormalizeJiraFieldView(view); err == nil {
					p.RenderDefaults.Jira.FieldViews[i] = normalized
				}
			}
			p.RenderDefaults.Jira.Include = uniqueSorted(p.RenderDefaults.Jira.Include)
			p.RenderDefaults.Jira.Exclude = uniqueSorted(p.RenderDefaults.Jira.Exclude)
			p.RenderDefaults.Jira.CustomFields = uniqueSorted(p.RenderDefaults.Jira.CustomFields)
		}
		if p.RenderDefaults.Confluence != nil {
			p.RenderDefaults.Confluence.Profile = strings.TrimSpace(p.RenderDefaults.Confluence.Profile)
			p.RenderDefaults.Confluence.Include = uniqueSorted(p.RenderDefaults.Confluence.Include)
			p.RenderDefaults.Confluence.Exclude = uniqueSorted(p.RenderDefaults.Confluence.Exclude)
		}
	}
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func hasPreferences(p Preferences) bool {
	return p.Confirmed || p.MirrorRoot != "" || len(p.Services) > 0
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sectionChanges(current Profile, exists bool, candidate Profile) []SectionChange {
	type section struct {
		name       string
		current    any
		candidate  any
		currentSet bool
		newSet     bool
	}
	sections := []section{
		{"schema", current.Schema, candidate.Schema, exists && !emptyJSON(current.Schema), !emptyJSON(candidate.Schema)},
		{"preferences", current.Preferences, candidate.Preferences, exists && hasPreferences(current.Preferences), hasPreferences(candidate.Preferences)},
		{"team_policy", current.TeamPolicy, candidate.TeamPolicy, exists && current.TeamPolicy != nil, candidate.TeamPolicy != nil},
		{"render_defaults", current.RenderDefaults, candidate.RenderDefaults, exists && current.RenderDefaults != nil, candidate.RenderDefaults != nil},
		{"selectors", current.Selectors, candidate.Selectors, exists && !emptyJSON(current.Selectors), !emptyJSON(candidate.Selectors)},
	}
	out := make([]SectionChange, 0, len(sections))
	for _, s := range sections {
		status := "unchanged"
		switch {
		case !s.currentSet && s.newSet:
			status = "added"
		case s.currentSet && !s.newSet:
			status = "removed"
		case s.currentSet && s.newSet && !jsonEqual(s.current, s.candidate):
			status = "changed"
		}
		out = append(out, SectionChange{Section: s.name, Status: status})
	}
	return out
}

func migrationSectionChanges() []SectionChange {
	sections := []string{"schema", "preferences", "team_policy", "render_defaults", "selectors"}
	out := make([]SectionChange, 0, len(sections))
	for _, section := range sections {
		out = append(out, SectionChange{Section: section, Status: "changed"})
	}
	return out
}

func emptyJSON(v any) bool {
	b, _ := json.Marshal(v)
	return string(b) == "{}" || string(b) == "null"
}

func jsonEqual(a, b any) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return bytes.Equal(aJSON, bJSON)
}
