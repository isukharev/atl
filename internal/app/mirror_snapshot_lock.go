package app

import (
	"errors"
	"fmt"
	"os"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// mirrorSnapshotLock coordinates a write-free inspection with the persistent
// lock used by mirror mutations. missing is accepted for initialized mirrors
// created before that lock existed, but finish must verify that no current
// writer created it while the inspection was in progress.
type mirrorSnapshotLock struct {
	root    string
	path    string
	lock    *safepath.FileLock
	missing bool
}

func beginMirrorSnapshotLock(root, path string) (*mirrorSnapshotLock, error) {
	lock, acquired, err := safepath.TrySharedLockExistingFileWithin(root, path)
	if os.IsNotExist(err) {
		return &mirrorSnapshotLock{root: root, path: path, missing: true}, nil
	}
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("%w: a mirror mutation is active", domain.ErrCheckFailed)
	}
	return &mirrorSnapshotLock{root: root, path: path, lock: lock}, nil
}

// finish releases a held shared lock. For a legacy mirror whose lock was
// initially absent, retry reports whether a cooperating writer created the
// persistent lock during inspection. Since writers never remove these files,
// an absent-before/absent-after inspection cannot overlap a current mutation.
func (g *mirrorSnapshotLock) finish() (retry bool, err error) {
	if g == nil {
		return false, nil
	}
	if g.lock != nil {
		return false, g.lock.Unlock()
	}
	if !g.missing {
		return false, nil
	}
	_, err = safepath.StatWithin(g.root, g.path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}
