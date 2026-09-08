//go:build darwin

package brokerjournal

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinFilesystemQualificationIsClosed(t *testing.T) {
	tests := []struct {
		name   string
		fs     unix.Statfs_t
		accept bool
	}{
		{"local APFS", darwinStatfs("apfs", unix.MNT_LOCAL), true},
		{"non-local APFS", darwinStatfs("apfs", 0), false},
		{"read-only APFS", darwinStatfs("apfs", unix.MNT_LOCAL|unix.MNT_RDONLY), false},
		{"ignored ownership APFS", darwinStatfs("apfs", unix.MNT_LOCAL|unix.MNT_UNKNOWNPERMISSIONS), false},
		{"other local filesystem", darwinStatfs("hfs", unix.MNT_LOCAL), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := qualifiedDarwinFilesystem(test.fs); got != test.accept {
				t.Fatalf("qualified=%t want=%t", got, test.accept)
			}
		})
	}
}

func TestDarwinAPFSAllocationAndSyncSurviveReopen(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if err := platformQualifyFilesystem(int(directory.Fd())); err != nil {
		t.Fatal("hosted Darwin fixture is not qualified local APFS")
	}

	const size = int64(1 << 20)
	path := filepath.Join(root, "allocated")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformAllocate(file, size); err != nil {
		_ = file.Close()
		t.Fatal("persistent physical allocation failed")
	}
	if err := platformSyncFile(file); err != nil {
		_ = file.Close()
		t.Fatal("full file synchronization failed")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	file, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := platformAllocated(file, size); err != nil {
		t.Fatal("physical allocation did not survive close and reopen")
	}
	if err := platformSyncFile(file); err != nil {
		t.Fatal("read-only full synchronization failed")
	}
	if err := platformSyncDirectory(directory); err != nil {
		t.Fatal("directory synchronization failed")
	}

	sparsePath := filepath.Join(root, "sparse")
	sparse, err := os.OpenFile(sparsePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sparse.Close() }()
	if err := sparse.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := platformAllocated(sparse, size); err == nil {
		t.Fatal("sparse truncate satisfied physical allocation check")
	}
	if err := platformAllocated(sparse, 0); err == nil {
		t.Fatal("zero-sized file satisfied physical allocation check")
	}
}

func darwinStatfs(name string, flags uint32) unix.Statfs_t {
	fs := unix.Statfs_t{Flags: flags}
	for i := range len(name) {
		fs.Fstypename[i] = name[i]
	}
	return fs
}
