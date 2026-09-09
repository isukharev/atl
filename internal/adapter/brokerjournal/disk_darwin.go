//go:build darwin

package brokerjournal

import (
	"os"

	"golang.org/x/sys/unix"
)

// F_ALLOCATEPERSIST is Darwin's public 0x8 F_PREALLOCATE flag. The pinned
// x/sys release exposes Fstore_t and the other allocation constants but omits
// this one, so keep the ABI value isolated to the Darwin implementation.
// See https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/fcntl.h.
const darwinAllocatePersist uint32 = 0x8

func platformQualifyFilesystem(fd int) error {
	var fs unix.Statfs_t
	if unix.Fstatfs(fd, &fs) != nil || !qualifiedDarwinFilesystem(fs) {
		return errUnavailable
	}
	return nil
}

func qualifiedDarwinFilesystem(fs unix.Statfs_t) bool {
	const rejected = unix.MNT_RDONLY | unix.MNT_UNKNOWNPERMISSIONS
	return fs.Flags&unix.MNT_LOCAL != 0 && fs.Flags&rejected == 0 && darwinFilesystemName(fs.Fstypename) == "apfs"
}

func darwinFilesystemName(name [16]byte) string {
	value := make([]byte, 0, len(name))
	for _, c := range name {
		if c == 0 {
			break
		}
		value = append(value, c)
	}
	return string(value)
}

func platformAllocate(file *os.File, size int64) error {
	if size < 1 || size > MaxArtifactBytes {
		return errUnavailable
	}
	store := unix.Fstore_t{
		Flags:   unix.F_ALLOCATEALL | darwinAllocatePersist,
		Posmode: unix.F_PEOFPOSMODE,
		Length:  size,
	}
	if unix.FcntlFstore(file.Fd(), unix.F_PREALLOCATE, &store) != nil || store.Bytesalloc < size {
		return errUnavailable
	}
	// F_PREALLOCATE reserves physical space without changing the logical EOF.
	// Truncation is only the second step, after all requested space is proven.
	if unix.Ftruncate(int(file.Fd()), size) != nil {
		return errUnavailable
	}
	return platformAllocated(file, size)
}

func platformAllocated(file *os.File, size int64) error {
	if size < 1 || size > MaxArtifactBytes {
		return errUnavailable
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || stat.Size != size || stat.Blocks < (size+511)/512 {
		return errUnavailable
	}
	return nil
}

func platformSyncFile(file *os.File) error {
	if _, err := unix.FcntlInt(file.Fd(), unix.F_FULLFSYNC, 0); err != nil {
		return errUnavailable
	}
	return nil
}

func platformSyncDirectory(file *os.File) error {
	if unix.Fsync(int(file.Fd())) != nil {
		return errUnavailable
	}
	return nil
}
