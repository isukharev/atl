//go:build linux

package brokerjournal

import (
	"os"

	"golang.org/x/sys/unix"
)

func platformQualifyFilesystem(fd int) error {
	// Qualify only known local filesystem types. Overlay backing storage must
	// also be local; mount topology remains a trusted deployment prerequisite.
	var fs unix.Statfs_t
	if unix.Fstatfs(fd, &fs) != nil {
		return errUnavailable
	}
	switch fs.Type {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC, unix.OVERLAYFS_SUPER_MAGIC:
		return nil
	default:
		return errUnavailable
	}
}

func platformAllocate(file *os.File, size int64) error {
	if unix.Fallocate(int(file.Fd()), 0, 0, size) != nil {
		return errUnavailable
	}
	return nil
}

// Linux fallocate is the allocation proof. Keep the existing behavior rather
// than imposing a second filesystem-accounting policy on the qualified types.
func platformAllocated(_ *os.File, _ int64) error { return nil }

func platformSyncFile(file *os.File) error      { return file.Sync() }
func platformSyncDirectory(file *os.File) error { return file.Sync() }
