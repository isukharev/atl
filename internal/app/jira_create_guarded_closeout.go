package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/jiramap"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const guardedCreateRegistrationOwnerLock = ".atl-jira-create-registration.lock"

func validateGuardedCreateReadback(snapshot *jiraGuardedCreateSnapshot, ack domain.JiraGuardedCreateAcknowledgement, readback domain.JiraGuardedCreateReadback) error {
	if snapshot == nil || ack.ID == "" || readback.ID != ack.ID || !domain.ValidConfluenceContentID(readback.ID) || !domain.ValidJiraIssueKey(readback.Key) || ack.Key != "" && readback.Key != ack.Key {
		return fmt.Errorf("%w: Jira create readback did not preserve acknowledgement identity", domain.ErrCheckFailed)
	}
	if !strings.HasPrefix(readback.Key, snapshot.project.Key+"-") || readback.ProjectID != snapshot.project.ID || readback.ProjectKey != snapshot.project.Key || readback.IssueTypeID != snapshot.metadata.IssueType.ID || readback.Summary != snapshot.resultSummary() {
		return fmt.Errorf("%w: Jira create readback moved or changed the reviewed core fields", domain.ErrCheckFailed)
	}
	expected, err := guardedCreatePayloadFields(snapshot.prepared.Payload)
	if err != nil {
		return err
	}
	for _, field := range snapshot.readFields {
		if evidence, ok := readback.Fields[field]; !ok || !evidence.Present {
			return fmt.Errorf("%w: Jira create readback omitted a requested field", domain.ErrCheckFailed)
		}
	}
	description, submitted := expected["description"]
	if !readback.Description.Present || !submitted && readback.Description.Value != nil || submitted && !guardedCreateValueMatches(description, readback.Description.Value) {
		return fmt.Errorf("%w: Jira create readback did not prove the submitted description", domain.ErrCheckFailed)
	}
	for _, field := range snapshot.result.Fields {
		evidence := readback.Fields[field.FieldID]
		if !evidence.Present || !guardedCreateValueMatches(expected[field.FieldID], evidence.Value) {
			return fmt.Errorf("%w: Jira create readback did not prove a supplied typed field", domain.ErrCheckFailed)
		}
	}
	created, createdOK := readback.Created.Value.(string)
	updated, updatedOK := readback.Updated.Value.(string)
	if !readback.Created.Present || !readback.Updated.Present || !createdOK || !updatedOK {
		return fmt.Errorf("%w: Jira create readback omitted qualified timestamps", domain.ErrCheckFailed)
	}
	createdAt, err := parseJiraHistoryTime(created)
	if err != nil {
		return fmt.Errorf("%w: Jira create readback created timestamp is invalid", domain.ErrCheckFailed)
	}
	updatedAt, err := parseJiraHistoryTime(updated)
	if err != nil || updatedAt.Before(createdAt) {
		return fmt.Errorf("%w: Jira create readback updated timestamp is invalid", domain.ErrCheckFailed)
	}
	return nil
}

func (snapshot *jiraGuardedCreateSnapshot) resultSummary() string {
	fields, err := guardedCreatePayloadFields(snapshot.prepared.Payload)
	if err != nil {
		return ""
	}
	value, _ := fields["summary"].(string)
	return value
}

func guardedCreatePayloadFields(payload []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var envelope struct {
		Fields map[string]any `json:"fields"`
	}
	if err := decoder.Decode(&envelope); err != nil || envelope.Fields == nil {
		return nil, fmt.Errorf("%w: prepared Jira create payload cannot be compared", domain.ErrCheckFailed)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: prepared Jira create payload has trailing data", domain.ErrCheckFailed)
	}
	return envelope.Fields, nil
}

// guardedCreateValueMatches applies recursive object containment while keeping
// arrays and scalars exact. Jira may enrich submitted objects on readback but
// may not drop or alter any reviewed member.
func guardedCreateValueMatches(expected, actual any) bool {
	switch expected := expected.(type) {
	case map[string]any:
		actual, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range expected {
			got, present := actual[key]
			if !present || !guardedCreateValueMatches(value, got) {
				return false
			}
		}
		return true
	case []any:
		actual, ok := actual.([]any)
		if !ok || len(expected) != len(actual) {
			return false
		}
		for index := range expected {
			if !guardedCreateValueMatches(expected[index], actual[index]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(expected, actual)
	}
}

func (s *JiraService) prepareGuardedCreateRegistration(root string) (*jiraGuardedCreateStage, error) {
	if err := validateCreatedRegistrationPlatform(runtime.GOOS); err != nil {
		return nil, err
	}
	root, err := createdRegistrationRoot(root)
	if err != nil {
		return nil, err
	}
	stage := &jiraGuardedCreateStage{
		root: root, mirror: mirror.New(root), registration: newRegistration(root),
		effects: &JiraGuardedCreateRegistrationEffects{PlannedFiles: guardedCreateRegistrationPlannedFiles(), ActualFiles: []string{}},
	}
	ownerLock, ownerCreated, err := lockGuardedCreateRegistrationOwner(root)
	if err != nil {
		return stage, err
	}
	stage.locks = append(stage.locks, ownerLock)
	if ownerCreated {
		stage.recordActual("<mirror-parent>/" + guardedCreateRegistrationOwnerLock)
	}
	gitignore := filepath.Join(root, ".gitignore")
	gitignoreExisted := guardedCreatePathExists(root, gitignore)
	if err := stage.mirror.EnsureScaffold(); err != nil {
		return stage, err
	}
	if !gitignoreExisted {
		stage.recordActual(".gitignore")
	}
	if err := qualifyGuardedCreatePrivacyGuard(root); err != nil {
		return stage, err
	}
	internalLockPath := jiraPendingFieldsLockPath(root)
	internalLockExisted := guardedCreatePathExists(root, internalLockPath)
	lock, err := lockJiraPendingFields(root, "create-registration")
	if err != nil {
		return stage, err
	}
	stage.locks = append(stage.locks, lock)
	if !internalLockExisted {
		stage.recordActual(".atl/pending/jira/.mirror.lock")
	}
	if _, err := stage.mirror.SyncStates(); err != nil {
		return stage, err
	}
	bindingLockPath := filepath.Join(root, ".atl", "backend-bindings.lock")
	bindingLockExisted := guardedCreatePathExists(root, bindingLockPath)
	want, err := backendBinding("jira", s.baseURL)
	if err != nil {
		return stage, err
	}
	guard, err := stage.mirror.BeginBackendBindingPopulation(want, wikiExt)
	if !bindingLockExisted && guardedCreatePathExists(root, bindingLockPath) {
		stage.recordActual(".atl/backend-bindings.lock")
	}
	if err != nil {
		return stage, err
	}
	stage.binding = guard
	stage.locks = append(stage.locks, guard)
	return stage, nil
}

func lockGuardedCreateRegistrationOwner(root string) (*safepath.FileLock, bool, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, false, err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return nil, false, fmt.Errorf("%w: mirror root must not be a filesystem root", domain.ErrUsage)
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%w: mirror root parent must be an existing directory", domain.ErrCheckFailed)
	}
	path := filepath.Join(parent, guardedCreateRegistrationOwnerLock)
	_, statErr := os.Lstat(path)
	created := os.IsNotExist(statErr)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, false, statErr
	}
	lock, acquired, err := safepath.TryLockFileWithin(parent, path, 0o600)
	if err != nil {
		return nil, false, err
	}
	if !acquired {
		return nil, false, fmt.Errorf("%w: another Jira create registration is active", domain.ErrCheckFailed)
	}
	lockInfo, err := os.Lstat(path)
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		_ = lock.Unlock()
		return nil, false, fmt.Errorf("%w: Jira create registration coordination lock must be an owner-only regular file", domain.ErrCheckFailed)
	}
	return lock, created, nil
}

func qualifyGuardedCreatePrivacyGuard(root string) error {
	path := filepath.Join(root, ".gitignore")
	info, err := safepath.StatWithin(root, path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: mirror .gitignore privacy guard must be a regular file", domain.ErrCheckFailed)
	}
	data, err := safepath.ReadFileWithinLimit(root, path, 1<<20)
	if err != nil {
		return fmt.Errorf("%w: read mirror .gitignore privacy guard", domain.ErrCheckFailed)
	}
	lines := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	for _, required := range []string{".atl/", "credentials.json", "*.pat"} {
		if !lines[required] {
			return fmt.Errorf("%w: mirror .gitignore does not contain the required privacy guards", domain.ErrCheckFailed)
		}
	}
	return nil
}

func guardedCreatePathExists(root, path string) bool {
	_, err := safepath.StatWithin(root, path)
	return err == nil
}

func (s *jiraGuardedCreateStage) recordActual(path string) {
	if s == nil || s.effects == nil || path == "" {
		return
	}
	for _, existing := range s.effects.ActualFiles {
		if existing == path {
			return
		}
	}
	s.effects.ActualFiles = append(s.effects.ActualFiles, path)
	sort.Strings(s.effects.ActualFiles)
}

func (s *JiraService) finishGuardedCreateRegistration(ctx context.Context, cancel context.CancelFunc, stage *jiraGuardedCreateStage, snapshot *jiraGuardedCreateSnapshot, readback domain.JiraGuardedCreateReadback) (*CreatedMirrorRegistration, error) {
	registration := stage.registration
	registration.ReadbackReconciled = true
	if err := ctx.Err(); err != nil {
		_, registration, failure := jiraRegistrationFailure(guardedCreateIssue(snapshot, readback), registration, "local_registration_failed", err)
		return registration, failure
	}
	bindingPath := filepath.Join(stage.root, ".atl", "backend-bindings.json")
	bindingExisted := guardedCreatePathExists(stage.root, bindingPath)
	created, err := stage.binding.Commit()
	if err != nil {
		if !bindingExisted && guardedCreatePathExists(stage.root, bindingPath) {
			stage.recordActual(".atl/backend-bindings.json")
		}
		_, registration, failure := jiraRegistrationFailure(guardedCreateIssue(snapshot, readback), registration, "local_registration_failed", err)
		return registration, failure
	}
	if created && !bindingExisted {
		stage.recordActual(".atl/backend-bindings.json")
	}
	issue := guardedCreateIssue(snapshot, readback)
	projectSegment, keySegment := safepath.Segment(issue.Project), safepath.Segment(issue.Key)
	if projectSegment != issue.Project || keySegment != issue.Key {
		_, registration, failure := jiraRegistrationFailure(issue, registration, "readback_unqualified", domain.ErrCheckFailed)
		return registration, failure
	}
	dir := filepath.Join(stage.root, projectSegment)
	if err := qualifyNewJiraRegistrationTarget(stage.mirror, stage.root, dir, keySegment); err != nil {
		_, registration, failure := jiraRegistrationFailure(issue, registration, "target_collision", err)
		return registration, failure
	}
	snapshotBytes, err := json.MarshalIndent(JiraIssueSnapshot{Key: issue.Key, ID: issue.ID, Fields: issue.Fields}, "", "  ")
	if err != nil {
		_, registration, failure := jiraRegistrationFailure(issue, registration, "local_registration_failed", err)
		return registration, failure
	}
	wikiRel, err := mirror.PublicArtifactPathWithin(stage.root, filepath.Join(dir, keySegment+wikiExt))
	if err != nil {
		_, registration, failure := jiraRegistrationFailure(issue, registration, "local_registration_failed", err)
		return registration, failure
	}
	mdRel, err := mirror.PublicArtifactPathWithin(stage.root, filepath.Join(dir, keySegment+".md"))
	if err != nil {
		_, registration, failure := jiraRegistrationFailure(issue, registration, "local_registration_failed", err)
		return registration, failure
	}
	jsonRel, err := mirror.PublicArtifactPathWithin(stage.root, filepath.Join(dir, keySegment+".json"))
	if err != nil {
		_, registration, failure := jiraRegistrationFailure(issue, registration, "local_registration_failed", err)
		return registration, failure
	}
	body := []byte(issue.Body)
	artifacts := []mirror.RegistrationArtifact{
		{Path: wikiRel, Data: body, Mode: 0o644},
		{Path: mdRel, Data: renderIssueMarkdown(issue, nil, snapshot.render), Mode: 0o644},
		{Path: jsonRel, Data: append(snapshotBytes, '\n'), Mode: 0o644},
	}
	identity, err := jiraSyncIdentity(issue.ID, nil)
	if err != nil {
		_, registration, failure := jiraRegistrationFailure(issue, registration, "local_registration_failed", err)
		return registration, failure
	}
	state := mirror.SyncState{ID: keySegment, Identity: identity, Hash: mirror.Hash(body), Path: wikiRel.String()}
	view := viewStateOf(snapshot.render)
	stateLockPath := filepath.Join(stage.root, ".atl", "state.lock")
	statePath := filepath.Join(stage.root, ".atl", "state.json")
	stateLockExisted := guardedCreatePathExists(stage.root, stateLockPath)
	stateExisted := guardedCreatePathExists(stage.root, statePath)
	s.guardedCreateRegistrationBoundary("before_register_new", cancel)
	if err := ctx.Err(); err != nil {
		_, registration, failure := jiraRegistrationFailure(issue, registration, "local_registration_failed", err)
		return registration, failure
	}
	if err := stage.mirror.RegisterNew(state, view, wikiExt, body, artifacts); err != nil {
		if !stateLockExisted && guardedCreatePathExists(stage.root, stateLockPath) {
			stage.recordActual(".atl/state.lock")
		}
		if !stateExisted && guardedCreatePathExists(stage.root, statePath) {
			stage.recordActual(".atl/state.json")
		}
		if committedErr := classifyGuardedCreateRegistrationCommitFailure(stage.mirror, registration, state, body, artifacts); committedErr != nil {
			return registration, committedErr
		}
		_, registration, failure := jiraRegistrationFailure(issue, registration, "local_registration_failed", err)
		return registration, failure
	}
	s.guardedCreateRegistrationBoundary("after_register_new", cancel)
	if !stateLockExisted {
		stage.recordActual(".atl/state.lock")
	}
	if !stateExisted {
		stage.recordActual(".atl/state.json")
	}
	registration.Status = "registered"
	registration.Path = wikiRel.String()
	registration.SHA256 = state.Hash
	return registration, nil
}

func classifyGuardedCreateRegistrationCommitFailure(m *mirror.Mirror, registration *CreatedMirrorRegistration, state mirror.SyncState, body []byte, artifacts []mirror.RegistrationArtifact) error {
	if !guardedCreateRegistrationCommitted(m, state, body, artifacts) {
		return nil
	}
	registration.Status = "registration_outcome_unknown"
	registration.Path = state.Path
	registration.SHA256 = state.Hash
	registration.Reason = "local_registration_durability_unknown"
	registration.Recovery = "preserve local files; inspect the emitted issue key in the emitted registration root because registration state and bytes may already be complete; do not pull or repeat issue create"
	return fmt.Errorf("%w: mirror registration state and bytes may already be committed, but local durability was not proved; do not replay create", domain.ErrCheckFailed)
}

func guardedCreateRegistrationCommitted(m *mirror.Mirror, state mirror.SyncState, body []byte, artifacts []mirror.RegistrationArtifact) bool {
	got, present, err := m.SyncStateOf(state.ID)
	if err != nil || !present || got != state {
		return false
	}
	base, present, err := m.ReadBaseBodyExt(state.ID, wikiExt)
	if err != nil || !present || !bytes.Equal(base, body) {
		return false
	}
	for _, artifact := range artifacts {
		data, err := safepath.ReadFileWithin(m.Root, filepath.Join(m.Root, filepath.FromSlash(artifact.Path.String())))
		if err != nil || !bytes.Equal(data, artifact.Data) {
			return false
		}
	}
	return true
}

func guardedCreateIssue(snapshot *jiraGuardedCreateSnapshot, readback domain.JiraGuardedCreateReadback) *domain.Issue {
	fields := make(map[string]any, len(readback.Fields))
	for key, evidence := range readback.Fields {
		if evidence.Present {
			fields[key] = evidence.Value
		}
	}
	issue := jiramap.Issue(readback.ID, readback.Key, fields)
	issue.Summary = readback.Summary
	issue.Type = snapshot.metadata.IssueType.Name
	issue.Project = readback.ProjectKey
	issue.Body, _ = readback.Description.Value.(string)
	return issue
}
