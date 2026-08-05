//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agenteval

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func hardenedTryAdvisoryLock(f *os.File) (func() error, bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }, true, nil
}
