//go:build windows

package promotion

import "os"

const StoreSupported = false

// The promotion store requires an owner-only directory and directory-entry
// fsyncs. Go's portable FileMode does not express Windows ACL ownership, and
// the current store has no native ACL admission/flush implementation. Refuse
// before any mutation rather than advertising a weaker persistence boundary.
func validateStoreRootPlatform(_ string, _ os.FileInfo) error {
	return fail(ErrorUnsupportedPlatform)
}

func validateStoreDirectoryPlatform(_ os.FileInfo) error { return fail(ErrorUnsupportedPlatform) }

func validateStoreRegularFilePlatform(_ os.FileInfo) error { return fail(ErrorUnsupportedPlatform) }
