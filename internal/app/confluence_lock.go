package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const confluenceMutationLockName = "confluence.mutation.lock"

// lockConfluenceMutations serializes every workflow that can replace a
// Confluence substrate/view/base/sidecar set. The file is deliberately
// persistent: all processes lock the same inode, while process exit still
// releases the advisory lock automatically.
func lockConfluenceMutations(root string, bootstrap bool) (*safepath.FileLock, error) {
	internalDir := filepath.Join(root, ".atl")
	if bootstrap {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		if err := safepath.MkdirAllWithin(root, internalDir, 0o755); err != nil {
			return nil, err
		}
	} else {
		info, err := os.Stat(internalDir)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not an initialized mirror (missing .atl)", domain.ErrNotFound, root)
		}
	}
	path := filepath.Join(internalDir, confluenceMutationLockName)
	lock, acquired, err := safepath.TryLockFileWithin(root, path, 0o600)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("%w: another Confluence mirror mutation is active for %s", domain.ErrCheckFailed, root)
	}
	return lock, nil
}

// AcquireConfluenceMutation serializes a CLI-local mutation with the same lock
// used by pull/render/apply/push. Mirror-local CLI edits use this narrow bridge
// because the external editor invocation itself remains a CLI concern. The
// returned release function must be deferred.
func AcquireConfluenceMutation(root string) (func() error, error) {
	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		return nil, err
	}
	return lock.Unlock, nil
}
