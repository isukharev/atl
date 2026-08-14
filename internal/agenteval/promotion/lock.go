package promotion

import (
	"errors"
	"os"
)

const promotionLockName = ".promotion.lock"

type storeLock struct {
	file   *os.File
	unlock func() error
}

func acquireStoreLock(root *os.Root) (*storeLock, error) {
	if info, err := root.Lstat(promotionLockName); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, fail(ErrorConflict)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := root.OpenFile(promotionLockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, fail(ErrorConflict)
	}
	unlock, acquired, err := tryStoreAdvisoryLock(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !acquired {
		_ = file.Close()
		return nil, fail(ErrorConflict)
	}
	return &storeLock{file: file, unlock: unlock}, nil
}

func (lock *storeLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := lock.unlock()
	closeErr := lock.file.Close()
	return errors.Join(unlockErr, closeErr)
}
