package mirror

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	completePullCheckpointSchema   = 1
	maxCompletePullCheckpointBytes = 64 << 20
	maxCompletePullProgressBytes   = 4 << 10
	maxCompletePullCheckpointIDs   = 1_000_000
	maxCompletePullIDBytes         = 256
)

// CompletePullCheckpoint is a private, backend-neutral snapshot of one exact
// selector plus the prefix whose mirror commits are known durable. IDs contain
// identities only: credentials, backend URLs, page bodies, and titles never
// enter this resume artifact.
type CompletePullCheckpoint struct {
	SchemaVersion   int      `json:"schema_version"`
	Service         string   `json:"service"`
	SelectorSHA256  string   `json:"selector_sha256"`
	OptionsSHA256   string   `json:"options_sha256"`
	SelectionSHA256 string   `json:"selection_sha256"`
	IDs             []string `json:"ids"`
	NextIndex       int      `json:"next_index"`
}

type completePullProgress struct {
	SchemaVersion   int    `json:"schema_version"`
	SelectorSHA256  string `json:"selector_sha256"`
	OptionsSHA256   string `json:"options_sha256"`
	SelectionSHA256 string `json:"selection_sha256"`
	NextIndex       int    `json:"next_index"`
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (m *Mirror) completePullCheckpointPath(selectorSHA256 string) (string, error) {
	if !validSHA256(selectorSHA256) {
		return "", fmt.Errorf("%w: complete-pull selector hash is not canonical SHA-256", domain.ErrCheckFailed)
	}
	return filepath.Join(m.Root, ".atl", "complete-pulls", selectorSHA256+".json"), nil
}

func (m *Mirror) completePullProgressPath(selectorSHA256 string) (string, error) {
	checkpoint, err := m.completePullCheckpointPath(selectorSHA256)
	if err != nil {
		return "", err
	}
	return checkpoint[:len(checkpoint)-len(".json")] + ".progress.json", nil
}

func validateCompletePullCheckpoint(value CompletePullCheckpoint, expectedSelectorSHA256 string) error {
	if value.SchemaVersion != completePullCheckpointSchema {
		return fmt.Errorf("%w: unsupported complete-pull checkpoint schema %d", domain.ErrCheckFailed, value.SchemaVersion)
	}
	if value.Service == "" || value.SelectorSHA256 != expectedSelectorSHA256 || !validSHA256(value.OptionsSHA256) || !validSHA256(value.SelectionSHA256) {
		return fmt.Errorf("%w: complete-pull checkpoint identity is invalid", domain.ErrCheckFailed)
	}
	if len(value.IDs) > maxCompletePullCheckpointIDs {
		return fmt.Errorf("%w: complete-pull checkpoint exceeds %d identities", domain.ErrCheckFailed, maxCompletePullCheckpointIDs)
	}
	if value.NextIndex < 0 || value.NextIndex > len(value.IDs) {
		return fmt.Errorf("%w: complete-pull checkpoint progress is outside its selection", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(value.IDs))
	for _, id := range value.IDs {
		if id == "" {
			return fmt.Errorf("%w: complete-pull checkpoint contains an empty identity", domain.ErrCheckFailed)
		}
		if len(id) > maxCompletePullIDBytes {
			return fmt.Errorf("%w: complete-pull checkpoint identity exceeds %d bytes", domain.ErrCheckFailed, maxCompletePullIDBytes)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w: complete-pull checkpoint contains duplicate identity %q", domain.ErrCheckFailed, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func decodeCompletePullJSON(path string, b []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: corrupt complete-pull state %s: %v", domain.ErrCheckFailed, path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%w: corrupt complete-pull state %s: trailing JSON value", domain.ErrCheckFailed, path)
	}
	return nil
}

func readCompletePullFile(root, path string, maxBytes int) ([]byte, bool, error) {
	info, err := safepath.StatWithin(root, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Size() > int64(maxBytes) {
		return nil, false, fmt.Errorf("%w: complete-pull state %s exceeds %d bytes", domain.ErrCheckFailed, path, maxBytes)
	}
	b, err := safepath.ReadFileWithin(root, path)
	if err != nil {
		return nil, false, err
	}
	if len(b) > maxBytes {
		return nil, false, fmt.Errorf("%w: complete-pull state %s changed beyond %d bytes while being read", domain.ErrCheckFailed, path, maxBytes)
	}
	return b, true, nil
}

func (m *Mirror) loadCompletePullSelection(selectorSHA256 string) (CompletePullCheckpoint, bool, error) {
	path, err := m.completePullCheckpointPath(selectorSHA256)
	if err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	b, found, err := readCompletePullFile(m.Root, path, maxCompletePullCheckpointBytes)
	if err != nil || !found {
		return CompletePullCheckpoint{}, found, err
	}
	var value CompletePullCheckpoint
	if err := decodeCompletePullJSON(path, b, &value); err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	if err := validateCompletePullCheckpoint(value, selectorSHA256); err != nil {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w in %s", err, path)
	}
	if value.NextIndex != 0 {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: complete-pull selection manifest %s contains mutable progress", domain.ErrCheckFailed, path)
	}
	return value, true, nil
}

// CompletePullCheckpoint loads the active snapshot for one selector. Missing
// state means no resumable run exists; malformed state fails closed.
func (m *Mirror) CompletePullCheckpoint(selectorSHA256 string) (CompletePullCheckpoint, bool, error) {
	value, found, err := m.loadCompletePullSelection(selectorSHA256)
	if err != nil || !found {
		return CompletePullCheckpoint{}, found, err
	}
	progressPath, err := m.completePullProgressPath(selectorSHA256)
	if err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	b, progressFound, err := readCompletePullFile(m.Root, progressPath, maxCompletePullProgressBytes)
	if err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	if !progressFound {
		return value, true, nil
	}
	var progress completePullProgress
	if err := decodeCompletePullJSON(progressPath, b, &progress); err != nil {
		return CompletePullCheckpoint{}, false, err
	}
	if progress.SchemaVersion != completePullCheckpointSchema {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: unsupported complete-pull progress schema %d in %s", domain.ErrCheckFailed, progress.SchemaVersion, progressPath)
	}
	if progress.SelectorSHA256 != value.SelectorSHA256 || progress.OptionsSHA256 != value.OptionsSHA256 || progress.SelectionSHA256 != value.SelectionSHA256 {
		// A crash after atomically replacing a restarted selection can leave the
		// previous tiny progress sidecar. Replaying from zero is conservative;
		// trusting or rejecting that stale prefix would make recovery worse.
		return value, true, nil
	}
	if progress.NextIndex < 0 || progress.NextIndex > len(value.IDs) {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: complete-pull progress is outside its selection in %s", domain.ErrCheckFailed, progressPath)
	}
	value.NextIndex = progress.NextIndex
	return value, true, nil
}

// SaveCompletePullCheckpoint atomically replaces one selector checkpoint.
// The long Confluence mutation lock serializes callers; the selector-specific
// file keeps independent unfinished snapshots from overwriting one another.
func (m *Mirror) SaveCompletePullCheckpoint(value CompletePullCheckpoint) error {
	if value.SchemaVersion == 0 {
		value.SchemaVersion = completePullCheckpointSchema
	}
	if err := validateCompletePullCheckpoint(value, value.SelectorSHA256); err != nil {
		return err
	}
	path, err := m.completePullCheckpointPath(value.SelectorSHA256)
	if err != nil {
		return err
	}
	if err := safepath.MkdirAllWithin(m.Root, filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing, found, err := m.loadCompletePullSelection(value.SelectorSHA256)
	if err != nil {
		return err
	}
	selectionChanged := !found || existing.Service != value.Service || existing.OptionsSHA256 != value.OptionsSHA256 || existing.SelectionSHA256 != value.SelectionSHA256 || !reflect.DeepEqual(existing.IDs, value.IDs)
	if selectionChanged {
		manifest := value
		manifest.NextIndex = 0
		b, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		if len(b)+1 > maxCompletePullCheckpointBytes {
			return fmt.Errorf("%w: complete-pull checkpoint would exceed %d bytes; narrow the selector or set --max-pages", domain.ErrCheckFailed, maxCompletePullCheckpointBytes)
		}
		if err := safepath.WriteFileWithin(m.Root, path, append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	progress := completePullProgress{
		SchemaVersion: completePullCheckpointSchema, SelectorSHA256: value.SelectorSHA256,
		OptionsSHA256: value.OptionsSHA256, SelectionSHA256: value.SelectionSHA256, NextIndex: value.NextIndex,
	}
	b, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		return err
	}
	progressPath, err := m.completePullProgressPath(value.SelectorSHA256)
	if err != nil {
		return err
	}
	return safepath.WriteFileWithin(m.Root, progressPath, append(b, '\n'), 0o600)
}

// RemoveCompletePullCheckpoint retires a fully consumed selector snapshot.
func (m *Mirror) RemoveCompletePullCheckpoint(selectorSHA256 string) error {
	path, err := m.completePullCheckpointPath(selectorSHA256)
	if err != nil {
		return err
	}
	if err := safepath.RemoveWithin(m.Root, path); err != nil && !os.IsNotExist(err) {
		return err
	}
	progressPath, err := m.completePullProgressPath(selectorSHA256)
	if err != nil {
		return err
	}
	if err := safepath.RemoveWithin(m.Root, progressPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
