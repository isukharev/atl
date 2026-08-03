package mirror

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	completePullWriteTokenBytes = 16
	maxCompletePullTempName     = 96
)

func newCompletePullWriteToken() (string, error) {
	var raw [completePullWriteTokenBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validCompletePullWriteToken(token string) bool {
	if len(token) != completePullWriteTokenBytes*2 {
		return false
	}
	raw, err := hex.DecodeString(token)
	return err == nil && hex.EncodeToString(raw) == token
}

func completePullArtifactTemp(token string, index int) string {
	return fmt.Sprintf(".atl-cp-%s-artifact-%04d.tmp", token, index)
}

func completePullJournalTemp(token string) string {
	return ".atl-cp-" + token + "-journal.tmp"
}

func completePullSidecarTemp(token string) string {
	return ".atl-cp-" + token + "-sidecar.tmp"
}

func completePullProgressTemp(token string) string {
	return ".atl-cp-" + token + "-progress.tmp"
}

func validCompletePullTempName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= maxCompletePullTempName && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\:`) && !strings.ContainsRune(name, 0)
}

// removeCompletePullOwnedResidue removes only the exact sibling temp declared
// by a surviving publication intent or journal. Unexpected type, mode, or size
// is preserved as evidence and fails closed.
func (m *Mirror) removeCompletePullOwnedResidue(target, tempBase string, maxSize int64, finalMode os.FileMode) error {
	if !validCompletePullTempName(tempBase) || maxSize < 0 {
		return fmt.Errorf("%w: invalid complete-pull temporary-file ownership", domain.ErrCheckFailed)
	}
	temp := filepath.Join(filepath.Dir(target), tempBase)
	info, err := safepath.StatWithin(m.Root, temp)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect owned complete-pull residue %s: %v", domain.ErrCheckFailed, temp, err)
	}
	mode := info.Mode()
	if !mode.IsRegular() || (mode.Perm() != 0o600 && mode.Perm() != finalMode.Perm()) || info.Size() < 0 || info.Size() > maxSize {
		return fmt.Errorf("%w: owned complete-pull residue %s has unsafe metadata; preserving it", domain.ErrCheckFailed, temp)
	}
	if err := safepath.RemoveWithin(m.Root, temp); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncPublicationPath(m.Root, temp, defaultCompletePullPublicationOps())
}

func (m *Mirror) writeCompletePullOwned(target, tempBase string, data []byte, mode os.FileMode) error {
	if err := m.removeCompletePullOwnedResidue(target, tempBase, int64(len(data)), mode); err != nil {
		return err
	}
	return safepath.WriteFileOwnedAtomicWithin(m.Root, target, tempBase, data, mode)
}
