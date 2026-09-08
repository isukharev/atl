//go:build linux || darwin

package brokerjournal

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type localDisk struct {
	parent, root, lock         *os.File
	parentPath, rootPath, base string
	parentInfo, rootInfo       os.FileInfo
	hook                       func(string) error // package-private durability fault injection
}

func openDisk(path string, create bool) (disk, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errInvalid
	}
	d := &localDisk{parentPath: filepath.Dir(path), rootPath: path, base: filepath.Base(path)}
	var err error
	d.parent, err = openDirectory(d.parentPath)
	if err != nil {
		return nil, errUnavailable
	}
	ok := false
	defer func() {
		if !ok {
			_ = d.close()
		}
	}()
	d.parentInfo, err = d.parent.Stat()
	if err != nil || !privateDirectory(d.parentInfo) || !ownedFD(d.parent, true) {
		return nil, errUnavailable
	}
	if create {
		if unix.Mkdirat(int(d.parent.Fd()), d.base, 0o700) != nil {
			return nil, errUnavailable
		}
	}
	fd, err := unix.Openat(int(d.parent.Fd()), d.base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	d.root = os.NewFile(uintptr(fd), "broker-journal-directory")
	d.rootInfo, err = d.root.Stat()
	if err != nil || !privateDirectory(d.rootInfo) || d.checkPaths() != nil {
		return nil, errUnavailable
	}
	if platformQualifyFilesystem(fd) != nil {
		return nil, errUnavailable
	}
	flags := unix.O_RDWR
	if create {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	d.lock, err = d.open("identity", flags)
	if err != nil {
		return nil, errUnavailable
	}
	lockInfo, err := d.lock.Stat()
	if err != nil || !privateFile(lockInfo) || unix.Flock(int(d.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, errUnavailable
	}
	if create {
		if platformAllocate(d.lock, slotBytes) != nil || platformSyncFile(d.lock) != nil ||
			platformSyncDirectory(d.root) != nil || platformSyncDirectory(d.parent) != nil {
			return nil, errUnavailable
		}
	}
	if d.check() != nil || platformSyncDirectory(d.root) != nil || platformSyncDirectory(d.parent) != nil {
		return nil, errUnavailable
	}
	ok = true
	return d, nil
}

func openDirectory(path string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, errUnavailable
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "broker-journal-parent"), nil
}

func privateDirectory(info os.FileInfo) bool {
	return info.IsDir() && info.Mode() == os.ModeDir|0o700
}

func privateFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode() == 0o600
}

func ownedFD(file *os.File, directory bool) bool {
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil {
		return false
	}
	return ownedStat(stat, os.Geteuid(), directory)
}

func ownedStat(stat unix.Stat_t, uid int, directory bool) bool {
	if int64(stat.Uid) != int64(uid) {
		return false
	}
	if directory {
		return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0o7777 == 0o700
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o7777 == 0o600 && stat.Nlink == 1
}

func (d *localDisk) checkPaths() error {
	if !ownedFD(d.parent, true) || !ownedFD(d.root, true) {
		return errUnavailable
	}
	// Reopen only for identity comparison, never for the actual read/write/sync.
	parent, err := openDirectory(d.parentPath)
	if err != nil {
		return errUnavailable
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Stat()
	if err != nil || !os.SameFile(info, d.parentInfo) {
		return errUnavailable
	}
	var named unix.Stat_t
	if unix.Fstatat(int(d.parent.Fd()), d.base, &named, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return errUnavailable
	}
	var held unix.Stat_t
	if unix.Fstat(int(d.root.Fd()), &held) != nil || named.Dev != held.Dev || named.Ino != held.Ino || named.Mode != held.Mode {
		return errUnavailable
	}
	return nil
}

func (d *localDisk) check() error {
	if d.checkPaths() != nil || d.lock == nil || !ownedFD(d.lock, false) {
		return errUnavailable
	}
	info, err := d.lock.Stat()
	if err != nil || info.Size() != slotBytes || platformAllocated(d.lock, slotBytes) != nil {
		return errUnavailable
	}
	return d.sameNamed("identity", d.lock)
}

func (d *localDisk) sameNamed(name string, file *os.File) error {
	var named, held unix.Stat_t
	if unix.Fstatat(int(d.root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || unix.Fstat(int(file.Fd()), &held) != nil || named.Dev != held.Dev || named.Ino != held.Ino || named.Mode != held.Mode || named.Nlink != 1 || !ownedFD(file, false) {
		return errUnavailable
	}
	return nil
}

func (d *localDisk) open(name string, flags int) (*os.File, error) {
	if !validFilename(name) {
		return nil, errUnavailable
	}
	fd, err := unix.Openat(int(d.root.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, errUnavailable
	}
	f := os.NewFile(uintptr(fd), "broker-journal-file")
	if !ownedFD(f, false) || d.sameNamed(name, f) != nil {
		_ = f.Close()
		return nil, errUnavailable
	}
	return f, nil
}

func (d *localDisk) checkpoint(point string) error {
	if d.hook != nil && d.hook(point) != nil {
		return errUnavailable
	}
	return nil
}

func (d *localDisk) allocate(name string, size int64) error {
	if d.check() != nil || size < 1 || size > MaxArtifactBytes {
		return errUnavailable
	}
	f, err := d.open(name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return errUnavailable
	}
	defer func() { _ = f.Close() }()
	if d.checkpoint("allocate") != nil || platformAllocate(f, size) != nil ||
		d.checkpoint("file_sync") != nil || platformSyncFile(f) != nil || platformAllocated(f, size) != nil ||
		d.sameNamed(name, f) != nil || d.checkpoint("directory_sync") != nil ||
		platformSyncDirectory(d.root) != nil || d.check() != nil {
		return errUnavailable
	}
	return nil
}

func (d *localDisk) read(name string, size int64) ([]byte, error) {
	if d.check() != nil || size < 1 || size > MaxArtifactBytes {
		return nil, errUnavailable
	}
	f, err := d.open(name, unix.O_RDONLY)
	if err != nil {
		return nil, errUnavailable
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() != size || platformAllocated(f, size) != nil {
		return nil, errUnavailable
	}
	data := make([]byte, size)
	// Stabilize a valid surviving write whose previous fsync acknowledgement
	// was lost. Reopen must not report a terminal result from dirty cache alone.
	if _, err := io.ReadFull(f, data); err != nil || platformSyncFile(f) != nil ||
		d.sameNamed(name, f) != nil || d.check() != nil {
		return nil, errUnavailable
	}
	return data, nil
}

func (d *localDisk) write(name string, offset int64, data []byte) error {
	if d.check() != nil {
		return errUnavailable
	}
	f, err := d.open(name, unix.O_RDWR)
	if err != nil {
		return errUnavailable
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || offset < 0 || offset > info.Size() || int64(len(data)) > info.Size()-offset ||
		platformAllocated(f, info.Size()) != nil {
		return errUnavailable
	}
	if d.checkpoint("before_write") != nil {
		return errUnavailable
	}
	n, err := f.WriteAt(data, offset)
	if err != nil || n != len(data) || d.checkpoint("file_sync") != nil || platformSyncFile(f) != nil ||
		platformAllocated(f, info.Size()) != nil || d.checkpoint("after_file_sync") != nil ||
		d.sameNamed(name, f) != nil || d.check() != nil {
		return errUnavailable
	}
	return nil
}

func (d *localDisk) names(limit int) ([]string, error) {
	if d.check() != nil {
		return nil, errUnavailable
	}
	fd, err := unix.Openat(int(d.root.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	f := os.NewFile(uintptr(fd), "broker-journal-inventory")
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(names) > limit || d.check() != nil {
		return nil, errUnavailable
	}
	return names, nil
}

func (d *localDisk) close() error {
	failed := false
	for _, file := range []*os.File{d.lock, d.root, d.parent} {
		if file != nil && file.Close() != nil {
			failed = true
		}
	}
	if failed {
		return errUnavailable
	}
	return nil
}
