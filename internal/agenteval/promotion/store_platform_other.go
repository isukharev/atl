//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package promotion

import "os"

const StoreSupported = true

func validateStoreRootPlatform(_ string, info os.FileInfo) error {
	return validateStoreDirectoryPlatform(info)
}

func validateStoreDirectoryPlatform(info os.FileInfo) error {
	if info.Mode().Perm() != 0o700 {
		return fail(ErrorInvalidIdentity)
	}
	return nil
}

func validateStoreRegularFilePlatform(info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return fail(ErrorInvalidIdentity)
	}
	return nil
}
