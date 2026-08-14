//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package promotion

import (
	"os"
	"syscall"
)

const StoreSupported = true

func validateStoreRootPlatform(_ string, info os.FileInfo) error {
	return validateStoreDirectoryPlatform(info)
}

func validateStoreDirectoryPlatform(info os.FileInfo) error {
	if info.Mode().Perm() != 0o700 {
		return fail(ErrorInvalidIdentity)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Uid) != uint64(os.Getuid()) { // #nosec G115 -- the platform UID is compared after a regular-file owner check.
		return fail(ErrorInvalidIdentity)
	}
	return nil
}

func validateStoreRegularFilePlatform(info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return fail(ErrorInvalidIdentity)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Uid) != uint64(os.Getuid()) { // #nosec G115 -- the platform UID is compared after a regular-file owner check.
		return fail(ErrorInvalidIdentity)
	}
	return nil
}
