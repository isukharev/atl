package agenteval

import (
	"path/filepath"
	"sync"
)

type attemptLedgerLocalLock struct {
	sync.Mutex
}

type attemptLedgerHeldLock struct {
	file  *hardenedFileLock
	local *attemptLedgerLocalLock
}

func (lock *attemptLedgerHeldLock) Unlock() error {
	if lock == nil || lock.local == nil {
		return nil
	}
	err := lock.file.Unlock()
	lock.local.Unlock()
	lock.local = nil
	return err
}

func (store *AttemptLedgerStore) lock() (*attemptLedgerHeldLock, error) {
	store.local.Lock()
	lock, acquired, err := hardenedTryLockFileWithin(store.root, filepath.Join(store.root, attemptLedgerLockName), 0o600)
	if err != nil {
		store.local.Unlock()
		return nil, attemptLedgerError("lock", err)
	}
	if !acquired {
		store.local.Unlock()
		return nil, ErrAttemptLedgerBusy
	}
	return &attemptLedgerHeldLock{file: lock, local: &store.local}, nil
}

func (store *AttemptLedgerStore) readOnlyLock() (*attemptLedgerHeldLock, error) {
	store.local.Lock()
	lock, acquired, err := hardenedTryReadOnlyLockFileWithin(store.root, filepath.Join(store.root, attemptLedgerLockName))
	if err != nil {
		store.local.Unlock()
		return nil, attemptLedgerError("read_lock", err)
	}
	if !acquired {
		store.local.Unlock()
		return nil, ErrAttemptLedgerBusy
	}
	return &attemptLedgerHeldLock{file: lock, local: &store.local}, nil
}
