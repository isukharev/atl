//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package corpus

import "os"

func platformAvailable() bool { return false }

func openReadOnlyNonblocking(_ *os.Root, _ string) (*os.File, error) {
	return nil, ErrUnsupported
}

func tryExclusiveLock(_ *os.File) (func() error, bool, error) {
	return nil, false, ErrUnsupported
}

func regularFileLinkCount(_ *os.File) (uint64, error) {
	return 0, ErrUnsupported
}
