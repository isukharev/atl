package profile

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	SuggestionSchemaVersion = 1
	SuggestionFileSuffix    = ".atl-suggestion.json"
	decisionsFileName       = "profile-decisions.json"
)

// Observations is explicit, caller-authored evidence used to propose profile
// changes. Team policy is deliberately absent and strict decoding rejects it.
type Observations struct {
	SchemaVersion   int                  `json:"schema_version"`
	BaseProfileHash string               `json:"base_profile_hash"`
	Schema          SchemaFacts          `json:"schema,omitempty"`
	Preferences     *PreferenceProposal  `json:"preferences,omitempty"`
	RenderDefaults  *config.RenderConfig `json:"render_defaults,omitempty"`
	Selectors       Selectors            `json:"selectors,omitempty"`
	Evidence        []Evidence           `json:"evidence,omitempty"`
}

type PreferenceProposal struct {
	Services   *[]string `json:"services,omitempty"`
	MirrorRoot *string   `json:"mirror_root,omitempty"`
}

type Evidence struct {
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	Reason     string    `json:"reason"`
}

type Suggestion struct {
	SchemaVersion   int        `json:"schema_version"`
	BaseProfileHash string     `json:"base_profile_hash"`
	SuggestionHash  string     `json:"suggestion_hash"`
	Candidate       Profile    `json:"candidate"`
	Evidence        []Evidence `json:"evidence,omitempty"`
}

type SuggestResult struct {
	Path               string `json:"path"`
	SuggestionHash     string `json:"suggestion_hash"`
	BaseProfileHash    string `json:"base_profile_hash"`
	PreviouslyRejected bool   `json:"previously_rejected"`
}

type SuggestionReview struct {
	SuggestionHash     string     `json:"suggestion_hash"`
	PreviouslyRejected bool       `json:"previously_rejected"`
	Evidence           []Evidence `json:"evidence,omitempty"`
	Preview            Preview    `json:"preview"`
}

type SuggestionApplyResult struct {
	SuggestionHash string      `json:"suggestion_hash"`
	Profile        ApplyResult `json:"profile"`
}

type SuggestionRejectResult struct {
	SuggestionHash string `json:"suggestion_hash"`
	Status         string `json:"status"`
	Changed        bool   `json:"changed"`
	Path           string `json:"path"`
}

type decisions struct {
	SchemaVersion int      `json:"schema_version"`
	Rejected      []string `json:"rejected,omitempty"`
}

func DecodeObservationsStrict(data []byte) (Observations, error) {
	var observations Observations
	if err := decodeStrictJSON(data, &observations, "observations"); err != nil {
		return Observations{}, err
	}
	if observations.SchemaVersion != SuggestionSchemaVersion {
		return Observations{}, fmt.Errorf("%w: unsupported observations schema_version %d (want %d)", domain.ErrUsage, observations.SchemaVersion, SuggestionSchemaVersion)
	}
	observations.BaseProfileHash = strings.TrimSpace(observations.BaseProfileHash)
	if !validHash(observations.BaseProfileHash) {
		return Observations{}, fmt.Errorf("%w: base_profile_hash must be a 64-character lowercase SHA-256", domain.ErrUsage)
	}
	if observations.Preferences != nil {
		if observations.Preferences.Services != nil {
			services := uniqueSorted(*observations.Preferences.Services)
			observations.Preferences.Services = &services
		}
		if observations.Preferences.MirrorRoot != nil {
			root := strings.TrimSpace(*observations.Preferences.MirrorRoot)
			observations.Preferences.MirrorRoot = &root
		}
	}
	normalizeEvidence(observations.Evidence)
	if hasNonSchemaProposals(observations) && len(observations.Evidence) == 0 {
		return Observations{}, fmt.Errorf("%w: preference, render, or selector proposals require evidence", domain.ErrUsage)
	}
	for _, evidence := range observations.Evidence {
		if evidence.Source == "" || evidence.Reason == "" || evidence.ObservedAt.IsZero() {
			return Observations{}, fmt.Errorf("%w: every evidence item requires source, observed_at, and reason", domain.ErrUsage)
		}
		if strings.ContainsAny(evidence.Source+evidence.Reason, "\r\n") {
			return Observations{}, fmt.Errorf("%w: evidence source and reason must be single-line", domain.ErrUsage)
		}
	}
	// Reuse profile validation for fact provenance and render/selector shape.
	probe := Profile{SchemaVersion: SchemaVersion, Schema: observations.Schema, Selectors: observations.Selectors, RenderDefaults: observations.RenderDefaults}
	if observations.Preferences != nil {
		probe.Preferences = Preferences{Confirmed: true}
		if observations.Preferences.Services != nil {
			probe.Preferences.Services = *observations.Preferences.Services
		}
		if observations.Preferences.MirrorRoot != nil {
			probe.Preferences.MirrorRoot = *observations.Preferences.MirrorRoot
		}
	}
	_, normalized, err := Canonical(probe)
	if err != nil {
		return Observations{}, err
	}
	probe, err = DecodeStrict(normalized)
	if err != nil {
		return Observations{}, err
	}
	observations.Schema = probe.Schema
	observations.Selectors = probe.Selectors
	observations.RenderDefaults = probe.RenderDefaults
	if observations.Preferences != nil {
		if observations.Preferences.Services != nil {
			services := append([]string(nil), probe.Preferences.Services...)
			observations.Preferences.Services = &services
		}
		if observations.Preferences.MirrorRoot != nil {
			root := probe.Preferences.MirrorRoot
			observations.Preferences.MirrorRoot = &root
		}
	}
	return observations, nil
}

// BuildSuggestion deterministically merges explicit observations into the exact
// current profile. It does not write profile or decision state.
func BuildSuggestion(configDir string, observations Observations) (Suggestion, bool, error) {
	encoded, err := json.Marshal(observations)
	if err != nil {
		return Suggestion{}, false, err
	}
	observations, err = DecodeObservationsStrict(encoded)
	if err != nil {
		return Suggestion{}, false, err
	}
	current, exists, currentHash, err := Read(configDir)
	if err != nil {
		return Suggestion{}, false, err
	}
	if observations.BaseProfileHash != currentHash {
		return Suggestion{}, false, fmt.Errorf("%w: profile changed since observations (got %s, observed %s)", domain.ErrVersionConflict, currentHash, observations.BaseProfileHash)
	}
	if !exists {
		current = Profile{SchemaVersion: SchemaVersion}
	}
	candidate, err := mergeObservations(current, observations)
	if err != nil {
		return Suggestion{}, false, err
	}
	suggestion := Suggestion{
		SchemaVersion:   SuggestionSchemaVersion,
		BaseProfileHash: currentHash,
		Candidate:       candidate,
		Evidence:        append([]Evidence(nil), observations.Evidence...),
	}
	hash, _, err := canonicalSuggestion(suggestion)
	if err != nil {
		return Suggestion{}, false, err
	}
	suggestion.SuggestionHash = hash
	rejected, err := WasRejected(configDir, hash)
	return suggestion, rejected, err
}

func mergeObservations(current Profile, observations Observations) (Profile, error) {
	candidate := current
	fields := make(map[string]JiraFieldFact, len(candidate.Schema.JiraFields))
	for _, fact := range candidate.Schema.JiraFields {
		fields[fact.ID] = fact
	}
	for _, observed := range observations.Schema.JiraFields {
		if prior, ok := fields[observed.ID]; ok {
			switch {
			case observed.VerifiedAt.Before(prior.VerifiedAt):
				continue
			case observed.VerifiedAt.Equal(prior.VerifiedAt) && !jsonEqual(prior, observed):
				return Profile{}, fmt.Errorf("%w: Jira field %q has conflicting facts at the same verification time", domain.ErrCheckFailed, observed.ID)
			}
		}
		fields[observed.ID] = observed
	}
	candidate.Schema.JiraFields = candidate.Schema.JiraFields[:0]
	for _, fact := range fields {
		candidate.Schema.JiraFields = append(candidate.Schema.JiraFields, fact)
	}

	spaces := make(map[string]ConfluenceSpaceFact, len(candidate.Schema.ConfluenceSpaces))
	for _, fact := range candidate.Schema.ConfluenceSpaces {
		spaces[fact.Key] = fact
	}
	for _, observed := range observations.Schema.ConfluenceSpaces {
		if prior, ok := spaces[observed.Key]; ok {
			switch {
			case observed.VerifiedAt.Before(prior.VerifiedAt):
				continue
			case observed.VerifiedAt.Equal(prior.VerifiedAt) && !jsonEqual(prior, observed):
				return Profile{}, fmt.Errorf("%w: Confluence space %q has conflicting facts at the same verification time", domain.ErrCheckFailed, observed.Key)
			}
		}
		spaces[observed.Key] = observed
	}
	candidate.Schema.ConfluenceSpaces = candidate.Schema.ConfluenceSpaces[:0]
	for _, fact := range spaces {
		candidate.Schema.ConfluenceSpaces = append(candidate.Schema.ConfluenceSpaces, fact)
	}

	if observations.Preferences != nil {
		candidate.Preferences.Confirmed = true
		if observations.Preferences.Services != nil {
			candidate.Preferences.Services = append([]string(nil), (*observations.Preferences.Services)...)
		}
		if observations.Preferences.MirrorRoot != nil {
			candidate.Preferences.MirrorRoot = *observations.Preferences.MirrorRoot
		}
	}
	if observations.RenderDefaults != nil {
		if candidate.RenderDefaults == nil {
			candidate.RenderDefaults = &config.RenderConfig{}
		}
		if observations.RenderDefaults.Jira != nil {
			candidate.RenderDefaults.Jira = observations.RenderDefaults.Jira
		}
		if observations.RenderDefaults.Confluence != nil {
			candidate.RenderDefaults.Confluence = observations.RenderDefaults.Confluence
		}
	}
	candidate.Selectors.Jira = mergeJiraSelectors(candidate.Selectors.Jira, observations.Selectors.Jira)
	candidate.Selectors.Confluence = mergeConfluenceSelectors(candidate.Selectors.Confluence, observations.Selectors.Confluence)

	_, canonical, err := Canonical(candidate)
	if err != nil {
		return Profile{}, err
	}
	return DecodeStrict(canonical)
}

func mergeJiraSelectors(current, observed []JiraSelector) []JiraSelector {
	byName := make(map[string]JiraSelector, len(current)+len(observed))
	for _, selector := range current {
		byName[selector.Name] = selector
	}
	for _, selector := range observed {
		byName[selector.Name] = selector
	}
	out := make([]JiraSelector, 0, len(byName))
	for _, selector := range byName {
		out = append(out, selector)
	}
	return out
}

func mergeConfluenceSelectors(current, observed []ConfluenceSelector) []ConfluenceSelector {
	byName := make(map[string]ConfluenceSelector, len(current)+len(observed))
	for _, selector := range current {
		byName[selector.Name] = selector
	}
	for _, selector := range observed {
		byName[selector.Name] = selector
	}
	out := make([]ConfluenceSelector, 0, len(byName))
	for _, selector := range byName {
		out = append(out, selector)
	}
	return out
}

func WriteSuggestion(path string, suggestion Suggestion) error {
	if err := ValidateSuggestion(suggestion); err != nil {
		return err
	}
	_, data, err := canonicalSuggestionWithHash(suggestion)
	if err != nil {
		return err
	}
	if len(data) > MaxBytes {
		return fmt.Errorf("%w: generated suggestion exceeds the %d MiB limit", domain.ErrCheckFailed, MaxBytes>>20)
	}
	if !strings.HasSuffix(filepath.Base(path), SuggestionFileSuffix) {
		return fmt.Errorf("%w: suggestion output must end in %s", domain.ErrUsage, SuggestionFileSuffix)
	}
	if err := safepath.WriteFileAtomicPrivate(path, data, 0o600); err != nil {
		if errors.Is(err, safepath.ErrUnsafePrivatePath) {
			return fmt.Errorf("%w: %v", domain.ErrUsage, err)
		}
		return err
	}
	return nil
}

func DecodeSuggestionStrict(data []byte) (Suggestion, error) {
	var suggestion Suggestion
	if err := decodeStrictJSON(data, &suggestion, "suggestion"); err != nil {
		return Suggestion{}, err
	}
	if err := ValidateSuggestion(suggestion); err != nil {
		return Suggestion{}, err
	}
	return suggestion, nil
}

func ValidateSuggestion(suggestion Suggestion) error {
	if suggestion.SchemaVersion != SuggestionSchemaVersion {
		return fmt.Errorf("%w: unsupported suggestion schema_version %d (want %d)", domain.ErrUsage, suggestion.SchemaVersion, SuggestionSchemaVersion)
	}
	if !validHash(suggestion.BaseProfileHash) || !validHash(suggestion.SuggestionHash) {
		return fmt.Errorf("%w: suggestion hashes must be 64-character lowercase SHA-256 values", domain.ErrUsage)
	}
	if err := Validate(suggestion.Candidate); err != nil {
		return err
	}
	for _, evidence := range suggestion.Evidence {
		if strings.TrimSpace(evidence.Source) == "" || strings.TrimSpace(evidence.Reason) == "" || evidence.ObservedAt.IsZero() {
			return fmt.Errorf("%w: invalid suggestion evidence", domain.ErrUsage)
		}
		if strings.ContainsAny(evidence.Source+evidence.Reason, "\r\n") {
			return fmt.Errorf("%w: suggestion evidence must be single-line", domain.ErrUsage)
		}
	}
	expected, _, err := canonicalSuggestion(suggestion)
	if err != nil {
		return err
	}
	if expected != suggestion.SuggestionHash {
		return fmt.Errorf("%w: suggestion content hash mismatch (got %s, want %s)", domain.ErrCheckFailed, suggestion.SuggestionHash, expected)
	}
	return nil
}

func ReviewSuggestion(configDir string, suggestion Suggestion) (SuggestionReview, error) {
	if err := ValidateSuggestion(suggestion); err != nil {
		return SuggestionReview{}, err
	}
	preview, err := BuildPreview(configDir, suggestion.Candidate)
	if err != nil {
		return SuggestionReview{}, err
	}
	if preview.CurrentHash != suggestion.BaseProfileHash {
		return SuggestionReview{}, fmt.Errorf("%w: profile changed since suggestion (got %s, based on %s)", domain.ErrVersionConflict, preview.CurrentHash, suggestion.BaseProfileHash)
	}
	rejected, err := WasRejected(configDir, suggestion.SuggestionHash)
	if err != nil {
		return SuggestionReview{}, err
	}
	return SuggestionReview{
		SuggestionHash: suggestion.SuggestionHash, PreviouslyRejected: rejected,
		Evidence: append([]Evidence(nil), suggestion.Evidence...), Preview: preview,
	}, nil
}

func ApplySuggestion(configDir string, suggestion Suggestion, expectedSuggestionHash, expectedCandidateHash, expectedCurrentHash string) (SuggestionApplyResult, error) {
	if err := ValidateSuggestion(suggestion); err != nil {
		return SuggestionApplyResult{}, err
	}
	if expectedSuggestionHash == "" || suggestion.SuggestionHash != expectedSuggestionHash {
		return SuggestionApplyResult{}, fmt.Errorf("%w: suggestion changed since review", domain.ErrCheckFailed)
	}
	if suggestion.BaseProfileHash != expectedCurrentHash {
		return SuggestionApplyResult{}, fmt.Errorf("%w: expected current hash does not match suggestion base", domain.ErrCheckFailed)
	}
	result, err := Apply(configDir, suggestion.Candidate, expectedCandidateHash, expectedCurrentHash)
	if err != nil {
		return SuggestionApplyResult{}, err
	}
	return SuggestionApplyResult{SuggestionHash: suggestion.SuggestionHash, Profile: result}, nil
}

func RejectSuggestion(configDir string, suggestion Suggestion, expectedSuggestionHash string) (SuggestionRejectResult, error) {
	if err := ValidateSuggestion(suggestion); err != nil {
		return SuggestionRejectResult{}, err
	}
	if expectedSuggestionHash == "" || suggestion.SuggestionHash != expectedSuggestionHash {
		return SuggestionRejectResult{}, fmt.Errorf("%w: suggestion changed since review", domain.ErrCheckFailed)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return SuggestionRejectResult{}, err
	}
	lockPath := filepath.Join(configDir, lockFileName)
	lock, acquired, err := safepath.TryLockFileWithin(configDir, lockPath, 0o600)
	if err != nil {
		return SuggestionRejectResult{}, err
	}
	if !acquired {
		return SuggestionRejectResult{}, fmt.Errorf("%w: another profile decision is in progress", domain.ErrCheckFailed)
	}
	defer func() { _ = lock.Unlock() }()

	state, err := readDecisions(configDir)
	if err != nil {
		return SuggestionRejectResult{}, err
	}
	for _, hash := range state.Rejected {
		if hash == suggestion.SuggestionHash {
			if err := writeDecisions(configDir, state); err != nil { // repairs mode
				return SuggestionRejectResult{}, err
			}
			return SuggestionRejectResult{SuggestionHash: hash, Status: "rejected", Path: decisionsPath(configDir)}, nil
		}
	}
	state.Rejected = append(state.Rejected, suggestion.SuggestionHash)
	sort.Strings(state.Rejected)
	if err := writeDecisions(configDir, state); err != nil {
		return SuggestionRejectResult{}, err
	}
	return SuggestionRejectResult{SuggestionHash: suggestion.SuggestionHash, Status: "rejected", Changed: true, Path: decisionsPath(configDir)}, nil
}

func WasRejected(configDir, suggestionHash string) (bool, error) {
	state, err := readDecisions(configDir)
	if err != nil {
		return false, err
	}
	i := sort.SearchStrings(state.Rejected, suggestionHash)
	return i < len(state.Rejected) && state.Rejected[i] == suggestionHash, nil
}

func decisionsPath(configDir string) string { return filepath.Join(configDir, decisionsFileName) }

func readDecisions(configDir string) (decisions, error) {
	path := decisionsPath(configDir)
	data, exists, err := readPrivateState(configDir, path, "profile decisions")
	if err != nil {
		return decisions{}, err
	}
	if !exists {
		return decisions{SchemaVersion: SuggestionSchemaVersion}, nil
	}
	var state decisions
	if err := decodeStrictJSON(data, &state, "profile decisions"); err != nil {
		return decisions{}, fmt.Errorf("%w: invalid profile decisions: %v", domain.ErrConfig, err)
	}
	if state.SchemaVersion != SuggestionSchemaVersion {
		return decisions{}, fmt.Errorf("%w: unsupported profile decisions schema_version %d", domain.ErrConfig, state.SchemaVersion)
	}
	state.Rejected = uniqueSorted(state.Rejected)
	for _, hash := range state.Rejected {
		if !validHash(hash) {
			return decisions{}, fmt.Errorf("%w: invalid rejected suggestion hash", domain.ErrConfig)
		}
	}
	return state, nil
}

func readPrivateState(configDir, path, label string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Size() > MaxBytes {
		return nil, false, fmt.Errorf("%w: %s exceeds the %d MiB limit", domain.ErrConfig, label, MaxBytes>>20)
	}
	data, err := safepath.ReadFileWithin(configDir, path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func writeDecisions(configDir string, state decisions) error {
	state.SchemaVersion = SuggestionSchemaVersion
	state.Rejected = uniqueSorted(state.Rejected)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > MaxBytes {
		return fmt.Errorf("%w: profile decisions would exceed the %d MiB limit", domain.ErrCheckFailed, MaxBytes>>20)
	}
	return safepath.WriteFileWithin(configDir, decisionsPath(configDir), append(data, '\n'), 0o600)
}

func canonicalSuggestion(suggestion Suggestion) (string, []byte, error) {
	suggestion.SuggestionHash = ""
	suggestion.Evidence = append([]Evidence(nil), suggestion.Evidence...)
	_, candidate, err := Canonical(suggestion.Candidate)
	if err != nil {
		return "", nil, err
	}
	suggestion.Candidate, err = DecodeStrict(candidate)
	if err != nil {
		return "", nil, err
	}
	normalizeEvidence(suggestion.Evidence)
	data, err := json.MarshalIndent(suggestion, "", "  ")
	if err != nil {
		return "", nil, err
	}
	data = append(data, '\n')
	return hashBytes(data), data, nil
}

func canonicalSuggestionWithHash(suggestion Suggestion) (string, []byte, error) {
	hash, _, err := canonicalSuggestion(suggestion)
	if err != nil {
		return "", nil, err
	}
	if suggestion.SuggestionHash != hash {
		return "", nil, fmt.Errorf("%w: suggestion content hash mismatch", domain.ErrCheckFailed)
	}
	raw, err := json.Marshal(suggestion)
	if err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(raw, &suggestion); err != nil {
		return "", nil, err
	}
	normalize(&suggestion.Candidate)
	normalizeEvidence(suggestion.Evidence)
	data, err := json.MarshalIndent(suggestion, "", "  ")
	if err != nil {
		return "", nil, err
	}
	return hash, append(data, '\n'), nil
}

func normalizeEvidence(evidence []Evidence) {
	for i := range evidence {
		evidence[i].Source = strings.TrimSpace(evidence[i].Source)
		evidence[i].Reason = strings.TrimSpace(evidence[i].Reason)
		evidence[i].ObservedAt = evidence[i].ObservedAt.UTC()
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].Source != evidence[j].Source {
			return evidence[i].Source < evidence[j].Source
		}
		if !evidence[i].ObservedAt.Equal(evidence[j].ObservedAt) {
			return evidence[i].ObservedAt.Before(evidence[j].ObservedAt)
		}
		return evidence[i].Reason < evidence[j].Reason
	})
}

func hasNonSchemaProposals(observations Observations) bool {
	return observations.Preferences != nil || observations.RenderDefaults != nil ||
		len(observations.Selectors.Jira) > 0 || len(observations.Selectors.Confluence) > 0
}

func validHash(hash string) bool {
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func decodeStrictJSON(data []byte, target any, label string) error {
	if len(data) > MaxBytes {
		return fmt.Errorf("%w: %s exceeds the %d MiB limit", domain.ErrUsage, label, MaxBytes>>20)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid %s JSON: %v", domain.ErrUsage, label, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: %s contains trailing JSON values", domain.ErrUsage, label)
		}
		return fmt.Errorf("%w: invalid trailing %s JSON: %v", domain.ErrUsage, label, err)
	}
	return nil
}
