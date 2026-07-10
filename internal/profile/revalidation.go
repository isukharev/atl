package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const revalidationFileName = "profile-revalidation.json"

const ObservationsFileSuffix = ".atl-observations.json"

type RevalidationBatch struct {
	SchemaVersion    int                    `json:"schema_version"`
	BaseProfileHash  string                 `json:"base_profile_hash"`
	CheckedAt        time.Time              `json:"checked_at"`
	JiraFields       []JiraFieldCheck       `json:"jira_fields,omitempty"`
	ConfluenceSpaces []ConfluenceSpaceCheck `json:"confluence_spaces,omitempty"`
}

type JiraFieldCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"` // verified|missing|failed
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
	Source string `json:"source"`
	Error  string `json:"error,omitempty"`
}

type ConfluenceSpaceCheck struct {
	Key    string `json:"key"`
	Status string `json:"status"` // verified|missing|failed
	Name   string `json:"name,omitempty"`
	Source string `json:"source"`
	Error  string `json:"error,omitempty"`
}

type RevalidationEntry struct {
	Service       string     `json:"service"`
	ID            string     `json:"id"`
	Name          string     `json:"name,omitempty"`
	Status        string     `json:"status"` // fresh|stale|verified_pending|missing|failed
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	Source        string     `json:"source,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type RevalidationResult struct {
	Path             string              `json:"path"`
	ObservationsHash string              `json:"observations_hash"`
	BaseProfileHash  string              `json:"base_profile_hash"`
	Entries          []RevalidationEntry `json:"entries"`
}

type RevalidationStatus struct {
	ProfileHash string              `json:"profile_hash"`
	StaleBefore time.Time           `json:"stale_before"`
	Entries     []RevalidationEntry `json:"entries"`
}

type revalidationState struct {
	SchemaVersion    int               `json:"schema_version"`
	JiraFields       []jiraCheckState  `json:"jira_fields,omitempty"`
	ConfluenceSpaces []spaceCheckState `json:"confluence_spaces,omitempty"`
}

type jiraCheckState struct {
	JiraFieldCheck
	CheckedAt time.Time `json:"checked_at"`
}

type spaceCheckState struct {
	ConfluenceSpaceCheck
	CheckedAt time.Time `json:"checked_at"`
}

func DecodeRevalidationBatchStrict(data []byte) (RevalidationBatch, error) {
	var batch RevalidationBatch
	if err := decodeStrictJSON(data, &batch, "revalidation batch"); err != nil {
		return RevalidationBatch{}, err
	}
	if batch.SchemaVersion != SuggestionSchemaVersion {
		return RevalidationBatch{}, fmt.Errorf("%w: unsupported revalidation schema_version %d", domain.ErrUsage, batch.SchemaVersion)
	}
	batch.BaseProfileHash = strings.TrimSpace(batch.BaseProfileHash)
	batch.CheckedAt = batch.CheckedAt.UTC()
	if !validHash(batch.BaseProfileHash) || batch.CheckedAt.IsZero() {
		return RevalidationBatch{}, fmt.Errorf("%w: revalidation requires base_profile_hash and checked_at", domain.ErrUsage)
	}
	if len(batch.JiraFields) > maxItems || len(batch.ConfluenceSpaces) > maxItems {
		return RevalidationBatch{}, fmt.Errorf("%w: revalidation section exceeds the %d-item limit", domain.ErrUsage, maxItems)
	}
	seen := map[string]bool{}
	for i := range batch.JiraFields {
		check := &batch.JiraFields[i]
		check.ID = strings.TrimSpace(check.ID)
		check.Name = strings.TrimSpace(check.Name)
		check.Type = strings.TrimSpace(check.Type)
		check.Source = strings.TrimSpace(check.Source)
		check.Error = strings.TrimSpace(check.Error)
		if err := validateRevalidationCheck("Jira field", check.ID, check.Status, check.Name, check.Source, check.Error); err != nil {
			return RevalidationBatch{}, err
		}
		key := "jira:" + check.ID
		if seen[key] {
			return RevalidationBatch{}, fmt.Errorf("%w: duplicate Jira field check %q", domain.ErrUsage, check.ID)
		}
		seen[key] = true
	}
	for i := range batch.ConfluenceSpaces {
		check := &batch.ConfluenceSpaces[i]
		check.Key = strings.TrimSpace(check.Key)
		check.Name = strings.TrimSpace(check.Name)
		check.Source = strings.TrimSpace(check.Source)
		check.Error = strings.TrimSpace(check.Error)
		if err := validateRevalidationCheck("Confluence space", check.Key, check.Status, check.Name, check.Source, check.Error); err != nil {
			return RevalidationBatch{}, err
		}
		key := "confluence:" + check.Key
		if seen[key] {
			return RevalidationBatch{}, fmt.Errorf("%w: duplicate Confluence space check %q", domain.ErrUsage, check.Key)
		}
		seen[key] = true
	}
	sort.Slice(batch.JiraFields, func(i, j int) bool { return batch.JiraFields[i].ID < batch.JiraFields[j].ID })
	sort.Slice(batch.ConfluenceSpaces, func(i, j int) bool { return batch.ConfluenceSpaces[i].Key < batch.ConfluenceSpaces[j].Key })
	return batch, nil
}

func validateRevalidationCheck(kind, id, status, name, source, checkError string) error {
	if id == "" || source == "" {
		return fmt.Errorf("%w: %s check requires id/key and source", domain.ErrUsage, kind)
	}
	if strings.ContainsAny(id+name+source+checkError, "\r\n") {
		return fmt.Errorf("%w: %s check values must be single-line", domain.ErrUsage, kind)
	}
	switch status {
	case "verified":
		if name == "" || checkError != "" {
			return fmt.Errorf("%w: verified %s check requires name and no error", domain.ErrUsage, kind)
		}
	case "missing":
		if checkError != "" {
			return fmt.Errorf("%w: missing %s check must not carry error", domain.ErrUsage, kind)
		}
	case "failed":
		if checkError == "" {
			return fmt.Errorf("%w: failed %s check requires error", domain.ErrUsage, kind)
		}
	default:
		return fmt.Errorf("%w: invalid %s check status %q (want verified|missing|failed)", domain.ErrUsage, kind, status)
	}
	return nil
}

// ApplyRevalidation records explicit check outcomes outside profile.json and
// writes verified facts as an observations artifact for the normal suggestion
// pipeline. Missing/failed checks never delete the last verified fact.
func ApplyRevalidation(configDir, out string, batch RevalidationBatch) (RevalidationResult, error) {
	encoded, err := json.Marshal(batch)
	if err != nil {
		return RevalidationResult{}, err
	}
	batch, err = DecodeRevalidationBatchStrict(encoded)
	if err != nil {
		return RevalidationResult{}, err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return RevalidationResult{}, err
	}
	lock, acquired, err := safepath.TryLockFileWithin(configDir, filepath.Join(configDir, lockFileName), 0o600)
	if err != nil {
		return RevalidationResult{}, err
	}
	if !acquired {
		return RevalidationResult{}, fmt.Errorf("%w: another profile decision is in progress", domain.ErrCheckFailed)
	}
	defer func() { _ = lock.Unlock() }()

	_, _, currentHash, err := Read(configDir)
	if err != nil {
		return RevalidationResult{}, err
	}
	if currentHash != batch.BaseProfileHash {
		return RevalidationResult{}, fmt.Errorf("%w: profile changed since revalidation reads", domain.ErrVersionConflict)
	}
	state, err := readRevalidationState(configDir)
	if err != nil {
		return RevalidationResult{}, err
	}
	if err := rejectOlderRevalidation(state, batch); err != nil {
		return RevalidationResult{}, err
	}

	observations := Observations{SchemaVersion: SuggestionSchemaVersion, BaseProfileHash: currentHash}
	entries := make([]RevalidationEntry, 0, len(batch.JiraFields)+len(batch.ConfluenceSpaces))
	for _, check := range batch.JiraFields {
		entry := RevalidationEntry{Service: "jira", ID: check.ID, Name: check.Name, Status: check.Status, LastCheckedAt: timePtr(batch.CheckedAt), Source: check.Source, Error: check.Error}
		entries = append(entries, entry)
		if check.Status == "verified" {
			observations.Schema.JiraFields = append(observations.Schema.JiraFields, JiraFieldFact{
				ID: check.ID, Name: check.Name, Type: check.Type, Source: check.Source, VerifiedAt: batch.CheckedAt,
			})
		}
	}
	for _, check := range batch.ConfluenceSpaces {
		entry := RevalidationEntry{Service: "confluence", ID: check.Key, Name: check.Name, Status: check.Status, LastCheckedAt: timePtr(batch.CheckedAt), Source: check.Source, Error: check.Error}
		entries = append(entries, entry)
		if check.Status == "verified" {
			observations.Schema.ConfluenceSpaces = append(observations.Schema.ConfluenceSpaces, ConfluenceSpaceFact{
				Key: check.Key, Name: check.Name, Source: check.Source, VerifiedAt: batch.CheckedAt,
			})
		}
	}

	if err := WriteObservations(out, observations); err != nil {
		return RevalidationResult{}, err
	}
	state = mergeRevalidationState(state, batch)
	if err := writeRevalidationState(configDir, state); err != nil {
		return RevalidationResult{}, err
	}
	hash, _, err := CanonicalObservations(observations)
	if err != nil {
		return RevalidationResult{}, err
	}
	sortRevalidationEntries(entries)
	return RevalidationResult{Path: out, ObservationsHash: hash, BaseProfileHash: currentHash, Entries: entries}, nil
}

func RevalidationStatusFor(configDir string, staleBefore time.Time, service string) (RevalidationStatus, error) {
	if staleBefore.IsZero() {
		return RevalidationStatus{}, fmt.Errorf("%w: stale-before is required", domain.ErrUsage)
	}
	if service != "" && service != "jira" && service != "confluence" {
		return RevalidationStatus{}, fmt.Errorf("%w: invalid service %q", domain.ErrUsage, service)
	}
	profile, exists, hash, err := Read(configDir)
	if err != nil {
		return RevalidationStatus{}, err
	}
	if !exists {
		profile = Profile{SchemaVersion: SchemaVersion}
	}
	state, err := readRevalidationState(configDir)
	if err != nil {
		return RevalidationStatus{}, err
	}
	entries := buildRevalidationStatus(profile, state, staleBefore.UTC(), service)
	return RevalidationStatus{ProfileHash: hash, StaleBefore: staleBefore.UTC(), Entries: entries}, nil
}

func buildRevalidationStatus(profile Profile, state revalidationState, staleBefore time.Time, service string) []RevalidationEntry {
	entries := []RevalidationEntry{}
	seen := map[string]bool{}
	fieldState := map[string]jiraCheckState{}
	for _, check := range state.JiraFields {
		fieldState[check.ID] = check
	}
	if service == "" || service == "jira" {
		for _, fact := range profile.Schema.JiraFields {
			seen["jira:"+fact.ID] = true
			entry := factStatus("jira", fact.ID, fact.Name, fact.Source, fact.VerifiedAt, staleBefore)
			if check, ok := fieldState[fact.ID]; ok && check.CheckedAt.After(fact.VerifiedAt) {
				applyCheckState(&entry, check.Status, check.Source, check.Error, check.CheckedAt)
			}
			entries = append(entries, entry)
		}
		for _, check := range state.JiraFields {
			if !seen["jira:"+check.ID] {
				entry := RevalidationEntry{Service: "jira", ID: check.ID, Name: check.Name}
				applyCheckState(&entry, check.Status, check.Source, check.Error, check.CheckedAt)
				entries = append(entries, entry)
			}
		}
	}
	spaceState := map[string]spaceCheckState{}
	for _, check := range state.ConfluenceSpaces {
		spaceState[check.Key] = check
	}
	if service == "" || service == "confluence" {
		for _, fact := range profile.Schema.ConfluenceSpaces {
			seen["confluence:"+fact.Key] = true
			entry := factStatus("confluence", fact.Key, fact.Name, fact.Source, fact.VerifiedAt, staleBefore)
			if check, ok := spaceState[fact.Key]; ok && check.CheckedAt.After(fact.VerifiedAt) {
				applyCheckState(&entry, check.Status, check.Source, check.Error, check.CheckedAt)
			}
			entries = append(entries, entry)
		}
		for _, check := range state.ConfluenceSpaces {
			if !seen["confluence:"+check.Key] {
				entry := RevalidationEntry{Service: "confluence", ID: check.Key, Name: check.Name}
				applyCheckState(&entry, check.Status, check.Source, check.Error, check.CheckedAt)
				entries = append(entries, entry)
			}
		}
	}
	sortRevalidationEntries(entries)
	return entries
}

func factStatus(service, id, name, source string, verifiedAt, staleBefore time.Time) RevalidationEntry {
	status := "fresh"
	if verifiedAt.Before(staleBefore) {
		status = "stale"
	}
	verified := verifiedAt.UTC()
	return RevalidationEntry{Service: service, ID: id, Name: name, Status: status, VerifiedAt: &verified, Source: source}
}

func applyCheckState(entry *RevalidationEntry, status, source, checkError string, checkedAt time.Time) {
	if status == "verified" {
		status = "verified_pending"
	}
	entry.Status = status
	entry.Source = source
	entry.Error = checkError
	entry.LastCheckedAt = timePtr(checkedAt)
}

func mergeRevalidationState(state revalidationState, batch RevalidationBatch) revalidationState {
	fields := map[string]jiraCheckState{}
	for _, check := range state.JiraFields {
		fields[check.ID] = check
	}
	for _, check := range batch.JiraFields {
		if prior, ok := fields[check.ID]; !ok || !batch.CheckedAt.Before(prior.CheckedAt) {
			fields[check.ID] = jiraCheckState{JiraFieldCheck: check, CheckedAt: batch.CheckedAt}
		}
	}
	state.JiraFields = state.JiraFields[:0]
	for _, check := range fields {
		state.JiraFields = append(state.JiraFields, check)
	}
	spaces := map[string]spaceCheckState{}
	for _, check := range state.ConfluenceSpaces {
		spaces[check.Key] = check
	}
	for _, check := range batch.ConfluenceSpaces {
		if prior, ok := spaces[check.Key]; !ok || !batch.CheckedAt.Before(prior.CheckedAt) {
			spaces[check.Key] = spaceCheckState{ConfluenceSpaceCheck: check, CheckedAt: batch.CheckedAt}
		}
	}
	state.ConfluenceSpaces = state.ConfluenceSpaces[:0]
	for _, check := range spaces {
		state.ConfluenceSpaces = append(state.ConfluenceSpaces, check)
	}
	sort.Slice(state.JiraFields, func(i, j int) bool { return state.JiraFields[i].ID < state.JiraFields[j].ID })
	sort.Slice(state.ConfluenceSpaces, func(i, j int) bool { return state.ConfluenceSpaces[i].Key < state.ConfluenceSpaces[j].Key })
	return state
}

func rejectOlderRevalidation(state revalidationState, batch RevalidationBatch) error {
	fieldStates := map[string]jiraCheckState{}
	for _, check := range state.JiraFields {
		fieldStates[check.ID] = check
	}
	for _, check := range batch.JiraFields {
		if prior, ok := fieldStates[check.ID]; ok {
			if batch.CheckedAt.Before(prior.CheckedAt) {
				return fmt.Errorf("%w: Jira field %q was already checked at %s", domain.ErrVersionConflict, check.ID, prior.CheckedAt.Format(time.RFC3339))
			}
			if batch.CheckedAt.Equal(prior.CheckedAt) && !jsonEqual(prior.JiraFieldCheck, check) {
				return fmt.Errorf("%w: Jira field %q has conflicting outcomes at the same check time", domain.ErrCheckFailed, check.ID)
			}
		}
	}
	spaceStates := map[string]spaceCheckState{}
	for _, check := range state.ConfluenceSpaces {
		spaceStates[check.Key] = check
	}
	for _, check := range batch.ConfluenceSpaces {
		if prior, ok := spaceStates[check.Key]; ok {
			if batch.CheckedAt.Before(prior.CheckedAt) {
				return fmt.Errorf("%w: Confluence space %q was already checked at %s", domain.ErrVersionConflict, check.Key, prior.CheckedAt.Format(time.RFC3339))
			}
			if batch.CheckedAt.Equal(prior.CheckedAt) && !jsonEqual(prior.ConfluenceSpaceCheck, check) {
				return fmt.Errorf("%w: Confluence space %q has conflicting outcomes at the same check time", domain.ErrCheckFailed, check.Key)
			}
		}
	}
	return nil
}

func readRevalidationState(configDir string) (revalidationState, error) {
	path := filepath.Join(configDir, revalidationFileName)
	data, exists, err := readPrivateState(configDir, path, "revalidation state")
	if err != nil {
		return revalidationState{}, err
	}
	if !exists {
		return revalidationState{SchemaVersion: SuggestionSchemaVersion}, nil
	}
	var state revalidationState
	if err := decodeStrictJSON(data, &state, "revalidation state"); err != nil {
		return revalidationState{}, fmt.Errorf("%w: invalid revalidation state: %v", domain.ErrConfig, err)
	}
	if state.SchemaVersion != SuggestionSchemaVersion {
		return revalidationState{}, fmt.Errorf("%w: unsupported revalidation state schema_version %d", domain.ErrConfig, state.SchemaVersion)
	}
	for _, check := range state.JiraFields {
		if check.CheckedAt.IsZero() {
			return revalidationState{}, fmt.Errorf("%w: Jira revalidation state has zero checked_at", domain.ErrConfig)
		}
		if err := validateRevalidationCheck("Jira field", check.ID, check.Status, check.Name, check.Source, check.Error); err != nil {
			return revalidationState{}, fmt.Errorf("%w: invalid Jira revalidation state: %v", domain.ErrConfig, err)
		}
	}
	for _, check := range state.ConfluenceSpaces {
		if check.CheckedAt.IsZero() {
			return revalidationState{}, fmt.Errorf("%w: Confluence revalidation state has zero checked_at", domain.ErrConfig)
		}
		if err := validateRevalidationCheck("Confluence space", check.Key, check.Status, check.Name, check.Source, check.Error); err != nil {
			return revalidationState{}, fmt.Errorf("%w: invalid Confluence revalidation state: %v", domain.ErrConfig, err)
		}
	}
	return state, nil
}

func writeRevalidationState(configDir string, state revalidationState) error {
	state.SchemaVersion = SuggestionSchemaVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > MaxBytes {
		return fmt.Errorf("%w: revalidation state would exceed the %d MiB limit", domain.ErrCheckFailed, MaxBytes>>20)
	}
	return safepath.WriteFileWithin(configDir, filepath.Join(configDir, revalidationFileName), append(data, '\n'), 0o600)
}

func CanonicalObservations(observations Observations) (string, []byte, error) {
	data, err := json.Marshal(observations)
	if err != nil {
		return "", nil, err
	}
	observations, err = DecodeObservationsStrict(data)
	if err != nil {
		return "", nil, err
	}
	data, err = json.MarshalIndent(observations, "", "  ")
	if err != nil {
		return "", nil, err
	}
	data = append(data, '\n')
	return hashBytes(data), data, nil
}

func WriteObservations(path string, observations Observations) error {
	_, data, err := CanonicalObservations(observations)
	if err != nil {
		return err
	}
	if len(data) > MaxBytes {
		return fmt.Errorf("%w: generated observations exceed the %d MiB limit", domain.ErrCheckFailed, MaxBytes>>20)
	}
	if !strings.HasSuffix(filepath.Base(path), ObservationsFileSuffix) {
		return fmt.Errorf("%w: observations output must end in %s", domain.ErrUsage, ObservationsFileSuffix)
	}
	if err := safepath.WriteFileAtomicPrivate(path, data, 0o600); err != nil {
		if errors.Is(err, safepath.ErrUnsafePrivatePath) {
			return fmt.Errorf("%w: %v", domain.ErrUsage, err)
		}
		return err
	}
	return nil
}

func sortRevalidationEntries(entries []RevalidationEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Service != entries[j].Service {
			return entries[i].Service < entries[j].Service
		}
		return entries[i].ID < entries[j].ID
	})
}

func timePtr(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}
