package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	nativeReconcileSchemaVersion    = 1
	nativeReconcileMaxBodyBytes     = 16 << 20
	nativeReconcileResponseBytes    = 64 << 20
	nativeReconcileMaxBlocks        = 4096
	nativeReconcileMaxPendingBytes  = 64 << 20
	nativeReconcileMaxPendingFields = 256
)

// NativeReconcileSide is content-free evidence for one member of a
// base/ours/theirs comparison. Native bytes never enter command output.
type NativeReconcileSide struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Valid  bool   `json:"valid"`
}

// NativeReconcileClassification is the exact whole-value three-way result.
// Converged distinguishes two identical concurrent changes from an unchanged
// value without inventing a fifth conflict state.
type NativeReconcileClassification struct {
	State         string `json:"state"`
	OursChanged   bool   `json:"ours_changed"`
	TheirsChanged bool   `json:"theirs_changed"`
	Converged     bool   `json:"converged,omitempty"`
	Conflict      bool   `json:"conflict"`
}

type NativeReconcileBounds struct {
	MaxBodyBytes          int `json:"max_body_bytes"`
	MaxBlocks             int `json:"max_blocks,omitempty"`
	MaxAlignmentCells     int `json:"max_alignment_cells,omitempty"`
	MaxPendingRecordBytes int `json:"max_pending_record_bytes,omitempty"`
	MaxPendingFields      int `json:"max_pending_fields,omitempty"`
}

type NativeReconcileArtifacts struct {
	BasePath   string `json:"base_path"`
	TheirsPath string `json:"theirs_path"`
	Cleanup    string `json:"cleanup"`
}

const nativeReconcileCleanup = "remove these two artifact files manually after external review; atl never deletes reconcile artifacts"

func nativeReconcileSide(body []byte, valid bool) NativeReconcileSide {
	return NativeReconcileSide{Bytes: len(body), SHA256: mirror.Hash(body), Valid: valid}
}

func classifyNativeReconcile(base, ours, theirs []byte) NativeReconcileClassification {
	baseHash, oursHash, theirsHash := mirror.Hash(base), mirror.Hash(ours), mirror.Hash(theirs)
	oursChanged, theirsChanged := oursHash != baseHash, theirsHash != baseHash
	result := NativeReconcileClassification{OursChanged: oursChanged, TheirsChanged: theirsChanged}
	switch {
	case oursHash == theirsHash:
		result.State = "unchanged"
		result.Converged = oursChanged
	case !oursChanged:
		result.State = "remote_only"
	case !theirsChanged:
		result.State = "local_only"
	default:
		result.State = "diverged"
		result.Conflict = true
	}
	return result
}

func checkNativeReconcileBodies(base, ours, theirs []byte) error {
	for _, item := range []struct {
		name string
		body []byte
	}{{"base", base}, {"ours", ours}, {"theirs", theirs}} {
		if len(item.body) > nativeReconcileMaxBodyBytes {
			return fmt.Errorf("%w: reconcile %s body exceeds the %d-byte safety bound", domain.ErrCheckFailed, item.name, nativeReconcileMaxBodyBytes)
		}
	}
	return nil
}

func nativeReconcileProposal(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
