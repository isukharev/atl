package mirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	CorpusSnapshotConfluence = "confluence"
	CorpusSnapshotJira       = "jira"

	corpusSnapshotFingerprintDomain = "atl.mirror.corpus-snapshot.v1"
	maxCorpusSnapshotJSONDepth      = 64
)

// CorpusSnapshotLimits bounds every file and aggregate allocation made while
// capturing pristine mirror evidence. Zero fields select DefaultCorpusSnapshotLimits.
type CorpusSnapshotLimits struct {
	MaxItems          int
	MaxNativeBytes    int64
	MaxMetadataBytes  int64
	MaxAuxiliaryBytes int64
	MaxStateBytes     int64
	MaxTotalBytes     int64
}

func DefaultCorpusSnapshotLimits() CorpusSnapshotLimits {
	return CorpusSnapshotLimits{
		MaxItems:          100_000,
		MaxNativeBytes:    1 << 30,
		MaxMetadataBytes:  64 << 20,
		MaxAuxiliaryBytes: 64 << 20,
		MaxStateBytes:     64 << 20,
		MaxTotalBytes:     64 << 30,
	}
}

// CorpusSnapshotOptions selects diagnostic handling only. Ordinary exports
// leave AllowUnreconciled false and refuse service-owned staged lineage.
type CorpusSnapshotOptions struct {
	Limits            CorpusSnapshotLimits
	AllowUnreconciled bool
}

// CorpusSnapshotFile is an immutable captured file. Path is the logical
// mirror-relative lineage path; pristine native bytes are read from .atl/base,
// never from this working path.
type CorpusSnapshotFile struct {
	Path   string
	Data   []byte
	SHA256 string
}

// CorpusSnapshotItem binds one state entry to a provider-stable numeric ID,
// pristine native baseline, structurally correlated primary metadata, and
// bounded optional sidecars.
type CorpusSnapshotItem struct {
	StateID     string
	ProviderID  string
	Version     int
	Native      CorpusSnapshotFile
	Metadata    CorpusSnapshotFile
	Auxiliaries []CorpusSnapshotFile
}

// CorpusSnapshot is an immutable service-specific inventory. Fingerprint is a
// domain-separated digest of the content and correlation evidence; it reveals
// no raw backend origin, provider object value, path, or content by itself.
type CorpusSnapshot struct {
	root         string
	service      string
	originSHA256 string
	fingerprint  string
	reconciled   bool
	items        []CorpusSnapshotItem
	options      CorpusSnapshotOptions
	stateStamp   string
}

func (s *CorpusSnapshot) Service() string      { return s.service }
func (s *CorpusSnapshot) OriginSHA256() string { return s.originSHA256 }
func (s *CorpusSnapshot) Fingerprint() string  { return s.fingerprint }
func (s *CorpusSnapshot) Reconciled() bool     { return s.reconciled }
func (s *CorpusSnapshot) Len() int             { return len(s.items) }

// Inventory returns content-free descriptors. Data is always nil; ReadItem
// performs the bounded exact-byte read for one selected descriptor.
func (s *CorpusSnapshot) Inventory() []CorpusSnapshotItem {
	return cloneCorpusSnapshotInventory(s.items)
}

// ReadItem loads and revalidates exactly one captured item. Whole-corpus
// consumers can stream indices [0, Len) without retaining every native body.
func (s *CorpusSnapshot) ReadItem(index int) (CorpusSnapshotItem, error) {
	if index < 0 || index >= len(s.items) {
		return CorpusSnapshotItem{}, corpusSnapshotError("snapshot item index is out of bounds")
	}
	descriptor := s.items[index]
	ext, err := corpusSnapshotExtension(s.service)
	if err != nil {
		return CorpusSnapshotItem{}, err
	}
	state := SyncState{ID: descriptor.StateID, Version: descriptor.Version, Hash: descriptor.Native.SHA256, Path: descriptor.Native.Path}
	item, _, err := New(s.root).captureCorpusSnapshotItem(s.service, ext, state, s.options.Limits)
	if err != nil {
		return CorpusSnapshotItem{}, err
	}
	if !sameCorpusSnapshotDescriptor(descriptor, item) {
		return CorpusSnapshotItem{}, corpusSnapshotError("snapshot item changed during export")
	}
	return item, nil
}

// BeginCorpusSnapshot captures one service from pristine bases. Callers that
// coordinate with mutations should hold the existing service read lock across
// capture, rendering, and the final Revalidate call. The method also performs
// its own before/after state check and immediate full revalidation.
func (m *Mirror) BeginCorpusSnapshot(service string, options CorpusSnapshotOptions) (*CorpusSnapshot, error) {
	options, err := normalizeCorpusSnapshotOptions(options)
	if err != nil {
		return nil, err
	}
	snapshot, err := m.captureCorpusSnapshot(service, options)
	if err != nil {
		return nil, err
	}
	if err := snapshot.Revalidate(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// Revalidate rejects any change to the service state, binding, pristine base,
// correlated metadata, or captured auxiliary sidecars since capture.
func (s *CorpusSnapshot) Revalidate() error {
	current, err := New(s.root).captureCorpusSnapshot(s.service, s.options)
	if err != nil {
		return err
	}
	if current.fingerprint != s.fingerprint || current.stateStamp != s.stateStamp ||
		current.originSHA256 != s.originSHA256 || current.reconciled != s.reconciled {
		return corpusSnapshotError("snapshot evidence changed during export")
	}
	return nil
}

func (m *Mirror) captureCorpusSnapshot(service string, options CorpusSnapshotOptions) (*CorpusSnapshot, error) {
	ext, err := corpusSnapshotExtension(service)
	if err != nil {
		return nil, err
	}
	binding, present, err := m.BackendBinding(service)
	if err != nil {
		return nil, corpusSnapshotError("backend binding is unreadable")
	}
	if !present || !validCorpusSnapshotOrigin(binding.OriginSHA256) {
		return nil, corpusSnapshotError("backend binding is missing or invalid")
	}
	sidecar, err := m.loadCorpusSnapshotSidecar(options.Limits.MaxStateBytes)
	if err != nil {
		return nil, err
	}
	states, reconciled, err := corpusSnapshotStates(sidecar, ext, options.AllowUnreconciled, options.Limits.MaxItems)
	if err != nil {
		return nil, err
	}
	stateStamp, err := corpusSnapshotStateStamp(service, binding, states, reconciled)
	if err != nil {
		return nil, err
	}

	items := make([]CorpusSnapshotItem, 0, len(states))
	providerIDs := make(map[string]struct{}, len(states))
	var total int64
	for _, state := range states {
		item, size, err := m.captureCorpusSnapshotItem(service, ext, state, options.Limits)
		if err != nil {
			return nil, err
		}
		if _, duplicate := providerIDs[item.ProviderID]; duplicate {
			return nil, corpusSnapshotError("duplicate provider identity")
		}
		providerIDs[item.ProviderID] = struct{}{}
		if size > options.Limits.MaxTotalBytes-total {
			return nil, corpusSnapshotError("snapshot exceeds the aggregate byte bound")
		}
		total += size
		clearCorpusSnapshotData(&item)
		items = append(items, item)
	}

	latestBinding, present, err := m.BackendBinding(service)
	if err != nil || !present || latestBinding != binding {
		return nil, corpusSnapshotError("backend binding changed during capture")
	}
	latestSidecar, err := m.loadCorpusSnapshotSidecar(options.Limits.MaxStateBytes)
	if err != nil {
		return nil, err
	}
	latestStates, latestReconciled, err := corpusSnapshotStates(latestSidecar, ext, options.AllowUnreconciled, options.Limits.MaxItems)
	if err != nil {
		return nil, err
	}
	latestStamp, err := corpusSnapshotStateStamp(service, binding, latestStates, latestReconciled)
	if err != nil || latestStamp != stateStamp {
		return nil, corpusSnapshotError("mirror state changed during capture")
	}

	fingerprint, err := corpusSnapshotFingerprint(service, binding.OriginSHA256, reconciled, items)
	if err != nil {
		return nil, err
	}
	return &CorpusSnapshot{
		root: m.Root, service: service, originSHA256: binding.OriginSHA256,
		fingerprint: fingerprint, reconciled: reconciled, items: items,
		options: options, stateStamp: stateStamp,
	}, nil
}

func (m *Mirror) captureCorpusSnapshotItem(service, ext string, state SyncState, limits CorpusSnapshotLimits) (CorpusSnapshotItem, int64, error) {
	if state.ID == "" || state.Path == "" || filepath.Ext(state.Path) != ext ||
		validateStagedPath(state.Path) != nil || !validCorpusSnapshotDigest(state.Hash) {
		return CorpusSnapshotItem{}, 0, corpusSnapshotError("invalid tracked state")
	}
	if service == CorpusSnapshotConfluence && state.Version <= 0 {
		return CorpusSnapshotItem{}, 0, corpusSnapshotError("invalid Confluence version")
	}
	if service == CorpusSnapshotJira && state.Version != 0 {
		return CorpusSnapshotItem{}, 0, corpusSnapshotError("invalid Jira version")
	}
	if service == CorpusSnapshotJira && strings.TrimSuffix(filepath.Base(filepath.FromSlash(state.Path)), ext) != state.ID {
		return CorpusSnapshotItem{}, 0, corpusSnapshotError("Jira tracked path is misbound")
	}

	base, present, err := m.ReadBaseBodyExtWithinLimit(state.ID, ext, limits.MaxNativeBytes)
	if err != nil || !present || Hash(base) != state.Hash {
		return CorpusSnapshotItem{}, 0, corpusSnapshotError("pristine baseline is missing, unreadable, or mismatched")
	}
	metadataPath := strings.TrimSuffix(state.Path, ext) + corpusSnapshotMetadataSuffix(service)
	metadata, err := safepath.ReadFileWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(metadataPath)), limits.MaxMetadataBytes)
	if err != nil {
		return CorpusSnapshotItem{}, 0, corpusSnapshotError("primary metadata is missing or unreadable")
	}
	providerID, err := validateCorpusSnapshotMetadata(service, state, metadata)
	if err != nil {
		return CorpusSnapshotItem{}, 0, err
	}
	auxiliaries, auxiliarySize, err := m.captureCorpusSnapshotAuxiliaries(service, state.Path, ext, limits)
	if err != nil {
		return CorpusSnapshotItem{}, 0, err
	}
	metadataHash := Hash(metadata)
	item := CorpusSnapshotItem{
		StateID: state.ID, ProviderID: providerID, Version: state.Version,
		Native:      CorpusSnapshotFile{Path: state.Path, Data: append([]byte(nil), base...), SHA256: state.Hash},
		Metadata:    CorpusSnapshotFile{Path: metadataPath, Data: append([]byte(nil), metadata...), SHA256: metadataHash},
		Auxiliaries: auxiliaries,
	}
	return item, int64(len(base)) + int64(len(metadata)) + auxiliarySize, nil
}

func (m *Mirror) captureCorpusSnapshotAuxiliaries(service, nativePath, ext string, limits CorpusSnapshotLimits) ([]CorpusSnapshotFile, int64, error) {
	stem := strings.TrimSuffix(nativePath, ext)
	var suffixes []string
	switch service {
	case CorpusSnapshotConfluence:
		suffixes = []string{".comments.json", ".attachments.json", ".jira-macros.json"}
	case CorpusSnapshotJira:
		suffixes = []string{".comments.json", ".attachments.json", ".epic-children.json"}
	}
	out := []CorpusSnapshotFile{}
	var total int64
	var attachmentSidecar []byte
	for _, suffix := range suffixes {
		path := stem + suffix
		data, err := safepath.ReadFileWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(path)), limits.MaxAuxiliaryBytes)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, 0, corpusSnapshotError("auxiliary metadata is unreadable")
		}
		if int64(len(data)) > limits.MaxTotalBytes-total {
			return nil, 0, corpusSnapshotError("auxiliary metadata exceeds the aggregate byte bound")
		}
		total += int64(len(data))
		out = append(out, CorpusSnapshotFile{Path: path, Data: append([]byte(nil), data...), SHA256: Hash(data)})
		if suffix == ".attachments.json" {
			attachmentSidecar = data
		}
	}
	if attachmentSidecar != nil {
		decoded, err := DecodeAttachmentSidecarV1(attachmentSidecar)
		if err != nil || decoded.Service != service {
			return nil, 0, corpusSnapshotError("attachment inventory is invalid or misbound")
		}
		for _, attachment := range decoded.Attachments {
			if attachment.Body.State != AttachmentBodyCaptured {
				continue
			}
			path := attachment.Body.Path
			data, err := safepath.ReadFileWithinLimit(m.Root, filepath.Join(m.Root, filepath.FromSlash(path)), limits.MaxAuxiliaryBytes)
			if err != nil || int64(len(data)) != attachment.Body.Size || Hash(data) != attachment.Body.SHA256 {
				return nil, 0, corpusSnapshotError("attachment body is missing, unreadable, or mismatched")
			}
			if int64(len(data)) > limits.MaxTotalBytes-total {
				return nil, 0, corpusSnapshotError("attachment bodies exceed the aggregate byte bound")
			}
			total += int64(len(data))
			out = append(out, CorpusSnapshotFile{Path: path, Data: append([]byte(nil), data...), SHA256: Hash(data)})
		}
	}
	return out, total, nil
}

func (m *Mirror) loadCorpusSnapshotSidecar(max int64) (sidecarFile, error) {
	empty := sidecarFile{Pages: map[string]SyncState{}, Views: map[string]ViewState{}, Staged: map[string]StagedState{}}
	data, err := safepath.ReadFileWithinLimit(m.Root, m.sidecarPath(), max)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, corpusSnapshotError("mirror state is unreadable")
	}
	var state sidecarFile
	if err := decodeCorpusSnapshotJSON(data, &state); err != nil {
		return empty, corpusSnapshotError("mirror state is invalid")
	}
	if state.Pages == nil {
		return empty, corpusSnapshotError("mirror state has no tracked-resource map")
	}
	if state.Views == nil {
		state.Views = map[string]ViewState{}
	}
	if state.Staged == nil {
		state.Staged = map[string]StagedState{}
	}
	return state, nil
}

func corpusSnapshotStates(state sidecarFile, ext string, allowUnreconciled bool, maxItems int) ([]SyncState, bool, error) {
	states := make([]SyncState, 0)
	paths := make(map[string]struct{})
	foldedPaths := make(map[string]struct{})
	for key, entry := range state.Pages {
		if filepath.Ext(entry.Path) != ext {
			continue
		}
		if key == "" || key != entry.ID {
			return nil, false, corpusSnapshotError("tracked-state identity is invalid")
		}
		if _, duplicate := paths[entry.Path]; duplicate {
			return nil, false, corpusSnapshotError("tracked paths are duplicated")
		}
		folded := foldCorpusSnapshotPath(entry.Path)
		if _, duplicate := foldedPaths[folded]; duplicate {
			return nil, false, corpusSnapshotError("tracked paths alias on supported filesystems")
		}
		paths[entry.Path] = struct{}{}
		foldedPaths[folded] = struct{}{}
		states = append(states, entry)
		if len(states) > maxItems {
			return nil, false, corpusSnapshotError("snapshot exceeds the item bound")
		}
	}
	reconciled := true
	for key, entry := range state.Staged {
		if filepath.Ext(entry.Path) != ext {
			continue
		}
		if key == "" || key != entry.ID || validateStagedState(key, entry) != nil {
			return nil, false, corpusSnapshotError("staged lineage is invalid")
		}
		reconciled = false
	}
	if !reconciled && !allowUnreconciled {
		return nil, false, corpusSnapshotError("service mirror has staged lineage")
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].Path != states[j].Path {
			return states[i].Path < states[j].Path
		}
		return states[i].ID < states[j].ID
	})
	return states, reconciled, nil
}

func validateCorpusSnapshotMetadata(service string, state SyncState, data []byte) (string, error) {
	switch service {
	case CorpusSnapshotConfluence:
		var metadata Meta
		if err := decodeCorpusSnapshotJSON(data, &metadata); err != nil || metadata.ID != state.ID ||
			metadata.Version != state.Version || metadata.Hash != state.Hash || !validCorpusProviderID(metadata.ID) {
			return "", corpusSnapshotError("Confluence metadata is invalid or misbound")
		}
		return metadata.ID, nil
	case CorpusSnapshotJira:
		var metadata struct {
			Key    string          `json:"key"`
			ID     string          `json:"id"`
			Fields json.RawMessage `json:"fields"`
		}
		if err := decodeCorpusSnapshotJSON(data, &metadata); err != nil ||
			metadata.Key == "" || safepath.Segment(metadata.Key) != state.ID ||
			(state.Identity != "" && metadata.ID != state.Identity) ||
			!validCorpusProviderID(metadata.ID) || !corpusJSONObject(metadata.Fields) {
			return "", corpusSnapshotError("Jira metadata is invalid or misbound")
		}
		return metadata.ID, nil
	default:
		return "", corpusSnapshotError("unsupported snapshot service")
	}
}

func corpusSnapshotFingerprint(service, origin string, reconciled bool, items []CorpusSnapshotItem) (string, error) {
	hash := sha256.New()
	writeCorpusSnapshotHashPart(hash, []byte(corpusSnapshotFingerprintDomain))
	writeCorpusSnapshotHashPart(hash, []byte(service))
	writeCorpusSnapshotHashPart(hash, []byte(origin))
	writeCorpusSnapshotHashPart(hash, []byte(fmt.Sprintf("%t", reconciled)))
	for _, item := range items {
		entry := struct {
			StateID        string   `json:"state_id"`
			ProviderID     string   `json:"provider_id"`
			Version        int      `json:"version"`
			NativePath     string   `json:"native_path"`
			NativeSHA256   string   `json:"native_sha256"`
			MetadataPath   string   `json:"metadata_path"`
			MetadataSHA256 string   `json:"metadata_sha256"`
			Auxiliary      []string `json:"auxiliary"`
		}{item.StateID, item.ProviderID, item.Version, item.Native.Path, item.Native.SHA256,
			item.Metadata.Path, item.Metadata.SHA256, []string{}}
		for _, auxiliary := range item.Auxiliaries {
			entry.Auxiliary = append(entry.Auxiliary, auxiliary.Path+":"+auxiliary.SHA256)
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return "", corpusSnapshotError("snapshot fingerprint encoding failed")
		}
		writeCorpusSnapshotHashPart(hash, data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func corpusSnapshotStateStamp(service string, binding BackendBinding, states []SyncState, reconciled bool) (string, error) {
	data, err := json.Marshal(struct {
		Service    string      `json:"service"`
		Binding    string      `json:"binding"`
		Reconciled bool        `json:"reconciled"`
		States     []SyncState `json:"states"`
	}{service, binding.OriginSHA256, reconciled, states})
	if err != nil {
		return "", corpusSnapshotError("snapshot state encoding failed")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func cloneCorpusSnapshotInventory(items []CorpusSnapshotItem) []CorpusSnapshotItem {
	out := make([]CorpusSnapshotItem, len(items))
	for i := range items {
		out[i] = items[i]
		out[i].Auxiliaries = make([]CorpusSnapshotFile, len(items[i].Auxiliaries))
		copy(out[i].Auxiliaries, items[i].Auxiliaries)
	}
	return out
}

func clearCorpusSnapshotData(item *CorpusSnapshotItem) {
	item.Native.Data = nil
	item.Metadata.Data = nil
	for index := range item.Auxiliaries {
		item.Auxiliaries[index].Data = nil
	}
}

func sameCorpusSnapshotDescriptor(left, right CorpusSnapshotItem) bool {
	if left.StateID != right.StateID || left.ProviderID != right.ProviderID || left.Version != right.Version ||
		left.Native.Path != right.Native.Path || left.Native.SHA256 != right.Native.SHA256 ||
		left.Metadata.Path != right.Metadata.Path || left.Metadata.SHA256 != right.Metadata.SHA256 ||
		len(left.Auxiliaries) != len(right.Auxiliaries) {
		return false
	}
	for index := range left.Auxiliaries {
		if left.Auxiliaries[index].Path != right.Auxiliaries[index].Path || left.Auxiliaries[index].SHA256 != right.Auxiliaries[index].SHA256 {
			return false
		}
	}
	return true
}

func normalizeCorpusSnapshotOptions(options CorpusSnapshotOptions) (CorpusSnapshotOptions, error) {
	defaults := DefaultCorpusSnapshotLimits()
	values := []*int64{&options.Limits.MaxNativeBytes, &options.Limits.MaxMetadataBytes,
		&options.Limits.MaxAuxiliaryBytes, &options.Limits.MaxStateBytes, &options.Limits.MaxTotalBytes}
	defaultValues := []int64{defaults.MaxNativeBytes, defaults.MaxMetadataBytes, defaults.MaxAuxiliaryBytes, defaults.MaxStateBytes, defaults.MaxTotalBytes}
	if options.Limits.MaxItems < 0 {
		return CorpusSnapshotOptions{}, corpusSnapshotError("snapshot item bound is invalid")
	}
	if options.Limits.MaxItems == 0 {
		options.Limits.MaxItems = defaults.MaxItems
	}
	if options.Limits.MaxItems > defaults.MaxItems {
		return CorpusSnapshotOptions{}, corpusSnapshotError("snapshot item bound exceeds the supported maximum")
	}
	for i, value := range values {
		if *value < 0 {
			return CorpusSnapshotOptions{}, corpusSnapshotError("snapshot byte bound is invalid")
		}
		if *value == 0 {
			*value = defaultValues[i]
		}
		if *value > defaultValues[i] {
			return CorpusSnapshotOptions{}, corpusSnapshotError("snapshot byte bound exceeds the supported maximum")
		}
	}
	return options, nil
}

func decodeCorpusSnapshotJSON(data []byte, destination any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateCorpusSnapshotJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func validateCorpusSnapshotJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxCorpusSnapshotJSONDepth {
		return fmt.Errorf("JSON nesting exceeds the supported maximum")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			folded := strings.ToLower(key)
			if _, duplicate := seen[folded]; duplicate {
				return fmt.Errorf("duplicate JSON object key")
			}
			seen[folded] = struct{}{}
			if err := validateCorpusSnapshotJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := validateCorpusSnapshotJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
}

func corpusSnapshotExtension(service string) (string, error) {
	switch service {
	case CorpusSnapshotConfluence:
		return ".csf", nil
	case CorpusSnapshotJira:
		return ".wiki", nil
	default:
		return "", corpusSnapshotError("unsupported snapshot service")
	}
}

func corpusSnapshotMetadataSuffix(service string) string {
	if service == CorpusSnapshotConfluence {
		return ".meta.json"
	}
	return ".json"
}

func validCorpusSnapshotDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for i := range value {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}

func validCorpusSnapshotOrigin(value string) bool {
	return strings.HasPrefix(value, backendid.Prefix) && validCorpusSnapshotDigest(strings.TrimPrefix(value, backendid.Prefix))
}

func validCorpusProviderID(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '0' {
		return false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func foldCorpusSnapshotPath(value string) string {
	var folded strings.Builder
	folded.Grow(len(value))
	for _, current := range value {
		minimum := current
		for next := current; ; {
			next = unicode.SimpleFold(next)
			if next == current {
				break
			}
			if next < minimum {
				minimum = next
			}
		}
		folded.WriteRune(minimum)
	}
	return folded.String()
}

func corpusJSONObject(data []byte) bool {
	data = bytes.TrimSpace(data)
	return len(data) >= 2 && data[0] == '{' && data[len(data)-1] == '}'
}

func writeCorpusSnapshotHashPart(destination io.Writer, part []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(part)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write(part)
}

func corpusSnapshotError(message string) error {
	return fmt.Errorf("%w: corpus snapshot: %s", domain.ErrCheckFailed, message)
}
