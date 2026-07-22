package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const jiraMirrorSnapshotSchemaVersion = 1

// JiraMirrorSnapshot is a content-free, deterministic health inventory of one
// durable Jira mirror. Detailed issue identities and bytes remain available
// through status and the mirror itself.
type JiraMirrorSnapshot struct {
	SchemaVersion   int                      `json:"schema_version"`
	Service         string                   `json:"service"`
	RemoteRequested bool                     `json:"remote_requested"`
	Complete        bool                     `json:"complete"`
	Reconciled      bool                     `json:"reconciled"`
	Local           JiraMirrorLocalSummary   `json:"local"`
	Native          JiraMirrorNativeSummary  `json:"native"`
	Snapshot        JiraMirrorRawSummary     `json:"snapshot"`
	Pending         JiraMirrorPendingSummary `json:"pending"`
	Render          JiraMirrorRenderSummary  `json:"render"`
	Remote          JiraMirrorRemoteSummary  `json:"remote"`
}

type JiraMirrorLocalSummary struct {
	Present       int  `json:"present"`
	Clean         int  `json:"clean"`
	LocallyEdited int  `json:"locally_edited"`
	Tracked       int  `json:"tracked"`
	Untracked     int  `json:"untracked"`
	NonCanonical  int  `json:"non_canonical"`
	Reconciled    bool `json:"reconciled"`
}

type JiraMirrorNativeSummary struct {
	Total              int  `json:"total"`
	Unchanged          int  `json:"unchanged"`
	Modified           int  `json:"modified"`
	Removed            int  `json:"removed"`
	Untracked          int  `json:"untracked"`
	NonCanonical       int  `json:"non_canonical"`
	MissingBaseline    int  `json:"missing_baseline"`
	BaselineMismatch   int  `json:"baseline_mismatch"`
	Unreadable         int  `json:"unreadable"`
	BaselinePresent    int  `json:"baseline_present"`
	BaselineMissing    int  `json:"baseline_missing"`
	BaselineUnreadable int  `json:"baseline_unreadable"`
	BaselineValid      int  `json:"baseline_valid"`
	BaselineInvalid    int  `json:"baseline_invalid"`
	Reconciled         bool `json:"reconciled"`
}

type JiraMirrorRawSummary struct {
	Expected      int  `json:"expected"`
	Present       int  `json:"present"`
	Missing       int  `json:"missing"`
	Readable      int  `json:"readable"`
	Unreadable    int  `json:"unreadable"`
	Valid         int  `json:"valid"`
	Invalid       int  `json:"invalid"`
	KeyMatched    int  `json:"key_matched"`
	KeyMismatched int  `json:"key_mismatched"`
	Reconciled    bool `json:"reconciled"`
}

type JiraMirrorPendingSummary struct {
	Total              int  `json:"total"`
	Valid              int  `json:"valid"`
	Invalid            int  `json:"invalid"`
	Unreadable         int  `json:"unreadable"`
	Bound              int  `json:"bound"`
	Unbound            int  `json:"unbound"`
	FieldEdits         int  `json:"field_edits"`
	ActiveTransactions int  `json:"active_transactions"`
	Reconciled         bool `json:"reconciled"`
}

type JiraMirrorRenderSummary struct {
	Expected           int  `json:"expected"`
	Present            int  `json:"present"`
	Missing            int  `json:"missing"`
	Current            int  `json:"current"`
	Legacy             int  `json:"legacy"`
	MissingMarker      int  `json:"missing_marker"`
	Unsupported        int  `json:"unsupported"`
	Unreadable         int  `json:"unreadable"`
	StateRecorded      int  `json:"state_recorded"`
	StateMissing       int  `json:"state_missing"`
	RendererCompatible bool `json:"renderer_compatible"`
	Reconciled         bool `json:"reconciled"`
}

type JiraMirrorRemoteSummary struct {
	Requested    bool `json:"requested"`
	Eligible     int  `json:"eligible"`
	Attempted    int  `json:"attempted"`
	NotAttempted int  `json:"not_attempted"`
	Checked      int  `json:"checked"`
	InSync       int  `json:"in_sync"`
	Drifted      int  `json:"drifted"`
	Unavailable  int  `json:"unavailable"`
	Reconciled   bool `json:"reconciled"`
}

type jiraMirrorLocalEvidence struct {
	local     *mirror.LocalWiki
	canonical bool
	baseline  []byte
	eligible  bool
	pending   *JiraPendingFields
}

// SnapshotJiraMirror inspects a mirror without config, credentials, network
// access, recovery, locks, or writes.
func SnapshotJiraMirror(dir string) (*JiraMirrorSnapshot, error) {
	result, _, err := inspectJiraMirror(dir)
	return result, err
}

// PreflightJiraMirrorRemoteSnapshot records a remote request after completing
// the write-free local inspection. The CLI calls this before backend setup.
func PreflightJiraMirrorRemoteSnapshot(dir string) (*JiraMirrorSnapshot, error) {
	result, _, err := inspectJiraMirror(dir)
	if result != nil {
		result.RemoteRequested = true
		result.Remote.Requested = true
		finalizeJiraMirrorSnapshot(result)
	}
	return result, err
}

// SnapshotMirror optionally adds one single-attempt remote issue read per
// eligible canonical substrate. Local integrity failures stop before network.
func (s *JiraService) SnapshotMirror(ctx context.Context, dir string, checkRemote bool) (*JiraMirrorSnapshot, error) {
	result, locals, localErr := inspectJiraMirror(dir)
	if result == nil || !checkRemote {
		return result, localErr
	}
	result.RemoteRequested = true
	result.Remote.Requested = true
	if localErr != nil || !result.Complete || !result.Reconciled {
		finalizeJiraMirrorSnapshot(result)
		return result, localErr
	}
	if s.tr == nil {
		return result, fmt.Errorf("%w: remote mirror snapshot requires a configured Jira backend", domain.ErrConfig)
	}
	probeContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	for _, evidence := range locals {
		if !evidence.eligible {
			continue
		}
		result.Remote.Attempted++
		fields := []string{"description"}
		if evidence.pending != nil {
			fields = append(fields, jiraPendingFieldIDs(evidence.pending)...)
		}
		issue, err := s.tr.GetIssue(probeContext, evidence.local.Key, fields)
		if err != nil || issue == nil || issue.Key != evidence.local.Key {
			result.Remote.Unavailable++
			continue
		}
		result.Remote.Checked++
		drifted := mirror.Hash([]byte(issue.Body)) != mirror.Hash(evidence.baseline) &&
			mirror.Hash([]byte(issue.Body)) != evidence.local.Current
		if evidence.pending != nil {
			for _, field := range evidence.pending.Fields {
				remote, valid := jiraSnapshotStringField(issue.Fields, field.ID)
				if !valid || (remote != field.Base && remote != field.Value) {
					drifted = true
					break
				}
			}
		}
		if drifted {
			result.Remote.Drifted++
		} else {
			result.Remote.InSync++
		}
	}
	finalizeJiraMirrorSnapshot(result)
	return result, nil
}

func inspectJiraMirror(dir string) (*JiraMirrorSnapshot, []*jiraMirrorLocalEvidence, error) {
	if dir == "" {
		dir = "mirror-jira"
	}
	m := mirror.New(dir)
	locals, err := m.ListWiki()
	if err != nil {
		return nil, nil, contentFreeJiraSnapshotError(err)
	}
	result := &JiraMirrorSnapshot{
		SchemaVersion: jiraMirrorSnapshotSchemaVersion,
		Service:       "jira",
		Complete:      true,
		Native:        JiraMirrorNativeSummary{Total: len(locals)},
		Snapshot:      JiraMirrorRawSummary{Expected: len(locals)},
		Render:        JiraMirrorRenderSummary{Expected: len(locals)},
	}
	evidence := make([]*jiraMirrorLocalEvidence, 0, len(locals))
	canonicalByKey := make(map[string]*jiraMirrorLocalEvidence, len(locals))
	keys := make([]string, 0, len(locals))
	var snapshotErr error
	for _, local := range locals {
		rel, relErr := filepath.Rel(dir, local.Path)
		canonical := relErr == nil && local.Synced != nil && filepath.Clean(rel) == filepath.Clean(local.Synced.Path)
		item := &jiraMirrorLocalEvidence{local: local, canonical: canonical}
		evidence = append(evidence, item)
		keys = append(keys, local.Key)
		result.Local.Present++
		if local.Dirty {
			result.Local.LocallyEdited++
		} else {
			result.Local.Clean++
		}
		switch {
		case canonical:
			result.Local.Tracked++
			canonicalByKey[local.Key] = item
		case local.Synced != nil:
			result.Local.Untracked++
			result.Local.NonCanonical++
			result.Native.NonCanonical++
			result.Native.BaselineMissing++
		default:
			result.Local.Untracked++
			result.Native.Untracked++
			result.Native.BaselineMissing++
		}
		if canonical {
			base, present, baseErr := m.ReadBaseBodyExt(local.Key, wikiExt)
			switch {
			case baseErr != nil:
				result.Native.Unreadable++
				result.Native.BaselineUnreadable++
				result.Complete = false
				snapshotErr = moreSevereErr(snapshotErr, fmt.Errorf("%w: one or more Jira mirror baselines are unreadable", domain.ErrCheckFailed))
			case !present:
				result.Native.MissingBaseline++
				result.Native.BaselineMissing++
			case mirror.Hash(base) != local.Synced.Hash:
				result.Native.BaselineMismatch++
				result.Native.BaselinePresent++
				result.Native.BaselineInvalid++
				result.Complete = false
				snapshotErr = moreSevereErr(snapshotErr, fmt.Errorf("%w: one or more Jira mirror baselines do not match tracked state", domain.ErrCheckFailed))
			default:
				item.baseline = base
				item.eligible = true
				result.Remote.Eligible++
				result.Native.BaselinePresent++
				result.Native.BaselineValid++
				if local.Dirty {
					result.Native.Modified++
				} else {
					result.Native.Unchanged++
				}
			}
		}
		inspectJiraRawSnapshot(result, dir, local)
	}
	states, err := m.SyncStates()
	if err != nil {
		return nil, nil, contentFreeJiraSnapshotError(err)
	}
	for _, state := range states {
		if filepath.Ext(state.Path) != wikiExt || canonicalByKey[state.ID] != nil {
			continue
		}
		result.Native.Total++
		result.Native.Removed++
		base, present, baseErr := m.ReadBaseBodyExt(state.ID, wikiExt)
		switch {
		case baseErr != nil:
			result.Native.BaselineUnreadable++
			result.Complete = false
			snapshotErr = moreSevereErr(snapshotErr, fmt.Errorf("%w: one or more Jira mirror baselines are unreadable", domain.ErrCheckFailed))
		case !present:
			result.Native.BaselineMissing++
		case mirror.Hash(base) != state.Hash:
			result.Native.BaselinePresent++
			result.Native.BaselineInvalid++
			result.Complete = false
			snapshotErr = moreSevereErr(snapshotErr, fmt.Errorf("%w: one or more Jira mirror baselines do not match tracked state", domain.ErrCheckFailed))
		default:
			result.Native.BaselinePresent++
			result.Native.BaselineValid++
		}
	}
	views, err := m.ViewStatesOf(keys)
	if err != nil {
		return nil, nil, contentFreeJiraSnapshotError(err)
	}
	for _, item := range evidence {
		if _, ok := views[item.local.Key]; ok && item.canonical {
			result.Render.StateRecorded++
		} else {
			result.Render.StateMissing++
		}
		mdPath := strings.TrimSuffix(item.local.Path, wikiExt) + ".md"
		body, readErr := safepath.ReadFileWithin(dir, mdPath)
		switch {
		case os.IsNotExist(readErr):
			result.Render.Missing++
		case readErr != nil:
			result.Render.Unreadable++
			result.Complete = false
			snapshotErr = moreSevereErr(snapshotErr, fmt.Errorf("%w: one or more Jira derived views are unreadable", domain.ErrCheckFailed))
		default:
			result.Render.Present++
			switch jiraViewMarkerClass(body) {
			case "current":
				result.Render.Current++
			case "legacy":
				result.Render.Legacy++
			case "unsupported":
				result.Render.Unsupported++
			default:
				result.Render.MissingMarker++
			}
		}
	}
	pendingByKey, pendingErr := inspectJiraPendingRecords(dir, result, canonicalByKey)
	if pendingErr != nil {
		result.Complete = false
		snapshotErr = moreSevereErr(snapshotErr, pendingErr)
	}
	for key, pending := range pendingByKey {
		if item := canonicalByKey[key]; item != nil {
			item.pending = pending
		}
	}
	if result.Snapshot.Unreadable > 0 || result.Snapshot.Invalid > 0 || result.Snapshot.KeyMismatched > 0 {
		result.Complete = false
		snapshotErr = moreSevereErr(snapshotErr, fmt.Errorf("%w: one or more Jira raw snapshots are unreadable, invalid, or misbound", domain.ErrCheckFailed))
	}
	finalizeJiraMirrorSnapshot(result)
	return result, evidence, contentFreeJiraSnapshotError(snapshotErr)
}

func inspectJiraRawSnapshot(result *JiraMirrorSnapshot, root string, local *mirror.LocalWiki) {
	path := strings.TrimSuffix(local.Path, wikiExt) + ".json"
	body, err := safepath.ReadFileWithin(root, path)
	switch {
	case os.IsNotExist(err):
		result.Snapshot.Missing++
		return
	case err != nil:
		result.Snapshot.Present++
		result.Snapshot.Unreadable++
		return
	}
	result.Snapshot.Present++
	result.Snapshot.Readable++
	var snapshot JiraIssueSnapshot
	if json.Unmarshal(body, &snapshot) != nil || snapshot.Key == "" {
		result.Snapshot.Invalid++
		return
	}
	result.Snapshot.Valid++
	if snapshot.Key == local.Key {
		result.Snapshot.KeyMatched++
	} else {
		result.Snapshot.KeyMismatched++
	}
}

func inspectJiraPendingRecords(root string, result *JiraMirrorSnapshot, canonical map[string]*jiraMirrorLocalEvidence) (map[string]*JiraPendingFields, error) {
	dir := filepath.Join(root, ".atl", "pending", "jira")
	entries, err := safepath.ReadDirWithin(root, dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: Jira pending-state directory is unreadable", domain.ErrCheckFailed)
	}
	bound := make(map[string]*JiraPendingFields)
	var inspectErr error
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".txn.json") {
			result.Pending.ActiveTransactions++
			inspectErr = moreSevereErr(inspectErr, fmt.Errorf("%w: one or more Jira pending transactions require recovery", domain.ErrCheckFailed))
			continue
		}
		if entry.IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".json" {
			continue
		}
		result.Pending.Total++
		path := filepath.Join(dir, name)
		body, readErr := safepath.ReadFileWithin(root, path)
		if readErr != nil {
			result.Pending.Unreadable++
			inspectErr = moreSevereErr(inspectErr, fmt.Errorf("%w: one or more Jira pending records are unreadable", domain.ErrCheckFailed))
			continue
		}
		var pending JiraPendingFields
		valid := json.Unmarshal(body, &pending) == nil && pending.Version == jiraPendingFieldsVersion && pending.Key != "" &&
			safepath.Segment(pending.Key)+".json" == name && validateJiraPendingFields(root, path, &pending) == nil
		if !valid {
			result.Pending.Invalid++
			inspectErr = moreSevereErr(inspectErr, fmt.Errorf("%w: one or more Jira pending records are invalid", domain.ErrCheckFailed))
			continue
		}
		result.Pending.Valid++
		result.Pending.FieldEdits += len(pending.Fields)
		local := canonical[pending.Key]
		if local == nil || local.local.Current != pending.WikiHash || filepath.Clean(local.local.Synced.Path) != filepath.Clean(pending.WikiPath) {
			result.Pending.Unbound++
			inspectErr = moreSevereErr(inspectErr, fmt.Errorf("%w: one or more Jira pending records are not bound to a canonical substrate", domain.ErrCheckFailed))
			continue
		}
		result.Pending.Bound++
		bound[pending.Key] = &pending
	}
	return bound, inspectErr
}

func jiraViewMarkerClass(body []byte) string {
	marker := jiraDocumentMarkerLine(string(body))
	switch marker {
	case jiraIssueDocumentMarker:
		return "current"
	case jiraIssueDocumentMarkerV2, jiraIssueDocumentMarkerV1, "<!-- atl:document jira-issue -->":
		return "legacy"
	default:
		if strings.HasPrefix(marker, "<!-- atl:document jira-issue") {
			return "unsupported"
		}
		return "missing"
	}
}

func contentFreeJiraSnapshotError(err error) error {
	if err == nil {
		return nil
	}
	kind := domain.ErrCheckFailed
	switch {
	case errors.Is(err, domain.ErrUsage):
		kind = domain.ErrUsage
	case errors.Is(err, domain.ErrNotFound):
		kind = domain.ErrNotFound
	case errors.Is(err, domain.ErrConfig):
		kind = domain.ErrConfig
	}
	return fmt.Errorf("%w: content-free Jira mirror snapshot could not be completed; preserve the mirror and use an approved issue-level command for details", kind)
}

func finalizeJiraMirrorSnapshot(result *JiraMirrorSnapshot) {
	result.Local.Reconciled = result.Local.Present == result.Local.Clean+result.Local.LocallyEdited &&
		result.Local.Present == result.Local.Tracked+result.Local.Untracked && result.Local.NonCanonical <= result.Local.Untracked
	stateTotal := result.Native.Unchanged + result.Native.Modified + result.Native.Removed + result.Native.Untracked + result.Native.NonCanonical +
		result.Native.MissingBaseline + result.Native.BaselineMismatch + result.Native.Unreadable
	result.Native.Reconciled = result.Native.Total == stateTotal &&
		result.Native.Total == result.Native.BaselinePresent+result.Native.BaselineMissing+result.Native.BaselineUnreadable &&
		result.Native.BaselinePresent == result.Native.BaselineValid+result.Native.BaselineInvalid
	result.Snapshot.Reconciled = result.Snapshot.Expected == result.Snapshot.Present+result.Snapshot.Missing &&
		result.Snapshot.Present == result.Snapshot.Readable+result.Snapshot.Unreadable &&
		result.Snapshot.Readable == result.Snapshot.Valid+result.Snapshot.Invalid &&
		result.Snapshot.Valid == result.Snapshot.KeyMatched+result.Snapshot.KeyMismatched
	result.Pending.Reconciled = result.Pending.Total == result.Pending.Valid+result.Pending.Invalid+result.Pending.Unreadable &&
		result.Pending.Valid == result.Pending.Bound+result.Pending.Unbound
	markerTotal := result.Render.Current + result.Render.Legacy + result.Render.MissingMarker + result.Render.Unsupported
	result.Render.Reconciled = result.Render.Expected == result.Render.Present+result.Render.Missing+result.Render.Unreadable &&
		result.Render.Present == markerTotal && result.Render.Expected == result.Render.StateRecorded+result.Render.StateMissing
	result.Render.RendererCompatible = result.Render.Unsupported == 0 && result.Render.Unreadable == 0
	result.Remote.NotAttempted = result.Local.Present - result.Remote.Attempted
	result.Remote.Reconciled = result.Remote.Eligible <= result.Local.Present && result.Remote.Attempted <= result.Remote.Eligible &&
		result.Remote.Attempted+result.Remote.NotAttempted == result.Local.Present &&
		result.Remote.Attempted == result.Remote.Checked+result.Remote.Unavailable &&
		result.Remote.Checked == result.Remote.InSync+result.Remote.Drifted
	if result.Remote.Requested && result.Remote.Unavailable > 0 {
		result.Complete = false
	}
	result.Reconciled = result.Local.Reconciled && result.Native.Reconciled && result.Snapshot.Reconciled &&
		result.Pending.Reconciled && result.Render.Reconciled && result.Remote.Reconciled
}
