package corpus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	buildAttemptsDir = "attempts"
	buildActiveFile  = "active.v1.json"
	buildActiveTemp  = ".active.next"
	buildLockFile    = ".build.lock"
	buildReceiptsDir = "receipts"
)

// BuildWorkspace owns the crash-safe attempt namespace beside an ordinary
// corpus Store. The caller-selected 0700 root is pinned for the lifetime of the
// workspace and one advisory build lock serializes all active-record changes.
type BuildWorkspace struct {
	store  *Store
	unlock func() error
	closed bool
}

// InitializeBuildWorkspace initializes an existing empty 0700 trust root. A
// partial bootstrap is deliberately not repaired by OpenBuildWorkspace.
func InitializeBuildWorkspace(ctx context.Context, rootPath string, opts Options) (*BuildWorkspace, error) {
	store, err := Initialize(rootPath, opts)
	if err != nil {
		return nil, err
	}
	if err := initializeBuildNamespace(store); err != nil {
		_ = store.Close()
		return nil, err
	}
	return lockBuildWorkspace(ctx, store)
}

// OpenBuildWorkspace opens only a fully initialized Store plus build namespace.
func OpenBuildWorkspace(ctx context.Context, rootPath string, opts Options) (*BuildWorkspace, error) {
	store, err := Open(rootPath, opts)
	if err != nil {
		return nil, err
	}
	if err := verifyDirectory(store.root, buildAttemptsDir); err != nil {
		_ = store.Close()
		return nil, reject(ReasonMembership)
	}
	if err := verifyRegularFile(store.root, buildLockFile, privateFileMode); err != nil {
		_ = store.Close()
		return nil, reject(ReasonMembership)
	}
	return lockBuildWorkspace(ctx, store)
}

func initializeBuildNamespace(store *Store) error {
	if err := store.root.Mkdir(buildAttemptsDir, privateDirMode); err != nil {
		return reject(ReasonIO)
	}
	if err := verifyDirectory(store.root, buildAttemptsDir); err != nil {
		return err
	}
	lock, err := store.root.OpenFile(buildLockFile, os.O_RDWR|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return reject(ReasonIO)
	}
	if err := lock.Chmod(privateFileMode); err != nil {
		_ = lock.Close()
		return reject(ReasonIO)
	}
	if err := lock.Sync(); err != nil {
		_ = lock.Close()
		return reject(ReasonIO)
	}
	if err := lock.Close(); err != nil {
		return reject(ReasonIO)
	}
	if err := verifyRegularFile(store.root, buildLockFile, privateFileMode); err != nil {
		return err
	}
	for _, directory := range []string{buildAttemptsDir, "."} {
		if err := syncDirectory(store.root, directory); err != nil {
			return reject(ReasonIO)
		}
	}
	return nil
}

func lockBuildWorkspace(ctx context.Context, store *Store) (*BuildWorkspace, error) {
	if ctx == nil {
		_ = store.Close()
		return nil, reject(ReasonType)
	}
	unlock, err := lockBuildFile(ctx, store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	workspace := &BuildWorkspace{store: store, unlock: unlock}
	if err := workspace.recoverActiveTemp(); err != nil {
		_ = workspace.Close()
		return nil, err
	}
	if _, _, err := workspace.LoadActive(); err != nil {
		_ = workspace.Close()
		return nil, err
	}
	return workspace, nil
}

func lockBuildFile(ctx context.Context, store *Store) (func() error, error) {
	info, err := store.root.Lstat(buildLockFile)
	if err != nil || !exactRegularMode(info.Mode(), privateFileMode) {
		return nil, reject(ReasonMode)
	}
	file, err := store.root.OpenFile(buildLockFile, os.O_RDWR, 0)
	if err != nil {
		return nil, reject(ReasonIO)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !exactRegularMode(opened.Mode(), privateFileMode) {
		_ = file.Close()
		return nil, reject(ReasonConcurrent)
	}
	links, err := regularFileLinkCount(file)
	if err != nil || links != 1 {
		_ = file.Close()
		return nil, reject(ReasonType)
	}
	for {
		unlock, acquired, err := tryExclusiveLock(file)
		if err != nil {
			_ = file.Close()
			return nil, reject(ReasonIO)
		}
		if acquired {
			return func() error {
				unlockErr := unlock()
				closeErr := file.Close()
				if unlockErr != nil || closeErr != nil {
					return reject(ReasonIO)
				}
				return nil
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, contextError(ctx)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Close releases the build lock and pinned Store root.
func (workspace *BuildWorkspace) Close() error {
	if workspace == nil || workspace.closed {
		return nil
	}
	workspace.closed = true
	var result error
	if workspace.unlock != nil {
		result = errors.Join(result, workspace.unlock())
		workspace.unlock = nil
	}
	if workspace.store != nil {
		result = errors.Join(result, workspace.store.Close())
		workspace.store = nil
	}
	return result
}

func (workspace *BuildWorkspace) ensureOpen() error {
	if workspace == nil || workspace.closed || workspace.store == nil || workspace.unlock == nil {
		return reject(ReasonIO)
	}
	return workspace.store.ensureRoot()
}

// BeginAttempt creates one retained random attempt and selected service roots.
func (workspace *BuildWorkspace) BeginAttempt(services []Service) (string, map[Service]string, error) {
	if err := workspace.ensureOpen(); err != nil {
		return "", nil, err
	}
	services = append([]Service(nil), services...)
	if !validBuildServices(services) {
		return "", nil, reject(ReasonMembership)
	}
	for candidate := 0; candidate < 8; candidate++ {
		id, err := randomToken(16)
		if err != nil {
			return "", nil, reject(ReasonIO)
		}
		attempt := buildAttemptPath(id)
		if err := workspace.store.root.Mkdir(attempt, privateDirMode); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", nil, reject(ReasonIO)
		}
		paths := make(map[Service]string, len(services))
		children := []string{buildReceiptsDir}
		for _, service := range services {
			children = append(children, string(service))
		}
		for _, child := range children {
			rel := attempt + "/" + child
			if err := workspace.store.root.Mkdir(rel, privateDirMode); err != nil {
				return "", nil, reject(ReasonIO)
			}
			if err := verifyDirectory(workspace.store.root, rel); err != nil {
				return "", nil, err
			}
			if child != buildReceiptsDir {
				paths[Service(child)] = filepath.Join(workspace.store.rootPath, filepath.FromSlash(rel))
			}
		}
		for index := len(children) - 1; index >= 0; index-- {
			if err := syncDirectory(workspace.store.root, attempt+"/"+children[index]); err != nil {
				return "", nil, reject(ReasonIO)
			}
		}
		for _, directory := range []string{attempt, buildAttemptsDir, "."} {
			if err := syncDirectory(workspace.store.root, directory); err != nil {
				return "", nil, reject(ReasonIO)
			}
		}
		return id, paths, nil
	}
	return "", nil, ErrAlreadyExists
}

// AttemptRoot returns an already-created selected service root after exact
// directory verification. It never creates or adopts a path.
func (workspace *BuildWorkspace) AttemptRoot(attemptID string, service Service) (string, error) {
	if err := workspace.ensureOpen(); err != nil {
		return "", err
	}
	if err := validGenerationID(attemptID); err != nil || !validQualificationService(service) {
		return "", reject(ReasonPath)
	}
	rel := buildAttemptPath(attemptID) + "/" + string(service)
	if err := verifyDirectory(workspace.store.root, rel); err != nil {
		return "", err
	}
	return filepath.Join(workspace.store.rootPath, filepath.FromSlash(rel)), nil
}

// SaveActive atomically replaces the exact active record. Failures after the
// rename return ErrOutcomeUnknown; LoadActive resolves the actual state.
func (workspace *BuildWorkspace) SaveActive(active BuildActive) error {
	if err := workspace.ensureOpen(); err != nil {
		return err
	}
	data, err := CanonicalBuildActive(active, workspace.store.limits)
	if err != nil {
		return err
	}
	if err := workspace.validateActiveAttempt(active); err != nil {
		return err
	}
	if err := workspace.recoverActiveTemp(); err != nil {
		return err
	}
	file, err := workspace.store.root.OpenFile(buildActiveTemp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return reject(ReasonIO)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return reject(ReasonIO)
	}
	if err := file.Chmod(privateFileMode); err != nil {
		_ = file.Close()
		return reject(ReasonIO)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return reject(ReasonIO)
	}
	if err := file.Close(); err != nil {
		return reject(ReasonIO)
	}
	if err := workspace.store.hit("after_build_active_temp_sync"); err != nil {
		return reject(ReasonIO)
	}
	if err := workspace.store.root.Rename(buildActiveTemp, buildActiveFile); err != nil {
		return reject(ReasonIO)
	}
	if err := workspace.store.hit("after_build_active_rename"); err != nil {
		return ErrOutcomeUnknown
	}
	if err := syncDirectory(workspace.store.root, "."); err != nil {
		return ErrOutcomeUnknown
	}
	if err := workspace.store.hit("after_build_active_sync"); err != nil {
		return ErrOutcomeUnknown
	}
	return nil
}

// LoadActive returns the current exact record or found=false for a fresh root.
func (workspace *BuildWorkspace) LoadActive() (BuildActive, bool, error) {
	if err := workspace.ensureOpen(); err != nil {
		return BuildActive{}, false, err
	}
	data, err := readRegularBytes(workspace.store.root, buildActiveFile, maxCaptureReceiptBytes)
	if os.IsNotExist(err) {
		return BuildActive{}, false, nil
	}
	if err != nil {
		return BuildActive{}, false, err
	}
	active, err := ParseBuildActive(data, workspace.store.limits)
	if err != nil {
		return BuildActive{}, false, err
	}
	if err := workspace.validateActiveAttempt(active); err != nil {
		return BuildActive{}, false, err
	}
	return active, true, nil

}

// SaveCaptureReceipt persists one canonical receipt exclusively. Repeating the
// exact write is idempotent; different existing bytes fail closed.
func (workspace *BuildWorkspace) SaveCaptureReceipt(attemptID string, receipt CaptureReceipt) error {
	if err := workspace.ensureOpen(); err != nil {
		return err
	}
	if _, err := workspace.AttemptRoot(attemptID, receipt.Service); err != nil {
		return err
	}
	data, err := CanonicalCaptureReceipt(receipt, workspace.store.limits)
	if err != nil {
		return err
	}
	rel := buildReceiptPath(attemptID, receipt.Service)
	if existing, err := readRegularBytes(workspace.store.root, rel, maxCaptureReceiptBytes); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return reject(ReasonConcurrent)
	} else if !os.IsNotExist(err) {
		return err
	}
	linked, err := writeExclusiveRegular(workspace.store.root, rel, data)
	if err != nil {
		if linked {
			return ErrOutcomeUnknown
		}
		return err
	}
	if !linked {
		return reject(ReasonIO)
	}
	for _, directory := range []string{buildAttemptPath(attemptID) + "/" + buildReceiptsDir, buildAttemptPath(attemptID), buildAttemptsDir, "."} {
		if err := syncDirectory(workspace.store.root, directory); err != nil {
			return ErrOutcomeUnknown
		}
	}
	return nil
}

// LoadCaptureReceipt reads one already-accepted receipt without adopting it.
func (workspace *BuildWorkspace) LoadCaptureReceipt(attemptID string, service Service) (CaptureReceipt, bool, error) {
	if err := workspace.ensureOpen(); err != nil {
		return CaptureReceipt{}, false, err
	}
	if err := validGenerationID(attemptID); err != nil || !validQualificationService(service) {
		return CaptureReceipt{}, false, reject(ReasonPath)
	}
	if _, err := workspace.AttemptRoot(attemptID, service); err != nil {
		return CaptureReceipt{}, false, err
	}
	data, err := readRegularBytes(workspace.store.root, buildReceiptPath(attemptID, service), maxCaptureReceiptBytes)
	if os.IsNotExist(err) {
		return CaptureReceipt{}, false, nil
	}
	if err != nil {
		return CaptureReceipt{}, false, err
	}
	receipt, err := ParseCaptureReceipt(data, workspace.store.limits)
	return receipt, err == nil, err
}

func (workspace *BuildWorkspace) recoverActiveTemp() error {
	if err := workspace.ensureOpen(); err != nil {
		return err
	}
	info, err := workspace.store.root.Lstat(buildActiveTemp)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !exactRegularMode(info.Mode(), privateFileMode) {
		return reject(ReasonMode)
	}
	file, err := openReadOnlyNonblocking(workspace.store.root, buildActiveTemp)
	if err != nil {
		return reject(ReasonIO)
	}
	opened, err := file.Stat()
	links, linkErr := regularFileLinkCount(file)
	closeErr := file.Close()
	if err != nil || linkErr != nil || closeErr != nil || links != 1 || !os.SameFile(info, opened) || !exactRegularMode(opened.Mode(), privateFileMode) {
		return reject(ReasonConcurrent)
	}
	if err := workspace.store.root.Remove(buildActiveTemp); err != nil {
		return reject(ReasonIO)
	}
	if err := syncDirectory(workspace.store.root, "."); err != nil {
		return reject(ReasonIO)
	}
	return nil
}

func (workspace *BuildWorkspace) validateActiveAttempt(active BuildActive) error {
	attempt := buildAttemptPath(active.AttemptID)
	if err := verifyDirectory(workspace.store.root, attempt); err != nil {
		return reject(ReasonMembership)
	}
	if err := verifyDirectory(workspace.store.root, attempt+"/"+buildReceiptsDir); err != nil {
		return reject(ReasonMembership)
	}
	for _, state := range active.Services {
		if err := verifyDirectory(workspace.store.root, attempt+"/"+string(state.Service)); err != nil {
			return reject(ReasonMembership)
		}
		if state.ReceiptDigest == "" {
			continue
		}
		receipt, found, err := workspace.LoadCaptureReceipt(active.AttemptID, state.Service)
		if err != nil || !found || receipt.ReceiptDigest != state.ReceiptDigest || receipt.ScopeDigest != state.ScopeDigest || receipt.SelectorDigest != state.SelectorDigest {
			return reject(ReasonLineage)
		}
	}
	return nil
}

func validBuildServices(services []Service) bool {
	if len(services) == 0 || len(services) > 2 || !sort.SliceIsSorted(services, func(i, j int) bool { return services[i] < services[j] }) {
		return false
	}
	for index, service := range services {
		if !validQualificationService(service) || index > 0 && services[index-1] == service {
			return false
		}
	}
	return true
}

func buildAttemptPath(attemptID string) string { return buildAttemptsDir + "/" + attemptID }

func buildReceiptPath(attemptID string, service Service) string {
	return strings.Join([]string{buildAttemptPath(attemptID), buildReceiptsDir, string(service) + ".capture.v1.json"}, "/")
}
