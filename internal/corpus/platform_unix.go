//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package corpus

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func platformAvailable() bool { return true }

func tryExclusiveLock(file *os.File) (func() error, bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }, true, nil
}

func regularFileLinkCount(file *os.File) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Nlink), nil
}
