package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	mirrorSnapshotWireSchemaVersion = 1
	mirrorSnapshotWireMaxBytes      = 64 << 10
	mirrorSnapshotWireMaxCount      = maxWorkspaceEntries
)

// jiraMirrorSnapshotWire and confluenceMirrorSnapshotWire are evaluator-owned
// projections of the released schema-v1 content-free mirror snapshot wires.
// They deliberately do not use product mirror types.
type jiraMirrorSnapshotWire struct {
	SchemaVersion   int                           `json:"schema_version"`
	Service         string                        `json:"service"`
	RemoteRequested bool                          `json:"remote_requested"`
	Complete        bool                          `json:"complete"`
	Reconciled      bool                          `json:"reconciled"`
	Local           mirrorSnapshotLocalWire       `json:"local"`
	Native          jiraMirrorSnapshotNativeWire  `json:"native"`
	Snapshot        jiraMirrorSnapshotRawWire     `json:"snapshot"`
	Pending         jiraMirrorSnapshotPendingWire `json:"pending"`
	Render          mirrorSnapshotRenderWire      `json:"render"`
	Remote          mirrorSnapshotRemoteWire      `json:"remote"`
}

type confluenceMirrorSnapshotWire struct {
	SchemaVersion   int                                    `json:"schema_version"`
	Service         string                                 `json:"service"`
	RemoteRequested bool                                   `json:"remote_requested"`
	Complete        bool                                   `json:"complete"`
	Reconciled      bool                                   `json:"reconciled"`
	Local           mirrorSnapshotLocalWire                `json:"local"`
	Native          confluenceMirrorSnapshotNativeWire     `json:"native"`
	Validation      confluenceMirrorSnapshotValidationWire `json:"validation"`
	Render          mirrorSnapshotRenderWire               `json:"render"`
	Remote          mirrorSnapshotRemoteWire               `json:"remote"`
}

type mirrorSnapshotLocalWire struct {
	Present       int  `json:"present"`
	Clean         int  `json:"clean"`
	LocallyEdited int  `json:"locally_edited"`
	Tracked       int  `json:"tracked"`
	Untracked     int  `json:"untracked"`
	NonCanonical  int  `json:"non_canonical"`
	Reconciled    bool `json:"reconciled"`
}

type jiraMirrorSnapshotNativeWire struct {
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

type confluenceMirrorSnapshotNativeWire struct {
	Total              int  `json:"total"`
	Unchanged          int  `json:"unchanged"`
	Added              int  `json:"added"`
	Removed            int  `json:"removed"`
	Modified           int  `json:"modified"`
	Malformed          int  `json:"malformed"`
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

type jiraMirrorSnapshotRawWire struct {
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

type jiraMirrorSnapshotPendingWire struct {
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

type confluenceMirrorSnapshotValidationWire struct {
	Total      int  `json:"total"`
	Present    int  `json:"present"`
	Absent     int  `json:"absent"`
	Valid      int  `json:"valid"`
	Invalid    int  `json:"invalid"`
	Unreadable int  `json:"unreadable"`
	Reconciled bool `json:"reconciled"`
}

type mirrorSnapshotRenderWire struct {
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

type mirrorSnapshotRemoteWire struct {
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

func decodeJiraMirrorSnapshotWire(r io.Reader) (jiraMirrorSnapshotWire, error) {
	var result jiraMirrorSnapshotWire
	if err := decodeMirrorSnapshotWire(r, &result, validateJiraMirrorSnapshotWireMembers, "Jira"); err != nil {
		return jiraMirrorSnapshotWire{}, err
	}
	if err := result.validate(); err != nil {
		return jiraMirrorSnapshotWire{}, fmt.Errorf("validate Jira mirror snapshot wire: %w", err)
	}
	return result, nil
}

func decodeConfluenceMirrorSnapshotWire(r io.Reader) (confluenceMirrorSnapshotWire, error) {
	var result confluenceMirrorSnapshotWire
	if err := decodeMirrorSnapshotWire(r, &result, validateConfluenceMirrorSnapshotWireMembers, "Confluence"); err != nil {
		return confluenceMirrorSnapshotWire{}, err
	}
	if err := result.validate(); err != nil {
		return confluenceMirrorSnapshotWire{}, fmt.Errorf("validate Confluence mirror snapshot wire: %w", err)
	}
	return result, nil
}

func decodeMirrorSnapshotWire(r io.Reader, dst any, validateMembers func([]byte) error, service string) error {
	limited := &io.LimitedReader{R: r, N: mirrorSnapshotWireMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read %s mirror snapshot wire: %w", service, err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("%s mirror snapshot wire exceeds %d bytes", service, mirrorSnapshotWireMaxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return fmt.Errorf("decode %s mirror snapshot wire: %w", service, err)
	}
	if err := validateMembers(data); err != nil {
		return fmt.Errorf("decode %s mirror snapshot wire: %w", service, err)
	}
	if err := decodeStrict(bytes.NewReader(data), dst); err != nil {
		return fmt.Errorf("decode %s mirror snapshot wire: %w", service, err)
	}
	return nil
}

func validateJiraMirrorSnapshotWireMembers(data []byte) error {
	root, err := mirrorSnapshotObject(data, "Jira mirror snapshot")
	if err != nil {
		return err
	}
	if err := requireMirrorSnapshotMembers(root, "Jira mirror snapshot", []string{
		"schema_version", "service", "remote_requested", "complete", "reconciled", "local", "native", "snapshot", "pending", "render", "remote",
	}); err != nil {
		return err
	}
	if _, err := mirrorSnapshotNested(root, "local", "Jira mirror snapshot", []string{
		"present", "clean", "locally_edited", "tracked", "untracked", "non_canonical", "reconciled",
	}); err != nil {
		return err
	}
	if _, err := mirrorSnapshotNested(root, "native", "Jira mirror snapshot", []string{
		"total", "unchanged", "modified", "removed", "untracked", "non_canonical", "missing_baseline", "baseline_mismatch", "unreadable",
		"baseline_present", "baseline_missing", "baseline_unreadable", "baseline_valid", "baseline_invalid", "reconciled",
	}); err != nil {
		return err
	}
	if _, err := mirrorSnapshotNested(root, "snapshot", "Jira mirror snapshot", []string{
		"expected", "present", "missing", "readable", "unreadable", "valid", "invalid", "key_matched", "key_mismatched", "reconciled",
	}); err != nil {
		return err
	}
	if _, err := mirrorSnapshotNested(root, "pending", "Jira mirror snapshot", []string{
		"total", "valid", "invalid", "unreadable", "bound", "unbound", "field_edits", "active_transactions", "reconciled",
	}); err != nil {
		return err
	}
	return validateCommonMirrorSnapshotWireMembers(root, "Jira mirror snapshot")
}

func validateConfluenceMirrorSnapshotWireMembers(data []byte) error {
	root, err := mirrorSnapshotObject(data, "Confluence mirror snapshot")
	if err != nil {
		return err
	}
	if err := requireMirrorSnapshotMembers(root, "Confluence mirror snapshot", []string{
		"schema_version", "service", "remote_requested", "complete", "reconciled", "local", "native", "validation", "render", "remote",
	}); err != nil {
		return err
	}
	if _, err := mirrorSnapshotNested(root, "local", "Confluence mirror snapshot", []string{
		"present", "clean", "locally_edited", "tracked", "untracked", "non_canonical", "reconciled",
	}); err != nil {
		return err
	}
	if _, err := mirrorSnapshotNested(root, "native", "Confluence mirror snapshot", []string{
		"total", "unchanged", "added", "removed", "modified", "malformed", "missing_baseline", "baseline_mismatch", "unreadable",
		"baseline_present", "baseline_missing", "baseline_unreadable", "baseline_valid", "baseline_invalid", "reconciled",
	}); err != nil {
		return err
	}
	if _, err := mirrorSnapshotNested(root, "validation", "Confluence mirror snapshot", []string{
		"total", "present", "absent", "valid", "invalid", "unreadable", "reconciled",
	}); err != nil {
		return err
	}
	return validateCommonMirrorSnapshotWireMembers(root, "Confluence mirror snapshot")
}

func validateCommonMirrorSnapshotWireMembers(root map[string]json.RawMessage, owner string) error {
	if _, err := mirrorSnapshotNested(root, "render", owner, []string{
		"expected", "present", "missing", "current", "legacy", "missing_marker", "unsupported", "unreadable",
		"state_recorded", "state_missing", "renderer_compatible", "reconciled",
	}); err != nil {
		return err
	}
	_, err := mirrorSnapshotNested(root, "remote", owner, []string{
		"requested", "eligible", "attempted", "not_attempted", "checked", "in_sync", "drifted", "unavailable", "reconciled",
	})
	return err
}

func mirrorSnapshotObject(raw []byte, owner string) (map[string]json.RawMessage, error) {
	if mirrorSnapshotNull(raw) {
		return nil, fmt.Errorf("%s must not be null", owner)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("%s: %w", owner, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func mirrorSnapshotNested(root map[string]json.RawMessage, name, owner string, members []string) (map[string]json.RawMessage, error) {
	value, ok := root[name]
	if !ok {
		return nil, fmt.Errorf("%s is missing required member %q", owner, name)
	}
	object, err := mirrorSnapshotObject(value, owner+"."+name)
	if err != nil {
		return nil, err
	}
	if err := requireMirrorSnapshotMembers(object, owner+"."+name, members); err != nil {
		return nil, err
	}
	return object, nil
}

func requireMirrorSnapshotMembers(object map[string]json.RawMessage, owner string, members []string) error {
	allowed := make(map[string]struct{}, len(members))
	for _, name := range members {
		value, ok := object[name]
		if !ok {
			return fmt.Errorf("%s is missing required member %q", owner, name)
		}
		if mirrorSnapshotNull(value) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
		allowed[name] = struct{}{}
	}
	for name := range object {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func mirrorSnapshotNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (value jiraMirrorSnapshotWire) validate() error {
	if value.SchemaVersion != mirrorSnapshotWireSchemaVersion || value.Service != "jira" {
		return fmt.Errorf("schema_version/service is not the released Jira schema-v1 wire")
	}
	if err := value.Local.validate(); err != nil {
		return fmt.Errorf("local: %w", err)
	}
	if err := value.Native.validate(); err != nil {
		return fmt.Errorf("native: %w", err)
	}
	if err := value.Snapshot.validate(); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := value.Pending.validate(); err != nil {
		return fmt.Errorf("pending: %w", err)
	}
	if err := value.Render.validate(); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	if err := value.Remote.validate(value.Local.Present, value.RemoteRequested); err != nil {
		return fmt.Errorf("remote: %w", err)
	}
	wantReconciled := value.Local.Reconciled && value.Native.Reconciled && value.Snapshot.Reconciled &&
		value.Pending.Reconciled && value.Render.Reconciled && value.Remote.Reconciled
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with component summaries")
	}
	return nil
}

func (value confluenceMirrorSnapshotWire) validate() error {
	if value.SchemaVersion != mirrorSnapshotWireSchemaVersion || value.Service != "confluence" {
		return fmt.Errorf("schema_version/service is not the released Confluence schema-v1 wire")
	}
	if err := value.Local.validate(); err != nil {
		return fmt.Errorf("local: %w", err)
	}
	if err := value.Native.validate(); err != nil {
		return fmt.Errorf("native: %w", err)
	}
	if err := value.Validation.validate(); err != nil {
		return fmt.Errorf("validation: %w", err)
	}
	if err := value.Render.validate(); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	if err := value.Remote.validate(value.Local.Present, value.RemoteRequested); err != nil {
		return fmt.Errorf("remote: %w", err)
	}
	wantReconciled := value.Local.Reconciled && value.Native.Reconciled && value.Validation.Reconciled &&
		value.Render.Reconciled && value.Remote.Reconciled
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with component summaries")
	}
	return nil
}

func (value mirrorSnapshotLocalWire) validate() error {
	if err := validateMirrorSnapshotCounts(value.Present, value.Clean, value.LocallyEdited, value.Tracked, value.Untracked, value.NonCanonical); err != nil {
		return err
	}
	wantReconciled := value.Present == value.Clean+value.LocallyEdited &&
		value.Present == value.Tracked+value.Untracked && value.NonCanonical <= value.Untracked
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with counts")
	}
	return nil
}

func (value jiraMirrorSnapshotNativeWire) validate() error {
	if err := validateMirrorSnapshotCounts(
		value.Total, value.Unchanged, value.Modified, value.Removed, value.Untracked, value.NonCanonical,
		value.MissingBaseline, value.BaselineMismatch, value.Unreadable, value.BaselinePresent, value.BaselineMissing,
		value.BaselineUnreadable, value.BaselineValid, value.BaselineInvalid,
	); err != nil {
		return err
	}
	stateTotal := value.Unchanged + value.Modified + value.Removed + value.Untracked + value.NonCanonical +
		value.MissingBaseline + value.BaselineMismatch + value.Unreadable
	wantReconciled := value.Total == stateTotal &&
		value.Total == value.BaselinePresent+value.BaselineMissing+value.BaselineUnreadable &&
		value.BaselinePresent == value.BaselineValid+value.BaselineInvalid
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with counts")
	}
	return nil
}

func (value confluenceMirrorSnapshotNativeWire) validate() error {
	if err := validateMirrorSnapshotCounts(
		value.Total, value.Unchanged, value.Added, value.Removed, value.Modified, value.Malformed, value.MissingBaseline,
		value.BaselineMismatch, value.Unreadable, value.BaselinePresent, value.BaselineMissing, value.BaselineUnreadable,
		value.BaselineValid, value.BaselineInvalid,
	); err != nil {
		return err
	}
	stateTotal := value.Unchanged + value.Added + value.Removed + value.Modified + value.Malformed +
		value.MissingBaseline + value.BaselineMismatch + value.Unreadable
	wantReconciled := value.Total == stateTotal &&
		value.Total == value.BaselinePresent+value.BaselineMissing+value.BaselineUnreadable &&
		value.BaselinePresent == value.BaselineValid+value.BaselineInvalid
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with counts")
	}
	return nil
}

func (value jiraMirrorSnapshotRawWire) validate() error {
	if err := validateMirrorSnapshotCounts(
		value.Expected, value.Present, value.Missing, value.Readable, value.Unreadable, value.Valid, value.Invalid,
		value.KeyMatched, value.KeyMismatched,
	); err != nil {
		return err
	}
	wantReconciled := value.Expected == value.Present+value.Missing && value.Present == value.Readable+value.Unreadable &&
		value.Readable == value.Valid+value.Invalid && value.Valid == value.KeyMatched+value.KeyMismatched
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with counts")
	}
	return nil
}

func (value jiraMirrorSnapshotPendingWire) validate() error {
	if err := validateMirrorSnapshotCounts(
		value.Total, value.Valid, value.Invalid, value.Unreadable, value.Bound, value.Unbound, value.FieldEdits, value.ActiveTransactions,
	); err != nil {
		return err
	}
	wantReconciled := value.Total == value.Valid+value.Invalid+value.Unreadable && value.Valid == value.Bound+value.Unbound
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with counts")
	}
	return nil
}

func (value confluenceMirrorSnapshotValidationWire) validate() error {
	if err := validateMirrorSnapshotCounts(value.Total, value.Present, value.Absent, value.Valid, value.Invalid, value.Unreadable); err != nil {
		return err
	}
	wantReconciled := value.Total == value.Present+value.Absent && value.Present == value.Valid+value.Invalid &&
		value.Unreadable <= value.Total
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with counts")
	}
	return nil
}

func (value mirrorSnapshotRenderWire) validate() error {
	if err := validateMirrorSnapshotCounts(
		value.Expected, value.Present, value.Missing, value.Current, value.Legacy, value.MissingMarker, value.Unsupported,
		value.Unreadable, value.StateRecorded, value.StateMissing,
	); err != nil {
		return err
	}
	markerTotal := value.Current + value.Legacy + value.MissingMarker + value.Unsupported
	wantReconciled := value.Expected == value.Present+value.Missing+value.Unreadable && value.Present == markerTotal &&
		value.Expected == value.StateRecorded+value.StateMissing
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with counts")
	}
	if value.RendererCompatible != (value.Unsupported == 0 && value.Unreadable == 0) {
		return fmt.Errorf("renderer_compatible is not reconciled with unsupported and unreadable counts")
	}
	return nil
}

func (value mirrorSnapshotRemoteWire) validate(localPresent int, remoteRequested bool) error {
	if err := validateMirrorSnapshotCounts(
		value.Eligible, value.Attempted, value.NotAttempted, value.Checked, value.InSync, value.Drifted, value.Unavailable,
	); err != nil {
		return err
	}
	if remoteRequested || value.Requested {
		return fmt.Errorf("offline mirror snapshot requested remote evidence")
	}
	if value.Attempted != 0 || value.Checked != 0 || value.InSync != 0 || value.Drifted != 0 || value.Unavailable != 0 {
		return fmt.Errorf("offline mirror snapshot records remote activity")
	}
	wantReconciled := value.Eligible <= localPresent && value.Attempted <= value.Eligible &&
		value.Attempted+value.NotAttempted == localPresent && value.Attempted == value.Checked+value.Unavailable &&
		value.Checked == value.InSync+value.Drifted
	if value.Reconciled != wantReconciled {
		return fmt.Errorf("reconciled is not reconciled with counts")
	}
	return nil
}

func validateMirrorSnapshotCounts(values ...int) error {
	for _, value := range values {
		if value < 0 || value > mirrorSnapshotWireMaxCount {
			return fmt.Errorf("count %d is outside 0..%d", value, mirrorSnapshotWireMaxCount)
		}
	}
	return nil
}
