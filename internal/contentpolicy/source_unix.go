//go:build !windows

package contentpolicy

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

func readPolicyFile(path string, max int64, expectedOwnerUID *uint32) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("policy source is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("policy source mode %04o is not owner-only", info.Mode().Perm())
	}
	if info.Mode().Perm()&0o400 == 0 {
		return nil, fmt.Errorf("policy source is not owner-readable")
	}
	if expectedOwnerUID != nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != *expectedOwnerUID {
			return nil, fmt.Errorf("policy source owner does not match the sealed owner")
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("policy exceeds %d-byte limit", max)
	}
	return data, nil
}
