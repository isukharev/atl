//go:build windows

package safepath

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryAdvisoryLock(f *os.File) (func() error, bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() error {
		return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
	}, true, nil
}
